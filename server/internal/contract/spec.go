package contract

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"
)

// ── 合成物语义 spec（M1：LLM 只出语义，渲染由 ai/render_spec.py 确定性完成）──
//
// LLM 产物不信任原则：spec 过 ValidateSpec（边界/重叠/揭示词位/文案长度）才落盘；
// 违规项回喂给模型修复一轮，仍不过则该段失败。

// CompSpec 一段画面的语义布局。
type CompSpec struct {
	Note string `json:"note"`
	// Camera 段级镜头缓推（内容层整体变换，字幕带不动）：
	// zoom_in|zoom_out|pan_left|pan_right|drift；空/none = 静止。
	Camera string `json:"camera,omitempty"`
	// CameraSpec 关键帧镜头（P1-4，可选；与 Camera 并存且优先）：
	// 起止 zoom/pan/rot + easing，替代 5 个微动预设做运镜叙事。
	CameraSpec *CameraSpec `json:"camera_spec,omitempty"`
	Elements   []SpecElement `json:"elements"`
}

// CameraSpec 关键帧镜头：from → to 线性/缓动插值（渲染层 tl tween，seek 确定性）。
type CameraSpec struct {
	From CameraKey `json:"from,omitempty"`
	To   CameraKey `json:"to,omitempty"`
	Ease string    `json:"ease,omitempty"` // power1.inOut（默认）| power2.inOut | none(线性)
}

// CameraKey 单时刻镜头参数（缺省 0/1 = 无该轴运动）。
type CameraKey struct {
	Zoom float64 `json:"zoom,omitempty"` // 缩放 1–1.6（1 = 原大）
	PanX float64 `json:"pan_x,omitempty"` // 水平平移 px（正值画面左移）
	PanY float64 `json:"pan_y,omitempty"` // 垂直平移 px
	Rot  float64 `json:"rot,omitempty"`   // 旋转度（±6°）
}

// ── 画幅（2026-09-23：默认 9:16、可切 16:9，plan.md §7 M1）─────────

// Canvas 画幅几何：尺寸 + 安全区 + 字幕带顶（元素禁放线）。
type Canvas struct {
	Aspect string  // "16:9" | "9:16"
	W, H   float64 // 像素
	// 安全区（元素占位必须整体落在其中）
	X0, Y0, X1, Y1 float64
	CapTop        float64 // 字幕带顶：y ≥ CapTop 禁放
}

func CanvasFor(aspect string) Canvas {
	if aspect == "9:16" {
		return Canvas{Aspect: "9:16", W: 1080, H: 1920,
			X0: 60, Y0: 60, X1: 1020, Y1: 1550, CapTop: 1594}
	}
	return Canvas{Aspect: "16:9", W: 1920, H: 1080,
		X0: 60, Y0: 60, X1: 1860, Y1: 870, CapTop: 896}
}

// SpecElement 元素语义。坐标体系随画幅（CanvasFor），y ≥ CapTop 为字幕带禁放。
type SpecElement struct {
	Kind string `json:"kind"` // title|note|label|big|beam|disc|circle|arrow|icon|chart_bar|chart_line|chart_pie|image|emoji
	X    float64 `json:"x,omitempty"`  // 左上角（note/label/big/beam/icon/chart_*/image/emoji）
	Y    float64 `json:"y,omitempty"`
	X1   float64 `json:"x1,omitempty"` // arrow 起点
	Y1   float64 `json:"y1,omitempty"`
	X2   float64 `json:"x2,omitempty"` // arrow 终点
	Y2   float64 `json:"y2,omitempty"`
	Cx   float64 `json:"cx,omitempty"` // 圆心（disc/circle）
	Cy   float64 `json:"cy,omitempty"`
	R    float64 `json:"r,omitempty"`  // 半径（disc/circle）
	W    float64 `json:"w,omitempty"`  // 宽（beam/chart_*/image）
	H    float64 `json:"h,omitempty"`
	Text string  `json:"text,omitempty"`
	Title string `json:"title,omitempty"` // panel 标题栏（可选）
	Name string  `json:"name,omitempty"`  // icon 名（lucide kebab，本地库）
	Query  string `json:"query,omitempty"`  // image 搜索词/生图画面描述（制作期下载本地化）
	Source string `json:"source,omitempty"` // image 图源：gen=AI 生图（智谱 CogView）/ 空|search=网络搜图
	Src    string `json:"src,omitempty"`    // image 取图结果引用：lib:<素材id>（assetlib catalog）或旧项目相对路径
	Page   string `json:"page,omitempty"`   // image 来源页（版权留档）
	Size float64 `json:"size,omitempty"` // icon 边长
	Values []float64 `json:"values,omitempty"` // 图表数值
	Labels []string  `json:"labels,omitempty"` // 图表标签
	Nodes  []string   `json:"nodes,omitempty"` // timeline 节点文字（2–6 条）
	Rows   [][]string `json:"rows,omitempty"`  // table 行（2–7 行 × 1–4 列）
	N      int        `json:"n,omitempty"`     // strip 方块数（2–8）
	Gap    float64    `json:"gap,omitempty"`   // timeline 节点间距
	CellH  float64    `json:"cell_h,omitempty"` // table 行高
	Bg   string `json:"bg,omitempty"`   // butter|mint|sky|coral|peach|pink|turq|white|ink
	Color string `json:"color,omitempty"`
	Fill string `json:"fill,omitempty"`
	Rot  float64 `json:"rot,omitempty"`
	Fs   float64 `json:"fs,omitempty"`
	Reveal int   `json:"reveal,omitempty"` // 揭示词位（段内词序号，0 起）
	// custom 自由元素（表达力逃生舱，每段 ≤3 个）：容器内直出 HTML/CSS/GSAP。
	Html string `json:"html,omitempty"` // 容器内 HTML（禁 script/外链/内联事件）
	Css  string `json:"css,omitempty"`  // 自动 scope 到容器；@keyframes 自动改名
	Js   string `json:"js,omitempty"`   // GSAP 片段：T=揭示秒、ID=容器选择器、tl=时间线
	// 动效词表 v2：anim 入场覆盖（pop|fade|rise|slide|wipe|blur|chars|none），
	// exit 退场词位（0 = 不退场，讲完该词即退）。
	Anim string `json:"anim,omitempty"`
	Exit int    `json:"exit,omitempty"`
	// role="bg" 全幅背景层（仅 image/custom）：满画幅垫底，豁免安全区/重叠检查；
	// 渲染层稳定排序保证它在所有内容元素之下。
	Role string `json:"role,omitempty"`
	// image 背景的氛围参数：dim 压暗程度 0–1（默认 0.55，纸色渐变遮罩浓度）、blur 毛玻璃 0–12px。
	Dim  float64 `json:"dim,omitempty"`
	Blur float64 `json:"blur,omitempty"`
	// scene 确定性逐帧画布（P1-1）：Code = __draw(t, ctx, W, H, rng) 函数体
	//（同 t 必须同像素；随机只用注入的 seeded rng，禁 Math.random/Date/rAF）。
	// 全画幅绘制层垫底，豁免安全区/重叠/元素数；Seed 注入 mulberry32（缺省 42）。
	Code string `json:"code,omitempty"`
	Seed int    `json:"seed,omitempty"`
	// formula 数学公式（P1-2）：KaTeX 离线排版，Tex = LaTeX 源码（≤500 字）。
	Tex string `json:"tex,omitempty"`
}

