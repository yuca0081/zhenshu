package produce

// scenecompile.go —— SceneIR → 场景文件 HTML 的确定性编译器 + IR 局部 patch。
//
// 设计边界（2026-10-30）：
//   - 画面归模型：每个元素的 HTML/CSS 原样进页面，平台不提供版式槽位、不套组件样式；
//   - 时间归平台：cues 表编译成唯一一条 paused GSAP timeline，修复只改 cue，不重抄文件；
//   - 逃生口：extra_js 只用于 canvas/WebGL 的 draw(t) 接线，仍受确定性 lint 约束。

import (
	"encoding/json"
	"errors"
	"fmt"
	stdhtml "html"
	"math"
	"strings"

	"webpage-video-agent/server/internal/contract"
)

// CompileSceneIR 把 IR 编译成 docs/contract.md 要求的 <template> 场景文件。
// segDur = 本段/本单元总时长（含收尾垫）；cv = 项目画幅。
func CompileSceneIR(ir *contract.SceneIR, segID string, cv contract.Canvas, segDur float64) (string, error) {
	if strings.TrimSpace(segID) == "" {
		return "", errors.New("编译场景需要段 id")
	}
	if errs := contract.ValidateSceneIR(ir, segDur, cv.W, cv.H); len(errs) > 0 {
		return "", fmt.Errorf("SceneIR 校验未过：\n- %s", strings.Join(errs, "\n- "))
	}
	w, h := int(cv.W), int(cv.H)
	if w <= 0 || h <= 0 {
		return "", fmt.Errorf("画幅无效: %dx%d", w, h)
	}
	segDur = roundIR(segDur)

	var css strings.Builder
	// 字体声明兜底：lint A2 要求「用了 font-family 就要有 @font-face」。目录字体
	// 与自装字体会由 FixupSegmentFrame/ClampSegmentFonts 在落盘后补齐；这里给 OS
	// 常用两项，保证编译产物单独过 lint。
	css.WriteString("@font-face{font-family:'Segoe UI';src:local('Segoe UI');}\n")
	css.WriteString("@font-face{font-family:'Microsoft YaHei';src:local('Microsoft YaHei');}\n")
	bg := strings.TrimSpace(ir.Background)
	if bg == "" {
		bg = "transparent"
	}
	fmt.Fprintf(&css, "#root{position:relative;width:%dpx;height:%dpx;overflow:hidden;background:%s;font-family:'Segoe UI','Microsoft YaHei',sans-serif;}\n", w, h, bg)
	if s := strings.TrimSpace(ir.CSS); s != "" {
		css.WriteString(s)
		css.WriteString("\n")
	}
	for _, e := range ir.Elements {
		if s := strings.TrimSpace(e.CSS); s != "" {
			css.WriteString(s)
			css.WriteString("\n")
		}
	}

	var body strings.Builder
	for i, e := range ir.Elements {
		st := "position:absolute;"
		if e.Box == nil {
			st += "left:0;top:0;width:100%;height:100%;"
		} else {
			st += fmt.Sprintf("left:%spx;top:%spx;width:%spx;height:%spx;", num(e.Box.X), num(e.Box.Y), num(e.Box.W), num(e.Box.H))
		}
		if e.Z != 0 {
			st += fmt.Sprintf("z-index:%d;", e.Z)
		}
		dur := e.Duration
		if dur <= 0 {
			dur = segDur
		}
		track := e.Track
		if track <= 0 {
			track = i
		}
		name := e.Name
		if strings.TrimSpace(name) == "" {
			name = e.ID
		}
		fmt.Fprintf(&body, "      <div id=\"%s\" class=\"clip\" data-start=\"%.3f\" data-duration=\"%.3f\" data-track-index=\"%d\" data-hf-name=\"%s\" style=\"%s\">%s</div>\n",
			stdhtml.EscapeString(e.ID), roundIR(e.Start), roundIR(dur), track, stdhtml.EscapeString(name), stdhtml.EscapeString(st), e.HTML)
	}

	var js strings.Builder
	js.WriteString("      var tl = gsap.timeline({ paused: true });\n")
	for _, c := range ir.Cues {
		line, err := compileCue(c)
		if err != nil {
			return "", err
		}
		js.WriteString("      " + line + "\n")
	}
	// 时钟垫：即使模型一条 cue 都没写，timeline 也覆盖整段（seek 到段尾不越界）。
	fmt.Fprintf(&js, "      tl.to({}, { duration: %.3f, ease: \"none\" }, 0);\n", segDur)
	if s := strings.TrimSpace(ir.ExtraJS); s != "" {
		for _, ln := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
			js.WriteString("      " + ln + "\n")
		}
	}
	js.WriteString("      window.__timelines = window.__timelines || {};\n")
	fmt.Fprintf(&js, "      window.__timelines[%q] = tl;\n", segID)

	var out strings.Builder
	out.WriteString("<template>\n  <style>\n")
	out.WriteString(css.String())
	out.WriteString("  </style>\n")
	fmt.Fprintf(&out, "  <div data-composition-id=%q data-width=\"%d\" data-height=\"%d\">\n", segID, w, h)
	out.WriteString("    <div id=\"root\">\n")
	out.WriteString(body.String())
	out.WriteString("    </div>\n  </div>\n  <script>\n    (function() {\n")
	out.WriteString(js.String())
	out.WriteString("    })();\n  </script>\n</template>\n")
	return out.String(), nil
}

