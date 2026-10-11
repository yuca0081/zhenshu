package react

import (
	"context"
	"strings"
	"testing"

	openai "github.com/sashabaranov/go-openai"
)

// mockChat 依次回放预设模型消息，记录每次收到的消息与工具表（不打网络）。
type mockChat struct {
	resps   []openai.ChatCompletionMessage
	idx     int
	gotMsgs [][]openai.ChatCompletionMessage
	gotTs   [][]openai.Tool
}

func (m *mockChat) Chat(_ context.Context, msgs []openai.ChatCompletionMessage, tools []openai.Tool) (openai.ChatCompletionMessage, openai.Usage, error) {
	m.gotMsgs = append(m.gotMsgs, msgs)
	m.gotTs = append(m.gotTs, tools)
	r := m.resps[min(m.idx, len(m.resps)-1)]
	m.idx++
	return r, openai.Usage{}, nil
}

func toolCall(id, name, args string) openai.ToolCall {
	return openai.ToolCall{ID: id, Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: name, Arguments: args}}
}

func textMsg(s string) openai.ChatCompletionMessage {
	return openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: s}
}

func callMsg(calls ...openai.ToolCall) openai.ChatCompletionMessage {
	return openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, ToolCalls: calls}
}

func echoTool(name string) Tool {
	return Tool{
		Def:  openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{Name: name}},
		Gate: func() string { return "" },
		Run:  func(_ context.Context, args string) string { return "echo:" + args },
	}
}

// 一轮直达：模型不点工具，直接出文本。
func TestRunDirectAnswer(t *testing.T) {
	m := &mockChat{resps: []openai.ChatCompletionMessage{textMsg("答案")}}
	c := New(m, []Tool{echoTool("t")}, 0, Hooks{})
	final, err := c.Run(context.Background(), []openai.ChatCompletionMessage{{Role: "user", Content: "问"}})
	if err != nil || final != "答案" {
		t.Fatalf("final=%q err=%v", final, err)
	}
	if len(m.gotTs) != 1 || len(m.gotTs[0]) != 1 || m.gotTs[0][0].Function.Name != "t" {
		t.Fatalf("工具 schema 应随首轮传给模型: %+v", m.gotTs)
	}
}

// 想→做→看→再想：首轮点工具，观察回喂后次轮出最终文本。
func TestRunLoopToolThenAnswer(t *testing.T) {
	m := &mockChat{resps: []openai.ChatCompletionMessage{
		callMsg(toolCall("c1", "t", `{"k":1}`)),
		textMsg("做完了"),
	}}
	var finished []string
	c := New(m, []Tool{echoTool("t")}, 0, Hooks{
		OnCall:   func(name, args string) { finished = append(finished, "call:"+name) },
		OnFinish: func(name, args, result string, rejected bool) { finished = append(finished, "fin:"+result) },
	})
	final, err := c.Run(context.Background(), nil)
	if err != nil || final != "做完了" {
		t.Fatalf("final=%q err=%v", final, err)
	}
	if len(m.gotMsgs) != 2 {
		t.Fatalf("应两轮模型调用，得到 %d", len(m.gotMsgs))
	}
	toolMsgs := m.gotMsgs[1]
	var fed bool
	for _, mm := range toolMsgs {
		if mm.Role == openai.ChatMessageRoleTool && mm.Content == "echo:{\"k\":1}" {
			fed = true
		}
	}
	if !fed {
		t.Fatalf("观察结果应回喂: %+v", toolMsgs)
	}
	if len(finished) != 2 || finished[0] != "call:t" || finished[1] != "fin:echo:{\"k\":1}" {
		t.Fatalf("钩子触发顺序异常: %v", finished)
	}
}

// 门禁拒绝：拒绝原因以「拒绝：」前缀回喂，OnCall 不触发，OnFinish 带 rejected=true。
func TestRunGateReject(t *testing.T) {
	m := &mockChat{resps: []openai.ChatCompletionMessage{
		callMsg(toolCall("c1", "t", `{}`)),
		textMsg("知道了"),
	}}
	blocked := echoTool("t")
	blocked.Gate = func() string { return "前置未齐" }
	var rejected bool
	c := New(m, []Tool{blocked}, 0, Hooks{OnFinish: func(_, _, result string, rej bool) { rejected = rej }})
	if _, err := c.Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	fed := m.gotMsgs[1][len(m.gotMsgs[1])-1]
	if !rejected || fed.Content != "拒绝：前置未齐" {
		t.Fatalf("拒绝原因应回喂且标记 rejected: %q rejected=%v", fed.Content, rejected)
	}
}

// 步数上限：模型永远点工具，跑满 maxSteps 即停，返回空串由宿主兜底。
func TestRunStepCap(t *testing.T) {
	m := &mockChat{resps: []openai.ChatCompletionMessage{callMsg(toolCall("c", "t", `{}`))}}
	c := New(m, []Tool{echoTool("t")}, 3, Hooks{})
	final, err := c.Run(context.Background(), nil)
	if err != nil || final != "" {
		t.Fatalf("步数用尽应返回空串: final=%q err=%v", final, err)
	}
	if m.idx != 3 {
		t.Fatalf("应恰好 3 轮模型调用，得到 %d", m.idx)
	}
}

// 未知工具：回喂提示，不触发任何钩子。
func TestRunUnknownTool(t *testing.T) {
	m := &mockChat{resps: []openai.ChatCompletionMessage{
		callMsg(toolCall("c1", "nope", `{}`)),
		textMsg("好"),
	}}
	hooked := false
	c := New(m, []Tool{echoTool("t")}, 0, Hooks{OnFinish: func(name, _, _ string, _ bool) {
		if name == "nope" {
			hooked = true
		}
	}})
	if _, err := c.Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if hooked {
		t.Fatal("未知工具不应触发钩子")
	}
	fed := m.gotMsgs[1][len(m.gotMsgs[1])-1]
	if fed.Content != "未知工具 nope" {
		t.Fatalf("未知工具应回喂提示: %q", fed.Content)
	}
}

// 重名工具：后注册者胜出，schema 不重复出现。
func TestNewDedupesTools(t *testing.T) {
	first, second := echoTool("t"), echoTool("t")
	second.Run = func(_ context.Context, _ string) string { return "second" }
	c := New(&mockChat{}, []Tool{first, second}, 0, Hooks{})
	if len(c.tools) != 1 || len(c.schemas()) != 1 {
		t.Fatalf("重名应去重: %d", len(c.tools))
	}
	if got := c.tools[0].Run(context.Background(), ""); got != "second" {
		t.Fatalf("后注册者应胜出: %q", got)
	}
}

// 预算帽（供撰稿人等搜索类工具复用）：帽内放行，用完回喂人话提示且不再打真搜索。
func TestWithCallBudget(t *testing.T) {
	calls := 0
	base := echoTool("search")
	base.Run = func(_ context.Context, _ string) string { calls++; return "结果" }
	capped := WithCallBudget(base, 2, "搜索额度已用完")
	for i := 0; i < 3; i++ {
		got := capped.Run(context.Background(), `{}`)
		if i < 2 && got != "结果" {
			t.Fatalf("帽内应放行: %q", got)
		}
		if i == 2 && !strings.Contains(got, "额度已用完") {
			t.Fatalf("超帽应回喂提示: %q", got)
		}
	}
	if calls != 2 {
		t.Fatalf("真执行应恰好 2 次，得到 %d", calls)
	}
}
