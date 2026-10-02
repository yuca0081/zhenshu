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
	"net/http"
	"os"
	"strings"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

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
}

// FromEnv 按环境变量构造供应商：LLM_API_KEY / LLM_BASE_URL / LLM_MODEL（旧名 DEEPSEEK_* 仍兼容）。
// 默认 GLM 编码套餐端点 + glm-5.3-flash。缺 key 返回错误（调用方转 ErrAwaitLLM 降级会话模式）。
func FromEnv(role Role) (*Provider, error) {
	get := func(newName, oldName string) string {
		if v := os.Getenv(newName); v != "" {
			return v
		}
		return os.Getenv(oldName)
	}
	switch role {
	case RolePlan, RoleDialogue, RoleVisual: // M0 单模型起步：bake-off 后按档分模型
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
		return &Provider{Name: "llm", BaseURL: base, APIKey: key, Model: model}, nil
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
	EffortMax      Effort = "max"      // 最深思考：与端点默认等价，显式声明时用
	EffortDisabled Effort = "disabled" // thinking.type=disabled 彻底关闭，结构化作业专用
)

// GenerateJSON 一次 JSON 作业：system+user → 模型 → 剥掉 markdown 围栏 → json.Unmarshal 到 out。
// 返回本次 token 用量（成本记账）；重试时的用量取最后一次成功调用。
func (p *Provider) GenerateJSON(ctx context.Context, system, user string, out any) (openai.Usage, error) {
	return p.generate(ctx, system,
		[]openai.ChatMessagePart{{Type: openai.ChatMessagePartTypeText, Text: user}}, out, 0.7, EffortDefault)
}

// GenerateJSONNoThink 同 GenerateJSON，但彻底关闭思考（thinking.type=disabled）。
// 结构化作业（样张/画面 HTML）思考会烧上万 reasoning token、拖到分钟级，纯亏时长。
func (p *Provider) GenerateJSONNoThink(ctx context.Context, system, user string, out any) (openai.Usage, error) {
	return p.generate(ctx, system,
		[]openai.ChatMessagePart{{Type: openai.ChatMessagePartTypeText, Text: user}}, out, 0.7, EffortDisabled)
}

// GenerateJSONEffort 同 GenerateJSON，思考档位由调用方定。
func (p *Provider) GenerateJSONEffort(ctx context.Context, system, user string, out any, effort Effort) (openai.Usage, error) {
	return p.generate(ctx, system,
		[]openai.ChatMessagePart{{Type: openai.ChatMessagePartTypeText, Text: user}}, out, 0.7, effort)
}

// GenerateJSONVision 多模态 JSON 作业：user 文本 + 若干图片（data URL）→ 模型，解析同 GenerateJSON。
// temperature 由调用方定：评审类低温（稳定判断），画面生成类高温（发散）。
func (p *Provider) GenerateJSONVision(ctx context.Context, system, user string, imageURLs []string, temperature float32, out any) (openai.Usage, error) {
	return p.generate(ctx, system, visionParts(user, imageURLs), out, temperature, EffortDefault)
}

// GenerateJSONVisionNoThink 同 GenerateJSONVision，但关闭思考。画面 spec 这类大 HTML 作业
// 开思考会烧 1 万+ reasoning token、单段拖到 5–13 分钟；关掉后同样的活分钟级完成。
func (p *Provider) GenerateJSONVisionNoThink(ctx context.Context, system, user string, imageURLs []string, temperature float32, out any) (openai.Usage, error) {
	return p.generate(ctx, system, visionParts(user, imageURLs), out, temperature, EffortDisabled)
}

// GenerateJSONVisionEffort 同 GenerateJSONVision，思考档位由调用方定。
func (p *Provider) GenerateJSONVisionEffort(ctx context.Context, system, user string, imageURLs []string, temperature float32, out any, effort Effort) (openai.Usage, error) {
	return p.generate(ctx, system, visionParts(user, imageURLs), out, temperature, effort)
}

func visionParts(user string, imageURLs []string) []openai.ChatMessagePart {
	parts := make([]openai.ChatMessagePart, 0, len(imageURLs)+1)
	for _, u := range imageURLs {
		parts = append(parts, openai.ChatMessagePart{
			Type:     openai.ChatMessagePartTypeImageURL,
			ImageURL: &openai.ChatMessageImageURL{URL: u},
		})
	}
	parts = append(parts, openai.ChatMessagePart{Type: openai.ChatMessagePartTypeText, Text: user})
	return parts
}

// httpClient 包级复用（连接池）；10 分钟超时兜底端点偶发挂住，防 Agent 循环/管线永久卡死。
var httpClient = &http.Client{Timeout: 10 * time.Minute}

func (p *Provider) generate(ctx context.Context, system string, userParts []openai.ChatMessagePart, out any, temperature float32, effort Effort) (openai.Usage, error) {
	var lastErr error
	var usage openai.Usage
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			// 重试退避感知取消：任务已被取消还空转重试只会白等
			select {
			case <-ctx.Done():
				return usage, ctx.Err()
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
		}
		resp, err := p.chatOnce(ctx, system, userParts, temperature, effort)
		if err != nil {
			if ctx.Err() != nil { // 取消/超时不是可重试错误
				return usage, ctx.Err()
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

// glmChatRequest ChatCompletionRequest + 智谱扩展字段（内嵌扁平序列化）。
type glmChatRequest struct {
	openai.ChatCompletionRequest
	Thinking        *glmThinking `json:"thinking,omitempty"`         // 思考开关（disabled 专用）
	ReasoningEffort string       `json:"reasoning_effort,omitempty"` // 思考档位：low/high/max，与 Thinking 互斥
}

func (p *Provider) chatOnce(ctx context.Context, system string, userParts []openai.ChatMessagePart, temperature float32, effort Effort) (openai.ChatCompletionResponse, error) {
	req := glmChatRequest{
		ChatCompletionRequest: openai.ChatCompletionRequest{
			Model: p.Model,
			Messages: []openai.ChatCompletionMessage{
				{Role: openai.ChatMessageRoleSystem, Content: system},
				{Role: openai.ChatMessageRoleUser, MultiContent: userParts},
			},
			ResponseFormat: &openai.ChatCompletionResponseFormat{Type: openai.ChatCompletionResponseFormatTypeJSONObject},
			Temperature:    temperature,
		},
	}
	applyEffort(&req, effort)
	return p.postChat(ctx, req)
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

// postChat 组包发送 + 响应解析（chatOnce 与 Chat 共用）。
func (p *Provider) postChat(ctx context.Context, req glmChatRequest) (openai.ChatCompletionResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		strings.TrimSuffix(p.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	defer httpResp.Body.Close()
	b, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	if httpResp.StatusCode != http.StatusOK {
		return openai.ChatCompletionResponse{}, fmt.Errorf("HTTP %d: %.300s", httpResp.StatusCode, b)
	}
	var resp openai.ChatCompletionResponse
	if err := json.Unmarshal(b, &resp); err != nil {
		return openai.ChatCompletionResponse{}, fmt.Errorf("响应解析失败: %w（原文前 200 字: %.200s）", err, b)
	}
	return resp, nil
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
