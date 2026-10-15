package contract

// prep.go —— 准备阶段三工件（docs/prep-agents-design.md §2/§3）：
//
//	工件 A 文字稿（旁白正文句编号 + fact_sheet 事实溯源）
//	工件 B 风格包（描述 + 剪辑语法 + 设计系统 + 样张）
//	工件 C 分镜稿（纯内容：结构 + 场景 brief，含首拍/末拍；缝概念已退役）
//
// 句切分与编号、工件校验与指纹属于确定性焊死清单（§5.4）：代码拥有，LLM 无权。
// 工件与既有契约（Manuscript/StyleSamples/Storyboard）的换算也在这里——制作阶段
// 零改动，靠 Translate/落盘侧翻译衔接。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// ── 工件 A：文字稿 ───────────────────────────────────────────────

// Script 文字稿：口播旁白正文 + 程序切分的编号句 + 事实清单。
// 分镜稿只引用句编号（narration_refs），零转抄漂移；fact_sheet 随工件进指纹，
// 供审核工位「事实核查」查项使用。
type Script struct {
	SourceMode    string     `json:"source_mode"` // topic（输入是主题，走过检索）| manuscript（输入是完整稿件）
	Text          string     `json:"text"`
	Sentences     []Sentence `json:"sentences"`
	FactSheet     []FactItem `json:"fact_sheet,omitempty"`
	WordCount     int        `json:"word_count"`
	TargetSeconds float64    `json:"target_seconds"`
	CreatedAt     time.Time  `json:"created_at"`
	Fingerprint   string     `json:"fingerprint"`
}

// Sentence 编号句：N1、N2……文本带原生句终符（拼接即还原正文）。
type Sentence struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// FactItem 事实清单一条：断言 → 来源 URL。硬事实逐条可溯源。
type FactItem struct {
	Claim string `json:"claim"`
	URL   string `json:"url"`
}

// SpeechRate 口播语速（字/秒，设计文档 §3.1）。
const SpeechRate = 4.5

// EstimateSpeechDuration 口播时长估算（秒）。
func EstimateSpeechDuration(text string) float64 {
	return float64(utf8.RuneCountInString(text)) / SpeechRate
}

// sentenceTerminals 句切分终符：句号/问叹/分号/省略/换行。逗号不切——句编号
// 要对齐朗读停顿的「句子」语义，不是短语。
const sentenceTerminals = "。！？；…!?;\n"

// SplitSentences 程序句切分 + 编号 N1..Nn（确定性焊死，LLM 无权）。
func SplitSentences(text string) []Sentence {
	var out []Sentence
	var cur strings.Builder
	flush := func() {
		s := strings.TrimSpace(cur.String())
		cur.Reset()
		if s == "" {
			return
		}
		out = append(out, Sentence{ID: fmt.Sprintf("N%d", len(out)+1), Text: s})
	}
	for _, r := range text {
		cur.WriteRune(r)
		if strings.ContainsRune(sentenceTerminals, r) {
			flush()
		}
	}
	flush()
	return out
}

// ValidateScriptText 旁白正文硬校验（submit_script 内置）：非空、纯文本、
// 无结构标记（标题井号/列表符/表格竖线/围栏/链接语法——旁白是拿来读的）。
func ValidateScriptText(text string) error {
	t := strings.TrimSpace(text)
	if t == "" {
		return fmt.Errorf("旁白正文为空")
	}
	for i, line := range strings.Split(t, "\n") {
		l := strings.TrimSpace(line)
		switch {
		case l == "":
			continue
		case strings.HasPrefix(l, "#"), strings.HasPrefix(l, "- "), strings.HasPrefix(l, "* "),
			strings.HasPrefix(l, "+ "), strings.HasPrefix(l, "> "), strings.HasPrefix(l, "```"),
			strings.HasPrefix(l, "|"):
			return fmt.Errorf("第 %d 行带结构标记（%q）——产物是纯旁白正文，不含标题/列表/表格", i+1, FirstRunes(l, 24))
		}
	}
	if strings.Contains(t, "](") && strings.Contains(t, "http") {
		return fmt.Errorf("正文带链接语法 …](http…)——来源 URL 写进 fact_sheet，不混进旁白")
	}
	return nil
}

