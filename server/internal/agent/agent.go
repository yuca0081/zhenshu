// Package agent：主管对话 Agent（ReAct 循环 + 工具调用，Go 侧驱动）。
// 护栏：① 工具受状态机门禁（越界调用被拒、原因回喂）② 循环上限 8 步
// ③ LLM 永不直接碰文件——只发起工具调用，Go 校验后执行。
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	openai "github.com/sashabaranov/go-openai"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/pipeline"
	"webpage-video-agent/server/internal/produce"
	"webpage-video-agent/server/internal/react"
	"webpage-video-agent/server/internal/store"
	"webpage-video-agent/server/internal/stylelib"
)

type Producer interface {
	Start(projectID string) (bool, error)
	Rework(projectID string, segIDs []string, instruction string) (bool, error)
	IsRunning(projectID string) bool
}

type Agent struct {
	DataDir  string
	Store    store.Store
	Lib      *stylelib.Library
	Emit     func(projectID, typ, stage, detail string)
	Producer Producer
	OnEvent  func(projectID, event, detail string) // 状态回写钩子（storyboard/style 等里程碑）
}

// ── 项目状态（从磁盘产物读，磁盘是唯一事实源）──────────────────

type State struct {
	Name           string
	HasManuscript  bool
	HasStoryboard  bool
	SegCount       int
	TotalHint      float64
	HasStyle       bool
	StyleDirection string
	StyleConfirmed bool
	HasVideo       bool
	VideoAt        time.Time
	// 三上游最近修改时间：任一晚于 VideoAt = 成片过期（api 层下发 video_stale，
	// 前端页签黄灯）。改稿/重出分镜/换风格不用改管线——文件时间戳即事实。
	ManuscriptAt time.Time
	StoryboardAt time.Time
	StyleAt      time.Time
	// 成片过期三轴（VideoAt 早于对应上游 mtime）：与 api 下发的 video_stale 是
	// 同一事实。模型侧必须同样可见——只喂前端会让 agent 拿过期成片当最新递给用户。
	Stale struct {
		Manuscript bool `json:"manuscript"`
		Storyboard bool `json:"storyboard"`
		Style      bool `json:"style"`
	}
	Producing bool
	// 最近一轮制作终态（manifest 里 produce/rework 的终态，含 interrupted）。
	// 不注入它，agent 会拿失败前渲染的旧成片把失败轮幻觉成「已完成」。
	LastEvent  string
	LastDetail string
	LastAt     time.Time
}

func (a *Agent) State(id string) State {
	p := pipeline.NewProject(a.DataDir, id)
	st := State{Producing: a.Producer != nil && a.Producer.IsRunning(id)}
	// 文稿门：input/article.txt（创建即有）或管线产物 manuscript.json 任一存在
	if b, err := os.ReadFile(p.Artifact("input/article.txt")); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		st.HasManuscript = true
	} else if b, err := os.ReadFile(p.Artifact("manuscripts/manuscript.json")); err == nil && len(b) > 0 {
		st.HasManuscript = true
	}
	if sb, err := p.LoadStoryboard(); err == nil {
		st.HasStoryboard = true
		st.SegCount = len(sb.Segments)
		st.TotalHint = sb.TotalHint()
	}
	var ss contract.StyleSamples
	if b, err := os.ReadFile(p.Artifact("style/style_samples.json")); err == nil && json.Unmarshal(b, &ss) == nil {
		st.HasStyle, st.StyleDirection, st.StyleConfirmed = true, ss.Direction, ss.Confirmed
	}
	if info, err := os.Stat(p.Artifact("renders/main.mp4")); err == nil {
		st.HasVideo = true
		st.VideoAt = info.ModTime()
	}
	// 三上游 mtime（文稿真源 article.txt 缺时回退管线产物；风格取样张+简报较晚者）
	if info, err := os.Stat(p.Artifact("input/article.txt")); err == nil {
		st.ManuscriptAt = info.ModTime()
	} else if info, err := os.Stat(p.Artifact("manuscripts/manuscript.json")); err == nil {
		st.ManuscriptAt = info.ModTime()
	}
	if info, err := os.Stat(p.Artifact("storyboards/storyboard.json")); err == nil {
		st.StoryboardAt = info.ModTime()
	}
	for _, f := range []string{"style/style_samples.json", "style/style_brief.json"} {
		if info, err := os.Stat(p.Artifact(f)); err == nil && info.ModTime().After(st.StyleAt) {
			st.StyleAt = info.ModTime()
		}
	}
	if st.HasVideo {
		st.Stale.Manuscript = st.VideoAt.Before(st.ManuscriptAt)
		st.Stale.Storyboard = st.VideoAt.Before(st.StoryboardAt)
		st.Stale.Style = st.VideoAt.Before(st.StyleAt)
	}
	if ev, detail, ts := produce.LastTerminal(p); ev != "" {
		st.LastEvent, st.LastDetail = ev, detail
		st.LastAt, _ = time.Parse(time.RFC3339, ts)
	}
	if b, err := os.ReadFile(p.Artifact("project.json")); err == nil {
		var pj struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(b, &pj)
		st.Name = pj.Name
	}
	return st
}

