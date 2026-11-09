package prep

// storyboard.go —— ③分镜工位（prep-agents-design §3.3，纯文本模型）：
// 基于文字稿与风格包产出分镜稿——纯内容：结构（段落+段）与场景规划（每段画面
// brief，含首拍/末拍）。不出现任何缝/转场/机制概念（§4 缝已退役）。
// 切段三层依据（按优先级）：话题完整（硬约束）> 停顿证据（tts_align 未注册——
// 先配后切未采纳，用时长估算顶替）> 剪辑语法（风格包的切分密度与段长带）。

import (
	"context"
	"fmt"
	"strings"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/pipeline"
	"webpage-video-agent/server/internal/react"
)

// storyboardBudget ③的回合预算（§5.2 基线 ③≤10；用户拍板放宽到 30；
// 2026-10-24 再放宽到 60——qwen3-vl-plus 逐段 estimate_speech_duration 每回合
// 只吃 1-2 段，31 段文稿 30 回合估不完就被掐，submit 永远排在队尾）。
const storyboardBudget = 60

// storyboardSystem 系统提示词（2026-10-28 边界化）：只装身份/任务、跨工具流程
// （拟段时长自检、先 validate 后 submit）与手艺政策（切段优先级、句编号引用、
// 首末拍承诺）；工具机制住各自 description（句编号契约在 get_script）。
const storyboardSystem = `你是分镜师兼视觉导演。任务：基于口播稿与风格包产出分镜稿。材料：get_script、get_style_package；拟段时用 estimate_speech_duration 自检每段落段长带。
切段按优先级：话题没讲完不在中间切；句与句之间是天然候选切点（目前没有真实停顿证据，用时长估算顶替）；疏密与段长跟随剪辑语法。想做出「画面连续过渡」，正确做法是并成一段，而不是设缝。
段落带一句话主题，按段序铺满全部内容；旁白只引用句编号（如 "N3-N4"），不改写原文、不抄写文本。
先定全片世界（world）：舞台、幕布、灯光、道具、观众席。有没有角色形象、画什么角色，由题材、文稿口吻与风格气质决定——定了角色就写 cast_legend：每个角色的外形、身体、眼睛、性格，全片复用，同一角色跨镜不能变形，镜头 who 用角色表里的同一名字。
每段写场景规划（visual_brief：场景 + 首拍 + 末拍）与镜头调度（shot）：
- size 景别：wide（全景）/ medium（中景）/ close（近景）/ overhead（俯拍）/ full（字卡/满屏）；
- camera 机位与运镜：写清从哪拍到哪、怎么动（如「从桌面近景横移到屏幕」）；
- stage 舞台/道具/前后景：本镜画面里真实摆什么；
- cast 本镜出场角色（0–4 个）：有角色才写，who 用角色表里的同一名字，pose 写身体动作，emotion 写情绪；没有角色的镜头（空镜/物件/字卡）不写；
- action 这一镜必须看得见的戏：谁看谁、谁指、谁退、谁转头、发生了什么；
- dialogue 只写短台词/字卡（≤12 字），不要复述旁白；没有就留空；
- transition 与下一镜的接法（匹配切/白闪/直切/叠化）。
真图素材：开工先 get_materials 看项目上传了哪些实拍图/截图。**标了「必需」的素材必须被某一镜认领**——在该段写 shot.images（file 逐字一致、role=hero|inset、at/hold、present 摆位），并且该段 visual_brief.scene 里要把这张图写进画面（写文件名或「截图/架构图/表格」这类具体指代）。必需素材漏认领 = 提交被拒。没有必需素材就整片不要写 shot.images，全部画面程序化绘制。
镜头是调度，不是信息图：镜头必须有可感知的景别/机位/运镜变化，禁止整段静态卡片；有角色的镜里，角色要有身体、眼睛、表情与反应关系。
真图是「证据」，不是贴纸：认领它的那一镜，画面要围着它演（角色在指/在翻/在对比），别把截图塞进角落当角标。
草稿先过 validate_storyboard，按报告修到零错误，再 submit_storyboard。`