// compileCue 一条 IR cue → 一行 GSAP 调用。
func compileCue(c contract.SceneIRCue) (string, error) {
	kind := c.CueKind()
	if kind == "" {
		return "", fmt.Errorf("未知 cue kind: %q", c.Kind)
	}
	target := strings.TrimSpace(c.Target)
	if target == "" {
		return "", errors.New("cue 缺 target")
	}
	ease := strings.TrimSpace(c.Ease)
	if ease == "" && kind != "set" {
		ease = "power2.out"
	}
	vars := map[string]any{}
	for k, v := range c.To {
		vars[k] = v
	}
	if kind != "set" {
		dur := c.Dur
		if dur <= 0 {
			dur = 0.6
		}
		vars["duration"] = roundIR(dur)
		vars["ease"] = ease
		if c.Stagger > 0 {
			vars["stagger"] = roundIR(c.Stagger)
		}
		if c.Repeat != 0 {
			vars["repeat"] = c.Repeat
		}
		if c.Yoyo {
			vars["yoyo"] = true
		}
	}
	at := roundIR(c.At)
	if at < 0 {
		at = 0
	}
	switch kind {
	case "set":
		return fmt.Sprintf("tl.set(%s, %s, %.3f);", jsString(target), jsLiteral(vars), at), nil
	case "to":
		return fmt.Sprintf("tl.to(%s, %s, %.3f);", jsString(target), jsLiteral(vars), at), nil
	case "fromTo":
		from := map[string]any{}
		for k, v := range c.From {
			from[k] = v
		}
		return fmt.Sprintf("tl.fromTo(%s, %s, %s, %.3f);", jsString(target), jsLiteral(from), jsLiteral(vars), at), nil
	}
	return "", fmt.Errorf("未知 cue kind: %q", c.Kind)
}

// SceneIRElements 从 IR 派生 elements 注册表（管线要求 ≥3 条）。
func SceneIRElements(ir *contract.SceneIR) []contract.NamedElement {
	if ir == nil {
		return nil
	}
	out := make([]contract.NamedElement, 0, len(ir.Elements))
	for _, e := range ir.Elements {
		out = append(out, contract.NamedElement{ID: e.ID, Name: e.Name})
	}
	return out
}

// ── IR 局部 patch ─────────────────────────────────────────────

