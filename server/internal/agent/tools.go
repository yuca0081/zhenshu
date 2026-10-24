// 工具定义与包级工具表（自 agent.go 纯移动，无逻辑改动）：门禁按状态机求值，
// 执行体绑当前项目。schema/门禁/执行体三件事都在这，agent.go 只剩循环与编排。
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	openai "github.com/sashabaranov/go-openai"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/pipeline"
	"webpage-video-agent/server/internal/prep"
	"webpage-video-agent/server/internal/produce"
	"webpage-video-agent/server/internal/stylelib"
	"webpage-video-agent/server/internal/styleref"
	"webpage-video-agent/server/internal/webfetch"
)

// prepDeps 准备阶段工位依赖（rerun 工具与三条改稿路径共用）。
func (a *Agent) prepDeps() prep.Deps {
	return prep.Deps{RootDir: a.RootDir, Lib: a.Lib, Emit: a.Emit}
}

// runPrepInline 对话侧起草工具（draft_storyboard/draft_style_samples）的互斥
// 入口：不动作废，只登记 running 跑缺站——与想法页异步链互斥（高-6 补漏，
// 2026-10-19 审查：两处裸跑 RunStrict 双向都不在互斥内）。返回错误原文。
func (a *Agent) runPrepInline(ctx context.Context, p *pipeline.Project) error {
	if a.Preps == nil {
		return prep.RunStrict(ctx, p, a.prepDeps())
	}
	err := a.Preps.RunInline(ctx, p.ID, func(ictx context.Context) error {
		return prep.RunStrict(ictx, p, a.prepDeps())
	})
	if errors.Is(err, prep.ErrPrepRunning) {
		return errors.New("准备阶段已有链在跑（想法页或更早的工具启动的），等它完成再试")
	}
	return err
}

// rerunPrep 反馈路由的重跑执行体：note 落工位附注 → 从 station 起级联失效 →
// 链式重跑缺的工位 → 汇报三工件状态。失败原文返回（调用方回喂对话）。
// 同步执行（与 draft_storyboard 跑管线同模式）；对话轮 20min 时限兜底。
func (a *Agent) rerunPrep(ctx context.Context, p *pipeline.Project, station, note string) string {
	switch station {
	case prep.StationScript:
		if note != "" {
			if err := os.WriteFile(p.Artifact("input/script_notes.txt"), []byte(note), 0o644); err != nil {
				return "附注写入失败：" + err.Error()
			}
		}
	case prep.StationStyle:
		if note != "" {
			if err := os.WriteFile(p.Artifact("input/style_notes.txt"), []byte(note), 0o644); err != nil {
				return "附注写入失败：" + err.Error()
			}
		}
	case prep.StationStoryboard:
		if note != "" {
			if err := os.WriteFile(p.Artifact("input/storyboard_notes.txt"), []byte(note), 0o644); err != nil {
				return "附注写入失败：" + err.Error()
			}
		}
	default:
		return "target 只能是 script / style / storyboard"
	}
	// 与异步链共用一把互斥（2026-10-19 高-6）：执行期间登记 running——想法页
	// runPrep / cancelPrep / deleteProject 都能看到「在跑」；反向（链已在跑）
	// 返回 ErrPrepRunning 如实回喂。作废挪进闭包：被拒时不白作废。
	var runErr error
	if a.Preps != nil {
		runErr = a.Preps.RunInline(ctx, p.ID, func(ictx context.Context) error {
			prep.InvalidateFrom(p, station)
			return prep.RunStrict(ictx, p, a.prepDeps())
		})
	} else {
		prep.InvalidateFrom(p, station)
		runErr = prep.RunStrict(ctx, p, a.prepDeps())
	}
	if runErr != nil {
		if errors.Is(runErr, prep.ErrPrepRunning) {
			return "准备阶段已有链在跑（想法页或更早的工具启动的），等它完成再重跑。"
		}
		if ctx.Err() != nil {
			return "重跑被中断。"
		}
		return "重跑失败：" + runErr.Error()
	}
	// 三工件齐 → 状态回「等终审确认」（与出样张后的门语义一致）
	a.setDoneStatus(p.ID, "style")
	if a.Emit != nil {
		a.Emit(p.ID, "prep_updated", "", station)
	}
	return "准备工件已按反馈重跑完成（" + station + " 起级联）：" + prep.Summary(p) +
		"。等用户在「终审」页签重新确认后才能开工"
}

// ── 工具定义 ─────────────────────────────────────────────────

type tool struct {
	def  openai.Tool
	gate func(s State) string // nil = 无门禁（免扫盘直放）；否则返回空串 = 放行、非空 = 拒绝原因（回喂）
	run  func(ctx context.Context, a *Agent, p *pipeline.Project, args string) string
}

// voiceAliases 项目音色别名（与 ai/tts_align.py VOICE_ALIASES 保持一致，
// voice_alias_test.go 钉住两表不漂移）。
var voiceAliases = map[string]string{
	"晓晓":   "zh-CN-XiaoxiaoNeural",
	"晓伊":   "zh-CN-YiyiNeural",
	"云健":   "zh-CN-YunjianNeural",
	"云希":   "zh-CN-YunxiNeural",
	"云扬":   "zh-CN-YunyangNeural",
	"云野":   "zh-CN-YunyeNeural",
	"龙安灵心": "longanlingxin",
	"龙安鲁风": "longanlufeng",
}

// isEdgeVoice / isBailianVoice 音色 id 判别：Edge 完整 id（zh-CN-YunjianNeural）、
// 百炼 Qwen-Audio-TTS 音色（longan* 龙安系 / loong* 英文系 / xunanchuan，含 _v3.x 后缀）。
// TTS 引擎 2026-10-10 起只有百炼（DASHSCOPE_API_KEY）→ Edge 兜底两档，CosyVoice 档退役。
func isEdgeVoice(id string) bool {
	return strings.Contains(id, "-") && strings.Contains(id, "Neural")
}

func isBailianVoice(id string) bool {
	return strings.HasPrefix(id, "longan") ||
		strings.HasPrefix(id, "loong") ||
		strings.HasPrefix(id, "xunanchuan")
}