// runStoryboardStation 跑③：成功后工件 C 落盘（draft.json + 翻译的 storyboard.json）。
func runStoryboardStation(ctx context.Context, p *pipeline.Project, d Deps) error {
	script, err := LoadScript(p)
	if err != nil {
		return fmt.Errorf("分镜工位依赖文字稿: %w", err)
	}
	pack, err := LoadStylePack(p)
	if err != nil {
		return fmt.Errorf("分镜工位依赖风格包: %w", err)
	}
	log := openSessionLog(p, StationStoryboard)
	defer log.close()
	loop := &Loop{
		Prov:      mustTextProv(),
		System:    storyboardSystem,
		Kickoff:   storyboardKickoff(p),
		Tools:     storyboardTools(p, d, script, pack),
		Submit:    "submit_storyboard",
		MaxRounds: storyboardBudget,
		OnStep:    manifestStep(p, StationStoryboard, log),
	}
	d.emit(p.ID, "stage", "prep/storyboard", "running")
	if err := runTracked(ctx, p, "prep.storyboard", "准备③分镜", loop); err != nil {
		p.Manifest("prep.storyboard_error", pipeline.FirstLine(err.Error(), 200))
		return fmt.Errorf("分镜工位: %w", err)
	}
	p.Manifest("prep.storyboard_saved", "工件 C 落盘")
	return nil
}

func mustTextProv() react.ChatClient {
	// ③纯文本模型。
	prov, _ := llm.FromEnv(llm.StationPrepStoryboard)
	return prov
}

func storyboardKickoff(p *pipeline.Project) string {
	k := "开工。用 get_script 与 get_style_package 读取材料，然后用 get_materials 看有没有必须出现的真图素材，再切段。"
	if notes := noteFile(p, "input/storyboard_notes.txt"); notes != "" {
		k += "\n用户对分镜的修改要求（优先满足）：" + notes
	}
	return k
}

