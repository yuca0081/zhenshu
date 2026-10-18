package produce

// 渲染前画面预审（run() 的 4.5 步）：check 过后、渲染之前，hyperframes snapshot 在
// 主组合上一次性给每段截 3 帧（开场后/中段/落定前），视觉模型按 frameQA 同款考卷
// 判分；有 high 问题的段把 issues 回喂 sceneGen 重出场景文件（≤2 轮），全部过关才
// 进渲染——画面问题拦在最贵的一步之前，返工不再发生在「渲完整片之后」。
// 修复轮只复查被修段（没动的段帧没变），第 2 次修复仍挂升回全量兜底（P4 收敛）。
//
// 与渲后 frameQA 的分工（run() 接线）：预审干净且风格无动态特色判据 → 跳过全量
// frameQA；预审动过的段 → 渲后只复查这些段；pre_review=false 或前置缺失 → 渲后
// 维持全量现状。rework 路径不接预审（段级重做本来就只渲 1-2 段）。
//
// 已知取舍（记在这里不藏实现里）：
//   - snapshot 时刻最早 0.5s（段首 0.25s 黑场渐变内截帧无意义），frameQA 的 t=0
//     首帧穿帮硬闸覆盖略弱——0.5s 帧仍能抓「晚出元素提前出现」；尾帧退场检查
//     （-0.08s）预审做不了，交渲后复查兜底。
//   - 修复轮自身不显式重跑 hyperframes check，但重出的段帧 mtime 已变，随后
//     run() 里 Run("render") 隐式跑的 stageCheck 会因 check 戳失效而真检（回渲
//     前的最后一道闸），layout 回归由它与渲后复查兜住（动过的段必在复查名单里）。
//   - 转场/运镜类特色判据的证据是渲后段片边界帧序列，预审截不出 → 过滤出预审
//     考卷，这些判据交渲后 frameQA（ReviewAll）。
//
// 非阻塞哲学同 frameQA：审查类基础设施故障（snapshot 失败/视觉调用失败/场景重出
// 失败）降级放行不挡制作；2 轮修不好也放行，标红记账交渲后复查与用户。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	openai "github.com/sashabaranov/go-openai"

	"webpage-video-agent/server/internal/agentlog"
	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/pipeline"
)

// preReviewOn 渲染前预审开关：project.json "pre_review"（*bool；缺省=开，false=
// 整段跳过、渲后维持全量 frameQA 现状——与 sfxFor 同款显式落值语义）。
func preReviewOn(p *pipeline.Project) bool {
	b, err := os.ReadFile(p.Artifact("project.json"))
	if err != nil {
		return true
	}
	var meta struct {
		PreReview *bool `json:"pre_review"`
	}
	if json.Unmarshal(b, &meta) != nil || meta.PreReview == nil {
		return true
	}
	return *meta.PreReview
}

// preReviewTimes 每段取 0.5s/中点/尾-0.5s 三个全局时刻（避开段首尾 0.25s 黑场渐变；
// 口径同 sceneReplayCheck 的累计法，但覆盖全部段而非只 scene 段）。缺旁白时长或缺
// 场景文件的段跳过（acc 照加，时刻轴不漂）。
func preReviewTimes(p *pipeline.Project, sb *contract.Storyboard, voices map[string]voiceMeta) (ats []float64, t2seg map[float64]string) {
	t2seg = map[float64]string{}
	seen := map[float64]bool{}
	acc := 0.0
	for _, seg := range sb.Segments {
		dur := voices[seg.ID].DurationS + contract.SegTail
		ok := dur > contract.SegTail && existsFile(p.Artifact("compositions/frames/"+seg.ID+".html"))
		if ok {
			for _, t := range []float64{0.5, dur / 2, dur - 0.5} {
				if t < 0.25 { // 段首 0.25s 黑场渐变内不取帧（极短段防御；正常段 8-16s 不会命中）
					continue
				}
				at := acc + t
				if at > acc+dur-0.05 { // 极短段：钳到段尾渐变区前
					at = acc + dur - 0.05
				}
				key := round2(at)
				if seen[key] { // 极短段三点重合成一点：去重，--at 不喂重复值
					continue
				}
				seen[key] = true
				ats = append(ats, key)
				t2seg[key] = seg.ID
			}
		}
		acc += dur
	}
	return ats, t2seg
}

