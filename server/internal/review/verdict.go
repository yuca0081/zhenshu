package review

// verdict.go —— 意见单权威 schema（production-stage-design §6.5）与程序校验层。
// 三种档案共用一套类型；差异（dims 键 / category 枚举 / anchor 形态 / fix 有无）
// 由 profile 常量承载。校验是确定性焊死清单的一部分：schema 校验、跨字段规则
// （severity×fix 一致性、修复轮 reassessment 硬覆盖）全是代码，LLM 无权。
// 校验失败 → 错误清单回流循环内自修，不落盘。

import (
	"fmt"
	"regexp"
	"strings"
)

// Verdict 意见单（submit_verdict 载荷）。
type Verdict struct {
	Dims         map[string]string `json:"dims"`
	Findings     []Finding         `json:"findings"`
	Reassessment []Reassess        `json:"reassessment,omitempty"`
}

// Finding 单条意见。ID 程序侧补（f1..fN，修复轮 reassessment 的锚）。
type Finding struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Anchor   string `json:"anchor"`
	Severity string `json:"severity"`
	Evidence string `json:"evidence"`
	Fix      string `json:"fix"` // 终审档案无 fix（空串）
}

// Reassess 修复轮对既往意见的逐条复核。
type Reassess struct {
	ID       string `json:"id"`
	Result   string `json:"result"` // fixed | still_broken
	Evidence string `json:"evidence,omitempty"`
}

// 档案差异常量（§6.5）。
var (
	dimsSegment  = []string{"content", "fact", "style", "first_last", "device", "subtitle"}
	dimsBoundary = []string{"outro_done", "intro_started", "continuity", "glitch"}
	catsSegment  = dimsSegment
	catsBoundary = dimsBoundary
	catsFinal    = []string{"pacing", "deadair", "stitch", "drift"}

	anchorSegRe   = regexp.MustCompile(`^(N[1-9][0-9]*|t=[0-9]+(\.[0-9]+)?)$`)
	anchorBndRe   = regexp.MustCompile(`^b[0-9]+\|t=-?[0-9]+(\.[0-9]+)?$`)
	anchorFinalRe = regexp.MustCompile(`^t=[0-9]+(\.[0-9]+)?$`)
)

// fixHighSeg 段审 high 的合法 fix；边界审另加 stitch_fix；终审无 fix。
const (
	fixRedraw   = "redraw"
	fixStitch   = "stitch_fix"
	fixPass     = "pass"
	sevHigh     = "high"
	sevLow      = "low"
	reassFixed  = "fixed"
	reassBroken = "still_broken"
)

// profile 一种调用档案的校验口径。
type profile struct {
	dims       []string // nil = 无 dims（终审）
	categories []string
	anchor     *regexp.Regexp
	fix        []string // nil = 无 fix 字段（终审）
}

var (
	profSegment  = profile{dims: dimsSegment, categories: catsSegment, anchor: anchorSegRe, fix: []string{fixRedraw, fixPass}}
	profBoundary = profile{dims: dimsBoundary, categories: catsBoundary, anchor: anchorBndRe, fix: []string{fixStitch, fixRedraw, fixPass}}
	profFinal    = profile{dims: nil, categories: catsFinal, anchor: anchorFinalRe, fix: nil}
)

