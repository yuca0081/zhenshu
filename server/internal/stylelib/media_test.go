package stylelib

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSnapshotMediaLinksAndSkipsExisting(t *testing.T) {
	l := New(t.TempDir())
	src := t.TempDir()
	mustWrite := func(name, body string) {
		if err := os.WriteFile(filepath.Join(src, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("video.mp4", "fakevideo")
	mustWrite("frame-0.8.png", "png0")
	mustWrite("frame-2.5.png", "png1")
	mustWrite("ref.json", `{}`) // 非媒体文件，不应快照

	if err := l.SnapshotMedia("vid-x", src); err != nil {
		t.Fatalf("SnapshotMedia: %v", err)
	}
	dir := l.MediaDir("vid-x")
	for _, name := range []string{"video.mp4", "frame-0.8.png", "frame-2.5.png"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(b) == "" {
			t.Fatalf("%s 未快照: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "ref.json")); err == nil {
		t.Fatal("ref.json 不应被快照")
	}
	// 幂等：重跑不改写已有文件
	if err := os.WriteFile(filepath.Join(dir, "video.mp4"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := l.SnapshotMedia("vid-x", src); err != nil {
		t.Fatalf("重跑: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "video.mp4"))
	if string(b) != "v2" {
		t.Fatal("重跑覆盖了已有快照")
	}
}

func TestSnapshotMediaMissingSource(t *testing.T) {
	l := New(t.TempDir())
	if err := l.SnapshotMedia("vid-x", filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("源目录缺失应报错")
	}
}

func TestBackfillMediaSkipsMissingAndFillsExisting(t *testing.T) {
	l := New(t.TempDir())
	references := t.TempDir()
	// 造两条已入库的 video 来源条目：一条有原片，一条没有
	dir := l.dir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	seed := []Style{
		{ID: "vid-have", Name: "有原片", Direction: "有原片", OriginProject: "video:ref-have", Published: true, CreatedAt: now},
		{ID: "vid-gone", Name: "原片已删", Direction: "原片已删", OriginProject: "video:ref-gone", Published: true, CreatedAt: now},
		{ID: "sp-draft", Name: "草稿不回填", Direction: "草稿不回填", OriginProject: "video:ref-draft", Published: false, CreatedAt: now},
	}
	b, _ := json.Marshal(seed)
	if err := os.WriteFile(filepath.Join(dir, "styles.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	refDir := filepath.Join(references, "ref-have")
	if err := os.MkdirAll(refDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(refDir, "video.mp4"), []byte("v"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(refDir, "frame-1.0.png"), []byte("p"), 0o644); err != nil {
		t.Fatal(err)
	}

	l.BackfillMedia(references)

	if _, err := os.Stat(filepath.Join(l.MediaDir("vid-have"), "video.mp4")); err != nil {
		t.Fatal("有原片的条目未回填:", err)
	}
	if _, err := os.Stat(filepath.Join(l.MediaDir("vid-have"), "frame-1.0.png")); err != nil {
		t.Fatal("抽帧未回填")
	}
	if _, err := os.Stat(l.MediaDir("vid-gone")); err == nil {
		t.Fatal("原片已删的条目不应建媒体目录")
	}
	if _, err := os.Stat(l.MediaDir("sp-draft")); err == nil {
		t.Fatal("草稿条目不应回填")
	}
}