func (s State) summary() string {
	lines := []string{}
	add := func(ok bool, what string) {
		if ok {
			lines = append(lines, "✓ "+what)
		} else {
			lines = append(lines, "✗ "+what)
		}
	}
	add(s.HasManuscript, "文稿")
	if s.HasStoryboard {
		add(true, fmt.Sprintf("分镜（%d 段 / %.0f 秒）", s.SegCount, s.TotalHint))
	} else {
		add(false, "分镜")
	}
	if s.HasStyle {
		if s.StyleConfirmed {
			add(true, fmt.Sprintf("风格已确认（%s）", s.StyleDirection))
		} else {
			lines = append(lines, "⏸ 风格样张已出，等用户在舞台点击确认（硬门）："+s.StyleDirection)
		}
	} else {
		add(false, "风格样张")
	}
	// 最近一轮制作终态不看 HasVideo：首做失败没有旧成片，藏了它模型就看着
	// 三门全✓把失败轮幻觉成「制作还在后台跑」，永远不再开工。中断/取消同理——
	// 终态一律无条件汇报，靠消费方自行取舍必漏。
	failed := strings.HasSuffix(s.LastEvent, ".error")
	interrupted := strings.HasSuffix(s.LastEvent, ".interrupted")
	cancelled := strings.HasSuffix(s.LastEvent, ".cancelled")
	switch {
	case failed:
		lines = append(lines, fmt.Sprintf("✗ 最近一轮制作失败（%s）：%s", s.LastAt.Format("15:04"), pipeline.FirstLine(s.LastDetail, 140)))
		if s.HasVideo {
			lines = append(lines, "⚠ 舞台上若能播成片，那是失败前的旧版（不要当成本轮结果）")
		} else {
			lines = append(lines, "⚠ 成片从未出过，制作现在也没在跑。用户说「继续/修复」就调 start_production 重新开工（检查修复轮会在制作里自动跑）")
		}
	case interrupted:
		if s.Producing {
			// 开机对账已自动续跑（produce.AutoResume）：别再让用户/模型去「继续」——活已经在跑
			lines = append(lines, fmt.Sprintf("↻ 上次制作因服务重启中断（%s），开机时已自动续跑，现在正在跑", s.LastAt.Format("15:04")))
		} else {
			lines = append(lines, fmt.Sprintf("⏹ 上次制作因服务重启中断（%s），产物已保留。用户要继续就调 start_production，做完的段自动复用", s.LastAt.Format("15:04")))
		}
	case cancelled:
		lines = append(lines, fmt.Sprintf("⏹ 最近一轮制作被取消（%s）", s.LastAt.Format("15:04")))
	}
	if s.HasVideo {
		if axes := staleAxes(s); len(axes) > 0 {
			lines = append(lines, fmt.Sprintf("⚠ 成片是旧版（出片后改过 %s），重新制作才套用新内容", strings.Join(axes, "、")))
		} else if !failed && !interrupted && !cancelled {
			lines = append(lines, "✓ 成片已就绪")
		}
	}
	if s.Producing {
		lines = append(lines, "⏳ 制作管线运行中（成片/失败都会自动汇报到对话）")
	}
	return strings.Join(lines, "\n")
}

// staleAxes 成片过期的轴（中文名，空 = 未过期）。摘要与工具门禁共用同一判别，
// 不许各写一套——这正是防失败机制要收敛的那类漂移。
func staleAxes(s State) []string {
	var axes []string
	if s.Stale.Manuscript {
		axes = append(axes, "文稿")
	}
	if s.Stale.Storyboard {
		axes = append(axes, "分镜")
	}
	if s.Stale.Style {
		axes = append(axes, "风格")
	}
	return axes
}

// lastFailedOrInterrupted 最近一轮制作以失败或服务重启中断告终（整片重做的合法理由）。
func lastFailedOrInterrupted(s State) bool {
	return strings.HasSuffix(s.LastEvent, ".error") || strings.HasSuffix(s.LastEvent, ".interrupted")
}