// SceneIROp 模型修复轮返回的一条改动。只动时间/单个元素，不整文件重抄。
type SceneIROp struct {
	Op      string                   `json:"op"` // set_cue | add_cue | remove_cue | set_element | add_element | remove_element
	Target  string                   `json:"target,omitempty"`
	Prop    string                   `json:"prop,omitempty"`
	Kind    string                   `json:"kind,omitempty"`
	From    map[string]any           `json:"from,omitempty"`
	To      map[string]any           `json:"to,omitempty"`
	At      *float64                 `json:"at,omitempty"`
	Dur     *float64                 `json:"dur,omitempty"`
	Ease    *string                  `json:"ease,omitempty"`
	Stagger *float64                 `json:"stagger,omitempty"`
	Cue     *contract.SceneIRCue     `json:"cue,omitempty"`
	Element *contract.SceneIRElement `json:"element,omitempty"`
	ID      string                   `json:"id,omitempty"`
}

// ApplySceneIROps 在 IR 副本上套用改动单；任一条失败整单失败（不产出半套用结果）。
func ApplySceneIROps(ir *contract.SceneIR, ops []SceneIROp) (*contract.SceneIR, error) {
	if ir == nil {
		return nil, errors.New("没有可 patch 的 SceneIR")
	}
	if len(ops) == 0 {
		return nil, errors.New("空改动单")
	}
	b, err := json.Marshal(ir)
	if err != nil {
		return nil, err
	}
	var out contract.SceneIR
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	for i, op := range ops {
		if err := applyOneIROp(&out, op); err != nil {
			return nil, fmt.Errorf("改动单第 %d 条（%s）: %w", i+1, op.Op, err)
		}
	}
	return &out, nil
}

func applyOneIROp(ir *contract.SceneIR, op SceneIROp) error {
	switch strings.ToLower(strings.TrimSpace(op.Op)) {
	case "set_cue":
		target := normCueTarget(firstField(op.Target, op.ID))
		if target == "" {
			return errors.New("set_cue 缺 target")
		}
		for i := range ir.Cues {
			c := &ir.Cues[i]
			if strings.TrimSpace(c.Target) != target {
				continue
			}
			if op.Prop != "" && !cueHasProp(*c, op.Prop) {
				continue
			}
			applyCueFields(c, op)
			return nil
		}
		// 没有同 target 的 cue：按新增处理（需要 to）。
		c := contract.SceneIRCue{Target: target, Kind: op.Kind, From: op.From, To: op.To}
		applyCueFields(&c, op)
		if len(c.To) == 0 {
			return errors.New("set_cue 找不到同 target 的 cue，且没给 to（无法新增）")
		}
		ir.Cues = append(ir.Cues, c)
		return nil
	case "add_cue":
		c := contract.SceneIRCue{}
		if op.Cue != nil {
			c = *op.Cue
			c.Target = normCueTarget(c.Target)
		} else {
			c = contract.SceneIRCue{Target: normCueTarget(firstField(op.Target, op.ID)), Kind: op.Kind, From: op.From, To: op.To}
			applyCueFields(&c, op)
		}
		if strings.TrimSpace(c.Target) == "" {
			return errors.New("add_cue 缺 target")
		}
		if len(c.To) == 0 && c.CueKind() != "set" {
			return errors.New("add_cue 缺 to")
		}
		ir.Cues = append(ir.Cues, c)
		return nil
	case "remove_cue":
		target := normCueTarget(firstField(op.Target, op.ID))
		if target == "" {
			return errors.New("remove_cue 缺 target")
		}
		kept := ir.Cues[:0]
		removed := 0
		for _, c := range ir.Cues {
			if strings.TrimSpace(c.Target) == target && (op.Prop == "" || cueHasProp(c, op.Prop)) {
				removed++
				continue
			}
			kept = append(kept, c)
		}
		if removed == 0 {
			return fmt.Errorf("remove_cue 打不中 %s（prop=%q）", target, op.Prop)
		}
		ir.Cues = kept
		return nil
	case "set_element":
		id := normElemID(firstField(op.Target, op.ID))
		if op.Element != nil && op.Element.ID != "" {
			id = normElemID(op.Element.ID)
		}
		if id == "" {
			return errors.New("set_element 缺 target")
		}
		for i := range ir.Elements {
			if ir.Elements[i].ID != id {
				continue
			}
			if op.Element != nil {
				// 只覆盖显式给出的字段：元素盒/起止/轨道是版式信息，patch 一条
				// 文字改动不该把它们清零（零值 = 保持原样；改版式请整元素替换）。
				el := ir.Elements[i]
				el.ID = id // id 不可改（cue 按 id 寻址）
				if op.Element.Name != "" {
					el.Name = op.Element.Name
				}
				if op.Element.HTML != "" {
					el.HTML = op.Element.HTML
				}
				if op.Element.CSS != "" {
					el.CSS = op.Element.CSS
				}
				if op.Element.Box != nil {
					el.Box = op.Element.Box
				}
				if op.Element.Start != 0 {
					el.Start = op.Element.Start
				}
				if op.Element.Duration != 0 {
					el.Duration = op.Element.Duration
				}
				if op.Element.Track != 0 {
					el.Track = op.Element.Track
				}
				if op.Element.Z != 0 {
					el.Z = op.Element.Z
				}
				ir.Elements[i] = el
			}
			return nil
		}
		return fmt.Errorf("set_element 打不中 #%s", id)
	case "add_element":
		if op.Element == nil || strings.TrimSpace(op.Element.ID) == "" {
			return errors.New("add_element 缺 element（含 id/name/html）")
		}
		el := *op.Element
		el.ID = normElemID(el.ID)
		ir.Elements = append(ir.Elements, el)
		return nil
	case "remove_element":
		id := normElemID(firstField(op.Target, op.ID))
		if id == "" {
			return errors.New("remove_element 缺 target")
		}
		keptEls := ir.Elements[:0]
		removed := 0
		for _, e := range ir.Elements {
			if e.ID == id {
				removed++
				continue
			}
			keptEls = append(keptEls, e)
		}
		if removed == 0 {
			return fmt.Errorf("remove_element 打不中 #%s", id)
		}
		ir.Elements = keptEls
		return nil
	default:
		return fmt.Errorf("不支持的 op %q（可用 set_cue/add_cue/remove_cue/set_element/add_element/remove_element）", op.Op)
	}
}