// FirstRunes 前 n 个 rune（超长加 …）。
func FirstRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// NewScript 组装文字稿工件：切句、编号、字数、指纹。
func NewScript(text, sourceMode string, facts []FactItem, targetSeconds float64) *Script {
	s := &Script{
		SourceMode:    sourceMode,
		Text:          strings.TrimSpace(text),
		Sentences:     SplitSentences(text),
		FactSheet:     facts,
		TargetSeconds: targetSeconds,
	}
	s.WordCount = utf8.RuneCountInString(s.Text)
	s.Fingerprint = fingerprint(s) // 指纹先算（CreatedAt 零值不进指纹），时间戳后填
	s.CreatedAt = time.Now()
	return s
}

// NewStylePack 组装风格包工件（指纹先算，时间戳后填——同 fingerprint 纪律）。
func NewStylePack(styleID, description string, g EditGrammar, ds *StyleSamples, sampleIDs []string) *StylePack {
	sp := &StylePack{
		StyleID:      strings.TrimSpace(styleID),
		Description:  strings.TrimSpace(description),
		Grammar:      g,
		DesignSystem: ds,
		SampleIDs:    sampleIDs,
	}
	sp.Fingerprint = fingerprint(sp)
	sp.CreatedAt = time.Now()
	return sp
}

// NewStoryboardDraft 组装分镜稿工件（指纹先算，时间戳后填——同 fingerprint 纪律）。
func NewStoryboardDraft(plan string, paragraphs []DraftParagraph, segments []DraftSegment) *StoryboardDraft {
	d := &StoryboardDraft{Plan: plan, Paragraphs: paragraphs, Segments: segments}
	d.Fingerprint = fingerprint(d)
	d.CreatedAt = time.Now()
	return d
}

