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
	TimingBasis string   `json:"timing_basis"` // narration | music
	StyleID     string   `json:"style_id,omitempty"`
	Plan        string   `json:"plan,omitempty"`
	Overlay     *Overlay `json:"overlay,omitempty"` // 跨段统一角标（P0-2，可选）
	// Arc/Motifs 导演统筹（导演定调产物，可选）：弧线给设计/生成/审查提供全片
	// 视野；缺省 = 无导演字段的老项目，全链路零感知（见 ValidateStoryboard）。
	Arc      *Arc      `json:"arc,omitempty"`
	Motifs   []Motif   `json:"motifs,omitempty"`
	Segments []Segment `json:"segments"`
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
	// Beats 本段节拍表（导演定调产物，可选）：旁白锚句 → 画面预期。
	// 生成侧照此编排元素揭示，审查侧（M2 审片）照此验收内容缺失。
	Beats []Beat `json:"beats,omitempty"`
	// Brief 结构化场景规划（准备工位分镜稿翻译产物，可选）：场景 + 首拍 + 末拍。
	// ④生成注入的邻段承诺从这里取（prep-agents-design §4：邻段两行承诺白送注入，
	// 内容=相邻 brief 的末拍/首拍）；老项目无此字段零感知。VisualBrief 文本仍是
	// 注入主渠道（scene+首末拍拼装），Brief 是机器可读的邻段寻址层。
	Brief *VisualBriefSpec `json:"brief,omitempty"`
}

// VisualBriefSpec 结构化首末拍（与分镜稿工件同形；缝已退役——延续/开场都写在这里）。
type VisualBriefSpec struct {
	Scene      string `json:"scene"`
	FirstFrame string `json:"first_frame"`
	LastFrame  string `json:"last_frame"`
}

// Arc 全片叙事弧线：一句话主线 + 分章张力曲线。张力 1–5 整数，审查时
// 对照实际观感（连续多章同张力 = 弧线没有起伏 = 定调不合格）。
type Arc struct {
	Spine    string       `json:"spine"` // 全片一句话主线
	Chapters []ArcChapter `json:"chapters"`
}

// ArcChapter 章：按段序连续铺满全片（ValidateStoryboard 硬校验）。
type ArcChapter struct {
	Title   string   `json:"title"`
	Mood    string   `json:"mood,omitempty"` // 章情绪（克制/递进/爆发…，可缺省）
	Tension int      `json:"tension"`        // 1–5
	Segs    []string `json:"segs"`           // 本章覆盖的段 id（连续）
}

// Motif 贯穿母题：同一视觉意象跨段复现（首立/复现/尾呼应）。不是每片都该有——
// 短平快题材零母题是合法选择；但声明的母题至少出现 2 次（单段出现的不是母题）。
type Motif struct {
	Name        string     `json:"name"`
	Desc        string     `json:"desc"` // 视觉形态一句话
	Appearances []MotifApp `json:"appearances"`
}

// MotifApp 母题的一次出场。
type MotifApp struct {
	SegID string `json:"seg_id"`
	Role  string `json:"role"` // establish 首次立起 | callback 中途复现 | echo 结尾呼应
}

// Beat 段内节拍：锚句必须逐字来自本段旁白（防导演幻觉锚句——审查时还要用
// 词级时间戳把锚句换算成精确时刻）。Expect 写「观众此刻该看到什么」（验收
// 口径），不写实现手法——怎么画是 scenegen 的事。
type Beat struct {
	Quote  string `json:"quote"`
	Expect string `json:"expect"`
}

// DirectorPlan 导演定调作业的输出形态（独立于 Storyboard，合入见
// pipeline.applyDirectorPlan——旁白/时长等分镜本体字段不经导演之手）。
type DirectorPlan struct {
	Arc    *Arc               `json:"arc,omitempty"`
	Motifs []Motif            `json:"motifs,omitempty"`
	Segs   []DirectorSegBeats `json:"segments"`
}

