package prep

// loop_toolmsg_test.go —— 并行 tool_calls 的消息契约回归（2026-11-03，与
// review/loop_toolmsg_test.go 同因同解）。
//
// 准备阶段工位（②风格 / ③分镜）也会在替身/自检里一轮并发调多个工具；附图 user
// 消息若插在 tool_calls 循环体内，tool 块被隔断，端点回 400（tool_calls must be
// followed by tool messages）→ 整站失败重试，白烧一轮预算。

import (
	"context"
	"errors"
	"testing"

	openai "github.com/sashabaranov/go-openai"
)

// msgCaptureProv 可回放的假模型：记下每次调用收到的完整上下文。
type msgCaptureProv struct {
	script []openai.ChatCompletionMessage
	calls  [][]openai.ChatCompletionMessage
}

func (p *msgCaptureProv) Chat(_ context.Context, msgs []openai.ChatCompletionMessage, _ []openai.Tool) (openai.ChatCompletionMessage, openai.Usage, error) {
	p.calls = append(p.calls, append([]openai.ChatCompletionMessage(nil), msgs...))
	if len(p.script) == 0 {
		return openai.ChatCompletionMessage{}, openai.Usage{}, errors.New("剧本耗尽")
	}
	m := p.script[0]
	p.script = p.script[1:]
	return m, openai.Usage{}, nil
}

func parallelToolMsg(calls ...openai.ToolCall) openai.ChatCompletionMessage {
	return openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, ToolCalls: calls}
}

func prepToolCall(id, name, args string) openai.ToolCall {
	return openai.ToolCall{ID: id, Type: openai.ToolTypeFunction,
		Function: openai.FunctionCall{Name: name, Arguments: args}}
}

func assertPrepToolContract(t *testing.T, msgs []openai.ChatCompletionMessage) {
	t.Helper()
	for i, m := range msgs {
		if m.Role != openai.ChatMessageRoleAssistant || len(m.ToolCalls) == 0 {
			continue
		}
		for j, call := range m.ToolCalls {
			idx := i + 1 + j
			if idx >= len(msgs) {
				t.Fatalf("assistant(tool_calls) 之后缺第 %d 条 tool（端点会 400）", j+1)
			}
			tm := msgs[idx]
			if tm.Role != openai.ChatMessageRoleTool {
				t.Fatalf("assistant(tool_calls) 之后第 %d 条是 %s 不是 tool（tool 块被隔断）", j+1, tm.Role)
			}
			if tm.ToolCallID != call.ID {
				t.Fatalf("第 %d 条 tool 的 ToolCallID=%q，want %q", j+1, tm.ToolCallID, call.ID)
			}
		}
	}
}

// TestPrepLoopParallelCallsKeepImagesAtBlockTail 两条并行工具调用都带图：tool 消息
// 连续、附图合并成一条接在块尾。
func TestPrepLoopParallelCallsKeepImagesAtBlockTail(t *testing.T) {
	prov := &msgCaptureProv{script: []openai.ChatCompletionMessage{
		parallelToolMsg(
			prepToolCall("c1", "check_render", `{"i":1}`),
			prepToolCall("c2", "check_render", `{"i":2}`),
		),
		parallelToolMsg(prepToolCall("c3", "submit_style", `{}`)),
	}}
	render := Tool{Def: toolSchema("check_render", "渲染自检", nil), Run: func(context.Context, string) Observation {
		return Observation{Text: "已渲染", Images: []string{"data:image/png;base64,AAAA"}}
	}}
	submit := Tool{Def: toolSchema("submit_style", "交卷", nil), Run: func(context.Context, string) Observation {
		return Observation{Text: "已收下", Done: true}
	}}
	l := Loop{Prov: prov, System: "s", Kickoff: "k",
		Tools: []Tool{render, submit}, Submit: "submit_style", MaxRounds: 3}
	if err := l.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(prov.calls) != 2 {
		t.Fatalf("模型调用 %d 次，want 2", len(prov.calls))
	}
	msgs := prov.calls[1]
	assertPrepToolContract(t, msgs)
	lastTool := -1
	for i, m := range msgs {
		if m.Role == openai.ChatMessageRoleTool {
			lastTool = i
		}
	}
	var imgMsgs []int
	imgs := 0
	for i, m := range msgs {
		if m.Role != openai.ChatMessageRoleUser || m.MultiContent == nil {
			continue
		}
		n := 0
		for _, p := range m.MultiContent {
			if p.Type == openai.ChatMessagePartTypeImageURL {
				n++
			}
		}
		if n > 0 {
			imgMsgs = append(imgMsgs, i)
			imgs += n
		}
	}
	if len(imgMsgs) != 1 {
		t.Fatalf("本轮附图消息 %d 条，want 1（必须合并）", len(imgMsgs))
	}
	if imgMsgs[0] != lastTool+1 {
		t.Fatalf("附图在 %d，最后一条 tool 在 %d——附图必须接在 tool 块尾", imgMsgs[0], lastTool)
	}
	if imgs != 2 {
		t.Fatalf("本轮附图 %d 张，want 2（两个工具各一张，合并进同一条消息）", imgs)
	}
}
