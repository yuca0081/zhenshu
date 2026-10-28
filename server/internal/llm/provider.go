// Package llm：LLM 作业的 API 实现（OpenAI 兼容协议：GLM 编码套餐 / DeepSeek / Qwen 通吃）。
// 产物一律过 schema 校验（contract 包），不信任模型输出。
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

// llmGate 账户级并发闸（2026-10-17 限流事故）：套餐对并发请求极敏感，实测 3 路
// 10k-token 大调用即撞 429，而调用方路径多（段写/段审/检查修复轮/对话）——任何
// 一处漏限流就是惊群。generate 与 Chat/ChatTemp（chatTemp）两条路都过这道闸
// （2026-10-19 高-8：Chat 路原先绕闸直发，是最后一个口子）；上层并发旋钮
// （PRODUCE_WRITE_CONCURRENCY 等）因此可以只管调度，不必各自保证限流。
var llmGate = func() chan struct{} {
	n := 2
	if v := os.Getenv("LLM_MAX_CONCURRENCY"); v != "" {
		if n2, err := strconv.Atoi(v); err == nil && n2 >= 1 && n2 <= 16 {
			n = n2
		}
	}
	return make(chan struct{}, n)
}()

type Role string

const (
	RoleDialogue Role = "dialogue" // 主管对话（便宜快）
	RolePlan     Role = "plan"     // 分镜规划（中档）
	RoleVisual   Role = "visual"   // 合成物生成（最强）
)

// Provider 一个 OpenAI 兼容供应商。默认 GLM 编码套餐；换 DeepSeek/按量计费平台改 BaseURL+Key+Model 即可。
type Provider struct {
	Name    string
	BaseURL string
	APIKey  string
	Model   string
	// EffortOverride LLM_EFFORT 覆盖档：把平台默认深思考档（EffortHigh）整体换成
	// low/max/disabled。2026-10-27 实测 GLM 编码端点 high 档思考失控（肥提示词
	// 240s 零字节、prep ② 15 分钟客户端超时两连），qwen 时代等于无深思考所以
	// 一直没暴露；判卷类显式降档调用不受此覆盖影响。
	EffortOverride Effort
}

// FromEnv 按环境变量构造供应商：LLM_API_KEY / LLM_BASE_URL / LLM_MODEL（旧名 DEEPSEEK_* 仍兼容），
// 单档模型可用 LLM_VISUAL_MODEL / LLM_PLAN_MODEL / LLM_DIALOGUE_MODEL 覆盖。
// 默认 GLM 编码套餐端点 + glm-5.3-flash。缺 key 返回错误（调用方转 ErrAwaitLLM 降级会话模式）。
func FromEnv(role Role) (*Provider, error) {
	get := func(newName, oldName string) string {
		if v := os.Getenv(newName); v != "" {
			return v
		}
		return os.Getenv(oldName)
	}
	switch role {
	case RolePlan, RoleDialogue, RoleVisual:
		key := get("LLM_API_KEY", "DEEPSEEK_API_KEY")
		if key == "" {
			return nil, errors.New("LLM_API_KEY 未配置（旧名 DEEPSEEK_API_KEY 亦未配置）")
		}
		base := get("LLM_BASE_URL", "DEEPSEEK_BASE_URL")
		if base == "" {
			base = "https://open.bigmodel.cn/api/coding/paas/v4"
		}
		model := get("LLM_MODEL", "DEEPSEEK_MODEL")
		if model == "" {
			model = "glm-5.3-flash"
		}
		// 按档分模型（bake-off 口子，2026-10-28 起用）：LLM_<ROLE>_MODEL 覆盖单档
		// （LLM_VISUAL_MODEL / LLM_PLAN_MODEL / LLM_DIALOGUE_MODEL），不设回落
		// LLM_MODEL。BaseURL/Key 三档共享——只分模型不分平台。
		if m := get("LLM_"+strings.ToUpper(string(role))+"_MODEL", ""); m != "" {
			model = m
		}
		eff := Effort(os.Getenv("LLM_EFFORT"))
		switch eff { // 非法值当没配（端点对非法档位也是静默按默认跑，同口径）
		case EffortDefault, EffortLow, EffortHigh, EffortDisabled:
		default:
			eff = EffortDefault
		}
		return &Provider{Name: "llm", BaseURL: base, APIKey: key, Model: model, EffortOverride: eff}, nil
	}
	return nil, fmt.Errorf("未知角色 %s", role)
}

