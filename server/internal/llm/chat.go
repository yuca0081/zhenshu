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
	return p.chatTemp(ctx, msgs, tools, nil)
}

// ChatTemp 同 Chat，显式设采样温度。判卷型审核（意见单要可复现、不发散）用低温；
// Chat 不设温度保持端点默认（既有工位行为不变）。temp=nil 等价 Chat。
func (p *Provider) ChatTemp(ctx context.Context, msgs []openai.ChatCompletionMessage, tools []openai.Tool, temp float64) (openai.ChatCompletionMessage, openai.Usage, error) {
	return p.chatTemp(ctx, msgs, tools, &temp)
}

func (p *Provider) chatTemp(ctx context.Context, msgs []openai.ChatCompletionMessage, tools []openai.Tool, temp *float64) (openai.ChatCompletionMessage, openai.Usage, error) {
	// 600s：判卷载荷比工位对话重（锚点帧/样张多图 + 深思考），端点忙时 300s 不够
	// （段审实测死在 5 分钟线上）；Chat 保持 300s 不变，只有审核走这档
	ctx, cancel := context.WithTimeout(ctx, 600*time.Second)
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
	if temp != nil {
		req.Temperature = float32(*temp)
	}
	if len(tools) > 0 {
		req.Tools = tools
	}
	applyEffort(&req, EffortHigh)
	// 429 限速退避重试（循环对话轮没有外层重试——审核/工位一轮 429 就整轮报废，
	// 代价远大于等一个限速窗口）。退避 15s/30s；ctx 取消立即让路。
	var usage openai.Usage
	for attempt := 0; ; attempt++ {
		resp, err := p.postChat(ctx, req)
		if err == nil {
			if len(resp.Choices) == 0 {
				return openai.ChatCompletionMessage{}, resp.Usage, ErrEmptyChoice
			}
			return resp.Choices[0].Message, resp.Usage, nil
		}
		usage = resp.Usage
		var se *StatusError
		if attempt >= 2 || ctx.Err() != nil || !errors.As(err, &se) || se.Code != 429 {
			return openai.ChatCompletionMessage{}, usage, err
		}
		select {
		case <-ctx.Done():
			return openai.ChatCompletionMessage{}, usage, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 15 * time.Second):
		}
	}
}