// fingerprint 工件内容指纹（sha256 前 8 字节 hex）。调用方先算指纹再填
// CreatedAt——时间戳不进指纹，同内容重跑指纹不变，不误伤下游缓存。
func fingerprint(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ":err"
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// Sentence 按编号取句文本。
func (s *Script) Sentence(id string) (string, bool) {
	for _, sen := range s.Sentences {
		if sen.ID == id {
			return sen.Text, true
		}
	}
	return "", false
}

// ── 工件 B：风格包 ───────────────────────────────────────────────

// EditGrammar 剪辑语法三字段（§3.2 产物契约）：切分密度 / 段长带（秒）/
// 同舞台延续强弱。③切段与④生成的节奏依据。
type EditGrammar struct {
	Density    string  `json:"density"`    // 切分密度（疏/中/密 + 一句话依据）
	BandMin    float64 `json:"band_min"`   // 段长带下限（秒，含入场动画预算）
	BandMax    float64 `json:"band_max"`   // 段长带上限（秒）
	Continuity string  `json:"continuity"` // 同舞台延续强弱（强=多段同舞台弱切分，弱=新场景快切）
}

// 段长全局钳制（§6 已了结项）：每风格段长带 ∩ [5,20]s；入场动画时长计入段长。
const (
	SegDurClampMin = 5.0
	SegDurClampMax = 20.0
)

// ClampedBand 段长带 ∩ 全局钳制。带与钳制无交集返回 false（submit 校验拦截）。
func (g EditGrammar) ClampedBand() (lo, hi float64, ok bool) {
	lo, hi = g.BandMin, g.BandMax
	if lo < SegDurClampMin {
		lo = SegDurClampMin
	}
	if hi > SegDurClampMax {
		hi = SegDurClampMax
	}
	return lo, hi, lo <= hi
}

// StylePack 风格包（工件 B）：③理解语境的载体——描述供③接视觉词汇，
// 剪辑语法供③切段，设计系统（入场/收尾模板在内）进制作。
type StylePack struct {
	StyleID      string       `json:"style_id"` // 库内条目或本次衍生登记 id
	Description  string       `json:"description"` // 风格描述 + 选择理由（一段，供③理解语境）
	Grammar      EditGrammar  `json:"grammar"`
	DesignSystem *StyleSamples `json:"design_system"` // 含标准入场/收尾动画模板
	SampleIDs    []string     `json:"sample_ids"`    // 样张 id 列表（≥1）
	Confirmed    bool         `json:"confirmed"`     // 打包终审硬门
	CreatedAt    time.Time    `json:"created_at"`
	Fingerprint  string       `json:"fingerprint"`
}

// ValidateStylePack 风格包校验：剪辑语法三字段齐全、段长带数值合法且与全局钳制
// 有交集、样张 ≥1、风格描述非空。design_system 的深校验走 ValidateStyleSamplesDraft。
func ValidateStylePack(sp *StylePack) error {
	if strings.TrimSpace(sp.StyleID) == "" {
		return fmt.Errorf("style_id 为空（须为库内条目或本次衍生登记 id）")
	}
	if strings.TrimSpace(sp.Description) == "" {
		return fmt.Errorf("风格描述为空（附选择理由，供分镜工位理解语境）")
	}
	g := sp.Grammar
	if strings.TrimSpace(g.Density) == "" {
		return fmt.Errorf("剪辑语法缺 density（切分密度）")
	}
	if strings.TrimSpace(g.Continuity) == "" {
		return fmt.Errorf("剪辑语法缺 continuity（同舞台延续强弱）")
	}
	if g.BandMin <= 0 || g.BandMax <= 0 {
		return fmt.Errorf("剪辑语法段长带非法：band_min=%.1f band_max=%.1f（须为正数秒）", g.BandMin, g.BandMax)
	}
	if g.BandMin >= g.BandMax {
		return fmt.Errorf("剪辑语法段长带倒挂：band_min=%.1f ≥ band_max=%.1f", g.BandMin, g.BandMax)
	}
	if _, _, ok := g.ClampedBand(); !ok {
		return fmt.Errorf("段长带 [%.1f, %.1f] 与全局钳制 [%.0f, %.0f] 无交集", g.BandMin, g.BandMax, SegDurClampMin, SegDurClampMax)
	}
	if len(sp.SampleIDs) < 1 {
		return fmt.Errorf("样张 id 列表为空（至少 1 张）")
	}
	if sp.DesignSystem == nil {
		return fmt.Errorf("缺设计系统（含标准入场/收尾动画模板）")
	}
	if err := ValidateStyleSamplesDraft(sp.DesignSystem); err != nil {
		return fmt.Errorf("设计系统不合格：%w", err)
	}
	return nil
}

// ── 工件 C：分镜稿 ───────────────────────────────────────────────

// DraftParagraph 段落：一句话主题 + 覆盖的段（按段序连续铺满全部内容）。
type DraftParagraph struct {
	Title string   `json:"title"`
	Segs  []string `json:"segs"`
}

// VisualBrief 场景规划三件：场景 + 首拍 + 末拍。首末拍是相邻段衔接的全部
// 依据（§4——缝已退役，延续/开场都写在这里）。
type VisualBrief struct {
	Scene      string `json:"scene"`
	FirstFrame string `json:"first_frame"` // 首拍：画面如何开场（延续上段则写明接续点）
	LastFrame  string `json:"last_frame"`  // 末拍：画面收在哪里
}

// DraftSegment 分镜稿的段：旁白只引用句编号，不抄写文本。
type DraftSegment struct {
	ID            string      `json:"id"` // seg01 起连续
	Idx           int         `json:"idx"`
	Key           string      `json:"key"`
	NarrationRefs string      `json:"narration_refs"` // 如 "N3-N4"
	Brief         VisualBrief `json:"visual_brief"`
	DurationHint  float64     `json:"duration_hint"` // 段长带内（含入场动画预算）
}

// StoryboardDraft 分镜稿（工件 C）：纯内容，不出现任何缝/转场/机制概念。
type StoryboardDraft struct {
	Plan        string          `json:"plan,omitempty"`
	Paragraphs  []DraftParagraph `json:"paragraphs"`
	Segments    []DraftSegment  `json:"segments"`
	CreatedAt   time.Time       `json:"created_at"`
	Fingerprint string          `json:"fingerprint"`
}

// ParseNarrationRefs 句引用解析："N3-N4"（区间）| "N3"（单句）| "N3,N4"（列表）。
// 返回展开后的句编号（保持书写顺序）。
func ParseNarrationRefs(refs string) ([]string, error) {
	r := strings.TrimSpace(refs)
	if r == "" {
		return nil, fmt.Errorf("narration_refs 为空")
	}
	var out []string
	for _, part := range strings.Split(r, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if lo, hi, ok := strings.Cut(part, "-"); ok {
			loN, err1 := sentenceNum(lo)
			hiN, err2 := sentenceNum(hi)
			if err1 != nil || err2 != nil {
				return nil, fmt.Errorf("narration_refs 区间 %q 不合法（应为 N3-N4 形态）", part)
			}
			if loN >= hiN {
				return nil, fmt.Errorf("narration_refs 区间 %q 倒挂", part)
			}
			for i := loN; i <= hiN; i++ {
				out = append(out, fmt.Sprintf("N%d", i))
			}
			continue
		}
		if _, err := sentenceNum(part); err != nil {
			return nil, fmt.Errorf("narration_refs 句编号 %q 不合法（应为 N3 形态）", part)
		}
		out = append(out, "N"+strings.TrimPrefix(strings.ToUpper(part), "N"))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("narration_refs 为空")
	}
	return out, nil
}

func sentenceNum(s string) (int, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if !strings.HasPrefix(s, "N") {
		return 0, fmt.Errorf("缺 N 前缀")
	}
	n := 0
	for _, r := range s[1:] {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("非数字")
		}
		n = n*10 + int(r-'0')
	}
	if n <= 0 {
		return 0, fmt.Errorf("编号须 ≥1")
	}
	return n, nil
}

