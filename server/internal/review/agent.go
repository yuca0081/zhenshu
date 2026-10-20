package review

// agent.go —— 判卷型审核 agent 的三个调用档案（§6.1/附录 A）：
//
//	AuditSegment  段审：锚点帧开场注入 + 六维判卷（回合预算 ≤6）
//	AuditBoundary 边界审：边界短片段 + 末首帧对 + 四查（回合预算 ≤6）
//	AuditFinal    全片终审：12–16 帧格 + 四查项（单轮、无工具）
//
// 系统提示词 = 附录 A 定稿全文；本片期望不预先生成，agent 从 brief+旁白现场推导
//（考卷退役，D5）。机制不进提示词：预算/缓存/路由/刹车都在框架侧。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"webpage-video-agent/server/internal/agentlog"
	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/pipeline"
)

// 审核温度 ≈0（判卷要可复现）；回合预算（§6.1）。
const (
	auditTemp      = 0.1
	segmentBudget  = 6
	boundaryBudget = 6 // 2026-10-17 实测：4 回合下"回合预算耗尽"反复出现（b04/b05 连续两轮），提一档降降级率
	// 终审预算 4：submit 被校验拒收要吃一轮才就地改对——预算 2 时一次拒收就烧光
	// 整个终审触发重跑（重发全部帧格+思考 ≈ 2 万 token/次，2026-10-19 实测 3 连败）。
	finalBudget = 4

	boundaryWindowMin = 1.5 // 边界短片段窗口下限（秒）

	// 取证预算帽（get_frame/get_clip 每审合计）：只剪失控尾巴不拦正常取证
	//（2026-10-19 实测均值段审 3.8 次/边界 7.7 次，无帽极端 14 次）。边界看动画
	// 接续合法地比段审多要点。用完回喂人话提示强制凭已取证据交卷，不拦 submit。
	segEvidenceBudget = 6
	bndEvidenceBudget = 8
)

// Service 审核工位对项目的依赖。Emit 可空。
type Service struct {
	P    *pipeline.Project
	Emit func(projectID, typ, stage, detail string)
}

func (s *Service) emit(id, typ, stage, detail string) {
	if s.Emit != nil {
		s.Emit(id, typ, stage, detail)
	}
}

// SegInput 段审材料（程序装配，不走工具）。
type SegInput struct {
	Seg       contract.Segment
	Sentences [][2]string  // {N几, 句文本} 本段句编号旁白（有序）
	Facts     []string     // 本段相关事实清单条目
	Rules     string       // 设计规则要点（可空）
	Samples   []llm.ImgRef // 样张（URL 注入模型；Path/Label 给「过程」页缩略图）
	Dur       float64      // 段片时长（锚点帧抽取与工具越界判断）
	Round     int          // 1=首审 ≥2=修复轮
	Prior     *Verdict     // 修复轮的既往意见单
	LogPath   string       // 取证留痕 jsonl（可空）
	// AnchorTimes 锚点帧时刻表（程序按句末时刻+首末帧算好，标注随图注入）
	AnchorTimes []AnchorFrame
}

// AnchorFrame 一张锚点帧：时刻 + 人话标注（「此刻刚念完 N3」）。
type AnchorFrame struct {
	T     float64
	Label string
}

// BndInput 边界审材料。
type BndInput struct {
	ID       string // b01…
	Prev     contract.Segment
	Next     contract.Segment
	PrevDur  float64
	NextDur  float64
	EntryDur float64 // 入场/收尾动画预算（窗口 ≥ max(预算, 1.5s)）
	Round    int
	Prior    *Verdict
	LogPath  string
}

// FinalInput 全片终审材料（§7：12–16 帧格，帧上标注「此刻=第几段/段落界」）。
type FinalInput struct {
	Frames     []AnchorFrame // 全片时刻 + 人话标注（帧由 service 从成片抽取）
	Paragraphs []string      // 段落主题列表（可空）
	TotalDur   float64
	Round      int
	LogPath    string
}

