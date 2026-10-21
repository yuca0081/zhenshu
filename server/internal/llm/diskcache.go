// diskcache.go —— LLM 请求级磁盘缓存 + 同键在飞单飞（2026-10-20 成本诊断 P0-2/P0-5）。
//
// 诊断账本：prompt 占总 token 的 79–84%，同一段被启动 10 次里大部分不是审核打回，
// 而是取消/429/重启后「一模一样的请求重新全额付费」。止血方式是把成功响应按
// 请求体哈希落盘：重启重跑、自动重试、断点恢复时相同请求零成本复用——
// 「LLM 调用成功即落盘」的最小实现，幂等键就是请求体本身。
//
// 单飞（singleflight）：相同请求体并发在飞时只有一份出网，其余等结果——
// (project, task, input_hash) 去重收在 llm 层一处，上层调度不必各自防重。
//
// 边界：只缓存 HTTP 200 且 choices 非空的响应；429/5xx/网络错不缓存（重试语义
// 不变）。缓存默认开（LLM_DISK_CACHE=off 关），目录 LLM_CACHE_DIR（默认
// data/llm-cache，相对进程 CWD——服务从仓库根启动）。文件 14 天过期，写入时
// 惰性清扫，不设后台任务。
package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

const (
	cacheFileAge    = 14 * 24 * time.Hour // 过期即扫掉
	cacheMaxEntry   = 64 << 20            // 单条上限（多模态大载荷也在内；超限只不缓存不报错）
	cachePruneEvery = 20                  // 每 N 次写入扫一轮过期
)

type chatCache struct {
	once   sync.Once
	dir    string
	dirOK  bool
	writes atomic.Int64 // 多 goroutine 并发落盘同时自增，必须原子
}

var chatCache_ chatCache

func (c *chatCache) init() {
	c.once.Do(func() {
		dir := os.Getenv("LLM_CACHE_DIR")
		if dir == "" {
			dir = filepath.Join("data", "llm-cache")
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Printf("[llm] 磁盘缓存目录建不了（%s），缓存停用: %v", dir, err)
			return
		}
		c.dir, c.dirOK = dir, true
	})
}

// cacheEnabled 开关每次调用即时读 env（LLM_DISK_CACHE=off）——once 只钉目录，
// 开关必须动态生效（包内测试切换、运行中改配置都靠它）。
func cacheEnabled() bool { return os.Getenv("LLM_DISK_CACHE") != "off" }

func cacheKey(model string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(model))
	h.Write([]byte{0})
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// cacheLookup 命中返回原始响应字节。读损坏按未命中处理（下轮重写）。
func cacheLookup(model string, body []byte) ([]byte, bool) {
	if !cacheEnabled() {
		return nil, false
	}
	chatCache_.init()
	if !chatCache_.dirOK {
		return nil, false
	}
	b, err := os.ReadFile(filepath.Join(chatCache_.dir, cacheKey(model, body)+".json"))
	if err != nil || len(b) == 0 {
		return nil, false
	}
	var resp openai.ChatCompletionResponse
	if json.Unmarshal(b, &resp) != nil || len(resp.Choices) == 0 {
		return nil, false
	}
	return b, true
}

// cacheStore 成功响应落盘（temp+rename 原子）；偶尔顺路扫一轮过期文件。
func cacheStore(model string, body, raw []byte) {
	if !cacheEnabled() {
		return
	}
	chatCache_.init()
	if !chatCache_.dirOK || len(raw) == 0 || len(raw) > cacheMaxEntry {
		return
	}
	path := filepath.Join(chatCache_.dir, cacheKey(model, body)+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
	if chatCache_.writes.Add(1)%cachePruneEvery == 0 {
		pruneCache(chatCache_.dir)
	}
}

func pruneCache(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-cacheFileAge)
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		if fi, err := e.Info(); err == nil && fi.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// ── 同键在飞单飞 ─────────────────────────────────────────────────

type sfFlight struct {
	done chan struct{}
	raw  []byte
	err  error
}

var (
	sfMu  sync.Mutex
	sfMap = map[string]*sfFlight{}
)

// sfEnter 注册在飞；第二个同名调用者拿到 isOwner=false，等 owner 的结果。
func sfEnter(key string) (f *sfFlight, isOwner bool) {
	sfMu.Lock()
	defer sfMu.Unlock()
	if f, ok := sfMap[key]; ok {
		return f, false
	}
	f = &sfFlight{done: make(chan struct{})}
	sfMap[key] = f
	return f, true
}

// sfLeave owner 收尾：成功才留响应（失败不留——等待者原样拿到错误后各自重试，
// 重试时 map 已清空会重新成为 owner）。
func sfLeave(key string, f *sfFlight, raw []byte, err error) {
	sfMu.Lock()
	if sfMap[key] == f {
		delete(sfMap, key)
	}
	f.raw, f.err = raw, err
	sfMu.Unlock()
	close(f.done)
}

// sfWait 等待者收尾。
func sfWait(ctx contextual, f *sfFlight) (raw []byte, err error) {
	select {
	case <-f.done:
		return f.raw, f.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// contextual 只需要 Done()（cachedChat/sfWait 的取消感知面收窄，便于测试）。
type contextual interface {
	Done() <-chan struct{}
	Err() error
}

// cachedChat postChat 的缓存/单飞封皮：命中直接回；未命中单飞出网，成功落盘。
// 预算闸与记账也收在这里（2026-10-21 修复）：只对真正出网的一跳过闸/记账——
// 重放（磁盘命中/单飞等待者）零成本复用，Usage 清零返回，否则同一响应重放 N 次
// 账面涨 N 倍，重跑越密集 8M 预算闸越早打死健康项目；agent.done 落的全价账还会
// 被 ProjectSpend 播种再累计一遍。
func cachedChat(ctx context.Context, model string, body []byte, net func() ([]byte, error)) (openai.ChatCompletionResponse, error) {
	if raw, ok := cacheLookup(model, body); ok {
		return replayChat(raw)
	}
	f, isOwner := sfEnter(cacheKey(model, body))
	if !isOwner {
		raw, err := sfWait(ctx, f)
		if err != nil {
			return openai.ChatCompletionResponse{}, err
		}
		return replayChat(raw)
	}
	// 预算闸放在缓存未命中之后：超限只拒「要花钱的新请求」，免费的缓存命中放行
	// （P0-2 的设计目标恰是重启重跑零成本复用，不能被自己的预算闸拦在门外）
	if err := budgetCheck(ctx); err != nil {
		sfLeave(cacheKey(model, body), f, nil, err)
		return openai.ChatCompletionResponse{}, err
	}
	raw, err := net()
	if err == nil {
		// 200 即记账（端点已计费，含空 choices）；空 choices 不落盘——内容审查/
		// 长度截断属可重试失败，落盘会让重试永远命中同一条死响应
		if resp, perr := parseChatBody(raw); perr == nil {
			budgetAdd(ctx, resp.Usage)
			if len(resp.Choices) > 0 {
				cacheStore(model, body, raw)
			}
		}
	}
	sfLeave(cacheKey(model, body), f, raw, err)
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	return parseChatBody(raw)
}

// replayChat 零成本复用（磁盘命中/单飞等待者）：内容原样、Usage 清零——这一次
// 没花钱，账面不能再记全价。
func replayChat(raw []byte) (openai.ChatCompletionResponse, error) {
	resp, err := parseChatBody(raw)
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	resp.Usage = openai.Usage{}
	return resp, nil
}

func parseChatBody(raw []byte) (openai.ChatCompletionResponse, error) {
	var resp openai.ChatCompletionResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	return resp, nil
}