// preReviewResult 预审结果 → run() 据此决定渲后 frameQA 的范围。
type preReviewResult struct {
	Repaired  []string // 预审重出过场景文件的段（渲后复查名单）
	ReviewAll bool     // 渲后仍需全量 frameQA：风格带转场/运镜类特色判据（证据=渲后段片边界帧，预审截不出）
	Ran       bool     // 预审实际跑了；false=开关关/前置缺失，渲后维持全量现状
}

// preReview 渲染前画面预审。err 只在 ctx 取消或结构性失败（compositions 落盘/组装
// ——渲染必然同样失败，拦在渲染前如实上抛）时非 nil；审查类故障一律降级放行。
func (r *Runner) preReview(ctx context.Context, p *pipeline.Project) (preReviewResult, error) {
	var res preReviewResult
	if !preReviewOn(p) {
		r.Manifest(p, "prereview.off", "")
		return res, nil
	}
	sb, err := p.LoadStoryboard()
	if err != nil {
		r.Manifest(p, "prereview.skip", "无分镜: "+err.Error())
		return res, nil
	}
	voices, err := loadAudioMeta(p)
	if err != nil {
		r.Manifest(p, "prereview.skip", "缺 audio_meta.json: "+err.Error())
		return res, nil
	}
	ats, t2seg := preReviewTimes(p, sb, voices)
	if len(ats) == 0 {
		r.Manifest(p, "prereview.skip", "无可审段")
		return res, nil
	}
	// 转场/运镜判据的证据（渲后段片边界帧序列）预审截不出 → 留给渲后 frameQA
	crits := signatureCriteria(pipeline.LoadStyleBrief(p), designOf(p))
	res.ReviewAll = hasDynamicCrit(crits)
	preCrits := staticCrits(crits)
	res.Ran = true

	id := p.ID
	r.emit(id, "stage", "prereview", "running")
	r.Manifest(p, "prereview.start", fmt.Sprintf("%d 段 × %d 时刻", segCount(t2seg), len(ats)))
	repairedSet := map[string]bool{}
	// 审查范围（P4 收敛）：round0 全量；修复轮只审 sceneGen 成功的段——没动的
	// 段帧一字未变，全量重审是纯浪费（实测一轮 ~2.5 分钟，预审占总时长 43% 的
	// 主因）；第 2 次修复仍挂时升回全量——连续局部失败提示可能有系统性问题，
	// 放行渲染前给一次全景（也是修复上限前最后一场）。
	pending := map[string]bool{}
	for _, seg := range sb.Segments {
		pending[seg.ID] = true
	}
	prevIssues := map[string]string{} // 段 → 上轮问题清单（修复回喂带上，防修 B 破 A 又震荡回去）
	for round := 0; ; round++ {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		if round > 0 { // round0 起手已是全量
			pending = roundScope(round, segIDsOf(sb), repairedSet)
		}
		if len(pending) == 0 { // 修复轮 sceneGen 全失败：帧没变没得重审，按未收敛处理交渲后复查
			r.emit(id, "stage", "prereview", "error: 修复重出全部失败（上一轮问题未收敛）")
			r.Manifest(p, "prereview.failed", "修复重出全部失败，明细见 prereview.issues")
			res.Repaired = sortedSegIDs(repairedSet)
			return res, nil
		}
		scope := sortedSegIDs(pending)
		r.Manifest(p, "prereview.round", fmt.Sprintf("round%d 审 %d 段（%s）", round+1, len(scope), strings.Join(scope, ",")))
		atArg := make([]string, 0, len(ats))
		for _, t := range ats {
			if pending[t2seg[t]] {
				atArg = append(atArg, fmt.Sprintf("%.2f", t))
			}
		}
		joined := strings.Join(atArg, ",")
		dir := fmt.Sprintf("reports/prereview/round%d", round+1)
		frames, serr := r.snapFrames(p, joined, dir)
		if serr != nil {
			r.emit(id, "stage", "prereview", "snapshot 失败，预审降级放行")
			r.Manifest(p, "prereview.snapshot_error", pipeline.FirstLine(serr.Error(), 160))
			res.Repaired = sortedSegIDs(repairedSet)
			return res, nil
		}
		failed, ferr := r.reviewSnapshots(ctx, p, sb, voices, groupSnapFrames(p, dir, frames, t2seg), preCrits, pending)
		if ferr != nil {
			if ctx.Err() != nil {
				return res, ctx.Err()
			}
			r.emit(id, "stage", "prereview", "审查调用失败，预审降级放行")
			r.Manifest(p, "prereview.review_error", pipeline.FirstLine(ferr.Error(), 160))
			res.Repaired = sortedSegIDs(repairedSet)
			return res, nil
		}
		if len(failed) == 0 {
			r.emit(id, "stage", "prereview", "done")
			r.Manifest(p, "prereview.pass", fmt.Sprintf("round%d 全过", round+1))
			res.Repaired = sortedSegIDs(repairedSet)
			return res, nil
		}
		if round >= 2 { // 修复上限：不阻塞渲染，标红交渲后复查与用户（与 frameQA 同哲学）
			summary := failSummary(failed)
			r.emit(id, "stage", "prereview", "error: "+summary)
			r.Manifest(p, "prereview.failed", summary)
			res.Repaired = sortedSegIDs(repairedSet)
			return res, nil
		}
		r.emit(id, "progress", "prereview",
			fmt.Sprintf("%d 段预审未过，重出画面（第 %d 轮）", len(failed), round+1))
		for segID, issues := range failed {
			r.Manifest(p, "prereview.repair", segID)
			// 回喂带历轮问题清单：只喂本轮新问题，重出容易把上轮修好的又改回去
			//（实测 seg02 越修问题越多的一条根因）
			feed := issues
			if prev := prevIssues[segID]; prev != "" && prev != issues {
				feed += "\n上一轮的问题（已修过，重出时不得回退）：\n" + prev
			}
			prevIssues[segID] = issues
			// 场景重出失败≠拦制作：旧 spec 已过 check+lint 可渲染，保留旧画面
			// 继续走，渲后复查会再看到这段的问题（宁放过不误杀）
			if gerr := r.sceneGen(ctx, p, "", feed, segID); gerr != nil {
				if ctx.Err() != nil {
					return res, ctx.Err()
				}
				r.Manifest(p, "prereview.regen_error", segID+": "+pipeline.FirstLine(gerr.Error(), 120))
				continue
			}
			repairedSet[segID] = true
			r.clearFrames(p, segID)
		}
		if err := pipeline.Run(ctx, p, "compositions"); err != nil {
			return res, fmt.Errorf("合成物落盘失败: %w", err)
		}
		if err := r.python(ctx, "ai/assemble.py", p.Dir, 2*time.Minute); err != nil {
			return res, fmt.Errorf("组装失败: %w", err)
		}
	}
}

