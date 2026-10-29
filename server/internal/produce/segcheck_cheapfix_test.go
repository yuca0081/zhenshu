package produce

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"webpage-video-agent/server/internal/pipeline"
)

// TestSegCheckCheapFixInputContract 钉住段级 cheap-fix 的输入契约：两个机械修复器
// 必须吃 check 全量输出。blockingCheckErrors 会剔除 Contrast 整段与所有无 ✗ 的续行
// （「Try rgb…; source …」与「…frames/segNN.html」），恰好是 contrastSuggRe /
// gsapOpacityNoopRe 的命门——喂过滤版永远 0 命中，cheap-fix 形同虚设、白烧 LLM 修复轮
// （2026-10-26 审查发现的段级路径死代码即此：接线误用了 blocking.Error()）。
// segCheck 现行接线用全量 out；若未来「统一口径」改回过滤版，下面两组断言一起指出失配机制。
func TestSegCheckCheapFixInputContract(t *testing.T) {
	out := `
Lint
  ✗ missing_lib: three@0.152.2 未采购
Contrast
  ✗ #seg01-big3 2.63:1 (need 3:1, t=5.946s)
    Try rgb(166,76,8); source compositions/frames/seg01.html
  0 error(s), 7 warning(s), 0 info(s)
Runtime
  ✗ gsap_from_opacity_noop: "#seg01-box" CSS opacity:0 与 gsap.from(opacity:0) 冲突
    compositions/frames/seg01.html #seg01-box t=2.3s
`
	filtered := blockingCheckErrors(out)
	if !strings.Contains(filtered, "missing_lib") {
		t.Fatalf("filtered output should keep non-contrast ✗ lines: %q", filtered)
	}
	if strings.Contains(filtered, "Try rgb") || strings.Contains(filtered, "frames/seg01.html") {
		t.Fatalf("filter keeps continuation lines — fixer contract changed, revisit this test: %q", filtered)
	}

	newProj := func() *pipeline.Project {
		dir := t.TempDir()
		p := pipeline.NewProject(filepath.Dir(dir), filepath.Base(dir))
		path := p.Artifact("compositions/frames/seg01.html")
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, []byte(`<html><head><style>#seg01-big3{color:#333}</style></head><body><div id="seg01-box" style="opacity:0;transform:translateX(10px)">x</div></body></html>`), 0o644)
		return p
	}

	// 全量输出：两个修复器各命中 1 处（segCheck 的现行接线）
	p := newProj()
	if n := applyContrastFixes(p, out); n != 1 {
		t.Fatalf("full output: want 1 contrast fix, got %d", n)
	}
	if n := applyGsapOpacityFixes(p, out); n != 1 {
		t.Fatalf("full output: want 1 opacity fix, got %d", n)
	}

	// 过滤版（blockingCheckErrors 产物）：两个修复器必 0 命中
	p2 := newProj()
	if n := applyContrastFixes(p2, filtered); n != 0 {
		t.Fatalf("filtered output must not feed contrast fixer, got %d", n)
	}
	if n := applyGsapOpacityFixes(p2, filtered); n != 0 {
		t.Fatalf("filtered output must not feed opacity fixer, got %d", n)
	}
}

// TestBlockingCheckErrorsStripsBareContrastLines 钉住对比度摘闸（2026-10-29）：
// checker 0.8.55 把对比度报成 error 且不冠 Contrast 段标题（seg09 六轮误判实测
// 形态），旧版按段标题过滤匹配不到、全判死。现按行特征剥——对比度 ✗ 行及其
// Try/source 续行都不许出现在 blocking 里；Layout 等真 bug 照旧保留。
func TestBlockingCheckErrorsStripsBareContrastLines(t *testing.T) {
	out := `
  ✗ #kicker 2.53:1 (need 3:1, t=16.712s)
    Try rgb(45,49,60); source compositions/frames/seg09.html
  ✗ div > div:nth-of-type(1) > div > div > div:nth-of-type(2) > div:nth-of-type(3) > b 1.96:1 (need 3:1, t=16.712s)
    Try rgb(63,56,80); source compositions/frames/seg09.html
  15 error(s), 0 warning(s), 0 info(s)

Snapshots
  ◇ disabled

◇  Check failed
`
	filtered := blockingCheckErrors(out)
	if strings.Contains(filtered, ":1 (need") || strings.Contains(filtered, "Try rgb") {
		t.Fatalf("bare contrast lines must be stripped (美术表达不判死): %q", filtered)
	}

	mixed := `
  ✗ #h1 2.05:1 (need 3:1, t=12.78s)
    Try rgb(80,74,89); source compositions/frames/seg09.html
Layout
  ✗ Two text blocks overlap and may render unreadable.
Motion
  ✗ gsap tween snaps to integer device pixels
`
	filtered2 := blockingCheckErrors(mixed)
	if !strings.Contains(filtered2, "integer device pixels") {
		t.Fatalf("motion errors (能不能跑) must survive the strip: %q", filtered2)
	}
	if strings.Contains(filtered2, ":1 (need") || strings.Contains(filtered2, "overlap") {
		t.Fatalf("contrast/layout (美术) lines must not survive: %q", filtered2)
	}
}

// 2026-10-29 seg09 实测：6 条版面 content_overlap 独占（对比度 19/19 全过），
// 旧放行条件「out 里有对比度行」不成立 → TailLines 兜底当 blocking → 白烧 3 轮
// 重写整链失败。摘闸放行的判据改为「剥光后无真 blocking + 有分项骨架」。
func TestLayoutOnlyCheckOutputPassesAfterStrip(t *testing.T) {
	out := `
Lint
  ◇ 0 errors, 0 warning(s), 0 info(s)

Runtime
  ◇ 0 errors, 0 warnings

Layout
  ✗ t=3.98-6.72s (23 samples) content_overlap #seg09-labL inside #seg09-subL "收拢腿 · 转得快" — Two text blocks overlap and may render unreadable.
  ✗ t=5.11-6.72s (14 samples) content_overlap #seg09-labL inside #seg09-labR "收拢腿 · 转得快" — Two text blocks overlap and may render unreadable.
  6 error(s), 0 warning(s), 0 info(s)

Motion
  ◇ 0 errors, 0 warnings

Contrast
  ◇ 19/19 text checks pass WCAG AA

◇  Check failed
`
	if blockingCheckErrors(out) != "" {
		t.Fatalf("layout-only output must strip to empty blocking: %q", blockingCheckErrors(out))
	}
	if !checkRanSections(out) {
		t.Fatal("layout-only output has section skeleton, checkRanSections must be true")
	}

	crash := `node:internal/process/promises: A callbacked function timed out
  ✗ Cannot access 'a' before initialization
    at index.html:12:3`
	if checkRanSections(crash) {
		t.Fatal("crash output has no section skeleton, checkRanSections must be false")
	}
	if blockingCheckErrors(crash) == "" {
		t.Fatal("crash runtime error is 能不能跑, must stay blocking")
	}
}
