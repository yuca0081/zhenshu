package stylelib

import (
	"os"
	"path/filepath"
	"testing"
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
