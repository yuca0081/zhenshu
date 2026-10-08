package contract

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// ── 三个前置件（硬门，plan.md §3.1）──────────────────────────────

// Manuscript 文稿：内容权威，分镜旁白只能由此改写。
type Manuscript struct {
	Content   string    `json:"content"`
	Source    string    `json:"source"` // paste | link | ai
	WordCount int       `json:"word_count"`
	CreatedAt time.Time `json:"created_at"`
}

func NewManuscript(content, source string) *Manuscript {
	return &Manuscript{
		Content:   content,
		Source:    source,
		WordCount: utf8.RuneCountInString(content),
		CreatedAt: time.Now(),
	}
}

// Storyboard 分镜：系统中枢数据结构（TTS 输入 / 合成物生成输入 / 重渲寻址单元）。
type Storyboard struct {
	TimingBasis string    `json:"timing_basis"` // narration | music
	StyleID     string    `json:"style_id,omitempty"`
	Plan        string    `json:"plan,omitempty"`
	Overlay     *Overlay  `json:"overlay,omitempty"` // 跨段统一角标（P0-2，可选）
	Segments    []Segment `json:"segments"`
}

type Segment struct {
	ID           string  `json:"id"` // seg01, seg02, ...
	Idx          int     `json:"idx"`
	Key          string  `json:"key"` // 舞台展示的段落名
	Narration    string  `json:"narration"`
	VisualBrief  string  `json:"visual_brief"`
	DurationHint float64 `json:"duration_hint"` // 秒；TTS 后以实际音频时长为准
	// Chapter 跨段章节角标（overlay 启用时）：相邻同值段视为同一章；
	// 可带英文小字：主标题|英文（竖线分隔）。
	Chapter string `json:"chapter,omitempty"`
}

// Overlay 跨段统一角标（HUD）：数据在分镜层（跨段天然一致），渲染时 assemble
// 注入每段页面（含 .hf-seg 单段工程——段级重渲各自带全，不依赖邻段）。
// 三锚点各自可缺省：左上章节角标（段 chapter 字段）/ 右上计数器 / 底部进度线。
type Overlay struct {
	Enabled        bool            `json:"enabled"`
	ChapterCounter *ChapterCounter `json:"chapter_counter,omitempty"` // 右上计数器（整片线性走值）
	Timeline       []string        `json:"timeline,omitempty"`        // 章节刻度（v1 仅定位刻度线，不渲染文字）
}

// ChapterCounter 右上角计数器：整片时长内 From 线性走到 To（如 距今 46 亿年 → 0）。
// 单段文件按该段全局时间片取起止值，值是 t 的线性函数，seek 确定性不受影响。
type ChapterCounter struct {
	Prefix   string  `json:"prefix,omitempty"` // 值前缀（如「距今」）
	Unit     string  `json:"unit,omitempty"`   // 值后缀（如「亿年」）
	From     float64 `json:"from"`
	To       float64 `json:"to"`
	Decimals int     `json:"decimals,omitempty"` // 小数位，缺省 2
}

// MaxDurationSec 时长硬上限（2026-09-22 决策：忠实原文，上限 15 分钟）。
const MaxDurationSec = 900

func (sb *Storyboard) TotalHint() float64 {
	t := 0.0
	for _, s := range sb.Segments {
		t += s.DurationHint
	}
	return t
}

// ValidateStoryboard 自检四项中的「结构合法」（plan.md §3.2）。
func ValidateStoryboard(sb *Storyboard) error {
	if sb.TimingBasis != "narration" && sb.TimingBasis != "music" {
		return fmt.Errorf("timing_basis 必须是 narration 或 music，得到 %q", sb.TimingBasis)
	}
	if len(sb.Segments) == 0 {
		return fmt.Errorf("segments 为空")
	}
	for i, s := range sb.Segments {
		if s.Narration == "" {
			return fmt.Errorf("段 %d（%s）旁白为空", i+1, s.ID)
		}
		if s.DurationHint <= 0 {
			return fmt.Errorf("段 %d（%s）duration_hint 非正", i+1, s.ID)
		}
	}
	if sb.TotalHint() > MaxDurationSec {
		return fmt.Errorf("总时长 %.0fs 超过上限 %ds（需压缩取舍并与用户协商）", sb.TotalHint(), MaxDurationSec)
	}
	if sb.Overlay != nil {
		if !sb.Overlay.Enabled {
			return nil // 配了但未启用 = 整体关闭，不再校验内部字段
		}
		if cc := sb.Overlay.ChapterCounter; cc != nil {
			if cc.From == cc.To {
				return fmt.Errorf("overlay.chapter_counter: from 与 to 相等（无走值意义）")
			}
			if cc.Decimals < 0 || cc.Decimals > 4 {
				return fmt.Errorf("overlay.chapter_counter.decimals %d 越界（0–4）", cc.Decimals)
			}
		}
		if n := len(sb.Overlay.Timeline); n > 8 {
			return fmt.Errorf("overlay.timeline %d 项超上限（≤8）", n)
		}
	}
	return nil
}