var specKinds = map[string]bool{
	"title": true, "note": true, "label": true, "big": true,
	"beam": true, "disc": true, "circle": true, "arrow": true,
	"icon": true, "chart_bar": true, "chart_line": true, "chart_pie": true,
	"chart_donut": true,
	"image": true, "emoji": true, "panel": true, "chip": true,
	"zone": true, "timeline": true, "bracket": true, "strip": true,
	"barrow": true, "table": true,
	"quote": true, "checklist": true, "stat": true,
	"custom": true,
	"scene": true, "formula": true,
}

// bbox 保守估计元素占位（供边界与重叠检查）。
func (e *SpecElement) bbox(cv Canvas) (x0, y0, w, h float64, ok bool) {
	runeLen := utf8.RuneCountInString(e.Text)
	fs := e.Fs
	switch e.Kind {
	case "title":
		if fs == 0 {
			fs = 84
		}
		w = float64(runeLen)*fs*1.18 + 40
		if max := cv.W - 2*cv.X0; w > max {
			w = max
		}
		return (cv.W - w) / 2, e.Y, w, fs * 1.4, true
	case "note":
		if fs == 0 {
			fs = 40
		}
		// 保守估计：楷体全角+边框内衬+阴影；宁可误报重叠也不放进 check 门禁
		return e.X, e.Y, float64(runeLen)*fs*1.15 + 96, fs + 52, true
	case "chip":
		if fs == 0 {
			fs = 40
		}
		return e.X, e.Y, float64(runeLen)*fs*1.12 + 70, fs*1.6, true
	case "panel":
		if e.W == 0 {
			e.W = 560
		}
		if e.H == 0 {
			e.H = 320
		}
		// 平移浅影余量
		return e.X, e.Y, e.W + 12, e.H + 12, true
	case "label":
		if fs == 0 {
			fs = 38
		}
		return e.X, e.Y, float64(runeLen)*fs*1.1 + 40, fs * 1.5, true
	case "big":
		if fs == 0 {
			fs = 110
		}
		return e.X, e.Y, float64(runeLen)*fs*0.85, fs * 1.2, true
	case "beam":
		if e.H == 0 {
			e.H = 22
		}
		return e.X, e.Y, e.W, e.H, true
	case "icon":
		if e.Size == 0 {
			e.Size = 120
		}
		return e.X, e.Y, e.Size, e.Size, true
	case "image":
		if e.W == 0 {
			e.W = 520
		}
		if e.H == 0 {
			e.H = 360
		}
		// 拍立得白框余量（border+padding，防重叠漏检）
		return e.X - 15, e.Y - 15, e.W + 30, e.H + 38, true
	case "emoji":
		if e.Fs == 0 {
			e.Fs = 140
		}
		return e.X, e.Y, e.Fs, e.Fs * 1.1, true
	case "chart_bar", "chart_line":
		if e.W == 0 {
			e.W = 560
		}
		if e.H == 0 {
			e.H = 360
		}
		return e.X, e.Y, e.W, e.H, true
	case "chart_pie":
		if e.W == 0 {
			e.W = 360
		}
		if e.H == 0 {
			e.H = 360
		}
		return e.X, e.Y, e.W, e.H, true
	case "chart_donut":
		if e.W == 0 {
			e.W = 460
		}
		if e.H == 0 {
			e.H = 360
		}
		return e.X, e.Y, e.W, e.H, true
	case "quote":
		if e.W == 0 {
			e.W = 1240
		}
		if fs == 0 {
			fs = 64
		}
		cap := math.Max(8, math.Floor(e.W/(fs*1.02)))
		lines := math.Ceil(float64(runeLen) / cap)
		return e.X, e.Y, e.W, lines*fs*1.6+130, true
	case "checklist":
		if e.Gap == 0 {
			e.Gap = 96
		}
		if fs == 0 {
			fs = 40
		}
		maxLen := 0
		for _, s := range e.Nodes {
			if l := utf8.RuneCountInString(s); l > maxLen {
				maxLen = l
			}
		}
		w := 70 + float64(maxLen)*fs*1.1
		h := float64(len(e.Nodes)-1)*e.Gap + 52
		return e.X, e.Y, w, h, true
	case "stat":
		if e.W == 0 {
			e.W = 420
		}
		if fs == 0 {
			fs = 96
		}
		lab := math.Max(28, fs*0.32)
		return e.X, e.Y, e.W, fs*1.7+lab*1.5, true
	case "disc", "circle":
		return e.Cx - e.R, e.Cy - e.R, 2 * e.R, 2 * e.R, true
	case "zone":
		if e.W == 0 {
			e.W = 800
		}
		if e.H == 0 {
			e.H = 400
		}
		return e.X, e.Y, e.W, e.H, true
	case "timeline":
		if e.Gap == 0 {
			e.Gap = 110
		}
		if e.Fs == 0 {
			e.Fs = 40
		}
		maxLen := 0
		for _, s := range e.Nodes {
			if l := utf8.RuneCountInString(s); l > maxLen {
				maxLen = l
			}
		}
		r := 22.0
		w := 60 + float64(maxLen)*e.Fs*1.1
		h := float64(len(e.Nodes)-1)*e.Gap + 2*r
		return e.X - r - 8, e.Y - 8, w, h + 16, true
	case "bracket":
		if e.H == 0 {
			e.H = 300
		}
		fs := e.Fs
		if fs == 0 {
			fs = 44
		}
		// 竖排文字单列宽 ≈ fs*1.3；括号字形宽 ≈ h*0.5
		return e.X, e.Y, e.H*0.5+fs*1.3+20, e.H, true
	case "strip":
		n := e.N
		if n == 0 {
			n = 5
		}
		size := e.Size
		if size == 0 {
			size = 44
		}
		w := float64(n)*(size+14) + 40
		if e.Text != "" {
			w += float64(utf8.RuneCountInString(e.Text))*36 + 50
		}
		return e.X, e.Y, w, size + 6, true
	case "barrow":
		w := e.W
		if w == 0 {
			w = 260
		}
		h := e.H
		if h == 0 {
			h = 90
		}
		rad := e.Rot * math.Pi / 180
		wr := math.Abs(w*math.Cos(rad)) + math.Abs(h*math.Sin(rad))
		hr := math.Abs(w*math.Sin(rad)) + math.Abs(h*math.Cos(rad))
		return e.X + w/2 - wr/2, e.Y + h/2 - hr/2, wr, hr, true
	case "table":
		if e.W == 0 {
			e.W = 520
		}
		cellH := e.CellH
		if cellH == 0 {
			cellH = 76
		}
		n := len(e.Rows)
		if n == 0 {
			n = 2
		}
		return e.X, e.Y, e.W, float64(n)*cellH + float64(n-1)*10, true
	case "arrow":
		x, y := e.X1, e.Y1
		if e.X2 < x {
			x = e.X2
		}
		if e.Y2 < y {
			y = e.Y2
		}
		w, h := abs(e.X2-e.X1), abs(e.Y2-e.Y1)
		if w < 68 {
			w = 68
		}
		if h < 68 {
			h = 68
		}
		return x, y, w, h, true
	case "custom":
		w := e.W
		if w == 0 {
			w = 560
		}
		h := e.H
		if h == 0 {
			h = 320
		}
		return e.X, e.Y, w, h, true
	case "formula":
		// KaTeX 公式：宽按 tex 长度保守估（下标/根号会横向膨胀），高按两行分数预估。
		fs := e.Fs
		if fs == 0 {
			fs = 48
		}
		w := float64(utf8.RuneCountInString(e.Tex)) * fs * 0.62
		if w < 240 {
			w = 240
		}
		if mx := cv.W - 2*cv.X0; w > mx {
			w = mx
		}
		h := e.H
		if h == 0 {
			h = fs * 2.2
		}
		return e.X, e.Y, w, h, true
	case "scene":
		// 全画幅绘制层：无占位（豁免安全区/重叠/元素数；见 ValidateSpec scene 分支）。
		return 0, 0, 0, 0, false
	}
	return 0, 0, 0, 0, false
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// ValidateSpec wordCount = 该段词级时间戳词数（reveal 上界）。
// cv = 画幅几何（安全区与字幕带随 9:16 / 16:9 变化）。
// 返回违规列表（空 = 通过）；违规项回喂模型修复。
// 箭头不参与重叠判定（它的职责就是连接/跨越其他元素）。
func ValidateSpec(s *CompSpec, wordCount int, cv Canvas) []string {
	var errs []string
	hasScene := false
	for i := range s.Elements {
		if s.Elements[i].Kind == "scene" {
			hasScene = true
		}
	}
	// scene 段是「全画幅绘制 + 少量叠加元素」的构图，元素下限放宽到 1（上限不变）。
	if n := len(s.Elements); (hasScene && (n < 1 || n > 12)) || (!hasScene && (n < 3 || n > 12)) {
		errs = append(errs, fmt.Sprintf("元素数 %d 不在范围（%s，尽量 4–8）", n,
			map[bool]string{true: "scene 段 1–12", false: "3–12"}[hasScene]))
	}
	switch s.Camera {
	case "", "none", "zoom_in", "zoom_out", "pan_left", "pan_right", "drift":
	default:
		errs = append(errs, fmt.Sprintf("camera %q 不在词表（zoom_in/zoom_out/pan_left/pan_right/drift）", s.Camera))
	}
	errs = append(errs, validateCameraSpec(s.CameraSpec)...)
	sceneCount := 0
	customCount, imgBg, customBg := 0, 0, 0
	type box struct {
		i int
		b specBox
	}
	var boxes []box
	for i := range s.Elements {
		e := &s.Elements[i]
		tag := fmt.Sprintf("元素%d(%s)", i+1, e.Kind)
		if !specKinds[e.Kind] {
			errs = append(errs, tag+": 未知 kind")
			continue
		}
		// role=bg 全幅背景层：仅 image/custom，逐类最多 1 个；豁免安全区/重叠
		isBg := false
		switch e.Role {
		case "":
		case "bg":
			if e.Kind != "image" && e.Kind != "custom" {
				errs = append(errs, tag+": role=\"bg\" 仅 image/custom 支持（其余 kind 本身就是内容层）")
			} else {
				isBg = true
				if e.Kind == "image" {
					imgBg++
					if imgBg > 1 {
						errs = append(errs, "背景图元素最多 1 个（多张会互相盖住）")
					}
				} else {
					customBg++
					if customBg > 1 {
						errs = append(errs, "custom 背景元素最多 1 个")
					}
				}
			}
		default:
			errs = append(errs, fmt.Sprintf("%s: role %q 不在词表（只支持 bg=全幅背景垫底）", tag, e.Role))
		}
		if e.Kind == "custom" {
			customCount++
		}
		// scene：全画幅确定性画布（P1-1）。每段 ≤1；专规校验后直接跳过
		// 安全区/重叠/重叠收集（绘制层天然满画幅垫底，卡片类检查全是误伤）。
		if e.Kind == "scene" {
			sceneCount++
			if sceneCount > 1 {
				errs = append(errs, "scene 元素最多 1 个（全画幅画布，多个会互相盖住）")
			}
			errs = append(errs, validateScene(e, tag)...)
			if e.Anim != "" || e.Exit != 0 || e.Reveal != 0 {
				errs = append(errs, tag+": scene 不支持 anim/exit/reveal（绘制节奏由 __draw 的 t 自控，入场淡入由渲染层统一加）")
			}
			continue
		}
		// formula：KaTeX 公式（P1-2），内容层元素照常参与布局检查。
		if e.Kind == "formula" {
			errs = append(errs, validateFormula(e, tag)...)
		}
		if e.Reveal < 0 || (wordCount > 0 && e.Reveal >= wordCount) {
			errs = append(errs, fmt.Sprintf("%s: reveal=%d 超出词数 %d", tag, e.Reveal, wordCount))
		}
		if e.Exit != 0 {
			if e.Exit < 0 || (wordCount > 0 && e.Exit >= wordCount) {
				errs = append(errs, fmt.Sprintf("%s: exit=%d 超出词数 %d", tag, e.Exit, wordCount))
			} else if e.Exit <= e.Reveal {
				errs = append(errs, fmt.Sprintf("%s: exit=%d 须晚于 reveal=%d（先入场再退场）", tag, e.Exit, e.Reveal))
			}
		}
		if e.Anim != "" {
			switch e.Anim {
			case "pop", "fade", "rise", "slide", "wipe", "blur", "chars", "none":
			default:
				errs = append(errs, fmt.Sprintf("%s: anim %q 不在动效词表（pop/fade/rise/slide/wipe/blur/chars/none）", tag, e.Anim))
			}
			if e.Anim == "chars" && e.Kind != "title" && e.Kind != "big" {
				errs = append(errs, tag+": anim=chars 仅 title/big 支持（其余 kind 无逐字 span）")
			}
			if e.Anim != "" && e.Kind != "custom" && !simpleAnimKind(e.Kind) {
				errs = append(errs, tag+": anim 入场覆盖不支持组合类元素（子元素有专属揭示脚本）")
			}
		}
		if e.Kind == "custom" {
			errs = append(errs, validateCustom(e, tag)...)
		}
		needText := e.Kind == "title" || e.Kind == "note" || e.Kind == "label" || e.Kind == "big" ||
			e.Kind == "chip" || e.Kind == "quote" || e.Kind == "stat"
		if needText && e.Text == "" {
			errs = append(errs, tag+": 缺 text")
		}
		lim := map[string]int{"title": 14, "note": 14, "label": 16, "big": 9, "chip": 12,
			"quote": 22, "stat": 8}
		if lim[e.Kind] > 0 && utf8.RuneCountInString(e.Text) > lim[e.Kind] {
			errs = append(errs, fmt.Sprintf("%s: 文案「%s」超长（≤%d 字）", tag, e.Text, lim[e.Kind]))
		}
		if e.Kind == "panel" {
			if e.Text == "" && e.Title == "" {
				errs = append(errs, tag+": 缺 title/text（至少给一个）")
			}
			if n := utf8.RuneCountInString(e.Title); n > 12 {
				errs = append(errs, fmt.Sprintf("%s: title「%s」超长（≤12 字）", tag, e.Title))
			}
			if n := utf8.RuneCountInString(e.Text); n > 60 {
				errs = append(errs, fmt.Sprintf("%s: text 超长（≤60 字，当前 %d）", tag, n))
			}
			if e.W != 0 && (e.W < 240 || e.W > 1400) {
				errs = append(errs, fmt.Sprintf("%s: w %v 超范围 240–1400", tag, e.W))
			}
			if e.H != 0 && (e.H < 160 || e.H > 900) {
				errs = append(errs, fmt.Sprintf("%s: h %v 超范围 160–900", tag, e.H))
			}
		}
		if e.Kind == "disc" || e.Kind == "circle" {
			if e.R < 18 || e.R > 220 {
				errs = append(errs, fmt.Sprintf("%s: 半径 %v 超范围 18–220", tag, e.R))
			}
		}
		if e.Kind == "icon" {
			if e.Name == "" {
				errs = append(errs, tag+": 缺 name（图标名）")
			}
			if e.Size != 0 && (e.Size < 48 || e.Size > 400) {
				errs = append(errs, fmt.Sprintf("%s: size %v 超范围 48–400", tag, e.Size))
			}
		}
		if e.Kind == "image" {
			if e.Query == "" {
				errs = append(errs, tag+": 缺 query（图片搜索词）")
			} else if e.Source == "gen" {
				// 生图：query 是一句完整画面描述，允许更长
				if n := utf8.RuneCountInString(e.Query); n > 60 {
					errs = append(errs, fmt.Sprintf("%s: query「%s」超长（gen ≤60 字）", tag, e.Query))
				}
			} else if n := utf8.RuneCountInString(e.Query); n > 12 {
				errs = append(errs, fmt.Sprintf("%s: query「%s」超长（≤12 字；AI 生图请加 source=\"gen\"）", tag, e.Query))
			}
			if e.Source != "" && e.Source != "gen" && e.Source != "search" {
				errs = append(errs, fmt.Sprintf("%s: source 只能是 gen 或 search", tag))
			}
			if e.Role == "bg" {
				// 背景层：满画幅由渲染层决定，w/h/dim/blur 只做范围检查
				if e.Dim < 0 || e.Dim > 1 {
					errs = append(errs, fmt.Sprintf("%s: dim %v 超范围 0–1（压暗程度）", tag, e.Dim))
				}
				if e.Blur < 0 || e.Blur > 12 {
					errs = append(errs, fmt.Sprintf("%s: blur %v 超范围 0–12px", tag, e.Blur))
				}
			} else {
				if e.W != 0 && (e.W < 200 || e.W > 1000) {
					errs = append(errs, fmt.Sprintf("%s: w %v 超范围 200–1000", tag, e.W))
				}
				if e.H != 0 && (e.H < 140 || e.H > 700) {
					errs = append(errs, fmt.Sprintf("%s: h %v 超范围 140–700", tag, e.H))
				}
			}
		}
		if e.Kind == "emoji" {
			if e.Text == "" {
				errs = append(errs, tag+": 缺 text（emoji 字符）")
			}
			if utf8.RuneCountInString(e.Text) > 4 {
				errs = append(errs, fmt.Sprintf("%s: text「%s」超长（≤4 字符）", tag, e.Text))
			}
			if e.Fs != 0 && e.Fs > 300 {
				errs = append(errs, fmt.Sprintf("%s: fs %v 超 300", tag, e.Fs))
			}
		}
		if e.Kind == "quote" {
			if n := utf8.RuneCountInString(e.Name); n > 10 {
				errs = append(errs, fmt.Sprintf("%s: 署名 name「%s」超长（≤10 字）", tag, e.Name))
			}
			if e.W != 0 && (e.W < 500 || e.W > 1700) {
				errs = append(errs, fmt.Sprintf("%s: w %v 超范围 500–1700", tag, e.W))
			}
		}
		if e.Kind == "stat" {
			if n := utf8.RuneCountInString(e.Title); n > 12 {
				errs = append(errs, fmt.Sprintf("%s: title「%s」超长（≤12 字）", tag, e.Title))
			}
			if e.W != 0 && (e.W < 280 || e.W > 900) {
				errs = append(errs, fmt.Sprintf("%s: w %v 超范围 280–900", tag, e.W))
			}
		}
		if e.Kind == "checklist" {
			if n := len(e.Nodes); n < 2 || n > 5 {
				errs = append(errs, fmt.Sprintf("%s: nodes %d 条不在 2–5 范围", tag, n))
			}
			for j, s := range e.Nodes {
				if l := utf8.RuneCountInString(s); l > 14 {
					errs = append(errs, fmt.Sprintf("%s: nodes[%d]「%s」超长（≤14 字）", tag, j, s))
				}
			}
		}
		if e.Kind == "chart_bar" || e.Kind == "chart_line" || e.Kind == "chart_pie" || e.Kind == "chart_donut" {
			lim := map[string]int{"chart_bar": 6, "chart_line": 8, "chart_pie": 5, "chart_donut": 5}[e.Kind]
			min := map[string]int{"chart_bar": 2, "chart_line": 3, "chart_pie": 2, "chart_donut": 2}[e.Kind]
			if n := len(e.Values); n < min || n > lim {
				errs = append(errs, fmt.Sprintf("%s: values 数量 %d 不在 %d–%d 范围", tag, n, min, lim))
			}
			for j, v := range e.Values {
				if v < 0 {
					errs = append(errs, fmt.Sprintf("%s: values[%d] 为负", tag, j))
				}
			}
			if len(e.Labels) > 0 && len(e.Labels) != len(e.Values) {
				errs = append(errs, fmt.Sprintf("%s: labels 数 %d 与 values 数 %d 不一致（或不给 labels）", tag, len(e.Labels), len(e.Values)))
			}
			for j, l := range e.Labels {
				if utf8.RuneCountInString(l) > 6 {
					errs = append(errs, fmt.Sprintf("%s: labels[%d]「%s」超长（≤6 字）", tag, j, l))
				}
			}
		}
		if e.Kind == "timeline" {
			if n := len(e.Nodes); n < 2 || n > 6 {
				errs = append(errs, fmt.Sprintf("%s: nodes %d 条不在 2–6 范围", tag, n))
			}
			for j, s := range e.Nodes {
				if l := utf8.RuneCountInString(s); l > 12 {
					errs = append(errs, fmt.Sprintf("%s: nodes[%d]「%s」超长（≤12 字）", tag, j, s))
				}
			}
		}
		if e.Kind == "bracket" {
			if l := utf8.RuneCountInString(e.Text); l > 8 {
				errs = append(errs, fmt.Sprintf("%s: text 超长（≤8 字）", tag))
			}
			if e.H != 0 && (e.H < 160 || e.H > 800) {
				errs = append(errs, fmt.Sprintf("%s: h %v 超范围 160–800", tag, e.H))
			}
		}
		if e.Kind == "strip" {
			if e.N != 0 && (e.N < 2 || e.N > 8) {
				errs = append(errs, fmt.Sprintf("%s: n %d 不在 2–8 范围", tag, e.N))
			}
			if l := utf8.RuneCountInString(e.Text); l > 10 {
				errs = append(errs, fmt.Sprintf("%s: text 超长（≤10 字）", tag))
			}
		}
		if e.Kind == "barrow" {
			if e.W != 0 && (e.W < 120 || e.W > 700) {
				errs = append(errs, fmt.Sprintf("%s: w %v 超范围 120–700", tag, e.W))
			}
			if e.H != 0 && (e.H < 60 || e.H > 240) {
				errs = append(errs, fmt.Sprintf("%s: h %v 超范围 60–240", tag, e.H))
			}
		}
		if e.Kind == "table" {
			if n := len(e.Rows); n < 2 || n > 7 {
				errs = append(errs, fmt.Sprintf("%s: rows %d 行不在 2–7 范围", tag, n))
			}
			cols := 0
			for j, row := range e.Rows {
				if j == 0 {
					cols = len(row)
				}
				if len(row) != cols {
					errs = append(errs, fmt.Sprintf("%s: 各行列数须一致（第 %d 行 %d 列 vs 首行 %d 列）", tag, j+1, len(row), cols))
				}
				if len(row) > 4 {
					errs = append(errs, fmt.Sprintf("%s: 每行 ≤4 列", tag))
				}
				for _, c := range row {
					if utf8.RuneCountInString(c) > 8 {
						errs = append(errs, fmt.Sprintf("%s: 单元格「%s」超长（≤8 字）", tag, c))
					}
				}
			}
		}
		if e.Kind == "zone" {
			if e.W != 0 && (e.W < 240 || e.W > 1700) {
				errs = append(errs, fmt.Sprintf("%s: w %v 超范围 240–1700", tag, e.W))
			}
			if e.H != 0 && (e.H < 200 || e.H > 1500) {
				errs = append(errs, fmt.Sprintf("%s: h %v 超范围 200–1500", tag, e.H))
			}
		}
		if isBg { // 背景层满画幅：不占安全区、不参与重叠判定
			continue
		}
		x0, y0, w, h, ok := e.bbox(cv)
		if !ok {
			continue
		}
		x1, y1 := x0+w, y0+h
		if x0 < cv.X0 || x1 > cv.X1 || y0 < cv.Y0 || y1 > cv.Y1 {
			errs = append(errs, fmt.Sprintf("%s: 占位 (%.0f,%.0f)-(%.0f,%.0f) 越出安全区 x[%.0f,%.0f] y[%.0f,%.0f]",
				tag, x0, y0, x1, y1, cv.X0, cv.X1, cv.Y0, cv.Y1))
		}
		boxes = append(boxes, box{i: i, b: specBox{x0, y0, x1, y1}})
	}
	if customCount > 3 {
		errs = append(errs, fmt.Sprintf("custom 元素 %d 个超上限（每段 ≤3 个，其余视觉用注册 kind 组合）", customCount))
	}
	// 重叠：交叠面积超过较小方块的 25% 判违规。豁免：
	//   箭头（职责就是连接/跨越其他元素）；zone（垫底高亮区，天生要圈住元素）；
	//   role=bg 全幅背景层；形状全包含嵌套（disc/circle 同心构图，如主体+内核）
	isFree := func(k string) bool { return k == "arrow" || k == "zone" }
	for a := 0; a < len(boxes); a++ {
		if isFree(s.Elements[boxes[a].i].Kind) {
			continue
		}
		for b := a + 1; b < len(boxes); b++ {
			if isFree(s.Elements[boxes[b].i].Kind) {
				continue
			}
			A, B := boxes[a], boxes[b]
			if nestedShapes(&s.Elements[A.i], A.b, &s.Elements[B.i], B.b) {
				continue
			}
			ow := min(A.b.x1, B.b.x1) - max(A.b.x0, B.b.x0)
			oh := min(A.b.y1, B.b.y1) - max(A.b.y0, B.b.y0)
			if ow <= 0 || oh <= 0 {
				continue
			}
			smaller := min((A.b.x1-A.b.x0)*(A.b.y1-A.b.y0), (B.b.x1-B.b.x0)*(B.b.y1-B.b.y0))
			if smaller > 0 && ow*oh > smaller*0.25 {
				errs = append(errs, fmt.Sprintf("元素%d(%s) 与 元素%d(%s) 重叠 %.0f%%",
					A.i+1, s.Elements[A.i].Kind, B.i+1, s.Elements[B.i].Kind, ow*oh/smaller*100))
			}
		}
	}
	return errs
}

type specBox struct{ x0, y0, x1, y1 float64 }

func boxOf(e *SpecElement, cv Canvas) (specBox, bool) {
	x0, y0, w, h, ok := e.bbox(cv)
	return specBox{x0, y0, x0 + w, y0 + h}, ok
}

// nestedShapes disc/circle 全包含嵌套（同心构图豁免）。
func nestedShapes(a *SpecElement, A specBox, b *SpecElement, B specBox) bool {
	shape := func(e *SpecElement) bool { return e.Kind == "disc" || e.Kind == "circle" }
	if !shape(a) || !shape(b) {
		return false
	}
	in := func(outer, inner specBox) bool {
		return inner.x0 >= outer.x0 && inner.y0 >= outer.y0 && inner.x1 <= outer.x1 && inner.y1 <= outer.y1
	}
	return in(A, B) || in(B, A)
}

// SanitizeSpec 保底清洗：先删未知/缺失 kind 的元素（模型偶发漏填 kind，
// 带病落盘会在渲染层炸掉整条任务），越界坐标夹进安全区，仍冲突的元素成对删后进者。
// 用于模型多轮修复仍不过校验时——确定性降级优于任务失败。
func SanitizeSpec(s *CompSpec, wordCount int, cv Canvas) []string {
	// camera_spec 越界确定性钳制（不删——镜头参数夹进合法界即可用）。
	if cs := s.CameraSpec; cs != nil {
		clampKey := func(k *CameraKey) {
			if k.Zoom != 0 {
				k.Zoom = clamp(k.Zoom, 1, 1.6)
			}
			k.PanX = clamp(k.PanX, -120, 120)
			k.PanY = clamp(k.PanY, -120, 120)
			k.Rot = clamp(k.Rot, -6, 6)
		}
		clampKey(&cs.From)
		clampKey(&cs.To)
		switch cs.Ease {
		case "power1.inOut", "power2.inOut", "none":
		default:
			cs.Ease = ""
		}
	}
	kept := s.Elements[:0]
	for _, e := range s.Elements {
		if specKinds[e.Kind] && !(e.Kind == "custom" && strings.TrimSpace(e.Html) == "") &&
			!(e.Kind == "scene" && strings.TrimSpace(e.Code) == "") &&
			!(e.Kind == "formula" && strings.TrimSpace(e.Tex) == "") {
			kept = append(kept, e) // custom/scene/formula 缺主体没有降级意义，直接删
		}
	}
	s.Elements = kept
	for i := range s.Elements {
		e := &s.Elements[i]
		if e.Reveal < 0 {
			e.Reveal = 0
		}
		if wordCount > 0 && e.Reveal >= wordCount {
			e.Reveal = wordCount - 1
		}
		if e.Exit < 0 {
			e.Exit = 0
		}
		if e.Exit != 0 && wordCount > 0 && e.Exit >= wordCount {
			e.Exit = wordCount - 1
		}
		if e.Kind == "scene" { // 画布层无坐标可夹，只钳 seed
			if e.Seed < 0 || e.Seed > SceneSeedMax {
				e.Seed = 42
			}
			continue
		}
		if e.Role == "bg" { // 背景层无坐标可夹，只夹氛围参数
			if e.Kind == "image" {
				if e.Dim <= 0 || e.Dim > 1 {
					e.Dim = 0.55
				}
				if e.Blur < 0 || e.Blur > 12 {
					e.Blur = 0
				}
			}
			continue
		}
		// 文本/条状类夹进安全区上半段；圆类圆心留出半径余量
		if e.Kind == "note" || e.Kind == "label" || e.Kind == "big" || e.Kind == "beam" ||
			e.Kind == "icon" || e.Kind == "chart_bar" || e.Kind == "chart_line" || e.Kind == "chart_pie" ||
			e.Kind == "image" || e.Kind == "emoji" || e.Kind == "chip" || e.Kind == "panel" ||
			e.Kind == "timeline" || e.Kind == "bracket" || e.Kind == "strip" || e.Kind == "barrow" ||
			e.Kind == "table" || e.Kind == "zone" || e.Kind == "custom" || e.Kind == "formula" {
			e.X = clamp(e.X, cv.X0+100, cv.X1-260)
			e.Y = clamp(e.Y, cv.Y0+60, cv.Y1-120)
		}
		if e.Kind == "custom" {
			if e.W == 0 {
				e.W = 560
			}
			if e.H == 0 {
				e.H = 320
			}
			e.W = clamp(e.W, 80, cv.W-2*cv.X0)
			e.H = clamp(e.H, 60, cv.Y1-cv.Y0-100)
		}
		if e.Kind == "panel" {
			e.W = clamp(e.W, 240, cv.W-2*cv.X0-40)
			e.H = clamp(e.H, 160, cv.Y1-cv.Y0-200)
		}
		if e.Kind == "table" {
			e.W = clamp(e.W, 260, cv.W-2*cv.X0)
		}
		if e.Kind == "zone" {
			e.W = clamp(e.W, 240, cv.W-2*cv.X0)
			e.H = clamp(e.H, 200, cv.Y1-cv.Y0-60)
		}
		if e.Kind == "chart_bar" || e.Kind == "chart_line" || e.Kind == "chart_pie" {
			e.W = clamp(e.W, 240, cv.W-2*cv.X0)
			e.H = clamp(e.H, 200, cv.Y1-cv.Y0-200)
		}
		if e.Kind == "image" {
			if e.Source != "gen" && e.Source != "search" {
				e.Source = "" // 不认识的图源一律回落搜图
			}
			e.W = clamp(e.W, 240, cv.W-2*cv.X0-60)
			e.H = clamp(e.H, 160, cv.Y1-cv.Y0-260)
		}
		if e.Kind == "disc" || e.Kind == "circle" {
			e.Cx = clamp(e.Cx, cv.X0+180, cv.X1-180)
			e.Cy = clamp(e.Cy, cv.Y0+160, cv.Y1-260)
		}
	}
	// 删除仍互相重叠的元素（每轮删编号最大的冲突方，保留先出现的）
	for len(s.Elements) > 3 {
		victim := -1
		for a := 0; a < len(s.Elements) && victim < 0; a++ {
			if k := s.Elements[a].Kind; k == "arrow" || k == "zone" || s.Elements[a].Role == "bg" {
				continue
			}
			A, okA := boxOf(&s.Elements[a], cv)
			if !okA {
				continue
			}
			for b := a + 1; b < len(s.Elements); b++ {
				if k := s.Elements[b].Kind; k == "arrow" || k == "zone" || s.Elements[b].Role == "bg" {
					continue
				}
				B, okB := boxOf(&s.Elements[b], cv)
				if !okB || nestedShapes(&s.Elements[a], A, &s.Elements[b], B) {
					continue
				}
				ow := min(A.x1, B.x1) - max(A.x0, B.x0)
				oh := min(A.y1, B.y1) - max(A.y0, B.y0)
				smaller := min((A.x1-A.x0)*(A.y1-A.y0), (B.x1-B.x0)*(B.y1-B.y0))
				if ow > 0 && oh > 0 && smaller > 0 && ow*oh > smaller*0.25 {
					victim = b // 删后进者；外层循环停止
					break
				}
			}
		}
		if victim < 0 {
			break
		}
		s.Elements = append(s.Elements[:victim], s.Elements[victim+1:]...)
	}
	return ValidateSpec(s, wordCount, cv)
}

func clamp(v, lo, hi float64) float64 { return min(max(v, lo), hi) }

// simpleAnimKind 单根、无隐藏子元素的 kind（anim 入场覆盖安全集；与渲染层 SIMPLE_KINDS 对应，custom 单列）。
func simpleAnimKind(k string) bool {
	switch k {
	case "title", "note", "label", "big", "beam", "disc", "circle", "chip",
		"icon", "image", "emoji", "panel", "zone", "bracket", "barrow", "stat", "formula":
		return true
	}
	return false
}

// custom 防呆禁用清单（渲染层 Python _BAN_PATTERNS/_BAN_JS 同规则再拦一道：
// 校验→修复轮→Sanitize 降级，任何一路漏进渲染都会被跳过，不炸整段 timeline）。
var (
	cssBan = []string{
		`<script`, `javascript:`, `<iframe`, `<object`, `<embed`, `<link`,
		`\son\w+\s*=`, `position\s*:\s*fixed`, `url\(\s*['"]?\s*https?:`,
		`src\s*=\s*['"]?\s*https?:`, `animation\s*:`, `transition\s*:`,
	}
	jsBan = []string{
		`repeat\s*:`, `yoyo\s*:`, `\bwindow\b`, `\bdocument\b`, `\bfetch\s*\(`,
		`\beval\s*\(`, `\bFunction\s*\(`, `\bDate\b`, `\bMath\.random\b`,
		`setTimeout`, `setInterval`, `requestAnimationFrame`, `\blocalStorage\b`,
		// ID 是容器选择器字符串（"#segNN-cK"），不是 DOM 元素——LLM 反复把它当元素用
		//（ID.querySelector 运行时炸整段 timeline，已两片咬伤）。动画只作用于容器本身
		// tl.to(ID, ...)，不要在 js 里查容器子元素。
		`ID\s*\.`,
	}
	cssBanRe = compileBan(cssBan)
	jsBanRe  = compileBan(jsBan)
)

// scene 专用防呆清单：Code 是 __draw(t, ctx, W, H, rng) 函数体，确定性是硬约束
//（同 t 必须同像素，回放测试 diff=0 才放行）。禁一切自管时间/真随机/出网/DOM 查询；
// Math 三角/幂/取整是数学绘制刚需，放行。渲染层 render_spec.py 同规则再拦一道。
var sceneCodeBan = []string{
	`\bMath\.random\b`, `\bDate\b`, `requestAnimationFrame`, `setTimeout`, `setInterval`,
	`\bfetch\s*\(`, `XMLHttpRequest`, `WebSocket`, `\beval\s*\(`, `\bFunction\s*\(`,
	`\bwindow\b`, `\bdocument\b`, `\blocation\b`, `\bnavigator\b`, `\blocalStorage\b`,
	`\bpostMessage\b`, `\bWorker\b`, `\bimport\b`, `https?:`,
}
var sceneCodeBanRe = compileBan(sceneCodeBan)

// formula tex 防呆：KaTeX 离线排版无出网能力，禁外链与文件包含类命令。
var texBan = []string{`\\href`, `\\includegraphics`, `\\input`, `\\externaldocument`, `https?:`}
var texBanRe = compileBan(texBan)

// SceneCodeViolation scene code 禁用模式检查（返回命中的首个禁用模式，干净返回 ""）。
// 供方法库守卫测试等外部调用方复用 validateScene 同一套禁令。
func SceneCodeViolation(code string) string {
	for _, re := range sceneCodeBanRe {
		if m := re.FindString(code); m != "" {
			return m
		}
	}
	return ""
}

// validateScene scene 元素专规：code 必填限长、seed 范围、确定性禁令。
func validateScene(e *SpecElement, tag string) []string {
	var errs []string
	if strings.TrimSpace(e.Code) == "" {
		return append(errs, tag+": 缺 code（__draw 函数体）")
	}
	if n := utf8.RuneCountInString(e.Code); n > SceneCodeMax {
		errs = append(errs, fmt.Sprintf("%s: code %d 字超长（≤%d）", tag, n, SceneCodeMax))
	}
	if e.Seed != 0 && (e.Seed < 0 || e.Seed > SceneSeedMax) {
		errs = append(errs, fmt.Sprintf("%s: seed %d 超范围 0–%d", tag, e.Seed, SceneSeedMax))
	}
	for _, re := range sceneCodeBanRe {
		if m := re.FindString(e.Code); m != "" {
			errs = append(errs, fmt.Sprintf("%s: code 含禁用模式 %q（确定性硬约束：禁自管时间/真随机/出网/DOM；随机只用注入的 rng）", tag, m))
			break
		}
	}
	return errs
}

// validateFormula formula 元素专规：tex 必填限长 + 出网/文件包含禁令；字号范围。
func validateFormula(e *SpecElement, tag string) []string {
	var errs []string
	if strings.TrimSpace(e.Tex) == "" {
		return append(errs, tag+": 缺 tex（LaTeX 源码）")
	}
	if n := utf8.RuneCountInString(e.Tex); n > 500 {
		errs = append(errs, fmt.Sprintf("%s: tex %d 字超长（≤500）", tag, n))
	}
	for _, re := range texBanRe {
		if m := re.FindString(e.Tex); m != "" {
			errs = append(errs, fmt.Sprintf("%s: tex 含禁用命令 %q（离线排版，禁外链/文件包含）", tag, m))
			break
		}
	}
	if e.Fs != 0 && (e.Fs < 24 || e.Fs > 160) {
		errs = append(errs, fmt.Sprintf("%s: fs %v 超范围 24–160", tag, e.Fs))
	}
	if e.W != 0 && (e.W < 200 || e.W > 1700) {
		errs = append(errs, fmt.Sprintf("%s: w %v 超范围 200–1700", tag, e.W))
	}
	return errs
}

// validateCameraSpec 关键帧镜头（P1-4）：范围钳制报错（回喂修复）+ ease 词表。
// 钳制界与渲染层 render_spec.py 的 CAMERA_SPEC 钳制同值。
func validateCameraSpec(cs *CameraSpec) []string {
	if cs == nil {
		return nil
	}
	var errs []string
	check := func(k CameraKey, when string) {
		if k.Zoom != 0 && (k.Zoom < 1 || k.Zoom > 1.6) {
			errs = append(errs, fmt.Sprintf("camera_spec.%s.zoom %v 超范围 1–1.6", when, k.Zoom))
		}
		if abs(k.PanX) > 120 {
			errs = append(errs, fmt.Sprintf("camera_spec.%s.pan_x %v 超范围 ±120", when, k.PanX))
		}
		if abs(k.PanY) > 120 {
			errs = append(errs, fmt.Sprintf("camera_spec.%s.pan_y %v 超范围 ±120", when, k.PanY))
		}
		if abs(k.Rot) > 6 {
			errs = append(errs, fmt.Sprintf("camera_spec.%s.rot %v 超范围 ±6°", when, k.Rot))
		}
	}
	check(cs.From, "from")
	check(cs.To, "to")
	switch cs.Ease {
	case "", "power1.inOut", "power2.inOut", "none":
	default:
		errs = append(errs, fmt.Sprintf("camera_spec.ease %q 不在词表（power1.inOut/power2.inOut/none）", cs.Ease))
	}
	return errs
}

func compileBan(pats []string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(pats))
	for _, p := range pats {
		out = append(out, regexp.MustCompile("(?i)" + p))
	}
	return out
}

