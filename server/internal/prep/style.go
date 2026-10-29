package prep

// style.go —— ②风格工位（prep-agents-design §3.2，多模态模型）：
// 为文字稿选定视觉风格，产出可直接指导制作的风格包。对风格库拥有完整探索能力：
// 检索、分面、图库、使用数据、衍生登记（单次 run 限 1 个；自动带 derived_from
// 出处 + 「未验证」标签，用它产出的片出片后才转正；不开放已有条目编辑/删除）。
// 样张 render_sample 走现有渲染壳（ai/snap_samples.py），同参数哈希缓存。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/pipeline"
	"webpage-video-agent/server/internal/react"
	"webpage-video-agent/server/internal/stylelib"
)

// styleBudget ②的回合预算（§5.2 基线 ②≤12；用户拍板放宽到 30——样张渲染
// 一张一轮，探索+衍生+3 张样张+交稿的链不宽裕）。预算不进提示词。
const styleBudget = 30

// styleSampleMax 样张上限（与 contract.StyleSamples 契约一致）：4–6 张是甜点区，
// 6 是硬顶——探索要多张，但每张都是完整的「写→机检→渲 mp4→判卷→修复」链，
// 张数乘的是整条链的成本。
const styleSampleMax = 6

// styleGrabWindow 抢收窗口（2026-10-23 中-2）：工位预算到期前这么多分钟起，
// render_sample 拒起新活并回喂「立即 submit_style」——带伤提交优先于撞线全烧。
// 一张样张带修复 ~7 分钟（真机实测），窗口必须盖过一次收尾提交的回合开销。
const styleGrabWindow = 4 * time.Minute

// styleSystem 系统提示词（委托书原则 + 2026-10-28 边界化）：只装身份/任务、
// 跨工具流程与政策；单工具机制住各自工具 description——真迹细读画像在
// get_style_detail、判卷修复内循环与 vend/字体声明在 render_sample、登记治理在
// register_style、四件字段契约在 submit_style 的 schema。
const styleSystem = `你是美术指导。任务：为口播稿选定视觉风格，产出可直接指导制作的风格包。流程：get_script 拿口播稿；search_styles 按情绪与题材定分面、有筛选地浏览，get_style_detail 细看候选条目；render_sample 渲染动态样张逐张验证落地效果；完成即调用 submit_style 一次交齐四件——风格描述（视觉语言、色彩、质感、构图倾向，附选择理由，分镜工位靠它理解语境）、剪辑语法（切分密度 / 段长带 / 同舞台延续）、设计系统（皮肤、字体角色、动效语言、贯穿装置、版式、负面清单、标准入场/收尾动画模板）、样张 id 列表。
规则：只在库内风格（含本次衍生登记）中选择，库中没有合适的就从最接近的条目衍生登记（register_style）；口播稿文本一个字不改。`

// runStyleStation 跑②：成功后工件 B 落盘（stylepack.json + legacy style_samples.json 未确认态）。
func runStyleStation(ctx context.Context, p *pipeline.Project, d Deps) error {
	script, err := LoadScript(p)
	if err != nil {
		return fmt.Errorf("风格工位依赖文字稿: %w", err)
	}
	r := &styleRun{p: p, d: d, script: script}
	// 抢收线从工位预算 ctx 的 deadline 推（无 deadline=无抢收，直测兼容）
	if dl, ok := ctx.Deadline(); ok {
		r.grabAt = dl.Add(-styleGrabWindow)
	}
	// 用户选定条目三层恢复：简报在 → 直接用（applyStyle 落的硬要求）；简报被反馈
	// 重跑作废但 project.json style_id 还在 → 重建简报（选型是项目级决定，不随
	// 工件作废）；都没有 → ②自主探索。chosen 是判卷对照物的起点。
	if brief := pipeline.LoadStyleBrief(p); brief != nil {
		r.chosen = brief.EntryID
	} else if sid := projectStyleID(p); sid != "" && d.Lib != nil {
		if _, ok := d.Lib.Get(sid); ok {
			if b, berr := d.Lib.BuildBrief(sid, nil); berr == nil {
				if werr := stylelib.WriteBriefFile(p.Artifact("style/style_brief.json"), b); werr == nil {
					r.chosen = sid
					p.Manifest("prep.style_brief", "选定简报重建 "+sid)
				}
			}
		}
	}
	log := openSessionLog(p, StationStyle)
	defer log.close()
	loop := &Loop{
		Prov:      r.prov(),
		System:    styleSystem,
		Kickoff:   styleKickoff(p),
		Submit:    "submit_style",
		MaxRounds: styleBudget,
		OnStep:    manifestStep(p, StationStyle, log),
	}
	// Tools 后挂：render_sample 的验证内循环要把 token 用量记回本工位任务卡
	//（runTracked 读 loop.Usage）——工具闭包得拿得到 loop。
	loop.Tools = r.tools(loop)
	d.emit(p.ID, "stage", "prep/style", "running")
	if err := runTracked(ctx, p, "prep.style", "准备②风格", loop); err != nil {
		p.Manifest("prep.style_error", pipeline.FirstLine(err.Error(), 200))
		return fmt.Errorf("风格工位: %w", err)
	}
	p.Manifest("prep.style_saved", "工件 B 落盘")
	return nil
}

func styleKickoff(p *pipeline.Project) string {
	cv := pipeline.ProjectCanvas(p)
	k := fmt.Sprintf("开工。本片画幅 %s（%d×%d）——样张与版式从第一笔起就按这个画幅设计。用 get_script 读取口播稿，然后开始探索风格库。",
		cv.Aspect, int(cv.W), int(cv.H))
	if brief := pipeline.LoadStyleBrief(p); brief != nil {
		name := brief.EntryName
		if name == "" {
			name = brief.EntryID
		}
		k += fmt.Sprintf("\n用户已在风格库为本片选定条目「%s」（%s）——这是硬要求：get_style_detail 先看它（真迹细读画像随观察给出，配色/质感/密度/字体从画像里来），风格包与样张以它为准；确有更强理由换选别的条目时必须先说明理由再看新条目。", name, brief.EntryID)
	}
	if idea := noteFile(p, IdeaFile); idea != "" {
		k += "\n用户的想法（导演意图参考——风格气质可以从中取材，与口播稿冲突时以稿为准）：" + idea
	}
	if notes := noteFile(p, "input/style_notes.txt"); notes != "" {
		k += "\n用户对本片风格的要求（优先满足）：" + notes
	}
	return k
}

// styleDraft ②进行中的样张草稿（style/prep-draft.json）：render_sample 逐张积累，
// submit_style 时并进正式工件。与正式 style_samples.json 分离——后者必须一次成文
// （设计系统+样张+未确认态），中途半成品不进正式产物。
type styleDraft struct {
	Samples []contract.Sample `json:"samples"`
}

type styleRun struct {
	p      *pipeline.Project
	d      Deps
	script *contract.Script
	// chosen 当前对照条目（真迹来源）：初值=用户选定简报的条目，随 get_style_detail
	// 跟踪；render_sample 的判卷/修复对照它的真迹帧。
	chosen string
	// refCache chosen 条目的真迹附图（dataURL，懒加载一次复用——压缩不便宜且
	// 每张样张的判卷/修复轮都要同一套）。
	refCache []string
	// registered 本次 run 衍生登记的条目 id（空=未登记；单次限 1 个）。
	registered string
	// lastRepairSig 上一轮修复前的问题签名（样张刹车判据；每张样张渲染完清零）。
	lastRepairSig string
	// grabAt 抢收线（2026-10-23 中-2）：工位预算 deadline 前 styleGrabWindow 的
	// 时刻点。过后 render_sample 拒起新活——逼模型立即 submit_style 带伤交付，
	// 优先于撞预算线全烧（三次真机两轮超时的直接对策）。
	grabAt time.Time
}

// refDetailMax get_style_detail 随观察附的真迹帧数：探索期一条 3 张，看几条
// 条目后历史里就攒出一批对照物，再多每回合重发成本滚起来。
const refDetailMax = 3

// refJudgeMax 判卷/修复随附的真迹帧数：单发调用（不进多轮历史），对照物给足。
const refJudgeMax = 4

// qEntryFrames / qSampleFrames 视觉副驾细读的固定问法（2026-10-28 换脑）。同图同问
// = 同请求体字节：get_style_detail、render_sample、修复轮共用同一份，磁盘缓存跨
// 调用点零成本复用——改措辞就是废缓存，动前先想清楚。
const qEntryFrames = "以下是被选风格条目的原片真迹帧——逐图细读四维画像：配色（底色/强调色/文字色，给近似色值）、质感（颗粒/描边/纹理）、画面密度与层次、字体气质。"
const qSampleFrames = "以下是同一条 1.8 秒动态样张的首/中/尾三帧——逐帧细读：构图与主体位置、全部可见文字清单、动效阶段（入场/揭示/定格）。"

// refImages chosen 条目的真迹附图（懒加载缓存）。条目无媒体/库未接线返回 nil，
// 判卷降级为不对照真迹（与旧行为一致）。
func (r *styleRun) refImages(maxN int) []string {
	if r.chosen == "" || r.d.Lib == nil {
		return nil
	}
	if r.refCache == nil {
		brief, err := r.d.Lib.BuildBrief(r.chosen, nil)
		if err != nil || len(brief.Evidence) == 0 {
			r.refCache = []string{} // 空 slice=已加载但无货，防反复重查
		} else {
			for _, src := range brief.Evidence {
				if u, err := shrinkRef(src, 640); err == nil {
					r.refCache = append(r.refCache, u)
				}
			}
			if r.refCache == nil {
				r.refCache = []string{}
			}
		}
	}
	if len(r.refCache) == 0 || maxN >= len(r.refCache) {
		return r.refCache
	}
	return r.refCache[:maxN]
}