var tools = []tool{
	{
		def: toolDef("draft_storyboard", "根据文稿生成分镜表（LLM 作业，自动完成）", nil),
		gate: func(s State) string {
			// 主题模式放行：没文稿但有一句话主题时，准备工位①从主题起步
			if !s.HasManuscript && s.Topic == "" {
				return "还没有文稿也没有主题——补文稿（粘贴/AI 起草），或确认建项目时给了主题"
			}
			return ""
		},
		run: func(ctx context.Context, a *Agent, p *pipeline.Project, _ string) string {
			// 新路径：非 legacy 项目一律走准备工位链（缺哪站跑哪站）——
			// 主题项目从①起步，已有文字稿的从缺的工位续跑
			if !prep.LegacyProject(p) {
				if prep.HasArtifact(p, prep.StationStoryboard) {
					return "分镜稿已出（准备工位产物）。要改结构/场景直接说要求，我走反馈重跑；开工前在「终审」页签确认。"
				}
				// 与异步链共用一把互斥（高-6 补漏，2026-10-19 审查）：这里裸跑
				// 既不登记 running 也不查在跑，想法页链跑着时对话里一句话就能
				// 开出第二条链互踩工件
				if err := a.runPrepInline(ctx, p); err != nil {
					return err.Error()
				}
				dr, derr := prep.LoadDraft(p)
				if derr != nil {
					return "分镜稿生成后读取失败：" + derr.Error()
				}
				a.setDoneStatus(p.ID, "style")
				if a.OnEvent != nil {
					a.OnEvent(p.ID, "storyboard", "")
				}
				return fmt.Sprintf("准备阶段完成：%s。分镜稿 %d 段 / 约 %.0f 秒。等用户在「终审」页签打包确认后才能开工",
					prep.Summary(p), len(dr.Segments), dr.TotalHint())
			}
			if err := pipeline.Run(ctx, p, "storyboard"); err != nil {
				return "分镜生成失败：" + err.Error()
			}
			sb, err := p.LoadStoryboard()
			if err != nil { // 管线称成功却读不出分镜：如实报错，不拿零值分镜编数字
				return "分镜生成后读取失败：" + err.Error()
			}
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
		run: func(ctx context.Context, a *Agent, p *pipeline.Project, args string) string {
			var in struct {
				Instruction string `json:"instruction"`
			}
			_ = json.Unmarshal([]byte(args), &in)
			if strings.TrimSpace(in.Instruction) == "" {
				return "缺少修改要求"
			}
			// 新路径：分镜稿是③的产物——反馈路由（改结构场景→只重③）
			if prep.HasArtifact(p, prep.StationScript) {
				return a.rerunPrep(ctx, p, prep.StationStoryboard, in.Instruction)
			}
			sbPath := p.Artifact("storyboards/storyboard.json")
			llmPath := p.LLMOutput("storyboard")
			// 旧分镜与 LLM 中间产物先改名 .bak（等价删除但可还原）：重生成失败时
			// 原样放回——项目任何时刻不留「分镜空洞」，对齐 regenerateStyle 的备份模式
			bak, llmBak := sbPath+".bak", llmPath+".bak"
			hadPrev := true
			if err := os.Rename(sbPath, bak); err != nil {
				if _, serr := os.Stat(sbPath); serr != nil {
					hadPrev = false // 首次出分镜：无旧可备
				} else {
					return "备份旧分镜失败：" + err.Error()
				}
			}
			hadLLM := false
			if _, err := os.Stat(llmPath); err == nil {
				if lerr := os.Rename(llmPath, llmBak); lerr != nil {
					if hadPrev { // 分镜已挪走，先还回去再报错
						_ = os.Rename(bak, sbPath)
					}
					return "备份 LLM 中间产物失败：" + lerr.Error()
				}
				hadLLM = true
			}
			restore := func(cause error) string {
				if hadLLM { // 中间产物一并还原，下次重试的起点与本次相同
					_ = os.Rename(llmBak, llmPath)
				}
				if !hadPrev {
					return "分镜重生成失败：" + cause.Error()
				}
				if rerr := os.Rename(bak, sbPath); rerr != nil {
					return fmt.Sprintf("分镜重生成失败，且还原旧分镜也失败（备份在 %s）：%v（原因：%v）", bak, rerr, cause)
				}
				return "分镜重生成失败（已还原旧分镜）：" + cause.Error()
			}
			if err := os.WriteFile(p.Artifact("input/storyboard_notes.txt"), []byte(in.Instruction), 0o644); err != nil {
				return restore(err)
			}
			if err := pipeline.Run(ctx, p, "storyboard"); err != nil {
				return restore(err)
			}
			_ = os.Remove(bak)
			_ = os.Remove(llmBak)
			sb, err := p.LoadStoryboard()
			if err != nil { // 管线称成功却读不出分镜：如实报错，不拿零值分镜编数字
				return "分镜重生成后读取失败：" + err.Error()
			}
			if a.OnEvent != nil {
				a.OnEvent(p.ID, "storyboard", "")
			}
			return fmt.Sprintf("分镜已按「%s」重生成：%d 段 / 约 %.0f 秒（若已有音频/画面需重新制作才会生效）",
				in.Instruction, len(sb.Segments), sb.TotalHint())
		},
	},
	{
		def: toolDef("save_manuscript", "保存口播文稿正文到项目（用户把文稿直接发进对话时用它落盘，效果等同「文稿」标签粘贴保存）", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"content": map[string]any{"type": "string", "description": "文稿全文（口播正文）"},
			},
			"required": []string{"content"},
		}),
		gate: func(s State) string {
			if s.Producing {
				return "制作运行中，先等本条完成"
			}
			return ""
		},
		run: func(ctx context.Context, a *Agent, p *pipeline.Project, args string) string {
			var in struct {
				Content string `json:"content"`
			}
			_ = json.Unmarshal([]byte(args), &in)
			return a.saveManuscriptContent(p, in.Content)
		},
	},
	{
		def: toolDef("draft_manuscript", "AI 起草一篇口播文稿（自主联网核实时效资料后成稿，默认 500 字）。用户给了选题/主题还没给文稿时用它；默认只出稿给用户过目，用户明确说「直接用」才 save=true 落盘", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"topic": map[string]any{"type": "string", "description": "一句话主题（例：蚊子为什么嗡嗡叫）"},
				"words": map[string]any{"type": "integer", "description": "目标字数（可选，默认 500，上限 5000）"},
				"save":  map[string]any{"type": "boolean", "description": "true=起草后直接保存为项目文稿；默认 false=只把稿子给用户过目"},
			},
			"required": []string{"topic"},
		}),
		gate: func(s State) string {
			if s.Producing {
				return "制作运行中，先等本条完成"
			}
			return ""
		},
		run: func(ctx context.Context, a *Agent, p *pipeline.Project, args string) string {
			var in struct {
				Topic string `json:"topic"`
				Words int    `json:"words"`
				Save  bool   `json:"save"`
			}
			_ = json.Unmarshal([]byte(args), &in)
			in.Topic = strings.TrimSpace(in.Topic)
			if in.Topic == "" {
				return "缺少主题"
			}
			if in.Words <= 0 {
				in.Words = 500
			}
			if in.Words > 5000 { // 与文稿弹窗同上限：更长的一次性成稿后半段易注水
				in.Words = 5000
			}
			prov, err := llm.FromEnv(llm.RoleDialogue)
			if err != nil {
				return "起草需要 API 模式（LLM_API_KEY）。可让用户直接把文稿发进来，你调 save_manuscript 存下"
			}
			// 360s 与文稿弹窗起草同预算：时事题材先联网核实（预算帽 2 次搜索）再成稿，
			// 收尾 high 思考一轮就要 1-3 分钟，短了必砍死最长的成稿轮
			dctx, cancel := context.WithTimeout(ctx, 360*time.Second)
			defer cancel()
			content, err := DraftManuscript(dctx, prov, in.Topic, in.Words)
			if err != nil {
				return "起草失败：" + err.Error()
			}
			if !in.Save {
				return "草稿如下（尚未保存，用户点头后调 save_manuscript 落盘，或带 save=true 重新起草）：\n\n" + content
			}
			return "已按主题起草并保存。" + a.saveManuscriptContent(p, content)
		},
	},
	{
		def: toolDef("draft_style_samples", "产出本片设计系统与风格样张（皮肤1-2套/字体角色/动效语言/贯穿装置/版式/负面清单 + 1-3张样张）：首次出样张——项目选了库内风格就按它的「标签+特色」强参考发明（吸收机制，物件用本片内容），没选就由你按分镜自由发明（现场发明约束，不套模板）；样张已存在时必须带 instruction 按用户要求重新生成", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"instruction": map[string]any{"type": "string", "description": "用户对风格的修改/新方向要求（如：更暖色调、加猫爪猫耳Q版点缀；换全新方向也写在这）。首次出样张可省略"},
			},
		}),
		gate: func(s State) string {
			// 制作运行中不许重出样张：重跑②会 InvalidateFrom 连坐删 stylepack/
			// 样张/设计衍生品——全是制作管线的硬门与渲染输入（2026-10-19 高-10）
			if s.Producing {
				return "制作运行中，不能重出样张——先等本条完成"
			}
			// 新路径（文字稿工件在）：②在③之前，不需要先有分镜
			if s.HasStoryboard || s.Prep != "" {
				return ""
			}
			return "先出分镜，再出风格样张"
		},
		run: func(ctx context.Context, a *Agent, p *pipeline.Project, args string) string {
			var in struct {
				Instruction string `json:"instruction"`
			}
			_ = json.Unmarshal([]byte(args), &in)
			out := p.Artifact("style/style_samples.json")
			// 新路径：风格包是②的产物——带要求=反馈重跑（②③级联），不带=等终审
			if prep.HasArtifact(p, prep.StationScript) {
				if prep.HasArtifact(p, prep.StationStyle) {
					// 换风格过期侦测：applyStyle 只重写选定简报+清样张，不动 stylepack，
					// 包就挂在了旧条目上——简报与包不一致/样张被清 = ②过期，级联重跑，
					// 不许回「已出」（2026-10-17 实测：两次短路回「已出」，模型还谎报样张已生成）
					if stale, why := prep.StyleStale(p); stale {
						if strings.TrimSpace(in.Instruction) == "" {
							in.Instruction = why
						}
						return a.rerunPrep(ctx, p, prep.StationStyle, in.Instruction)
					}
					if strings.TrimSpace(in.Instruction) == "" {
						return "风格包已出（准备工位产物），本次调用没有重新出样张——向用户汇报时不得说「样张已生成/已重新生成」。等用户在「终审」页签打包确认；要改风格方向，把要求带在 instruction 里我走反馈重跑"
					}
					return a.rerunPrep(ctx, p, prep.StationStyle, in.Instruction)
				}
				// 同 draft_storyboard：过 RunInline 互斥（高-6 补漏）
				if err := a.runPrepInline(ctx, p); err != nil {
					return err.Error()
				}
				sp, serr := prep.LoadStylePack(p)
				if serr != nil {
					return "风格包生成后读取失败：" + serr.Error()
				}
				a.setDoneStatus(p.ID, "style")
				dir := ""
				if sp.DesignSystem != nil {
					dir = sp.DesignSystem.Direction
				}
				if a.OnEvent != nil {
					a.OnEvent(p.ID, "style_draft", dir)
				}
				return fmt.Sprintf("风格包已产出（%s），样张见「终审」页签。等用户打包确认后才能开工", dir)
			}
			if strings.TrimSpace(in.Instruction) != "" {
				// 带要求（首次或重生成一律走这条）：用户明确的方向优先于任何选定风格，
				// 直接按要求现写——样张与风格文件（skin）同产（§12.2）
				return a.regenerateStyle(ctx, p, in.Instruction, out)
			}
			if _, err := os.Stat(out); err == nil {
				return "风格样张已出，等用户在舞台点击「确认风格」；用户想改风格就带着要求调 instruction 重新生成"
			}
			// 来源优先级：项目选定的风格库条目（强参考：设计阶段现发明并逐条吸收特色）
			// → 模型自由发挥。v3 条目没有自带样张——设计系统必须本片现发明，
			// 样张确认硬门对所有人成立。
			if st := a.selectedStyle(p); st != nil {
				briefPath := p.Artifact("style/style_brief.json")
				if _, err := os.Stat(briefPath); err != nil {
					// GatesModal 的 applyStyle 通常已落简报；直接对话选用时在此补齐（全开）
					brief, err := a.Lib.BuildBrief(st.ID, nil)
					if err != nil {
						return "简报组装失败：" + err.Error()
					}
					if err := stylelib.WriteBriefFile(briefPath, brief); err != nil {
						return err.Error()
					}
				}
				if err := pipeline.Run(ctx, p, "style_samples"); err != nil {
					return "样张生成失败：" + err.Error()
				}
				return a.styleDraftReport(p, out, "强参考「"+st.Name+"」：标签气质+特色逐条吸收")
			}
			if err := pipeline.Run(ctx, p, "style_samples"); err != nil {
				return "样张生成失败：" + err.Error()
			}
			return a.styleDraftReport(p, out, "自由发挥")
		},
	},
	{
		def:  toolDef("run_selfcheck", "自检三前置硬门（文稿/分镜/风格确认）", nil),
		gate: nil,
		run: func(ctx context.Context, a *Agent, p *pipeline.Project, _ string) string {
			s := a.State(p.ID)
			var r []string
			r = append(r, fmt.Sprintf("文稿：%v", s.HasManuscript))
			if s.HasStoryboard {
				r = append(r, fmt.Sprintf("分镜：%d 段 / %.0f 秒", s.SegCount, s.TotalHint))
			} else {
				r = append(r, "分镜：缺")
			}
			r = append(r, fmt.Sprintf("风格：%s", map[bool]string{true: "已确认", false: "未确认"}[s.StyleConfirmed]))
			// 成片新旧与最近制作终态也报：自检是模型的开工前盘点，漏了这两样
			// 就会在过期/失败现场做出「成片已就绪」的错误结论
			if s.HasVideo {
				if axes := staleAxes(s); len(axes) > 0 {
					r = append(r, "成片：旧版（"+strings.Join(axes, "、")+"过期）")
				} else {
					r = append(r, "成片：最新")
				}
			}
			switch {
			case strings.HasSuffix(s.LastEvent, ".error"):
				r = append(r, fmt.Sprintf("最近制作：%s 失败", s.LastAt.Format("15:04")))
			case strings.HasSuffix(s.LastEvent, ".interrupted"):
				r = append(r, fmt.Sprintf("最近制作：%s 被服务重启中断", s.LastAt.Format("15:04")))
			case strings.HasSuffix(s.LastEvent, ".cancelled"):
				r = append(r, fmt.Sprintf("最近制作：%s 被取消", s.LastAt.Format("15:04")))
			}
			if s.HasManuscript && s.HasStoryboard && s.StyleConfirmed {
				r = append(r, "结论：三前置齐备，可以开工")
			} else {
				r = append(r, "结论：前置未齐，不能开工（代码强制）")
			}
			return strings.Join(r, "；")
		},
	},
	{
		def: toolDef("start_production", "启动制作管线（TTS→画面→检查→渲染，全程自动，进度实时播报）。成片过期或上次失败/中断后的整片重做也走这里", nil),
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
			if s.HasVideo && videoFresh(s) {
				return "成片已就绪且为最新，无需重新制作（要改画面/重做某段用 rework；要下载地址用 export）"
			}
			// 成片旧版/上次失败·中断：整片重做合法（与舞台「按最新内容重做」按钮同权），放行
			if len(missing) > 0 {
				return "前置未齐（缺 " + strings.Join(missing, "、") + "）。补齐路径，向用户说明：文稿——点「文稿」标签粘贴保存，或把文稿直接发进对话由你调 save_manuscript 存下；分镜——你调用 draft_storyboard 生成；风格——点「风格」标签选库内风格（选定后你调 draft_style_samples 出样张）或描述方向出样张"
			}
			return ""
		},
		run: func(ctx context.Context, a *Agent, p *pipeline.Project, _ string) string {
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
		run: func(ctx context.Context, a *Agent, p *pipeline.Project, args string) string {
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
			msg := fmt.Sprintf("段级重做已启动（%s）：只重生成目标段画面 → 检查 → 整片重渲，进度随时报", strings.Join(labels, "、"))
			// 旁白真源已变时必须点名 rework 的边界：它只重画不重配，别让用户
			// 以为改了文稿再 rework 就能换旁白
			return msg + reworkStaleNote(a.State(p.ID))
		},
	},
	{
		def: toolDef("extract_stylepack", "把本片风格沉淀成风格库草稿（标签+特色清单，LLM 打标；成片出片后自动做，用户在风格库确认入库后可复用；草稿的证据帧留空，由用户在库页回看补录）", nil),
		gate: func(s State) string {
			if !s.HasVideo {
				return "成片未就绪，出片后再提炼"
			}
			return ""
		},
		run: func(ctx context.Context, a *Agent, p *pipeline.Project, _ string) string {
			// 幂等先行：本就出自库内风格 / 已起草过 → 直接交代（不起 LLM）
			if st := a.draftedStyle(p); st != nil {
				if st.Published {
					return fmt.Sprintf("「%s」已在风格库（已发布）", st.Name)
				}
				return fmt.Sprintf("风格「%s」已是草稿，用户在风格库页点「入库」确认", st.Name)
			}
			prov, err := llm.FromEnv(llm.RolePlan)
			if err != nil {
				return "提炼需要 API 模式（LLM_API_KEY）"
			}
			digest, derr := designDigest(p)
			if derr != nil {
				return "提炼失败：" + derr.Error()
			}
			ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
			defer cancel()
			d, terr := styleref.DraftFromDesign(ctx, prov, a.Lib, a.State(p.ID).Name, digest)
			if terr != nil {
				return "提炼失败：" + terr.Error()
			}
			pack, err := a.Lib.DraftFromProject(p.Dir, p.ID, stylelib.ProjectDraftIn{
				Name: d.Name, Subject: d.Subject, Facets: d.Facets, Tags: d.Tags,
				TagEvidence: d.TagEvidence, Signatures: d.Signatures,
			})
			if err != nil {
				return "提炼失败：" + err.Error()
			}
			if pack.Published {
				return fmt.Sprintf("「%s」已在风格库（已发布）", pack.Name)
			}
			return fmt.Sprintf("风格「%s」已起草（标签 %s + 特色×%d，草稿）。用户在风格库页点「入库」确认后，之后新建项目可直接复用",
				pack.Name, pack.Tags.Main, len(pack.Signatures))
		},
	},
	{
		def: toolDef("rerun_prep", "准备阶段反馈重跑（打包终审后的反馈路由）：target=\"script\" 改文字（①②③级联）/ \"style\" 改风格（②③级联）/ \"storyboard\" 改结构场景（只重③）。note=用户的具体修改要求。三个准备工位会按链重跑，完成后等用户在「终审」页签重新确认", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"target": map[string]any{"type": "string", "description": "script | style | storyboard——按用户改的是哪类内容选，拿不准就问一句"},
				"note":   map[string]any{"type": "string", "description": "用户的修改要求（原样转述，工位会优先满足）"},
			},
			"required": []string{"target"},
		}),
		gate: func(s State) string {
			if s.Producing {
				return "制作运行中，先等本条完成"
			}
			if !s.HasManuscript && s.Topic == "" {
				return "还没有文稿/主题，无处重跑"
			}
			return ""
		},
		run: func(ctx context.Context, a *Agent, p *pipeline.Project, args string) string {
			var in struct {
				Target string `json:"target"`
				Note   string `json:"note"`
			}
			if err := json.Unmarshal([]byte(args), &in); err != nil {
				return "参数解析失败：" + err.Error()
			}
			if !prep.HasArtifact(p, prep.StationScript) && prep.LegacyProject(p) {
				return "这个项目没走准备工位（无文字稿工件）——改稿用 save_manuscript + draft_storyboard 老路径"
			}
			return a.rerunPrep(ctx, p, strings.TrimSpace(in.Target), strings.TrimSpace(in.Note))
		},
	},
	{
		def:  toolDef("read_project", "查看项目当前状态与产物", nil),
		gate: nil,
		run: func(_ context.Context, a *Agent, p *pipeline.Project, _ string) string {
			return a.State(p.ID).summary()
		},
	},
	{
		def: toolDef("search_styles", "检索风格库（v3：标签+特色清单的可复用风格）。新建项目选风格、用户问「有什么风格」、或用户描述的气质可能命中库内条目时先查这里；命中后引导用户在风格门/风格库里选它（报 id）", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "气质/风格关键词（如 粘土定格 故障风 蒸汽波 等距）"},
			},
			"required": []string{"query"},
		}),
		gate: nil,
		run: func(ctx context.Context, a *Agent, p *pipeline.Project, args string) string {
			var in struct {
				Query string `json:"query"`
			}
			_ = json.Unmarshal([]byte(args), &in)
			styles, err := a.Lib.List()
			if err != nil {
				return "风格库读取失败：" + err.Error()
			}
			vocab := a.Lib.VocabList()
			tagName := func(id string) string {
				for _, e := range vocab {
					if e.ID == id {
						return e.Name
					}
				}
				return id
			}
			q := strings.ToLower(strings.TrimSpace(in.Query))
			var lines []string
			for _, ss := range styles {
				keys := []string{ss.ID, ss.Name, ss.Subject, ss.Tags.Main}
				keys = append(keys, ss.Tags.Aux...)
				for _, sg := range ss.Signatures {
					keys = append(keys, sg.Name, sg.Mechanism)
				}
				if q != "" && !strings.Contains(strings.ToLower(strings.Join(keys, " ")), q) {
					continue
				}
				state := map[bool]string{true: "已入库", false: "草稿待入库"}[ss.Published]
				line := fmt.Sprintf("- %s | %s | %s", ss.ID, ss.Name, state)
				if ss.Tags.Main != "" {
					line += " | 标签:" + tagName(ss.Tags.Main)
					for _, aux := range ss.Tags.Aux {
						line += "+" + tagName(aux)
					}
				}
				if len(ss.Signatures) > 0 {
					names := make([]string, 0, len(ss.Signatures))
					for _, sg := range ss.Signatures {
						names = append(names, sg.Name)
					}
					line += fmt.Sprintf(" | 特色×%d（%s）", len(ss.Signatures), strings.Join(names, "、"))
				}
				lines = append(lines, line)
			}
			if len(lines) == 0 {
				if q != "" && len(styles) > 0 {
					return "没有命中「" + q + "」的风格。库里现有 " + fmt.Sprintf("%d", len(styles)) + " 条，重调一次可放宽或换关键词"
				}
				return "风格库为空：出片后我会自动起草风格，或在风格库页上传参考视频解析入库"
			}
			return fmt.Sprintf("风格库 %d 条：\n", len(lines)) + strings.Join(lines, "\n") +
				"\n选用：用户点头后你调 apply_style（style_id）记入项目并出样张；或用户在风格门弹窗点卡片"
		},
	},
	{
		def: toolDef("web_search", "联网搜索实时资料（新闻/版本号/价格/数据等模型知识截止后的信息，或核实文稿事实）。写实元素（人物/产品/地标/新闻实物）的画面调研先搜再定。额度有限（套餐月约 200 次），必要才调，一次到位；要细读某条结果用 fetch_url（免费）", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":    map[string]any{"type": "string", "description": "搜索词，≤70 字，关键词组效果最好"},
				"recency":  map[string]any{"type": "string", "description": "时间范围（可选）：oneDay/oneWeek/oneMonth/oneYear，查新闻类用 oneWeek"},
				"location": map[string]any{"type": "string", "description": "地域（可选）：cn 中文区（默认）/ us 非中文区"},
			},
			"required": []string{"query"},
		}),
		gate: nil,
		run: func(ctx context.Context, _ *Agent, _ *pipeline.Project, args string) string {
			return runWebSearch(ctx, args)
		},
	},
	{
		def: toolDef("fetch_url", "读取一个网页的正文（纯文本，免费不占搜索额度）。用户贴了链接想让你看内容、或 web_search 结果里有要细看的条目时用它。只抓静态页面（文章/文档/博客/wiki），不执行 JS，重前端页面会读不到正文", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{"type": "string", "description": "要读取的网页地址（http/https）"},
			},
			"required": []string{"url"},
		}),
		gate: nil,
		run: func(ctx context.Context, _ *Agent, _ *pipeline.Project, args string) string {
			return runFetchURL(ctx, args)
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
		run: func(ctx context.Context, a *Agent, p *pipeline.Project, args string) string {
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
		run: func(ctx context.Context, a *Agent, p *pipeline.Project, args string) string {
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
		run: func(ctx context.Context, a *Agent, p *pipeline.Project, _ string) string {
			sb, err := p.LoadStoryboard()
			if err != nil {
				return "分镜读取失败：" + err.Error()
			}
			var b strings.Builder
			for _, seg := range sb.Segments {
				fmt.Fprintf(&b, "段%d %s「%s」（约%.0fs）旁白：%s\n", seg.Idx, seg.ID, seg.Key, seg.DurationHint, pipeline.FirstLine(seg.Narration, 60))
				// 画面构想与章节角标带上：模型答「这段画面是什么」不用猜，
				// 全文另读 storyboard.json 的场景用 edit_segment/rework 定位
				if strings.TrimSpace(seg.VisualBrief) != "" {
					fmt.Fprintf(&b, "  画面：%s\n", pipeline.FirstLine(seg.VisualBrief, 100))
				}
				if strings.TrimSpace(seg.Chapter) != "" {
					fmt.Fprintf(&b, "  章节：%s\n", seg.Chapter)
				}
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
				"voice": map[string]any{"type": "string", "description": "音色中文名（龙安灵心/龙安鲁风/晓晓/晓伊/云健/云希/云扬/云野）、完整 Edge 音色 id，或百炼音色 id（longanlingxin/longanlufeng，需 DASHSCOPE_API_KEY）；留空只列音色"},
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
		run: func(ctx context.Context, a *Agent, p *pipeline.Project, args string) string {
			var in struct {
				Voice string `json:"voice"`
			}
			_ = json.Unmarshal([]byte(args), &in)
			want := strings.TrimSpace(in.Voice)
			if want == "" {
				return "可用音色：龙安灵心（女·知心温暖，百炼·默认，需 DASHSCOPE_API_KEY）、龙安鲁风（男·明亮开朗，百炼）、晓晓（女·温暖）、晓伊（女·清亮）、云健（男·磁性浑厚）、云希（男·年轻阳光）、云扬（男·新闻专业）、云野（男·沉稳）。用 voice=名字 设置"
			}
			id := voiceAliases[want]
			if id == "" {
				id = want // 允许直接给完整 id（Edge/百炼）
			}
			if !isEdgeVoice(id) && !isBailianVoice(id) {
				return fmt.Sprintf("不认识音色 %q。可用：龙安灵心/龙安鲁风（百炼）、晓晓/晓伊/云健/云希/云扬/云野，或完整 Edge 音色 id", want)
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
		run: func(ctx context.Context, a *Agent, p *pipeline.Project, args string) string {
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
		run: func(ctx context.Context, a *Agent, p *pipeline.Project, _ string) string {
			st := a.State(p.ID)
			addr := "成片下载地址：/api/projects/" + p.ID + "/video/main.mp4（1080p，含旁白与逐词字幕）"
			// 地址照给，版本事实必须说：旧版/失败前旧片不许包装成最新成果递出去
			var notes []string
			if axes := staleAxes(st); len(axes) > 0 {
				notes = append(notes, fmt.Sprintf("成片是旧版（出片后改过 %s），要最新版先重新制作", strings.Join(axes, "、")))
			}
			if strings.HasSuffix(st.LastEvent, ".error") {
				notes = append(notes, "上次制作失败，这是失败前的旧片")
			}
			if len(notes) > 0 {
				return "注意：" + strings.Join(notes, "；") + "。" + addr
			}
			return addr
		},
	},
	{
		def: toolDef("read_manuscript", "读项目文稿全文（唯一真源 article.txt）。涉及文稿内容、措辞、或要核对文稿与分镜是否一致时，先读它再答——禁止凭印象答文本内容", nil),
		gate: func(s State) string {
			if !s.HasManuscript {
				return "还没有文稿"
			}
			return ""
		},
		run: func(_ context.Context, a *Agent, p *pipeline.Project, _ string) string {
			raw, err := os.ReadFile(p.Artifact("input/article.txt"))
			if err != nil { // 真源缺失回退管线产物（State.HasManuscript 的同款回退）
				var m contract.Manuscript
				if b, jerr := os.ReadFile(p.Artifact("manuscripts/manuscript.json")); jerr == nil && json.Unmarshal(b, &m) == nil {
					raw = []byte(m.Content)
					err = nil
				}
			}
			if err != nil {
				return "文稿读取失败：" + err.Error()
			}
			content := strings.TrimSpace(string(raw))
			const maxRunes = 8000
			r := []rune(content)
			if len(r) > maxRunes {
				return fmt.Sprintf("文稿（共 %d 字，超长只保留前 %d 字）：\n%s", len(r), maxRunes, string(r[:maxRunes]))
			}
			return fmt.Sprintf("文稿全文（%d 字）：\n%s", len(r), content)
		},
	},
	{
		def: toolDef("edit_segment", "精准编辑单个分镜段：旁白用词/段名/时长预估/章节角标，不动其他段。旁白改了下次制作会自动只重配该段（内容指纹检测）。重构段落划分/整体节奏才用 update_storyboard 整表重生成", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"segment":       map[string]any{"type": "string", "description": "目标段：段号（如 3）或段 id（如 seg03）"},
				"narration":     map[string]any{"type": "string", "description": "新的旁白全文（整段替换，不能为空）；不改就不带"},
				"key":           map[string]any{"type": "string", "description": "新的段名（舞台展示用）；不改就不带"},
				"duration_hint": map[string]any{"type": "number", "description": "新的时长预估（秒）；不改就不带"},
				"chapter":       map[string]any{"type": "string", "description": "章节角标（空串=清除）；不改就不带"},
			},
			"required": []string{"segment"},
		}),
		gate: func(s State) string {
			if !s.HasStoryboard {
				return "还没有分镜"
			}
			if s.Producing {
				return "制作/重做运行中，先等完成"
			}
			return ""
		},
		run: func(ctx context.Context, a *Agent, p *pipeline.Project, args string) string {
			var in struct {
				Segment      string   `json:"segment"`
				Narration    *string  `json:"narration"`
				Key          *string  `json:"key"`
				DurationHint *float64 `json:"duration_hint"`
				Chapter      *string  `json:"chapter"`
			}
			if err := json.Unmarshal([]byte(args), &in); err != nil {
				return "参数解析失败：" + err.Error()
			}
			changed := 0
			if in.Narration != nil {
				changed++
			}
			if in.Key != nil {
				changed++
			}
			if in.DurationHint != nil {
				changed++
			}
			if in.Chapter != nil {
				changed++
			}
			if changed == 0 {
				return "没有要改的字段（narration/key/duration_hint/chapter 至少给一个）"
			}
			if in.Narration != nil && strings.TrimSpace(*in.Narration) == "" {
				return "旁白不能为空（空旁白段两个 TTS 引擎都合不出声）"
			}
			sbPath := p.Artifact("storyboards/storyboard.json")
			// 先在内存里改好并过校验，才动磁盘文件：备份-写-失败还原的窗口越小越好
			sb, err := p.LoadStoryboard()
			if err != nil {
				return "分镜读取失败：" + err.Error()
			}
			segID, label, err := a.resolveSegment(p, in.Segment)
			if err != nil {
				return err.Error()
			}
			var seg *contract.Segment
			for i := range sb.Segments {
				if sb.Segments[i].ID == segID {
					seg = &sb.Segments[i]
					break
				}
			}
			if seg == nil {
				return "段 " + segID + " 不在分镜里"
			}
			var what []string
			if in.Narration != nil {
				seg.Narration = strings.TrimSpace(*in.Narration)
				what = append(what, "旁白")
			}
			if in.Key != nil {
				seg.Key = strings.TrimSpace(*in.Key)
				what = append(what, "段名")
			}
			if in.DurationHint != nil {
				if *in.DurationHint <= 0 || *in.DurationHint > 600 {
					return "duration_hint 需在 (0,600] 秒"
				}
				seg.DurationHint = *in.DurationHint
				what = append(what, fmt.Sprintf("时长（约 %.0fs）", seg.DurationHint))
			}
			if in.Chapter != nil {
				seg.Chapter = strings.TrimSpace(*in.Chapter)
				what = append(what, "章节角标")
			}
			if err := contract.ValidateStoryboard(sb); err != nil {
				return "修改后分镜未过校验（未落盘）：" + err.Error()
			}
			// 备份旧文件再原子写：写失败原样还原，项目任何时刻不留「分镜空洞」
			bak := sbPath + ".bak"
			if err := os.Rename(sbPath, bak); err != nil {
				return "备份旧分镜失败：" + err.Error()
			}
			if err := pipeline.WriteFileAtomic(sbPath, pipeline.MustJSONBytes(sb), 0o644); err != nil {
				if rerr := os.Rename(bak, sbPath); rerr != nil {
					return fmt.Sprintf("写入失败，且还原旧分镜也失败（备份在 %s）：%v（原因：%v）", bak, rerr, err)
				}
				return "写入失败（已还原旧分镜）：" + err.Error()
			}
			_ = os.Remove(bak)
			if a.OnEvent != nil {
				a.OnEvent(p.ID, "storyboard", "")
			}
			msg := fmt.Sprintf("%s 已改（%s）", label, strings.Join(what, "、"))
			if in.Narration != nil {
				msg += "；该段旁白下次制作自动重配，已有成片现为旧版"
			} else {
				msg += "；重新制作才会进成片"
			}
			return msg
		},
	},
	{
		def: toolDef("apply_style", "把风格库条目选定为本片强参考（记 style_id + 写风格简报，旧样张作废）。用户在检索结果里点了头就用它；选完必须再调 draft_style_samples 出样张（硬门不变）", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"style_id": map[string]any{"type": "string", "description": "风格库条目 id（search_styles 结果里拿）"},
				"disabled": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "要关掉的特色 id（默认全开吸收）"},
			},
			"required": []string{"style_id"},
		}),
		gate: func(s State) string {
			if s.Producing {
				return "制作运行中，先等本条完成"
			}
			return ""
		},
		run: func(ctx context.Context, a *Agent, p *pipeline.Project, args string) string {
			var in struct {
				StyleID  string   `json:"style_id"`
				Disabled []string `json:"disabled"`
			}
			_ = json.Unmarshal([]byte(args), &in)
			id := strings.TrimSpace(in.StyleID)
			if id == "" {
				return "缺少 style_id（search_styles 结果里拿）"
			}
			pack, ok := a.Lib.Get(id)
			if !ok || !pack.Published {
				return "风格不存在或未入库（草稿条目要用户在风格库页点「入库」）"
			}
			disabled := map[string]bool{}
			for _, sig := range in.Disabled {
				disabled[sig] = true
			}
			brief, err := a.Lib.BuildBrief(pack.ID, disabled)
			if err != nil {
				return "简报组装失败：" + err.Error()
			}
			if err := stylelib.WriteBriefFile(p.Artifact("style/style_brief.json"), brief); err != nil {
				return "简报写入失败：" + err.Error()
			}
			// 旧样张产物作废（强参考换了，设计必须重发明）：与舞台「选用」同清单
			for _, f := range []string{
				"style/style_samples.json", "style/skin.json", "style/design.md", "llm/style_samples.json",
				"style/sample-0.png", "style/sample-1.png", "style/sample-2.png",
			} {
				_ = os.Remove(p.Artifact(f))
			}
			_ = os.RemoveAll(p.Artifact("style/clips"))
			_ = os.RemoveAll(p.Artifact(".hf-clip"))
			_ = os.RemoveAll(p.Artifact(".hf-sample"))
			a.invalidateArtifacts(p,
				"style/style_samples.json", "style/skin.json", "style/design.md", "llm/style_samples.json",
				"style/sample-0.png", "style/sample-1.png", "style/sample-2.png")
			// style_id 记入 project.json：持 per-project 锁——对话轮内其他 SetProjectField
			// 与此处并发时各基于旧快照整文件回写、后写覆盖先写
			mu := pipeline.ProjectMu(p)
			mu.Lock()
			wrote := false
			if b, rerr := os.ReadFile(p.Artifact("project.json")); rerr == nil {
				var meta map[string]any
				if json.Unmarshal(b, &meta) == nil {
					meta["style_id"] = pack.ID
					wrote = pipeline.WriteFileAtomic(p.Artifact("project.json"), pipeline.MustJSONBytes(meta), 0o644) == nil
				}
			}
			mu.Unlock()
			if !wrote {
				return "style_id 记入 project.json 失败（文件读不出或写坏），简报已落、可重试"
			}
			// 风格门回到「等出样张」：状态按剩余前置降级（与舞台选用同语义）
			back := "created"
			if _, err := os.Stat(p.Artifact("input/article.txt")); err == nil {
				back = "manuscript"
			}
			if _, err := p.LoadStoryboard(); err == nil {
				back = "storyboard"
			}
			a.setDoneStatus(p.ID, back)
			if a.Emit != nil {
				a.Emit(p.ID, "style_applied", "", pack.Name)
			}
			msg := fmt.Sprintf("已选用风格「%s」（特色×%d 吸收），旧样张已作废。接着调 draft_style_samples 出样张（可带用户要求）", pack.Name, len(brief.Signatures))
			if len(in.Disabled) > 0 {
				msg += fmt.Sprintf("（关掉 %d 条特色）", len(in.Disabled))
			}
			return msg
		},
	},
	{
		def:  toolDef("cancel_production", "取消正在运行的制作/重做（整片或段级）。用户说停就停，别劝阻；取消终态会自动报进对话", nil),
		gate: nil,
		run: func(ctx context.Context, a *Agent, p *pipeline.Project, _ string) string {
			if a.Producer == nil {
				return "制作运行器未接线（Producer 为空）"
			}
			if !a.Producer.Cancel(p.ID) {
				return "当前没有正在进行的制作任务"
			}
			return "已请求取消，收尾几秒，终态我会报进对话"
		},
	},
	{
		def:  toolDef("read_check", "读最近一轮制作的逐段检查报告（frameqa 各段 pass/fail 与问题明细）与制作过程事件。用户问「哪几段没过/为什么」、或制作失败要定位原因时用它", nil),
		gate: nil,
		run: func(ctx context.Context, a *Agent, p *pipeline.Project, _ string) string {
			lines, err := produce.ReadJSONLTail(p.Artifact("manifest.jsonl"), 512)
			if err != nil {
				return "没有制作记录（manifest 读不出）：" + err.Error()
			}
			var qa, proc []string
			for _, l := range lines {
				var e struct {
					Event  string `json:"event"`
					Detail string `json:"detail"`
					TS     string `json:"ts"`
				}
				if json.Unmarshal(l, &e) != nil {
					continue
				}
				line := ""
				if len(e.TS) >= 19 {
					line = e.TS[11:19] + " "
				}
				line += e.Detail
				switch {
				case strings.HasPrefix(e.Event, "frameqa."):
					qa = append(qa, line)
				case strings.HasPrefix(e.Event, "produce.") || strings.HasPrefix(e.Event, "rework.") ||
					strings.HasPrefix(e.Event, "prereview.") || strings.HasPrefix(e.Event, "check."):
					proc = append(proc, e.Event+" "+line)
				}
			}
			if len(qa) == 0 && len(proc) == 0 {
				return "manifest 里没有画面检查/制作事件（还没制作过，或记录太老被截断）"
			}
			// 过程事件只留尾部 30 条：过程很长，头部全是开场白
			if len(proc) > 30 {
				proc = proc[len(proc)-30:]
			}
			var b strings.Builder
			if len(qa) > 0 {
				b.WriteString("画面检查（最近一轮）：\n" + strings.Join(qa, "\n") + "\n\n")
			}
			b.WriteString("制作过程（尾部）：\n" + strings.Join(proc, "\n"))
			return b.String()
		},
	},
	{
		def:  toolDef("list_projects", "列出数据目录里的全部项目（id/名称/状态/有无成片）。用户问「有哪些项目」「之前那个 XX 项目」时用它", nil),
		gate: nil,
		run: func(ctx context.Context, a *Agent, p *pipeline.Project, _ string) string {
			entries, err := os.ReadDir(filepath.Join(a.DataDir, "projects"))
			if err != nil {
				return "项目目录读不出：" + err.Error()
			}
			uid, role := userFrom(ctx)
			filterByOwner := uid != "" && role != "admin" // 开放模式/直测不过滤，与 api 列表同语义
			var rows []string
			for _, e := range entries {
				if !e.IsDir() || strings.HasPrefix(e.Name(), "_") {
					continue
				}
				b, rerr := os.ReadFile(filepath.Join(a.DataDir, "projects", e.Name(), "project.json"))
				if rerr != nil {
					continue
				}
				var meta struct {
					Name   string `json:"name"`
					Status string `json:"status"`
					UserID string `json:"user_id"`
				}
				if json.Unmarshal(b, &meta) != nil {
					continue
				}
				if filterByOwner && meta.UserID != uid {
					continue
				}
				video := ""
				if _, serr := os.Stat(filepath.Join(a.DataDir, "projects", e.Name(), "renders", "main.mp4")); serr == nil {
					video = " ✓成片"
				}
				rows = append(rows, fmt.Sprintf("- %s「%s」%s%s", e.Name(), meta.Name, meta.Status, video))
			}
			if len(rows) == 0 {
				return "没有项目（create_project 可新建）"
			}
			return fmt.Sprintf("共 %d 个项目：\n", len(rows)) + strings.Join(rows, "\n")
		},
	},
	{
		def: toolDef("create_project", "新建一个视频项目（名称+一句话主题；可选带文稿直建）。新建成功后用户在项目列表里打开它——对话仍留在当前项目", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":       map[string]any{"type": "string", "description": "项目名（缺省取主题前 24 字）"},
				"topic":      map[string]any{"type": "string", "description": "一句话主题（带 manuscript 时可省）"},
				"manuscript": map[string]any{"type": "string", "description": "可选：文稿全文直建（等同建完粘贴保存）"},
				"aspect":     map[string]any{"type": "string", "description": "画幅：9:16（默认）| 16:9"},
			},
		}),
		gate: nil,
		run: func(ctx context.Context, a *Agent, p *pipeline.Project, args string) string {
			var in struct {
				Name       string `json:"name"`
				Topic      string `json:"topic"`
				Manuscript string `json:"manuscript"`
				Aspect     string `json:"aspect"`
			}
			_ = json.Unmarshal([]byte(args), &in)
			in.Topic = strings.TrimSpace(in.Topic)
			in.Manuscript = strings.TrimSpace(in.Manuscript)
			if in.Topic == "" && in.Manuscript == "" {
				return "主题必填（一句话说清要讲什么），或带上文稿全文"
			}
			if in.Aspect != "16:9" && in.Aspect != "9:16" {
				in.Aspect = "9:16"
			}
			if in.Name == "" {
				src := in.Topic
				if src == "" {
					src = strings.SplitN(in.Manuscript, "\n", 2)[0]
				}
				in.Name = src
				if r := []rune(in.Name); len(r) > 24 {
					in.Name = string(r[:24])
				}
			}
			// id 秒级时间戳 + os.Mkdir 原子占号（与 api 建项目同一写法，同秒连建撞号换号重试）
			id := "p" + time.Now().Format("20060102-150405")
			if err := os.MkdirAll(filepath.Join(a.DataDir, "projects"), 0o755); err != nil {
				return "建目录失败：" + err.Error()
			}
			np := pipeline.NewProject(a.DataDir, id)
			n := 2
			for {
				err := os.Mkdir(np.Dir, 0o755)
				if err == nil {
					break
				}
				if !os.IsExist(err) {
					return "建项目目录失败：" + err.Error()
				}
				if n > 10 {
					return "项目 ID 连续撞号，放弃创建（时钟/磁盘异常？）"
				}
				id = fmt.Sprintf("p%s-%d", time.Now().Format("20060102-150405"), n)
				np = pipeline.NewProject(a.DataDir, id)
				n++
			}
			for _, d := range []string{
				"input", "llm", "manuscripts", "storyboards", "style",
				"audio", "compositions/frames", "compositions/registry", "renders",
			} {
				_ = os.MkdirAll(filepath.Join(np.Dir, d), 0o755)
			}
			if in.Manuscript != "" {
				if err := os.WriteFile(np.Artifact("input/article.txt"), []byte(in.Manuscript), 0o644); err != nil {
					return "文稿写入失败：" + err.Error()
				}
			}
			uid, _ := userFrom(ctx)
			meta, _ := json.MarshalIndent(map[string]any{
				"id": id, "name": in.Name, "status": "created", "aspect": in.Aspect,
				"topic": in.Topic, "user_id": uid, "created_at": time.Now().Format(time.RFC3339),
			}, "", "  ")
			if err := os.WriteFile(np.Artifact("project.json"), meta, 0o644); err != nil {
				return "project.json 写入失败：" + err.Error()
			}
			np.Manifest("project.created", fmt.Sprintf("%s（%s）", in.Name, in.Aspect))
			_ = a.Store.CreateProject(id, in.Name, uid)
			msg := fmt.Sprintf("项目已建：%s（%s，%s）。用户在项目列表里打开它继续——当前对话还在「%s」",
				id, in.Name, in.Aspect, a.State(p.ID).Name)
			if in.Manuscript != "" {
				msg += "；文稿已带入，下一步 draft_storyboard 出分镜"
			} else {
				msg += "；补文稿：draft_manuscript 起草或让用户粘贴后 save_manuscript"
			}
			return msg
		},
	},
	{
		def:  toolDef("list_references", "列出风格参考视频库（上传的参考片及其解析状态：pending/running/done/error、是否已入库）。用户问参考素材、或要引导上传参考片时用它", nil),
		gate: nil,
		run: func(ctx context.Context, a *Agent, p *pipeline.Project, _ string) string {
			if a.Refs == nil {
				return "参考视频库未接线（服务端未配置）"
			}
			refs, err := a.Refs.List()
			if err != nil {
				return "参考库读取失败：" + err.Error()
			}
			if len(refs) == 0 {
				return "参考库为空。用户可在「参考」页上传 mp4/mov/webm（≤200MB），上传即自动解析出标签+特色"
			}
			tagName := a.tagNames()
			var rows []string
			for _, r := range refs {
				state := map[string]string{"pending": "待解析", "running": "解析中", "done": "已解析", "error": "解析失败"}[r.Status]
				if state == "" {
					state = r.Status
				}
				if r.Confirmed {
					state += "·已入库"
				}
				line := fmt.Sprintf("- %s「%s」%s", r.ID, r.Name, state)
				if r.Status == "error" && r.Error != "" {
					line += "：" + pipeline.FirstLine(r.Error, 80)
				}
				if r.Style != nil {
					names := tagName(r.Style.Tags.Main)
					for _, aux := range r.Style.Tags.Aux {
						names += "+" + tagName(aux)
					}
					line += fmt.Sprintf(" | 标签:%s | 特色×%d", names, len(r.Style.Signatures))
				}
				rows = append(rows, line)
			}
			return fmt.Sprintf("参考库 %d 条：\n", len(rows)) + strings.Join(rows, "\n") +
				"\n入库确认在「参考」页人工过目证据帧后进行；入库后条目出现在风格库（search_styles 可检索）"
		},
	},
	{
		def: toolDef("analyze_reference", "触发/重试一个参考视频的解析（上传时已自动起过一次）。解析是后台作业，完成后用 list_references 看结果", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "参考条目 id（list_references 结果里拿）"},
			},
			"required": []string{"id"},
		}),
		gate: nil,
		run: func(ctx context.Context, a *Agent, p *pipeline.Project, args string) string {
			if a.Refs == nil {
				return "参考视频库未接线（服务端未配置）"
			}
			var in struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal([]byte(args), &in)
			id := strings.TrimSpace(in.ID)
			if id == "" {
				return "缺少参考条目 id"
			}
			started, err := a.Refs.Analyze(id)
			if err != nil {
				return "解析启动失败：" + err.Error()
			}
			if !started {
				return "该参考已在解析中，等 list_references 里状态变 done"
			}
			return "解析已启动（后台跑，抽帧+打标约一两分钟），完成后 list_references 看结果"
		},
	},
}

