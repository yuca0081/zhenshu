package prep

// unified.go —— 统一准备智能体 rev2（docs/prep-unified-agent-design.md）：
// 一个 ReAct agent 单上下文产出三件（文稿/风格包/分镜），工作顺序与返工节奏
// 由模型定。原则：过程放开（怎么干模型定），终态焊死（契约+过闸+咬合）。
//
// 与老三站链的关系：project.json unified_prep=1 走本链（E 系列实验档，前端
// 暂不暴露），否则老链兜底；落盘工件、打包终审、produce 全部同构，下游零改动。
//
// 工具五类（§4）：读输入（read_source/web_search/fetch_page）/ 查风格库
// （search_styles 等）/ 出样张（render_sample 内循环黑盒只回结论）/ 校时长 /
// 写工件（write_script / write_stylepack / write_storyboard，保存即校验+失效
// 提醒）+ 完工（finish_prep 终态咬合检查，预算耗尽 runner 自动执行）。

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/pipeline"
	"webpage-video-agent/server/internal/react"
)

// 统一链预算（§7 总池制）：老链 6/40/25 阶段配额合并为总池，模型自调度
// （样张子循环的大头额度仍在 render_sample 内部）；链级总闸 80 分钟照兜底。
// 回合预算只防光说不做的打转——时间闸才是主约束。
const (
	unifiedBudget   = 70 * time.Minute
	unifiedRounds   = 120 // 老链三站回合和（30+30+60）同量级
	unifiedWriteMax = 10  // write_* 每件病理上限：只防死循环空转，不防正常迭代
)

// unifiedSystem 系统提示词（§5 全文，本设计的核心交付物）：三段结构——权责与
// 材料 / 过程与终态合同 / 创作放权。方针只写过程与合同，不写品味。
const unifiedSystem = `你是这支视频的编导负责人，对三件产物负全责：口播文稿（write_script）、风格包（write_stylepack，设计系统要经样张验证站得住）、分镜（write_storyboard）。
开场已附用户想法与材料，动手前先 read_source 吃透——材料只是一句话想法时，先 web_search 检索再动笔。
三件随时可写可改——write 保存即校验：过闸错误、以及「这次修改让哪些下游失效」都会跟着保存回给你，照着修到零错误、改到重新咬合。硬事实逐条给出来源。全部完成时调用 finish_prep：三件在盘、过闸、相互咬合（分镜句引用对得上最终稿、段长落在最终风格带内）才放行。
画面里出现什么、怎么表演、有没有角色形象，由你根据题材、文稿口吻与风格气质决定；定了角色就在 cast_legend 写清外形/身体/眼睛/性格并全片保持一致。除本提示词列出的流程与契约外，没有任何附加创作限制。`

// UnifiedPrep 统一准备链开关（project.json unified_prep=1；E 系列实验档，
// curl/对话建实验项目，前端暂不暴露）。
func UnifiedPrep(p *pipeline.Project) bool {
	return strings.TrimSpace(pipeline.ReadProjectField(p, "unified_prep")) == "1"
}

// runUnified 统一链编排（run.go 分支进入）：总池预算 + 冷重试一次 + 收口记账。
// plan 语义与老链一致：nil=缓存语义（缺哪件补哪件），非空=勾选集（本链由
// Runner.Start 收敛为三件全选——三件互相影响，部分重做没有意义）。
func runUnified(ctx context.Context, p *pipeline.Project, d Deps, plan []string) error {
	sctx, scancel := context.WithTimeout(ctx, unifiedBudget)
	defer scancel()
	err := runWithColdRetry(sctx, p, "unified", func(c context.Context) error {
		return runUnifiedAgent(c, p, d, plan)
	})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			err = fmt.Errorf("统一准备链超出 %s 总池预算——已完成：%s。三件已齐且咬合则已自动收口，否则重跑将从缺的补起（已完成工件走缓存不重烧）",
				unifiedBudget, doneStations(p))
			p.Manifest("prep.station_budget", "unified")
		}
		p.Manifest("prep.error", "unified: "+pipeline.FirstLine(err.Error(), 160))
		d.emit(p.ID, "stage", "prep", "error: "+pipeline.FirstLine(err.Error(), 120))
		d.emit(p.ID, "prep_updated", "", "error")
		return err
	}
	p.Manifest("prep.done", "三工件就绪（统一链），等待打包终审")
	d.emit(p.ID, "stage", "prep", "done")
	if plan == nil {
		d.emit(p.ID, "prep_updated", "", "done")
	}
	return nil
}