func (r *styleRun) prov() react.ChatClient {
	// ②主脑走文本主力档（2026-10-28 换脑 glm-5.3，无多模态）：看图活全走视觉
	// 副驾（llm.VisionDigest，flash 档细读成文字画像），判卷/修复对照物另起
	// RoleVisual 单发调用——脑与眼分档，各吃各的模型。
	prov, _ := llm.FromEnv(llm.StationPrepStyleText)
	return prov
}

// projectStyleID project.json 的 style_id（用户在界面/上一轮 ② 选定的条目）。
func projectStyleID(p *pipeline.Project) string {
	b, err := os.ReadFile(p.Artifact("project.json"))
	if err != nil {
		return ""
	}
	var meta struct {
		StyleID string `json:"style_id"`
	}
	if json.Unmarshal(b, &meta) != nil {
		return ""
	}
	return strings.TrimSpace(meta.StyleID)
}

func (r *styleRun) tools(loop *Loop) []Tool {
	snapLeft := styleSampleMax
	return []Tool{
		r.getScriptTool(),
		r.searchStylesTool(),
		r.getStyleDetailTool(loop),
		r.getStyleUsageTool(),
		r.renderSampleTool(&snapLeft, loop),
		r.registerStyleTool(),
		r.submitStyleTool(),
	}
}

// getScriptTool get_script：冻结文字稿（句编号）。
func (r *styleRun) getScriptTool() Tool {
	return Tool{
		Def: toolSchema("get_script", "读取冻结文字稿（句编号 N1..Nn 与全文）。只读，一个字不改。", map[string]any{
			"type": "object", "properties": map[string]any{},
		}),
		Run: func(context.Context, string) Observation {
			return Observation{Text: formatScriptForPeers(r.script)}
		},
	}
}

func formatScriptForPeers(s *contract.Script) string {
	var b strings.Builder
	fmt.Fprintf(&b, "文字稿（%d 句 / %d 字，目标 %.0f 秒）：\n", len(s.Sentences), s.WordCount, s.TargetSeconds)
	for _, sen := range s.Sentences {
		b.WriteString(sen.ID + " " + sen.Text + "\n")
	}
	if len(s.FactSheet) > 0 {
		fmt.Fprintf(&b, "\nfact_sheet %d 条（硬事实已溯源）。\n", len(s.FactSheet))
	}
	return b.String()
}

// searchStylesTool search_styles：库的主入口；空 query = 全量分页。
func (r *styleRun) searchStylesTool() Tool {
	return Tool{
		Def: toolSchema("search_styles", "检索风格库（主入口）。空 query = 全量分页；分面给情绪/题材（关键词）/色调/密度；按分面有筛选地浏览，不必遍历全库。看中的条目用 get_style_detail 细看。", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":   map[string]any{"type": "string", "description": "关键词（风格名/题材/气质词；可空=全量）"},
				"mood":    map[string]any{"type": "string", "description": "情绪分面（如 沉稳/明快/悬疑）"},
				"tone":    map[string]any{"type": "string", "description": "色调分面（如 黑金/低饱和暖色/冷蓝）"},
				"density": map[string]any{"type": "string", "description": "密度分面（如 疏朗/中密/高密）"},
				"page":    map[string]any{"type": "integer", "description": "页码，从 1 起（默认 1，每页 10 条）"},
			},
		}),
		Run: func(_ context.Context, args string) Observation {
			var in struct {
				Query   string `json:"query"`
				Mood    string `json:"mood"`
				Tone    string `json:"tone"`
				Density string `json:"density"`
				Page    int    `json:"page"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return Observation{Text: err.Error()}
			}
			if r.d.Lib == nil {
				return Observation{Text: "风格库未接线。"}
			}
			items, total, err := r.d.Lib.Search(in.Query, stylelib.StyleFacets{
				Mood: in.Mood, Tone: in.Tone, Density: in.Density,
			}, in.Page, 10)
			if err != nil {
				return Observation{Text: "风格库读取失败：" + err.Error()}
			}
			if len(items) == 0 {
				if in.Query == "" && in.Mood == "" && in.Tone == "" && in.Density == "" {
					return Observation{Text: "风格库是空的（共 0 条）。用 register_style 从最接近的方向衍生登记，或完全自定义。"}
				}
				return Observation{Text: r.facetMissHint(in.Query, in.Mood, in.Tone, in.Density)}
			}
			vocab := r.d.Lib.VocabList()
			tagName := func(id string) string {
				for _, e := range vocab {
					if e.ID == id {
						return e.Name
					}
				}
				return id
			}
			var b strings.Builder
			fmt.Fprintf(&b, "命中 %d / 共 %d 条（第 %d 页）：\n", len(items), total, max(in.Page, 1))
			for _, st := range items {
				line := fmt.Sprintf("- %s「%s」", st.ID, st.Name)
				if st.Tags.Main != "" {
					line += " 标签:" + tagName(st.Tags.Main)
					for _, aux := range st.Tags.Aux {
						line += "+" + tagName(aux)
					}
				}
				if st.Facets.Mood != "" || st.Facets.Tone != "" || st.Facets.Density != "" {
					line += fmt.Sprintf(" 分面[情绪:%s 色调:%s 密度:%s]", st.Facets.Mood, st.Facets.Tone, st.Facets.Density)
				}
				if st.Subject != "" {
					line += " 题材:" + contract.FirstRunes(st.Subject, 40)
				}
				if st.Usage.Count > 0 {
					line += fmt.Sprintf(" 使用%d次", st.Usage.Count)
					if st.Usage.LastScore > 0 {
						line += fmt.Sprintf("/最近审核 %.1f 分", st.Usage.LastScore)
					}
				}
				if st.Unverified {
					line += " ⚠未验证（衍生自 " + st.DerivedFrom + "）"
				} else if st.Published {
					line += " 已入库"
				} else {
					line += " 草稿"
				}
				b.WriteString(line + "\n")
			}
			return Observation{Text: b.String()}
		},
	}
}

// facetMissHint 分面/关键词 0 命中 ≠ 库空。Search 返回的 total 是过滤后计数，
// 直接打「库内共 0 条」会把模型骗去自造风格（2026-10-15 实测 P3）——这里报真实
// 库容，并把库内现有取值列出来，模型下一轮就能换对标签直接命中。
func (r *styleRun) facetMissHint(query, mood, tone, density string) string {
	libTotal, moods, tones, densities := 0, map[string]bool{}, map[string]bool{}, map[string]bool{}
	if all, err := r.d.Lib.List(); err == nil {
		libTotal = len(all)
		for _, st := range all {
			if st.Facets.Mood != "" {
				moods[st.Facets.Mood] = true
			}
			if st.Facets.Tone != "" {
				tones[st.Facets.Tone] = true
			}
			if st.Facets.Density != "" {
				densities[st.Facets.Density] = true
			}
		}
	}
	joinVals := func(set map[string]bool) string {
		if len(set) == 0 {
			return "（库里这个分面暂无取值）"
		}
		vals := make([]string, 0, len(set))
		for v := range set {
			vals = append(vals, v)
		}
		return strings.Join(vals, "、")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "没有命中（库内共 %d 条——库不是空的，是筛选没对上）。", libTotal)
	if query != "" {
		fmt.Fprintf(&b, "关键词「%s」无匹配；", query)
	}
	if mood != "" {
		fmt.Fprintf(&b, "情绪=%s 库内没有（现有取值：%s）；", mood, joinVals(moods))
	}
	if tone != "" {
		fmt.Fprintf(&b, "色调=%s 库内没有（现有取值：%s）；", tone, joinVals(tones))
	}
	if density != "" {
		fmt.Fprintf(&b, "密度=%s 库内没有（现有取值：%s）；", density, joinVals(densities))
	}
	b.WriteString("换用以上取值，或去掉部分筛选再看全量。")
	return b.String()
}

// getStyleDetailTool get_style_detail：完整描述 + 特色 + 样张图库（帧清单）。
func (r *styleRun) getStyleDetailTool(loop *Loop) Tool {
	return Tool{
		Def: toolSchema("get_style_detail", "看一条风格的完整档案：标签与特色机制、分面、题材、样张图库清单、衍生出处与验证状态。该条目的原片真迹会由视觉副驾细读成文字画像随观察给你——配色、质感、画面密度、字体气质一律以画像为准，禁止只凭条目文字想象风格。", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "风格条目 id（search_styles 结果里拿）"},
			},
			"required": []string{"id"},
		}),
		Run: func(ctx context.Context, args string) Observation {
			var in struct {
				ID string `json:"id"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return Observation{Text: err.Error()}
			}
			st, ok := r.d.Lib.Get(strings.TrimSpace(in.ID))
			if !ok {
				return Observation{Text: "风格 " + in.ID + " 不存在"}
			}
			r.chosen = st.ID // 判卷对照物随查看跟踪：最后细看的条目就是候选选型
			r.refCache = nil
			vocab := r.d.Lib.VocabList()
			tagName := func(id string) string {
				for _, e := range vocab {
					if e.ID == id {
						return e.Name
					}
				}
				return id
			}
			var b strings.Builder
			fmt.Fprintf(&b, "风格 %s「%s」\n", st.ID, st.Name)
			if st.Subject != "" {
				fmt.Fprintf(&b, "题材（只供理解，不进画面）：%s\n", st.Subject)
			}
			fmt.Fprintf(&b, "标签：%s", tagName(st.Tags.Main))
			for _, aux := range st.Tags.Aux {
				b.WriteString("+" + tagName(aux))
			}
			b.WriteString("\n")
			if st.Facets.Mood != "" || st.Facets.Tone != "" || st.Facets.Density != "" {
				fmt.Fprintf(&b, "分面：情绪 %s / 色调 %s / 密度 %s\n", st.Facets.Mood, st.Facets.Tone, st.Facets.Density)
			}
			if len(st.Signatures) > 0 {
				b.WriteString("特色机制（吸收机制而非临摹物件）：\n")
				for _, sg := range st.Signatures {
					line := fmt.Sprintf("- [%s] %s——%s", sg.Kind, sg.Name, sg.Mechanism)
					if strings.TrimSpace(sg.UseHint) != "" {
						line += "（适用：" + sg.UseHint + "）"
					}
					b.WriteString(line + "\n")
				}
			}
			// 真迹细读（视觉副驾，2026-10-28 大杯换脑）：主脑纯文本，真迹帧由 flash
			// 细读成文字画像回喂——风格的本体在帧里不在文字里，纯文本档案喂不出贴合的
			// 配色/质感/字体（2026-10-22 实测根因），画像保住这条命脉。副驾不可用
			// 降级为计数提示：判卷链仍对照真图，闭环没断。
			var obs Observation
			obs.Text = b.String()
			if imgs := r.refImages(refDetailMax); len(imgs) > 0 {
				if d, u := llm.VisionDigest(ctx, imgs, qEntryFrames); d != "" {
					obs.Text += fmt.Sprintf("样张图库：原片真迹 %d 张已由视觉副驾细读（配色/质感/密度/字体气质以画像为准；画里的物件是原片题材，不进本片）：\n%s\n", len(imgs), d)
					llm.AccumulateUsage(&loop.Usage, u)
					loop.Calls++
				} else {
					obs.Text += fmt.Sprintf("样张图库：原片真迹 %d 张（视觉副驾暂不可用，画像缺省——凭文字档案从严执行；判卷仍会对照真图）\n", len(imgs))
				}
			} else {
				frames, hasVideo := r.d.Lib.MediaNames(st.ID)
				switch {
				case hasVideo && len(frames) > 0:
					obs.Text += fmt.Sprintf("样张图库：%d 张抽帧 + 原片（历史实拍；附图压缩失败，凭文字档案从严执行）\n", len(frames))
				case len(frames) > 0:
					obs.Text += fmt.Sprintf("样张图库：%d 张抽帧（历史实拍；附图压缩失败，凭文字档案从严执行）\n", len(frames))
				default:
					obs.Text += "样张图库：暂无（无历史实拍）\n"
				}
			}
			if st.Unverified {
				obs.Text += fmt.Sprintf("治理状态：未验证（衍生自 %s）——用它产出的片出片后才转正\n", st.DerivedFrom)
			}
			if st.Usage.Count > 0 {
				obs.Text += fmt.Sprintf("使用记录：%d 次，其中通过 %d 次", st.Usage.Count, st.Usage.Passes)
				if st.Usage.LastScore > 0 {
					obs.Text += fmt.Sprintf("，最近审核 %.1f/5 分", st.Usage.LastScore)
				}
				obs.Text += "\n"
			}
			return obs
		},
	}
}