// DirectorSegBeats 逐段节拍（id 必须是已有段）。
type DirectorSegBeats struct {
	ID    string `json:"id"`
	Beats []Beat `json:"beats"`
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
	seenID := map[string]bool{}
	seenIdx := map[int]bool{}
	for i, s := range sb.Segments {
		if s.Narration == "" {
			return fmt.Errorf("段 %d（%s）旁白为空", i+1, s.ID)
		}
		if s.DurationHint <= 0 {
			return fmt.Errorf("段 %d（%s）duration_hint 非正", i+1, s.ID)
		}
		// 段身份（id/idx）是时间轴/画面注册表/聊天引用的锚：模型产出重复段会让
		// 按 idx/id 寻址的环节静默串段（两段共用一个 id/注册表），必须在这里拦下
		if seenID[s.ID] {
			return fmt.Errorf("段 %d（%s）id 重复——每个段的 id 必须唯一", i+1, s.ID)
		}
		seenID[s.ID] = true
		if seenIdx[s.Idx] {
			return fmt.Errorf("段 %d（%s）idx=%d 重复——段序必须连续唯一", i+1, s.ID, s.Idx)
		}
		seenIdx[s.Idx] = true
	}
	if sb.TotalHint() > MaxDurationSec {
		return fmt.Errorf("总时长 %.0fs 超过上限 %ds（需压缩取舍并与用户协商）", sb.TotalHint(), MaxDurationSec)
	}
	// 导演统筹字段（全部可选；出现才校验——老项目零字段零感知）。
	if verr := validateDirector(sb); verr != nil {
		return verr
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

// validateDirector 导演统筹字段校验（出现才查，老项目零字段零感知）：
// 弧线章必须按段序连续铺满全片、弧线在场则每段必须有节拍表、节拍锚句必须
// 逐字来自本段旁白（防导演幻觉锚句——锚句是 M2 审片换算时刻的定位键）、
// 母题出场合法。子串比对忽略空白差异（旁白排版空格不构成否决锚句的理由）。
func validateDirector(sb *Storyboard) error {
	if sb.Arc != nil {
		if strings.TrimSpace(sb.Arc.Spine) == "" {
			return fmt.Errorf("arc.spine 为空（全片主线一句话不能缺）")
		}
		if n := len(sb.Arc.Chapters); n < 2 || n > 6 {
			return fmt.Errorf("arc.chapters %d 章越界（2–6：一章不叫弧线，太多章观众记不住）", n)
		}
		var flat []string
		inChapter := map[string]bool{}
		for i, ch := range sb.Arc.Chapters {
			if strings.TrimSpace(ch.Title) == "" {
				return fmt.Errorf("arc.chapters[%d].title 为空", i)
			}
			if ch.Tension < 1 || ch.Tension > 5 {
				return fmt.Errorf("arc.chapters[%d]（%s）tension=%d 越界（1–5）", i, ch.Title, ch.Tension)
			}
			if len(ch.Segs) == 0 {
				return fmt.Errorf("arc.chapters[%d]（%s）没有覆盖任何段", i, ch.Title)
			}
			for _, id := range ch.Segs {
				if inChapter[id] {
					return fmt.Errorf("段 %s 出现在多个章里（章划分不能重叠）", id)
				}
				inChapter[id] = true
				flat = append(flat, id)
			}
		}
		for i, seg := range sb.Segments {
			if i >= len(flat) {
				return fmt.Errorf("arc.chapters 没有铺满全片：段 %s 不属于任何章", seg.ID)
			}
			if flat[i] != seg.ID {
				return fmt.Errorf("arc.chapters 必须按段序连续铺满全片：第 %d 个应是 %s，得到 %s", i+1, seg.ID, flat[i])
			}
		}
		if len(flat) > len(sb.Segments) {
			return fmt.Errorf("arc.chapters 引用了不存在的段（章内 %d 段 > 全片 %d 段）", len(flat), len(sb.Segments))
		}
	}
	for i, seg := range sb.Segments {
		if sb.Arc != nil && len(seg.Beats) < 2 {
			return fmt.Errorf("段 %d（%s）节拍表不足 2 条（弧线在场时逐段必须有节拍）", i+1, seg.ID)
		}
		if len(seg.Beats) > 6 {
			return fmt.Errorf("段 %d（%s）节拍 %d 条超上限（≤6：节拍是骨架不是逐句字幕）", i+1, seg.ID, len(seg.Beats))
		}
		for j, b := range seg.Beats {
			if strings.TrimSpace(b.Quote) == "" || strings.TrimSpace(b.Expect) == "" {
				return fmt.Errorf("段 %d（%s）节拍 %d 的 quote/expect 为空", i+1, seg.ID, j+1)
			}
			if !containsIgnoreSpaces(seg.Narration, b.Quote) {
				return fmt.Errorf("段 %d（%s）节拍 %d 锚句不是旁白原文（必须逐字抄本段旁白）：%s", i+1, seg.ID, j+1, b.Quote)
			}
		}
	}
	if n := len(sb.Motifs); n > 3 {
		return fmt.Errorf("motifs %d 个超上限（≤3：母题多了观众一个都记不住）", n)
	}
	segIDs := map[string]bool{}
	for _, s := range sb.Segments {
		segIDs[s.ID] = true
	}
	for i, m := range sb.Motifs {
		if strings.TrimSpace(m.Name) == "" || strings.TrimSpace(m.Desc) == "" {
			return fmt.Errorf("motifs[%d] name/desc 为空", i)
		}
		if len(m.Appearances) < 2 {
			return fmt.Errorf("母题「%s」只出场 %d 次（≥2 才算贯穿，单段出现的不是母题）", m.Name, len(m.Appearances))
		}
		for _, ap := range m.Appearances {
			switch ap.Role {
			case "establish", "callback", "echo":
			default:
				return fmt.Errorf("母题「%s」出场角色 %q 非法（establish|callback|echo）", m.Name, ap.Role)
			}
			if !segIDs[ap.SegID] {
				return fmt.Errorf("母题「%s」引用了不存在的段 %s", m.Name, ap.SegID)
			}
		}
	}
	return nil
}

// containsIgnoreSpaces 子串比对忽略空白差异。
func containsIgnoreSpaces(hay, needle string) bool {
	if strings.Contains(hay, needle) {
		return true
	}
	if needle == "" {
		return false
	}
	return strings.Contains(stripSpaces(hay), stripSpaces(needle))
}

func stripSpaces(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\n', '\r':
			return -1
		}
		return r
	}, s)
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
	Samples   []Sample `json:"samples"`             // 1–6 张（用主皮肤渲出的所见即所得预览）
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

	// EntryExit 标准入场/收尾动画模板（prep-agents-design §3.2/§4）：转场的载体。
	// 一套模板一段内生效（新场景开场走入场模板、收尾走收尾模板），禁止逐段即兴
	// 发明；入场时长计入段长带预算。受 paused-timeline 契约约束（禁 CSS animation
	// 等 lint 照查）。老文件无此字段，读侧一律按可选处理。
	EntryExit *EntryExitSpec `json:"entry_exit,omitempty"`

	// Post 全片统一后期质感（可选）：暗角/颗粒/色差是设计决策不是平台默认——
	// 设计没写 post = 成片不叠任何后期。写进 project.json "post"，stitch 消费。
	Post *PostFXSpec `json:"post,omitempty"`

	// VendLibs 样张自采的外部库清单（lib@精确版本，去重排序）：样张阶段经 vend
	// 机械闸（许可证白名单/钉版/哈希/台账）采购入库的库，随设计系统携带——正式段
	// 场景优先声明同款 vend 复现样张效果（货架优先），货架没有的才申请新采。
	// 老文件无此字段，读侧一律按可选处理。
	VendLibs []string `json:"vend_libs,omitempty"`

	// CustomFonts 样张阶段自装字体清单（终审页披露「装了哪些字体」的唯一依据）：
	// 样张 HTML 里 <!-- font: Family | 协议 | 直链URL --> 声明，平台下载/子集化后
	// 真字体渲样张，随设计系统携带进制作（申请书保留 + 段级钳制放行）。协议是
	// 模型自报的披露字段，人工验收在终审页看——不在装的时候拦。老文件无此字段。
	CustomFonts []CustomFont `json:"custom_fonts,omitempty"`

	Confirmed bool `json:"confirmed"` // 硬门：用户点头后才允许开工
}

