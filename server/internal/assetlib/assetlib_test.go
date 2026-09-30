package assetlib

import (
	"os"
	"path/filepath"
	"testing"
)

// 1×1 PNG（最小合法图，meta 尺寸提取走真实解码路径）
var png1x1 = []byte{
	0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D,
	0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4, 0x89, 0x00, 0x00, 0x00,
	0x0A, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4, 0x00, 0x00, 0x00, 0x00, 0x49,
	0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82,
}

func writeTemp(t *testing.T, dir, name string, b []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestIngestSearchResolve(t *testing.T) {
	dir := t.TempDir()
	lib := New(dir)

	src := writeTemp(t, dir, "in-艾尔登法环.png", png1x1)
	e, err := lib.Ingest("image", src, TagsForQuery("艾尔登法环 玛莲妮亚 壁纸"), "艾尔登法环 玛莲妮亚", "fetch", map[string]any{"page": "https://x"})
	if err != nil {
		t.Fatalf("入库: %v", err)
	}
	if e.ID != "img-000001" {
		t.Fatalf("首个 id 应为 img-000001，得到 %s", e.ID)
	}
	if e.File != "media/image/img-000001.png" {
		t.Fatalf("file 路径不对: %s", e.File)
	}
	if e.Meta["w"] != 1 || e.Meta["h"] != 1 {
		t.Fatalf("尺寸提取失败: %v", e.Meta)
	}
	// 源文件保留（拷贝语义；staging 清理由调用方 produce 负责）
	if _, err := os.Stat(src); err != nil {
		t.Fatal("源文件不应被删（staging 清理归调用方）")
	}

	// 同内容幂等：再入库返回同 id
	src2 := writeTemp(t, dir, "again.png", png1x1)
	e2, err := lib.Ingest("image", src2, TagsForQuery("重复图"), "同内容", "fetch", nil)
	if err != nil {
		t.Fatal(err)
	}
	if e2.ID != e.ID {
		t.Fatalf("同 hash 应回已有条目 %s，得到 %s", e.ID, e2.ID)
	}

	// 检索：tag 子串命中
	hits := lib.Search("image", "玛莲妮亚", 10)
	if len(hits) != 1 || hits[0].ID != e.ID {
		t.Fatalf("tag 检索未命中: %+v", hits)
	}
	if got := lib.Search("audio", "", 10); len(got) != 0 {
		t.Fatalf("type 过滤失效: %+v", got)
	}
	// desc 子串命中
	if got := lib.Search("image", "艾尔登法环", 10); len(got) != 1 {
		t.Fatalf("desc 检索未命中: %+v", got)
	}

	// ResolveInto：拷入项目目录 + 幂等
	dest := filepath.Join(dir, "proj", "assets", "img")
	rel, err := lib.ResolveInto(e.ID, dest)
	if err != nil {
		t.Fatal(err)
	}
	if rel != "img-000001.png" {
		t.Fatalf("拷入文件名不对: %s", rel)
	}
	if _, err := os.Stat(filepath.Join(dest, rel)); err != nil {
		t.Fatal("拷入文件缺失")
	}
	if rel2, _ := lib.ResolveInto(e.ID, dest); rel2 != rel {
		t.Fatal("幂等失败")
	}

	// 未知类型拒绝
	if _, err := lib.Ingest("font", src, nil, "", "upload", nil); err == nil {
		t.Fatal("未知 type 应报错")
	}
}

func TestSeedBGM(t *testing.T) {
	dir := t.TempDir()
	aud := filepath.Join(dir, "library", "media", "audio") // New(dataDir) 会拼 library/
	if err := os.MkdirAll(aud, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTemp(t, aud, "calm.mp3", []byte("fake-mp3-bytes"))
	writeTemp(t, aud, "tracks.json", []byte(`[{"name":"calm","file":"calm.mp3","duration":96,"mood":"沉静"}]`))

	lib := New(dir)
	if err := lib.SeedBGM(); err != nil {
		t.Fatal(err)
	}
	if e, ok := lib.Get("aud-calm"); !ok || e.Origin != "builtin" || e.Type != "audio" {
		t.Fatalf("BGM 入库失败: %+v ok=%v", e, ok)
	}
	// 幂等：再跑不重复
	if err := lib.SeedBGM(); err != nil {
		t.Fatal(err)
	}
	if got := lib.Search("audio", "", 0); len(got) != 1 {
		t.Fatalf("SeedBGM 应幂等，得到 %d 条", len(got))
	}
}

func TestTagsForQuery(t *testing.T) {
	got := TagsForQuery("艾尔登法环 玛莲妮亚　壁纸，4k")
	// 排序后为集合比较：4 个分词 + 完整 query，去重排序
	want := map[string]bool{"4k": true, "艾尔登法环": true, "玛莲妮亚": true, "壁纸": true, "艾尔登法环 玛莲妮亚　壁纸，4k": true}
	if len(got) != len(want) {
		t.Fatalf("tags 数量 %d != %d: %v", len(got), len(want), got)
	}
	for _, g := range got {
		if !want[g] {
			t.Fatalf("多出 tag %q", g)
		}
	}
	if TagsForQuery("  ") != nil {
		t.Fatal("空 query 应返回 nil")
	}
}
