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

// GenerateJSON 一次 JSON 作业：system+user → 模型 → 剥掉 markdown 围栏 → json.Unmarshal 到 out。
// 返回本次 token 用量（成本记账）；重试时的用量取最后一次成功调用。
func (p *Provider) GenerateJSON(ctx context.Context, system, user string, out any) (openai.Usage, error) {
	return p.generate(ctx, system,
		[]openai.ChatMessagePart{{Type: openai.ChatMessagePartTypeText, Text: user}}, out, 0.7, false)
}

// GenerateJSONNoThink 同 GenerateJSON，但显式关闭 GLM 的思考（thinking.type=disabled）。
// 结构化作业（样张/画面 HTML）思考会烧上万 reasoning token、拖到分钟级，纯亏时长。
func (p *Provider) GenerateJSONNoThink(ctx context.Context, system, user string, out any) (openai.Usage, error) {
	return p.generate(ctx, system,
		[]openai.ChatMessagePart{{Type: openai.ChatMessagePartTypeText, Text: user}}, out, 0.7, true)
}

// GenerateJSONVision 多模态 JSON 作业：user 文本 + 若干图片（data URL）→ 模型，解析同 GenerateJSON。
// temperature 由调用方定：评审类低温（稳定判断），画面生成类高温（发散）。
func (p *Provider) GenerateJSONVision(ctx context.Context, system, user string, imageURLs []string, temperature float32, out any) (openai.Usage, error) {
	return p.generate(ctx, system, visionParts(user, imageURLs), out, temperature, false)
}

// GenerateJSONVisionNoThink 同 GenerateJSONVision，但关闭思考。画面 spec 这类大 HTML 作业
// 开思考会烧 1 万+ reasoning token、单段拖到 5–13 分钟；关掉后同样的活分钟级完成。
func (p *Provider) GenerateJSONVisionNoThink(ctx context.Context, system, user string, imageURLs []string, temperature float32, out any) (openai.Usage, error) {
	return p.generate(ctx, system, visionParts(user, imageURLs), out, temperature, true)
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

func (p *Provider) generate(ctx context.Context, system string, userParts []openai.ChatMessagePart, out any, temperature float32, noThink bool) (openai.Usage, error) {
	var lastErr error
	var usage openai.Usage
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}
		resp, err := p.chatOnce(ctx, system, userParts, temperature, noThink)
		if err != nil {
			lastErr = err
			continue // 网络/限流类错误重试
		}
		// 上游 200 但 choices 空（内容审查截断/配额截断常见）：不设防会越界 panic
		if len(resp.Choices) == 0 {
			lastErr = fmt.Errorf("上游返回空 choices（可能被内容审查或长度截断），finish 原因未知")
			continue
		}
		usage = resp.Usage
		content := stripFence(resp.Choices[0].Message.Content)
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
	Thinking *glmThinking `json:"thinking,omitempty"`
}

func (p *Provider) chatOnce(ctx context.Context, system string, userParts []openai.ChatMessagePart, temperature float32, noThink bool) (openai.ChatCompletionResponse, error) {
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
	if noThink {
		req.Thinking = &glmThinking{Type: "disabled"}
	}
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
	cli := &http.Client{Timeout: 10 * time.Minute}
	httpResp, err := cli.Do(httpReq)
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

func cfg(p *Provider) openai.ClientConfig {
	c := openai.DefaultConfig(p.APIKey)
	c.BaseURL = p.BaseURL
	// 默认 http.Client 无超时：端点偶发挂住会把 Agent 循环/管线永久卡死
	c.HTTPClient = &http.Client{Timeout: 3 * time.Minute}
	return c
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