// StyleSamples 风格样张 + 本片设计系统（plan-v2 §4 笼子二）：唯一必须用户确认的前置件
// （不好看只有人能判断）。v2 design 阶段：模型通读全部分镜为本片**发明**设计系统
// ——皮肤（1–2 套）/字体角色/动效语言/贯穿装置/版式网格/负面清单，样张是它的视觉预览。
// 旧字段 Skin（单皮肤）保留读兼容；设计 tokens 即本文件本体（style/style_samples.json），
// 确认后机械渲染出 style/design.md（人读 + 门2）与 style/skin.json（引擎件）。
type StyleSamples struct {
	StyleID   string   `json:"style_id,omitempty"` // 风格库条目 id（全链路锚点：场景/拼接/再提炼按它查库）
	Direction string   `json:"direction"`          // 如「手绘叙事（纸面·马克笔）」
	Desc      string   `json:"desc,omitempty"`
	Concept   string   `json:"concept,omitempty"`   // 一句话：本片视觉概念，为什么这套系统配这部片
	Samples   []Sample `json:"samples"`             // 1–3 张（用主皮肤渲出的所见即所得预览）
	Skin      *Skin    `json:"skin,omitempty"`      // 旧字段（单皮肤，读兼容；EffectiveSkins 统一读口）
	Skins     []Skin   `json:"skins,omitempty"`     // 1–2 套；双皮肤是穆阿蒂布式加分项（黑霓虹×米色图版按乐段交替）
	SkinRule  string   `json:"skin_rule,omitempty"` // 双皮肤切换规则（哪类段/哪个节拍穿哪套）

	Fonts        *FontRoles     `json:"fonts,omitempty"`         // 字体三角色分工（禁一栈到底）
	Motion       *MotionLang    `json:"motion,omitempty"`        // 动效语言（缓动族+时长档+错峰+转场词汇）
	Device       *DeviceSpec    `json:"device,omitempty"`        // 贯穿装置（全片反复出现、随内容变奏的招牌画面）
	Layouts      []DesignLayout `json:"layouts,omitempty"`       // 本片版式网格（段轮换的优先来源，注册表降为兜底）
	AntiPatterns []string       `json:"anti_patterns,omitempty"` // 本片负面清单（出现即返工的俗手）

	// v3（§4.5）：风格简报存在时，设计交卷必须逐条登记特色落点；覆盖校验在
	// stageStyleSamples（草稿校验通过后追加），这里只承载不校验（老文件无此字段）。
	SignatureAbsorption []SigAbsorption `json:"signature_absorption,omitempty"`

	Confirmed bool `json:"confirmed"` // 硬门：用户点头后才允许开工
}

// EffectiveSkins 新旧两种落法的统一读口：Skins 优先，退回旧 Skin 字段。
func (ss *StyleSamples) EffectiveSkins() []Skin {
	if len(ss.Skins) > 0 {
		return ss.Skins
	}
	if ss.Skin != nil {
		return []Skin{*ss.Skin}
	}
	return nil
}

type Sample struct {
	Tag  string `json:"tag"`  // 「样张 A · 标题帧」
	Desc string `json:"desc"` // 一句话说明
	HTML string `json:"html"` // 样张内容（与成片同一套风格 token，所见即所得）
}

// FontRole 单一字体角色：字体栈 + 字重 + 字距（三模型对比评审：字距普遍拉开是高档片签名）。
type FontRole struct {
	Stack    string `json:"stack"`              // CSS font-family，必须含中文字体
	Weight   string `json:"weight,omitempty"`   // 如 400/700/900
	Tracking string `json:"tracking,omitempty"` // CSS letter-spacing，如 0.06em / normal
}