func storyboardTools(p *pipeline.Project, d Deps, script *contract.Script, pack *contract.StylePack) []Tool {
	return []Tool{
		{
			Def: toolSchema("get_script", "读取文字稿（句编号 N1..Nn）。切段只能引用句编号。", map[string]any{
				"type": "object", "properties": map[string]any{},
			}),
			Run: func(context.Context, string) Observation {
				return Observation{Text: formatScriptForPeers(script)}
			},
		},
		{
			Def: toolSchema("get_style_package", "读取风格包：风格描述（视觉语境）、剪辑语法（切分密度/段长带/同舞台延续）、视觉词汇摘要（配色/字体/动效/入场收尾模板）。", map[string]any{
				"type": "object", "properties": map[string]any{},
			}),
			Run: func(context.Context, string) Observation {
				return Observation{Text: formatStylePackForStoryboard(p, pack)}
			},
		},
		{
			Def: toolSchema("get_materials", "读取项目上传的真图素材清单（实拍图/截图/官方图表），带「必需」标记与内容说明。必需素材必须被某一镜认领，否则提交被拒。", map[string]any{
				"type": "object", "properties": map[string]any{},
			}),
			Run: func(context.Context, string) Observation {
				return Observation{Text: formatImageMaterialsForPeer(loadImageMaterials(p))}
			},
		},
		{
			Def: toolSchema("estimate_speech_duration", "估算文本口播时长（秒，约 4.5 字/秒）。用来核每段引用句的时长是否落在段长带内。", map[string]any{
				"type": "object",
				"properties": map[string]any{
					"text": map[string]any{"type": "string", "description": "要估算的句子串（把引用的原文句子拼进来估）"},
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
				lo, hi, _ := pack.Grammar.ClampedBand()
				return Observation{Text: fmt.Sprintf("约 %.1f 秒（%d 字）；本片段长带 %.1f–%.1f 秒", sec, len([]rune(in.Text)), lo, hi)}
			},
		},
		{
			Def: toolSchema("validate_storyboard", "校验分镜稿草稿：段落铺满、narration_refs 存在且有序且恰好铺满全部句、id/idx 连续、段长带（入场动画预算计入）、key/场景/首末拍非空、每段镜头字段完整。", map[string]any{
				"type":       "object",
				"properties": map[string]any{"draft": storyboardDraftParam()},
				"required":   []string{"draft"},
			}),
			Run: func(_ context.Context, args string) Observation {
				draft, err := parseDraftArg(args)
				if err != nil {
					return Observation{Text: "参数错误：" + err.Error()}
				}
				if err := contract.ValidateStoryboardDraft(draft, script, pack); err != nil {
					return Observation{Text: "未过校验（逐条修完再验）：\n- " + err.Error()}
				}
				if err := contract.ValidateDraftShots(draft); err != nil {
					return Observation{Text: "镜头表未过校验（每段都要有完整 shot）：\n- " + err.Error()}
				}
				req, all := imageFileSets(p)
				if err := contract.ValidateShotImages(draft, req, all); err != nil {
					return Observation{Text: "未过校验（逐条修完再验）：\n- " + err.Error()}
				}
				return Observation{Text: fmt.Sprintf("校验通过：%d 段铺满 %d 句，总时长约 %.0f 秒，镜头 %d/%d，真图 %d 张已认领。可以 submit_storyboard。",
					len(draft.Segments), len(script.Sentences), draft.TotalHint(), draft.ShotGate().Ready, len(draft.Segments), len(contract.UsedImageFiles(draft)))}
			},
		},
		{
			Def: toolSchema("submit_storyboard", "提交分镜稿（唯一出口）= validate + 指纹落盘。", map[string]any{
				"type":       "object",
				"properties": map[string]any{"draft": storyboardDraftParam()},
				"required":   []string{"draft"},
			}),
			Run: func(_ context.Context, args string) Observation {
				draft, err := parseDraftArg(args)
				if err != nil {
					return Observation{Text: "参数错误：" + err.Error()}
				}
				if err := contract.ValidateStoryboardDraft(draft, script, pack); err != nil {
					return Observation{Text: "被拒（先 validate_storyboard 修到零错误）：\n- " + err.Error()}
				}
				if err := contract.ValidateDraftShots(draft); err != nil {
					return Observation{Text: "被拒（镜头表不完整，先 validate_storyboard 修到零错误）：\n- " + err.Error()}
				}
				req, all := imageFileSets(p)
				if err := contract.ValidateShotImages(draft, req, all); err != nil {
					return Observation{Text: "被拒（必需素材没被认领/认领了但画面提示没写，先 validate_storyboard 修到零错误）：\n- " + err.Error()}
				}
				final := contract.NewStoryboardDraft(draft.Plan, draft.World, draft.CastLegend, draft.Paragraphs, draft.Segments)
				summary, err := persistDraft(p, d, final, script, pack)
				if err != nil {
					return Observation{Text: "分镜落盘失败：" + err.Error()}
				}
				return Observation{
					Text: "分镜稿已提交：" + summary + "。工件已落盘，等待打包终审。",
					Done: true,
				}
			},
		},
	}
}

// persistDraft 分镜稿过闸后落盘（老链 submit_storyboard 与统一链 write_storyboard
// 共体）：翻译制作契约 storyboard.json（先落）+ 工件 draft.json（最后落）——
// 两笔之间崩溃不留「站被跳过但制作契约陈旧」的半完成态。返回随观察回喂的摘要。
func persistDraft(p *pipeline.Project, d Deps, final *contract.StoryboardDraft, script *contract.Script, pack *contract.StylePack) (string, error) {
	sb := final.Translate(script, pack)
	if err := contract.ValidateStoryboard(sb); err != nil {
		return "", fmt.Errorf("翻译产物未过制作契约校验（内部错误，请原样重交）：%w", err)
	}
	if err := writeJSONFile(p.Artifact("storyboards/storyboard.json"), sb); err != nil {
		return "", err
	}
	if err := writeJSONFile(draftPath(p), final); err != nil {
		return "", err
	}
	d.emit(p.ID, "stage", "prep/storyboard", fmt.Sprintf("done：%d 段 / %d 段落 / %d 镜 / 约 %.0f 秒",
		len(final.Segments), len(final.Paragraphs), final.ShotGate().Ready, final.TotalHint()))
	return fmt.Sprintf("%d 段铺满 %d 句（%d 个段落，%d 个镜头），总时长约 %.0f 秒",
		len(final.Segments), len(script.Sentences), len(final.Paragraphs), final.ShotGate().Ready, final.TotalHint()), nil
}

// storyboardDraftParam 分镜稿参数 schema（结构化字段格式放工具参数，§5.3）。
func storyboardDraftParam() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"plan":        map[string]any{"type": "string", "description": "一句话分段思路"},
			"world":       map[string]any{"type": "string", "description": "全片世界/舞台设定（幕布、灯光、道具、观众席），各镜复用"},
			"cast_legend": map[string]any{"type": "string", "description": "全片角色表：每个角色的外形/身体/眼睛/性格，跨镜保持一致。没有角色可不写；写了，镜头 cast.who 必须用表内同名"},
			"paragraphs": map[string]any{
				"type":        "array",
				"description": "段落层：按段序连续铺满全部段。每段 {title:一句话主题, segs:[\"seg01\",\"seg02\"]}",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"title": map[string]any{"type": "string"},
						"segs":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					},
					"required": []string{"title", "segs"},
				},
			},
			"segments": map[string]any{
				"type":        "array",
				"description": "段层：id 从 seg01 连续编号，idx 从 1 连续。duration_hint 落在段长带内（入场动画预算计入）。每段同时是这一镜的调度表。",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":  map[string]any{"type": "string"},
						"idx": map[string]any{"type": "integer"},
						"key": map[string]any{"type": "string", "description": "段短标题"},
						"narration_refs": map[string]any{
							"type":        "string",
							"description": "本段旁白的句编号引用，如 \"N3-N4\"（区间）或 \"N3,N4\"（列表）——只引用，不抄文本；全部段合起来必须不重不漏铺满 N1..Nn",
						},
						"visual_brief": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"scene":       map[string]any{"type": "string", "description": "场景规划（用风格包的视觉词汇写）"},
								"first_frame": map[string]any{"type": "string", "description": "首拍：画面如何开场（延续上段则写明接续点）"},
								"last_frame":  map[string]any{"type": "string", "description": "末拍：画面收在哪里"},
							},
							"required": []string{"scene", "first_frame", "last_frame"},
						},
						"shot": map[string]any{
							"type":        "object",
							"description": "这一段的镜头调度（一期：1 镜 = 1 段）",
							"properties": map[string]any{
								"size": map[string]any{
									"type": "string", "enum": contract.ShotSizes,
									"description": "景别：wide 全景 / medium 中景 / close 近景 / overhead 俯拍 / full 字卡满屏",
								},
								"camera":     map[string]any{"type": "string", "description": "机位与运镜：从哪拍到哪、怎么动"},
								"stage":      map[string]any{"type": "string", "description": "舞台/道具/前后景：本镜真实摆什么"},
								"action":     map[string]any{"type": "string", "description": "这一镜必须看得见的戏：谁看谁/谁指/谁退/发生了什么"},
								"dialogue":   map[string]any{"type": "string", "description": "短台词/字卡（≤12 字），没有留空"},
								"transition": map[string]any{"type": "string", "description": "与下一镜的接法（匹配切/白闪/直切/叠化）"},
								"cast": map[string]any{
									"type":        "array",
									"description": "本镜出场角色 0–4 个（who 用角色表同一名字，pose 写身体动作，emotion 写情绪）；没有角色的镜头（空镜/物件/字卡）不写",
									"maxItems":    4,
									"items": map[string]any{
										"type": "object",
										"properties": map[string]any{
											"who":     map[string]any{"type": "string"},
											"pose":    map[string]any{"type": "string"},
											"emotion": map[string]any{"type": "string"},
										},
										"required": []string{"who", "pose"},
									},
								},
								"ref_frame": map[string]any{"type": "string", "description": "参考帧项目内相对路径（可空）"},
								"ref_note":  map[string]any{"type": "string", "description": "参考帧说明（只借构图/光/角色关系）"},
								"images": map[string]any{
									"type":        "array",
									"description": "本镜必须出现在画面上的真图素材（只准引用 get_materials 清单里的文件）。没有就不写。每条「必需」素材至少被一段认领。",
									"items": map[string]any{
										"type": "object",
										"properties": map[string]any{
											"file": map[string]any{"type": "string", "description": "assets/img/xxx.jpg，与素材清单逐字一致"},
											"role": map[string]any{
												"type": "string", "enum": contract.ShotImageRoles,
												"description": "hero 主画面（面积 ≥25%）/ inset 贴图（面积 ≥6%、短边 ≥200px）",
											},
											"at":      map[string]any{"type": "number", "description": "段内出现秒数（0/省略 = 首帧就在）"},
											"hold":    map[string]any{"type": "number", "description": "最短停留秒数"},
											"present": map[string]any{"type": "string", "description": "呈现配方：摆位/裁切/边框/重点描哪"},
										},
										"required": []string{"file", "role"},
									},
								},
							},
							"required": []string{"size", "camera", "stage", "action"},
						},
						"duration_hint": map[string]any{"type": "number", "description": "段长估算秒数（段长带内）"},
					},
					"required": []string{"id", "idx", "key", "narration_refs", "visual_brief", "shot", "duration_hint"},
				},
			},
		},
		"required": []string{"world", "paragraphs", "segments"},
	}
}