// tagNames 词表 id → 正式名（参考库/风格库列标签用；查不到原样显示 id）。
func (a *Agent) tagNames() func(id string) string {
	vocab := a.Lib.VocabList()
	return func(id string) string {
		for _, e := range vocab {
			if e.ID == id {
				return e.Name
			}
		}
		return id
	}
}

// saveManuscriptContent 文稿落盘的统一语义（save_manuscript 与 draft_manuscript
// save=true 共用）：article.txt 是唯一真源；manuscript.json 是重放产物，不删的话
// 重新制作时 TTS 拿的还是旧稿；已有分镜回报过期但不硬删——与文稿弹窗保存同语义。
func (a *Agent) saveManuscriptContent(p *pipeline.Project, content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return "缺少文稿正文"
	}
	// prep 链运行中拒改稿（2026-10-19 中-30，与 api 侧 saveManuscript 同口径）：
	// 改稿的级联作废会与后台链的工件写入互踩
	if a.Preps != nil && a.Preps.IsRunning(p.ID) {
		return "准备阶段运行中，等它完成再改稿（改稿会级联作废准备工件）。"
	}
	// 落盘+级联作废走统一语义（2026-10-19 中-34，与 api saveManuscript 同源）
	invalidated, err := prep.ReplaceManuscript(p, content)
	if err != nil {
		return "保存失败：" + err.Error()
	}
	_, hasSb := os.Stat(p.Artifact("storyboards/storyboard.json"))
	a.setDoneStatus(p.ID, "manuscript")
	msg := fmt.Sprintf("文稿已保存（%d 字）", len([]rune(content)))
	if hasSb == nil {
		msg += "；已有分镜基于旧稿，要重新出分镜才会生效"
	}
	if invalidated {
		msg += "；准备工件（文字稿/风格包/分镜稿）已按新稿级联作废，重跑后需重新终审确认"
	}
	return msg
}

// formatArticle 网页正文整理成回喂文本：超长截断，防一次读取撑爆上下文。
func formatArticle(art *webfetch.Article) string {
	head := ""
	if art.Title != "" {
		head = "《" + art.Title + "》\n"
	}
	const maxRunes = 15000
	content := []rune(art.Content)
	if len(content) > maxRunes {
		return fmt.Sprintf("已读取网页（共 %d 字）：\n%s%s\n\n（正文过长，只保留前 %d 字）", art.Words, head, string(content[:maxRunes]), maxRunes)
	}
	return fmt.Sprintf("已读取网页（%d 字）：\n%s%s", art.Words, head, art.Content)
}

func toolDef(name, desc string, params map[string]any) openai.Tool {
	if params == nil {
		params = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{
		Name: name, Description: desc, Parameters: params,
	}}
}