// runUnifiedAgent 单会话统一智能体：组装工具跑 Loop；预算耗尽自动终检
// （§7——模型忘调 finish_prep 也不浪费已落盘成果）。
func runUnifiedAgent(ctx context.Context, p *pipeline.Project, d Deps, plan []string) error {
	sr := &scriptRun{p: p, d: d}
	st := &styleRun{p: p, d: d}
	st.chosen = restoreStyleChoice(p, d)
	// 抢收线：总池 deadline 前 styleGrabWindow；指路话术覆写为先落盘再收口。
	if dl, ok := ctx.Deadline(); ok {
		st.grabAt = dl.Add(-styleGrabWindow)
		st.grabHint = fmt.Sprintf("时间预算将尽（剩不足 %s）——不要再渲新样张也不要再修旧的，立即用 write_stylepack 落盘现有成果、再 finish_prep 收口：带伤交付优先于超时全烧，缺一张样张是小事，链超时是全烧。", styleGrabWindow)
	}
	u := &unifiedRun{p: p, d: d, sr: sr, st: st, target: targetSeconds(p), writes: map[string]int{}}
	log := openSessionLog(p, "unified")
	defer log.close()
	loop := &Loop{
		Prov:      unifiedProv(),
		System:    unifiedSystem,
		Kickoff:   unifiedKickoff(p, d, plan),
		Tools:     nil, // 下方组装（render_sample 要拿 loop 引用）
		Submit:    "finish_prep",
		MaxRounds: unifiedRounds,
		OnStep:    manifestStep(p, "unified", log),
	}
	loop.Tools = u.tools(loop)
	d.emit(p.ID, "stage", "prep/unified", "running")
	err := runTracked(ctx, p, "prep.unified", "准备·统一智能体", loop)
	if err != nil {
		p.Manifest("prep.unified_error", pipeline.FirstLine(err.Error(), 200))
	}
	// 预算耗尽自动终检：能过就过，不能过带缺口清单报错退出。
	if err != nil && (ctx.Err() != nil || errors.Is(err, ErrBudgetExhausted)) {
		if gaps := unifiedGapReport(p, d); len(gaps) == 0 {
			p.Manifest("prep.unified_autofinish", "预算耗尽自动终检通过——三件在盘、过闸、咬合")
			return nil
		} else if ctx.Err() != nil {
			return fmt.Errorf("预算耗尽（%w），终检未过：\n- %s", ctx.Err(), strings.Join(gaps, "\n- "))
		} else {
			return fmt.Errorf("回合预算耗尽，终检未过：\n- %s", strings.Join(gaps, "\n- "))
		}
	}
	return err
}

// unifiedProv 统一链主脑：与②同档（文本主力——三件里最重的活是设计系统+
// 样张 HTML 写作），未配置时退①的档。
func unifiedProv() react.ChatClient {
	if prov, err := llm.FromEnv(llm.StationPrepStyleText); err == nil {
		return prov
	}
	prov, _ := llm.FromEnv(llm.StationPrepScript)
	return prov
}

