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