// videoFresh 成片就绪且为最新（无过期轴、无失败/中断终态）——此时整片重做挡回
// rework；旧版/失败/中断后 start_production 放行（与舞台「按最新内容重做」同权）。
func videoFresh(s State) bool {
	return s.HasVideo && len(staleAxes(s)) == 0 && !lastFailedOrInterrupted(s)
}

// reworkStaleNote 文稿/分镜过期时 rework 结果语必须带上的边界说明（只重画不重配）。
func reworkStaleNote(s State) string {
	if !s.HasVideo || (!s.Stale.Manuscript && !s.Stale.Storyboard) {
		return ""
	}
	return "。注意：文稿/分镜在出片后改过——换旁白/换内容要整片重做（start_production），rework 只重画不重配"
}

// selectedStyle 建项目时选定的风格库条目（project.json style_id，旧字段 stylepack_id 兜底）。
func (a *Agent) selectedStyle(p *pipeline.Project) *stylelib.Style {
	b, err := os.ReadFile(p.Artifact("project.json"))
	if err != nil {
		return nil
	}
	var meta struct {
		StyleID string `json:"style_id"`
		// StylePackID 旧字段名，老项目兼容读取
		StylePackID string `json:"stylepack_id"`
	}
	if json.Unmarshal(b, &meta) != nil {
		return nil
	}
	id := meta.StyleID
	if id == "" {
		id = meta.StylePackID
	}
	if id == "" {
		return nil
	}
	st, ok := a.Lib.Get(id)
	if !ok || !st.Published {
		return nil
	}
	return st
}

// draftedStyle 幂等探测（extract_stylepack 前行）：style_samples.style_id 命中库内
// 条目（本就出自库）或同项目已起草过 → 返回该条目；nil = 需要走 LLM 起草。
func (a *Agent) draftedStyle(p *pipeline.Project) *stylelib.Style {
	if st := a.selectedStyle(p); st != nil {
		return st
	}
	styles, err := a.Lib.List()
	if err != nil {
		return nil
	}
	for i := range styles {
		if styles[i].OriginProject == p.ID {
			return &styles[i]
		}
	}
	return nil
}

// designDigest 本片设计系统人话摘要（extract_stylepack 的打标原料）。
func designDigest(p *pipeline.Project) (string, error) {
	b, err := os.ReadFile(p.Artifact("style/style_samples.json"))
	if err != nil {
		return "", fmt.Errorf("项目无设计系统（style_samples.json 缺失）")
	}
	var ss contract.StyleSamples
	if err := json.Unmarshal(b, &ss); err != nil {
		return "", err
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "- 方向：%s\n- 概念：%s\n", ss.Direction, ss.Concept)
	for i, sk := range ss.EffectiveSkins() {
		label := "皮肤"
		if i == 1 {
			label = "副皮肤"
		}
		fmt.Fprintf(&sb, "- %s「%s」：底 %s / 字 %s / 强调 %s；css %s\n",
			label, sk.Name, sk.Slots.BG, sk.Slots.TXT, sk.Slots.Accent, pipeline.FirstLine(sk.CSS, 200))
	}
	if ss.SkinRule != "" {
		fmt.Fprintf(&sb, "- 皮肤切换：%s\n", ss.SkinRule)
	}
	if f := ss.Fonts; f != nil {
		fmt.Fprintf(&sb, "- 字体：display %s / body %s / data %s\n", f.Display.Stack, f.Body.Stack, f.Data.Stack)
	}
	if m := ss.Motion; m != nil {
		fmt.Fprintf(&sb, "- 动效：%s，进场 %.2fs 落定 %.2fs 错峰 %.2fs——%s\n", m.Ease, m.DurIn, m.DurSettle, m.Stagger, m.Notes)
	}
	if d := ss.Device; d != nil {
		fmt.Fprintf(&sb, "- 贯穿装置「%s」：%s；逐段：%s\n", d.Name, d.Desc, d.PerSeg)
	}
	for _, l := range ss.Layouts {
		fmt.Fprintf(&sb, "- 版式 %s（%s）：%s\n", l.ID, l.Name, l.Desc)
	}
	if len(ss.AntiPatterns) > 0 {
		fmt.Fprintf(&sb, "- 负面清单：%s\n", strings.Join(ss.AntiPatterns, "、"))
	}
	return sb.String(), nil
}