// getStyleUsageTool get_style_usage：使用次数 + 历史成片的审核记账（数据驱动选型）。
func (r *styleRun) getStyleUsageTool() Tool {
	return Tool{
		Def: toolSchema("get_style_usage", "查一条风格的历史使用数据：被选用次数、产出成片数、最近审核分。选型时数据说话。", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "风格条目 id"},
			},
			"required": []string{"id"},
		}),
		Run: func(_ context.Context, args string) Observation {
			var in struct {
				ID string `json:"id"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return Observation{Text: err.Error()}
			}
			st, ok := r.d.Lib.Get(strings.TrimSpace(in.ID))
			if !ok {
				return Observation{Text: "风格 " + in.ID + " 不存在"}
			}
			if st.Usage.Count == 0 {
				return Observation{Text: fmt.Sprintf("「%s」还没有被任何项目选用过——没有历史账，首用即开源。", st.Name)}
			}
			return Observation{Text: fmt.Sprintf("「%s」：选用 %d 次 / 成片 %d 次 / 最近审核 %.1f/5（%s）",
				st.Name, st.Usage.Count, st.Usage.Passes, st.Usage.LastScore,
				st.Usage.LastUsedAt.Format("2006-01-02"))}
		},
	}
}

// 样张 HTML 字符预算：12000 目标给像素精灵点阵留余地（一个 32×32 精灵 ≈1-2KB
// 的字符串编码），30000 硬顶防跑飞。旧值 2000 实测只能画出彩色方块摆一摆。
// sampleDur 动态样张时长：1–2 秒区间取 1.8——够走完一次完整揭示节拍，渲染压在
// 分钟级（hyperframes 软光栅）。
const (
	sampleHTMLTarget = 12000
	sampleHTMLMax    = 30000
	sampleRepairMax  = 2 // 样张内循环修复轮上限（机械检查与判卷共用预算）；轮数已封顶，不再另设调用保险丝
	sampleDur        = 1.8
)

// vendSpecRe 样张 HTML 里的 vend 声明（lib@精确版本；与 scene_lint A3 同词汇）。
var vendSpecRe = regexp.MustCompile(`(?i)<!--\s*vend:\s*([A-Za-z0-9_.-]+@[A-Za-z0-9_.-]+)\s*-->`)

// vendSpecs 提取去重保序的采购申请单。
func vendSpecs(html string) []string {
	seen, out := map[string]bool{}, []string{}
	for _, m := range vendSpecRe.FindAllStringSubmatch(html, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// sampleVendLibs 汇总草稿样张里声明的 vend 库（去重排序）：submit 时进设计系统
// （contract.StyleSamples.VendLibs），正式段 sceneGen 按货架优先复现样张效果。
func sampleVendLibs(samples []contract.Sample) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range samples {
		for _, spec := range vendSpecs(s.HTML) {
			if !seen[spec] {
				seen[spec] = true
				out = append(out, spec)
			}
		}
	}
	sort.Strings(out)
	return out
}

// vendMu 采购串行化：vendor manifest 写与 tarball 落盘防竞态（与 scenegen 同口径）。
var vendMu sync.Mutex

// vendEnsure 平台代为采购（模型只有申请权）：npm 钉版+许可证白名单+哈希+台账，
// 与正式段 sceneLintWithVend 同一工具同一闸。失败原样上抛——调用方把「换已登记库/
// 纯 CSS 实现」的选择权交还模型。
func (r *styleRun) vendEnsure(ctx context.Context, specs []string) error {
	for _, spec := range specs {
		vendMu.Lock()
		cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		cmd := pipeline.CommandContext(cctx, pipeline.PythonExe(),
			filepath.Join(r.d.RootDir, "ai", "vendor_tool.py"), "ensure", spec, "--allow-pending-replay")
		cmd.Dir = r.d.RootDir
		out, err := cmd.CombinedOutput()
		cancel()
		vendMu.Unlock()
		if err != nil {
			if cctx.Err() != nil {
				return cctx.Err()
			}
			return fmt.Errorf("%s: %s", spec, pipeline.FirstLine(string(out), 140))
		}
		r.p.Manifest("prep.vend", "样张自采入库 "+spec)
	}
	return nil
}

// vendInline 渲染副本的就地内联（声明注释 → <script> 库源码）：样张页自包含，
// 渲染器无需工程 assets 目录。draft 里保留的是带声明的模型版（正式段复现的凭据）。
func (r *styleRun) vendInline(ctx context.Context, relPath string) error {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := pipeline.CommandContext(cctx, pipeline.PythonExe(),
		filepath.Join(r.d.RootDir, "ai", "vendor_tool.py"), "inline", r.p.Artifact(relPath))
	cmd.Dir = r.d.RootDir
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		if cctx.Err() != nil {
			return cctx.Err()
		}
		return fmt.Errorf("%s", pipeline.FirstLine(buf.String(), 160))
	}
	return nil
}

// ensureSampleShell 渲染工程底座（与 assemble_samples.py 同款，prep 阶段先于正式
// assemble 跑得自备）：assets/gsap.min.js+hf.js、package.json、index.html（渲染器
// 从它提取 render shell，缺它整批渲染必炸）。
func (r *styleRun) ensureSampleShell() {
	assets := r.p.Artifact("assets")
	_ = os.MkdirAll(assets, 0o755)
	for _, f := range []string{"gsap.min.js", "hf.js"} {
		dst := filepath.Join(assets, f)
		if _, err := os.Stat(dst); err == nil {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(r.d.RootDir, "ai", "assets", f)); err == nil {
			_ = os.WriteFile(dst, b, 0o644)
		}
	}
	if _, err := os.Stat(r.p.Artifact("package.json")); err != nil {
		_ = os.WriteFile(r.p.Artifact("package.json"), []byte(fmt.Sprintf(
			`{"name":%q,"private":true,"type":"module"}`, r.p.ID)), 0o644)
	}
}

// ensureSampleFonts 首渲前备字体：样张阶段还没有设计系统（LockDesign 的申请书
// 不存在），按样张 HTML 的目录匹配 + 自装声明（<!-- font: Family | 协议 | URL -->）
// 现场写申请书 + font_tool ensure（下载/子集化走机器级缓存，首项目冷启动慢、
// 后续项目快）。不备的代价 = 字体静默回退雅黑渲染进 mp4，判卷的字体气质意见
// 全部失真（2026-10-22 真机实测复犯）。
// 早退条件是「需求全部已 resolved」而非「产物文件存在」——后渲样张新增字体
// 照样补装（旧实现的文件存在即跳过会让后加的字体永远装不上）。
// 失败语义两档：目录字体失败=静默降级记账、不挡样张（既有语义）；自装字体
// 失败/声明格式错=返回清单，由 render_sample 回喂模型当场换（vend 采购同款）。
func (r *styleRun) ensureSampleFonts(ctx context.Context, htmls []string) (failedCustoms, badDecls []string) {
	matched := pipeline.MatchSampleFonts(htmls)
	var customs []contract.CustomFont
	for _, h := range htmls {
		cs, bad := pipeline.CustomFontDecls(h)
		customs = append(customs, cs...)
		badDecls = append(badDecls, bad...)
	}
	// 申请书写全集（本项目的字体 BOM，锁提交/确认时 LockDesign 重写也不丢自装
	// 条目）；font_tool 只在有缺口时才跑（已 resolved 的家族重跑只是白子集化）
	resolved, _ := pipeline.LoadFontAssetsDetail(r.p)
	needCatalog := map[string]*pipeline.CatalogFont{}
	for fam, f := range matched {
		if resolved[strings.ToLower(fam)] == "" {
			needCatalog[fam] = f
		}
	}
	var needCustoms []contract.CustomFont
	for _, c := range customs {
		if resolved[strings.ToLower(c.Family)] == "" {
			needCustoms = append(needCustoms, c)
		}
	}
	if err := pipeline.WriteFontAssetRequest(r.p, matched, customs); err != nil {
		r.p.Manifest("prep.style_font_degrade", "申请书落盘失败: "+pipeline.FirstLine(err.Error(), 160))
		return nil, badDecls
	}
	if len(needCatalog) == 0 && len(needCustoms) == 0 {
		return nil, badDecls
	}
	// 冷启动连下多款大字库时 6 分钟偏紧（重试×3 在内），放宽到 8 分钟
	if err := r.runAIScript(ctx, 8*time.Minute, "font_tool.py", "ensure", r.p.Dir); err != nil {
		r.p.Manifest("prep.style_font_degrade", pipeline.FirstLine(err.Error(), 160))
	}
	// 成败以 font_assets.json 实际 resolved 为准（font_tool 剥族降级不算整脚本失败）
	after, _ := pipeline.LoadFontAssetsDetail(r.p)
	var okCatalog []string
	for fam := range matched {
		if after[strings.ToLower(fam)] != "" {
			okCatalog = append(okCatalog, fam)
		}
	}
	sort.Strings(okCatalog)
	if len(okCatalog) > 0 {
		r.p.Manifest("prep.style_fonts", "样张字体已备("+strings.Join(okCatalog, "、")+")")
	}
	for _, c := range needCustoms {
		if after[strings.ToLower(c.Family)] == "" {
			failedCustoms = append(failedCustoms, c.Family)
			continue
		}
		lic := c.License
		if lic == "" {
			lic = "协议未注明"
		}
		r.p.Manifest("prep.style_font_custom", fmt.Sprintf("样张自装字体 %s（%s）← %s", c.Family, lic, c.URL))
	}
	return failedCustoms, badDecls
}

// grabFrame ffmpeg 抽帧：mp4 指定时刻 → 一帧 jpg（判卷三帧/海报共用）。
func (r *styleRun) grabFrame(ctx context.Context, mp4 string, t float64, out string) error {
	ff, err := pipeline.FFmpegPath()
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	cmd := pipeline.CommandContext(cctx, ff, "-y", "-ss", strconv.FormatFloat(t, 'f', 2, 64),
		"-i", r.p.Artifact(mp4), "-frames:v", "1", "-q:v", "3", out)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		if cctx.Err() != nil {
			return cctx.Err()
		}
		return fmt.Errorf("%s", pipeline.FirstLine(buf.String(), 120))
	}
	return nil
}

// renderSampleVideo 动态样张渲染管线：机械整形 → 落渲染页 → vend 内联 →
// hyperframes 软光栅出 mp4 → 抽海报（style/sample-N.png，旧契约位）+ 首/中/尾三帧。
// 返回判卷帧 dataURL。
func (r *styleRun) renderSampleVideo(ctx context.Context, html string, idx int) ([]string, error) {
	page := fmt.Sprintf(".hf-sample/sample-%d.html", idx)
	mp4 := fmt.Sprintf("style/clips/sample-%d.mp4", idx)
	if err := os.MkdirAll(r.p.Artifact(".hf-sample"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(r.p.Artifact("style/clips"), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(r.p.Artifact(page), []byte(pipeline.PrepareSamplePage(html, sampleDur)), 0o644); err != nil {
		return nil, err
	}
	r.ensureSampleShell()
	if len(vendSpecs(html)) > 0 {
		if err := r.vendInline(ctx, page); err != nil {
			return nil, fmt.Errorf("vend 内联失败: %w", err)
		}
	}
	outLog, err := pipeline.RunCLI(r.p, "npx", pipeline.HyperframesPkg, "render", ".",
		"-c", page, "-o", mp4, "--no-browser-gpu")
	if err != nil {
		return nil, fmt.Errorf("%s", pipeline.FirstLine(pipeline.TailLines(outLog, 6), 200))
	}
	// 海报（70% 时长的落定态，旧 png 契约位）+ 判卷三帧（首/中/尾）
	poster := fmt.Sprintf("style/sample-%d.png", idx)
	if err := r.grabFrame(ctx, mp4, sampleDur*0.7, r.p.Artifact(poster)); err != nil {
		return nil, fmt.Errorf("抽海报失败: %w", err)
	}
	frames := []string{}
	for i, t := range []float64{0.05, sampleDur * 0.55, sampleDur - 0.15} {
		f := fmt.Sprintf("style/clips/sample-%d-f%d.jpg", idx, i)
		if err := r.grabFrame(ctx, mp4, t, r.p.Artifact(f)); err != nil {
			return nil, fmt.Errorf("抽判卷帧失败: %w", err)
		}
		if u, err := shrinkFrame(r.p.Artifact(f)); err == nil {
			frames = append(frames, u)
		}
	}
	if len(frames) == 0 {
		return nil, fmt.Errorf("判卷帧全部抽取失败")
	}
	return frames, nil
}

// sampleJudgeSystem 样张判卷官（NoThink 快档；系统提示词全项目固定——同一前缀
// 反复出现是平台前缀缓存最好命中的形态）。
const sampleJudgeSystem = `你是样张质检员。给一条短视频动态样张的首/中/尾三帧判卷：可读性与构图是硬伤；随消息附有原片真迹时，样张对真迹的风格贴合（配色/质感/密度/字体气质）同为准绳。只输出 JSON。`

// renderSampleTool render_sample：动态样张渲染验证管线（1.8 秒 mp4——样张即动效，
// 与成片同一台渲染器同一套工序：场景页 → 机械检查 → vend 采购内联 → hyperframes
// 软光栅 → 抽首/中/尾三帧判卷（对照条目真迹判风格贴合）→ 修复轮 ≤2 同类刹车）。
// 模型只写带 vend 声明的源码（正式段复现的凭据），渲染副本由平台内联库源码。
func (r *styleRun) renderSampleTool(snapLeft *int, loop *Loop) Tool {
	cv := pipeline.ProjectCanvas(r.p) // 画幅跟项目走：横版样张上验竖版版式=无效验证（2026-10-15 实测 P1）
	w, h := int(cv.W), int(cv.H)
	desc := fmt.Sprintf(`渲染一张动态样张（%.1f 秒 mp4，与成片同一台渲染器同一套工序，动效/字体/质感即所得）。scene_brief=场景简述（必须取自口播稿代表性内容，禁止通用示例）；html=%d×%d 自包含动态页面：
- 根元素 <div data-composition-id="main" data-width="%d" data-height="%d" data-start="0" data-duration="%.1f">，%.1f 秒走完一次完整揭示节拍（开场视觉锚点即亮，收尾定格在内容帧不拖尾）
- <head> 里保留 <script src="assets/gsap.min.js"></script> 与 <script src="assets/hf.js"></script>（漏写平台会机械补）
- 动效全写进一条 gsap 时间轴：var tl=gsap.timeline({paused:true}); …; window.__timelines=window.__timelines||{}; window.__timelines["main"]=tl; 可补间只有 opacity/x/y/scale/rotation；禁 CSS animation/transition/@keyframes；禁 rAF/setTimeout/setInterval/Date.now；Math.random 必须种子化
- <canvas> 绘制 = window.__draw_xxx=function(t,ctx,W,H,rng) 纯函数，由时间轴 onUpdate 驱动逐帧重画
- 需要外部库（three.js/物理/图表等）：在 html 里写 <!-- vend: 包名@精确版本 --> 声明，平台自动采购注入（许可证白名单：MIT/Apache/BSD/CC0 等，白名单外拒收）；静态画面能解决的别引大库
- 字体：目录字体（%s）＋微软雅黑/黑体/宋体/楷体＋monospace；目录真没有的（像素体/手写体等）在 html 写 <!-- font: Family | 协议 | 直链URL --> 声明自装（ttf/otf 直链或 zip release 资产；Family 与 font-family 用法逐字一致；下载失败会回喂你换）——字体必须在渲前装好，判卷的字体意见才作数；颜色/字体与本片设计系统皮肤槽位逐字一致；自包含无图片无外链
- 提交后自动过机械检查并渲染判卷（抽首/中/尾三帧对照真迹判风格贴合），只有 high 硬伤（读不出/压叠/空帧/溢出破图/风格明显两路）才打回带诊断修复（≤%d 轮），med/low 毛边记录不立案、别主动返工；构图版式必须与本组其他样张明显不同，整组 4–5 张为宜（上限 %d 张）。目标 ≤%d 字符，上限 %d`,
		sampleDur, w, h, w, h, sampleDur, sampleDur, pipeline.FontCatalogList(), sampleRepairMax, styleSampleMax, sampleHTMLTarget, sampleHTMLMax)
	return Tool{
		Def: toolSchema("render_sample", desc, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"scene_brief": map[string]any{"type": "string", "description": "场景简述（一句话：这张样张画口播稿的哪个代表性内容）"},
				"html":        map[string]any{"type": "string", "description": fmt.Sprintf("样张 HTML（完整自包含动态文档，画布 %d×%d，时长 %.1f 秒）", w, h, sampleDur)},
				"replace":     map[string]any{"type": "integer", "description": "要替换的样张序号（1-based：2=样张B）；0/缺省=新增。同一张被判卷打回后的返工重渲必须带 replace，别新增重复样张"},
			},
			"required": []string{"scene_brief", "html"},
		}),
		Run: func(ctx context.Context, args string) Observation {
			// 抢收门（2026-10-23 中-2）：预算窗口将尽，拒起新样张——渲一张带修复
			// ~7 分钟，撞线时开新活等于整链作废。回喂直接指路 submit_style。
			if !r.grabAt.IsZero() && time.Now().After(r.grabAt) {
				return Observation{Text: fmt.Sprintf("时间预算将尽（剩不足 %s）——不要再渲新样张也不要再修旧的，立即调用 submit_style 提交当前成果：带伤提交优先于超时全烧，缺一张样张是小事，链超时是全烧。", styleGrabWindow)}
			}
			var in struct {
				SceneBrief string `json:"scene_brief"`
				HTML       string `json:"html"`
				Replace    int    `json:"replace"` // 1-based 样张序号；0=新增
			}
			if err := decodeArgs(args, &in); err != nil {
				return Observation{Text: err.Error()}
			}
			if strings.TrimSpace(in.HTML) == "" {
				return Observation{Text: "被拒：html 为空"}
			}
			if len(in.HTML) > sampleHTMLMax {
				return Observation{Text: fmt.Sprintf("被拒：样张 HTML 超过 %d 字符（目标 ≤%d）——拆掉重复样式、用 class 归并", sampleHTMLMax, sampleHTMLTarget)}
			}
			if strings.TrimSpace(in.SceneBrief) == "" {
				return Observation{Text: "被拒：scene_brief 为空——样张场景必须取自口播稿代表性内容"}
			}
			draft := r.loadDraft()
			reworkIdx := -1 // ≥0 = 替换已有槽位（文件名不变，不多占一张）
			if in.Replace > 0 {
				if in.Replace < 1 || in.Replace > len(draft.Samples) {
					return Observation{Text: fmt.Sprintf("被拒：replace=%d 越界（现有 %d 张，可选 1–%d）", in.Replace, len(draft.Samples), len(draft.Samples))}
				}
				reworkIdx = in.Replace - 1
			}
			if reworkIdx < 0 && len(draft.Samples) >= styleSampleMax {
				return Observation{Text: fmt.Sprintf("样张已满 %d 张（契约上限）——要改其中一张就用 replace=序号 替换它（渲染额度照扣、不多占），或直接 submit_style。", styleSampleMax)}
			}
			// 同 HTML 已在草稿 = 连同其动态小样都在，防重复追加
			for _, s := range draft.Samples {
				if s.HTML == in.HTML {
					return Observation{Text: fmt.Sprintf("%s 已在草稿里，无需重复渲染——要改就换 HTML 后带 replace=序号 重新 render_sample，或直接 submit_style。", s.Tag)}
				}
			}
			if *snapLeft <= 0 {
				return Observation{Text: fmt.Sprintf("渲染额度已用完（最多 %d 张），直接 submit_style。", styleSampleMax)}
			}
			*snapLeft--
			idx := len(draft.Samples) // 候选槽位（返工时改写为被替换槽位）
			if reworkIdx >= 0 {
				idx = reworkIdx
			}

			// vend 采购（模型申请，平台代购，机械闸把关）：失败把选择权交还模型
			specs := vendSpecs(in.HTML)
			if len(specs) > 0 {
				if err := r.vendEnsure(ctx, specs); err != nil {
					return Observation{Text: "vend 采购失败：" + err.Error() +
						"。换台账里已登记的库、或去掉该库用纯 CSS/Canvas 实现后重新 render_sample（其余样张不受影响）。"}
				}
			}

			// 首渲前备字体（含本组已有样张的声明，一次备齐）：自装失败/声明格式错
			// 回喂模型当场换（vend 采购同款语义），目录字体失败静默降级不挡样张
			fontHTMLs := []string{in.HTML}
			for _, s0 := range draft.Samples {
				fontHTMLs = append(fontHTMLs, s0.HTML)
			}
			if failed, bad := r.ensureSampleFonts(ctx, fontHTMLs); len(failed) > 0 || len(bad) > 0 {
				var parts []string
				if len(failed) > 0 {
					parts = append(parts, "自装字体下载失败："+strings.Join(failed, "、")+"（来源不可达、超时或不是字体文件——体积上限 20MB/zip 64MB）")
				}
				for _, b := range bad {
					parts = append(parts, "声明格式错（应为 <!-- font: Family | 协议 | 直链URL -->）："+pipeline.FirstLine(b, 100))
				}
				return Observation{Text: "字体自装失败：" + strings.Join(parts, "；") +
					"。换可用直链重试、改用目录字体，或去掉该字体重新 render_sample（其余样张不受影响）。"}
			}

			// 渲染验证内循环：机检 → 渲 mp4 → 抽三帧判卷 → 修复（≤sampleRepairMax 轮，同类刹车）
			html, carried, skipNote, nits := in.HTML, []string(nil), "", []string(nil)
			var judgeFrames []string
			for round := 0; ; round++ {
				fixed, mechIssues := pipeline.CheckSampleHTML(html, pipeline.SampleCanvas{W: w, H: h},
					pipeline.LoadSampleFontAssets(r.p))
				html = fixed
				if !strings.Contains(html, "__timelines") {
					mechIssues = append(mechIssues, `动态样张缺 gsap 时间轴注册（window.__timelines["main"]=tl）——没有时间轴渲出来是静帧`)
				}
				frames, rerr := r.renderSampleVideo(ctx, html, idx)
				if rerr != nil {
					carried, skipNote = mechIssues, "渲染失败："+pipeline.FirstLine(rerr.Error(), 160)
					break
				}
				judgeFrames = frames
				if len(mechIssues) > 0 {
					carried = mechIssues
				} else if verdict, jok := r.judgeSample(ctx, loop, in.SceneBrief, frames, w, h); !jok {
					skipNote = "判卷通道不可用（LLM 调用失败），机检过检即放行"
					break
				} else if verdict.Pass {
					carried, nits = nil, issueLines(verdict.MedLowIssues())
					break
				} else {
					carried = issueLines(verdict.HighIssues()) // 修复轮只喂硬伤；med/low 已在判卷里降档不立案
				}
				if round >= sampleRepairMax {
					break // 轮次耗尽：带问题说明放行
				}
				sig := issueSignature(carried)
				if sig == r.lastRepairSig {
					break // 刹车：同类问题两轮原样重现=原地打转，保留现版不再烧
				}
				r.lastRepairSig = sig
				fixed2, ok := r.repairSample(ctx, loop, in.SceneBrief, html, frames, carried)
				if !ok {
					break // 修复调用失败：带问题说明放行
				}
				html = fixed2
			}
			r.lastRepairSig = ""

			// 落草稿 + 镜像。mp4 与海报（style/sample-N.png）已在渲染流程落盘；
			// draft 保留带 vend 声明的模型源码（正式段复现的凭据）。
			// 返工识别（仅新增路径）：场景简述一字相同，或构图感知哈希近重复
			// （字体/小文案改动动不了低频结构），判定为同一张的返工——替换原槽位
			// 而不是新增（2026-10-22 实测：模型修完字体重渲，E/F 两条同构图占双席）。
			if reworkIdx < 0 {
				if j := findReworkTarget(r.p, draft, in.SceneBrief, idx); j >= 0 {
					reworkIdx = j
					for _, f := range []struct{ from, to string }{
						{fmt.Sprintf("style/clips/sample-%d.mp4", idx), fmt.Sprintf("style/clips/sample-%d.mp4", j)},
						{fmt.Sprintf("style/sample-%d.png", idx), fmt.Sprintf("style/sample-%d.png", j)},
					} {
						_ = os.Remove(r.p.Artifact(f.to))
						_ = os.Rename(r.p.Artifact(f.from), r.p.Artifact(f.to))
					}
					_ = os.Remove(r.p.Artifact(fmt.Sprintf(".hf-sample/sample-%d.html", idx)))
					for k := 0; k < 3; k++ {
						_ = os.Remove(r.p.Artifact(fmt.Sprintf("style/clips/sample-%d-f%d.jpg", idx, k)))
					}
					idx = j
				}
			}
			entry := contract.Sample{
				Tag:   fmt.Sprintf("样张 %c · %s", 'A'+idx, contract.FirstRunes(in.SceneBrief, 20)),
				Desc:  in.SceneBrief,
				HTML:  html,
				Video: fmt.Sprintf("style/clips/sample-%d.mp4", idx),
			}
			replaced := ""
			if reworkIdx >= 0 {
				replaced = fmt.Sprintf("（检测到与「%s」同简述/同构图——已按返工处理：替换该张而非新增）", draft.Samples[idx].Tag)
				draft.Samples[idx] = entry
			} else {
				draft.Samples = append(draft.Samples, entry)
			}
			if err := writeJSONFile(styleDraftPath(r.p), draft); err != nil {
				return Observation{Text: "样张草稿落盘失败：" + err.Error()}
			}
			_ = writeJSONFile(r.p.Artifact("style/style_samples.json"), map[string]any{
				"samples": draft.Samples, "confirmed": false,
			})
			note := fmt.Sprintf("动态样张 %d 已渲染（style/clips/sample-%d.mp4，%.1f 秒）：", idx+1, idx, sampleDur)
			switch {
			case len(carried) > 0:
				note += fmt.Sprintf("机检/判卷还有 %d 项硬伤未修掉（已到修复轮上限或同类刹车）：%s。", len(carried), strings.Join(carried, "；"))
			case len(nits) > 0:
				note += fmt.Sprintf("机检过检，判卷放行（%d 项 med/low 小疵不立案，不修也行）：%s。", len(nits), strings.Join(nits, "；"))
			case skipNote != "":
				note += skipNote + "。"
			default:
				note += "机检与判卷全过。"
			}
			note += replaced
			note += fmt.Sprintf("要改这张就带 replace=%d 重新渲染（额度剩 %d 张）。已采库：%s。", idx+1, *snapLeft, orNone(specs))
			// 三帧细读（视觉副驾）：主脑看不见图，三帧画像替它自查构图与文字——
			// 问法与修复轮同源（qSampleFrames），同图同问磁盘缓存免费重放。
			if d, u := llm.VisionDigest(ctx, judgeFrames, qSampleFrames); d != "" {
				note += "\n三帧细读（视觉副驾）：\n" + d
				llm.AccumulateUsage(&loop.Usage, u)
				loop.Calls++
			}
			return Observation{Text: note}
		},
	}
}

// ahashReworkMax 构图近重复阈值（8×8 均值哈希汉明距离）。真机校准
// （p20261022-183926 六张海报两两实测）：同构图换字体=12，不同构图对 ≥23——
// 阈值取 14 居中；命中即判返工替换，误伤可用重渲恢复，代价不对称时从严。
const ahashReworkMax = 14

// findReworkTarget 返工识别：候选样张（已落 style/sample-<candidateIdx>.png）与
// 草稿里哪一张同简述或构图近重复。返回被替换槽位下标；-1=全新构图。
func findReworkTarget(p *pipeline.Project, draft *styleDraft, brief string, candidateIdx int) int {
	for j, s := range draft.Samples {
		if strings.TrimSpace(s.Desc) == strings.TrimSpace(brief) {
			return j
		}
	}
	candHash, err := aHash(p.Artifact(fmt.Sprintf("style/sample-%d.png", candidateIdx)))
	if err != nil {
		return -1
	}
	for j := range draft.Samples {
		h, err := aHash(p.Artifact(fmt.Sprintf("style/sample-%d.png", j)))
		if err == nil && hashDistance(candHash, h) <= ahashReworkMax {
			return j
		}
	}
	return -1
}

// orNone 空切片回显占位。
func orNone(ss []string) string {
	if len(ss) == 0 {
		return "（无）"
	}
	return strings.Join(ss, "、")
}

// sampleIssue 判卷问题一条。
type sampleIssue struct {
	Kind     string `json:"kind"`
	Detail   string `json:"detail"`
	Severity string `json:"severity"` // high=打回修复；med/low=记录不立案（2026-10-29 提速拍板）
}

// sampleJudge 判卷结论。
type sampleJudge struct {
	Pass   bool
	Score  float64
	Issues []sampleIssue
}

// HighIssues 全部 high 意见（唯一进修复轮的材料；med/low 留在观察里给模型参考）。
func (v sampleJudge) HighIssues() []sampleIssue {
	var out []sampleIssue
	for _, is := range v.Issues {
		if strings.EqualFold(is.Severity, "high") {
			out = append(out, is)
		}
	}
	return out
}

// MedLowIssues 全部 med/low 意见（放行不立案，观察里亮出来给模型参考）。
func (v sampleJudge) MedLowIssues() []sampleIssue {
	var out []sampleIssue
	for _, is := range v.Issues {
		if !strings.EqualFold(is.Severity, "high") {
			out = append(out, is)
		}
	}
	return out
}

// issueLines 意见清单 → 人话行（修复轮回喂与观察展示共用）。
func issueLines(ss []sampleIssue) []string {
	out := make([]string, 0, len(ss))
	for _, is := range ss {
		out = append(out, strings.TrimSpace(is.Kind+"："+is.Detail))
	}
	return out
}

// judgeSample 动态样张判卷（多模态 NoThink，结构化结论）。frames=首/中/尾三帧
// dataURL；refs 非空时真迹跟在样张帧之后，判卷含风格贴合项；refs 为空降级为旧口径
// （可读性/构图）。ok=false = 判卷通道不可用，调用方降级为「机械过检即放行」。
func (r *styleRun) judgeSample(ctx context.Context, loop *Loop, brief string, frames []string, w, h int) (sampleJudge, bool) {
	prov, err := llm.FromEnv(llm.StationPrepStyleVision)
	if err != nil {
		return sampleJudge{}, false
	}
	refs := r.refImages(refJudgeMax)
	var out struct {
		Verdict string        `json:"verdict"`
		Score   float64       `json:"score"`
		Issues  []sampleIssue `json:"issues"`
	}
	var criteria string
	if len(refs) > 0 {
		criteria = fmt.Sprintf("图1–图%d 是同一条 %.1f 秒动态样张的首/中/尾三帧；图%d–图%d 是该风格来源视频的原片真迹（对照物）。看图判卷：1 文字是否全部可读（字号/对比度/贴边裁切）；2 构图是否成立（主体清晰，无大片失衡空洞）；3 画面元素是否溢出或破图；4 尾帧是否定格在内容帧（尾帧黑屏/空屏=收尾拖尾事故）；5 整体是否贴合场景描述；6 风格贴合——对照真迹核配色（底色/强调色/文字色）、质感（颗粒/描边/纹理）、画面密度、字体气质四个维度；样张画的题材内容与真迹不同是应该的，不算偏离。每条 issue 必须带 severity：high=硬伤（文字读不出/互相压叠/空帧黑屏/明显溢出破图/风格四个维度明显两路——如真迹粗像素位图字、样张平滑衬线体）；med=能用但有明确毛边（对比度贴线、密度略偏、同路数但气质小差）；low=口味级吹毛求疵（对齐微偏、间距、可忽略的偏好项）。只有 high 打回修复，med/low 记录不立案——不要把毛边标成 high。", len(frames), sampleDur, len(frames)+1, len(frames)+len(refs))
	} else {
		criteria = fmt.Sprintf("图1–图%d 是同一条 %.1f 秒动态样张的首/中/尾三帧。看图判卷：1 文字是否全部可读（字号/对比度/贴边裁切）；2 构图是否成立（主体清晰，无大片失衡空洞）；3 画面元素是否溢出或破图；4 尾帧是否定格在内容帧（尾帧黑屏/空屏=收尾拖尾事故）；5 整体是否贴合场景描述。每条 issue 必须带 severity：high=硬伤（文字读不出/互相压叠/空帧黑屏/明显溢出破图）；med=能用但有明确毛边；low=口味级吹毛求疵。只有 high 打回修复，med/low 记录不立案——不要把毛边标成 high。", len(frames), sampleDur)
	}
	user := fmt.Sprintf("样张场景：%s\n画幅 %d×%d。该图已通过机械检查（无文字互相压叠、无禁用单位、字体可解析）。\n%s\n输出：{\"verdict\":\"pass\"或\"fail\",\"score\":0到10,\"issues\":[{\"kind\":\"unreadable|overlap|overflow|offstyle|blank|other\",\"detail\":\"一句人话\",\"severity\":\"high|med|low\"}]}，没问题 issues 给空数组；风格贴合问题用 offstyle，detail 写清哪个维度和真迹差在哪。", brief, w, h, criteria)
	// 站级 180s 总闸（2026-10-29 实测：端点黑洞时 300s leash×3 重试把秒级判卷挂满
	// 15 分钟，r7 卡死整段停摆）。判卷 flash 档 NoThink 秒级作业，180s 有余量；到期
	// 走「判卷通道不可用，机检过检即放行」降级，不挂死工位。
	ctx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	images := append(append([]string{}, frames...), refs...)
	usage, err := prov.GenerateJSONVisionNoThink(ctx, sampleJudgeSystem, user, images, 0.2, &out)
	llm.AccumulateUsage(&loop.Usage, usage)
	if err == nil {
		loop.Calls++ // 失败调用不计次数（账虚高，巡检小项）
	}
	if err != nil || out.Verdict == "" {
		return sampleJudge{}, false
	}
	j := sampleJudge{Score: out.Score, Issues: out.Issues}
	applySampleSeverity(&j)
	return j, true
}

// applySampleSeverity pass=无 high（2026-10-29 提速拍板）：旧口径任何 issue 一票
// 否决，实测样张 A 顶着 4 项口味精疵修满 2 轮一项未修掉、照样带伤放行——修复轮
// 白烧 ~4.5 分钟。硬伤三兄弟（压叠/不可读/空帧）永远 high，模型标低也压不住；
// severity 缺失按旧口径从严记 high（兼容不守新 schema 的输出）。raw verdict 只作
// 参考——fail 但全是 med/low 时按新口径放行。
func applySampleSeverity(j *sampleJudge) {
	j.Pass = true
	for i := range j.Issues {
		is := &j.Issues[i]
		k := strings.ToLower(is.Kind)
		if k == "overlap" || k == "unreadable" || k == "blank" || strings.TrimSpace(is.Severity) == "" {
			is.Severity = "high"
		}
		if strings.EqualFold(is.Severity, "high") {
			j.Pass = false
		}
	}
}

// sampleRepairSystem 修复轮（与判卷同一固定前缀形态，利前缀缓存）。
const sampleRepairSystem = `你是样张修复师。重写完整样张 HTML，修复全部质检问题，其余保持不变。只输出 JSON。`

// repairSample 按诊断重写样张 HTML（大杯主脑 + 文字对照物）：真迹画像与三帧细读
// 都是视觉副驾产物（问法与 get_style_detail/render_sample 同源，同图同问磁盘缓存
// 免费重放）——修复师对着画像修，不是凭一句「风格不贴合」瞎猜。
func (r *styleRun) repairSample(ctx context.Context, loop *Loop, brief, html string, frames []string, issues []string) (string, bool) {
	prov, err := llm.FromEnv(llm.StationPrepStyleText)
	if err != nil {
		return "", false
	}
	cv := pipeline.ProjectCanvas(r.p)
	refs := r.refImages(refJudgeMax)
	refDigest, u1 := llm.VisionDigest(ctx, refs, qEntryFrames)
	frameDigest, u2 := llm.VisionDigest(ctx, frames, qSampleFrames)
	llm.AccumulateUsage(&loop.Usage, u1)
	llm.AccumulateUsage(&loop.Usage, u2)
	if len(refs) > 0 {
		loop.Calls++
	}
	if len(frames) > 0 {
		loop.Calls++
	}
	var out struct {
		HTML string `json:"html"`
	}
	contrast := ""
	if refDigest != "" {
		contrast += "真迹画像（风格贴合的对照物）：\n" + refDigest + "\n\n"
	}
	if frameDigest != "" {
		contrast += "三帧细读（把问题清单对应到具体帧）：\n" + frameDigest + "\n\n"
	}
	user := fmt.Sprintf(`你写的样张（场景：%s）判卷未过，重写完整 HTML 修复全部问题（没被点名的部分保持不变）。

问题清单：
- %s

%s当前 HTML：
%s

只输出 JSON：{"html":"<完整新HTML>"}（无围栏）。画布 %d×%d；字体用目录字体（%s）、系统字体（微软雅黑/黑体/宋体/楷体）、monospace，或保留原样张里 <!-- font: Family | 协议 | 直链URL --> 自装声明（平台已装好的家族，Family 写法逐字保留别改）。`,
		brief, strings.Join(issues, "\n- "), contrast, html, int(cv.W), int(cv.H), pipeline.FontCatalogList())
	usage, err := prov.GenerateJSONNoThinkTemp(ctx, sampleRepairSystem, user, 0.5, &out)
	llm.AccumulateUsage(&loop.Usage, usage)
	if err == nil {
		loop.Calls++
	}
	if err != nil || len(out.HTML) < 200 || len(out.HTML) > sampleHTMLMax {
		return "", false
	}
	return out.HTML, true
}

// runAIScript 跑 ai/ 侧脚本（仓库根为 cwd；pipeline.CommandContext 取消时击杀
// 整棵进程树，python/孙进程不残留）。
func (r *styleRun) runAIScript(ctx context.Context, timeout time.Duration, script string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	argv := append([]string{filepath.Join(r.d.RootDir, "ai", script)}, args...)
	cmd := pipeline.CommandContext(ctx, pipeline.PythonExe(), argv...)
	cmd.Dir = r.d.RootDir
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("%v: %s", err, pipeline.TailLines(string(out), 4))
	}
	return nil
}

// issueSignature 问题清单归一化签名（刹车判据）：逐行取前 16 rune 排序拼接——
// 同一批问题两轮原样重现即视为原地打转。
func issueSignature(issues []string) string {
	sorted := append([]string(nil), issues...)
	sort.Strings(sorted)
	parts := make([]string, 0, len(sorted))
	for _, s := range sorted {
		r := []rune(s)
		if len(r) > 16 {
			r = r[:16]
		}
		parts = append(parts, string(r))
	}
	return strings.Join(parts, "|")
}

func (r *styleRun) loadDraft() *styleDraft {
	var d styleDraft
	if b, err := os.ReadFile(styleDraftPath(r.p)); err == nil {
		_ = json.Unmarshal(b, &d)
	}
	return &d
}

// registerStyleTool register_style：衍生/自定义登记（治理见包注释）。
func (r *styleRun) registerStyleTool() Tool {
	return Tool{
		Def: toolSchema("register_style", "库中没有合适的风格时，从最接近的条目衍生一个（自动带出处与「未验证」标签，出片后才转正；单次 run 限登记 1 个；不开放已有条目编辑/删除）。base_id 可空=完全自定义（须给 tags_main，从词表选）。", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"base_id":   map[string]any{"type": "string", "description": "基风格 id（可空=自定义）"},
				"name":      map[string]any{"type": "string", "description": "新风格名"},
				"delta":     map[string]any{"description": "与基风格的差异（自定义时是风格说明）", "type": "string"},
				"subject":   map[string]any{"type": "string", "description": "题材一句话（可空）"},
				"tags_main": map[string]any{"type": "string", "description": "主标签（覆盖继承值时给；自定义必填，用词表名）"},
				"mood":      map[string]any{"type": "string", "description": "情绪分面（可空）"},
				"tone":      map[string]any{"type": "string", "description": "色调分面（可空）"},
				"density":   map[string]any{"type": "string", "description": "密度分面（可空）"},
			},
			"required": []string{"name", "delta"},
		}),
		Run: func(_ context.Context, args string) Observation {
			var in struct {
				BaseID   string `json:"base_id"`
				Name     string `json:"name"`
				Delta    string `json:"delta"`
				Subject  string `json:"subject"`
				TagsMain string `json:"tags_main"`
				Mood     string `json:"mood"`
				Tone     string `json:"tone"`
				Density  string `json:"density"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return Observation{Text: err.Error()}
			}
			if r.d.Lib == nil {
				return Observation{Text: "风格库未接线，无法登记。"}
			}
			if r.registered != "" {
				return Observation{Text: fmt.Sprintf("本次已登记过 %s（单次限 1 个）——在它基础上写设计系统即可。", r.registered)}
			}
			tags := contract.StyleTags{}
			if s := strings.TrimSpace(in.TagsMain); s != "" {
				tags.Main = s
			}
			st, err := r.d.Lib.Derive(stylelib.DeriveInput{
				BaseID:  strings.TrimSpace(in.BaseID),
				Name:    in.Name,
				Delta:   in.Delta,
				Subject: in.Subject,
				Tags:    &tags,
				Facets:  &stylelib.StyleFacets{Mood: in.Mood, Tone: in.Tone, Density: in.Density},
			})
			if err != nil {
				msg := "登记被拒：" + err.Error()
				// 词表相关的拒绝把现有词表列出来——盲猜词表名是死循环的根源
				if strings.Contains(msg, "词表") && r.d.Lib != nil {
					if vocab := r.d.Lib.VocabList(); len(vocab) > 0 {
						names := make([]string, 0, len(vocab))
						for _, e := range vocab {
							names = append(names, e.Name)
						}
						msg += "。当前词表收了这些风格名（任选其一或衍生）：" + strings.Join(names, "、")
					}
				}
				return Observation{Text: msg}
			}
			r.registered = st.ID
			// 衍生条目不自带媒体：对照真迹退回基风格（视觉最近邻），chosen 空时才补位
			if r.chosen == "" && st.DerivedFrom != "" {
				r.chosen = st.DerivedFrom
				r.refCache = nil
			}
			r.p.Manifest("prep.style_registered", st.ID+" "+st.Name)
			return Observation{Text: fmt.Sprintf("已登记衍生风格 %s「%s」（未验证，出片后转正）。submit_style 时 style_id 用它。", st.ID, st.Name)}
		},
	}
}