// Effort 思考档位。2026-09-30 实测编码端点：reasoning_effort 可用（low/high/max），
// thinking.type=disabled 仍有效；端点不校验非法档位值（传错静默按默认跑），枚举在编译期挡手滑。
type Effort string

const (
	EffortDefault  Effort = ""         // 不传思考字段，跟随端点默认（GLM-5.x 开思考时默认 max）
	EffortLow      Effort = "low"      // 轻思考：机械作业（翻译/改写）够用，几乎零 reasoning token
	EffortHigh     Effort = "high"     // 深思考：判断类作业的质量/成本平衡点
	EffortDisabled Effort = "disabled" // thinking.type=disabled 彻底关闭，结构化作业专用
)

// GenerateJSON 一次 JSON 作业：system+user → 模型 → 剥掉 markdown 围栏 → json.Unmarshal 到 out。
// 返回本次 token 用量（成本记账）；重试时的用量取最后一次成功调用。
func (p *Provider) GenerateJSON(ctx context.Context, system, user string, out any) (openai.Usage, error) {
	return p.generate(ctx, system, user, nil, "", out, 0.7, EffortDefault)
}

// GenerateJSONNoThink 同 GenerateJSON，但彻底关闭思考（thinking.type=disabled）。
// 结构化作业（样张/画面 HTML）思考会烧上万 reasoning token、拖到分钟级，纯亏时长。
func (p *Provider) GenerateJSONNoThink(ctx context.Context, system, user string, out any) (openai.Usage, error) {
	return p.generate(ctx, system, user, nil, "", out, 0.7, EffortDisabled)
}

// GenerateJSONNoThinkTemp 同 GenerateJSONNoThink，显式温度（样张修复轮 0.5 这类
// 与缺省 0.7 不同的档）。
func (p *Provider) GenerateJSONNoThinkTemp(ctx context.Context, system, user string, temperature float32, out any) (openai.Usage, error) {
	return p.generate(ctx, system, user, nil, "", out, temperature, EffortDisabled)
}

// GenerateJSONEffort 同 GenerateJSON，思考档位由调用方定。
func (p *Provider) GenerateJSONEffort(ctx context.Context, system, user string, out any, effort Effort) (openai.Usage, error) {
	return p.generate(ctx, system, user, nil, "", out, 0.7, effort)
}

// GenerateJSONVisionNoThink 多模态 JSON 作业：user 文本 + 若干图片（data URL）→ 模型，关闭思考。
// 画面 spec 这类大 HTML 作业开思考会烧 1 万+ reasoning token、单段拖到 5–13 分钟；
// 关掉后同样的活分钟级完成。评审类低温（稳定判断），画面生成类高温（发散）。
func (p *Provider) GenerateJSONVisionNoThink(ctx context.Context, system, user string, imageURLs []string, temperature float32, out any) (openai.Usage, error) {
	return p.generate(ctx, system, user, imageURLs, "", out, temperature, EffortDisabled)
}

// GenerateJSONVisionEffort 同 GenerateJSONVisionNoThink，思考档位由调用方定。
func (p *Provider) GenerateJSONVisionEffort(ctx context.Context, system, user string, imageURLs []string, temperature float32, out any, effort Effort) (openai.Usage, error) {
	return p.generate(ctx, system, user, imageURLs, "", out, temperature, effort)
}