// ValidateStoryboardDraft 分镜稿校验（validate_storyboard/submit_storyboard 内置）：
// 段落铺满且有序、narration_refs 存在且有序且恰好铺满全部编号句（零丢句零重叠）、
// id/idx 连续、段长带（风格段长带 ∩ 全局钳制，入场动画计入段长）、key 与场景非空。
func ValidateStoryboardDraft(d *StoryboardDraft, script *Script, pack *StylePack) error {
	if len(d.Segments) == 0 {
		return fmt.Errorf("segments 为空")
	}
	// 段长带
	lo, hi := SegDurClampMin, SegDurClampMax
	if pack != nil {
		a, b, ok := pack.Grammar.ClampedBand()
		if !ok {
			return fmt.Errorf("风格段长带与全局钳制无交集")
		}
		lo, hi = a, b
	}
	seenID := map[string]bool{}
	covered := map[string]bool{}
	for i, seg := range d.Segments {
		wantID := fmt.Sprintf("seg%02d", i+1)
		if seg.ID != wantID {
			return fmt.Errorf("第 %d 段 id 应为 %s（连续编号），得到 %s", i+1, wantID, seg.ID)
		}
		if seg.Idx != i+1 {
			return fmt.Errorf("段 %s idx 应为 %d（连续），得到 %d", seg.ID, i+1, seg.Idx)
		}
		if strings.TrimSpace(seg.Key) == "" {
			return fmt.Errorf("段 %s key（短标题）为空", seg.ID)
		}
		if strings.TrimSpace(seg.Brief.Scene) == "" {
			return fmt.Errorf("段 %s 场景规划为空", seg.ID)
		}
		if strings.TrimSpace(seg.Brief.FirstFrame) == "" || strings.TrimSpace(seg.Brief.LastFrame) == "" {
			return fmt.Errorf("段 %s 首拍/末拍缺一——它们是相邻段衔接的全部依据", seg.ID)
		}
		if seg.DurationHint < lo || seg.DurationHint > hi {
			return fmt.Errorf("段 %s duration_hint=%.1fs 超出段长带 [%.1f, %.1f]（入场动画预算计入段长）",
				seg.ID, seg.DurationHint, lo, hi)
		}
		if seenID[seg.ID] {
			return fmt.Errorf("段 %s 重复", seg.ID)
		}
		seenID[seg.ID] = true
		refs, err := ParseNarrationRefs(seg.NarrationRefs)
		if err != nil {
			return fmt.Errorf("段 %s：%w", seg.ID, err)
		}
		for _, id := range refs {
			if script != nil {
				if _, ok := script.Sentence(id); !ok {
					return fmt.Errorf("段 %s 引用了不存在的句 %s（文字稿共 %d 句）", seg.ID, id, len(script.Sentences))
				}
			}
			if covered[id] {
				return fmt.Errorf("句 %s 被多个段引用（切段不能重叠）", id)
			}
			covered[id] = true
		}
	}
	// 句铺满：refs 展开序必须恰为 N1..Nn（零丢句、跨段乱序立即现形）
	if script != nil {
		for i, sen := range script.Sentences {
			if !covered[sen.ID] {
				return fmt.Errorf("句 %s 没有被任何段引用（旁白必须铺满全部内容）：%.30s", sen.ID, sen.Text)
			}
			_ = i
		}
	}
	// 段落铺满且按段序
	var flat []string
	for i, para := range d.Paragraphs {
		if strings.TrimSpace(para.Title) == "" {
			return fmt.Errorf("paragraphs[%d] title 为空（一句话主题）", i)
		}
		if len(para.Segs) == 0 {
			return fmt.Errorf("paragraphs[%d]（%s）没有覆盖任何段", i, para.Title)
		}
		flat = append(flat, para.Segs...)
	}
	if len(flat) != len(d.Segments) {
		return fmt.Errorf("paragraphs 覆盖 %d 段 ≠ segments %d 段（必须不重不漏铺满）", len(flat), len(d.Segments))
	}
	for i, id := range flat {
		if id != d.Segments[i].ID {
			return fmt.Errorf("paragraphs 未按段序铺满：第 %d 个应是 %s，得到 %s", i+1, d.Segments[i].ID, id)
		}
	}
	if total := d.TotalHint(); total > MaxDurationSec {
		return fmt.Errorf("总时长 %.0fs 超过上限 %ds", total, MaxDurationSec)
	}
	return nil
}