// snapFrames 主组合一次性截全部时刻（一次浏览器冷启）。--no-browser-gpu 强制
// SwiftShader：渲前看到的帧必须和渲染器将产出的画面同源（渲染器 auto-detect 掷
// 硬币是 seg07 事故根因，7f3b61d 已强制）。
func (r *Runner) snapFrames(p *pipeline.Project, atArg, dir string) ([]string, error) {
	_ = os.RemoveAll(p.Artifact(dir))
	out, err := pipeline.RunCLI(p, "npx", "hyperframes@0.8.55", "snapshot", ".",
		"--at", atArg, "--no-end", "--no-browser-gpu", "-o", dir)
	if err != nil {
		return nil, fmt.Errorf("snapshot 失败: %s", pipeline.TailLines(out, 8))
	}
	entries, err := os.ReadDir(p.Artifact(dir))
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "frame-") && strings.HasSuffix(e.Name(), ".png") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("snapshot 未产出帧（--at %s）", atArg)
	}
	return names, nil
}

// groupSnapFrames 截帧按文件名时刻归段，段内按时刻升序（图1→图N 时间正序）。
func groupSnapFrames(p *pipeline.Project, dir string, frames []string, t2seg map[float64]string) map[string][]string {
	nameAt := map[string]float64{}
	for _, name := range frames {
		if m := reSnapAt.FindStringSubmatch(name); m != nil {
			if t, err := strconv.ParseFloat(m[1], 64); err == nil {
				nameAt[name] = round2(t)
			}
		}
	}
	bySeg := map[string][]string{}
	for name, t := range nameAt {
		if segID := t2seg[t]; segID != "" {
			bySeg[segID] = append(bySeg[segID], filepath.Join(p.Artifact(dir), name))
		}
	}
	for id, paths := range bySeg {
		ss := paths
		sort.Slice(ss, func(i, j int) bool {
			return nameAt[filepath.Base(ss[i])] < nameAt[filepath.Base(ss[j])]
		})
		bySeg[id] = ss
	}
	return bySeg
}