// validateCustom custom 元素专规：html 必填限长、css/js 限长、命名、容器尺寸、禁用模式。
func validateCustom(e *SpecElement, tag string) []string {
	var errs []string
	if strings.TrimSpace(e.Html) == "" {
		errs = append(errs, tag+": 缺 html（容器内 HTML）")
	}
	if n := utf8.RuneCountInString(e.Html); n > 3200 {
		errs = append(errs, fmt.Sprintf("%s: html %d 字超长（≤3200）", tag, n))
	}
	if n := utf8.RuneCountInString(e.Css); n > 2000 {
		errs = append(errs, fmt.Sprintf("%s: css %d 字超长（≤2000）", tag, n))
	}
	if n := utf8.RuneCountInString(e.Js); n > 2000 {
		errs = append(errs, fmt.Sprintf("%s: js %d 字超长（≤2000）", tag, n))
	}
	if n := utf8.RuneCountInString(e.Text); n > 12 {
		errs = append(errs, fmt.Sprintf("%s: text「%s」超长（≤12 字，元素命名）", tag, e.Text))
	}
	if e.Role != "bg" { // 背景层满画幅尺寸由渲染层决定；防呆禁用清单照走
		if e.W != 0 && (e.W < 80 || e.W > 1800) {
			errs = append(errs, fmt.Sprintf("%s: w %v 超范围 80–1800", tag, e.W))
		}
		if e.H != 0 && (e.H < 60 || e.H > 1000) {
			errs = append(errs, fmt.Sprintf("%s: h %v 超范围 60–1000", tag, e.H))
		}
	}
	if e.Text == "" {
		errs = append(errs, tag+": 缺 text（元素命名，素材区可读）")
	}
	for _, re := range cssBanRe {
		if loc := firstMatch(re, e.Html, e.Css); loc != "" {
			errs = append(errs, fmt.Sprintf("%s: html/css 含禁用内容 %q（禁 script/外链/内联事件/position:fixed/CSS transition·animation——确定性硬规则）", tag, loc))
			break
		}
	}
	for _, re := range jsBanRe {
		if loc := firstMatch(re, e.Js); loc != "" {
			hint := "时间线 seek 确定性：禁 repeat/yoyo/DOM/定时器/随机"
			if strings.HasPrefix(loc, "ID.") {
				hint = "ID 是容器选择器字符串（如 \"#seg01-c1\"）不是 DOM 元素，不能 ID.querySelector；js 只写 tl.to(ID,…) 这类容器级动画"
			}
			errs = append(errs, fmt.Sprintf("%s: js 含禁用内容 %q（%s）", tag, loc, hint))
			break
		}
	}
	return errs
}

// firstMatch 返回正则在任一候选串里的首个匹配文本（回喂修复时给 LLM 看实据）。
func firstMatch(re *regexp.Regexp, candidates ...string) string {
	for _, c := range candidates {
		if loc := re.FindString(c); loc != "" {
			return loc
		}
	}
	return ""
}