// normalizeStyleArgs 提交参数的宽容归一（P2 兜底层）：schema 写了示例模型仍会
// 漏看，数字字重/布尔叠层/"12px" 圆角这类一笔之差直接拒太贵（实测连败 4 轮烧掉
// 2 分钟+4 个回合）。已知反直觉字段就地转换并生成归一说明回喂，帮模型下次给对。
// 解析不动的原样返回（让 decodeArgs 的类型报错去接）。
func normalizeStyleArgs(args string) (string, []string) {
	var m map[string]any
	if json.Unmarshal([]byte(args), &m) != nil {
		return args, nil
	}
	ds, _ := m["design_system"].(map[string]any)
	if ds == nil {
		return args, nil
	}
	var notes []string
	if fonts, _ := ds["fonts"].(map[string]any); fonts != nil {
		for _, role := range []string{"display", "body", "data"} {
			fr, _ := fonts[role].(map[string]any)
			if fr == nil {
				continue
			}
			if n, ok := fr["weight"].(float64); ok {
				s := strconv.FormatFloat(n, 'f', -1, 64)
				fr["weight"] = s
				notes = append(notes, fmt.Sprintf("fonts.%s.weight %v→%q（数字→字符串）", role, n, s))
			}
		}
	}
	if layouts, _ := ds["layouts"].([]any); layouts != nil {
		for i, lv := range layouts {
			l, _ := lv.(map[string]any)
			if l == nil {
				continue
			}
			switch b := l["layered"].(type) {
			case bool:
				if b {
					l["layered"] = "yes"
					notes = append(notes, fmt.Sprintf("layouts[%d].layered true→\"yes\"（布尔→字符串；叠层要写明哪层压哪层，下次直接写说明）", i))
				} else {
					delete(l, "layered")
					notes = append(notes, fmt.Sprintf("layouts[%d].layered false→已删除（无叠层不传该字段）", i))
				}
			case float64:
				delete(l, "layered")
				notes = append(notes, fmt.Sprintf("layouts[%d].layered 数字→已删除（该字段是叠层说明文字，不是数值）", i))
			}
		}
	}
	fixSlots := func(where string, s map[string]any) {
		slots, _ := s["slots"].(map[string]any)
		if slots == nil {
			return
		}
		for _, key := range []string{"radius", "border_w"} {
			v, ok := slots[key].(string)
			if !ok {
				continue
			}
			if n, err := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "px"))); err == nil {
				slots[key] = n
				notes = append(notes, fmt.Sprintf("%s.slots.%s %q→%d（\"Npx\"→数字）", where, key, v, n))
			}
		}
	}
	if skins, _ := ds["skins"].([]any); skins != nil {
		for i, sv := range skins {
			if s, _ := sv.(map[string]any); s != nil {
				fixSlots(fmt.Sprintf("skins[%d]", i), s)
			}
		}
	}
	if s, _ := ds["skin"].(map[string]any); s != nil {
		fixSlots("skin", s)
	}
	if len(notes) == 0 {
		return args, nil
	}
	if b, err := json.Marshal(m); err == nil {
		return string(b), notes
	}
	return args, nil
}