// FontRoles 字体角色分工：大标题/正文/数据各管各的（穆阿蒂布签名：终端等宽+金色衬线大字+重无衬线）。
type FontRoles struct {
	Display FontRole `json:"display"`        // 大标题/大数字
	Body    FontRole `json:"body"`           // 正文/说明
	Data    FontRole `json:"data,omitempty"` // 数据/标签/代码（等宽优先；缺省退 body）
}

// MotionLang 动效语言：缓动族 + 时长档 + 错峰间隔 + 一句话性格。
// 三模型对比评审结论「决定档次的是动效原理与时间表工程，不是代码行数」——
// 全片统一时长档/缓动族是段间节奏一致性的来源。软约束：只注入 prompt，不 lint。
// 物理边界（hf 白名单）：可补间属性只有 opacity/x/y/scale/rotation 系——动效语言必须在这个物理内设计。
type MotionLang struct {
	Ease      string  `json:"ease"`       // 本片缓动族，如 power3.out
	DurIn     float64 `json:"dur_in"`     // 元素进场时长档（秒）
	DurSettle float64 `json:"dur_settle"` // 落定/强调时长档（秒）
	Stagger   float64 `json:"stagger"`    // 同组元素错峰间隔（秒）
	Notes     string  `json:"notes"`      // 本片动效语言：什么怎么动、禁什么动（含转场习惯）
}

// DeviceSpec 贯穿装置：全片反复出现、随内容变奏的招牌画面机制
// （列LEN访客卡×5、西西弗斯单版式状态机）——段间呼应正是风格本身（plan §2.5）。
type DeviceSpec struct {
	Name   string `json:"name"`    // 装置名，如「访客卡片」
	Desc   string `json:"desc"`    // 装置是什么、观众为什么会记住它
	PerSeg string `json:"per_seg"` // 逐段运用方式：怎么重复/变奏/演变
}

// DesignLayout 本片版式（设计阶段现场发明，段轮换的优先来源；注册表 layouts 降为兜底）。
type DesignLayout struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Desc string `json:"desc"` // 构图描述：主体位置/元素铺排/留白方向
}

// Skin 风格文件（§12.2 风格自由化）：必填槽位（插座）+ 自由 CSS。
// 渲染引擎给标题/便签/箭头这些标准件上色只认槽位；自由 CSS 做纹理/特效/装饰，
// 逐段追加到合成物 <style> 尾部（后写的同特异性规则胜出，可覆盖底妆）。
type Skin struct {
	Name  string    `json:"name"` // 皮肤名（可与 direction 不同：direction 是方向描述）
	Slots SkinSlots `json:"slots"`
	CSS   string    `json:"css"` // 自由 CSS（≤6000 字符；禁动画/外链/视口单位）
}

// SkinSlots 必填槽位暂定四项（跑几个项目后再增减）+ 常用可选扩展。
type SkinSlots struct {
	BG     string `json:"bg"`     // 底色（纸面/画布）
	TXT    string `json:"txt"`    // 文字色（落在底色上）
	Accent string `json:"accent"` // 强调色（主按钮/高亮/大数字）
	Font   string `json:"font"`   // 字体栈（必须含中文字体）

	Mute     string `json:"mute,omitempty"`      // 次级文字色（缺省由 txt 降透明度推导）
	Surface  string `json:"surface,omitempty"`   // 卡片/面板底（缺省由 bg 推导）
	PanelTxt string `json:"panel_txt,omitempty"` // 面板内文字色（缺省由 bg/txt 推导）
	Radius   int    `json:"radius,omitempty"`    // 圆角 px（0–40）
	BorderW  int    `json:"border_w,omitempty"`  // 描边宽 px（0–6）
	ImgRot   int    `json:"img_rot,omitempty"`   // 配图旋转角（-8–8，构成主义斜置等）
}

