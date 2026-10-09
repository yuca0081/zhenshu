package llm

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

var ErrEmptyChoice = errors.New("模型返回空 choices")

// Chat 一轮带工具的对话（Agent ReAct 驱动器）。
// 思考档 high：Agent 每轮都跑，端点默认 max 烧 reasoning token 又拖响应延迟；
// high 兜住工具调用推理。返回模型消息（可能含 ToolCalls）与本次 token 用量。
func (p *Provider) Chat(ctx context.Context, msgs []openai.ChatCompletionMessage, tools []openai.Tool) (openai.ChatCompletionMessage, openai.Usage, error) {
	// 300s：思考档推理在端点忙时会超 2 分钟（连续两轮样张重生成指令死在 deadline），放宽到 5 分钟
	ctx, cancel := context.WithTimeout(ctx, 300*time.Second)
	defer cancel()
	// 消息逐条预序列化（含 tool_calls 等字段原样透传），再拼成 RawMessage 数组进统一壳
	raw := make([]json.RawMessage, 0, len(msgs))
	for _, m := range msgs {
		b, err := json.Marshal(m)
		if err != nil {
			return openai.ChatCompletionMessage{}, openai.Usage{}, err
		}
		raw = append(raw, b)
	}
	arr, err := json.Marshal(raw)
	if err != nil {
		return openai.ChatCompletionMessage{}, openai.Usage{}, err
	}
	req := glmChatRequest{Model: p.Model, Messages: arr}
	if len(tools) > 0 {
		req.Tools = tools
	}
	applyEffort(&req, EffortHigh)
	resp, err := p.postChat(ctx, req)
	if err != nil {
		return openai.ChatCompletionMessage{}, resp.Usage, err
	}
	if len(resp.Choices) == 0 {
		return openai.ChatCompletionMessage{}, resp.Usage, ErrEmptyChoice
	}
	return resp.Choices[0].Message, resp.Usage, nil
}
