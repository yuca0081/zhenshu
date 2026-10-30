package contract

// sceneir.go —— 场景时间轴 IR（2026-10-30 引入，试点）。
//
// 目的：把「画面长什么样」和「什么时候发生」拆开。模型仍然自由写每个元素的
// HTML/CSS（视觉表达不被模板化），但时间轴不再手写 <script>，而是写成 cues
// 表；produce 包的编译器把它确定性地编译成 <template> + 一条 GSAP timeline。
//
// 这样修复轮可以只改一行 cue（retime/add_cue/set_cue），不必整文件重抄；
// 结构校验（目标存在、时间在段内、同目标同属性不冲突）也能在渲染前做。
//
// 注意：IR 是低层原语，不是「标题/卡片/三栏」那种版式槽位。元素 HTML/CSS 原样
// 由模型提供；平台只负责舞台骨架、clip 调度与时间线编译。

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

// SceneIR 一个段/段落单元的场景定义。canvas 尺寸以项目画幅为准（不从这里取）。
type SceneIR struct {
	Version    int              `json:"version,omitempty"`
	Background string           `json:"background,omitempty"` // 舞台底色（CSS 颜色/渐变；缺省 transparent）
	CSS        string           `json:"css,omitempty"`        // 本段全局 CSS（模型自由写）
	Elements   []SceneIRElement `json:"elements"`
	Cues       []SceneIRCue     `json:"cues"`
	// ExtraJS 逃生口：canvas/WebGL 的 draw(t) 注册、特殊组件接线。只允许写
	// 「纯函数绘制 + 时间线驱动」，禁止 ticker/随机/时钟 API（lint 会再查一遍）。
	ExtraJS string `json:"extra_js,omitempty"`
}

// SceneIRElement 一个可见元素（编译成 #root 下的一个 clip 层）。
// HTML 是该层内部的原始标记；CSS 是该层追加的样式规则（选择器自己写，
// 建议以 #<id> 开头）。Box 缺省 = 铺满舞台（背景/全屏画布）。
type SceneIRElement struct {
	ID       string      `json:"id"`
	Name     string      `json:"name"` // data-hf-name 人话名
	HTML     string      `json:"html"`
	CSS      string      `json:"css,omitempty"`
	Box      *SceneIRBox `json:"box,omitempty"`
	Start    float64     `json:"start,omitempty"`
	Duration float64     `json:"duration,omitempty"`
	Track    int         `json:"track,omitempty"`
	Z        int         `json:"z,omitempty"`
}

// SceneIRBox 元素在舞台上的像素盒（缺省铺满）。
type SceneIRBox struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// SceneIRCue 一条时间轴补间。At/Dur 都是段内绝对秒（0 起，与词序表同一时钟）。
// Kind: fromTo（缺省，From+To 都要）| to（只要 To）| set（瞬间设置，只要 To）。
type SceneIRCue struct {
	Target  string         `json:"target"` // "#id" 或 "#id .child"
	Kind    string         `json:"kind,omitempty"`
	From    map[string]any `json:"from,omitempty"`
	To      map[string]any `json:"to,omitempty"`
	At      float64        `json:"at"`
	Dur     float64        `json:"dur,omitempty"`
	Ease    string         `json:"ease,omitempty"`
	Stagger float64        `json:"stagger,omitempty"`
	Repeat  int            `json:"repeat,omitempty"`
	Yoyo    bool           `json:"yoyo,omitempty"`
}

var (
	irElementIDRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
	irTargetRe    = regexp.MustCompile(`^#[A-Za-z][A-Za-z0-9_-]{0,63}(?:\s+[.#][A-Za-z][A-Za-z0-9_-]{0,63})*$`)
	irEaseRe      = regexp.MustCompile(`^[A-Za-z0-9.()_,+\- ]{0,80}$`)
)

// CueKind 归一化补间类型。
func (c SceneIRCue) CueKind() string {
	switch strings.ToLower(strings.TrimSpace(c.Kind)) {
	case "fromto", "from_to", "from-to":
		return "fromTo"
	case "to":
		return "to"
	case "set":
		return "set"
	case "":
		if c.From != nil {
			return "fromTo"
		}
		return "to"
	}
	return ""
}

