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
	RootDir  string // 仓库根（ai/ 脚本）
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
	Producing      bool
	// 最近一轮制作终态（manifest 里 produce/rework 的 done|error|cancelled）。
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
	if ev, detail, ts := lastProduceResult(p); ev != "" {
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
	if s.HasVideo {
		if s.LastEvent != "" && strings.HasSuffix(s.LastEvent, ".error") {
			lines = append(lines, fmt.Sprintf("✗ 最近一轮制作失败（%s）：%s", s.LastAt.Format("15:04"), firstLine(s.LastDetail, 140)))
			lines = append(lines, "⚠ 舞台上若能播成片，那是失败前的旧版（不要当成本轮结果）")
		} else if s.LastEvent != "" && strings.HasSuffix(s.LastEvent, ".cancelled") {
			lines = append(lines, fmt.Sprintf("⏹ 最近一轮制作被取消（%s）", s.LastAt.Format("15:04")))
		} else {
			lines = append(lines, "✓ 成片已就绪")
		}
	}
	if s.Producing {
		lines = append(lines, "⏳ 制作管线运行中（成片/失败都会自动汇报到对话）")
	}
	return strings.Join(lines, "\n")
}

// lastProduceResult 读 manifest 最近一条制作终态事件（produce/rework 的 done|error|cancelled）。
// 事件名与 produce 包共协议（produce.Event* 常量），不再手写魔法字符串。
func lastProduceResult(p *pipeline.Project) (event, detail, ts string) {
	b, err := os.ReadFile(p.Artifact("manifest.jsonl"))
	if err != nil {
		return "", "", ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		var e struct {
			Event  string `json:"event"`
			Detail string `json:"detail"`
			TS     string `json:"ts"`
		}
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		switch e.Event {
		case produce.EventProduceDone, produce.EventProduceError, produce.EventProduceCancelled,
			produce.EventReworkDone, produce.EventReworkError, produce.EventReworkCancelled:
			event, detail, ts = e.Event, e.Detail, e.TS
		}
	}
	return event, detail, ts
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

// regenerateStyle 按用户修改要求重新出样张：旧样张备份防翻车，要求写 input/style_notes.txt，
// 跑 style_samples 阶段（API 直调；未配 LLM 时降级为会话请求文件）。
// 失败还原旧样张——项目任何时刻都不留「样张空洞」；成功则清库内指向与截图缓存。
func (a *Agent) regenerateStyle(p *pipeline.Project, instruction, out string) string {
	var prev contract.StyleSamples
	prevDir := ""
	if json.Unmarshal(mustReadFile(out), &prev) == nil {
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
	_ = os.Remove(p.Artifact("style/skin.json"))   // 旧皮肤随旧样张作废，确认新样张时重落
	_ = os.Remove(p.Artifact("style/design.md"))   // 旧设计系统文档同样作废（确认新样张时机械重渲）
	notes := "原风格方向：" + prevDir + "\n用户修改要求：" + instruction
	if err := os.MkdirAll(filepath.Dir(p.Artifact("input/style_notes.txt")), 0o755); err != nil {
		return restore(err)
	}
	if err := os.WriteFile(p.Artifact("input/style_notes.txt"), []byte(notes), 0o644); err != nil {
		return restore(err)
	}
	if err := pipeline.Run(context.Background(), p, "style_samples"); err != nil {
		return restore(err)
	}
	_ = os.Remove(bak)
	// 自拟重生成后不再指向库内风格包（样张已不是那一包的）
	if b, rerr := os.ReadFile(p.Artifact("project.json")); rerr == nil {
		var meta map[string]any
		if json.Unmarshal(b, &meta) == nil {
			if id, ok := meta["stylepack_id"].(string); ok && id != "" {
				meta["stylepack_id"] = ""
				if nb, merr := json.MarshalIndent(meta, "", "  "); merr == nil {
					_ = pipeline.WriteFileAtomic(p.Artifact("project.json"), nb, 0o644)
				}
			}
		}
	}
	dir := prevDir
	var ss contract.StyleSamples
	if b, rerr := os.ReadFile(out); rerr == nil && json.Unmarshal(b, &ss) == nil && ss.Direction != "" {
		dir = ss.Direction
	}
	if a.OnEvent != nil {
		a.OnEvent(p.ID, "style_draft", dir)
	}
	msg := fmt.Sprintf("样张已按要求「%s」重新生成，新方向：%s（舞台可预览）。confirmed 已复位——用户须在舞台重新点「确认风格」才能开工", instruction, dir)
	if a.State(p.ID).HasVideo {
		msg += "；已有成片仍是旧风格，重新制作才会套用"
	}
	return msg
}

func mustReadFile(path string) []byte {
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
		s, e := strconv.Atoi(strings.TrimPrefix(strings.TrimPrefix(ref, "段"), "第"))
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

// ── 工具定义 ─────────────────────────────────────────────────

type tool struct {
	def  openai.Tool
	gate func(s State) string // 返回空串 = 放行；否则拒绝原因（回喂）
	run  func(a *Agent, p *pipeline.Project, args string) string
}

func noGate(State) string { return "" }

// voiceAliases 项目音色别名（与 ai/tts_align.py VOICE_ALIASES 保持一致，
// voice_alias_test.go 钉住两表不漂移）。
var voiceAliases = map[string]string{
	"晓晓": "zh-CN-XiaoxiaoNeural",
	"晓伊": "zh-CN-YiyiNeural",
	"云健": "zh-CN-YunjianNeural",
	"云希": "zh-CN-YunxiNeural",
	"云扬": "zh-CN-YunyangNeural",
	"云野": "zh-CN-YunyeNeural",
}

// cosyVoiceNames CosyVoice2 内置音色短名（与 ai/tts_align.py COSY_VOICES 同步；
// 付费档 TTS，需 .env 配 SILICONFLOW_API_KEY）。
var cosyVoiceNames = map[string]bool{
	"alex": true, "benjamin": true, "charles": true, "david": true,
}

// isEdgeVoice / isCosyVoice 音色 id 判别：Edge 完整 id（zh-CN-YunjianNeural）
// 或 CosyVoice 短名/完整 id（alex / FunAudioLLM/CosyVoice2-0.5B:alex）。
func isEdgeVoice(id string) bool {
	return strings.Contains(id, "-") && strings.Contains(id, "Neural")
}

func isCosyVoice(id string) bool {
	if strings.HasPrefix(id, "FunAudioLLM/CosyVoice") {
		return true
	}
	return cosyVoiceNames[id]
}

var tools = []tool{
	{
		def: toolDef("draft_storyboard", "根据文稿生成分镜表（LLM 作业，自动完成）", nil),
		gate: func(s State) string {
			if !s.HasManuscript {
				return "还没有文稿，无法出分镜"
			}
			return ""
		},
		run: func(a *Agent, p *pipeline.Project, _ string) string {
			if err := pipeline.Run(context.Background(), p, "storyboard"); err != nil {
				return "分镜生成失败：" + err.Error()
			}
			sb, _ := p.LoadStoryboard()
			if a.OnEvent != nil {
				a.OnEvent(p.ID, "storyboard", "")
			}
			return fmt.Sprintf("分镜已生成：%d 段 / 约 %.0f 秒。思路：%s", len(sb.Segments), sb.TotalHint(), sb.Plan)
		},
	},
	{
		def: toolDef("update_storyboard", "按用户要求重新生成分镜表", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"instruction": map[string]any{"type": "string", "description": "用户对分镜的修改要求（如：每段更短、合并前两段）"},
			},
			"required": []string{"instruction"},
		}),
		gate: func(s State) string {
			if !s.HasManuscript {
				return "还没有文稿"
			}
			if s.Producing {
				return "制作运行中，先等本条完成"
			}
			return ""
		},
		run: func(a *Agent, p *pipeline.Project, args string) string {
			var in struct {
				Instruction string `json:"instruction"`
			}
			_ = json.Unmarshal([]byte(args), &in)
			if strings.TrimSpace(in.Instruction) == "" {
				return "缺少修改要求"
			}
			_ = os.WriteFile(p.Artifact("input/storyboard_notes.txt"), []byte(in.Instruction), 0o644)
			_ = os.Remove(p.Artifact("storyboards/storyboard.json"))
			_ = os.Remove(p.LLMOutput("storyboard"))
			if err := pipeline.Run(context.Background(), p, "storyboard"); err != nil {
				return "分镜重生成失败：" + err.Error()
			}
			sb, _ := p.LoadStoryboard()
			if a.OnEvent != nil {
				a.OnEvent(p.ID, "storyboard", "")
			}
			return fmt.Sprintf("分镜已按「%s」重生成：%d 段 / 约 %.0f 秒（若已有音频/画面需重新制作才会生效）",
				in.Instruction, len(sb.Segments), sb.TotalHint())
		},
	},
	{
		def: toolDef("draft_style_samples", "产出本片设计系统与风格样张（皮肤1-2套/字体角色/动效语言/贯穿装置/版式/负面清单 + 1-3张样张）：首次出样张（风格库或默认包冷启动）；样张已存在时必须带 instruction 按用户要求重新生成", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"instruction": map[string]any{"type": "string", "description": "用户对风格的修改/新方向要求（如：更暖色调、加猫爪猫耳Q版点缀；换全新方向也写在这）。首次出样张可省略"},
			},
		}),
		gate: func(s State) string {
			if !s.HasStoryboard {
				return "先出分镜，再出风格样张"
			}
			return ""
		},
		run: func(a *Agent, p *pipeline.Project, args string) string {
			var in struct {
				Instruction string `json:"instruction"`
			}
			_ = json.Unmarshal([]byte(args), &in)
			out := p.Artifact("style/style_samples.json")
			if strings.TrimSpace(in.Instruction) != "" {
				// 带要求（首次或重生成一律走这条）：冷启动默认包不符合用户明确的方向，
				// 直接按要求现写——样张与风格文件（skin）同产（§12.2）
				return a.regenerateStyle(p, in.Instruction, out)
			}
			if _, err := os.Stat(out); err == nil {
				return "风格样张已出，等用户在舞台点击「确认风格」；用户想改风格就带着要求调 instruction 重新生成"
			}
			// 来源优先级：项目选定的风格库条目 → 默认风格包（冷启动）
			if st := a.selectedStyle(p); st != nil {
				if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
					return err.Error()
				}
				if err := os.WriteFile(out, st.StyleSamplesJSON(), 0o644); err != nil {
					return err.Error()
				}
				if a.OnEvent != nil {
					a.OnEvent(p.ID, "style_draft", st.Direction)
				}
				return fmt.Sprintf("风格样张已出（复用风格库「%s」，origin=%s）。硬门——用户在舞台点头确认后才能开工", st.Name, st.OriginProject)
			}
			b, err := os.ReadFile(filepath.Join(a.DataDir, "library", "style_samples_default.json"))
			if err != nil {
				return "默认风格包缺失：" + err.Error()
			}
			var ss contract.StyleSamples
			if err := json.Unmarshal(b, &ss); err != nil {
				return "默认风格包损坏：" + err.Error()
			}
			if st, ok := a.Lib.ByDirection(ss.Direction); ok {
				ss.StyleID = st.ID // 方向命中库条目即锚定：转场/后期/再提炼按 id 查库
			}
			ss.Confirmed = false // 硬门：必须用户确认
			b, _ = json.MarshalIndent(ss, "", "  ")
			if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
				return err.Error()
			}
			if err := os.WriteFile(out, b, 0o644); err != nil {
				return err.Error()
			}
			if a.OnEvent != nil {
				a.OnEvent(p.ID, "style_draft", ss.Direction)
			}
			return fmt.Sprintf("风格样张已出：%s（%d 张，舞台可预览）。这是硬门——用户在舞台点头确认后才能开工", ss.Direction, len(ss.Samples))
		},
	},
	{
		def:  toolDef("run_selfcheck", "自检三前置硬门（文稿/分镜/风格确认）", nil),
		gate: noGate,
		run: func(a *Agent, p *pipeline.Project, _ string) string {
			s := a.State(p.ID)
			var r []string
			r = append(r, fmt.Sprintf("文稿：%v", s.HasManuscript))
			if s.HasStoryboard {
				r = append(r, fmt.Sprintf("分镜：%d 段 / %.0f 秒", s.SegCount, s.TotalHint))
			} else {
				r = append(r, "分镜：缺")
			}
			r = append(r, fmt.Sprintf("风格：%s", map[bool]string{true: "已确认", false: "未确认"}[s.StyleConfirmed]))
			if s.HasManuscript && s.HasStoryboard && s.StyleConfirmed {
				r = append(r, "结论：三前置齐备，可以开工")
			} else {
				r = append(r, "结论：前置未齐，不能开工（代码强制）")
			}
			return strings.Join(r, "；")
		},
	},
	{
		def: toolDef("start_production", "启动制作管线（TTS→画面→检查→渲染，全程自动，进度实时播报）", nil),
		gate: func(s State) string {
			var missing []string
			if !s.HasManuscript {
				missing = append(missing, "文稿")
			}
			if !s.HasStoryboard {
				missing = append(missing, "分镜")
			}
			if !s.HasStyle || !s.StyleConfirmed {
				missing = append(missing, "风格确认")
			}
			if s.Producing {
				return "制作已在运行中"
			}
			if s.HasVideo {
				return "成片已就绪，无需重新制作（要改画面/重做某段用 rework；要下载地址用 export）"
			}
			if len(missing) > 0 {
				return "前置未齐（缺 " + strings.Join(missing, "、") + "）。补齐路径，向用户说明：文稿——点对话栏上方的「文稿」标签粘贴保存，或直接把文稿发进对话；分镜——你调用 draft_storyboard 生成；风格——点「风格」标签选库内风格（即时生效）或描述方向出样张"
			}
			return ""
		},
		run: func(a *Agent, p *pipeline.Project, _ string) string {
			if a.Producer == nil {
				return "制作运行器未接线（Producer 为空）"
			}
			ok, err := a.Producer.Start(p.ID)
			if err != nil {
				return "启动失败：" + err.Error()
			}
			if !ok {
				return "制作已在运行中"
			}
			a.Emit(p.ID, "stage", "pipeline", "running")
			return "制作已启动：配音 → 画面 → 布局检查 → 渲染，进度我随时报"
		},
	},
	{
		def: toolDef("rework", "重做段画面（修改单元=段；元素/文案/布局的改动都整段重生成画面，音频不动，改完整片重渲）。支持一次多段（segments 数组，共用同一条指令）。改旁白请走 update_storyboard 重新制作", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"segment":     map[string]any{"type": "string", "description": "目标段（单个时用）：段号（如 3）或段 id（如 seg03）——从用户的 📎 引用或话里解析"},
				"segments":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "目标段列表（多个时用）；与 segment 二选一，都给则合并"},
				"instruction": map[string]any{"type": "string", "description": "画面修改要求（改什么元素、怎么改）"},
			},
			"required": []string{"instruction"},
		}),
		gate: func(s State) string {
			if s.Producing {
				return "制作/重做运行中，先等完成"
			}
			if !s.HasVideo {
				return "成片未就绪，先制作（首做走 start_production）"
			}
			return ""
		},
		run: func(a *Agent, p *pipeline.Project, args string) string {
			var in struct {
				Segment     string   `json:"segment"`
				Segments    []string `json:"segments"`
				Instruction string   `json:"instruction"`
			}
			_ = json.Unmarshal([]byte(args), &in)
			if strings.TrimSpace(in.Instruction) == "" {
				return "缺少修改要求"
			}
			targets := in.Segments
			if s := strings.TrimSpace(in.Segment); s != "" {
				targets = append(targets, s)
			}
			if len(targets) == 0 {
				return "缺少目标段（segment 或 segments）"
			}
			seen := map[string]bool{}
			var segIDs, labels []string
			for _, t := range targets {
				segID, label, err := a.resolveSegment(p, t)
				if err != nil {
					return err.Error()
				}
				if seen[segID] {
					continue
				}
				seen[segID] = true
				segIDs = append(segIDs, segID)
				labels = append(labels, label)
			}
			if a.Producer == nil {
				return "制作运行器未接线（Producer 为空）"
			}
			ok, err := a.Producer.Rework(p.ID, segIDs, in.Instruction)
			if err != nil {
				return "重做启动失败：" + err.Error()
			}
			if !ok {
				return "制作/重做已在运行中"
			}
			a.Emit(p.ID, "stage", "pipeline", "rework")
			return fmt.Sprintf("段级重做已启动（%s）：只重生成目标段画面 → 检查 → 整片重渲，进度随时报", strings.Join(labels, "、"))
		},
	},
	{
		def: toolDef("extract_stylepack", "把本片风格提炼成风格库草稿（风格文件+全套样张；成片出片后自动做，用户在风格库确认入库后可复用）", nil),
		gate: func(s State) string {
			if !s.HasVideo {
				return "成片未就绪，出片后再提炼"
			}
			return ""
		},
		run: func(a *Agent, p *pipeline.Project, _ string) string {
			pack, err := a.Lib.DraftFromProject(p.Dir, p.ID, a.State(p.ID).Name)
			if err != nil {
				return "提炼失败：" + err.Error()
			}
			if pack.Published {
				return fmt.Sprintf("「%s」已在风格库（已发布）", pack.Name)
			}
			return fmt.Sprintf("风格「%s」已起草（草稿）。用户在风格库页点「入库」确认后，之后新建项目可直接复用", pack.Name)
		},
	},
	{
		def:  toolDef("read_project", "查看项目当前状态与产物", nil),
		gate: noGate,
		run:  func(a *Agent, p *pipeline.Project, _ string) string { return a.State(p.ID).summary() },
	},
	{
		def: toolDef("search_styles", "检索风格库（可复用的视觉风格：色板/版式/样张）。新建项目选风格、用户问「有什么风格」、或用户描述的气质可能命中库内条目时先查这里；命中后引导用户在风格门/风格库里选它（报 id）", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "题材/气质关键词（如 宇宙 黑板 科普 数据）"},
			},
			"required": []string{"query"},
		}),
		gate: noGate,
		run: func(a *Agent, p *pipeline.Project, args string) string {
			styles, err := a.Lib.List()
			if err != nil {
				return "风格库读取失败：" + err.Error()
			}
			q := strings.ToLower(strings.TrimSpace(args))
			key := func(ss stylelib.Style) string {
				return strings.ToLower(strings.Join(append([]string{ss.ID, ss.Name, ss.Direction, ss.Genre}, ss.Keywords...), " "))
			}
			var lines []string
			for _, ss := range styles {
				if q != "" && !strings.Contains(key(ss), q) {
					continue
				}
				state := map[bool]string{true: "已入库", false: "草稿待入库"}[ss.Published]
				line := fmt.Sprintf("- %s | %s | %s", ss.ID, ss.Name, state)
				if ss.Genre != "" {
					line += " | 题材:" + ss.Genre
				}
				if len(ss.Keywords) > 0 {
					line += " | 词:" + strings.Join(ss.Keywords, ",")
				}
				if pal := produce.PaletteNote(ss.Tokens); pal != "" {
					line += " | " + pal
				}
				if len(ss.Layouts) > 0 {
					line += fmt.Sprintf(" | 版式×%d", len(ss.Layouts))
				}
				if len(ss.Samples) > 0 {
					line += fmt.Sprintf(" | 样张×%d", len(ss.Samples))
				}
				lines = append(lines, line)
			}
			if len(lines) == 0 {
				if q != "" {
					full, _ := a.Lib.List()
					if len(full) == 0 {
						return "风格库为空：出片后我会自动起草风格，用户确认入库"
					}
					return "没有命中「" + q + "」的风格。库里现有 " + fmt.Sprintf("%d", len(full)) + " 条，重调一次可放宽或换关键词"
				}
				return "风格库为空：出片后我会自动起草风格，用户确认入库"
			}
			return fmt.Sprintf("风格库 %d 条：\n", len(lines)) + strings.Join(lines, "\n") +
				"\n选用：风格门弹窗点卡片，或让我把 id 记入 project（style_id）"
		},
	},
	{
		def: toolDef("web_search", "联网搜索实时资料（新闻/版本号/价格/数据等模型知识截止后的信息，或核实文稿事实）。写实元素（人物/产品/地标/新闻实物）的画面调研先搜再定。额度有限（套餐月约 200 次），必要才调，一次到位", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":    map[string]any{"type": "string", "description": "搜索词，≤70 字，关键词组效果最好"},
				"recency":  map[string]any{"type": "string", "description": "时间范围（可选）：oneDay/oneWeek/oneMonth/oneYear，查新闻类用 oneWeek"},
				"location": map[string]any{"type": "string", "description": "地域（可选）：cn 中文区（默认）/ us 非中文区"},
			},
			"required": []string{"query"},
		}),
		gate: noGate,
		run: func(a *Agent, p *pipeline.Project, args string) string {
			var in struct {
				Query    string `json:"query"`
				Recency  string `json:"recency"`
				Location string `json:"location"`
			}
			_ = json.Unmarshal([]byte(args), &in)
			q := strings.TrimSpace(in.Query)
			if q == "" {
				return "缺少 query"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			rs, err := llm.WebSearch(ctx, q, strings.TrimSpace(in.Recency), strings.TrimSpace(in.Location))
			if err != nil {
				return "搜索失败：" + err.Error()
			}
			if len(rs) == 0 {
				return "没有搜到相关结果，换个更具体的搜索词再试"
			}
			var b strings.Builder
			fmt.Fprintf(&b, "「%s」搜到 %d 条：\n", q, len(rs))
			for i, r := range rs {
				meta := r.Media
				if r.PublishDate != "" {
					if meta != "" {
						meta += " · "
					}
					meta += r.PublishDate
				}
				if meta != "" {
					meta = "（" + meta + "）"
				}
				fmt.Fprintf(&b, "%d. %s%s\n%s\n%s\n", i+1, r.Title, meta, r.Content, r.Link)
			}
			return b.String()
		},
	},
	{
		def: toolDef("set_bgm", "背景音乐：不带参数=列出曲库；track=曲名 设置/更换；track=\"off\" 关闭。成片已就绪时立即重新混音（几秒钟，不重渲画面）", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"track": map[string]any{"type": "string", "description": "曲名（先用空参查曲库）；off=关闭；留空=只列曲库"},
			},
		}),
		gate: func(s State) string {
			if s.Producing {
				return "制作运行中，先等本条完成"
			}
			return ""
		},
		run: func(a *Agent, p *pipeline.Project, args string) string {
			var in struct {
				Track string `json:"track"`
			}
			_ = json.Unmarshal([]byte(args), &in)
			lib := pipeline.ListBGM()
			if strings.TrimSpace(in.Track) == "" {
				if lib == "" {
					return "曲库为空（data/library/media/audio/tracks.json 缺失或为空）"
				}
				return "曲库：" + lib + "。用 track=曲名 设置，off 关闭"
			}
			if err := pipeline.SetBGM(p, in.Track); err != nil {
				return "设置失败：" + err.Error()
			}
			if strings.EqualFold(strings.TrimSpace(in.Track), "off") {
				if _, err := os.Stat(p.Artifact("renders/main_voice.mp4")); err == nil {
					if err := pipeline.StitchNow(p); err != nil {
						return "关闭失败（混音出错）：" + err.Error()
					}
					return "背景音乐已关闭，成片已重出（纯人声）"
				}
				return "背景音乐已关闭，下次制作生效"
			}
			if _, err := os.Stat(p.Artifact("renders/main_voice.mp4")); err == nil {
				if err := pipeline.StitchNow(p); err != nil {
					return "已设置但混音失败：" + err.Error()
				}
				return fmt.Sprintf("背景音乐「%s」已混入成片（人声闪避自动压低音乐，画面未动）", strings.ToLower(strings.TrimSpace(in.Track)))
			}
			return fmt.Sprintf("背景音乐「%s」已设置，下次制作/重渲自动混入", strings.ToLower(strings.TrimSpace(in.Track)))
		},
	},
	{
		def: toolDef("set_sfx", "转场音效：state=\"on\" 开启（段边界加气流声缝合硬切，章节切换加上扬预告）；\"off\" 关闭；不带参数查当前状态。成片就绪时立即重新混音（几秒，不重渲画面）", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"state": map[string]any{"type": "string", "description": "on=开启 | off=关闭；其他值/留空=查状态"},
			},
		}),
		gate: func(s State) string {
			if s.Producing {
				return "制作运行中，先等本条完成"
			}
			return ""
		},
		run: func(a *Agent, p *pipeline.Project, args string) string {
			var in struct {
				State string `json:"state"`
			}
			_ = json.Unmarshal([]byte(args), &in)
			want := strings.ToLower(strings.TrimSpace(in.State))
			if want != "on" && want != "off" {
				return map[bool]string{
					true:  "转场音效当前开启（whoosh 段边界气流 + riser 章节上扬）。用 state=off 关闭",
					false: "转场音效当前关闭。用 state=on 开启（段边界 whoosh，章节切换 riser，音量垫在人声下）",
				}[pipeline.TransitionSFXOn(p)]
			}
			if err := pipeline.SetTransitionSFX(p, want == "on"); err != nil {
				return "设置失败：" + err.Error()
			}
			if _, err := os.Stat(p.Artifact("renders/main_voice.mp4")); err == nil {
				if err := pipeline.StitchNow(p); err != nil {
					return "已设置但混音失败：" + err.Error()
				}
				return map[bool]string{
					true:  "转场音效已开启并混入成片（画面未动）",
					false: "转场音效已关闭，成片已重出",
				}[want == "on"]
			}
			return map[bool]string{
				true:  "转场音效已开启，下次制作生效",
				false: "转场音效已关闭，下次制作生效",
			}[want == "on"]
		},
	},
	{
		def: toolDef("list_segments", "列出分镜各段：id/段名/旁白摘要/画面元素清单（id+人话名）。要精确点名某段某元素改动前先看这里", nil),
		gate: func(s State) string {
			if !s.HasStoryboard {
				return "还没有分镜"
			}
			return ""
		},
		run: func(a *Agent, p *pipeline.Project, _ string) string {
			sb, err := p.LoadStoryboard()
			if err != nil {
				return "分镜读取失败：" + err.Error()
			}
			var b strings.Builder
			for _, seg := range sb.Segments {
				fmt.Fprintf(&b, "段%d %s「%s」（约%.0fs）旁白：%s\n", seg.Idx, seg.ID, seg.Key, seg.DurationHint, firstLine(seg.Narration, 60))
				if rb, rerr := os.ReadFile(p.Artifact("compositions/registry/" + seg.ID + ".json")); rerr == nil {
					var reg contract.ElementRegistry
					if json.Unmarshal(rb, &reg) == nil && len(reg.Elements) > 0 {
						parts := make([]string, 0, len(reg.Elements))
						for _, e := range reg.Elements {
							parts = append(parts, fmt.Sprintf("%s（%s）", e.Name, e.ID))
						}
						b.WriteString("  元素：" + strings.Join(parts, "、") + "\n")
					}
				}
			}
			return b.String()
		},
	},
	{
		def: toolDef("set_voice", "换配音音色：voice 留空=列出可用音色；设置后音频全量重配并自动整片重制（成片好了自动在对话里报结果）", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"voice": map[string]any{"type": "string", "description": "音色中文名（晓晓/晓伊/云健/云希/云扬/云野）、完整 Edge 音色 id，或 CosyVoice 音色（alex/benjamin/charles/david，付费档需 SILICONFLOW_API_KEY）；留空只列音色"},
			},
		}),
		gate: func(s State) string {
			if !s.HasStoryboard {
				return "还没有分镜，没有旁白可配音"
			}
			if s.Producing {
				return "制作/重做运行中，先等完成"
			}
			return ""
		},
		run: func(a *Agent, p *pipeline.Project, args string) string {
			var in struct {
				Voice string `json:"voice"`
			}
			_ = json.Unmarshal([]byte(args), &in)
			want := strings.TrimSpace(in.Voice)
			if want == "" {
				return "可用音色：晓晓（女·温暖，默认）、晓伊（女·清亮）、云健（男·磁性浑厚）、云希（男·年轻阳光）、云扬（男·新闻专业）、云野（男·沉稳）；CosyVoice 付费档（更自然，需 SILICONFLOW_API_KEY）：alex/benjamin/charles/david。用 voice=名字 设置"
			}
			id := voiceAliases[want]
			if id == "" {
				id = want // 允许直接给完整 id（Edge 或 CosyVoice）
			}
			if !isEdgeVoice(id) && !isCosyVoice(id) {
				return fmt.Sprintf("不认识音色 %q。可用：晓晓/晓伊/云健/云希/云扬/云野、完整 Edge 音色 id，或 CosyVoice 音色 alex/benjamin/charles/david（付费档）", want)
			}
			if err := pipeline.SetVoice(p, id); err != nil {
				return "设置失败：" + err.Error()
			}
			if _, err := os.Stat(p.Artifact("audio_meta.json")); err != nil {
				return fmt.Sprintf("音色已设为 %s，首次制作时生效", id)
			}
			if a.Producer == nil {
				return "制作运行器未接线（Producer 为空）"
			}
			ok, err := a.Producer.Start(p.ID)
			if err != nil {
				return "重制启动失败：" + err.Error()
			}
			if !ok {
				return "制作已在运行中，音色已更新到下一次制作"
			}
			a.Emit(p.ID, "stage", "pipeline", "running")
			return fmt.Sprintf("音色已换为 %s：配音全量重配 + 整片重制已启动，结果自动在对话里报", id)
		},
	},
	{
		def: toolDef("set_subtitle", "双语字幕开关：subtitle=\"en\" 开启中英双语（中文主行+英文斜体次行，自动短语级翻译）；\"off\" 关回纯中文。开启/关闭后自动整片重制（成片好了自动在对话里报结果）", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"subtitle": map[string]any{"type": "string", "description": "en=开启双语 | off=关闭；其他值列出当前状态"},
			},
		}),
		gate: func(s State) string {
			if !s.HasStoryboard {
				return "还没有分镜，没有字幕可设"
			}
			if s.Producing {
				return "制作/重做运行中，先等完成"
			}
			return ""
		},
		run: func(a *Agent, p *pipeline.Project, args string) string {
			var in struct {
				Subtitle string `json:"subtitle"`
			}
			_ = json.Unmarshal([]byte(args), &in)
			want := strings.ToLower(strings.TrimSpace(in.Subtitle))
			cur := false
			if b, err := os.ReadFile(p.Artifact("project.json")); err == nil {
				var meta map[string]any
				_ = json.Unmarshal(b, &meta)
				cur, _ = meta["bi_subtitle"].(bool)
			}
			if want != "en" && want != "off" {
				if cur {
					return "当前：双语字幕已开启。subtitle=\"off\" 关闭"
				}
				return "当前：纯中文字幕。subtitle=\"en\" 开启双语（英文短语自动翻译，重制后生效）"
			}
			on := want == "en"
			if on == cur {
				if on {
					return "双语字幕已是开启状态，无需重制"
				}
				return "双语字幕已是关闭状态"
			}
			if err := pipeline.SetBiSubtitle(p, on); err != nil {
				return "设置失败：" + err.Error()
			}
			if _, err := os.Stat(p.Artifact("audio_meta.json")); err != nil {
				if on {
					return "双语字幕已开启，首次制作时自动翻译英文短语"
				}
				return "已设为纯中文字幕"
			}
			if a.Producer == nil {
				return "制作运行器未接线（Producer 为空）"
			}
			ok, err := a.Producer.Start(p.ID)
			if err != nil {
				return "重制启动失败：" + err.Error()
			}
			if !ok {
				return "制作已在运行中，设置已更新到下一次制作"
			}
			a.Emit(p.ID, "stage", "pipeline", "running")
			if on {
				return "双语字幕已开启：短语翻译 + 整片重制已启动，结果自动在对话里报"
			}
			return "已切回纯中文字幕：整片重制已启动，结果自动在对话里报"
		},
	},
	{
		def: toolDef("export", "导出成片（返回下载地址）", nil),
		gate: func(s State) string {
			if !s.HasVideo {
				return "成片未就绪"
			}
			return ""
		},
		run: func(a *Agent, p *pipeline.Project, _ string) string {
			return "成片下载地址：/api/projects/" + p.ID + "/video/main.mp4（1080p，含旁白与逐词字幕）"
		},
	},
}