// CustomFont 样张自装字体的一条记账：Family 必须与样张/设计栈里 font-family 的
// 写法逐字一致（渲染端 @font-face 按它配对）；License 是模型自报的披露字段；
// URL 是直链（ttf/otf 或 zip release 资产），font_tool 按内容分流下载。
type CustomFont struct {
	Family  string `json:"family"`
	License string `json:"license,omitempty"`
	URL     string `json:"url"`
}

// EntryExitSpec 标准入场/收尾动画模板：风格资产，写一次用一世。
// Entry/Exit 写「画面如何显影/如何收」的机制一句话（如 水墨晕染显影 0.4s）；
// EntryDur 是入场预算秒数，计入段长带（§7：0.3–0.5s/段）。
type EntryExitSpec struct {
	Entry    string  `json:"entry"`
	Exit     string  `json:"exit"`
	EntryDur float64 `json:"entry_dur,omitempty"` // 入场预算（秒，0.2–1.0；缺省 0.4）
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

	// 迷你真段（样张升级）：同一设计系统的 2-4s 动态小样，与正式段走同一份场景
	// 契约 + 同一台渲染器。ClipHTML 落 style/clips/clip-N.html（场景片段），
	// Video = 渲出的 mp4（相对项目根）。两者为空 = 该条降级静态预览（生成/渲染
	// 失败，原因记 ClipNote）；老文件无这些字段，读侧一律按可选处理。
	ClipHTML string `json:"clip_html,omitempty"`
	Video    string `json:"video,omitempty"`
	ClipNote string `json:"clip_note,omitempty"`
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
	// ↓ 动效编排（设计阶段的结构化章节）：生动感的源头在这里定档——只给一句话
	// 档位时，模型会全片复用同一种浮入（实测即「PPT 感」的来源），故菜单/节奏/
	// 出场/运镜/转场必须分开设计。全部 omitempty：旧设计文件读侧不炸。
	Entrances  []MotionMove `json:"entrances,omitempty"`  // 入场手法菜单 3–5 条，手法之间必须一眼不同
	Rhythm     string       `json:"rhythm,omitempty"`     // 段内节奏下限：隔几秒必须有一个可见事件、旁白信息点如何驱动画面
	Exits      string       `json:"exits,omitempty"`      // 出场：何时退、怎么退（干脆不拖尾）
	Camera     string       `json:"camera,omitempty"`     // 运镜计划：全段持续的背景运动，幅度必须可感知
	Transition string       `json:"transition,omitempty"` // 段间转场政策：简报含转场特色时必须按特色机制实现
}