// regenerateStyle 按用户修改要求重新出样张：旧样张备份防翻车，要求写 input/style_notes.txt，
// 跑 style_samples 阶段（API 直调；未配 LLM 时降级为会话请求文件）。
// 失败还原旧样张——项目任何时刻都不留「样张空洞」；成功则清库内指向与截图缓存。
func (a *Agent) regenerateStyle(ctx context.Context, p *pipeline.Project, instruction, out string) string {
	var prev contract.StyleSamples
	prevDir := ""
	if json.Unmarshal(readFileLoose(out), &prev) == nil {
		prevDir = prev.Direction
	}
	bak := out + ".bak"
	hadPrev := true
	if err := os.Rename(out, bak); err != nil {
		if _, serr := os.Stat(out); serr != nil {
			hadPrev = false // 首次出样张：无旧可备
		} else {
			return "备份旧样张失败：" + err.Error()
		}
	}
	restore := func(cause error) string {
		if !hadPrev {
			return "样张生成失败：" + cause.Error()
		}
		if rerr := os.Rename(bak, out); rerr != nil {
			return fmt.Sprintf("样张重生成失败，且还原旧样张也失败（备份在 %s）：%v（原因：%v）", bak, rerr, cause)
		}
		return "样张重生成失败（已还原旧样张）：" + cause.Error()
	}
	_ = os.Remove(p.LLMOutput("style_samples"))
	for i := 0; i < 3; i++ { // 截图缓存一并清掉，防 produce 把旧图配新样张
		_ = os.Remove(p.Artifact(fmt.Sprintf("style/sample-%d.png", i)))
	}
	_ = os.RemoveAll(p.Artifact("style/clips"))         // 迷你真段随旧样张作废（片段/清单/mp4 一锅端）
	_ = os.RemoveAll(p.Artifact(".hf-clip"))            // 小样单段工程壳同上
	_ = os.Remove(p.Artifact("style/skin.json"))        // 旧皮肤随旧样张作废，确认新样张时重落
	_ = os.Remove(p.Artifact("style/design.md"))        // 旧设计系统文档同样作废（确认新样张时机械重渲）
	_ = os.Remove(p.Artifact("style/style_brief.json")) // 用户点名换方向：强参考简报一并作废
	notes := "原风格方向：" + prevDir + "\n用户修改要求：" + instruction
	if err := os.MkdirAll(filepath.Dir(p.Artifact("input/style_notes.txt")), 0o755); err != nil {
		return restore(err)
	}
	if err := os.WriteFile(p.Artifact("input/style_notes.txt"), []byte(notes), 0o644); err != nil {
		return restore(err)
	}
	if err := pipeline.Run(ctx, p, "style_samples"); err != nil {
		return restore(err)
	}
	_ = os.Remove(bak)
	// 自拟重生成后不再指向库内风格（样张已不是那一包的；简报也已在开头清掉）
	var pj struct {
		StylePackID string `json:"stylepack_id"`
	}
	if b, rerr := os.ReadFile(p.Artifact("project.json")); rerr == nil && json.Unmarshal(b, &pj) == nil && pj.StylePackID != "" {
		_ = pipeline.SetProjectField(p, "stylepack_id", "")
	}
	_ = pipeline.SetProjectField(p, "style_id", "")
	dir := prevDir
	var ss contract.StyleSamples
	if b, rerr := os.ReadFile(out); rerr == nil && json.Unmarshal(b, &ss) == nil && ss.Direction != "" {
		dir = ss.Direction
	}
	if a.OnEvent != nil {
		a.OnEvent(p.ID, "style_draft", dir)
	}
	msg := fmt.Sprintf("样张已按要求「%s」重新生成，新方向：%s%s（舞台可预览）。confirmed 已复位——用户须在舞台重新点「确认风格」才能开工", instruction, dir, clipSummary(&ss))
	if a.State(p.ID).HasVideo {
		msg += "；已有成片仍是旧风格，重新制作才会套用"
	}
	return msg
}

// styleDraftReport 首次出样张的工具回话：方向/张数/小样条数如实报，硬门提醒固定带。
func (a *Agent) styleDraftReport(p *pipeline.Project, out, source string) string {
	var ss contract.StyleSamples
	dir := ""
	if b, err := os.ReadFile(out); err == nil && json.Unmarshal(b, &ss) == nil && ss.Direction != "" {
		dir = ss.Direction
	}
	if a.OnEvent != nil {
		a.OnEvent(p.ID, "style_draft", dir)
	}
	return fmt.Sprintf("风格样张已出（%s）：%s%s。硬门——用户在舞台点头确认后才能开工", source, dir, clipSummary(&ss))
}

