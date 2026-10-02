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

func TestPickKeyframes(t *testing.T) {
	if got := PickKeyframes(nil, 5); got != nil {
		t.Fatalf("空清单应返回 nil，得到 %v", got)
	}
	names := []string{"f0", "f1", "f2", "f3", "f4", "f5", "f6", "f7", "f8", "f9"}
	// 首尾必含
	if got := PickKeyframes(names, 3); got[0] != "f0" || got[len(got)-1] != "f9" {
		t.Fatalf("首尾必含: %v", got)
	}
	if got := PickKeyframes(names[:1], 5); len(got) != 1 || got[0] != "f0" {
		t.Fatalf("清单不足 n 收敛为 1: %v", got)
	}
	if got := PickKeyframes(names, 1); len(got) != 1 || got[0] != "f0" {
		t.Fatalf("n=1 取首张: %v", got)
	}
	// 均匀且无重复
	seen := map[string]bool{}
	got := PickKeyframes(names, 5)
	for _, name := range got {
		if seen[name] {
			t.Fatalf("重复挑帧: %v", got)
		}
		seen[name] = true
	}
	if len(got) != 5 {
		t.Fatalf("应挑 5 张: %v", got)
	}
}

func TestKeyframePathsFallbackScan(t *testing.T) {
	l := New(t.TempDir())
	// 无条目无媒体 → nil
	if got := l.KeyframePaths("vid-none", 5); got != nil {
		t.Fatalf("无媒体应返回 nil: %v", got)
	}
	dir := l.MediaDir("vid-old")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 老条目：无 Keyframes 登记清单 → 回退扫描目录 frame-*.png 均匀补挑
	for _, name := range []string{"frame-00.png", "frame-01.png", "frame-02.png", "frame-03.png", "video.mp4"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := l.KeyframePaths("vid-old", 3)
	if len(got) != 3 {
		t.Fatalf("回退扫描应挑 3 张: %v", got)
	}
	for _, p := range got {
		if filepath.Base(p) == "video.mp4" {
			t.Fatal("真迹只挑 frame-*.png，不该带上原片")
		}
	}
	if filepath.Base(got[0]) != "frame-00.png" || filepath.Base(got[2]) != "frame-03.png" {
		t.Fatalf("首尾必含: %v", got)
	}
}

func TestKeyframePathsListedNames(t *testing.T) {
	l := New(t.TempDir())
	seed := []Style{{ID: "vid-new", Keyframes: []string{"frame-02.png", "frame-05.png", "frame-99.png"}}}
	dir := l.MediaDir("vid-new")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"frame-00.png", "frame-02.png", "frame-05.png"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := json.Marshal(seed)
	if err := os.WriteFile(filepath.Join(l.dir, "styles.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	// 登记清单优先，失效项（frame-99）跳过，只回存在的
	got := l.KeyframePaths("vid-new", 5)
	if len(got) != 2 || filepath.Base(got[0]) != "frame-02.png" || filepath.Base(got[1]) != "frame-05.png" {
		t.Fatalf("按登记清单取存在文件: %v", got)
	}
}