// MotionMove 入场手法菜单的一条：设计阶段现场发明、全片段落轮换取用——
// 「同一张卡片的 N 种亮相」正是段间呼应的来源（与 device 逐段变奏同理）。
type MotionMove struct {
	Name   string `json:"name"`    // 手法名，如「终端逐行点亮」「滑入揭示」「字卡打字机」
	How    string `json:"how"`     // 初始态→终态怎么补间（物理内：opacity/x/y/scale/rotation/count）
	UseFor string `json:"use_for"` // 适用：哪类元素/哪类版式（标题/面板组/数据卡/满幅场景）
}

// PostFXSpec 全片统一后期（0–1；全 0 或缺省 = 关）。设计阶段按本片气质决定
// 要不要这层质感（怀旧/胶片感可以开颗粒，干净的 UI/科技风开色差反而脏）。
type PostFXSpec struct {
	Vignette float64 `json:"vignette"` // 暗角
	Grain    float64 `json:"grain"`    // 胶片颗粒
	Chroma   float64 `json:"chroma"`   // 轻微色差
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
	// Layered 非空 = 本版式声明叠层（有意叠字手法）：写明哪层压哪层、为何可读
	// （衬底层低对比/前景层有实底）。场景 HTML 据此给上层块加 data-layout-allow-overlap；
	// 空 = 无叠层构图，该版式下任何叠压都按事故拦。渲染端钳子（clampOverlapDecls）
	// 只认设计里声明过叠层的片，声明外贴属性一律剥掉。
	Layered string `json:"layered,omitempty"`
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
	if len(ss.Samples) < 1 || len(ss.Samples) > 6 {
		return fmt.Errorf("样张数量须为 1–6，得到 %d", len(ss.Samples))
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
	// 自装字体记账校验：三字段齐 + 家族去重 + 上限封顶（样张才几张，字体超编
	// 说明模型在拿字体堆花样，不是在服务画面）。
	if len(ss.CustomFonts) > 6 {
		return fmt.Errorf("custom_fonts 最多 6 款（得到 %d）——目录与系统字体不够用才自装", len(ss.CustomFonts))
	}
	seenF := map[string]bool{}
	for i, cf := range ss.CustomFonts {
		if strings.TrimSpace(cf.Family) == "" || strings.TrimSpace(cf.URL) == "" {
			return fmt.Errorf("custom_fonts[%d] family/url 不能为空", i)
		}
		k := strings.ToLower(strings.TrimSpace(cf.Family))
		if seenF[k] {
			return fmt.Errorf("custom_fonts[%d] 家族 %q 重复", i, cf.Family)
		}
		seenF[k] = true
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
	// 动效编排四件套：只给档位+一句话时，模型全片复用同一种浮入（实测「PPT感」
	// 来源）——入场手法菜单/节奏下限/出场/运镜/转场必须分开设计，缺件打回重出。
	if len(ss.Motion.Entrances) < 3 || len(ss.Motion.Entrances) > 5 {
		return fmt.Errorf("motion.entrances 须为 3–5 条入场手法（得到 %d 条）——手法菜单是全片生动感的来源，只给一种=整片像PPT", len(ss.Motion.Entrances))
	}
	seen := map[string]bool{}
	for i, mv := range ss.Motion.Entrances {
		if strings.TrimSpace(mv.Name) == "" || strings.TrimSpace(mv.How) == "" || strings.TrimSpace(mv.UseFor) == "" {
			return fmt.Errorf("motion.entrances[%d] 三件齐（name/how/use_for）才可用", i)
		}
		if seen[mv.Name] {
			return fmt.Errorf("motion.entrances[%d] 手法名 %q 重复——菜单里的手法必须一眼不同", i, mv.Name)
		}
		seen[mv.Name] = true
	}
	if strings.TrimSpace(ss.Motion.Rhythm) == "" {
		return fmt.Errorf("motion.rhythm 为空——段内节奏下限（隔几秒一个可见事件、旁白信息点如何驱动画面）必须成文")
	}
	if strings.TrimSpace(ss.Motion.Exits) == "" {
		return fmt.Errorf("motion.exits 为空——出场何时退怎么退必须成文")
	}
	if strings.TrimSpace(ss.Motion.Camera) == "" {
		return fmt.Errorf("motion.camera 为空——运镜计划（持续背景运动+可感知幅度基准）必须成文")
	}
	if strings.TrimSpace(ss.Motion.Transition) == "" {
		return fmt.Errorf("motion.transition 为空——段间转场政策必须成文（黑场一瞬须论证才可用）")
	}
	if ss.Device == nil || strings.TrimSpace(ss.Device.Name) == "" ||
		strings.TrimSpace(ss.Device.Desc) == "" || strings.TrimSpace(ss.Device.PerSeg) == "" {
		return fmt.Errorf("缺 device（贯穿装置：名称/是什么/逐段运用方式三件齐）")
	}
	// 标准入场/收尾动画模板（转场载体，prep-agents-design §4）：新设计必须成文，
	// 一套模板一段内生效——逐段即兴发明转场是退役了的缝概念还魂。
	if ee := ss.EntryExit; ee == nil || strings.TrimSpace(ee.Entry) == "" || strings.TrimSpace(ee.Exit) == "" {
		return fmt.Errorf("缺 entry_exit（标准入场/收尾动画模板——转场的载体，一套模板一段内生效）")
	} else if d := ee.EntryDur; d != 0 && (d < 0.2 || d > 1.0) {
		return fmt.Errorf("entry_exit.entry_dur=%.2f 超范围 0.2–1.0 秒（入场动画计入段长带预算）", d)
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

// ── 段落制渲染单元（2026-10-29 换骨，para-transition-plan §5）──────────
// 渲染单位=段落（一单元一张 HTML/一个 mp4），分镜退居旁白/字幕/锚句层。
// units.json 由 produce 在 TTS 后构建；pipeline（边界分类/segmap/帧定位）与
// ai/assemble.py（单元挂载/合并音轨/单元字幕）同读这一份契约。

// RenderUnit 一个渲染单元：一整个段落（超限强制拆分的段落=同 ParaIdx 多个 Part，
// 拆点边界按段内 fade 叠化兜底）。
type RenderUnit struct {
	ID        string    `json:"id"`        // para01 / para03b（段落序 1 起 + 拆分后缀）
	ParaIdx   int       `json:"para"`      // 段落序（0 基；拆分各 Part 共用）
	Part      int       `json:"part"`      // 第几拆（1 起，未拆=1）
	Parts     int       `json:"parts"`     // 同段落共几拆
	Title     string    `json:"title"`     // 段落主题（拆分带「上/中/下」注）
	Chapter   string    `json:"chapter"`   // 章节角标（首分镜的章；overlay 用）
	Narration string    `json:"narration"` // 段落内全部分镜旁白拼接（句匹配按前缀游走）
	Dur       float64   `json:"dur"`       // Σ(旁白+收尾垫)；timeline 全长
	Segs      []UnitSeg `json:"segs"`
}

// UnitSeg 单元内的一个分镜格子：旁白贴格、画面穿格（plan §5.1）。
// 三要素（Key/Narration/Brief）随格子携带——段落作业书、字幕、锚句都按真分镜。
type UnitSeg struct {
	ID        string  `json:"id"`    // segNN（真分镜 id，字幕/TTS/锚句仍按它）
	Start     float64 `json:"start"` // 单元内格子起点 = Σ_{k<i}(旁白k+SegTail)
	End       float64 `json:"end"`
	NarrDur   float64 `json:"narr_dur"`  // 旁白时长（不含尾垫）
	Key       string  `json:"key"`       // 分镜标题（段落作业书各分镜信息行）
	Narration string  `json:"narration"` // 分镜旁白
	Brief     string  `json:"brief"`     // 分镜画面提示
}