// unifiedKickoff 开工语 + 盘面注入（崩溃续跑/重跑同语义：盘上已过闸工件写明，
// 模型从缺的补起——与老链「已完成走缓存」对齐）。
func unifiedKickoff(p *pipeline.Project, d Deps, plan []string) string {
	cv := pipeline.ProjectCanvas(p)
	var b strings.Builder
	fmt.Fprintf(&b, "开工。本片画幅 %s（%d×%d）。先 read_source 吃透材料与目标时长；三件的工作顺序你自己定。盘面状态：", cv.Aspect, int(cv.W), int(cv.H))
	if s, err := LoadScript(p); err == nil {
		fmt.Fprintf(&b, "\n- 文稿已落盘：%d 句 / %d 字（约 %.0f 秒）", len(s.Sentences), s.WordCount, contract.EstimateSpeechDuration(s.Text))
	} else {
		b.WriteString("\n- 文稿：缺")
	}
	if sp, err := LoadStylePack(p); err == nil {
		lo, hi, _ := sp.Grammar.ClampedBand()
		fmt.Fprintf(&b, "\n- 风格包已落盘：%s（段长带 %.1f–%.1f 秒）", sp.StyleID, lo, hi)
	} else {
		b.WriteString("\n- 风格包：缺")
	}
	if dr, err := LoadDraft(p); err == nil {
		fmt.Fprintf(&b, "\n- 分镜已落盘：%d 段 / 约 %.0f 秒", len(dr.Segments), dr.TotalHint())
	} else {
		b.WriteString("\n- 分镜：缺")
	}
	if plan != nil {
		fmt.Fprintf(&b, "\n用户本次要求重做：%s。计划内工件即使盘上已有也要重写；不在计划内的盘上工件可复用，finish_prep 的咬合校验会兜底。", StationListText(plan))
	} else {
		b.WriteString("\n缺哪件补哪件；已落盘且过闸的直接复用——finish_prep 会终检咬合。")
	}
	if brief := pipeline.LoadStyleBrief(p); brief != nil {
		name := brief.EntryName
		if name == "" {
			name = brief.EntryID
		}
		fmt.Fprintf(&b, "\n用户已在风格库为本片选定条目「%s」（%s）——这是硬要求：get_style_detail 先看它，风格包与样张以它为准；确有更强理由换选时必须先说明理由。", name, brief.EntryID)
	}
	if idea := noteFile(p, IdeaFile); idea != "" {
		b.WriteString("\n用户的想法（选题角度与侧重优先体现，留空处照常发挥）：" + idea)
	}
	for _, n := range []string{"input/script_notes.txt", "input/style_notes.txt", "input/storyboard_notes.txt"} {
		if notes := noteFile(p, n); notes != "" {
			fmt.Fprintf(&b, "\n用户修改要求（%s，优先满足）：%s", filepath.Base(n), notes)
		}
	}
	return b.String()
}

// unifiedRun 统一链会话状态：scriptRun 管 used（行为闸）与检索缓存，styleRun
// 管 chosen/样张草稿/衍生登记，writes 是 write_* 病理上限的计数。
type unifiedRun struct {
	p      *pipeline.Project
	d      Deps
	sr     *scriptRun
	st     *styleRun
	target float64
	writes map[string]int
}

// tools 组装 11 个工具（§4 清单；老站工具原样复用，write_*/finish 是新的）。
func (u *unifiedRun) tools(loop *Loop) []Tool {
	searchLeft := scriptSearchBudget
	fetchLeft := scriptFetchBudget
	snapLeft := styleSampleMax
	return []Tool{
		u.sr.readSourceTool(u.target),
		u.sr.searchTool(&searchLeft),
		u.sr.fetchTool(&fetchLeft),
		u.estimateTool(),
		u.st.searchStylesTool(),
		u.st.getStyleDetailTool(loop),
		u.st.getStyleUsageTool(),
		u.st.registerStyleTool(),
		u.st.renderSampleTool(&snapLeft, loop),
		u.writeScriptTool(),
		u.writeStylepackTool(),
		u.writeStoryboardTool(),
		u.finishTool(),
	}
}

