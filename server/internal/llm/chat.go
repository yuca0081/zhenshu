package llm

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"net/http"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

var ErrEmptyChoice = errors.New("模型返回空 choices")

// Chat 一轮带工具的对话（Agent ReAct 驱动器）。
// 思考档 high：Agent 每轮都跑，端点默认 max 烧 reasoning token 又拖响应延迟；
// high 兜住工具调用推理。返回模型消息（可能含 ToolCalls）与本次 token 用量。
func (p *Provider) Chat(ctx context.Context, msgs []openai.ChatCompletionMessage, tools []openai.Tool) (openai.ChatCompletionMessage, openai.Usage, error) {
	return p.chatTemp(ctx, msgs, tools, nil, EffortHigh)
}

// ChatTemp 同 Chat，显式设采样温度。判卷型审核（意见单要可复现、不发散）用低温；
// Chat 不设温度保持端点默认（既有工位行为不变）。temp=nil 等价 Chat。
// 思考档固定 high：Agent 每轮都要在工具间做推理。
func (p *Provider) ChatTemp(ctx context.Context, msgs []openai.ChatCompletionMessage, tools []openai.Tool, temp float64) (openai.ChatCompletionMessage, openai.Usage, error) {
	return p.chatTemp(ctx, msgs, tools, &temp, EffortHigh)
}

// ChatTempEffort 同 ChatTemp，思考档位由调用方给。判卷型审核走这里降档：
// 对照清单打勾的活，high 档烧的 reasoning token 比意见单本身贵一个量级
// （2026-10-19 实测账：审核 completion 46.5 万/项目，意见单撑死十分之一）。
func (p *Provider) ChatTempEffort(ctx context.Context, msgs []openai.ChatCompletionMessage, tools []openai.Tool, temp float64, effort Effort) (openai.ChatCompletionMessage, openai.Usage, error) {
	return p.chatTemp(ctx, msgs, tools, &temp, effort)
}

func (p *Provider) chatTemp(ctx context.Context, msgs []openai.ChatCompletionMessage, tools []openai.Tool, temp *float64, effort Effort) (openai.ChatCompletionMessage, openai.Usage, error) {
	// 900s：②风格工位真迹附图上线后（2026-10-22 实测），带图工位对话 + 深思考写
	// 大 HTML 的单调用两连死在 600s 线（prep.style cold_retry×2，context deadline
	// exceeded）。此前 300s 不够判卷载荷（段审实测死在 5 分钟线上）升到 600；
	// 现在样张探索会话常驻 3 张真迹图，再放一档。Chat 与 ChatTemp 同走这档。
	ctx, cancel := context.WithTimeout(ctx, 900*time.Second)
	defer cancel()
	// 消息逐条预序列化（含 tool_calls 等字段原样透传），再拼成 RawMessage 数组进统一壳。
	// 预序列化前过一遍图片校验（P0-3）：非法 image_url 在这里拦下换占位，不让
	// 它把整轮请求送去端点吃 400。
	raw := make([]json.RawMessage, 0, len(msgs))
	for _, m := range msgs {
		b, err := json.Marshal(sanitizeChatMsg(m))
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
	applyEffort(&req, effort)
	// 瞬时错误退避重试（循环对话轮没有外层重试——审核/工位一轮失败就整轮报废，
	// 代价远大于等一个限速窗口）。可重试=429 限流/5xx/网络错，与 generate 同一口径
	// （2026-10-26 审查发现原先只重试 429：一次网络抖动或上游 5xx 即整轮报废）。
	// 退避 15s/30s，叠随机抖动——多路调用方同步退避会一起醒来再撞一次
	// （2026-10-17 惊群教训，与 generate 同款）；ctx 取消立即让路。
	// 预算闸/记账在 cachedChat 里（postChat 一跳）：缓存命中免费放行，只有真正
	// 出网才过闸记账；预算错误不可重试，下方显式放行立即失败。
	for attempt := 0; ; attempt++ {
		// 账户级并发闸（2026-10-19 高-8）：Chat/ChatTemp 原先绕闸直发，而准备三
		// 工位与全部审片循环都走这条路——多项目并行时正是 429 惊群最后一个口子。
		// 闸在退避等待之外（睡着的调用不占坑），与 generate 同一道闸。
		select {
		case <-ctx.Done():
			return openai.ChatCompletionMessage{}, openai.Usage{}, ctx.Err()
		case llmGate <- struct{}{}:
		}
		resp, err := func() (openai.ChatCompletionResponse, error) {
			defer func() { <-llmGate }()
			return p.postChat(ctx, req)
		}()
		if err == nil {
			if len(resp.Choices) == 0 {
				return openai.ChatCompletionMessage{}, resp.Usage, ErrEmptyChoice
			}
			return resp.Choices[0].Message, resp.Usage, nil
		}
		var se *StatusError
		if attempt >= 2 || ctx.Err() != nil || errors.Is(err, ErrBudgetExceeded) {
			return openai.ChatCompletionMessage{}, openai.Usage{}, err
		}
		// 4xx 客户端错（除 429）重试不会好：立即失败透传状态码；
		// 429 限流/5xx/网络错（非 StatusError）照旧退避重试——generate 同款分类
		if errors.As(err, &se) && se.Code >= 400 && se.Code < 500 && se.Code != http.StatusTooManyRequests {
			return openai.ChatCompletionMessage{}, openai.Usage{}, err
		}
		select {
		case <-ctx.Done():
			return openai.ChatCompletionMessage{}, openai.Usage{}, ctx.Err()
		case <-time.After(time.Duration(attempt+1)*15*time.Second + time.Duration(rand.Int63n(int64(6*time.Second)))):
		}
	}
}