// clipSummary 迷你真段条数的事实摘要（工具回话用）：几条就报几条，没出成
// 不装没出——舞台里每条样张下有降级原因，对话侧如实转述。
func clipSummary(ss *contract.StyleSamples) string {
	n, clips := 0, 0
	if ss != nil {
		n = len(ss.Samples)
		for _, s := range ss.Samples {
			if s.Video != "" {
				clips++
			}
		}
	}
	if n == 0 {
		return ""
	}
	if clips == 0 {
		return fmt.Sprintf("（%d 张静态样张；动态小样这次没出成，原因见舞台每条样张下方）", n)
	}
	if clips < n {
		return fmt.Sprintf("（%d 张，其中 %d 条带迷你真段小样，其余降级静态、原因见舞台）", n, clips)
	}
	return fmt.Sprintf("（%d 张全部带迷你真段小样，所见即所得）", n)
}

// readFileLoose 宽松读：读失败返回 nil 不报错。调用方按「无此文件」处理
// （如 regenerateStyle 探测旧样张方向，nil 时 Unmarshal 必败即当无旧方向）；
// 名字里不带 must——它从不 panic，吞错是特性不是事故。
func readFileLoose(path string) []byte {
	b, _ := os.ReadFile(path)
	return b
}

// resolveSegment 用户话里的段引用（3 / 段3 / seg03）→ 分镜里的段。
// ResolveSegment 段引用解析（段号/段id/「第N段」→ segID+人话标签；API 一键重做复用）。
func (a *Agent) ResolveSegment(p *pipeline.Project, ref string) (string, string, error) {
	return a.resolveSegment(p, ref)
}

func (a *Agent) resolveSegment(p *pipeline.Project, ref string) (id, label string, err error) {
	sb, lerr := p.LoadStoryboard()
	if lerr != nil {
		return "", "", fmt.Errorf("分镜未生成")
	}
	ref = strings.TrimSpace(ref)
	n := -1
	if strings.HasPrefix(ref, "seg") {
		if s, e := strconv.Atoi(strings.TrimPrefix(ref, "seg")); e == nil {
			n = s
		}
	} else {
		// 「第3段」「第3」「段3」「3」都收：剥前缀「第/段」再剥后缀「段」
		s, e := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(ref, "段"), "第"), "段"))
		if e == nil {
			n = s
		}
	}
	if n > 0 {
		want := fmt.Sprintf("seg%02d", n)
		for _, seg := range sb.Segments {
			if seg.ID == want {
				return seg.ID, fmt.Sprintf("段%d「%s」", seg.Idx, seg.Key), nil
			}
		}
	}
	for _, seg := range sb.Segments {
		if seg.ID == ref {
			return seg.ID, fmt.Sprintf("段%d「%s」", seg.Idx, seg.Key), nil
		}
	}
	return "", "", fmt.Errorf("段 %q 不在分镜里（共 %d 段）", ref, len(sb.Segments))
}

// ── ReAct 循环 ───────────────────────────────────────────────

const maxSteps = 8

func (a *Agent) systemPrompt(id string, s State) string {
	return fmt.Sprintf(`你是「帧述」的主管 Agent——靠谱的剪辑搭档：懂行、话少、主动汇报、给专业建议但尊重用户决定。

当前项目「%s」（ID: %s）状态（唯一事实源，说话前先对照它）：
%s

工作准则：
- 上面这份状态是唯一事实，说话前先对照它：「成片已就绪」才给下载地址 /api/projects/%s/video/main.mp4；状态说「旧版/失败/中断/未出过」就按它说，制作没在跑（无 ⏳ 行）就绝不说「在跑/渲染中」；不确定先 read_project 核对，禁止按聊天历史想象状态。
- 三前置硬门：文稿、分镜、风格样张（用户确认）——齐了才能 start_production，代码强制，别硬闯；缺哪项就引导用户补齐——文稿直接粘进对话由你 save_manuscript 存下，分镜/风格点对话栏上方的同名标签。成片就绪且未过期时不再 start_production；成片过期（「成片是旧版」）或上次失败/中断后用户要重做，就调 start_production 整片重来。
- 换风格/调样张：调 draft_style_samples 并把用户要求原样放进 instruction，样张没重新生成就不能说「已出新样张」——没做工具调用就当没做。
- 成片就绪后：改某段画面用 rework（整段重生成画面、音频不动）；用户带 📎 段/元素引用的消息几乎都是 rework 意图。元素引用指名了改哪个元素，rework 的 instruction 里点名它（人话名，必要时带元素 id 与时刻）。要精确定位段与元素先 list_segments。换配音音色用 set_voice（空参列音色，设置后自动整片重制）。出片后主动 extract_stylepack 沉淀风格（一次就够，已提炼过不必重复）。
- 默认自主连贯：能做的直接做（出分镜→出样张→自检一路做下去），到用户门（风格确认）停下说清楚等什么。
- 涉及时效性事实（新闻/版本/价格/数据）先用 web_search 核实再写进文稿/分镜；搜索不可用就说明信息可能过时，别编。
- web_search 消耗套餐月度额度（每月仅约 200 次，与其他 MCP 共享）——非必要不搜：一轮对话合并成尽量少的精准查询；空结果最多换个词再试一次，仍空就如实说并标明信息可能过时，不连环重试。
- 用户贴了链接、或要细读某条搜索结果时用 fetch_url 抓网页正文——它免费不占额度，能用它就别为看内容再搜一次；读不到正文就如实说，让用户直接粘贴。
- 工具被拒就换路或向用户解释，不重复硬试；每轮最多 %d 步。
- 回复短：一段话讲清做了什么、下一步是什么，不堆术语、不复述参数。`, s.Name, id, s.summary(), id, maxSteps)
}