// estimateTool 时长自检（统一版）：秒数 + 目标带 + 盘上风格包的段长带（有才报）。
func (u *unifiedRun) estimateTool() Tool {
	return Tool{
		Def: toolSchema("estimate_speech_duration", "估算文本的口播时长（秒）。口播语速约 4.5 字/秒；写稿/切段前自检。", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"text": map[string]any{"type": "string", "description": "要估算的旁白正文"},
			},
			"required": []string{"text"},
		}),
		Run: func(_ context.Context, args string) Observation {
			var in struct {
				Text string `json:"text"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return Observation{Text: err.Error()}
			}
			sec := contract.EstimateSpeechDuration(in.Text)
			out := fmt.Sprintf("约 %.0f 秒（%d 字 @ %.1f 字/秒）；目标 %.0f 秒（±20%% 合理带 %.0f–%.0f 秒）",
				sec, len([]rune(in.Text)), contract.SpeechRate, u.target, u.target*0.8, u.target*1.2)
			if sp, err := LoadStylePack(u.p); err == nil {
				if lo, hi, ok := sp.Grammar.ClampedBand(); ok {
					out += fmt.Sprintf("；盘上风格包段长带 %.1f–%.1f 秒（每段落带内）", lo, hi)
				}
			}
			return Observation{Text: out}
		},
	}
}

// writeCapped write_* 病理上限的入口检查（每件成功落盘 ≤unifiedWriteMax 次）。
// 只计成功落盘——参数解析失败（deepseek-flash 大 JSON 截断会连败多次，2026-11-09
// 首跑 8 次里 6 次是参数错差点烧光额度）与被拒不计数：帽防的是「写成了还在
// 反复重写」的空转，修错迭代交给时间闸与回合预算。
func (u *unifiedRun) writeCapped(name string) bool {
	return u.writes[name] >= unifiedWriteMax
}

// countWrite 成功落盘后计数（与 writeCapped 配对）。
func (u *unifiedRun) countWrite(name string) {
	u.writes[name]++
}

func writeCapMessage(name string, n int) string {
	return fmt.Sprintf("write_%s 已成功落盘 %d 次（病理上限 %d）——同一件反复重写是在空转。停下复盘：要么按上一轮的失效提醒把下游一并改到重新咬合，要么先去做别的件，回头再收这件。", name, n, unifiedWriteMax)
}

// writeScriptTool 落盘/改写文稿（保存即校验 + 下游失效提醒）。
func (u *unifiedRun) writeScriptTool() Tool {
	return Tool{
		Def: toolSchema("write_script", "落盘或改写口播文稿（保存即校验）：text=旁白正文；fact_sheet=事实清单（断言→来源 URL，本会话用过 web_search 则必填且每条带 URL）。半成品可以先不写；改稿若让盘上分镜的句引用失效，失效清单会随保存返回。", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"text": map[string]any{"type": "string", "description": "旁白正文（纯文本，无标题/列表/结构标记）"},
				"fact_sheet": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"claim": map[string]any{"type": "string", "description": "事实断言（年份/数字/人名/结论）"},
							"url":   map[string]any{"type": "string", "description": "来源 URL"},
						},
						"required": []string{"claim", "url"},
					},
				},
			},
			"required": []string{"text"},
		}),
		Run: func(_ context.Context, args string) Observation {
			if u.writeCapped("script") {
				return Observation{Text: writeCapMessage("script", u.writes["script"])}
			}
			obs, ok := u.sr.writeScript(args, u.target)
			if !ok {
				return obs
			}
			u.countWrite("script")
			if s, err := LoadScript(u.p); err == nil {
				if note := u.scriptExpiryNote(s); note != "" {
					obs.Text += "\n" + note
				}
			}
			return obs
		},
	}
}

// scriptExpiryNote write_script 的下游失效提醒（§8）：盘上分镜引用了新稿里
// 已不存在的句子（悬空引用），逐条列出——模型照着改分镜到重新咬合。
func (u *unifiedRun) scriptExpiryNote(s *contract.Script) string {
	d, err := LoadDraft(u.p)
	if err != nil {
		return "" // 盘上没有分镜，无从失效
	}
	var dangling []string
	for _, seg := range d.Segments {
		refs, rerr := contract.ParseNarrationRefs(seg.NarrationRefs)
		if rerr != nil {
			dangling = append(dangling, fmt.Sprintf("%s：引用写法 %q 已不合法", seg.ID, seg.NarrationRefs))
			continue
		}
		for _, id := range refs {
			if _, ok := s.Sentence(id); !ok {
				dangling = append(dangling, fmt.Sprintf("%s 引用 %s（新稿已无此句）", seg.ID, id))
			}
		}
	}
	if len(dangling) == 0 {
		return ""
	}
	return "⚠ 盘上分镜与本次文稿改动已不咬合：\n- " + strings.Join(dangling, "\n- ") +
		"\n→ 改 write_storyboard 到重新咬合（句引用恰好铺满新稿 N1..Nn）。"
}

// writeStylepackTool 落盘/改写风格包（保存即校验 + 段长带失效提醒）。
func (u *unifiedRun) writeStylepackTool() Tool {
	return Tool{
		Def: toolSchema("write_stylepack", "落盘或改写风格包（保存即校验），一次交齐四件：风格描述、剪辑语法、设计系统、样张 id 列表。样张先于风格包定稿——sample_ids 只收 render_sample 渲过的张。段长带改动若让盘上分镜超带，失效清单会随保存返回。", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"style_id":    map[string]any{"type": "string", "description": "风格条目 id（库内或本次 register_style 登记）"},
				"description": map[string]any{"type": "string", "description": "风格描述：视觉语言、色彩、质感、构图倾向，附选择理由（一段话，分镜靠它理解语境）"},
				"grammar": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"density":    map[string]any{"type": "string", "description": "切分密度（疏/中/密 + 一句话依据）"},
						"band_min":   map[string]any{"type": "number", "description": "段长带下限（秒，含入场动画预算；与全局钳制 5–20 取交集）"},
						"band_max":   map[string]any{"type": "number", "description": "段长带上限（秒）"},
						"continuity": map[string]any{"type": "string", "description": "同舞台延续强弱（强=多段同舞台，弱=新场景快切）"},
					},
					"required": []string{"density", "band_min", "band_max", "continuity"},
				},
				"design_system": map[string]any{
					"type": "object",
					"description": "本片设计系统。字段（全部必填除非注明）：direction 方向名；concept 概念一句话（为什么配这部片）；" +
						"skins:[{name,slots:{bg,txt,accent,font,mute?,surface?,panel_txt?,radius?,border_w?},css≤4000字符}] 1–2 套，色值 #RRGGBB、txt 对 bg 对比度 ≥4.5:1、font 含中文字体、radius/border_w 是数字 px（如 12、2，不是 \"12px\" 字符串），双皮肤必须另给 skin_rule；" +
						"fonts:{display:{stack,weight?,tracking?},body:{...},data?{...}} 三角色分工、每栈含中文字体、weight 是字符串（如 \"700\"，不是数字 700）；" +
						"motion:{ease,dur_in(0.1-1.5),dur_settle(0.05-1.0),stagger(0-0.5),entrances:[{name,how,use_for}] 3-5条互异,rhythm,exits,camera,transition,notes}；" +
						"device:{name,desc,per_seg} 贯穿装置；entry_exit:{entry,exit,entry_dur(0.2-1.0)} 标准入场/收尾动画模板（转场载体，一套模板一段内生效，禁止逐段即兴发明）；" +
						"layouts:[{id,name,desc,layered?}] 0 个或 ≥2 个、layered 是字符串（有叠层写「哪层压哪层、为何可读」，无叠层不传——不是布尔）；" +
						"anti_patterns ≤8 条；post:{vignette,grain,chroma} 各 0-1",
				},
				"sample_ids": map[string]any{
					"type": "array", "items": map[string]any{"type": "string"},
					"description": "样张 id 列表（render_sample 渲过的张，如 [\"样张 A · …\"]；≥1）",
				},
			},
			"required": []string{"style_id", "description", "grammar", "design_system"},
		}),
		Run: func(ctx context.Context, args string) Observation {
			if u.writeCapped("stylepack") {
				return Observation{Text: writeCapMessage("stylepack", u.writes["stylepack"])}
			}
			obs, ok := u.st.writeStylepack(ctx, args)
			if !ok {
				return obs
			}
			u.countWrite("stylepack")
			if sp, err := LoadStylePack(u.p); err == nil {
				if note := u.bandExpiryNote(sp.Grammar); note != "" {
					obs.Text += "\n" + note
				}
			}
			return obs
		},
	}
}

// bandExpiryNote write_stylepack 的下游失效提醒（§8）：新段长带下超带的
// 盘上分镜段逐条列出。
func (u *unifiedRun) bandExpiryNote(g contract.EditGrammar) string {
	d, err := LoadDraft(u.p)
	if err != nil {
		return ""
	}
	lo, hi, ok := g.ClampedBand()
	if !ok {
		return "⚠ 新段长带与全局钳制无交集——盘上分镜必然全部超带，先改段长带。"
	}
	var over []string
	for _, seg := range d.Segments {
		if seg.DurationHint < lo || seg.DurationHint > hi {
			over = append(over, fmt.Sprintf("%s %.1fs", seg.ID, seg.DurationHint))
		}
	}
	if len(over) == 0 {
		return ""
	}
	return fmt.Sprintf("⚠ 盘上分镜超出新段长带 [%.1f, %.1f]：%s\n→ 改 write_storyboard（调段长或并段拆段）到重新咬合。", lo, hi, strings.Join(over, "、"))
}

// writeStoryboardTool 落盘/改写分镜（保存即校验 + 对当前盘上稿/风格包的咬合校验）。
func (u *unifiedRun) writeStoryboardTool() Tool {
	return Tool{
		Def: toolSchema("write_storyboard", "落盘或改写分镜稿（保存即校验）：narration_refs 恰好铺满盘上文稿 N1..Nn、段长落盘上风格包段长带内、cast_legend↔cast 对账、必需素材认领。校验不过会被拒并逐条回喂错误。", map[string]any{
			"type":       "object",
			"properties": map[string]any{"draft": storyboardDraftParam()},
			"required":   []string{"draft"},
		}),
		Run: func(_ context.Context, args string) Observation {
			if u.writeCapped("storyboard") {
				return Observation{Text: writeCapMessage("storyboard", u.writes["storyboard"])}
			}
			draft, err := parseDraftArg(args)
			if err != nil {
				return Observation{Text: "参数错误：" + err.Error()}
			}
			script, err := LoadScript(u.p)
			if err != nil {
				return Observation{Text: "盘上还没有文稿——先 write_script（分镜的句引用要铺满它）"}
			}
			pack, err := LoadStylePack(u.p)
			if err != nil {
				return Observation{Text: "盘上还没有风格包——先 write_stylepack（分镜的段长要落它的带内）"}
			}
			if err := contract.ValidateStoryboardDraft(draft, script, pack); err != nil {
				return Observation{Text: "被拒（逐条修完再写）：\n- " + err.Error()}
			}
			if err := contract.ValidateDraftShots(draft); err != nil {
				return Observation{Text: "被拒（镜头表不完整，逐条修完再写）：\n- " + err.Error()}
			}
			req, all := imageFileSets(u.p)
			if err := contract.ValidateShotImages(draft, req, all); err != nil {
				return Observation{Text: "被拒（必需素材没被认领/认领了但画面提示没写，逐条修完再写）：\n- " + err.Error()}
			}
			final := contract.NewStoryboardDraft(draft.Plan, draft.World, draft.CastLegend, draft.Paragraphs, draft.Segments)
			summary, err := persistDraft(u.p, u.d, final, script, pack)
			if err != nil {
				return Observation{Text: "分镜落盘失败：" + err.Error()}
			}
			u.countWrite("storyboard")
			return Observation{Text: "分镜稿已落盘：" + summary + "。"}
		},
	}
}

// finishTool 终态咬合检查（§7 loop 唯一出口）。
func (u *unifiedRun) finishTool() Tool {
	return Tool{
		Def: toolSchema("finish_prep", "终态咬合检查（唯一收口）：三件在盘、过闸、相互咬合（分镜句引用 ⊆ 最终稿、段长 ⊆ 最终风格带、样张属于最终风格包）才放行；缺口清单会逐条回喂。", map[string]any{
			"type": "object", "properties": map[string]any{},
		}),
		Run: func(context.Context, string) Observation {
			gaps := unifiedGapReport(u.p, u.d)
			// 行为闸终检复核（write_script 已拦过一次，这里防「检索后从未写稿」绕过）。
			if u.sr.used {
				if s, err := LoadScript(u.p); err == nil && len(s.FactSheet) == 0 {
					gaps = append(gaps, "本会话用过 web_search，但盘上文稿 fact_sheet 为空——write_script 补上来源清单")
				}
			}
			if len(gaps) > 0 {
				return Observation{Text: "未放行（逐条补齐后再来）：\n- " + strings.Join(gaps, "\n- ")}
			}
			u.d.emit(u.p.ID, "stage", "prep/unified", "finish：三件过闸咬合")
			return Observation{
				Text: "三件齐备、过闸、相互咬合——准备链收口。等待打包终审（人工，唯一门）。",
				Done: true,
			}
		},
	}
}

// unifiedGapReport 终态咬合检查（finish_prep 与预算耗尽自动终检共用，§7）：
// 三件在盘、各自过闸、相互咬合。返回人话缺口清单（空 = 放行）。
func unifiedGapReport(p *pipeline.Project, d Deps) []string {
	var gaps []string
	script, err := LoadScript(p)
	if err != nil {
		gaps = append(gaps, "文稿（write_script）还没落盘")
	} else if verr := contract.ValidateScriptText(script.Text); verr != nil {
		gaps = append(gaps, "文稿过闸失败："+verr.Error())
	} else if sec := contract.EstimateSpeechDuration(script.Text); sec > contract.MaxDurationSec {
		gaps = append(gaps, fmt.Sprintf("口播时长约 %.0f 秒超上限 %d 秒——压缩后重新 write_script", sec, contract.MaxDurationSec))
	}
	pack, perr := LoadStylePack(p)
	if perr != nil {
		gaps = append(gaps, "风格包（write_stylepack）还没落盘")
	} else if verr := contract.ValidateStylePack(pack); verr != nil {
		gaps = append(gaps, "风格包过闸失败："+verr.Error())
	}
	if stale, why := StyleStale(p); stale {
		gaps = append(gaps, why)
	}
	draft, derr := LoadDraft(p)
	if derr != nil {
		gaps = append(gaps, "分镜（write_storyboard）还没落盘")
	} else if script != nil {
		if verr := contract.ValidateStoryboardDraft(draft, script, pack); verr != nil {
			gaps = append(gaps, "分镜与当前盘上文稿/风格包不咬合："+verr.Error())
		} else if serr := contract.ValidateDraftShots(draft); serr != nil {
			gaps = append(gaps, "分镜镜头表："+serr.Error())
		} else {
			req, all := imageFileSets(p)
			if ierr := contract.ValidateShotImages(draft, req, all); ierr != nil {
				gaps = append(gaps, "素材落地："+ierr.Error())
			}
		}
	}
	return gaps
}
