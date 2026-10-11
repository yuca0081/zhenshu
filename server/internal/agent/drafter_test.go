package agent

import (
	"context"
	"strings"
	"testing"

	openai "github.com/sashabaranov/go-openai"

	"webpage-video-agent/server/internal/react"
)

// 撰稿工位测试：模型走 mock、工具走假搜索，不打网络不烧搜索额度。

// mockChatClient 依次回放预设模型消息，记录每次收到的消息表。
type mockChatClient struct {
	resps   []openai.ChatCompletionMessage
	idx     int
	gotMsgs [][]openai.ChatCompletionMessage
}

func (m *mockChatClient) Chat(_ context.Context, msgs []openai.ChatCompletionMessage, _ []openai.Tool) (openai.ChatCompletionMessage, openai.Usage, error) {
	m.gotMsgs = append(m.gotMsgs, msgs)
	r := m.resps[min(m.idx, len(m.resps)-1)]
	m.idx++
	return r, openai.Usage{}, nil
}

func stubSearchTool(result string) react.Tool {
	return react.Tool{
		Def:  toolDef("web_search", "stub", nil),
		Gate: func() string { return "" },
		Run:  func(_ context.Context, _ string) string { return result },
	}
}

func TestDraftDirectAnswer(t *testing.T) {
	m := &mockChatClient{resps: []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleAssistant, Content: "钩子句。知识点。结尾？"},
	}}
	got, err := draftWithTools(context.Background(), m, nil, "黑洞有多大", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "钩子句") {
		t.Fatalf("应原样返回成稿: %q", got)
	}
	if len(m.gotMsgs) != 1 {
		t.Fatalf("应一轮完成: %d", len(m.gotMsgs))
	}
	// system 提示词与用户主题应进首轮
	first := m.gotMsgs[0]
	if first[0].Role != openai.ChatMessageRoleSystem || !strings.Contains(first[0].Content, "口播撰稿人") {
		t.Fatalf("system 提示词缺失: %.60s", first[0].Content)
	}
	if !strings.Contains(first[1].Content, "黑洞有多大") {
		t.Fatalf("用户主题缺失: %.60s", first[1].Content)
	}
}

// 想→做→看→再想：先搜后写，搜索观察回喂给模型后才成稿。
func TestDraftSearchThenWrite(t *testing.T) {
	m := &mockChatClient{resps: []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleAssistant, ToolCalls: []openai.ToolCall{{
			ID: "c1", Type: openai.ToolTypeFunction,
			Function: openai.FunctionCall{Name: "web_search", Arguments: `{"query":"最新观测"}`},
		}}},
		{Role: openai.ChatMessageRoleAssistant, Content: "基于搜索的成稿。"},
	}}
	tools := []react.Tool{stubSearchTool("搜到 3 条：直径 930 亿光年…")}
	got, err := draftWithTools(context.Background(), m, tools, "宇宙有多大", 1)
	if err != nil || got != "基于搜索的成稿。" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	var fed bool
	for _, mm := range m.gotMsgs[1] {
		if mm.Role == openai.ChatMessageRoleTool && strings.Contains(mm.Content, "930 亿光年") {
			fed = true
		}
	}
	if !fed {
		t.Fatalf("搜索观察应回喂: %+v", m.gotMsgs[1])
	}
}

// 步数用尽：模型一直点工具不交稿 → 明确报错而非空串。
func TestDraftStepCap(t *testing.T) {
	m := &mockChatClient{resps: []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleAssistant, ToolCalls: []openai.ToolCall{{
			ID: "c", Type: openai.ToolTypeFunction,
			Function: openai.FunctionCall{Name: "web_search", Arguments: `{"query":"x"}`},
		}}},
	}}
	_, err := draftWithTools(context.Background(), m, []react.Tool{stubSearchTool("结果")}, "主题", 1)
	if err == nil || !strings.Contains(err.Error(), "未在限定步数内交稿") {
		t.Fatalf("步数用尽应报未交稿: %v", err)
	}
}

func TestStripContentWrapper(t *testing.T) {
	cases := []struct{ in, want string }{
		{"纯文本成稿", "纯文本成稿"},
		{"  带空白  ", "带空白"},
		{`{"content":"老习惯 JSON 包装"}`, "老习惯 JSON 包装"},
		{`{"other":1}`, `{"other":1}`}, // 非 content 字段不误剥
		{"不是 JSON { 开头", "不是 JSON { 开头"},
	}
	for _, c := range cases {
		if got := stripContentWrapper(c.in); got != c.want {
			t.Errorf("stripContentWrapper(%q)=%q want %q", c.in, got, c.want)
		}
	}
}