// reviewSnapshots 按段归组的截帧 → 缩水 → 视觉模型判分。只审 pending 里的段
// （修复轮范围控制）。返回 未过段 → 问题回喂文本。
// 段间零依赖（各审各的图、verdict 独立）→ 有界并发，模式同 reviewSegments；
// 单段审查调用失败视为审查器抖动：manifest 记账后跳过（宁可放过不误杀）。
func (r *Runner) reviewSnapshots(ctx context.Context, p *pipeline.Project, sb *contract.Storyboard, voices map[string]voiceMeta, bySeg map[string][]string, crits []sigCriterion, pending map[string]bool) (map[string]string, error) {
	prov, err := llm.FromEnv(llm.RoleVisual)
	if err != nil {
		return nil, fmt.Errorf("预审需要 API 模式（LLM_API_KEY）: %w", err)
	}
	var todo []contract.Segment
	for _, seg := range sb.Segments {
		if pending[seg.ID] && len(bySeg[seg.ID]) > 0 {
			todo = append(todo, seg)
		}
	}
	// 真迹/样张/判据每段同一套，循环外算一次（同 reviewSegments 的成本策略）
	keyImgs := styleKeyframeImages(ctx, p)
	samples := styleSampleImages(p)
	failed := map[string]string{}
	var mu sync.Mutex
	firstErr := error(nil)
	sem := make(chan struct{}, frameqaConcurrency())
	var wg sync.WaitGroup
	review := func(seg contract.Segment) {
		defer wg.Done()
		defer func() { <-sem }()
		if err := ctx.Err(); err != nil {
			mu.Lock()
			if firstErr == nil {
				firstErr = err
			}
			mu.Unlock()
			return
		}
		var vd frameVerdict
		// 图1..图N = 该段截帧（时间正序）；样张/真迹附在其后（风格一致性同场评审）
		imgs := make([]string, 0, len(bySeg[seg.ID])+len(samples)+len(keyImgs))
		for _, png := range bySeg[seg.ID] {
			jpg, serr := shrinkFrame(ctx, png)
			if serr != nil {
				r.Manifest(p, "prereview.review_error", seg.ID+": "+serr.Error())
				return
			}
			imgs = append(imgs, dataURL(jpg))
		}
		imgs = append(imgs, samples...)
		imgs = append(imgs, keyImgs...)
		// 单发任务也走统一记账（「过程」页）：极简留痕 = 请求 + 响应各一条。
		sys := "你是视频的画面质检员，只输出 JSON。"
		prompt := preReviewPrompt(p, seg, voices[seg.ID], len(samples), len(keyImgs), crits)
		at := agentlog.Begin(p, "prereview."+seg.ID, "画面预审 "+seg.ID)
		if rec := at.Rec(); rec != nil {
			rec.Message(openai.ChatCompletionMessage{Role: openai.ChatMessageRoleSystem, Content: sys})
			rec.Message(openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: prompt})
		}
		u, rerr := prov.GenerateJSONVisionEffort(ctx, sys, prompt, imgs, 0.2, &vd, llm.EffortHigh)
		at.Add(u)
		if rerr != nil {
			at.Done(rerr)
			r.Manifest(p, "prereview.review_error", seg.ID+": "+rerr.Error())
			return
		}
		if rec := at.Rec(); rec != nil {
			if b, merr := json.Marshal(vd); merr == nil {
				rec.Message(openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: string(b)})
			}
		}
		at.Done(nil)
		mu.Lock()
		defer mu.Unlock()
		for _, sv := range vd.Signatures {
			if sv.Verdict != "" {
				r.Manifest(p, "prereview.sig", fmt.Sprintf("%s %s: %s %s", seg.ID, sv.SigID, sv.Verdict, sv.Note))
			}
		}
		if vd.Pass && !hasHigh(vd) && !sigFailed(vd.Signatures) {
			r.Manifest(p, "prereview.seg", fmt.Sprintf("%s: pass", seg.ID))
			return
		}
		r.Manifest(p, "prereview.seg", fmt.Sprintf("%s: fail（%d 个问题）", seg.ID, len(vd.Issues)))
		feedback := issuesFeedback(vd)
		if sigFailed(vd.Signatures) {
			feedback += sigFeedback(vd.Signatures, crits)
		}
		// 问题明细落账（P11）：只记个数事后无法追溯——换行拉平防账本按行解析错位
		detail := pipeline.FirstLine(strings.ReplaceAll(strings.TrimSpace(feedback), "\n", "；"), 300)
		r.Manifest(p, "prereview.issues", seg.ID+": "+detail)
		failed[seg.ID] = feedback
	}
	for _, seg := range todo {
		wg.Add(1)
		sem <- struct{}{}
		go review(seg)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return failed, nil
}