// ValidateSkin 风格文件实测校验（§12.2：实测过闸才允许确认锁定）：
// 槽位齐、颜色合法、对比度达标（txt/bg ≥ 3:1 大字标准，accessibility 下限）、
// 字体栈含中文、自由 CSS 不带确定性禁区（动画/外链/视口单位——hyperframes lint 会拒）。
func ValidateSkin(sk *Skin) error {
	if sk == nil {
		return nil
	}
	if strings.TrimSpace(sk.Slots.BG) == "" || strings.TrimSpace(sk.Slots.TXT) == "" ||
		strings.TrimSpace(sk.Slots.Accent) == "" || strings.TrimSpace(sk.Slots.Font) == "" {
		return fmt.Errorf("skin 四个必填槽位（bg/txt/accent/font）不能为空")
	}
	for name, hex := range map[string]string{"bg": sk.Slots.BG, "txt": sk.Slots.TXT, "accent": sk.Slots.Accent} {
		if _, _, _, err := ParseHexColor(hex); err != nil {
			return fmt.Errorf("skin 槽位 %s=%q 不是合法 #RRGGBB 颜色", name, hex)
		}
	}
	if ratio := ContrastRatio(sk.Slots.TXT, sk.Slots.BG); ratio < 3.0 {
		return fmt.Errorf("skin 文字/底色对比度 %.1f:1 不足（需 ≥3:1，建议 ≥4.5:1）", ratio)
	}
	if !HasCJKFont(sk.Slots.Font) {
		return fmt.Errorf("skin 字体栈 %q 未包含中文字体（渲染端中文会豆腐块）", sk.Slots.Font)
	}
	if len(sk.CSS) > 6000 {
		return fmt.Errorf("skin 自由 CSS 超过 6000 字符（当前 %d）", len(sk.CSS))
	}
	for _, ban := range []string{"url(", "@import", "<script", "animation", "transition", "position:fixed", "vw", "vh"} {
		if strings.Contains(sk.CSS, ban) {
			return fmt.Errorf("skin 自由 CSS 禁用 %q（确定性渲染/无网络/固定画布约束）", ban)
		}
	}
	if r := sk.Slots.Radius; r < 0 || r > 40 {
		return fmt.Errorf("skin radius=%d 超范围 0–40", r)
	}
	if w := sk.Slots.BorderW; w < 0 || w > 6 {
		return fmt.Errorf("skin border_w=%d 超范围 0–6", w)
	}
	return nil
}

// cjkFontHints 字体栈里的中文字体线索（Windows 渲染端实际可用的本地字体族）。
var cjkFontHints = []string{
	"KaiTi", "SimSun", "SimHei", "Microsoft YaHei", "FangSong", "STKaiti", "STSong",
	"PingFang", "Noto Sans CJK", "Noto Serif CJK", "Source Han", "楷", "宋体", "黑体", "雅黑", "仿宋",
}

func HasCJKFont(font string) bool {
	for _, k := range cjkFontHints {
		if strings.Contains(font, k) {
			return true
		}
	}
	return false
}

// ValidateStyleSamples 落定校验：结构合法 + 用户确认（硬门）。
// 宽松档——旧项目/库内风格包（applyPack 直写 confirmed 文件）没有设计系统块也能过，
// 兜底路径不因 v2 升级被堵死；新生成的设计在草稿档（ValidateStyleSamplesDraft）已严格过闸。
func ValidateStyleSamples(ss *StyleSamples) error {
	if err := ValidateStyleSamplesBase(ss); err != nil {
		return err
	}
	if !ss.Confirmed {
		return fmt.Errorf("风格图未经用户确认（confirmed=false）——硬门，不可跳过")
	}
	return nil
}

// ValidateStyleSamplesBase 结构校验（不含确认门、不要求设计系统块）：
// direction、样张、皮肤（新旧两种落法都查）。落定文件与库内包走这档。
func ValidateStyleSamplesBase(ss *StyleSamples) error {
	if ss.Direction == "" {
		return fmt.Errorf("direction 为空")
	}
	if len(ss.Samples) < 1 || len(ss.Samples) > 3 {
		return fmt.Errorf("样张数量须为 1–3，得到 %d", len(ss.Samples))
	}
	for i, s := range ss.Samples {
		if s.HTML == "" {
			return fmt.Errorf("样张 %d 缺少 HTML 内容", i+1)
		}
	}
	skins := ss.EffectiveSkins()
	if len(skins) == 0 {
		return fmt.Errorf("缺皮肤（skins 或 skin）")
	}
	for i := range skins {
		if err := ValidateSkin(&skins[i]); err != nil {
			return fmt.Errorf("皮肤 %d 不合格：%w", i+1, err)
		}
	}
	return nil
}

