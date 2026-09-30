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

// StyleSamples 风格样张：唯一必须用户确认的前置件（不好看只有人能判断）。
type StyleSamples struct {
	Direction string   `json:"direction"` // 如「手绘叙事（纸面·马克笔）」
	Samples   []Sample `json:"samples"`   // 1–3 张
	Skin      *Skin    `json:"skin,omitempty"` // 风格文件（§12.2：样张阶段 LLM 同步产出，确认时锁定为 style/skin.json）
	Confirmed bool     `json:"confirmed"` // 硬门：用户点头后才允许开工
}

type Sample struct {
	Tag  string `json:"tag"`  // 「样张 A · 标题帧」
	Desc string `json:"desc"` // 一句话说明
	HTML string `json:"html"` // 样张内容（与成片同一套风格 token，所见即所得）
}

// Skin 风格文件（§12.2 风格自由化）：必填槽位（插座）+ 自由 CSS。
// 渲染引擎给标题/便签/箭头这些标准件上色只认槽位；自由 CSS 做纹理/特效/装饰，
// 逐段追加到合成物 <style> 尾部（后写的同特异性规则胜出，可覆盖底妆）。
type Skin struct {
	Name  string    `json:"name"`  // 皮肤名（可与 direction 不同：direction 是方向描述）
	Slots SkinSlots `json:"slots"`
	CSS   string    `json:"css"`   // 自由 CSS（≤6000 字符；禁动画/外链/视口单位）
}

// SkinSlots 必填槽位暂定四项（跑几个项目后再增减）+ 常用可选扩展。
type SkinSlots struct {
	BG     string `json:"bg"`     // 底色（纸面/画布）
	TXT    string `json:"txt"`    // 文字色（落在底色上）
	Accent string `json:"accent"` // 强调色（主按钮/高亮/大数字）
	Font   string `json:"font"`   // 字体栈（必须含中文字体）

	Mute     string `json:"mute,omitempty"`     // 次级文字色（缺省由 txt 降透明度推导）
	Surface  string `json:"surface,omitempty"`  // 卡片/面板底（缺省由 bg 推导）
	PanelTxt string `json:"panel_txt,omitempty"` // 面板内文字色（缺省由 bg/txt 推导）
	Radius   int    `json:"radius,omitempty"`    // 圆角 px（0–40）
	BorderW  int    `json:"border_w,omitempty"` // 描边宽 px（0–6）
	ImgRot   int    `json:"img_rot,omitempty"`  // 配图旋转角（-8–8，构成主义斜置等）
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
	if !hasCJKFont(sk.Slots.Font) {
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

func hasCJKFont(font string) bool {
	for _, k := range cjkFontHints {
		if strings.Contains(font, k) {
			return true
		}
	}
	return false
}

// ValidateStyleSamples 落定校验：结构合法 + 用户确认（硬门）。
func ValidateStyleSamples(ss *StyleSamples) error {
	if err := ValidateStyleSamplesDraft(ss); err != nil {
		return err
	}
	if !ss.Confirmed {
		return fmt.Errorf("风格图未经用户确认（confirmed=false）——硬门，不可跳过")
	}
	return nil
}

// ValidateStyleSamplesDraft 草稿校验（不含确认门）：LLM 刚生成的样张
// confirmed 必为 false（等用户在舞台点头），只查结构 + skin 实测校验（§12.2）。
func ValidateStyleSamplesDraft(ss *StyleSamples) error {
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
	if err := ValidateSkin(ss.Skin); err != nil {
		return fmt.Errorf("风格文件不合格：%w", err)
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