// Validate 校验意见单。prior 非空 = 修复轮：dims 仍须完整重交、既往意见每条 id
// 必须出现在 reassessment。返回错误清单（一条一行，整体回流给模型）。
func (v *Verdict) Validate(p profile, prior *Verdict) error {
	if v == nil {
		return fmt.Errorf("意见单为空")
	}
	var errs []string
	add := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }

	if p.dims != nil {
		if v.Dims == nil {
			add("缺 dims——六维/四查每维都必须给出 pass/fail/NA")
		} else {
			for _, k := range p.dims {
				val, ok := v.Dims[k]
				if !ok {
					add("dims 缺键 %s（每维必须重交，哪怕是 pass）", k)
					continue
				}
				if val != "pass" && val != "fail" && val != "NA" {
					add("dims.%s=%q 非法（只准 pass/fail/NA）", k, val)
				}
			}
			for k := range v.Dims {
				if !sliceContains(p.dims, k) {
					add("dims 有未知键 %s（只准：%s）", k, strings.Join(p.dims, "/"))
				}
			}
		}
	}

	if len(v.Findings) == 0 && (p.dims == nil || countFails(v.Dims, p.dims) > 0) {
		add("存在 fail 维度但 findings 为空——每个 fail 都要有一条意见")
	}
	seenID := map[string]bool{}
	for i, f := range v.Findings {
		n := i + 1
		if !sliceContains(p.categories, f.Category) {
			add("findings[%d].category=%q 非法（只准：%s）", n, f.Category, strings.Join(p.categories, "/"))
		}
		if !p.anchor.MatchString(strings.TrimSpace(f.Anchor)) {
			add("findings[%d].anchor=%q 不合形态（本档案要求：%s）", n, f.Anchor, anchorHint(p))
		}
		if f.Severity != sevHigh && f.Severity != sevLow {
			add("findings[%d].severity=%q 非法（只准 high/low）", n, f.Severity)
		}
		if strings.TrimSpace(f.Evidence) == "" {
			add("findings[%d].evidence 为空——现状 vs 期望一句话必须有", n)
		}
		if f.Severity == sevHigh && !strings.Contains(f.Evidence, "t=") {
			add("findings[%d]（high）evidence 缺帧时刻——必须写明 t=几秒，给修复轮定位", n)
		}
		if p.fix != nil {
			if !sliceContains(p.fix, f.Fix) {
				add("findings[%d].fix=%q 非法（只准：%s）", n, f.Fix, strings.Join(p.fix, "/"))
			}
			// severity×fix 一致性：high → redraw/stitch_fix；low → pass
			if f.Severity == sevHigh && f.Fix == fixPass {
				add("findings[%d] severity=high 与 fix=pass 矛盾——high 必须给 redraw（或边界审 stitch_fix）", n)
			}
			if f.Severity == sevLow && f.Fix != fixPass {
				add("findings[%d] severity=low 与 fix=%s 矛盾——low 只准 pass（记录不修）", n, f.Fix)
			}
		} else if f.Fix != "" {
			add("findings[%d].fix 应缺省（终审的处置由程序按时刻落段映射，不由模型给）", n)
		}
		if seenID[f.ID] {
			add("findings[%d].id=%q 重复", n, f.ID)
		}
		seenID[f.ID] = true
	}

	if prior != nil {
		need := map[string]bool{}
		for _, f := range prior.Findings {
			need[f.ID] = true
		}
		got := map[string]bool{}
		for _, r := range v.Reassessment {
			if !need[r.ID] {
				add("reassessment 引用了未知意见 id %s（只准复核上一轮意见单里的条目）", r.ID)
				continue
			}
			if r.Result != reassFixed && r.Result != reassBroken {
				add("reassessment[%s].result=%q 非法（只准 fixed/still_broken）", r.ID, r.Result)
			}
			if r.Result == reassBroken && strings.TrimSpace(r.Evidence) == "" {
				add("reassessment[%s] 判 still_broken 必须附 evidence", r.ID)
			}
			got[r.ID] = true
		}
		for id := range need {
			if !got[id] {
				add("上一轮意见 %s 没有复核——修复轮必须逐条 reassessment（fixed/still_broken）", id)
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("意见单校验未过（逐条修正后重新提交）:\n- %s", strings.Join(errs, "\n- "))
	}
	return nil
}

func countFails(dims map[string]string, keys []string) int {
	n := 0
	for _, k := range keys {
		if dims[k] == "fail" {
			n++
		}
	}
	return n
}

func anchorHint(p profile) string {
	switch p.anchor {
	case anchorSegRe:
		return "N几（句号）或 t=秒"
	case anchorBndRe:
		return "b几|t=±秒（负=前段内，如 b02|t=-0.2）"
	default:
		return "t=全片秒（如 t=83.5）"
	}
}

func sliceContains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// AssignIDs 给意见补程序侧编号（f1..fN）并返回。落盘/复核引用都用它。
func (v *Verdict) AssignIDs() {
	for i := range v.Findings {
		v.Findings[i].ID = fmt.Sprintf("f%d", i+1)
	}
}

// HighFindings 取 high 意见（打回的立案范围）。low 只记账不立案。
func (v *Verdict) HighFindings() []Finding {
	var out []Finding
	for _, f := range v.Findings {
		if f.Severity == sevHigh {
			out = append(out, f)
		}
	}
	return out
}

// Signatures 签名清单（§4.5：类别+锚点，如 事实@N3）。
func (v *Verdict) Signatures() []string {
	var out []string
	for _, f := range v.HighFindings() {
		out = append(out, f.Category+"@"+f.Anchor)
	}
	return out
}

// verdictSchema 档案对应的 submit_verdict 参数 JSON Schema（给模型的形状契约；
// 程序校验以上面 Validate 为准——schema 引导形状，代码拥有真理）。
func verdictSchema(p profile) map[string]any {
	enumItem := func(vals ...string) map[string]any {
		return map[string]any{"type": "string", "enum": vals}
	}
	dimObj := func(keys []string) map[string]any {
		props := map[string]any{}
		for _, k := range keys {
			props[k] = enumItem("pass", "fail", "NA")
		}
		return map[string]any{
			"type": "object", "additionalProperties": false,
			"required": keys, "properties": props,
		}
	}
	findingProps := map[string]any{
		"category": enumItem(p.categories...),
		"anchor":   map[string]any{"type": "string", "pattern": p.anchor.String()},
		"severity": enumItem(sevHigh, sevLow),
		"evidence": map[string]any{"type": "string", "minLength": 1},
	}
	required := []string{"category", "anchor", "severity", "evidence"}
	if p.fix != nil {
		findingProps["fix"] = enumItem(p.fix...)
		required = append(required, "fix")
	}
	finding := map[string]any{
		"type": "object", "additionalProperties": false,
		"required": required, "properties": findingProps,
	}
	props := map[string]any{
		"findings": map[string]any{"type": "array", "items": finding},
	}
	req := []string{"findings"}
	if p.dims != nil {
		props["dims"] = dimObj(p.dims)
		req = []string{"dims", "findings"}
		props["reassessment"] = map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []string{"id", "result"},
				"properties": map[string]any{
					"id":       map[string]any{"type": "string"},
					"result":   enumItem(reassFixed, reassBroken),
					"evidence": map[string]any{"type": "string"},
				},
			},
		}
	}
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": req, "properties": props,
	}
}