func toolDef(name, desc string, params map[string]any) openai.Tool {
	if params == nil {
		params = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{
		Name: name, Description: desc, Parameters: params,
	}}
}

func toolSchemas() []openai.Tool {
	out := make([]openai.Tool, len(tools))
	for i, t := range tools {
		out[i] = t.def
	}
	return out
}

func findTool(name string) *tool {
	for i := range tools {
		if tools[i].def.Function.Name == name {
			return &tools[i]
		}
	}
	return nil
}

// ── ReAct 循环 ───────────────────────────────────────────────

const maxSteps = 8

func (a *Agent) systemPrompt(id string, s State) string {
	return fmt.Sprintf(`你是「帧述」的主管 Agent——靠谱的剪辑搭档：懂行、话少、主动汇报、给专业建议但尊重用户决定。

当前项目「%s」（ID: %s）状态（唯一事实源，说话前先对照它）：
%s

工作准则：
- 上面这份状态是唯一事实：状态说成片就绪就是已就绪，直接给下载地址 /api/projects/%s/video/main.mp4；不确定就先 read_project 核对，禁止按聊天历史想象状态。
- 三前置硬门：文稿、分镜、风格样张（用户确认）——齐了才能 start_production，代码强制，别硬闯；缺哪项就引导用户点对话栏上方的同名标签补齐（文稿也可直接粘进对话），成片就绪后不再 start_production。
- 换风格/调样张：调 draft_style_samples 并把用户要求原样放进 instruction，样张没重新生成就不能说「已出新样张」——没做工具调用就当没做。
- 成片就绪后：改某段画面用 rework（整段重生成画面、音频不动）；用户带 📎 段/元素引用的消息几乎都是 rework 意图。元素引用指名了改哪个元素，rework 的 instruction 里点名它（人话名，必要时带元素 id 与时刻）。要精确定位段与元素先 list_segments。换配音音色用 set_voice（空参列音色，设置后自动整片重制）。出片后主动 extract_stylepack 沉淀风格（一次就够，已提炼过不必重复）。
- 默认自主连贯：能做的直接做（出分镜→出样张→自检一路做下去），到用户门（风格确认）停下说清楚等什么。
- 涉及时效性事实（新闻/版本/价格/数据）先用 web_search 核实再写进文稿/分镜；搜索不可用就说明信息可能过时，别编。
- web_search 消耗套餐月度额度（每月仅约 200 次，与其他 MCP 共享）——非必要不搜：一轮对话合并成尽量少的精准查询；空结果最多换个词再试一次，仍空就如实说并标明信息可能过时，不连环重试。
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
	msgs := []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleSystem, Content: a.systemPrompt(projectID, a.State(projectID))}}
	for _, h := range a.history(projectID, 10) {
		msgs = append(msgs, h)
	}
	// 实时状态随最新 user 消息注入（模型对最新消息权重最高，防聊天历史锚定幻觉）
	msgs = append(msgs, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: fmt.Sprintf("［系统·实时项目状态，以此为准］\n%s\n%s\n用户消息：%s",
		a.State(projectID).summary(), a.formatRefs(p, refs), userText)})

	// 对话轮级时限：卡死的 LLM 调用（上游挂住时 10min×3 重试）会把 turnMu 下的
	// 后续对话轮全部拖成沉默；超时给用户明确交代
	turnCtx, cancelTurn := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancelTurn()

	final := ""
	for step := 0; step < maxSteps; step++ {
		msg, _, cerr := prov.Chat(turnCtx, msgs, toolSchemas())
		if cerr != nil {
			a.finish(projectID, "模型调用失败："+cerr.Error(), "error")
			return
		}
		if len(msg.ToolCalls) == 0 {
			final = msg.Content
			break
		}
		msgs = append(msgs, msg)
		for _, call := range msg.ToolCalls {
			obs := a.execTool(p, call.Function.Name, call.Function.Arguments)
			msgs = append(msgs, openai.ChatCompletionMessage{
				Role:       openai.ChatMessageRoleTool,
				Content:    obs,
				ToolCallID: call.ID,
				Name:       call.Function.Name,
			})
		}
	}
	if final == "" {
		final = "我先停一下：这轮动作做完了，等你的下一步指示。"
	}
	a.finish(projectID, final, "text")
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

// execTool 门禁 + 执行 + 记账（工具轨迹入聊天案卷）。
func (a *Agent) execTool(p *pipeline.Project, name, args string) string {
	t := findTool(name)
	if t == nil {
		return "未知工具 " + name
	}
	s := a.State(p.ID)
	if reason := t.gate(s); reason != "" {
		obs := "拒绝：" + reason
		a.saveToolMsg(p.ID, name, args, obs)
		a.Emit(p.ID, "tool_result", "", name+" "+obs)
		return obs
	}
	a.Emit(p.ID, "tool_call", "", name+" "+compactArgs(args))
	obs := t.run(a, p, args)
	a.saveToolMsg(p.ID, name, args, obs)
	a.Emit(p.ID, "tool_result", "", name+" → "+firstLine(obs, 120))
	return obs
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
	p := pipeline.NewProject(a.DataDir, projectID)
	if b, err := os.ReadFile(p.Artifact("project.json")); err == nil {
		var meta map[string]any
		if json.Unmarshal(b, &meta) == nil {
			meta["status"] = status
			_ = pipeline.WriteFileAtomic(p.Artifact("project.json"), mustJSONIndent(meta), 0o644)
		}
	}
	_ = a.Store.UpdateStatus(projectID, status)
}

func mustJSONIndent(v any) []byte {
	b, _ := json.MarshalIndent(v, "", "  ")
	return b
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
		msg := detail
		if strings.Contains(detail, "✗") { // check 类失败：只挑 ✗ 行与计数，对话框不刷屏
			var keep []string
			for _, ln := range strings.Split(detail, "\n") {
				t := strings.TrimSpace(ln)
				if strings.HasPrefix(t, "✗") || strings.Contains(t, "error(s)") {
					keep = append(keep, t)
				}
			}
			if len(keep) > 0 {
				msg = strings.Join(keep, "\n")
			}
		}
		content = "❌ 制作失败：" + capRunes(msg, 500) + "\n回我一句「继续」，我就接着修。"
		typ = "error"
	}
	_ = a.Store.SaveMsg(&store.Msg{ProjectID: projectID, Role: "agent", Type: typ, Content: content})
	a.Emit(projectID, "agent_msg", "", content)
}

// capRunes 截断到 n 个 rune（多行保留，失败诊断往往跨行）。
func capRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

func (a *Agent) saveToolMsg(projectID, name, args, result string) {
	_ = a.Store.SaveMsg(&store.Msg{
		ProjectID: projectID, Role: "agent", Type: "tool_call",
		Content: name, ToolName: name, ToolArgs: args, ToolResult: result,
	})
}

func (a *Agent) history(projectID string, n int) []openai.ChatCompletionMessage {
	msgs, err := a.Store.ListMsgs(projectID, 0, 100)
	if err != nil {
		return nil
	}
	var hist []openai.ChatCompletionMessage
	for _, m := range msgs {
		if m.Type != "text" || m.Role == "system" {
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

func firstLine(s string, n int) string {
	s = strings.SplitN(s, "\n", 2)[0]
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
