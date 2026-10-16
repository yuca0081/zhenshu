package produce

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/pipeline"
)

// 样张源码注入（文字样式权威规范）：有确认样张 → 注入 HTML 源码与"样式照抄/
// 内容按画面提示"纪律；无样张/无文件 → 静默空串。
func TestSampleStyleNote(t *testing.T) {
	dir := t.TempDir()
	p := &pipeline.Project{ID: "t", Dir: dir}
	if sampleStyleNote(p) != "" {
		t.Fatalf("无 style_samples.json 应静默空串")
	}
	ss := &contract.StyleSamples{
		Direction: "测试",
		Samples: []contract.Sample{
			{Tag: "样张 A", HTML: `<template><style>#t{color:#F2E7CF;}</style><div>齐马蓝</div></template>`},
			{Tag: "样张 B", HTML: ""},
		},
	}
	b, _ := json.Marshal(ss)
	if err := os.MkdirAll(p.Artifact("style"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Artifact("style/style_samples.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	note := sampleStyleNote(p)
	for _, want := range []string{
		"文字样式的权威规范",
		"样式照它，内容按本段画面提示",
		"样张 0（样张 A）",
		"#F2E7CF",
		"只搬样式不搬内容",
	} {
		if !strings.Contains(note, want) {
			t.Fatalf("样张源码块缺 %q\n---\n%s", want, note)
		}
	}
	if strings.Contains(note, "样张 1") {
		t.Fatalf("空 HTML 样张不应出现:\n%s", note)
	}
}