// shrinkFrame snapshot 的 PNG → 1280 宽 JPEG（与 extractFrames 同参），控制多模态请求体积。
func shrinkFrame(ctx context.Context, src string) (string, error) {
	ff, err := pipeline.FFmpegPath()
	if err != nil {
		return "", err
	}
	dst := strings.TrimSuffix(src, ".png") + ".jpg"
	cmd := pipeline.CommandContext(ctx, ff, "-y", "-v", "error", "-i", src,
		"-frames:v", "1", "-vf", "scale=1280:-2", "-q:v", "4", dst)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s 缩水失败: %w\n%s", filepath.Base(src), err, pipeline.TailLines(buf.String(), 5))
	}
	return dst, nil
}

// preReviewPrompt 预审考卷：三帧一调。rubric 与 reviewPrompt 同源（scene 豁免块/
// 风格块/特色判据块共用），差异只在帧位语义：没有 t=0 首帧与尾帧——穿帮靠图1抓
// 「晚出元素提前出现」，退场检查交渲后复查。
func preReviewPrompt(p *pipeline.Project, seg contract.Segment, v voiceMeta, samples, keys int, crits []sigCriterion) string {
	styleBlock := styleCheckBlock(designOf(p), samples, keys, 3)
	var b strings.Builder
	header := "这是同一段视频的三帧渲染前预览截屏（底部字幕带是画面一部分，字幕文字不算元素）。"
	switch {
	case samples > 0 && keys > 0:
		header = fmt.Sprintf("这是同一段视频的三帧渲染前预览截屏 + %d 张已确认的风格样张 + %d 张本风格来源视频的原片真迹（底部字幕带是画面一部分，字幕文字不算元素）。", samples, keys)
	case samples > 0:
		header = fmt.Sprintf("这是同一段视频的三帧渲染前预览截屏 + %d 张已确认的风格样张（底部字幕带是画面一部分，字幕文字不算元素）。", samples)
	case keys > 0:
		header = fmt.Sprintf("这是同一段视频的三帧渲染前预览截屏 + %d 张本风格来源视频的原片真迹（底部字幕带是画面一部分，字幕文字不算元素）。", keys)
	}
	b.WriteString(fmt.Sprintf(`%s
图1 = 开场后（约 0.5s，开场元素入场中或刚落定）；图2 = 中段；图3 = 落定帧（段尾稍前，元素最全）。

## 段信息
- 标题：%s
- 旁白：%s
- 画面提示（本段该画什么，以此为准）：%s
`, header, seg.Key, seg.Narration, seg.VisualBrief))
	if specHasScene(p, seg.ID) {
		b.WriteString(`
## 本段含程序化画布（scene）
画面主体是全画幅 canvas 绘制（粒子/曲线/线框等，深底+发光前景是常态）：
- 大片深色留白、单主体居中、暗调层次 —— 都是设计，一律不报
- 只查：整屏全黑（绘制崩了）、画面撕裂/明显错位、叠加的文字元素不可读

## 检查（宁严勿松，只报画面上真实可见的问题）
图1/图2/图3 逐帧查：
1. 画布崩坏：整屏全黑/全白、画面静止无演化迹象（代码未生效）
2. 裂图：图片未加载/占位符裸奔/画面撕裂/明显错位
3. 叠加文字不可读：过小、对比不足、被截断
通用：动效中间态（半透明/位移中）不算问题；canvas 绘制内容/背景纹理/纯色底不算「元素」；轻微审美偏好不算问题。
`)
	} else {
		b.WriteString(`
## 检查（宁严勿松，只报画面上真实可见的问题）
图1 开场后：
1. 提前穿帮：清单外的元素此刻已清晰可见（按旁白节拍本该更晚出场的主体元素 = high；正在入场的半透明/位移中元素不算）
2. 裂图：图片未加载/占位符裸奔/空白主体
图2 中段：
1. 排列：元素重叠遮挡（文字压文字/压图形主体）、越界出画、贴边过紧
2. 布局（只报失衡不是报留白）：大面积空白与不对称构图可以是设计——除非画面提示或设计系统要求满构图，否则不报；元素挤在一角且互无组织才算失衡
3. 可读性：文字过小、对比不足；裂图/截断
图3 落定帧：
1. 同图2 的排列/布局/可读性全项（此刻元素最全，最容易暴露重叠越界）
2. 画布崩坏：整屏全黑/全白、画面静止无演化迹象（代码未生效）
通用：动效中间态（半透明/位移中）不算问题；背景纹理/纯色底/装饰线不算元素；轻微审美偏好不算问题。
`)
	}
	b.WriteString(styleBlock)
	b.WriteString(criteriaBlock(crits, 0))
	b.WriteString(`
## 输出 JSON
{"pass": true/false, "issues": [{"frame": "open|mid|last|sample", "severity": "high|medium|low", "element": "哪个元素", "problem": "什么问题", "suggestion": "怎么改"}]`)
	if len(crits) > 0 {
		b.WriteString(`,
"signatures": [{"sig_id": "sig1", "verdict": "absorbed|drifted|missing", "note": "一句话依据"}]`)
	}
	b.WriteString(`
}
pass 标准：无 high 问题即 true；medium/low 记录但不算失败` +
		map[bool]string{true: "；特色判据任一 missing/drifted 也算失败", false: ""}[len(crits) > 0] + "。")
	return b.String()
}

