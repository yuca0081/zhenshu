package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBGMSetAndResolve SetBGM 写字段 → bgmFor 解析回环；非法曲名/关掉/缺曲库文件各分支。
func TestBGMSetAndResolve(t *testing.T) {
	// 仓库根：包目录 server/internal/pipeline → 上三级；曲库真实存在才测
	root, _ := filepath.Abs(filepath.Join("..", "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "data", "library", "media", "audio", "tracks.json")); err != nil {
		t.Skipf("曲库未生成（ai/make_bgm.py），跳过: %v", err)
	}
	old := dataDir
	SetDataDir(filepath.Join(root, "data"))
	defer SetRootDir(old)

	dir := t.TempDir()
	p := &Project{ID: "t", Dir: dir}
	if err := os.WriteFile(p.Artifact("project.json"), []byte(`{"name":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// 非法曲名拒绝
	if err := SetBGM(p, "bogus"); err == nil {
		t.Fatal("非法曲名应被拒绝")
	}
	// 设置 → 解析到真实文件
	if err := SetBGM(p, "CALM"); err != nil { // 大小写不敏感，落盘统一小写
		t.Fatal(err)
	}
	name, file := bgmFor(p)
	if name != "calm" || file == "" {
		t.Fatalf("bgmFor 应解析到 calm，得到 %q %q", name, file)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("曲库文件不存在: %v", err)
	}
	// off → 写哨兵值（删键会被 pickBGM 当「没配乐」自动填回，2026-10-21 巡检中危#3），
	// 解析侧 bgmFor 照旧回空
	if err := SetBGM(p, "off"); err != nil {
		t.Fatal(err)
	}
	if n, f := bgmFor(p); n != "" || f != "" {
		t.Fatalf("off 后应为空，得到 %q %q", n, f)
	}
	if b, _ := os.ReadFile(p.Artifact("project.json")); !strings.Contains(string(b), `"bgm": "off"`) {
		t.Fatalf("off 应写哨兵值（bgmFor 认识、pickBGM 不推翻）: %s", b)
	}
	// "" 才是真清除（回「未决定」）
	if err := SetBGM(p, ""); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p.Artifact("project.json")); strings.Contains(string(b), "bgm") {
		t.Fatalf("空串清除后不应残留 bgm 字段: %s", b)
	}
}