// applyCueFields 把 op 里显式给出的字段合并进 cue（未给的不动）。
func applyCueFields(c *contract.SceneIRCue, op SceneIROp) {
	if op.Kind != "" {
		c.Kind = op.Kind
	}
	if op.At != nil {
		c.At = *op.At
	}
	if op.Dur != nil {
		c.Dur = *op.Dur
	}
	if op.Ease != nil {
		c.Ease = *op.Ease
	}
	if op.Stagger != nil {
		c.Stagger = *op.Stagger
	}
	if op.From != nil {
		c.From = op.From
	}
	if op.To != nil {
		c.To = op.To
	}
}

// cueHasProp 这条 cue 是否补间了 prop（to 或 from 里出现即算）。set_cue/remove_cue
// 的消歧不能只看 to 的第一个键：一条 cue 常常同时补 opacity+y，按字典序取首键会
// 让「改 opacity」永远打不中。
func cueHasProp(c contract.SceneIRCue, prop string) bool {
	if prop == "" {
		return true
	}
	if _, ok := c.To[prop]; ok {
		return true
	}
	_, ok := c.From[prop]
	return ok
}

func firstField(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// normElemID/normCueTarget 容忍模型多写/少写 #：实测修复轮会回 "##axis"（示例里
// target 已带 #，模型又补一个）——不归一化就整单打不中、白白回退整份重写。
func normElemID(s string) string { return strings.TrimLeft(strings.TrimSpace(s), "#") }

func normCueTarget(s string) string {
	s = strings.TrimLeft(strings.TrimSpace(s), "#")
	if s == "" {
		return ""
	}
	return "#" + s
}

func roundIR(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*1000) / 1000
}

func num(v float64) string { return fmt.Sprintf("%g", roundIR(v)) }

func jsLiteral(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	s := string(b)
	s = strings.ReplaceAll(s, "</", "<\\/")
	s = strings.ReplaceAll(s, "\u2028", "\\u2028")
	s = strings.ReplaceAll(s, "\u2029", "\\u2029")
	return s
}

func jsString(s string) string { return jsLiteral(s) }