// parseDraftArg 从工具参数取 draft 并规整（sentence refs 原样保留给校验器）。
func parseDraftArg(args string) (*contract.StoryboardDraft, error) {
	var in struct {
		Draft *contract.StoryboardDraft `json:"draft"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return nil, err
	}
	if in.Draft == nil {
		return nil, fmt.Errorf("缺 draft")
	}
	d := in.Draft
	for i := range d.Segments {
		d.Segments[i].NarrationRefs = strings.TrimSpace(d.Segments[i].NarrationRefs)
		if sh := d.Segments[i].Shot; sh != nil {
			sh.Normalize()
		}
	}
	return d, nil
}

// formatStylePackForStoryboard ③看到的工件 B：描述 + 剪辑语法 + 视觉词汇摘要。
// ③看不到②的探索过程，只看沉淀的工件（§2：信号走工件，噪音留在过程里）。
func formatStylePackForStoryboard(p *pipeline.Project, pack *contract.StylePack) string {
	var b strings.Builder
	lo, hi, _ := pack.Grammar.ClampedBand()
	fmt.Fprintf(&b, "风格包（工件 B）\n- 风格：%s\n", pack.StyleID)
	fmt.Fprintf(&b, "- 描述：%s\n", pack.Description)
	fmt.Fprintf(&b, "- 剪辑语法：切分密度 %s ｜ 段长带 %.1f–%.1f 秒（全局钳制后 %.1f–%.1f，入场动画预算计入） ｜ 同舞台延续：%s\n",
		pack.Grammar.Density, pack.Grammar.BandMin, pack.Grammar.BandMax, lo, hi, pack.Grammar.Continuity)
	if ds := pack.DesignSystem; ds != nil {
		fmt.Fprintf(&b, "- 视觉方向：%s\n", ds.Direction)
		if ds.Concept != "" {
			fmt.Fprintf(&b, "- 概念：%s\n", ds.Concept)
		}
		if skins := ds.EffectiveSkins(); len(skins) > 0 {
			s := skins[0].Slots
			fmt.Fprintf(&b, "- 主皮肤「%s」：底 %s / 字 %s / 强调 %s\n", skins[0].Name, s.BG, s.TXT, s.Accent)
		}
		if m := ds.Motion; m != nil {
			fmt.Fprintf(&b, "- 动效：%s（进场 %.2fs 落定 %.2fs）——%s\n", m.Ease, m.DurIn, m.DurSettle, m.Notes)
		}
		if ee := ds.EntryExit; ee != nil {
			fmt.Fprintf(&b, "- 标准入场模板：%s\n- 标准收尾模板：%s\n（衔接只写首末拍内容；入场/收尾动画由设计系统模板统一接管，你不写转场）\n", ee.Entry, ee.Exit)
		}
		if n := len(ds.AntiPatterns); n > 0 {
			fmt.Fprintf(&b, "- 负面清单：%d 条（%s…）\n", n, strings.Join(ds.AntiPatterns, "、"))
		}
	}
	return b.String()
}