// GenerateJSONVisionVideoNoThink 视频直读版多模态 JSON 作业：若干图片（data URL）+
// 一段视频（公网 URL，模型侧自行拉取播放）→ 模型。视频块承载静止帧给不了的时间维度
// 信息（动效/运镜/转场/节奏）；videoURL 为空等价 GenerateJSONVisionNoThink。
func (p *Provider) GenerateJSONVisionVideoNoThink(ctx context.Context, system, user string, imageURLs []string, videoURL string, temperature float32, out any) (openai.Usage, error) {
	return p.generate(ctx, system, user, imageURLs, videoURL, out, temperature, EffortDisabled)
}

// glmMedia 多模态块的 URL 载荷。
type glmMedia struct {
	URL string `json:"url"`
}

// glmMediaPart 多模态内容块（text | image_url | video_url）。go-openai 的
// ChatMessagePart 只认 text/image_url，视频块必须本地组包（实测编码端点接受
// video_url 与 image_url 混排，视频块放首位——与官方 video_url 在 content 首位的约定一致）。
type glmMediaPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *glmMedia `json:"image_url,omitempty"`
	VideoURL *glmMedia `json:"video_url,omitempty"`
}

// glmMessage 聊天消息（Content 是字符串或 []glmMediaPart，组包时决定）。
type glmMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

func mediaParts(user string, imageURLs []string, videoURL string) []glmMediaPart {
	imageURLs = sanitizeImageURLs(imageURLs) // 坏图拦在门口：一张坏 data URL 会让端点整包 400
	parts := make([]glmMediaPart, 0, len(imageURLs)+2)
	if videoURL != "" {
		parts = append(parts, glmMediaPart{Type: "video_url", VideoURL: &glmMedia{URL: videoURL}})
	}
	for _, u := range imageURLs {
		parts = append(parts, glmMediaPart{Type: "image_url", ImageURL: &glmMedia{URL: u}})
	}
	parts = append(parts, glmMediaPart{Type: "text", Text: user})
	return parts
}

// httpClient 包级复用（连接池）；15 分钟超时兜底端点偶发挂住，防 Agent 循环/管线
// 永久卡死。2026-10-22 从 10 分钟升档：②风格工位真迹附图后，带图工位对话 + 深思考
// 写大 HTML 的单调用两连死在 600s（chatTemp 的 ctx 超时还没到线，这层先掐了——
// 双层超时必须同步放宽，只改一层是无效修复）。
var httpClient = &http.Client{Timeout: 15 * time.Minute}

// callLeash 单次尝试超时秒数（LLM_CALL_TIMEOUT，默认 300；≤0 关闭走外层闸）。
// 进程内只读一次：env 每跳都查太贵。
func callLeash() time.Duration {
	callLeashOnce.Do(func() {
		v := os.Getenv("LLM_CALL_TIMEOUT")
		if v == "" {
			callLeashSec = 300
			return
		}
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			callLeashSec = -1
			return
		}
		callLeashSec = n
	})
	if callLeashSec <= 0 {
		return 0
	}
	return time.Duration(callLeashSec) * time.Second
}

var (
	callLeashOnce sync.Once
	callLeashSec  int
)

// StatusError 上游非 2xx 的结构化错误：状态码 + 响应体。重试策略按它分流——
// 401/403/400 这类客户端错（key 错/欠费封禁/参数非法）重试也不会好，烧满 3 次
// 退避只是把失败拖慢几秒并掩盖真实病因；429/5xx/网络错才值得重试。
type StatusError struct {
	Code int    // HTTP 状态码
	Body string // 响应体（截断展示）
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("HTTP %d: %.300s", e.Code, e.Body)
}