// submitStyleTool submit_style：唯一出口。校验通过 = 风格包 + legacy style_samples.json 落盘。
func (r *styleRun) submitStyleTool() Tool {
	return Tool{
		Def: toolSchema("submit_style", "提交风格包（唯一出口），一次交齐四件：风格描述、剪辑语法、设计系统、样张 id 列表（字段契约见 schema；entry_exit 模板是转场的载体）。校验：剪辑语法三字段齐全且段长带数值合法；样张 ≥1；style_id 已存在（库内或本次登记）；设计系统缺件会被打回。", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"style_id":    map[string]any{"type": "string", "description": "风格条目 id（库内或本次 register_style 登记）"},
				"description": map[string]any{"type": "string", "description": "风格描述：视觉语言、色彩、质感、构图倾向，附选择理由（一段话，分镜工位靠它理解语境）"},
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
					"description": "样张 id 列表（本次 render_sample 渲过的张，如 [\"样张 A · …\"]；≥1）",
				},
			},
			"required": []string{"style_id", "description", "grammar", "design_system"},
		}),
		Run: func(ctx context.Context, args string) Observation {
			normArgs, notes := normalizeStyleArgs(args)
			// 归一说明附在每次回喂头部：这次放行不等于下次也放——模型得学会给对
			reply := func(text string) Observation {
				if len(notes) > 0 {
					text = "（已自动归一：" + strings.Join(notes, "；") + "——下次按 schema 标注的类型直接给对）\n" + text
				}
				return Observation{Text: text}
			}
			var in struct {
				StyleID      string                `json:"style_id"`
				Description  string                `json:"description"`
				Grammar      contract.EditGrammar  `json:"grammar"`
				DesignSystem contract.StyleSamples `json:"design_system"`
				SampleIDs    []string              `json:"sample_ids"`
			}
			if err := decodeArgs(normArgs, &in); err != nil {
				return reply(err.Error())
			}
			// style_id 已存在（库内含未入库草稿，或本次登记）
			sid := strings.TrimSpace(in.StyleID)
			if r.d.Lib != nil {
				if _, ok := r.d.Lib.Get(sid); !ok && sid != r.registered {
					return reply(fmt.Sprintf("被拒：style_id %s 不在库内也不是本次登记的条目（本次登记：%s）", sid, r.registered))
				}
			}
			if sid == "" {
				return reply("被拒：style_id 为空")
			}
			draft := r.loadDraft()
			if len(draft.Samples) < 1 {
				return reply("被拒：还没有样张——先 render_sample 至少 1 张（场景取自口播稿代表性内容）")
			}
			ids := in.SampleIDs
			if len(ids) == 0 {
				for _, s := range draft.Samples {
					ids = append(ids, s.Tag)
				}
			}
			ds := in.DesignSystem
			ds.StyleID = sid
			ds.Samples = draft.Samples
			ds.VendLibs = sampleVendLibs(draft.Samples) // 样张自采货架：正式段优先复现
			// 样张自装字体记账（终审页披露 + 制作段钳制放行的依据）：HTML 声明 ∪
			// 申请书账——修复轮重写 HTML 可能弄丢声明，申请书是持久 BOM，并集才不丢
			var cfHTMLs []string
			for _, s := range draft.Samples {
				cfHTMLs = append(cfHTMLs, s.HTML)
			}
			ds.CustomFonts = pipeline.MergeCustomFonts(
				pipeline.CustomFontsFromHTMLs(cfHTMLs),
				pipeline.CustomFontsFromRequest(r.p))
			ds.Confirmed = false // 打包终审硬门：确认前禁止开工
			sp := contract.NewStylePack(sid, in.Description, in.Grammar, &ds, ids)
			if err := contract.ValidateStylePack(sp); err != nil {
				return reply("被拒：" + err.Error())
			}
			// 字体闸：设计声明里出现渲染端没有的字体族 = 样张与正片双双静默回退
			//（齐马蓝教训，2026-10-21 呼啸骑士战实测复犯）。可用=目录/系统白名单/
			// 通用族/已 ensure 的样张字体（assets——目录与自装都进 font_assets.json）。
			// 打回并列出可用品类，模型当场换——比事后剥族保得住设计意图。
			if bad := pipeline.UnresolvedDesignFonts(&ds, pipeline.LoadSampleFontAssets(r.p)); len(bad) > 0 {
				return reply(fmt.Sprintf("被拒：设计系统字体栈含渲染端没有的字体族「%s」。目录字体可选：%s；系统字体：微软雅黑/黑体/宋体/楷体/仿宋/Noto Sans SC；样张自装的字体以样张里 <!-- font: … --> 声明过的家族名为准（设计栈与声明写法逐字一致）。换好后重交。",
					strings.Join(bad, "、"), pipeline.FontCatalogList()))
			}
			// 设计锁定（legacy 路径同源）：字体钳制/接缝契约机械执法/目录字体申请书/
			// design.md+skin.json。prep 路径此前没人跑 LockDesign，目录字体申请书
			// 从未落盘——font_tool 永远空转，样张只能拿系统字体兜底（实测缺口）。
			if err := pipeline.LockDesign(r.p, &ds); err != nil {
				return reply("设计锁定失败：" + err.Error())
			}
			// legacy 先落、工件标记（stylepack.json）最后落（2026-10-19 中-21，
			// 与 script/storyboard 同纪律）：两笔之间崩溃不留「站被跳过但
			// style_samples 陈旧」的半完成态
			if err := writeJSONFile(r.p.Artifact("style/style_samples.json"), &ds); err != nil {
				return reply("设计系统落盘失败：" + err.Error())
			}
			if err := writeJSONFile(stylePackPath(r.p), sp); err != nil {
				return reply("落盘失败：" + err.Error())
			}
			_ = pipeline.SetProjectField(r.p, "style_id", sid)
			// 简报保底：②自己选的条目（用户没在界面选过）也要落 style_brief.json——
			// 制作阶段的真迹注入（sceneGen 对照/frameqa 考卷）全靠它，②不落则下游全盲。
			// 已有同条目简报（applyStyle 落的）不动；换条目时重写。
			if r.d.Lib != nil {
				if cur := pipeline.LoadStyleBrief(r.p); cur == nil || cur.EntryID != sid {
					if b, berr := r.d.Lib.BuildBrief(sid, nil); berr == nil {
						if werr := stylelib.WriteBriefFile(r.p.Artifact("style/style_brief.json"), b); werr == nil {
							r.p.Manifest("prep.style_brief", "简报落盘 "+sid+"（制作阶段真迹注入可用）")
						}
					}
				}
			}
			_ = os.Remove(styleDraftPath(r.p))
			// 字体准备 + 全量重截：首渲染时 font_assets 未备好，样张截图回退过系统
			// 字体——ensure 出真字体后按缓存键（font_assets.json 新鲜度）重截全部
			// 样张。失败只降级记账，不挡交稿。
			if ferr := r.runAIScript(ctx, 6*time.Minute, "font_tool.py", "ensure", r.p.Dir); ferr != nil {
				r.p.Manifest("prep.style_font_degrade", pipeline.FirstLine(ferr.Error(), 160))
			} else if serr := r.runAIScript(ctx, 2*time.Minute, "snap_samples.py", r.p.Dir); serr != nil {
				r.p.Manifest("prep.style_resnap_error", pipeline.FirstLine(serr.Error(), 160))
			}
			r.d.emit(r.p.ID, "stage", "prep/style", fmt.Sprintf("done：%s「%s」语法[%s %.0f-%.0fs] 样张×%d",
				sid, ds.Direction, in.Grammar.Density, in.Grammar.BandMin, in.Grammar.BandMax, len(draft.Samples)))
			customNote := "无"
			if len(ds.CustomFonts) > 0 {
				names := make([]string, 0, len(ds.CustomFonts))
				for _, c := range ds.CustomFonts {
					names = append(names, c.Family)
				}
				customNote = strings.Join(names, "、")
			}
			done := reply(fmt.Sprintf("风格包已提交：%s（%s）；剪辑语法 %s / %.0f–%.0f 秒 / %s；样张 %d 张；自采库：%s；自装字体：%s。自装清单会随终审页列出（含协议与来源）。样张未经用户终审确认前禁止开工。",
				sid, ds.Direction, in.Grammar.Density, in.Grammar.BandMin, in.Grammar.BandMax, in.Grammar.Continuity, len(draft.Samples), orNone(ds.VendLibs), customNote))
			done.Done = true
			return done
		},
	}
}
