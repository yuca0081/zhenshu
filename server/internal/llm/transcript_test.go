package llm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	openai "github.com/sashabaranov/go-openai"
)

func TestValidTask(t *testing.T) {
	ok := []string{"prep.script", "seg.write.seg01", "seg.audit.seg03.r2", "final.audit.r1", "a"}
	bad := []string{"", "../etc", "a/b", "A.upper", ".hidden", strings.Repeat("a", 100), "seg..x "}
	for _, s := range ok {
		if !ValidTask(s) {
			t.Errorf("ValidTask(%q) = false, want true", s)
		}
	}
	for _, s := range bad {
		if ValidTask(s) {
			t.Errorf("ValidTask(%q) = true, want false", s)
		}
	}
}

func TestTranscriptAppendAndSanitize(t *testing.T) {
	dir := t.TempDir()
	tr := OpenTranscript(dir, "seg.write.seg01")
	if tr == nil {
		t.Fatal("OpenTranscript 返回 nil")
	}
	long := strings.Repeat("字", maxContentRunes+500)
	tr.Message(openai.ChatCompletionMessage{Role: "system", Content: "系统提示"})
	tr.Message(openai.ChatCompletionMessage{Role: "user", MultiContent: []openai.ChatMessagePart{
		{Type: openai.ChatMessagePartTypeText, Text: "看图"},
		{Type: openai.ChatMessagePartTypeImageURL, ImageURL: &openai.ChatMessageImageURL{URL: "data:image/png;base64,AAAA"}},
	}})
	tr.Message(openai.ChatCompletionMessage{
		Role: "assistant",
		ToolCalls: []openai.ToolCall{{
			ID: "c1", Type: "function",
			Function: openai.FunctionCall{Name: "get_frame", Arguments: strings.Repeat("x", maxArgsRunes+100)},
		}},
	})
	tr.Message(openai.ChatCompletionMessage{Role: "tool", Content: long, ToolCallID: "c1", Name: "get_frame"})
	tr.Note("模型调用失败: %s", "超时")
	tr.Close()
	tr.Close() // 重复关不炸

	b, err := os.ReadFile(filepath.Join(dir, "seg.write.seg01.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var msgs []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("行不是合法 JSON: %v\n%s", err, line)
		}
		msgs = append(msgs, m)
	}
	if len(msgs) != 5 {
		t.Fatalf("留痕 %d 条, want 5", len(msgs))
	}
	if msgs[0]["content"] != "系统提示" {
		t.Errorf("正文原样保留失败: %v", msgs[0]["content"])
	}
	if c, _ := msgs[1]["content"].(string); !strings.Contains(c, "[图片×1]") {
		t.Errorf("图片应换成占位: %v", msgs[1]["content"])
	}
	calls := msgs[2]["tool_calls"].([]any)
	args := calls[0].(map[string]any)["args"].(string)
	if !strings.Contains(args, "截断") {
		t.Errorf("超长参数应截断")
	}
	if c, _ := msgs[3]["content"].(string); !strings.Contains(c, "截断，共") {
		t.Errorf("超长正文应截断: %v", c)
	}
	if msgs[4]["role"] != "note" {
		t.Errorf("旁注 role = %v", msgs[4]["role"])
	}
	if tr.Rel != "llm/transcripts/seg.write.seg01.jsonl" {
		t.Errorf("Rel = %q", tr.Rel)
	}
}

func TestOpenTranscriptRejectsBadTask(t *testing.T) {
	if tr := OpenTranscript(t.TempDir(), "../evil"); tr != nil {
		t.Fatal("非法任务名应返回 nil")
	}
}