// provider 审核模型：RoleVisual 档（多模态），判卷低温。
func auditProvider() (ChatClient, error) {
	prov, err := llm.FromEnv(llm.RoleVisual)
	if err != nil {
		return nil, fmt.Errorf("审核需要 API 模式（LLM_API_KEY）: %w", err)
	}
	// 注入探针通道要持 vidMu：段审/边界审 goroutine 并发调 videoSupported 读它
	// ——包级全局无锁读写是数据竞争（2026-10-19 中-20）
	vidMu.Lock()
	videoProbeProv = prov
	vidMu.Unlock()
	return prov, nil
}

// auditEffort 审核思考档位（LLM_AUDIT_EFFORT，默认 low）：判卷是对照清单打勾，
// high 档的 reasoning 开销比意见单本身贵一个量级（2026-10-19 实测账：审核
// completion 46.5 万/项目，意见单撑死十分之一）。判卷质量不满意改环境变量回
// high 或 disabled，不动代码。
func auditEffort() llm.Effort {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LLM_AUDIT_EFFORT"))) {
	case "high":
		return llm.EffortHigh
	case "disabled":
		return llm.EffortDisabled
	default:
		return llm.EffortLow
	}
}

// withEvidenceBudget 取证预算帽：帽内原样执行，用完不再取证、回喂人话提示
// （凭已取证据交卷；submit 不受帽限）。两个取证工具共享同一计数。
func withEvidenceBudget(t Tool, left *int, total int) Tool {
	base := t.Run
	t.Run = func(ctx context.Context, args string) Observation {
		if *left <= 0 {
			return Observation{Text: fmt.Sprintf("取证额度已用完（本审上限 %d 次）——凭已取证据汇总意见单，调用 submit_verdict。", total)}
		}
		*left--
		return base(ctx, args)
	}
	return t
}

// runAudit Loop 组装 + 取证留痕的共用尾。verdict 由各档案的 submit 工具在
// 校验通过后闭包捕获返回。TaskID 非空时统一挂「过程」页任务账（对话留痕 +
// agent.start/done + token 用量）。
func (s *Service) runAudit(ctx context.Context, prov ChatClient, loop Loop, prof profile, prior *Verdict) (Verdict, error) {
	var verdict Verdict
	loop.Prov = prov
	loop.Temp = auditTemp
	loop.Effort = auditEffort()
	loop.Submit = "submit_verdict"
	submit := toolSchema("submit_verdict",
		"提交结构化意见单（唯一出口；提交前先按流程完成判卷）。载荷不合规范会被整体拒收并回喂错误清单。",
		verdictSchema(prof))
	loop.Tools = append(loop.Tools, Tool{
		Def: submit,
		Run: func(ctx context.Context, args string) Observation {
			var v Verdict
			if err := decodeArgs(args, &v); err != nil {
				return Observation{Text: err.Error()}
			}
			if err := v.Validate(prof, prior); err != nil {
				return Observation{Text: "被拒：" + err.Error()}
			}
			v.AssignIDs()
			verdict = v
			return Observation{Text: "意见单已收下并落盘。", Done: true}
		},
	})
	if loop.LogPath != "" && loop.OnTool == nil {
		_ = os.MkdirAll(filepath.Dir(loop.LogPath), 0o755)
		loop.OnTool = toolLogger(loop.LogPath)
	}
	var runErr error
	if loop.TaskID != "" {
		at := agentlog.Begin(s.P, loop.TaskID, loop.TaskLabel)
		loop.Rec = at.Rec()
		defer func() {
			at.Total(loop.Usage, loop.Calls)
			at.Done(runErr)
		}()
	}
	if runErr = loop.Run(ctx); runErr != nil {
		return Verdict{}, runErr
	}
	return verdict, nil
}

// toolLogger 取证留痕（§4.4：工具+参数+结果指纹落盘可查；追加只写）。
func toolLogger(path string) func(name, args, result string) {
	return func(name, args, result string) {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		defer f.Close()
		line, _ := json.Marshal(map[string]any{
			"name": name, "args": compactJSON(args), "fp": hash8(result),
		})
		f.Write(append(line, '\n'))
	}
}

