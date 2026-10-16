package prep

// style.go —— ②风格工位（prep-agents-design §3.2，多模态模型）：
// 为文字稿选定视觉风格，产出可直接指导制作的风格包。对风格库拥有完整探索能力：
// 检索、分面、图库、使用数据、衍生登记（单次 run 限 1 个；自动带 derived_from
// 出处 + 「未验证」标签，用它产出的片出片后才转正；不开放已有条目编辑/删除）。
// 样张 render_sample 走现有渲染壳（ai/snap_samples.py），同参数哈希缓存。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/pipeline"
	"webpage-video-agent/server/internal/react"
	"webpage-video-agent/server/internal/stylelib"
)

// styleBudget ②的回合预算（§5.2 基线 ②≤12；用户拍板放宽到 30——样张渲染
// 一张一轮，探索+衍生+3 张样张+交稿的链不宽裕）。预算不进提示词。
const styleBudget = 30

// styleSampleMax 样张上限（与 contract.StyleSamples 契约一致）。
const styleSampleMax = 3

const styleSystem = `你是美术指导。任务：为口播稿选定视觉风格，产出可直接指导制作的风格包。材料：get_script 拿口播稿；search_styles 按情绪与题材定分面、有筛选地浏览（不必遍历全库）；get_style_detail 看中的条目——描述、特色、样张图库、历史使用记录；render_sample 用口播稿代表性内容验证本片落地效果。
库中没有合适的，从最接近的条目衍生一个并说明差异（register_style，单次限登记 1 个；衍生条目带「未验证」标签，出片后才转正）。
产出四件（submit_style 一次交齐）：风格描述（视觉语言、色彩、质感、构图倾向，附选择理由——分镜工位靠它理解语境）；剪辑语法（切分密度 / 段长带秒数 / 同舞台延续强弱）；设计系统（皮肤、字体角色、动效语言、贯穿装置、版式、负面清单，以及本风格的**标准入场/收尾动画模板**——它是转场的载体，一套模板一段内生效，禁止逐段即兴发明）；样张 id 列表。
规则：只在库内风格（含本次衍生登记）中选择；样张场景必须取自口播稿代表性内容，禁止通用示例；口播稿文本一个字不改。完成即调用 submit_style。`