func (p *Provider) generate(ctx context.Context, system, user string, imageURLs []string, videoURL string, out any, temperature float32, effort Effort) (openai.Usage, error) {
	var lastErr error
	var usage openai.Usage
	for attempt := 0; attempt < 3; attempt++ {
		// 预算闸/记账在 cachedChat 里（chatOnce→postChat 一跳）：缓存命中免费放行，
		// 只有真正出网才过闸记账。预算错误在这里快速失败，不吃退避空转。
		if attempt > 0 {
			// 重试退避感知取消：任务已被取消还空转重试只会白等。
			// 429 要按限速窗口退避（实测 2/4s 出不了窗口，三连试全烧在限速上）；
			// 再叠随机抖动——多路调用方同步退避会一起醒来再撞一次（惊群，
			// 2026-10-17 实测：8 个 worker 全在同一秒重试全灭）。
			delay := time.Duration(attempt) * 2 * time.Second
			var se *StatusError
			if errors.As(lastErr, &se) && se.Code == http.StatusTooManyRequests {
				delay = time.Duration(attempt)*15*time.Second + time.Duration(rand.Int63n(int64(6*time.Second)))
			}
			select {
			case <-ctx.Done():
				return usage, ctx.Err()
			case <-time.After(delay):
			}
		}
		// 账户级并发闸：过闸后才发请求（闸在重试等待之外，睡着的调用不占坑）
		select {
		case <-ctx.Done():
			return usage, ctx.Err()
		case llmGate <- struct{}{}:
		}
		resp, err := func() (r openai.ChatCompletionResponse, e error) {
			defer func() { <-llmGate }()
			return p.chatOnce(ctx, system, mediaParts(user, imageURLs, videoURL), temperature, effort)
		}()
		if err != nil {
			if ctx.Err() != nil { // 取消/超时不是可重试错误
				return usage, ctx.Err()
			}
			if errors.Is(err, ErrBudgetExceeded) { // 预算闸拒发（cachedChat 里）：立刻失败
				return usage, err
			}
			// 4xx 客户端错（除 429）重试不会好：立即失败并透传状态码；
			// 429 限流/5xx/网络错照旧退避重试
			var se *StatusError
			if errors.As(err, &se) && se.Code >= 400 && se.Code < 500 && se.Code != http.StatusTooManyRequests {
				return usage, fmt.Errorf("LLM 请求被上游拒绝（HTTP %d，不重试）: %w", se.Code, se)
			}
			lastErr = err
			continue // 网络/限流类错误重试
		}
		// 上游 200 但 choices 空（内容审查截断/配额截断常见）：不设防会越界 panic
		if len(resp.Choices) == 0 {
			lastErr = fmt.Errorf("上游返回空 choices（可能被内容审查或长度截断），finish 原因未知")
			continue
		}
		usage = resp.Usage
		content := extractJSON(stripFence(resp.Choices[0].Message.Content))
		if err := json.Unmarshal([]byte(content), out); err != nil {
			lastErr = fmt.Errorf("模型输出不是合法 JSON: %w（原文前 200 字: %.200s）", err, content)
			continue
		}
		return usage, nil
	}
	return usage, fmt.Errorf("LLM 作业 3 次尝试均失败: %w", lastErr)
}

// glmThinking 智谱思考开关（go-openai 无此字段，手工组包时带上）。
type glmThinking struct {
	Type string `json:"type"` // enabled | disabled
}

// glmChatRequest 统一组包壳。消息数组由组包方预序列化成 RawMessage——字符串内容
// （Chat/工具对话）与多模态块（含 video_url）两条路共用一个壳，postChat 零分叉。
type glmChatRequest struct {
	Model           string                               `json:"model"`
	Messages        json.RawMessage                      `json:"messages"`
	ResponseFormat  *openai.ChatCompletionResponseFormat `json:"response_format,omitempty"`
	Temperature     float32                              `json:"temperature,omitempty"`
	Tools           []openai.Tool                        `json:"tools,omitempty"`
	Thinking        *glmThinking                         `json:"thinking,omitempty"`
	ReasoningEffort string                               `json:"reasoning_effort,omitempty"`
}

func (p *Provider) chatOnce(ctx context.Context, system string, userParts []glmMediaPart, temperature float32, effort Effort) (openai.ChatCompletionResponse, error) {
	msgs, err := json.Marshal([]glmMessage{
		{Role: openai.ChatMessageRoleSystem, Content: system},
		{Role: openai.ChatMessageRoleUser, Content: userParts},
	})
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	req := glmChatRequest{
		Model:          p.Model,
		Messages:       msgs,
		ResponseFormat: &openai.ChatCompletionResponseFormat{Type: openai.ChatCompletionResponseFormatTypeJSONObject},
		Temperature:    temperature,
	}
	applyEffort(&req, effort)
	return p.postChat(ctx, req)
}