// getFrameTool / getClipTool 取证工具（段审/边界审共用；targets = 可取的段）。
func (s *Service) getFrameTool(fs *serviceFS, targets map[string]float64) Tool {
	return Tool{Def: toolSchema("get_frame",
		"取指定段的指定时刻单帧（画面证据）。t 为该段内秒数。",
		map[string]any{
			"type": "object", "additionalProperties": false,
			"required": []string{"seg", "t"},
			"properties": map[string]any{
				"seg": map[string]any{"type": "string", "enum": mapKeys(targets)},
				"t":   map[string]any{"type": "number"},
			},
		},
	),
		Run: func(ctx context.Context, args string) Observation {
			var in struct {
				Seg string  `json:"seg"`
				T   float64 `json:"t"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return Observation{Text: err.Error()}
			}
			dur, ok := targets[in.Seg]
			if !ok {
				return Observation{Text: "被拒：seg 只准 " + strings.Join(mapKeys(targets), "/")}
			}
			url, _, err := fs.extractFrame(ctx, in.Seg, in.T, dur)
			if err != nil {
				return Observation{Text: err.Error()}
			}
			return Observation{Text: fmt.Sprintf("已取回 %s t=%.2fs 的帧（附图）。先核实再下结论。", in.Seg, in.T), Images: []string{url}}
		},
	}
}

func (s *Service) getClipTool(fs *serviceFS, targets map[string]float64) Tool {
	return Tool{Def: toolSchema("get_clip",
		"取指定段的连续短片段（t1–t2，段内秒，跨度 ≤2.5s，超限截断）。看连续动画/边界过程用。",
		map[string]any{
			"type": "object", "additionalProperties": false,
			"required": []string{"seg", "t1", "t2"},
			"properties": map[string]any{
				"seg": map[string]any{"type": "string", "enum": mapKeys(targets)},
				"t1":  map[string]any{"type": "number"},
				"t2":  map[string]any{"type": "number"},
			},
		},
	),
		Run: func(ctx context.Context, args string) Observation {
			var in struct {
				Seg string  `json:"seg"`
				T1  float64 `json:"t1"`
				T2  float64 `json:"t2"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return Observation{Text: err.Error()}
			}
			dur, ok := targets[in.Seg]
			if !ok {
				return Observation{Text: "被拒：seg 只准 " + strings.Join(mapKeys(targets), "/")}
			}
			text, imgs, err := fs.extractClip(ctx, in.Seg, in.T1, in.T2, dur)
			if err != nil {
				return Observation{Text: err.Error()}
			}
			return Observation{Text: text, Images: imgs}
		},
	}
}