// runStyleStation 跑②：成功后工件 B 落盘（stylepack.json + legacy style_samples.json 未确认态）。
func runStyleStation(ctx context.Context, p *pipeline.Project, d Deps) error {
	script, err := LoadScript(p)
	if err != nil {
		return fmt.Errorf("风格工位依赖文字稿: %w", err)
	}
	r := &styleRun{p: p, d: d, script: script}
	log := openSessionLog(p, StationStyle)
	defer log.close()
	loop := &Loop{
		Prov:      r.prov(),
		System:    styleSystem,
		Kickoff:   styleKickoff(p),
		Tools:     r.tools(),
		Submit:    "submit_style",
		MaxRounds: styleBudget,
		OnStep:    manifestStep(p, StationStyle, log),
	}
	d.emit(p.ID, "stage", "prep/style", "running")
	if err := loop.Run(ctx); err != nil {
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
	p          *pipeline.Project
	d          Deps
	script     *contract.Script
	registered string // 本次 run 衍生登记的条目 id（空=未登记；单次限 1 个）
}

func (r *styleRun) prov() react.ChatClient {
	// ②必须多模态——RoleVisual 档。Chat 的多模态观察走 user 附图通道。
	prov, _ := llm.FromEnv(llm.RoleVisual)
	return prov
}

func (r *styleRun) tools() []Tool {
	snapLeft := styleSampleMax
	return []Tool{
		r.getScriptTool(),
		r.searchStylesTool(),
		r.getStyleDetailTool(),
		r.getStyleUsageTool(),
		r.renderSampleTool(&snapLeft),
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
		Def: toolSchema("search_styles", "检索风格库（主入口）。空 query = 全量分页；分面给情绪/题材（关键词）/色调/密度。看中的条目用 get_style_detail 细看。", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "关键词（风格名/题材/气质词；可空=全量）"},
				"mood":  map[string]any{"type": "string", "description": "情绪分面（如 沉稳/明快/悬疑）"},
				"tone":  map[string]any{"type": "string", "description": "色调分面（如 黑金/低饱和暖色/冷蓝）"},
				"density": map[string]any{"type": "string", "description": "密度分面（如 疏朗/中密/高密）"},
				"page":  map[string]any{"type": "integer", "description": "页码，从 1 起（默认 1，每页 10 条）"},
			},
		}),
		Run: func(_ context.Context, args string) Observation {
			var in struct {
				Query  string `json:"query"`
				Mood   string `json:"mood"`
				Tone   string `json:"tone"`
				Density string `json:"density"`
				Page   int    `json:"page"`
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
func (r *styleRun) getStyleDetailTool() Tool {
	return Tool{
		Def: toolSchema("get_style_detail", "看一条风格的完整档案：标签与特色机制、分面、题材、样张图库清单、衍生出处与验证状态。", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "风格条目 id（search_styles 结果里拿）"},
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
			frames, hasVideo := r.d.Lib.MediaNames(st.ID)
			switch {
			case hasVideo && len(frames) > 0:
				fmt.Fprintf(&b, "样张图库：%d 张抽帧 + 原片（历史实拍，参考质感与密度）\n", len(frames))
			case len(frames) > 0:
				fmt.Fprintf(&b, "样张图库：%d 张抽帧（历史实拍）\n", len(frames))
			default:
				b.WriteString("样张图库：暂无（无历史实拍）\n")
			}
			if st.Unverified {
				fmt.Fprintf(&b, "治理状态：未验证（衍生自 %s）——用它产出的片出片后才转正\n", st.DerivedFrom)
			}
			if st.Usage.Count > 0 {
				fmt.Fprintf(&b, "使用记录：%d 次，其中通过 %d 次", st.Usage.Count, st.Usage.Passes)
				if st.Usage.LastScore > 0 {
					fmt.Fprintf(&b, "，最近审核 %.1f/5 分", st.Usage.LastScore)
				}
				b.WriteString("\n")
			}
			return Observation{Text: b.String()}
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

// renderSampleTool render_sample：样张渲染（现有渲染壳 + 同参数哈希缓存）。
// 样张 HTML 由模型现写（与设计系统同一套皮肤色值），截图回喂多模态自检。
func (r *styleRun) renderSampleTool(snapLeft *int) Tool {
	cv := pipeline.ProjectCanvas(r.p) // 画幅跟项目走：横版样张上验竖版版式=无效验证（2026-10-15 实测 P1）
	w, h := int(cv.W), int(cv.H)
	return Tool{
		Def: toolSchema("render_sample", fmt.Sprintf("渲染一张样张验证落地效果：scene_brief=场景简述（必须取自口播稿代表性内容）；html=%d×%d 自包含样张页面（画幅必须与本项目一致，颜色/字体必须与本片设计系统皮肤槽位逐字一致，纯 CSS 无图片无外链，画布 html,body{width:%dpx;height:%dpx;overflow:hidden}，≤2000 字符）。渲染结果截图回喂给你检查；最多 3 张，构图版式必须明显不同。", w, h, w, h), map[string]any{
			"type": "object",
			"properties": map[string]any{
				"scene_brief": map[string]any{"type": "string", "description": "场景简述（一句话：这张样张画口播稿的哪个代表性内容）"},
				"html":        map[string]any{"type": "string", "description": fmt.Sprintf("样张 HTML（完整自包含文档，画布 %d×%d）", w, h)},
			},
			"required": []string{"scene_brief", "html"},
		}),
		Run: func(ctx context.Context, args string) Observation {
			var in struct {
				SceneBrief string `json:"scene_brief"`
				HTML       string `json:"html"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return Observation{Text: err.Error()}
			}
			if strings.TrimSpace(in.HTML) == "" {
				return Observation{Text: "被拒：html 为空"}
			}
			if len(in.HTML) > 20000 {
				return Observation{Text: "被拒：样张 HTML 超过 20000 字符（目标 ≤2000）"}
			}
			if strings.TrimSpace(in.SceneBrief) == "" {
				return Observation{Text: "被拒：scene_brief 为空——样张场景必须取自口播稿代表性内容"}
			}
			draft := r.loadDraft()
			if len(draft.Samples) >= styleSampleMax {
				return Observation{Text: fmt.Sprintf("样张已满 %d 张（契约上限），直接 submit_style。", styleSampleMax)}
			}
			// 同参数哈希缓存：同 html 重渲染直接回上次的截图说明
			idx := len(draft.Samples)
			if hit, note := r.cachedRender(in.HTML, idx); hit {
				return Observation{Text: note}
			}
			if *snapLeft <= 0 {
				return Observation{Text: "渲染额度已用完（最多 3 张），直接 submit_style。"}
			}
			*snapLeft--
			draft.Samples = append(draft.Samples, contract.Sample{
				Tag:  fmt.Sprintf("样张 %c · %s", 'A'+idx, contract.FirstRunes(in.SceneBrief, 20)),
				Desc: in.SceneBrief,
				HTML: in.HTML,
			})
			// 截图脚本吃 style_samples.json——把样张草稿镜像进去（正式落盘仍以 submit_style 为准）
			if err := writeJSONFile(styleDraftPath(r.p), draft); err != nil {
				return Observation{Text: "样张草稿落盘失败：" + err.Error()}
			}
			_ = writeJSONFile(r.p.Artifact("style/style_samples.json"), map[string]any{
				"samples": draft.Samples, "confirmed": false,
			})
			if err := snapSamples(ctx, r.d, r.p); err != nil {
				return Observation{Text: fmt.Sprintf("样张已保存（第 %d 张），但截图失败（%s）——无法目检，凭代码自查后提交", idx+1, pipeline.FirstLine(err.Error(), 120))}
			}
			png := r.p.Artifact(fmt.Sprintf("style/sample-%d.png", idx))
			var images []string
			note := fmt.Sprintf("样张 %d 已渲染并截图（style/sample-%d.png）：", idx+1, idx)
			if dataURL, err := shrinkFrame(png); err == nil {
				images = append(images, dataURL)
				note += "附图见下。检查：构图是否成立、配色是否与皮肤一致、文字是否可读、有无溢出画布、高亮条/色块不得压住文字（压到字下半也不行）。不满意就调整后重新渲染这一张（重新调用会替换当前张）。"
			} else {
				note += "截图缩略失败，凭代码自查。"
			}
			r.markRenderCache(in.HTML)
			return Observation{Text: note, Images: images}
		},
	}
}

// cachedRender 同 HTML 参数哈希命中 → 不重渲染（§3.2 同参数哈希缓存）。
func (r *styleRun) cachedRender(html string, idx int) (bool, string) {
	key := renderHash(html)
	cached := r.p.Artifact("llm/prep-render-cache/" + key + ".txt")
	if b, err := os.ReadFile(cached); err == nil {
		return true, string(b)
	}
	return false, ""
}

func (r *styleRun) markRenderCache(html string) {
	key := renderHash(html)
	dir := r.p.Artifact("llm/prep-render-cache")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(dir+"/"+key+".txt", []byte("同参数样张已渲染过（缓存命中，未重渲）"), 0o644)
}

func renderHash(html string) string {
	return contentHash8(html)
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
				BaseID: strings.TrimSpace(in.BaseID),
				Name:   in.Name,
				Delta:  in.Delta,
				Subject: in.Subject,
				Tags:   &tags,
				Facets: &stylelib.StyleFacets{Mood: in.Mood, Tone: in.Tone, Density: in.Density},
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
		Def: toolSchema("submit_style", "提交风格包（唯一出口）。校验：剪辑语法三字段齐全且段长带数值合法；样张 ≥1；style_id 已存在（库内或本次登记）；设计系统缺件会被打回。", map[string]any{
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
						"device:{name,desc,per_seg} 贯穿装置；entry_exit:{entry,exit,entry_dur(0.2-1.0)} 标准入场/收尾动画模板（转场载体，一套模板一段内生效）；" +
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
		Run: func(_ context.Context, args string) Observation {
			normArgs, notes := normalizeStyleArgs(args)
			// 归一说明附在每次回喂头部：这次放行不等于下次也放——模型得学会给对
			reply := func(text string) Observation {
				if len(notes) > 0 {
					text = "（已自动归一：" + strings.Join(notes, "；") + "——下次按 schema 标注的类型直接给对）\n" + text
				}
				return Observation{Text: text}
			}
			var in struct {
				StyleID      string               `json:"style_id"`
				Description  string               `json:"description"`
				Grammar      contract.EditGrammar `json:"grammar"`
				DesignSystem contract.StyleSamples `json:"design_system"`
				SampleIDs    []string             `json:"sample_ids"`
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
			ds.Confirmed = false // 打包终审硬门：确认前禁止开工
			sp := contract.NewStylePack(sid, in.Description, in.Grammar, &ds, ids)
			if err := contract.ValidateStylePack(sp); err != nil {
				return reply("被拒：" + err.Error())
			}
			if err := writeJSONFile(stylePackPath(r.p), sp); err != nil {
				return reply("落盘失败：" + err.Error())
			}
			// legacy 兼容：设计系统整包写 style_samples.json（未确认态——
			// 现有确认门/样张页/produce 的读法全部不变）
			if err := writeJSONFile(r.p.Artifact("style/style_samples.json"), &ds); err != nil {
				return reply("设计系统落盘失败：" + err.Error())
			}
			_ = pipeline.SetProjectField(r.p, "style_id", sid)
			_ = os.Remove(styleDraftPath(r.p))
			r.d.emit(r.p.ID, "stage", "prep/style", fmt.Sprintf("done：%s「%s」语法[%s %.0f-%.0fs] 样张×%d",
				sid, ds.Direction, in.Grammar.Density, in.Grammar.BandMin, in.Grammar.BandMax, len(draft.Samples)))
			done := reply(fmt.Sprintf("风格包已提交：%s（%s）；剪辑语法 %s / %.0f–%.0f 秒 / %s；样张 %d 张；设计系统含入场/收尾模板。样张未经用户终审确认前禁止开工。",
				sid, ds.Direction, in.Grammar.Density, in.Grammar.BandMin, in.Grammar.BandMax, in.Grammar.Continuity, len(draft.Samples)))
			done.Done = true
			return done
		},
	}
}