// chatOnceMsgs 多轮组包（GenerateJSONContinue 专用）：调用方给完整消息数组
// （system 在首位），JSON 模式与单轮 chatOnce 一致。
func (p *Provider) chatOnceMsgs(ctx context.Context, msgs []glmMessage, temperature float32, effort Effort) (openai.ChatCompletionResponse, error) {
	raw, err := json.Marshal(msgs)
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	req := glmChatRequest{
		Model:          p.Model,
		Messages:       raw,
		ResponseFormat: &openai.ChatCompletionResponseFormat{Type: openai.ChatCompletionResponseFormatTypeJSONObject},
		Temperature:    temperature,
	}
	applyEffort(&req, effort)
	return p.postChat(ctx, req)
}

// GenerateJSONContinue 多轮续写（2026-10-20 降本 D2，docs/cost-optimization-plan §3.2）：
// 修复轮在原对话上追加指令——模型带着自己首轮的全上下文改自己的稿，不再失忆重开。
// 重试/退避/预算闸/JSON 解析与 generate 同款；断点续跑时同历史同指令的请求体哈希
// 一致，磁盘缓存天然复用（重启接着续写不重付）。
func (p *Provider) GenerateJSONContinue(ctx context.Context, msgs []openai.ChatCompletionMessage, temperature float32, effort Effort, out any) (openai.Usage, error) {
	glmMsgs := make([]glmMessage, len(msgs))
	for i, m := range msgs {
		glmMsgs[i] = glmMessage{Role: m.Role, Content: m.Content}
	}
	var lastErr error
	var usage openai.Usage
	for attempt := 0; attempt < 3; attempt++ {
		// 预算闸/记账在 cachedChat 里（chatOnceMsgs→postChat 一跳），与 generate 同款
		if attempt > 0 {
			// 重试退避感知取消；429 按限速窗口退避（与 generate 同款，含抖动防惊群）
			delay := time.Duration(attempt) * 2 * time.Second
			var se *StatusError
			if errors.As(lastErr, &se) && se.Code == http.StatusTooManyRequests {
				delay = time.Duration(attempt)*15*time.Second + time.Duration(rand.Int63n(int64(6*time.Second)))
			}
			select {
			case <-ctx.Done():
				return usage, ctx.Err()
			case <-time.After(delay):
			}
		}
		select {
		case <-ctx.Done():
			return usage, ctx.Err()
		case llmGate <- struct{}{}:
		}
		resp, err := func() (r openai.ChatCompletionResponse, e error) {
			defer func() { <-llmGate }()
			return p.chatOnceMsgs(ctx, glmMsgs, temperature, effort)
		}()
		if err != nil {
			if ctx.Err() != nil {
				return usage, ctx.Err()
			}
			if errors.Is(err, ErrBudgetExceeded) { // 预算闸拒发（cachedChat 里）：立刻失败
				return usage, err
			}
			var se *StatusError
			if errors.As(err, &se) && se.Code >= 400 && se.Code < 500 && se.Code != http.StatusTooManyRequests {
				return usage, fmt.Errorf("LLM 请求被上游拒绝（HTTP %d，不重试）: %w", se.Code, se)
			}
			lastErr = err
			continue
		}
		if len(resp.Choices) == 0 {
			lastErr = fmt.Errorf("上游返回空 choices（可能被内容审查或长度截断），finish 原因未知")
			continue
		}
		usage = resp.Usage
		content := extractJSON(stripFence(resp.Choices[0].Message.Content))
		if err := json.Unmarshal([]byte(content), out); err != nil {
			lastErr = fmt.Errorf("模型输出不是合法 JSON: %w（原文前 200 字: %.200s）", err, content)
			continue
		}
		return usage, nil
	}
	return usage, fmt.Errorf("LLM 作业 3 次尝试均失败: %w", lastErr)
}