// Run 一轮对话：userText 与 refs 已入库。Agent 循环产出最终回复入库并广播。
func (a *Agent) Run(projectID, userText string, refs []store.Ref) {
	// 兜底：任何 panic 也必须给对话框一个交代，不能静默死掉
	defer func() {
		if r := recover(); r != nil {
			a.finish(projectID, fmt.Sprintf("这一轮处理中断了：%v", r), "error")
		}
	}()
	p := pipeline.NewProject(a.DataDir, projectID)
	prov, err := llm.FromEnv(llm.RoleDialogue)
	if err != nil {
		a.finish(projectID, "当前未配置 LLM API（LLM_API_KEY），我只能看不能想。配好 .env 再聊。", "error")
		return
	}
	st := a.State(projectID) // 一轮只全量扫一次盘；system 提示与状态注入吃同一份
	msgs := []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleSystem, Content: a.systemPrompt(projectID, st)}}
	hist := a.history(projectID, 10)
	// 刚落库的这条用户消息在历史里已有一份，下面的状态注入还会带一遍——
	// 去掉历史里那份，同一句话不在上下文里出现两次
	for i := len(hist) - 1; i >= 0; i-- {
		if hist[i].Role == openai.ChatMessageRoleUser {
			if hist[i].Content == userText {
				hist = append(hist[:i], hist[i+1:]...)
			}
			break
		}
	}
	msgs = append(msgs, hist...)
	// 实时状态随最新 user 消息注入（模型对最新消息权重最高，防聊天历史锚定幻觉）
	msgs = append(msgs, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: fmt.Sprintf("［系统·实时项目状态，以此为准］\n%s\n%s\n用户消息：%s",
		st.summary(), a.formatRefs(p, refs), userText)})

	// 对话轮级时限：卡死的 LLM 调用（上游挂住时 10min×3 重试）会把 turnMu 下的
	// 后续对话轮全部拖成沉默；超时给用户明确交代
	turnCtx, cancelTurn := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancelTurn()

	core := react.New(prov, a.reactTools(p), maxSteps, react.Hooks{
		OnCall: func(name, args string) {
			a.Emit(p.ID, "tool_call", "", name+" "+compactArgs(args))
		},
		OnFinish: func(name, args, result string, rejected bool) {
			a.saveToolMsg(p.ID, name, args, result)
			if rejected {
				a.Emit(p.ID, "tool_result", "", name+" "+result)
			} else {
				a.Emit(p.ID, "tool_result", "", name+" → "+pipeline.FirstLine(result, 120))
			}
		},
	})
	final, err := core.Run(turnCtx, msgs)
	if err != nil {
		a.finish(projectID, "模型调用失败："+err.Error(), "error")
		return
	}
	if final == "" {
		final = "我先停一下：这轮动作做完了，等你的下一步指示。"
	}
	a.finish(projectID, final, "text")
}

// reactTools 把包级工具表适配成 react 核心：门禁按实时状态求值，执行体绑当前项目。
// 工具表本体不动——签名带宿主上下文。生产路径只有 react 循环这一个消费方，
// 直接遍历 tools 表（findTool/toolSchemas 无生产调用方，仅测试直测单项用）。
func (a *Agent) reactTools(p *pipeline.Project) []react.Tool {
	out := make([]react.Tool, 0, len(tools))
	for i := range tools {
		t := &tools[i]
		out = append(out, react.Tool{
			Def:  t.def,
			Gate: func() string { return t.gate(a.State(p.ID)) },
			Run:  func(ctx context.Context, args string) string { return t.run(ctx, a, p, args) },
		})
	}
	return out
}

