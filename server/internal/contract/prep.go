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
	"path"
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
func NewStoryboardDraft(plan, world, castLegend string, paragraphs []DraftParagraph, segments []DraftSegment) *StoryboardDraft {
	d := &StoryboardDraft{Plan: plan, World: world, CastLegend: castLegend, Paragraphs: paragraphs, Segments: segments}
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
	StyleID      string        `json:"style_id"`    // 库内条目或本次衍生登记 id
	Description  string        `json:"description"` // 风格描述 + 选择理由（一段，供③理解语境）
	Grammar      EditGrammar   `json:"grammar"`
	DesignSystem *StyleSamples `json:"design_system"` // 含标准入场/收尾动画模板
	SampleIDs    []string      `json:"sample_ids"`    // 样张 id 列表（≥1）
	Confirmed    bool          `json:"confirmed"`     // 打包终审硬门
	CreatedAt    time.Time     `json:"created_at"`
	Fingerprint  string        `json:"fingerprint"`
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

// ── 镜头层（一期：1 镜 = 1 段）────────────────────────────────
//
// 现有 VisualBrief 是「这段画面大概是什么」，回答的是内容；ShotDirection 回答
// 「怎么拍」：谁在画面里、什么景别、机位在哪、舞台怎么摆、角色做什么动作、
// 说什么台词、怎么接下一镜。③分镜工位产出，sceneGen 注入导演指令块，
// confirmPrep 以「每段镜头完整」为硬门。

// ShotCast 本镜出场角色：谁、姿态/动作、情绪。Who 在全片保持同一写法，
// 让模型跨镜认出同一个角色。
type ShotCast struct {
	Who     string `json:"who"`
	Pose    string `json:"pose"`
	Emotion string `json:"emotion,omitempty"`
}

// ShotImage 本镜必须出现在画面上的真图素材（用户上传时勾了「必需」，或分镜主动
// 认领）。2026-11-02 事故修复：素材只在写作期以旁挂备注出现时模型会整段忽略
// （实测 10 段 9 段不写 <img>），写进分镜并带量化约束才能变成硬要求。
type ShotImage struct {
	File string `json:"file"` // assets/img/xxx.jpg，逐字命中素材清单
	// Role 决定闸门的面积下限，只给两档——档位多了模型就乱填，闸门也定不了线：
	//   hero  主画面（面积 ≥ 25% 画面）
	//   inset 贴图/画中画（面积 ≥ 6% 画面，且短边 ≥ 200px）
	Role string `json:"role"`
	// At 段内出现时刻（秒）；Hold 最短停留（秒）。空/0 = 交给画面工程师自定，
	// 但闸门仍按 role 的面积下限卡（时间维度靠抽帧复核，不进静态闸门）。
	At      float64 `json:"at,omitempty"`
	Hold    float64 `json:"hold,omitempty"`
	Present string  `json:"present,omitempty"` // 呈现配方：摆位/裁切/边框/重点描哪
}

// ShotImageRoles 合法角色（hero 主画面 / inset 贴图）。
var ShotImageRoles = []string{"hero", "inset"}

// ValidImageRole 角色是否合法（大小写不敏感）。
func ValidImageRole(role string) bool {
	r := strings.ToLower(strings.TrimSpace(role))
	for _, v := range ShotImageRoles {
		if r == v {
			return true
		}
	}
	return false
}

// ImageAreaMin 该角色要求的画面面积占比下限（0–1）。闸门用它把「贴了一张 300×97
// 的缩略图」判成没落地——那正是 2026-11-02 那支片子唯一「用到」素材的方式。
func ImageAreaMin(role string) float64 {
	if strings.ToLower(strings.TrimSpace(role)) == "hero" {
		return 0.25
	}
	return 0.06
}

// Normalize 去空白、角色转小写。
func (im *ShotImage) Normalize() {
	if im == nil {
		return
	}
	im.File = strings.TrimSpace(im.File)
	im.Role = strings.ToLower(strings.TrimSpace(im.Role))
	im.Present = strings.TrimSpace(im.Present)
}

// Validate 单条素材约束：file 非空、role 合法、长度/时刻非负。
func (im *ShotImage) Validate() error {
	if im == nil {
		return fmt.Errorf("素材条目为空")
	}
	if strings.TrimSpace(im.File) == "" {
		return fmt.Errorf("缺 file")
	}
	if !ValidImageRole(im.Role) {
		return fmt.Errorf("file=%s 的 role %q 不合法（可选 %s）", im.File, im.Role, strings.Join(ShotImageRoles, "/"))
	}
	if im.At < 0 || im.Hold < 0 {
		return fmt.Errorf("file=%s 的 at/hold 不能为负", im.File)
	}
	return nil
}

// ShotDirection 一个镜头的调度表（一期内嵌在 Segment / DraftSegment 上）。
type ShotDirection struct {
	Size       string      `json:"size"`                 // wide|medium|close|overhead|full
	Camera     string      `json:"camera"`               // 机位与运镜（如「从桌面近景横移到屏幕」）
	Stage      string      `json:"stage"`                // 舞台/道具/前后景
	Cast       []ShotCast  `json:"cast"`                 // 出场角色与动作
	Action     string      `json:"action"`               // 这一镜发生什么（必须看得见）
	Dialogue   string      `json:"dialogue,omitempty"`   // 短台词/字卡（不是旁白复述）
	Transition string      `json:"transition,omitempty"` // 与下一镜的接法
	RefFrame   string      `json:"ref_frame,omitempty"`  // 项目内相对路径（可空）
	RefNote    string      `json:"ref_note,omitempty"`   // 参考帧说明
	Images     []ShotImage `json:"images,omitempty"`     // 本镜必须落地的真图素材
}

// ShotSizes 合法景别（按由远到近排列，供提示词/前端复用）。
var ShotSizes = []string{"wide", "medium", "close", "overhead", "full"}

// ValidShotSize 景别是否合法（大小写不敏感）。
func ValidShotSize(size string) bool {
	s := strings.ToLower(strings.TrimSpace(size))
	for _, v := range ShotSizes {
		if s == v {
			return true
		}
	}
	return false
}

// Normalize 去空白、景别转小写、丢弃全空的 cast 行（前端「+ 角色」会先加空行）。
func (s *ShotDirection) Normalize() {
	if s == nil {
		return
	}
	s.Size = strings.ToLower(strings.TrimSpace(s.Size))
	s.Camera = strings.TrimSpace(s.Camera)
	s.Stage = strings.TrimSpace(s.Stage)
	s.Action = strings.TrimSpace(s.Action)
	s.Dialogue = strings.TrimSpace(s.Dialogue)
	s.Transition = strings.TrimSpace(s.Transition)
	s.RefFrame = strings.TrimSpace(s.RefFrame)
	s.RefNote = strings.TrimSpace(s.RefNote)
	imgs := s.Images[:0]
	for i := range s.Images {
		s.Images[i].Normalize()
		if s.Images[i].File == "" {
			continue
		}
		imgs = append(imgs, s.Images[i])
	}
	s.Images = imgs
	cast := s.Cast[:0]
	for _, c := range s.Cast {
		c.Who = strings.TrimSpace(c.Who)
		c.Pose = strings.TrimSpace(c.Pose)
		c.Emotion = strings.TrimSpace(c.Emotion)
		if c.Who == "" && c.Pose == "" && c.Emotion == "" {
			continue
		}
		cast = append(cast, c)
	}
	s.Cast = cast
}

// Clone 深拷贝（Translate / 缓存指纹用，避免草稿与制作契约共享切片）。
func (s *ShotDirection) Clone() *ShotDirection {
	if s == nil {
		return nil
	}
	cp := *s
	cp.Cast = append([]ShotCast(nil), s.Cast...)
	cp.Images = append([]ShotImage(nil), s.Images...)
	return &cp
}

// Validate 镜头调度校验：景别合法、机位/舞台/动作非空、角色 1–4 且都有 who。
func (s *ShotDirection) Validate() error {
	if s == nil {
		return fmt.Errorf("镜头为空")
	}
	if !ValidShotSize(s.Size) {
		return fmt.Errorf("景别 %q 不合法（可选 %s）", s.Size, strings.Join(ShotSizes, "/"))
	}
	if strings.TrimSpace(s.Camera) == "" {
		return fmt.Errorf("机位/运镜为空")
	}
	if strings.TrimSpace(s.Stage) == "" {
		return fmt.Errorf("舞台为空")
	}
	if strings.TrimSpace(s.Action) == "" {
		return fmt.Errorf("本镜动作/戏为空")
	}
	if n := len(s.Cast); n < 1 || n > 4 {
		return fmt.Errorf("出场角色 %d 个（须 1–4 个）", n)
	}
	for i, c := range s.Cast {
		if strings.TrimSpace(c.Who) == "" {
			return fmt.Errorf("第 %d 个角色缺 who", i+1)
		}
	}
	for i := range s.Images {
		if err := s.Images[i].Validate(); err != nil {
			return fmt.Errorf("第 %d 张素材：%w", i+1, err)
		}
	}
	return nil
}

// ValidateShotImages 分镜稿的素材落地校验（分镜工位 validate/submit 内置）：
//  1. 每段 images 的 file 必须逐字命中素材清单（清单外渲染必裂图）；
//  2. 单条约束（role 合法、at/hold 非负）；
//  3. 认领了图的段，visual_brief.scene 里要能读到它——写作阶段实际读的是
//     visual_brief 拼成的画面提示，不在散文里出现一次，图就会变成旁挂贴纸；
//  4. 用户勾了「必需」（required）的素材必须被至少一段认领。
//
// 第 4 条是 2026-11-02 事故的直接堵口：素材只在写作期以旁挂备注出现时，模型
// 10 段里 9 段整段忽略。写进分镜、且分镜阶段就报「没认领」，才不会无声漏掉。
func ValidateShotImages(d *StoryboardDraft, required, allowed []string) error {
	if d == nil {
		return fmt.Errorf("分镜稿为空")
	}
	allow := map[string]bool{}
	for _, f := range allowed {
		allow[strings.TrimSpace(f)] = true
	}
	claimed := map[string]bool{}
	var issues []string
	for _, seg := range d.Segments {
		if seg.Shot == nil || len(seg.Shot.Images) == 0 {
			continue
		}
		scene := seg.Brief.Scene
		for i := range seg.Shot.Images {
			im := &seg.Shot.Images[i]
			im.Normalize()
			if err := im.Validate(); err != nil {
				issues = append(issues, seg.ID+"："+err.Error())
				continue
			}
			if len(allow) > 0 && !allow[im.File] {
				issues = append(issues, fmt.Sprintf("%s：素材 %s 不在素材清单内（只准引用清单里的文件）", seg.ID, im.File))
			}
			if !sceneMentionsImage(scene, im.File) {
				issues = append(issues, fmt.Sprintf("%s：shot.images 认领了 %s，但 visual_brief.scene 里没写这张图（画面提示必须能读到它，否则写作期只会画纯图形）", seg.ID, im.File))
			}
			claimed[im.File] = true
		}
	}
	var missing []string
	for _, f := range required {
		f = strings.TrimSpace(f)
		if f == "" || claimed[f] {
			continue
		}
		missing = append(missing, f)
	}
	if len(missing) > 0 {
		issues = append(issues, fmt.Sprintf("以下必需素材没有任何一段认领（用户勾了「必需」= 必须出现在成片里）：\n    - %s\n  → 请在合适的段补 shot.images 条目（file 逐字一致）",
			strings.Join(missing, "\n    - ")))
	}
	if len(issues) > 0 {
		return fmt.Errorf("素材未落地：\n- %s", strings.Join(issues, "\n- "))
	}
	return nil
}

// sceneMentionsImage 画面提示里是否点到了这张图。接受文件名主干，也接受
// 「图/截图/架构图/表格/照片/实拍」这类指代——不要求逐字写路径，但必须有一次
// 明确的指认，且不能只在末拍里当装饰。
func sceneMentionsImage(scene, file string) bool {
	s := strings.ToLower(strings.TrimSpace(scene))
	if s == "" {
		return false
	}
	base := strings.ToLower(file)
	if i := strings.LastIndexAny(base, "/\\"); i >= 0 {
		base = base[i+1:]
	}
	stem := strings.TrimSuffix(base, path.Ext(base))
	if stem != "" && strings.Contains(s, stem) {
		return true
	}
	if strings.Contains(s, strings.ToLower(file)) {
		return true
	}
	// 只认具体指代：本片设计系统满篇「工程图纸」，裸判「图」会永远命中（等于没查）。
	for _, kw := range []string{"截图", "实拍", "照片", "架构图", "表格", "画中画", "贴图", "素材", "真图"} {
		if strings.Contains(s, kw) {
			return true
		}
	}
	return false
}

// UsedImageFiles 分镜稿里被认领的全部素材路径（按首次出现顺序去重）——闸门与
// 报告共用：这些文件必须在场景 HTML 里真的出现。
func UsedImageFiles(d *StoryboardDraft) []string {
	if d == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, seg := range d.Segments {
		if seg.Shot == nil {
			continue
		}
		for _, im := range seg.Shot.Images {
			f := strings.TrimSpace(im.File)
			if f == "" || seen[f] {
				continue
			}
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

// ShotGate 镜头完整度（confirmPrep 硬门 / 前端镜头板共用）。
type ShotGate struct {
	Ready   int      `json:"ready"`   // 字段完整的镜数
	Total   int      `json:"total"`   // 段数
	Missing []string `json:"missing"` // 缺失/不合格的段 id
}

// Enabled 是否启用了镜头层：ready=0 视为老项目/未生成，不拦确认（零感知）。
func (g ShotGate) Enabled() bool { return g.Ready > 0 }

// Complete 全部段都有合格镜头。
func (g ShotGate) Complete() bool { return g.Total > 0 && g.Ready == g.Total }

func shotIssue(s *ShotDirection) string {
	if s == nil {
		return "缺镜头"
	}
	if err := s.Validate(); err != nil {
		return err.Error()
	}
	return ""
}

// ShotGate 分镜稿镜头完整度。
func (d *StoryboardDraft) ShotGate() ShotGate {
	g := ShotGate{Total: len(d.Segments), Missing: []string{}}
	for _, seg := range d.Segments {
		if shotIssue(seg.Shot) == "" {
			g.Ready++
		} else {
			g.Missing = append(g.Missing, seg.ID)
		}
	}
	return g
}

// ShotGate 制作契约镜头完整度（分镜页手动编辑后 / 终审用）。
func (sb *Storyboard) ShotGate() ShotGate {
	g := ShotGate{Total: len(sb.Segments), Missing: []string{}}
	for _, seg := range sb.Segments {
		if shotIssue(seg.Shot) == "" {
			g.Ready++
		} else {
			g.Missing = append(g.Missing, seg.ID)
		}
	}
	return g
}

// ValidateDraftShots ③分镜工位出口的镜头硬校验：每段都要有合格镜头。
// 与 ValidateStoryboardDraft 分开——老项目/老测试没有 shot 字段仍可过基础校验，
// 但新分镜提交必须带镜头（否则 confirmPrep 的镜头板没有内容）。
func ValidateDraftShots(d *StoryboardDraft) error {
	var issues []string
	for _, seg := range d.Segments {
		if msg := shotIssue(seg.Shot); msg != "" {
			issues = append(issues, seg.ID+"："+msg)
		}
	}
	if len(issues) > 0 {
		return fmt.Errorf("镜头表不完整：\n- %s", strings.Join(issues, "\n- "))
	}
	return nil
}

// DraftSegment 分镜稿的段：旁白只引用句编号，不抄写文本。
type DraftSegment struct {
	ID            string      `json:"id"` // seg01 起连续
	Idx           int         `json:"idx"`
	Key           string      `json:"key"`
	NarrationRefs string      `json:"narration_refs"` // 如 "N3-N4"
	Brief         VisualBrief `json:"visual_brief"`
	DurationHint  float64     `json:"duration_hint"` // 段长带内（含入场动画预算）
	// Shot 镜头调度（一期：1 镜 = 1 段）：③工位产出的导演字段，翻译进制作契约。
	Shot *ShotDirection `json:"shot,omitempty"`
}

// StoryboardDraft 分镜稿（工件 C）：纯内容，不出现任何缝/转场/机制概念。
type StoryboardDraft struct {
	Plan        string           `json:"plan,omitempty"`
	World       string           `json:"world,omitempty"`       // 全片世界/舞台设定
	CastLegend  string           `json:"cast_legend,omitempty"` // 全片角色表（外形/身体/性格）
	Paragraphs  []DraftParagraph `json:"paragraphs"`
	Segments    []DraftSegment   `json:"segments"`
	CreatedAt   time.Time        `json:"created_at"`
	Fingerprint string           `json:"fingerprint"`
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
		// 有镜头字段就校验（没有 = 老项目/未启用镜头层，零感知；是否要求每段都有
		// 由 ValidateDraftShots / confirmPrep 的镜头门决定）。
		if seg.Shot != nil {
			if err := seg.Shot.Validate(); err != nil {
				return fmt.Errorf("段 %s 镜头不合格：%w", seg.ID, err)
			}
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
		for _, sen := range script.Sentences {
			if !covered[sen.ID] {
				return fmt.Errorf("句 %s 没有被任何段引用（旁白必须铺满全部内容）：%.30s", sen.ID, sen.Text)
			}
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
	sb := &Storyboard{TimingBasis: "narration", World: d.World, CastLegend: d.CastLegend}
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
		vb := seg.Brief.Scene + "\n首拍：" + seg.Brief.FirstFrame + "\n末拍：" + seg.Brief.LastFrame
		// 认领了真图的段，把素材行焊进 visual_brief 文本（2026-11-02）：写作阶段
		// 实际读的是这段散文，图只躺在 shot.images 里就会被整段忽略。
		if seg.Shot != nil {
			for _, im := range seg.Shot.Images {
				if strings.TrimSpace(im.File) == "" {
					continue
				}
				vb += fmt.Sprintf("\n必须出现素材：%s（%s，面积≥%.0f%%）", im.File, im.Role, ImageAreaMin(im.Role)*100)
			}
		}
		s := Segment{
			ID:           seg.ID,
			Idx:          seg.Idx,
			Key:          seg.Key,
			Narration:    strings.Join(lines, ""),
			VisualBrief:  vb,
			DurationHint: seg.DurationHint,
			Brief:        &VisualBriefSpec{Scene: seg.Brief.Scene, FirstFrame: seg.Brief.FirstFrame, LastFrame: seg.Brief.LastFrame},
			Shot:         seg.Shot.Clone(),
		}
		sb.Segments = append(sb.Segments, s)
	}
	return sb
}