// applyEffort 把档位落到请求上：disabled 走 thinking 开关，low/high/max 走 reasoning_effort，
// 默认档什么都不传（跟随端点默认）。两者互斥，绝不同时出现。
func applyEffort(req *glmChatRequest, effort Effort) {
	switch effort {
	case EffortDisabled:
		req.Thinking = &glmThinking{Type: "disabled"}
	case EffortDefault:
		// 不传：端点默认（GLM-5.x 开思考时默认 max）
	default:
		req.ReasoningEffort = string(effort)
	}
}

// postChat 组包发送 + 响应解析（chatOnce 与 Chat 共用）。缓存/单飞封皮见
// diskcache.go：相同请求体命中磁盘缓存零成本复用；并发同键只出一份网（P0-2/P0-5）。
func (p *Provider) postChat(ctx context.Context, req glmChatRequest) (openai.ChatCompletionResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	return cachedChat(ctx, p.Model, body, func() ([]byte, error) { return p.postChatNet(ctx, body) })
}

// postChatNet 真正的 HTTP 一跳（缓存未命中时由 cachedChat 调）。
func (p *Provider) postChatNet(ctx context.Context, body []byte) ([]byte, error) {
	// 单次尝试超时（LLM_CALL_TIMEOUT 秒，默认 300）：2026-10-27 切 GLM 实测编码端点
	// 会按概率「黑洞」请求——连接建立、请求发完、header 永不回（同体重放 4 连发
	// 命中 3 黑洞 1 成功），generate 的 3 次重试每次都死等 httpClient 15 分钟线，
	// 一次坏运气放大成 45 分钟站级烧穿。给每次尝试独立短闸，黑洞快速失败进下一
	// 次重试；正常长输出（实测 58 tok/s，万 token 级 HTML ≈ 3 分钟）不受影响。
	if leash := callLeash(); leash > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, leash)
		defer cancel()
	}
	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		strings.TrimSuffix(p.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()
	b, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, err
	}
	if httpResp.StatusCode != http.StatusOK {
		return nil, &StatusError{Code: httpResp.StatusCode, Body: string(b)}
	}
	return b, nil
}

// extractJSON 从模型输出里捞第一个完整 JSON 对象（括号配平，跳过字符串字面量内的花括号）。
// flash 级模型常在 JSON 前后夹标签词/解释句（实测 glm-5.3-flash 输出 "html\n{...}"），
// ResponseFormat=JSONObject 也拦不住；纯 JSON 输出时本函数是恒等变换。
func extractJSON(s string) string {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return s
	}
	inStr, esc, depth := false, false, 0
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return s[start:] // 未闭合：原样交给 Unmarshal 报出可读错误
}

func stripFence(s string) string {
	s = strings.TrimSpace(s)
	for _, fence := range []string{"```json", "```JSON", "```"} {
		if i := strings.Index(s, fence); i >= 0 {
			s = s[i+len(fence):]
			if j := strings.LastIndex(s, "```"); j >= 0 {
				s = s[:j]
			}
			return strings.TrimSpace(s)
		}
	}
	return s
}

// AccumulateUsage 把一次调用的用量累进总量（ReAct/判卷/准备循环的记账收口）。
// prompt/completion/calls 之外把 cached_tokens（平台前缀缓存命中）也带上——
// 5 小时额度账要能看出缓存命中率，2026-10-19 起 agent.done 落 cached_tokens。
func AccumulateUsage(dst *openai.Usage, src openai.Usage) {
	dst.PromptTokens += src.PromptTokens
	dst.CompletionTokens += src.CompletionTokens
	if src.PromptTokensDetails != nil {
		if dst.PromptTokensDetails == nil {
			dst.PromptTokensDetails = &openai.PromptTokensDetails{}
		}
		dst.PromptTokensDetails.CachedTokens += src.PromptTokensDetails.CachedTokens
	}
}