// staticCrits 过滤出预审可判的特色判据：转场/运镜类的证据是渲后段片边界帧序列，
// 预审截不出（留给渲后 frameQA）；其余判据静态帧可判。
func staticCrits(crits []sigCriterion) []sigCriterion {
	out := make([]sigCriterion, 0, len(crits))
	for _, c := range crits {
		if !contract.IsDynamicSignature(c.Sig.Kind) {
			out = append(out, c)
		}
	}
	return out
}

// roundScope 第 round 轮的审查范围（P4 收敛）：0=全量；1=只审被修段（没动的段
// 帧没变，重审纯浪费）；≥2=升回全量兜底——连续局部失败提示可能有系统性问题，
// 放行渲染前给一次全景（也是修复上限前最后一场）。
func roundScope(round int, all []string, repaired map[string]bool) map[string]bool {
	scope := make(map[string]bool, len(all))
	if round == 1 {
		for id := range repaired {
			scope[id] = true
		}
		return scope
	}
	for _, id := range all {
		scope[id] = true
	}
	return scope
}

func segIDsOf(sb *contract.Storyboard) []string {
	ids := make([]string, 0, len(sb.Segments))
	for _, seg := range sb.Segments {
		ids = append(ids, seg.ID)
	}
	return ids
}

func segCount(t2seg map[float64]string) int {
	set := map[string]bool{}
	for _, id := range t2seg {
		set[id] = true
	}
	return len(set)
}

func sortedSegIDs(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