// formatRefs 引用卡片 → 模型可读的引用块（空则空串，不占位）。
// 时刻引用换算段内偏移（口径与 MP4 拼接一致：配音时长+0.35 尾垫），模型能精确理解"这里"。
func (a *Agent) formatRefs(p *pipeline.Project, refs []store.Ref) string {
	if len(refs) == 0 {
		return ""
	}
	starts := segStarts(p)
	var b strings.Builder
	b.WriteString("用户引用（点选自舞台，精确上下文，优先于口头描述）：\n")
	for _, r := range refs {
		line := fmt.Sprintf("- 段%d", r.Idx)
		if r.Key != "" {
			line += fmt.Sprintf("「%s」", r.Key)
		}
		if r.T > 0 {
			line += fmt.Sprintf(" · 全片 %.1fs（第 %d 帧）", r.T, int(r.T*contract.FPS+0.5))
			if start, ok := starts[r.Idx]; ok && r.T >= start {
				line += fmt.Sprintf("，该段第 %.1fs", r.T-start)
			}
		}
		if r.ElementName != "" {
			line += fmt.Sprintf(" · 元素「%s」", r.ElementName)
			if r.ElementID != "" {
				line += fmt.Sprintf("（%s）", r.ElementID)
			}
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// segStarts 段起点表：audio duration_s + 尾垫累计（contract.SegTail 三端同口径）；
// 无音频对齐产物时退回分镜 duration_hint。
func segStarts(p *pipeline.Project) map[int]float64 {
	sb, err := p.LoadStoryboard()
	if err != nil {
		return nil
	}
	durs := map[string]float64{}
	var meta struct {
		Voices []struct {
			ID        string  `json:"id"`
			DurationS float64 `json:"duration_s"`
		} `json:"voices"`
	}
	if b, rerr := os.ReadFile(p.Artifact("audio_meta.json")); rerr == nil && json.Unmarshal(b, &meta) == nil {
		for _, v := range meta.Voices {
			durs[v.ID] = v.DurationS + contract.SegTail
		}
	}
	starts := map[int]float64{}
	acc := 0.0
	for _, seg := range sb.Segments {
		starts[seg.Idx] = acc
		if d, ok := durs[seg.ID]; ok && d > 0 {
			acc += d
		} else {
			acc += seg.DurationHint
		}
	}
	return starts
}

func (a *Agent) finish(projectID, content, typ string) {
	if typ == "error" {
		_ = a.Store.SaveMsg(&store.Msg{ProjectID: projectID, Role: "agent", Type: "error", Content: content})
	} else {
		_ = a.Store.SaveMsg(&store.Msg{ProjectID: projectID, Role: "agent", Type: "text", Content: content})
	}
	a.Emit(projectID, "agent_msg", "", content)
}

// setDoneStatus 终态落两处：project.json（磁盘事实源，projectView 读它）+ PG 行（首页列表读它）。
func (a *Agent) setDoneStatus(projectID, status string) {
	WriteProjectStatus(a.DataDir, a.Store, projectID, status)
}

// WriteProjectStatus 项目状态落两处：project.json（磁盘事实源，projectView 读它）
// + Store 行（首页列表读它）。agent 终态与 api 状态更新此前各持一份逐行相同的实现，
// 改一处漏一处就是状态分叉——收敛到这一个入口（api 包引用，无 import 环）。
func WriteProjectStatus(dataDir string, st store.Store, projectID, status string) {
	_ = pipeline.SetProjectField(pipeline.NewProject(dataDir, projectID), "status", status)
	_ = st.UpdateStatus(projectID, status)
}

// PostNotice 制作运行器的非终态插话（如开机自动续跑通知）：落聊天框、推 SSE，
// 但不动项目状态——它不是终态，只是让用户知道流程自己在走，不用他做任何事。
func (a *Agent) PostNotice(projectID, content string) {
	_ = a.Store.SaveMsg(&store.Msg{ProjectID: projectID, Role: "agent", Type: "text", Content: content})
	a.Emit(projectID, "agent_msg", "", content)
}

// NotifyResult 制作管线终态回聊天。制作/重做是对话轮结束后异步跑的，
// 成败必须由这里兜底写进对话框，否则用户无从得知结果（OnDone 由 main 接线到本方法）。
// 同时回写项目状态：启动时标了 producing，终态不回写会让项目列表永远「制作中」。
func (a *Agent) NotifyResult(projectID, status, detail string) {
	switch status {
	case "video":
		a.setDoneStatus(projectID, "video")
	case "cancelled", "failed":
		// 回到出片前的态：有成片回 video（重做中断旧片仍在），否则回 style（三前置已齐）
		if _, err := os.Stat(pipeline.NewProject(a.DataDir, projectID).Artifact("renders/main.mp4")); err == nil {
			a.setDoneStatus(projectID, "video")
		} else {
			a.setDoneStatus(projectID, "style")
		}
	}
	var content, typ string
	switch status {
	case "video":
		content = "✅ 成片就绪：" + detail + "，舞台可直接预览。"
		typ = "text"
	case "cancelled":
		content = "⏹ 已取消：" + detail
		typ = "text"
	default: // failed
		// 对话栏只说人话：✗ 诊断 / lint 转储 / CSS 选择器用户读不了也修不了，
		// 一律不上对话（技术细节留在 manifest 与事实清单，模型侧随时可查）。
		// 走到这说明自动重跑已烧完（真失败才惊动用户），「继续」走 start_production
		// 重跑，修复轮拿的是全新诊断，不靠旧报错。
		note := ""
		if strings.Contains(detail, "已自动重跑") {
			note = "自动重跑也没能救回来——"
		}
		content = "❌ 这次制作没能跑完：" + note + friendlyFailReason(detail) + "\n回我一句「继续」，我就接着修。"
		typ = "error"
	}
	_ = a.Store.SaveMsg(&store.Msg{ProjectID: projectID, Role: "agent", Type: typ, Content: content})
	a.Emit(projectID, "agent_msg", "", content)
}

// friendlyFailReason 把失败细节翻译成一句用户能懂的话；中间过程（质检没过但
// 修复轮还在跑）本就不进对话，只有流程真停下来才走到这里。
func friendlyFailReason(detail string) string {
	switch {
	case strings.Contains(detail, "context deadline exceeded"):
		return "AI 服务这次响应太慢，等超时了"
	case strings.Contains(detail, "三轮未过"):
		// check 与场景契约的修复轮上限都是三轮，同因：AI 画面质量有随机性
		return "画面自动质检三轮都没通过——AI 画面的质量有随机性，重跑一次通常就能过"
	case strings.Contains(detail, "组装失败"):
		return "画面拼装环节出了问题"
	case strings.Contains(detail, "渲染失败"), strings.Contains(detail, "render failed"):
		return "渲染环节出了问题"
	default:
		return "中途出了点技术问题，已停下"
	}
}

func (a *Agent) saveToolMsg(projectID, name, args, result string) {
	_ = a.Store.SaveMsg(&store.Msg{
		ProjectID: projectID, Role: "agent", Type: "tool_call",
		Content: name, ToolName: name, ToolArgs: args, ToolResult: result,
	})
}

func (a *Agent) history(projectID string, n int) []openai.ChatCompletionMessage {
	// 尾部窗口取最近 100 条：ListMsgs 恒从最旧往后取，消息一超量最新对话
	// 永远进不了 LLM 上下文（模型只记得项目头 100 条，越聊越失忆）
	msgs, err := a.Store.ListMsgsTail(projectID, 100)
	if err != nil {
		return nil
	}
	var hist []openai.ChatCompletionMessage
	for _, m := range msgs {
		if m.Role == "system" {
			continue
		}
		// error 类型放行 agent 的失败/中断通告：它是对话里发生过的事实，被滤掉
		// 模型就对自己报过的失败失忆（状态摘要是唯一感知腿，断一条即全盲）。
		// tool_call 仍排除——工具每轮按实时状态重新门禁执行，旧结果只会误导。
		if m.Type != "text" && !(m.Role == "agent" && m.Type == "error") {
			continue
		}
		role := openai.ChatMessageRoleUser
		if m.Role == "agent" {
			role = openai.ChatMessageRoleAssistant
		}
		hist = append(hist, openai.ChatCompletionMessage{Role: role, Content: m.Content})
	}
	if len(hist) > n {
		hist = hist[len(hist)-n:]
	}
	return hist
}

func compactArgs(args string) string {
	var m map[string]any
	if json.Unmarshal([]byte(args), &m) == nil {
		b, _ := json.Marshal(m)
		return string(b)
	}
	return args
}
