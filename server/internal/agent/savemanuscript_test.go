package agent

import (
	"context"
	"os"
	"strings"
	"testing"

	"webpage-video-agent/server/internal/pipeline"
	"webpage-video-agent/server/internal/store"
)

type nopStore struct{ store.Store }

func (nopStore) UpdateStatus(string, string) error { return nil }

// save_manuscript：对话粘贴的文稿落 article.txt（唯一真源）、删旧 manuscript.json
// （重放语义），已有分镜回报过期但不硬删——与文稿弹窗保存同一套语义。
func TestSaveManuscriptTool(t *testing.T) {
	dataDir := t.TempDir()
	a := &Agent{DataDir: dataDir, Store: nopStore{}}
	p := pipeline.NewProject(dataDir, "t1")
	for _, dir := range []string{"input", "manuscripts", "storyboards"} {
		if err := os.MkdirAll(p.Artifact(dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(p.Artifact("project.json"), []byte(`{"name":"t"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Artifact("manuscripts/manuscript.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Artifact("storyboards/storyboard.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	tl := findTool("save_manuscript")
	if tl == nil {
		t.Fatal("save_manuscript 工具未注册")
	}
	if got := tl.gate(State{Producing: true}); got == "" {
		t.Fatal("制作中应被门禁拒绝")
	}

	out := tl.run(context.Background(), a, p, `{"content":"这是测试文稿。"}`)
	if !strings.Contains(out, "旧稿") {
		t.Fatalf("已有分镜时应提示过期: %q", out)
	}
	if b, err := os.ReadFile(p.Artifact("input/article.txt")); err != nil || string(b) != "这是测试文稿。" {
		t.Fatalf("article.txt 未落盘: %v %q", err, b)
	}
	if _, err := os.Stat(p.Artifact("manuscripts/manuscript.json")); !os.IsNotExist(err) {
		t.Fatal("旧 manuscript.json 应被删除（重放语义）")
	}
	if _, err := os.Stat(p.Artifact("storyboards/storyboard.json")); err != nil {
		t.Fatal("已有分镜不应被硬删")
	}

	// 无分镜：不再提示过期
	if err := os.Remove(p.Artifact("storyboards/storyboard.json")); err != nil {
		t.Fatal(err)
	}
	if out := tl.run(context.Background(), a, p, `{"content":"第二版。"}`); strings.Contains(out, "旧稿") {
		t.Fatalf("无分镜时不应提示过期: %q", out)
	}
}
