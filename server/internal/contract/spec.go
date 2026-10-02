package contract

// v1 语义 spec（CompSpec/SpecElement/ValidateSpec/SanitizeSpec）已随 render_spec.py
// 一并拆除（commit e483877）：v2 中间层 = 模型直写场景 HTML + ai/scene_lint.py 契约
// 门禁（见 docs/contract.md）。本文件只保留两件仍在服役的契约件：
// 画幅几何 Canvas（produce 场景注入/布局口径）与 scene 确定性禁令（方法库守卫测试）。

import "regexp"

// ── 画幅（默认 9:16、可切 16:9，plan.md §7 M1）────────────────────

// Canvas 画幅几何：尺寸 + 安全区 + 字幕带顶（元素禁放线）。
type Canvas struct {
	Aspect string  // "16:9" | "9:16"
	W, H   float64 // 像素
	// 安全区（元素占位必须整体落在其中）
	X0, Y0, X1, Y1 float64
	CapTop         float64 // 字幕带顶：y ≥ CapTop 禁放
}

func CanvasFor(aspect string) Canvas {
	if aspect == "9:16" {
		return Canvas{Aspect: "9:16", W: 1080, H: 1920,
			X0: 60, Y0: 60, X1: 1020, Y1: 1550, CapTop: 1594}
	}
	return Canvas{Aspect: "16:9", W: 1920, H: 1080,
		X0: 60, Y0: 60, X1: 1860, Y1: 870, CapTop: 896}
}

// ── scene 确定性禁令（P1-1）───────────────────────────────────────

// scene 专用防呆清单：Code 是 __draw(t, ctx, W, H, rng) 函数体，确定性是硬约束
// （同 t 必须同像素，回放门禁两轮截帧像素比对，见 produce.sceneReplayCheck）。
// 禁一切自管时间/真随机/出网/DOM 查询；Math 三角/幂/取整是数学绘制刚需，放行。
// ai/scene_lint.py 对模型直写的场景代码同规则再拦一道。
var sceneCodeBan = []string{
	`\bMath\.random\b`, `\bDate\b`, `requestAnimationFrame`, `setTimeout`, `setInterval`,
	`\bfetch\s*\(`, `XMLHttpRequest`, `WebSocket`, `\beval\s*\(`, `\bFunction\s*\(`,
	`\bwindow\b`, `\bdocument\b`, `\blocation\b`, `\bnavigator\b`, `\blocalStorage\b`,
	`\bpostMessage\b`, `\bWorker\b`, `\bimport\b`, `https?:`,
}
var sceneCodeBanRe = compileBan(sceneCodeBan)

// SceneCodeViolation scene code 禁用模式检查（返回命中的首个禁用模式，干净返回 ""）。
// 方法库守卫测试（methods_test）用它把关 data/library/methods 的 scene 参考实现。
func SceneCodeViolation(code string) string {
	for _, re := range sceneCodeBanRe {
		if m := re.FindString(code); m != "" {
			return m
		}
	}
	return ""
}

func compileBan(pats []string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(pats))
	for _, p := range pats {
		out = append(out, regexp.MustCompile("(?i)"+p))
	}
	return out
}
