package llm

import (
	"encoding/base64"
	"strings"
	"testing"

	openai "github.com/sashabaranov/go-openai"
)

const goodPNG = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="

func pngDataURL(body string) string {
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte(body))
}

func TestValidImageData(t *testing.T) {
	cases := []struct {
		name string
		u    string
		ok   bool
	}{
		{"合法 PNG", goodPNG, true},
		{"空串", "", false},
		{"本地路径", "file:///C:/renders/frame.png", false},
		{"http 外链图", "https://example.com/a.png", false},
		{"无 base64 标记", "data:image/png,rawbytes", false},
		{"非 image 类型", "data:video/mp4;base64,AAAA", false},
		{"截断载荷", "data:image/png;base64,AAAA", true}, // 头 4 字节可解即放行（抽头解码的边界）
		{"垃圾 base64", "data:image/png;base64,%%%%不是base64%%%%", false},
	}
	for _, c := range cases {
		if got := ValidImageData(c.u); got != c.ok {
			t.Fatalf("%s: 期望 %v 得到 %v", c.name, c.ok, got)
		}
	}
}

// generate 附图清单过滤：坏图不外发，好图保序保留。
func TestMediaPartsSanitized(t *testing.T) {
	parts := mediaParts("正文", []string{goodPNG, "", "file:///x.png", pngDataURL("hello")}, "")
	var n int
	for _, p := range parts {
		if p.Type == "image_url" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("应保留 2 张合法图，实际 %d 张（共 %d 块）", n, len(parts))
	}
}

// 对话消息过滤：非法 image_url 块被拦、文本块保留；全图被拦时留文字占位。
func TestSanitizeChatMsg(t *testing.T) {
	mk := func(urls ...string) openai.ChatCompletionMessage {
		parts := []openai.ChatMessagePart{{Type: openai.ChatMessagePartTypeText, Text: "判卷正文"}}
		for _, u := range urls {
			parts = append(parts, openai.ChatMessagePart{
				Type: openai.ChatMessagePartTypeImageURL, ImageURL: &openai.ChatMessageImageURL{URL: u},
			})
		}
		return openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, MultiContent: parts}
	}
	got := sanitizeChatMsg(mk(goodPNG, "file:///bad.png"))
	if len(got.MultiContent) != 2 {
		t.Fatalf("坏图应被剥掉（剩文本+好图），实际 %d 块", len(got.MultiContent))
	}
	all := sanitizeChatMsg(func() openai.ChatCompletionMessage {
		parts := make([]openai.ChatMessagePart, 0, 2)
		for _, u := range []string{"file:///a.png", ""} {
			parts = append(parts, openai.ChatMessagePart{
				Type: openai.ChatMessagePartTypeImageURL, ImageURL: &openai.ChatMessageImageURL{URL: u},
			})
		}
		return openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, MultiContent: parts}
	}())
	if len(all.MultiContent) != 1 || !strings.Contains(all.MultiContent[0].Text, "拦截") {
		t.Fatalf("全图被拦应留占位文本，实际 %+v", all.MultiContent)
	}
	clean := mk(goodPNG)
	if &clean.MultiContent[0] == nil {
		t.Fatal("unreachable")
	}
	if len(sanitizeChatMsg(clean).MultiContent) != 2 {
		t.Fatal("全合法消息不应被改动")
	}
}
