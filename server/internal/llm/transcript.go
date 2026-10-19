package llm

// transcript.go —— 智能体任务对话留痕（「过程」页的数据底座）。
//
// 一个任务一个文件（llm/transcripts/<task>.jsonl），逐条追加：任务跑到一半打开
// 也能看到半程对话；每任务一文件还天然避开三池并发的写冲突。
// 写入时减脂：超长正文截断、图片载荷换占位——留痕是给人看的排查材料，
// 不是全量请求报文。留痕是旁路：任何写失败都不挡制作主链路。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

// 合法任务名（seg.audit.seg03.r1 / prep.script 这类点分层级）。API 侧与落盘侧
// 共用同一套校验——目录穿越防线不依赖调用方自觉。
var taskNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,79}$`)

// ValidTask 任务名是否合法。
func ValidTask(task string) bool { return taskNameRe.MatchString(task) }

// 单条留痕的截断上限（rune 数）：正文 20k 兜住 fetch_page/场景考卷这类大注入
// （12k 时场景提示的段信息被截在金样例之后，恰恰是排查时最想看的部分），
// 工具参数 4k——参数是模型手写的 JSON，超长必是跑偏。
const (
	maxContentRunes = 20000
	maxArgsRunes    = 4000
)

// ImgRef 留痕里引用的一张项目内图片：给「过程」页渲染缩略图、点击放大用。
// 图片载荷本身（data URL）不落盘——那是给模型的，落盘只记项目内路径。
type ImgRef struct {
	URL   string `json:"-"`               // 注入模型用的 data URL（留痕不落盘，走 Path 取件）
	Path  string `json:"path"`            // 项目内相对路径（正斜杠），走 /projects/:id/file/ 取件
	Label string `json:"label,omitempty"` // 这张图是什么（风格样张/真迹/素材/截帧）
}

// Transcript 单个智能体任务的对话留痕文件。并发安全（内部互斥；跨任务各写各文件）。
type Transcript struct {
	mu  sync.Mutex
	f   *os.File
	Rel string // 项目内相对路径（正斜杠），manifest 账行与前端靠它取件
}

// OpenTranscript 打开（创建/追加）任务留痕文件。dir = 项目内 llm/transcripts 目录。
// 打不开返回 nil 不挡主链路（留痕是旁路）。
func OpenTranscript(dir, task string) *Transcript {
	if !ValidTask(task) {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(dir, task+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil
	}
	return &Transcript{f: f, Rel: "llm/transcripts/" + task + ".jsonl"}
}

// Message 追加一条消息（减脂后）。
func (t *Transcript) Message(msg openai.ChatCompletionMessage) {
	if t == nil || t.f == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	line, _ := json.Marshal(transcriptMsg(msg))
	t.f.Write(append(line, '\n'))
}

// MessageImgs 追加一条带图片注入清单的消息：多模态调用把图片喂给模型的同时，
// 把项目内路径清单记进留痕——「过程」页据此渲染缩略图，点击放大看原文件。
func (t *Transcript) MessageImgs(msg openai.ChatCompletionMessage, imgs []ImgRef) {
	if t == nil || t.f == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := transcriptMsg(msg)
	if len(imgs) > 0 {
		refs := make([]ImgRef, 0, len(imgs))
		refs = append(refs, imgs...)
		out["images"] = refs
	}
	line, _ := json.Marshal(out)
	t.f.Write(append(line, '\n'))
}

// Note 追加一条旁注（模型调用失败、重试边界这类过程性说明）。
func (t *Transcript) Note(format string, a ...any) {
	if t == nil || t.f == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	line, _ := json.Marshal(map[string]any{
		"role": "note", "content": fmt.Sprintf(format, a...), "ts": time.Now().Format(time.RFC3339),
	})
	t.f.Write(append(line, '\n'))
}

// Close 落锁关文件（可安全对 nil / 重复调用）。
func (t *Transcript) Close() {
	if t == nil || t.f == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.f.Close()
	t.f = nil
}

// transcriptMsg 一条消息的留痕视图：正文/参数截断、图片换占位。
func transcriptMsg(msg openai.ChatCompletionMessage) map[string]any {
	out := map[string]any{"role": msg.Role, "ts": time.Now().Format(time.RFC3339)}
	if msg.Name != "" {
		out["name"] = msg.Name
	}
	if msg.ToolCallID != "" {
		out["tool_call_id"] = msg.ToolCallID
	}
	if msg.Content != "" {
		out["content"] = truncRunes(msg.Content, maxContentRunes)
	}
	if len(msg.ToolCalls) > 0 {
		calls := make([]map[string]any, 0, len(msg.ToolCalls))
		for _, tc := range msg.ToolCalls {
			calls = append(calls, map[string]any{
				"id":   tc.ID,
				"name": tc.Function.Name,
				"args": truncRunes(tc.Function.Arguments, maxArgsRunes),
			})
		}
		out["tool_calls"] = calls
	}
	if len(msg.MultiContent) > 0 {
		var texts []string
		imgs := 0
		for _, part := range msg.MultiContent {
			switch part.Type {
			case openai.ChatMessagePartTypeText:
				texts = append(texts, truncRunes(part.Text, maxContentRunes))
			case openai.ChatMessagePartTypeImageURL:
				imgs++
			}
		}
		if imgs > 0 {
			texts = append(texts, fmt.Sprintf("[图片×%d]", imgs))
		}
		out["content"] = strings.Join(texts, "\n")
	}
	return out
}

func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	// 保头保尾截中段：场景提示的打回意见单/输出契约在尾部，排查时恰恰最想看
	// （只保头会把它们截掉——2026-10-19 实测重写考卷 29k 字，意见单正好看不见）。
	keepTail := n / 4
	return string(r[:n-keepTail]) + fmt.Sprintf("…[中段截断，共 %d 字；尾部如下]…", len(r)) + string(r[len(r)-keepTail:])
}
