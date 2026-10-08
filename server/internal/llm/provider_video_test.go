package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

// video_url 块的序列化形状（智谱格式）：{"type":"video_url","video_url":{"url":…}}，
// 与 image_url/text 混排、视频块在首位——2026-10-08 实测编码端点接受此形状（探针 200，
// 模型同时看懂视频与图片）。这是跨包组包的唯一出口，形状漂移等于全线断粮，测试钉死。
func TestMediaPartsWithVideoMarshalShape(t *testing.T) {
	parts := mediaParts("任务正文",
		[]string{"data:image/png;base64,AAA", "data:image/png;base64,BBB"},
		"https://v.example.com/clip.mp4")
	b, err := json.Marshal(parts)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		`{"type":"video_url","video_url":{"url":"https://v.example.com/clip.mp4"}}`,
		`{"type":"image_url","image_url":{"url":"data:image/png;base64,AAA"}}`,
		`{"type":"image_url","image_url":{"url":"data:image/png;base64,BBB"}}`,
		`{"type":"text","text":"任务正文"}`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("缺块 %q\n%s", want, s)
		}
	}
	if strings.Index(s, "video_url") > strings.Index(s, "image_url") {
		t.Fatalf("视频块应在首位: %s", s)
	}
}

// videoURL 为空 = 纯图路径：不出现 video_url 块（等价老 GenerateJSONVisionNoThink 行为）。
func TestMediaPartsPlainHasNoVideo(t *testing.T) {
	b, _ := json.Marshal(mediaParts("hi", []string{"data:image/png;base64,AAA"}, ""))
	if strings.Contains(string(b), "video_url") {
		t.Fatalf("纯图路径不该有 video_url 块: %s", b)
	}
}