func mapKeys(m map[string]float64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ── 段审（附录 A.1）───────────────────────────────────────────────

const segmentSystem = `你是成片审片员，对本段成片判卷，产出结构化意见单。

## 输入材料（开场已注入）
1. 分镜 brief——场景、首拍、末拍（本段画面承诺）
2. 句编号旁白——N1…Nn（本段内容承诺）
3. 样张——本片风格的实物基准
4. 设计规则要点——贯穿装置与皮肤切换规则（可能为空）
5. 事实清单——硬事实与出处
6. 锚点帧——每帧标注「此刻刚念完 N 几」

## 判卷流程
1. 先读 brief 与旁白，列出本段承诺了什么：该出现什么、每句承载什么画面。
2. 逐帧对照，按六维过一遍，每维给出 pass / fail / NA。
3. 锚点帧不够判断时：get_frame 取指定时刻的帧，get_clip 看连续片段。先核实，再下结论；拿不准的不写。
4. 汇总意见单，调用 submit_verdict。

## 六维判定
| # | 维度 | 判什么 | 记 fail | 记 NA |
|---|---|---|---|---|
| ① | 内容一致 | 承诺的元素是否出现、旁白说到的是否画了 | 承诺没画、画错（多画不算错） | 无 |
| ② | 事实 | 画面的数字、年份、专名与旁白及事实清单是否一致 | 出现且冲突 | 画面未出现数字/专名 |
| ③ | 风格 | 质感、色彩、字体、装饰密度像不像样张 | 明显出戏 | 无 |
| ④ | 首末拍合规 | 首帧按 brief 首拍开场、末帧收在 brief 末拍 | 开场或收尾与承诺不符 | 无 |
| ⑤ | 装置落点 | 贯穿装置/皮肤切换符合设计规则 | 违反规则或漏落点 | 设计规则为空 |
| ⑥ | 字幕文字 | 字幕与画面文字无乱码、缺字、错字、断句错位 | 出现任一 | 画面无文字 |

## 意见条目
每条 fail 写一条 finding：
- category：六维之一（content / fact / style / first_last / device / subtitle）
- anchor：句号（N 几）或帧时刻（t=秒）
- severity：high = 观众直接察觉的硬伤——错字、数字/专名错、承诺元素缺失或画错、文字不可读、字幕错配。low = 挑剔级瑕疵：元素已出现且方向正确，仅位置/大小/配色/数量/幅度与 brief 有出入，或动画细节与 brief 文字描述不完全一致（brief 的首拍/末拍是意图描述不是像素规格）——一律 low。只有 high 会触发打回；每次误标 high 都可能让整条制作多烧一轮重写，标 high 先过这条门槛。
- evidence：帧时刻 + 现状 vs 期望，一句话。
- fix：redraw（需重画）/ pass（记录不修）。

## 输出样例
{
  "dims": {
    "content": "pass", "fact": "fail", "style": "pass",
    "first_last": "pass", "device": "NA", "subtitle": "pass"
  },
  "findings": [
    { "category": "fact", "anchor": "N3", "severity": "high",
      "evidence": "帧 t=12.4s：画面标题写 1985，N3 旁白为 1984",
      "fix": "redraw" },
    { "category": "subtitle", "anchor": "t=8.0", "severity": "low",
      "evidence": "帧 t=8.0s：字幕「绿素」缺字显示为「绿 x」",
      "fix": "pass" }
  ]
}

## 修复轮
输入会附上一轮意见单。先对每条既往意见逐条复核（reassessment：fixed / still_broken，still_broken 需附 evidence），之后才考虑新发现——新发现必须证据确凿才立案，不追加数量。修复轮载荷在同层多 reassessment 数组，findings 只收新发现。

## 输出
完成判卷即调用 submit_verdict；除工具调用外不要输出结论文本。`

// AuditSegment 段审。返回（意见单, error）；审查器自身故障（模型失败/预算烧尽）
// 由调用方按降级哲学处理。
func (s *Service) AuditSegment(ctx context.Context, in SegInput) (Verdict, error) {
	prov, err := auditProvider()
	if err != nil {
		return Verdict{}, err
	}
	fs := &serviceFS{
		segVideo: func(segID string) string { return s.P.Artifact("renders/segs/" + segID + ".mp4") },
		cacheDir: s.P.Artifact("reports/audit"),
		relBase:  "reports/audit",
	}
	targets := map[string]float64{in.Seg.ID: in.Dur}
	// 锚点帧（程序装配）：首帧 + 句末帧（封顶均匀采样）+ 末帧，标注随文注入
	var kick strings.Builder
	fmt.Fprintf(&kick, "判卷对象：段 %s「%s」（段长 %.2fs，第 %d 轮审核）。\n\n", in.Seg.ID, in.Seg.Key, in.Dur, in.Round)
	kick.WriteString("## 1. 分镜 brief（本段画面承诺）\n" + strings.TrimSpace(in.Seg.VisualBrief) + "\n\n")
	kick.WriteString("## 2. 句编号旁白（本段内容承诺）\n")
	for _, sn := range in.Sentences {
		fmt.Fprintf(&kick, "%s：%s\n", sn[0], sn[1])
	}
	kick.WriteString("\n## 3. 样张")
	if len(in.Samples) > 0 {
		kick.WriteString("（风格实物基准，见锚点帧之后的附图）\n")
	} else if in.Round > 1 {
		// 重审不附样张（2026-10-19 成本刀）：首轮已按样张判过风格，重审聚焦修复
		// 复核；风格基准以上一轮意见单的判词为准（随 ## 上一轮意见单 注入）。
		kick.WriteString("：本轮不附图（首轮已按样张判过风格，以上一轮意见单的判词为基准）\n")
	} else {
		kick.WriteString("：（本片未配置样张——风格维按设计规则判）\n")
	}
	if in.Rules != "" {
		kick.WriteString("\n## 4. 设计规则要点\n" + in.Rules + "\n")
	} else {
		kick.WriteString("\n## 4. 设计规则要点：（空——装置落点按 NA 判）\n")
	}
	if len(in.Facts) > 0 {
		kick.WriteString("\n## 5. 事实清单（本段相关条目）\n")
		for _, f := range in.Facts {
			kick.WriteString("- " + f + "\n")
		}
	} else {
		kick.WriteString("\n## 5. 事实清单：（本段无登记条目——事实维按画面与旁白互证）\n")
	}
	kick.WriteString("\n## 6. 锚点帧\n")
	// 抽帧并行（2026-10-19 中-24）：帧间完全独立，每帧一次 ffmpeg 进程串行跑
	// 白付进程起降；并发 4 抽完原序拼接（缓存键含 t，无写冲突）
	imgs := make([]string, len(in.AnchorTimes))
	refs := make([]llm.ImgRef, len(in.AnchorTimes))
	if err := parallelFrames(len(in.AnchorTimes), func(i int) error {
		u, rel, err := fs.extractFrame(ctx, in.Seg.ID, in.AnchorTimes[i].T, in.Dur)
		if err != nil {
			return err
		}
		imgs[i] = u
		refs[i] = llm.ImgRef{URL: u, Path: rel, Label: in.AnchorTimes[i].Label}
		return nil
	}); err != nil {
		return Verdict{}, fmt.Errorf("锚点帧抽取失败: %w", err)
	}
	for i, a := range in.AnchorTimes {
		fmt.Fprintf(&kick, "图%d = %s（t=%.2fs）\n", i+1, a.Label, a.T)
	}
	for i, smp := range in.Samples {
		imgs = append(imgs, smp.URL)
		refs = append(refs, smp)
		fmt.Fprintf(&kick, "图%d = 样张%d（风格实物基准）\n", len(in.AnchorTimes)+i+1, i+1)
	}
	if in.Prior != nil {
		b, _ := json.MarshalIndent(in.Prior, "", "  ")
		kick.WriteString("\n## 上一轮意见单（修复轮：逐条 reassessment 后才考虑新发现）\n```json\n" + string(b) + "\n```\n")
	}
	evLeft := segEvidenceBudget
	loop := Loop{
		System:      segmentSystem,
		Kickoff:     kick.String(),
		KickoffIm:   imgs,
		KickoffRefs: refs,
		Tools: []Tool{
			withEvidenceBudget(s.getFrameTool(fs, targets), &evLeft, segEvidenceBudget),
			withEvidenceBudget(s.getClipTool(fs, targets), &evLeft, segEvidenceBudget),
		},
		MaxRounds: segmentBudget,
		LogPath:   in.LogPath,
		TaskID:    fmt.Sprintf("seg.audit.%s.r%d", in.Seg.ID, in.Round),
		TaskLabel: fmt.Sprintf("段审 %s（第 %d 轮）", in.Seg.ID, in.Round),
	}
	s.emit(s.P.ID, "progress", "review", fmt.Sprintf("段审 %s（第 %d 轮）开工", in.Seg.ID, in.Round))
	v, err := s.runAudit(ctx, prov, loop, profSegment, in.Prior)
	if err == nil {
		s.emit(s.P.ID, "progress", "review", fmt.Sprintf("段审 %s 完成：%d 条意见（%d high）", in.Seg.ID, len(v.Findings), len(v.HighFindings())))
	}
	return v, err
}

// ── 边界审（附录 A.2）─────────────────────────────────────────────

const boundarySystem = `你是成片审片员，对相邻两段的拼接边界判卷，产出结构化意见单。

## 输入材料（开场已注入）
1. 前段末拍承诺（brief）
2. 后段首拍承诺（brief）
3. 前段收尾短片段（最后 1–2 秒）
4. 后段入场短片段（最前 1–2 秒）
5. 末帧×首帧对（物理拼接点）

## 判卷流程
1. 读两条承诺，明确这一边界应该是什么样：延续（末帧状态 = 首帧状态）还是新场景（干净开场）。
2. 按四查过一遍，每查给出 pass / fail / NA。
3. 短片段不够判断时：get_clip 取更早或更晚的连续画面核实。先核实，再下结论；拿不准的不写。
4. 汇总意见单，调用 submit_verdict。

## 四查判定
| # | 查什么 | 记 fail | 记 NA |
|---|---|---|---|
| ① | 收尾播完 | 前段收尾动画停在半途被掐断（淡出没走完、元素收一半） | 前段无收尾动画 |
| ② | 入场起步 | 后段入场动画卡死、跳帧、长时间黑屏 | 后段无入场动画 |
| ③ | 接续成立 | 承诺延续而末帧与首帧对不上（构图/物体位置/颜色跳变）；承诺新场景而后段开场带前段残影 | 两侧无相邻关系承诺且各自完整 |
| ④ | 突变 | 闪帧、黑帧、亮度色量突跳 | 无 |

## 意见条目
每条 fail 写一条 finding：
- category：四查之一（outro_done / intro_started / continuity / glitch）
- anchor：边界号 + 相对拼接点的时刻（如 b02|t=-0.2，负 = 前段内）
- severity：high = 观众直接察觉（黑帧、明显跳变、动画半途截断）；low = 挑剔级。只有 high 触发处理。
- evidence：时刻 + 现状 vs 期望，一句话。
- fix：stitch_fix（拼接参数能修：字幕时序、淡入淡出参数）/ redraw（画面或动画本身错）/ pass。能用拼接参数修的，不要判 redraw。

## 输出样例
{
  "dims": {
    "outro_done": "pass", "intro_started": "pass",
    "continuity": "fail", "glitch": "pass"
  },
  "findings": [
    { "category": "continuity", "anchor": "b02|t=-0.2", "severity": "high",
      "evidence": "前段末帧 t=6.8 推近终点停在画面中心，后段首帧推近起点在左 1/3——接续点不吻合",
      "fix": "redraw" },
    { "category": "glitch", "anchor": "b02|t=0.3", "severity": "low",
      "evidence": "入场第 0.3s 单帧亮度突跳",
      "fix": "pass" }
  ]
}

## 修复轮
输入会附上一轮意见单。先对每条既往意见逐条复核（reassessment：fixed / still_broken），之后才考虑新发现——新发现必须证据确凿才立案，不追加数量。

## 输出
完成即调用 submit_verdict；除工具调用外不要输出结论文本。`

// AuditBoundary 边界审。窗口 = max(入场/收尾动画预算, 1.5s)。
func (s *Service) AuditBoundary(ctx context.Context, in BndInput) (Verdict, error) {
	prov, err := auditProvider()
	if err != nil {
		return Verdict{}, err
	}
	fs := &serviceFS{
		segVideo: func(segID string) string { return s.P.Artifact("renders/segs/" + segID + ".mp4") },
		cacheDir: s.P.Artifact("reports/audit"),
		relBase:  "reports/audit",
	}
	targets := map[string]float64{in.Prev.ID: in.PrevDur, in.Next.ID: in.NextDur}
	win := in.EntryDur
	if win < boundaryWindowMin {
		win = boundaryWindowMin
	}
	if win > clipMaxSpan {
		win = clipMaxSpan
	}
	var kick strings.Builder
	fmt.Fprintf(&kick, "判卷对象：边界 %s（前段 %s「%s」× 后段 %s「%s」，第 %d 轮审核）。\n\n",
		in.ID, in.Prev.ID, in.Prev.Key, in.Next.ID, in.Next.Key, in.Round)
	kick.WriteString("## 1. 前段末拍承诺（brief）\n" + briefOf(in.Prev, "末拍") + "\n\n")
	kick.WriteString("## 2. 后段首拍承诺（brief）\n" + briefOf(in.Next, "首拍") + "\n\n")
	kick.WriteString(fmt.Sprintf("## 3. 边界材料（附图）\n- 前段收尾短片段：最后 %.2fs（t=%.2f–%.2f）\n- 后段入场短片段：最前 %.2fs（t=0–%.2f）\n- 末帧×首帧对：前段最后一帧 vs 后段第一帧（物理拼接点）\n\n",
		win, in.PrevDur-win, in.PrevDur, win, win))
	var imgs []string
	text, cimgs, err := fs.extractClip(ctx, in.Prev.ID, in.PrevDur-win, in.PrevDur, in.PrevDur)
	if err != nil {
		return Verdict{}, fmt.Errorf("前段收尾片段抽取失败: %w", err)
	}
	imgs = append(imgs, cimgs...)
	kick.WriteString(text + "\n")
	text, cimgs, err = fs.extractClip(ctx, in.Next.ID, 0, win, in.NextDur)
	if err != nil {
		return Verdict{}, fmt.Errorf("后段入场片段抽取失败: %w", err)
	}
	imgs = append(imgs, cimgs...)
	kick.WriteString(text + "\n")
	// 末帧×首帧对（附路径引用：「过程」页缩略图；片段视频是 mp4 dataURL 不进引用）
	last, lastRel, err := fs.extractFrame(ctx, in.Prev.ID, in.PrevDur-0.04, in.PrevDur)
	if err != nil {
		return Verdict{}, fmt.Errorf("前段末帧抽取失败: %w", err)
	}
	first, firstRel, err := fs.extractFrame(ctx, in.Next.ID, 0.0, in.NextDur)
	if err != nil {
		return Verdict{}, fmt.Errorf("后段首帧抽取失败: %w", err)
	}
	imgs = append(imgs, last, first)
	refs := []llm.ImgRef{
		{URL: last, Path: lastRel, Label: "末帧（" + in.Prev.ID + " 最后一帧）"},
		{URL: first, Path: firstRel, Label: "首帧（" + in.Next.ID + " 第一帧）"},
	}
	kick.WriteString("最后两张附图 = 末帧（前段最后一帧）× 首帧（后段第一帧），紧挨着的就是物理拼接点。\n")
	if in.Prior != nil {
		b, _ := json.MarshalIndent(in.Prior, "", "  ")
		kick.WriteString("\n## 上一轮意见单（修复轮：逐条 reassessment 后才考虑新发现）\n```json\n" + string(b) + "\n```\n")
	}
	bndLeft := bndEvidenceBudget
	loop := Loop{
		System:      boundarySystem,
		Kickoff:     kick.String(),
		KickoffIm:   imgs,
		KickoffRefs: refs,
		Tools: []Tool{
			withEvidenceBudget(s.getFrameTool(fs, targets), &bndLeft, bndEvidenceBudget),
			withEvidenceBudget(s.getClipTool(fs, targets), &bndLeft, bndEvidenceBudget),
		},
		MaxRounds: boundaryBudget,
		LogPath:   in.LogPath,
		TaskID:    fmt.Sprintf("seg.boundary.%s", in.ID),
		TaskLabel: fmt.Sprintf("边界审 %s", in.ID),
	}
	s.emit(s.P.ID, "progress", "review", fmt.Sprintf("边界审 %s（第 %d 轮）开工", in.ID, in.Round))
	return s.runAudit(ctx, prov, loop, profBoundary, in.Prior)
}

// briefOf 从段的 VisualBrief（场景\n首拍：…\n末拍：…拼装文本）里取单侧承诺。
func briefOf(seg contract.Segment, which string) string {
	for _, line := range strings.Split(seg.VisualBrief, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, which+"：") {
			return strings.TrimPrefix(line, which+"：")
		}
	}
	return seg.VisualBrief // 旧分镜无结构化行：整段文本兜底
}