// ValidateStyleSamplesDraft 草稿校验（设计阶段 LLM 新产出专用，最严档）：
// Base 全项 + 设计系统四件套齐备（字体角色/动效语言/贯穿装置 + concept）。
// 缺任何一件 = 模型只交了 v1 水平的配色卡，打回重出（修复轮回喂缺项）。
func ValidateStyleSamplesDraft(ss *StyleSamples) error {
	if err := ValidateStyleSamplesBase(ss); err != nil {
		return err
	}
	if strings.TrimSpace(ss.Concept) == "" {
		return fmt.Errorf("concept 为空——设计系统必须写明「为什么这套系统配这部片」")
	}
	skins := ss.EffectiveSkins()
	if len(skins) > 2 {
		return fmt.Errorf("skins 最多 2 套（双皮肤封顶，多了是拼盘）")
	}
	if len(skins) == 2 && strings.TrimSpace(ss.SkinRule) == "" {
		return fmt.Errorf("双皮肤必须给 skin_rule（哪类段/哪个节拍穿哪套，可机械执行）")
	}
	if ss.Fonts == nil {
		return fmt.Errorf("缺 fonts（字体三角色 display/body/data 分工）")
	}
	for _, r := range []struct {
		name string
		fr   FontRole
	}{{"display", ss.Fonts.Display}, {"body", ss.Fonts.Body}} {
		if strings.TrimSpace(r.fr.Stack) == "" {
			return fmt.Errorf("fonts.%s 字体栈为空", r.name)
		}
		if !HasCJKFont(r.fr.Stack) {
			return fmt.Errorf("fonts.%s 字体栈 %q 未包含中文字体（渲染端中文会豆腐块）", r.name, r.fr.Stack)
		}
	}
	if d := ss.Fonts.Data; strings.TrimSpace(d.Stack) != "" && !HasCJKFont(d.Stack) {
		return fmt.Errorf("fonts.data 字体栈 %q 未包含中文字体（数据标签也有中文，栈尾必须带 CJK 回退）", d.Stack)
	}
	if ss.Motion == nil {
		return fmt.Errorf("缺 motion（动效语言：缓动族/时长档/错峰/性格）")
	}
	if strings.TrimSpace(ss.Motion.Ease) == "" || strings.TrimSpace(ss.Motion.Notes) == "" {
		return fmt.Errorf("motion.ease 与 motion.notes 不能为空（缓动族 + 动效性格一句话）")
	}
	if ss.Motion.DurIn < 0.1 || ss.Motion.DurIn > 1.5 {
		return fmt.Errorf("motion.dur_in=%.2f 超范围 0.1–1.5 秒", ss.Motion.DurIn)
	}
	if ss.Motion.DurSettle < 0.05 || ss.Motion.DurSettle > 1.0 {
		return fmt.Errorf("motion.dur_settle=%.2f 超范围 0.05–1.0 秒", ss.Motion.DurSettle)
	}
	if ss.Motion.Stagger < 0 || ss.Motion.Stagger > 0.5 {
		return fmt.Errorf("motion.stagger=%.2f 超范围 0–0.5 秒", ss.Motion.Stagger)
	}
	if ss.Device == nil || strings.TrimSpace(ss.Device.Name) == "" ||
		strings.TrimSpace(ss.Device.Desc) == "" || strings.TrimSpace(ss.Device.PerSeg) == "" {
		return fmt.Errorf("缺 device（贯穿装置：名称/是什么/逐段运用方式三件齐）")
	}
	if len(ss.Layouts) > 0 && len(ss.Layouts) < 2 {
		return fmt.Errorf("layouts 给了就至少 2 个（段轮换需要变化，1 个等于没给）")
	}
	if n := len(ss.AntiPatterns); n > 8 {
		return fmt.Errorf("anti_patterns %d 条超上限（≤8，负面清单是缰绳不是清单文学）", n)
	}
	return nil
}

// ── 元素注册表（四级寻址·元素级的地基，plan.md §4.2）────────────

type ElementRegistry struct {
	Elements []NamedElement `json:"elements"`
}

type NamedElement struct {
	ID   string `json:"id"`   // 稳定 id，如 seg03-loop-arrow
	Name string `json:"name"` // 人话名，如「箭头：蓝光弹开」
}
