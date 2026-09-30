package llm

import (
	"context"
	"errors"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

var ErrEmptyChoice = errors.New("模型返回空 choices")

// Chat 一轮带工具的对话（Agent ReAct 驱动器）。
// 返回模型消息（可能含 ToolCalls）与本次 token 用量。
func (p *Provider) Chat(ctx context.Context, msgs []openai.ChatCompletionMessage, tools []openai.Tool) (openai.ChatCompletionMessage, openai.Usage, error) {
	cli := openai.NewClientWithConfig(cfg(p))
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	req := openai.ChatCompletionRequest{
		Model:    p.Model,
		Messages: msgs,
	}
	if len(tools) > 0 {
		req.Tools = tools
	}
	resp, err := cli.CreateChatCompletion(ctx, req)
	if err != nil {
		return openai.ChatCompletionMessage{}, resp.Usage, err
	}
	if len(resp.Choices) == 0 {
		return openai.ChatCompletionMessage{}, resp.Usage, ErrEmptyChoice
	}
	return resp.Choices[0].Message, resp.Usage, nil
}