// ── 全片终审（§7，附录 A.3 同模板）───────────────────────────────

const finalSystem = `你是成片审片员，对整部成片判卷，产出结构化意见单。你是全片视角的唯一一道兜底：逐段审看不见的段间渐变跑偏（drift），在这里必须判。

## 输入材料（开场已注入）
1. 帧格 12–16 张——每帧标注「此刻=第几段/段落界」（t 为全片秒）
2. 段落主题列表（全片结构）

## 判卷流程
1. 先读段落主题列表，明确全片结构：几章、每段该是什么气质。
2. 逐帧过一遍四查项。
3. 段间对照看 drift：相邻段的色彩/字体/质感/装饰密度是否在同一部片里（渐变跑偏=每段都及格但连起来像换了片子）。
4. 汇总意见单，调用 submit_verdict。无工具：以注入帧格为准，拿不准的不写。

## 查项
| # | 查什么 | 记 fail |
|---|---|---|
| ① | pacing | 节奏明显失衡：连续多段无信息增量地拖、或关键段落被切得过快读不完 |
| ② | deadair | 单点长时间无信息空转：某一帧附近画面长时间静止且旁白也无推进 |
| ③ | stitch | 段序错、黑帧、音画错位、拼接点闪跳 |
| ④ | drift | 段间风格渐变：相邻段风格要素（色系/字体/质感/密度）像两部片 |

## 意见条目
每条 fail 写一条 finding：
- category：四查之一（pacing / deadair / stitch / drift）
- anchor：t=全片秒（问题最明显的时刻；drift 写在跑偏开始的边界时刻）
- severity：high = 观众直接察觉；low = 挑剔级。只有 high 会触发重渲。
- evidence：时刻 + 现状 vs 期望，一句话。

## 输出样例
{
  "findings": [
    { "category": "drift", "anchor": "t=83.5", "severity": "high",
      "evidence": "第4段起底色从米纸面跳到深霓虹且再没回来——与前3段像两部片" }
  ]
}

## 输出
完成判卷即调用 submit_verdict；除工具调用外不要输出结论文本。`

