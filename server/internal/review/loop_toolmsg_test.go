package review

// loop_toolmsg_test.go —— 并行 tool_calls 的消息契约回归（2026-11-03）。
//
// 事故：段审取证工具一轮里被并发调用（≥2 条 tool_calls）时，旧版把「系统附图」
// user 消息插在 tool_calls 循环体内，tool 消息被隔断 → GLM 端点 400
// （assistant tool_calls must be followed by tool messages）→ 审查器故障降级。
// 2026-11-02 素材链测试 9/9 段审全程盲跑就是这么来的。
//
// 本测试锁死两条不变量：
//  1. assistant(tool_calls) 之后紧跟 len(tool_calls) 条 tool 消息，顺序与 ID 一一对应；
//  2. 本轮附图合并成**一条** user 消息，位置在所有 tool 消息之后。

import (
	"context"
	"strings"
	"testing"

	openai "github.com/sashabaranov/go-openai"
)

func parallelCallMsg(calls ...openai.ToolCall) openai.ChatCompletionMessage {
	return openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, ToolCalls: calls}
}

func toolCallOf(id, name, args string) openai.ToolCall {
	return openai.ToolCall{ID: id, Type: openai.ToolTypeFunction,
		Function: openai.FunctionCall{Name: name, Arguments: args}}
}

// assertToolBlockContract 断言整份上下文满足 tool_calls → tool 消息连续契约。
func assertToolBlockContract(t *testing.T, msgs []openai.ChatCompletionMessage) {
	t.Helper()
	for i, m := range msgs {
		if m.Role != openai.ChatMessageRoleAssistant || len(m.ToolCalls) == 0 {
			continue
		}
		for j, call := range m.ToolCalls {
			idx := i + 1 + j
			if idx >= len(msgs) {
				t.Fatalf("assistant(tool_calls) 之后只有 %d 条消息，缺第 %d 条 tool（端点会 400）", len(msgs)-i-1, j+1)
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

// imageUserIdx 带图 user 消息的下标（按出现顺序）。
func imageUserIdx(msgs []openai.ChatCompletionMessage) []int {
	var out []int
	for i, m := range msgs {
		if m.Role != openai.ChatMessageRoleUser || m.MultiContent == nil {
			continue
		}
		for _, p := range m.MultiContent {
			if p.Type == openai.ChatMessagePartTypeImageURL {
				out = append(out, i)
				break
			}
		}
	}
	return out
}

func countImageParts(m openai.ChatCompletionMessage) int {
	n := 0
	for _, p := range m.MultiContent {
		if p.Type == openai.ChatMessagePartTypeImageURL {
			n++
		}
	}
	return n
}

// TestLoopParallelToolCallsKeepImagesAtBlockTail 三条并行取证（首尾带图、中间无图）：
// tool 消息必须连续，附图只一条且带齐 2 张、位置在块尾。
func TestLoopParallelToolCallsKeepImagesAtBlockTail(t *testing.T) {
	prov := &scriptedProv{script: []openai.ChatCompletionMessage{
		parallelCallMsg(
			toolCallOf("c1", "get_frame", `{"t":1}`),
			toolCallOf("c2", "get_frame", `{"t":2}`),
			toolCallOf("c3", "get_clip", `{"t":3}`),
		),
		callMsg("submit_verdict", okVerdict, "c4"),
	}}
	frame := Tool{Def: toolSchema("get_frame", "取帧", nil), Run: func(_ context.Context, args string) Observation {
		if strings.Contains(args, `"t":2`) {
			return Observation{Text: "该时刻无可用帧"}
		}
		return Observation{Text: "已取回帧", Images: []string{"data:image/jpeg;base64,AAAA"}}
	}}
	clip := Tool{Def: toolSchema("get_clip", "取片段", nil), Run: func(context.Context, string) Observation {
		return Observation{Text: "已取回片段", Images: []string{"data:image/jpeg;base64,BBBB"}}
	}}
	l := Loop{Prov: prov, System: "sys", Kickoff: "kick",
		Tools: []Tool{frame, clip, submitTool()}, Submit: "submit_verdict", MaxRounds: 4, Temp: 0.1}
	if err := l.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(prov.calls) != 2 {
		t.Fatalf("模型调用 %d 次，want 2", len(prov.calls))
	}
	msgs := prov.calls[1]
	assertToolBlockContract(t, msgs)
	idx := imageUserIdx(msgs)
	if len(idx) != 1 {
		t.Fatalf("本轮附图消息 %d 条，want 1（必须合并）", len(idx))
	}
	if got := countImageParts(msgs[idx[0]]); got != 2 {
		t.Fatalf("合并附图 %d 张，want 2（c1 + c3）", got)
	}
	// 附图位置：最后一条 tool 之后
	lastTool := -1
	for i, m := range msgs {
		if m.Role == openai.ChatMessageRoleTool {
			lastTool = i
		}
	}
	if idx[0] != lastTool+1 {
		t.Fatalf("附图在 %d，最后一条 tool 在 %d——附图必须接在 tool 块尾", idx[0], lastTool)
	}
}

// TestLoopSubmitWithImagesKeepsContract submit 与取证同轮并行：submit 通过后循环返回，
// 本轮已取回的图仅进留痕、不再外发，且此前的外发上下文仍满足契约。
func TestLoopSubmitWithImagesKeepsContract(t *testing.T) {
	prov := &scriptedProv{script: []openai.ChatCompletionMessage{
		parallelCallMsg(
			toolCallOf("c1", "get_frame", `{"t":1}`),
			toolCallOf("c2", "submit_verdict", okVerdict),
		),
	}}
	var rec recordingSink
	frame := Tool{Def: toolSchema("get_frame", "取帧", nil), Run: func(context.Context, string) Observation {
		return Observation{Text: "已取回帧", Images: []string{"data:image/jpeg;base64,AAAA"}}
	}}
	l := Loop{Prov: prov, System: "sys", Kickoff: "kick", Rec: &rec,
		Tools: []Tool{frame, submitTool()}, Submit: "submit_verdict", MaxRounds: 3, Temp: 0.1}
	if err := l.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(prov.calls) != 1 {
		t.Fatalf("模型调用 %d 次，want 1（submit 当轮收工）", len(prov.calls))
	}
	assertToolBlockContract(t, prov.calls[0])
	// 留痕里要有那条「收工附图」——过程页要能看见取证过什么
	if n := rec.imageMsgs(); n != 1 {
		t.Fatalf("留痕附图 %d 条，want 1", n)
	}
}

// recordingSink 最简留痕接收端：只记带图 user 消息条数。
type recordingSink struct {
	msgs []openai.ChatCompletionMessage
}

func (r *recordingSink) Message(m openai.ChatCompletionMessage) { r.msgs = append(r.msgs, m) }
func (r *recordingSink) Note(string, ...any)                    {}

func (r *recordingSink) imageMsgs() int {
	n := 0
	for _, m := range r.msgs {
		if m.Role == openai.ChatMessageRoleUser {
			if n2 := countImageParts(m); n2 > 0 {
				n++
			}
		}
	}
	return n
}
