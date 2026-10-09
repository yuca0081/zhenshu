// 工具定义与包级工具表（自 agent.go 纯移动，无逻辑改动）：门禁按状态机求值，
// 执行体绑当前项目。schema/门禁/执行体三件事都在这，agent.go 只剩循环与编排。
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	openai "github.com/sashabaranov/go-openai"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/pipeline"
	"webpage-video-agent/server/internal/stylelib"
	"webpage-video-agent/server/internal/styleref"
	"webpage-video-agent/server/internal/webfetch"
)

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
		run: func(a *Agent, p *pipeline.Project, args string) string {
			var in struct {
				Content string `json:"content"`
			}
			_ = json.Unmarshal([]byte(args), &in)
			content := strings.TrimSpace(in.Content)
			if content == "" {
				return "缺少文稿正文"
			}
			if err := os.WriteFile(p.Artifact("input/article.txt"), []byte(content), 0o644); err != nil {
				return "保存失败：" + err.Error()
			}
			// 与文稿弹窗保存同语义：article.txt 是唯一真源；manuscript.json 是重放产物，
			// 不删的话重新制作时 TTS 拿的还是旧稿。
			_ = os.Remove(p.Artifact("manuscripts/manuscript.json"))
			_, hasSb := os.Stat(p.Artifact("storyboards/storyboard.json"))
			a.setDoneStatus(p.ID, "manuscript")
			msg := fmt.Sprintf("文稿已保存（%d 字）", len([]rune(content)))
			if hasSb == nil {
				msg += "；已有分镜基于旧稿，要重新出分镜才会生效"
			}
			return msg
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
				// 带要求（首次或重生成一律走这条）：用户明确的方向优先于任何选定风格，
				// 直接按要求现写——样张与风格文件（skin）同产（§12.2）
				return a.regenerateStyle(p, in.Instruction, out)
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
				if err := pipeline.Run(context.Background(), p, "style_samples"); err != nil {
					return "样张生成失败：" + err.Error()
				}
				var ss contract.StyleSamples
				dir, n := "", 0
				if b, rerr := os.ReadFile(out); rerr == nil && json.Unmarshal(b, &ss) == nil {
					dir, n = ss.Direction, len(ss.Samples)
				}
				if a.OnEvent != nil {
					a.OnEvent(p.ID, "style_draft", dir)
				}
				return fmt.Sprintf("风格样张已出（强参考「%s」：标签气质+特色逐条吸收）：%s（%d 张，舞台可预览）。硬门——用户在舞台点头确认后才能开工", st.Name, dir, n)
			}
			if err := pipeline.Run(context.Background(), p, "style_samples"); err != nil {
				return "样张生成失败：" + err.Error()
			}
			var ss contract.StyleSamples
			dir, n := "", 0
			if b, rerr := os.ReadFile(out); rerr == nil && json.Unmarshal(b, &ss) == nil {
				dir, n = ss.Direction, len(ss.Samples)
			}
			if a.OnEvent != nil {
				a.OnEvent(p.ID, "style_draft", dir)
			}
			return fmt.Sprintf("风格样张已出（自由发挥）：%s（%d 张，舞台可预览）。硬门——用户在舞台点头确认后才能开工", dir, n)
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
		run: func(a *Agent, p *pipeline.Project, _ string) string {
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
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			d, terr := styleref.DraftFromDesign(ctx, prov, a.Lib, a.State(p.ID).Name, digest)
			if terr != nil {
				return "提炼失败：" + terr.Error()
			}
			pack, err := a.Lib.DraftFromProject(p.Dir, p.ID, stylelib.ProjectDraftIn{
				Name: d.Name, Subject: d.Subject, Tags: d.Tags,
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
		def:  toolDef("read_project", "查看项目当前状态与产物", nil),
		gate: noGate,
		run:  func(a *Agent, p *pipeline.Project, _ string) string { return a.State(p.ID).summary() },
	},
	{
		def: toolDef("search_styles", "检索风格库（v3：标签+特色清单的可复用风格）。新建项目选风格、用户问「有什么风格」、或用户描述的气质可能命中库内条目时先查这里；命中后引导用户在风格门/风格库里选它（报 id）", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "气质/风格关键词（如 粘土定格 故障风 蒸汽波 等距）"},
			},
			"required": []string{"query"},
		}),
		gate: noGate,
		run: func(a *Agent, p *pipeline.Project, args string) string {
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
			q := strings.ToLower(strings.TrimSpace(args))
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
				if q != "" {
					full, _ := a.Lib.List()
					if len(full) == 0 {
						return "风格库为空：出片后我会自动起草风格，或在风格库页上传参考视频解析入库"
					}
					return "没有命中「" + q + "」的风格。库里现有 " + fmt.Sprintf("%d", len(full)) + " 条，重调一次可放宽或换关键词"
				}
				return "风格库为空：出片后我会自动起草风格，或在风格库页上传参考视频解析入库"
			}
			return fmt.Sprintf("风格库 %d 条：\n", len(lines)) + strings.Join(lines, "\n") +
				"\n选用：风格门弹窗点卡片（可关掉不想吸收的特色），或让我把 id 记入 project（style_id）"
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
		gate: noGate,
		run:  func(_ *Agent, _ *pipeline.Project, args string) string { return runWebSearch(args) },
	},
	{
		def: toolDef("fetch_url", "读取一个网页的正文（纯文本，免费不占搜索额度）。用户贴了链接想让你看内容、或 web_search 结果里有要细看的条目时用它。只抓静态页面（文章/文档/博客/wiki），不执行 JS，重前端页面会读不到正文", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{"type": "string", "description": "要读取的网页地址（http/https）"},
			},
			"required": []string{"url"},
		}),
		gate: noGate,
		run:  func(_ *Agent, _ *pipeline.Project, args string) string { return runFetchURL(args) },
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
				fmt.Fprintf(&b, "段%d %s「%s」（约%.0fs）旁白：%s\n", seg.Idx, seg.ID, seg.Key, seg.DurationHint, pipeline.FirstLine(seg.Narration, 60))
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