// AuditFinal 全片终审（单轮、无工具、无 fix）。
func (s *Service) AuditFinal(ctx context.Context, in FinalInput) (Verdict, error) {
	prov, err := auditProvider()
	if err != nil {
		return Verdict{}, err
	}
	var kick strings.Builder
	fmt.Fprintf(&kick, "判卷对象：整部成片（总长 %.2fs，第 %d 轮终审）。\n\n", in.TotalDur, in.Round)
	if len(in.Paragraphs) > 0 {
		kick.WriteString("## 段落主题\n")
		for i, p := range in.Paragraphs {
			fmt.Fprintf(&kick, "%d. %s\n", i+1, p)
		}
		kick.WriteString("\n")
	}
	kick.WriteString("## 帧格（附图，按时间正序）\n")
	var imgs []string
	for i, f := range in.Frames {
		fmt.Fprintf(&kick, "图%d = %s（t=%.2fs）\n", i+1, f.Label, f.T)
	}
	// 终审 12–16 帧并行抽（2026-10-19 中-24）：每帧一次 ffmpeg 进程，串行是
	// 分钟级白等；并发 4 原序拼接。serviceFS 提到循环外共用（旧版每帧新建）。
	ffFS := &serviceFS{
		segVideo: func(string) string { return s.P.Artifact("renders/main.mp4") },
		cacheDir: s.P.Artifact("reports/audit"),
		relBase:  "reports/audit",
	}
	imgs = make([]string, len(in.Frames))
	refs := make([]llm.ImgRef, len(in.Frames))
	if err := parallelFrames(len(in.Frames), func(i int) error {
		u, rel, err := ffFS.extractFrame(ctx, "film", in.Frames[i].T, 0)
		if err != nil {
			return fmt.Errorf("t=%.2f: %w", in.Frames[i].T, err)
		}
		imgs[i] = u
		refs[i] = llm.ImgRef{URL: u, Path: rel, Label: in.Frames[i].Label}
		return nil
	}); err != nil {
		return Verdict{}, fmt.Errorf("终审抽帧失败: %w", err)
	}
	loop := Loop{
		System:      finalSystem,
		Kickoff:     kick.String(),
		KickoffIm:   imgs,
		KickoffRefs: refs,
		MaxRounds:   finalBudget,
		LogPath:     in.LogPath,
		TaskID:      fmt.Sprintf("final.audit.r%d", in.Round),
		TaskLabel:   fmt.Sprintf("全片终审（第 %d 轮）", in.Round),
	}
	s.emit(s.P.ID, "progress", "review", "全片终审开工")
	return s.runAudit(ctx, prov, loop, profFinal, nil)
}

// parallelFrames 并发抽帧的小骨架：n 项独立作业、并发 4、任一失败取首个错误、
// 结果由调用方按下标写入（键互不相同，无写冲突）。抽帧互相独立，串行跑只是
// 白付每帧一次的 ffmpeg 进程起降（2026-10-19 中-24）。
func parallelFrames(n int, fn func(i int) error) error {
	if n <= 0 {
		return nil
	}
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	sem := make(chan struct{}, 4)
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := fn(i); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	return firstErr
}
