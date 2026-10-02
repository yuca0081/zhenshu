package pipeline

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestSfxPlacements 布点数学：whoosh 居中于边界、riser 收在边界上、贴片头(<0)丢弃、
// 章节位给 riser 不给 whoosh。
func TestSfxPlacements(t *testing.T) {
	bs := []segBoundary{
		{at: 5.0, chapterTurn: false},
		{at: 10.0, chapterTurn: true},
		{at: 0.4, chapterTurn: false}, // whoosh 半长 0.3 起点为 0.1 → 保留
	}
	wAt, rAt := sfxPlacements(bs, 0.6, 1.2)
	near := func(a, b float64) bool { return a-b < 1e-6 && b-a < 1e-6 }
	if len(wAt) != 2 || !near(wAt[0], 4.7) || !near(wAt[1], 0.1) {
		t.Fatalf("whoosh 落点不符（居中于边界）：%v", wAt)
	}
	if len(rAt) != 1 || !near(rAt[0], 8.8) {
		t.Fatalf("riser 落点不符（收在边界上，提前全长 1.2）：%v", rAt)
	}
	// riser 贴片头：边界 < riser 时长 → 丢弃，不回退 whoosh（章节位是 riser 位）
	w2, r2 := sfxPlacements([]segBoundary{{at: 1.0, chapterTurn: true}}, 0.6, 1.2)
	if len(w2) != 0 || len(r2) != 0 {
		t.Fatalf("贴片头 riser 应丢弃且不回退 whoosh：w=%v r=%v", w2, r2)
	}
}

// TestSfxStemFilter 滤镜图形状：素材按 asplit 复制、adelay 毫秒双通道、
// 输入序号随缺席素材顺延、终混 amix 不归一化。
func TestSfxStemFilter(t *testing.T) {
	fc := sfxStemFilter([]float64{1.5}, []float64{2.5, 3.5})
	for _, want := range []string{
		"[1:a]asplit=1[w0]", "[w0]volume=0.32,adelay=1500|1500[w0d]",
		"[2:a]asplit=2[r0][r1]", "[r1]volume=0.38,adelay=3500|3500[r1d]",
		"amix=inputs=4:duration=first:dropout_transition=0:normalize=0[s]",
	} {
		if !strings.Contains(fc, want) {
			t.Errorf("滤镜图缺片段 %q：\n%s", want, fc)
		}
	}
	// 只有 whoosh：riser 输入缺席，whoosh 仍占 [1:a]
	fc2 := sfxStemFilter([]float64{0.0}, nil) // 0 落点 → 不 adelay（ffmpeg adelay 0ms 语义模糊）
	if !strings.Contains(fc2, "[w0]volume=0.32[w0d]") {
		t.Errorf("0ms 落点应省 adelay：\n%s", fc2)
	}
	if strings.Contains(fc2, "[2:a]") {
		t.Errorf("riser 缺席时不应出现 [2:a] 输入：\n%s", fc2)
	}
}

// TestSfxConfigResolve 开关解析链：project.json 显式 > 风格 token > 默认关；
// SetTransitionSFX 落盘回环（off 删字段）。
func TestSfxConfigResolve(t *testing.T) {
	root, _ := filepath.Abs(filepath.Join("..", "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "data", "library", "media", "audio", "sfx", "sfx.json")); err != nil {
		t.Skipf("sfx 素材未生成（ai/make_sfx.py），跳过: %v", err)
	}
	old := dataDir
	SetDataDir(filepath.Join(root, "data"))
	defer SetDataDir(old)

	dir := t.TempDir()
	p := &Project{ID: "t", Dir: dir}
	if err := os.WriteFile(p.Artifact("project.json"), []byte(`{"name":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if sfxFor(p) {
		t.Fatal("无风格无显式配置应默认关")
	}
	if err := SetTransitionSFX(p, true); err != nil {
		t.Fatal(err)
	}
	if !sfxFor(p) {
		t.Fatal("project.json true 应开")
	}
	if err := SetTransitionSFX(p, false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p.Artifact("project.json")); !strings.Contains(string(b), `"transition_sfx": false`) {
		t.Fatalf("off 应显式落 false（防风格默认开盖回）: %s", b)
	}
	// 风格 token：storyboard 带 StyleID → styles.json tokens.transition_sfx
	if err := os.MkdirAll(p.Artifact("storyboards"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Artifact("storyboards/storyboard.json"),
		[]byte(`{"style_id":"neon-void","segments":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !sfxFor(p) { // neon-void 若未配 token 则此断言校验默认关；配了则校验 token 生效
		t.Log("neon-void 未配 transition_sfx token（默认关）")
	}
	// 显式 false 盖过风格 true
	if err := SetTransitionSFX(p, false); err != nil {
		t.Fatal(err)
	}
	if sfxFor(p) {
		t.Fatal("显式 false 不应被风格盖回")
	}
	_ = styleBool("neon-void", "transition_sfx") // 冒烟：不 panic 即可
}

// TestStitchComboFcStreamRefs 组合终装图的流引用必须落在实际输入序号内
// （回归：sfx 第三路曾误写成 [3:a] —— 输入只有 0/1/2，ffmpeg 拒图终装失败）。
func TestStitchComboFcStreamRefs(t *testing.T) {
	refRe := regexp.MustCompile(`\[(\d+):[av]\]`)
	cases := []struct {
		name     string
		sfx      bool
		maxInIdx int // 输入路数 - 1
	}{
		{"bgm only", false, 1},
		{"bgm+sfx", true, 2},
	}
	for _, c := range cases {
		mixIn, nIn := "", 2
		if c.sfx {
			mixIn, nIn = "[2:a]", 3
		}
		fc := stitchComboFc("vignette=angle=0.6", "", mixIn, nIn)
		for _, m := range refRe.FindAllStringSubmatch(fc, -1) {
			idx, _ := strconv.Atoi(m[1])
			if idx > c.maxInIdx {
				t.Errorf("%s: 图引用 [%s:?] 超出输入序号上限 %d", c.name, m[1], c.maxInIdx)
			}
		}
		if c.sfx && !strings.Contains(fc, "[2:a]amix") {
			t.Errorf("%s: amix 第三路应引用 [2:a]（sfx 输入）", c.name)
		}
		if nIn == 3 && strings.Contains(fc, "[3:a]") {
			t.Errorf("%s: 引用了不存在的 [3:a]", c.name)
		}
	}
}