// TotalHint 分镜稿总时长。
func (d *StoryboardDraft) TotalHint() float64 {
	t := 0.0
	for _, s := range d.Segments {
		t += s.DurationHint
	}
	return t
}

// Translate 分镜稿 → 制作契约（Storyboard）。制作阶段零改动的衔接点：
// narration_refs 就地展开成旁白全文；场景三件拼成 visual_brief 文本（首末拍
// 随段落传递，④的邻段注入与⑤的边界帧查项由此拿到内容）。
func (d *StoryboardDraft) Translate(script *Script, pack *StylePack) *Storyboard {
	sb := &Storyboard{TimingBasis: "narration"}
	if pack != nil {
		sb.StyleID = pack.StyleID
	}
	var titles []string
	for _, para := range d.Paragraphs {
		titles = append(titles, para.Title)
	}
	sb.Plan = strings.Join(titles, " → ")
	for _, seg := range d.Segments {
		var lines []string
		if refs, err := ParseNarrationRefs(seg.NarrationRefs); err == nil && script != nil {
			for _, id := range refs {
				if t, ok := script.Sentence(id); ok {
					lines = append(lines, t)
				}
			}
		}
		s := Segment{
			ID:           seg.ID,
			Idx:          seg.Idx,
			Key:          seg.Key,
			Narration:    strings.Join(lines, ""),
			VisualBrief:  seg.Brief.Scene + "\n首拍：" + seg.Brief.FirstFrame + "\n末拍：" + seg.Brief.LastFrame,
			DurationHint: seg.DurationHint,
			Brief:        &VisualBriefSpec{Scene: seg.Brief.Scene, FirstFrame: seg.Brief.FirstFrame, LastFrame: seg.Brief.LastFrame},
		}
		sb.Segments = append(sb.Segments, s)
	}
	return sb
}