// ValidateSceneIR 机械校验。segDur = 本段/本单元总时长（含收尾垫）；w/h = 画幅。
// 返回空 = 通过。错误消息直接回喂模型，必须可定位。
func ValidateSceneIR(ir *SceneIR, segDur, w, h float64) []string {
	if ir == nil {
		return []string{"ir 为空"}
	}
	var errs []string
	if len(ir.Elements) < 3 {
		errs = append(errs, fmt.Sprintf("elements 只有 %d 个（至少 3 个，元素级指认依赖它）", len(ir.Elements)))
	}
	if len(ir.Elements) > 40 {
		errs = append(errs, fmt.Sprintf("elements 过多（%d 个，上限 40）——合并次要装饰", len(ir.Elements)))
	}
	if len(ir.Cues) > 300 {
		errs = append(errs, fmt.Sprintf("cues 过多（%d 条，上限 300）", len(ir.Cues)))
	}
	if len(ir.CSS) > 24000 {
		errs = append(errs, "css 超过 24000 字符")
	}
	if len(ir.ExtraJS) > 24000 {
		errs = append(errs, "extra_js 超过 24000 字符（只放 canvas 接线，画面逻辑尽量写进 html）")
	}
	if segDur <= 0 {
		errs = append(errs, "段时长为 0，无法校验时间轴")
	}
	if w <= 0 || h <= 0 {
		errs = append(errs, "画幅无效")
	}

	ids := map[string]bool{}
	cued := map[string]bool{}
	for _, c := range ir.Cues {
		t := strings.TrimSpace(c.Target)
		if strings.HasPrefix(t, "#") {
			root := strings.TrimPrefix(strings.Fields(t)[0], "#")
			if root != "" {
				cued[root] = true
			}
		}
	}
	for i, e := range ir.Elements {
		p := fmt.Sprintf("elements[%d]", i)
		id := strings.TrimSpace(e.ID)
		if !irElementIDRe.MatchString(id) {
			errs = append(errs, p+` id 非法（字母开头，仅字母/数字/_/-，≤64）: "`+e.ID+`"`)
		} else if ids[id] {
			errs = append(errs, p+" id 重复: "+id)
		}
		ids[id] = true
		if strings.TrimSpace(e.Name) == "" {
			errs = append(errs, p+" 缺 name（data-hf-name 人话名）")
		}
		if strings.TrimSpace(e.HTML) == "" {
			errs = append(errs, p+" html 为空——每个元素必须有可见内容")
		}
		if len(e.HTML) > 24000 {
			errs = append(errs, p+" html 超过 24000 字符")
		}
		if len(e.CSS) > 12000 {
			errs = append(errs, p+" css 超过 12000 字符")
		}
		// T9 的 IR 版：CSS 给「会被 cue 补间的元素」写 transform，GSAP 会整体
		// 覆盖 transform（translateX(-50%) 居中会被静默丢掉）。层定位本来由 box
		// 决定，这里直接拒，逼模型把定位交给 box、把运动交给 cues。
		if cued[id] {
			re := regexp.MustCompile(`(?s)#` + regexp.QuoteMeta(id) + `\s*\{[^}]*transform\s*:`)
			if re.MatchString(strings.ToLower(ir.CSS + "\n" + e.CSS)) {
				errs = append(errs, p+" 的 CSS 给 #"+id+" 写了 transform，同时 cue 也补间它——GSAP 会覆盖 CSS transform（居中/位移静默失效）。层定位用 box 决定；层内居中用 flex/margin；运动全部写 cues")
			}
		}
		if badTag(e.HTML) != "" {
			errs = append(errs, p+" html 含禁写标签 "+badTag(e.HTML)+"（场景文件由平台编译；canvas 绘制接线写 extra_js）")
		}
		if badTag(e.CSS) != "" {
			errs = append(errs, p+" css 含禁写标签 "+badTag(e.CSS))
		}
		if e.Start < 0 || math.IsNaN(e.Start) || math.IsInf(e.Start, 0) {
			errs = append(errs, p+" start 非法")
		}
		if e.Duration < 0 || math.IsNaN(e.Duration) || math.IsInf(e.Duration, 0) {
			errs = append(errs, p+" duration 非法")
		}
		if segDur > 0 && e.Start > segDur+0.01 {
			errs = append(errs, fmt.Sprintf("%s start=%.3f 超出段长 %.3f", p, e.Start, segDur))
		}
		if e.Box != nil {
			if math.IsNaN(e.Box.W) || math.IsNaN(e.Box.H) || e.Box.W < 0 || e.Box.H < 0 {
				errs = append(errs, p+" box 宽高非法")
			}
		}
	}

	for i, c := range ir.Cues {
		p := fmt.Sprintf("cues[%d]", i)
		t := strings.TrimSpace(c.Target)
		if !irTargetRe.MatchString(t) {
			errs = append(errs, p+` target 非法（要 "#元素id" 或 "#元素id .子选择器"）: "`+c.Target+`"`)
		} else {
			root := strings.TrimPrefix(strings.Fields(t)[0], "#")
			if !ids[root] {
				errs = append(errs, p+" target 指向不存在的元素 #"+root)
			}
		}
		kind := c.CueKind()
		if kind == "" {
			errs = append(errs, p+` kind 只能是 fromTo/to/set: "`+c.Kind+`"`)
		}
		if kind == "fromTo" && len(c.From) == 0 {
			errs = append(errs, p+" fromTo 缺 from")
		}
		if kind != "" && kind != "set" && len(c.To) == 0 {
			errs = append(errs, p+" 缺 to")
		}
		if kind == "set" && len(c.To) == 0 {
			errs = append(errs, p+" set 缺 to")
		}
		if c.At < 0 || math.IsNaN(c.At) || math.IsInf(c.At, 0) {
			errs = append(errs, p+" at 非法（段内绝对秒，≥0）")
		}
		if c.Dur < 0 || math.IsNaN(c.Dur) || math.IsInf(c.Dur, 0) {
			errs = append(errs, p+" dur 非法")
		}
		if segDur > 0 && c.At > segDur+0.5 {
			errs = append(errs, fmt.Sprintf("%s at=%.3f 超出段长 %.3f", p, c.At, segDur))
		}
		if segDur > 0 && kind != "set" && c.At+c.Dur > segDur+1.0 {
			errs = append(errs, fmt.Sprintf("%s 结束时刻 %.3f 超出段长 %.3f（at+dur 必须落在段内）", p, c.At+c.Dur, segDur))
		}
		if c.Ease != "" && !irEaseRe.MatchString(c.Ease) {
			errs = append(errs, p+` ease 含非法字符: "`+c.Ease+`"`)
		}
		if !jsonSafe(c.From) || !jsonSafe(c.To) {
			errs = append(errs, p+" from/to 只能是数字/字符串/布尔/数组（不要嵌套对象）")
		}
	}

	if badTag(ir.ExtraJS) != "" {
		errs = append(errs, "extra_js 含禁写标签 "+badTag(ir.ExtraJS))
	}
	for _, pat := range []struct{ name, re string }{
		{"Date.now", `Date\s*\.\s*now`},
		{"new Date", `new\s+Date`},
		{"performance.now", `performance\s*\.\s*now`},
		{"Math.random", `Math\s*\.\s*random`},
		{"setTimeout", `setTimeout`},
		{"setInterval", `setInterval`},
		{"requestAnimationFrame", `requestAnimationFrame`},
	} {
		if regexp.MustCompile(pat.re).MatchString(ir.ExtraJS) {
			errs = append(errs, "extra_js 禁用 "+pat.name+"（确定性契约）")
		}
	}
	return errs
}

// badTag 返回命中的禁写标签名（空 = 无）。模型只能写内容标记，不能写脚本/样式块
// 或整个文档——场景骨架由编译器生成。
func badTag(s string) string {
	low := strings.ToLower(s)
	for _, t := range []string{"<script", "</script", "<style", "</style", "<template", "</template", "<!doctype", "<html", "<head", "<body"} {
		if strings.Contains(low, t) {
			return t
		}
	}
	return ""
}

// jsonSafe 只允许标量或标量数组（GSAP 值形态）。
func jsonSafe(m map[string]any) bool {
	for _, v := range m {
		switch x := v.(type) {
		case nil, string, bool, float64, float32, int, int64:
		case []any:
			for _, e := range x {
				switch e.(type) {
				case nil, string, bool, float64, float32, int, int64:
				default:
					return false
				}
			}
		case map[string]any:
			return false
		default:
			return false
		}
	}
	return true
}
