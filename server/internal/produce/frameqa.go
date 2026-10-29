package produce

// 段级画面审查（plan §4.5「快照审查」）：渲出的每段段片抽 首帧/开场帧/落定帧/尾帧，
// 视觉模型按 rubric 检查（提前穿帮/裂图/重叠/越界/截断/失衡/退场失败），不过的段
// 带问题回喂重出场景文件 → 只重渲该段（自修复 ≤2 轮）。仍不过不阻塞成片：
// stage 事件标红 + manifest 记账，用户知情后可对该段 rework。
// 审查模型与生成共用 LLM_* 配置（glm-5.3-flash 已实测支持图片输入 + JSON 模式）。
//
// 首帧是「分镜一开始元素就在画面上」的硬闸：t=0 时除 anim=none 垫底板外所有元素
// 都应处于 gsap.set 藏起的初始态，图1 上任何内容元素可见即提前穿帮——开场帧(0.8s)
// 已入场落定，单靠它查不出这类问题。

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	openai "github.com/sashabaranov/go-openai"

	"webpage-video-agent/server/internal/agentlog"
	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/pipeline"
)

const (
	firstFrameAt   = "0.8"   // 开场帧时刻：首元素 reveal 最早 0.05s + 入场 ~0.35s，0.8s 已落定
	lastFrameFrom  = "-0.3"  // 落定帧：段尾倒数 0.3s（同 M0 visual_qa 的「段落定状态」口径）
	finalFrameFrom = "-0.08" // 尾帧：段最后一帧（退场是否退净以此为准）
)

type frameVerdict struct {
	Pass   bool `json:"pass"`
	Issues []struct {
		Frame      string `json:"frame"` // first|open|last|final
		Severity   string `json:"severity"`
		Element    string `json:"element"`
		Problem    string `json:"problem"`
		Suggestion string `json:"suggestion"`
	} `json:"issues"`
	// v3 特色判据（§4.6 第二层）：简报存在时逐条判 absorbed/drifted/missing；
	// missing|drifted → 判据原句进修复要求（sigFeedback）。
	Signatures []sigVerdictRow `json:"signatures"`
}

// frameQA 对 only 段（空=全片）做视觉审查 + 自修复。instruction 是 rework 的
// 用户改画要求：修复轮重出场景文件时必须原样带上，否则用户要求会在修复轮被丢掉。
// 返回带伤摘要（空串=全过）：修复上限到了仍不过时不阻塞成片（plan §4.5），
// 摘要交调用方写进完成通知——带伤交付不许只报「完成」（2026-10-23 中-3）。
func (r *Runner) frameQA(ctx context.Context, p *pipeline.Project, instruction, only string) (string, error) {
	id := p.ID
	r.emit(id, "stage", "frameqa", "running")
	r.Manifest(p, "frameqa.start", map[bool]string{true: only, false: "全片"}[only != ""])
	for round := 0; ; round++ {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		failed, err := r.reviewSegments(ctx, p, only)
		if err != nil {
			return "", err
		}
		if len(failed) == 0 {
			r.emit(id, "stage", "frameqa", "done")
			r.Manifest(p, "frameqa.pass", only)
			return "", nil
		}
		if round >= 2 { // 修复上限：不阻塞成片，标记问题交给用户决定（plan §4.5）
			summary := failSummary(failed)
			r.emit(id, "stage", "frameqa", "error: "+summary)
			r.Manifest(p, "frameqa.failed", summary)
			return summary, nil
		}
		r.emit(id, "progress", "frameqa",
			fmt.Sprintf("%d 段审查未过，带问题重出画面（第 %d 轮）", len(failed), round+1))
		// 备份本轮要动的段 spec：修复轮是掷骰子，新画面可能反而过不了 check，
		// 到时回滚到已知能过的版本交付（QA 问题标记后交用户决定）
		prev := map[string][]byte{}
		for segID := range failed {
			if b, err := os.ReadFile(p.LLMOutput("comp-" + segID)); err == nil {
				prev[segID] = b
			}
		}
		for segID, issues := range failed {
			if err := r.sceneGen(ctx, p, instruction, issues, segID); err != nil {
				return "", err
			}
			r.clearFrames(p, segID)
			// 删除失败必须留痕（中-L16）：Windows 上文件被渲染进程占用时 Remove
			// 会失败，render 按「段片存在即复用」吃旧画面，修复轮白做且无感知
			if rerr := os.Remove(p.Artifact("renders/segs/" + segID + ".mp4")); rerr != nil && !os.IsNotExist(rerr) {
				r.Manifest(p, "frameqa.seg_remove_failed", fmt.Sprintf("%s: %v（旧段片可能被复用，修复或未生效）", segID, rerr))
				_ = os.Remove(p.Artifact("renders/segs/"+segID+".mp4") + ".sum")
			} else {
				_ = os.Remove(p.Artifact("renders/segs/"+segID+".mp4") + ".sum")
			}
		}
		// 全部段改完统一落盘一次（与 prereview 修复轮同构）：compositions 是
		// 全量重扫，逐段跑一遍 N 段就白付 N-1 遍
		if err := pipeline.Run(ctx, p, "compositions"); err != nil {
			return "", fmt.Errorf("合成物落盘失败: %w", err)
		}
		if err := r.assemble(ctx, p); err != nil {
			return "", fmt.Errorf("组装失败: %w", err)
		}
		if err := pipeline.Run(ctx, p, "render"); err != nil {
			// 修复轮的新画面没过 check：整体回滚本轮改动，用旧画面交付——
			// QA 修复是加分项不是人质（frameqa.failed 标记仍在，用户可自行决定重做）
			r.emit(id, "progress", "frameqa", "修复轮画面未过 check，回滚到修复前画面")
			r.Manifest(p, "frameqa.rollback", pipeline.FirstLine(err.Error(), 120))
			for segID := range failed {
				if orig, ok := prev[segID]; ok {
					if werr := os.WriteFile(p.LLMOutput("comp-"+segID), orig, 0o644); werr != nil {
						return "", werr
					}
				}
				r.clearFrames(p, segID)
			}
			if cerr := pipeline.Run(ctx, p, "compositions"); cerr != nil {
				return "", fmt.Errorf("合成物落盘失败: %w", cerr)
			}
			if cerr := r.assemble(ctx, p); cerr != nil {
				return "", fmt.Errorf("组装失败: %w", cerr)
			}
			if cerr := pipeline.Run(ctx, p, "render"); cerr != nil {
				return "", fmt.Errorf("重渲失败: %w", cerr) // 回滚后仍失败=系统性问题，如实上抛
			}
		}
		// 修复轮动了段片 → 必须重缝合成片，否则交付的 main.mp4 还是修复前画面
		// （run/runRework 的 stitch 在 frameQA 之前，救不了这里）。
		if err := pipeline.Run(ctx, p, "stitch"); err != nil {
			return "", fmt.Errorf("重缝合失败: %w", err)
		}
	}
}

// reviewSegments 范围内每段抽四帧送审，返回 未过段 → 问题回喂文本。
// 段间零依赖（各读各的段片、各写各的帧图、verdict 独立）→ 有界并发，模式同 sceneGen：
// 抽帧 + 视觉调用都在并发区，429/网络抖动由 provider 重试兜底。
// 单段审查调用失败视为审查器抖动：manifest 记账后跳过（宁可放过不误杀管线）。
func (r *Runner) reviewSegments(ctx context.Context, p *pipeline.Project, only string) (map[string]string, error) {
	sb, err := p.WorkStoryboard() // 段落制=单元表（抽帧按单元片：首/尾帧=段落首尾帧，与接续契约同粒度）
	if err != nil {
		return nil, err
	}
	voices, err := loadAudioMeta(p)
	if err != nil {
		return nil, err
	}
	prov, err := llm.FromEnv(llm.StationFrameQA)
	if err != nil {
		return nil, fmt.Errorf("画面审查需要 API 模式（LLM_API_KEY）: %w", err)
	}
	// only 逗号分隔多段（多段重做一起审）；空 = 全片。
	onlySet := map[string]bool{}
	for _, id := range strings.Split(only, ",") {
		if id = strings.TrimSpace(id); id != "" {
			onlySet[id] = true
		}
	}
	var todo []contract.Segment
	for _, seg := range sb.Segments {
		if len(onlySet) > 0 && !onlySet[seg.ID] {
			continue
		}
		if !existsFile(p.Artifact("renders/segs/" + seg.ID + ".mp4")) {
			continue // 无段片：交给渲染阶段的报错，这里不背
		}
		todo = append(todo, seg)
	}
	failed := map[string]string{}
	var mu sync.Mutex
	firstErr := error(nil)
	sem := make(chan struct{}, frameqaConcurrency())
	var wg sync.WaitGroup
	// 真迹关键帧（风格来源视频原片帧）：考卷对照物升级——漂移判断有原始目标可依；
	// 每段同一套真迹，循环外算一次。无媒体/压图失败为空（考卷退回只对照样张）。
	keyImgs := styleKeyframeImages(ctx, p)
	// 样张同上（每段同一套）：prereview 同位置已外提，这里跟上——每段重读盘
	// +base64 是白付的成本
	samples := styleSampleImages(p)
	// v3 特色判据清单（简报特色 × 落点表）：每段同一套，循环外算一次；
	// 含动态特色（transition/camera）才追加边界帧——没勾动态特色零额外抽帧（§4.6 成本策略）。
	crits := signatureCriteria(pipeline.LoadStyleBrief(p), designOf(p))
	dynamic := hasDynamicCrit(crits)
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
		sf, err := extractFrames(ctx, p, seg.ID)
		if err != nil {
			mu.Lock()
			if firstErr == nil {
				firstErr = err
			}
			mu.Unlock()
			return
		}
		var vd frameVerdict
		// high：逐段审查调用量大，max 的深思考成本×段数不划算；max 留给 storyboard 这类每片一次的深规划。
		// 样张/真迹附在四帧之后（图5+）：风格一致性同场评审——笼子二的回路（plan-v2 §4 Tier1 报告项）。
		// 段边界前后帧序列（transition 判据的考核证据）附在最后。
		frameVis := []visImg{
			{URL: dataURL(sf.first), Path: projRel(p, sf.first), Label: "首帧（0s）"},
			{URL: dataURL(sf.open), Path: projRel(p, sf.open), Label: "开场帧（约0.8s）"},
			{URL: dataURL(sf.last), Path: projRel(p, sf.last), Label: "落定帧（段尾稍前）"},
			{URL: dataURL(sf.final), Path: projRel(p, sf.final), Label: "尾帧"},
		}
		vis := append([]visImg{}, frameVis...)
		vis = append(vis, samples...)
		vis = append(vis, keyImgs...)
		var boundary []visImg
		if dynamic {
			boundary = extractBoundaryFrames(ctx, p, seg.Idx)
			vis = append(vis, boundary...)
		}
		imgs := visURLs(vis)
		// 单发任务也走统一记账（「过程」页）：请求（带图清单）+ 响应各一条。
		sys := "你是视频的画面质检员，只输出 JSON。"
		prompt := reviewPrompt(p, seg, voices[seg.ID], len(keyImgs), len(samples), crits, len(boundary))
		at := agentlog.Begin(p, "frameqa."+seg.ID, "画面审查 "+seg.ID)
		if rec := at.Rec(); rec != nil {
			rec.Message(openai.ChatCompletionMessage{Role: openai.ChatMessageRoleSystem, Content: sys})
			rec.MessageImgs(openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: prompt}, visRefs(vis))
		}
		u, rerr := prov.GenerateJSONVisionEffort(ctx, sys, prompt, imgs, 0.2, &vd, llm.EffortHigh)
		at.Add(u)
		if rerr != nil {
			at.Done(rerr)
			r.Manifest(p, "frameqa.review_error", seg.ID+": "+rerr.Error())
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
				r.Manifest(p, "frameqa.sig", fmt.Sprintf("%s %s: %s %s", seg.ID, sv.SigID, sv.Verdict, sv.Note))
			}
		}
		if vd.Pass && !hasHigh(vd) && !sigFailed(vd.Signatures) {
			r.Manifest(p, "frameqa.seg", fmt.Sprintf("%s: pass", seg.ID))
			return
		}
		nIssue := len(vd.Issues)
		if sigFailed(vd.Signatures) {
			nIssue += len(vd.Signatures)
		}
		r.Manifest(p, "frameqa.seg", fmt.Sprintf("%s: fail（%d 个问题）", seg.ID, nIssue))
		feedback := issuesFeedback(vd)
		if sigFailed(vd.Signatures) {
			feedback += sigFeedback(vd.Signatures, crits)
		}
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

// frameqaConcurrency 审查并发度（默认 3 与 spec 生成一致；视觉调用带 4 图较重，
// 超限 429 由 provider 重试兜底；FRAMEQA_CONCURRENCY 环境变量可调）。
func frameqaConcurrency() int {
	if n, err := strconv.Atoi(os.Getenv("FRAMEQA_CONCURRENCY")); err == nil && n >= 1 && n <= 9 {
		return n
	}
	return 3
}

type segFrames struct {
	first, open, last, final string
}

// extractFrames 段片抽 首帧/开场帧/落定帧/尾帧 各一张（缩到 1280 宽的 JPEG，控制多模态请求体积）。
// 每帧先删旧图再抽（防重跑残留旧帧混审）；指定时刻抽不到（极短段）回退直接取第一帧。
func extractFrames(ctx context.Context, p *pipeline.Project, segID string) (*segFrames, error) {
	ff, err := pipeline.FFmpegPath()
	if err != nil {
		return nil, err
	}
	mp4 := p.Artifact("renders/segs/" + segID + ".mp4")
	dir := p.Artifact("reports/frameqa")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	const vf = "scale=1280:-2"
	run := func(args ...string) error {
		cmd := pipeline.CommandContext(ctx, ff, args...)
		var buf bytes.Buffer
		cmd.Stdout, cmd.Stderr = &buf, &buf
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%w\n%s", err, pipeline.TailLines(buf.String(), 5))
		}
		return nil
	}
	// grab 依次试各 seek 方案（nil = 不 seek，取第一帧），全失败才报错
	grab := func(out string, seeks ...[]string) error {
		_ = os.Remove(out)
		for _, seek := range seeks {
			args := append([]string{"-y", "-v", "error"}, seek...)
			args = append(args, "-i", mp4, "-frames:v", "1", "-vf", vf, "-q:v", "4", out)
			if err := run(args...); err == nil && existsFile(out) {
				return nil
			}
			_ = os.Remove(out)
		}
		return fmt.Errorf("%s 抽帧失败", out)
	}
	sf := &segFrames{
		first: filepath.Join(dir, segID+".first.jpg"),
		open:  filepath.Join(dir, segID+".open.jpg"),
		last:  filepath.Join(dir, segID+".last.jpg"),
		final: filepath.Join(dir, segID+".final.jpg"),
	}
	if err := grab(sf.first, nil); err != nil {
		return nil, fmt.Errorf("%s 抽首帧失败: %w", segID, err)
	}
	if err := grab(sf.open, []string{"-ss", firstFrameAt}, nil); err != nil {
		return nil, fmt.Errorf("%s 抽开场帧失败: %w", segID, err)
	}
	if err := grab(sf.last, []string{"-sseof", lastFrameFrom}, nil); err != nil {
		return nil, fmt.Errorf("%s 抽落定帧失败: %w", segID, err)
	}
	if err := grab(sf.final, []string{"-sseof", finalFrameFrom}, nil); err != nil {
		return nil, fmt.Errorf("%s 抽尾帧失败: %w", segID, err)
	}
	return sf, nil
}

func dataURL(file string) string {
	b, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(b)
}

func hasHigh(v frameVerdict) bool {
	for _, i := range v.Issues {
		if i.Severity == "high" {
			return true
		}
	}
	return false
}

// specHasScene 该段帧是否含程序化画布 <canvas>（frameQA 提示词豁免 / 回放门禁选段共用）。
// v2：场景文件直写后，scene 的判据从 spec kind 改为帧 HTML 里的 canvas 元素。
func specHasScene(p *pipeline.Project, segID string) bool {
	b, err := os.ReadFile(p.Artifact("compositions/frames/" + segID + ".html"))
	if err != nil {
		return false
	}
	return strings.Contains(string(b), "<canvas")
}

// reviewPrompt 四帧一调（省钱且模型可对照）：首帧查提前穿帮，开场帧查清单外提前
// 出现，落定帧查布局全项，尾帧查退场残留。图片按 图1=首帧、图2=开场帧、
// 图3=落定帧、图4=尾帧 顺序附在消息里；样张/原片真迹跟在图5+（笼子二回路）；
// 段边界前后帧序列（transition 判据的考核证据）附在最末。crits 是特色判据清单（§4.6）。
func reviewPrompt(p *pipeline.Project, seg contract.Segment, v voiceMeta, keysN, samplesN int, crits []sigCriterion, boundaryN int) string {
	styleBlock := styleCheckBlock(designOf(p), samplesN, keysN, 4)
	var b strings.Builder
	header := "这是同一段视频的四帧渲染画面（底部字幕带是画面一部分，字幕文字不算元素）。"
	switch {
	case samplesN > 0 && keysN > 0:
		header = fmt.Sprintf("这是同一段视频的四帧渲染画面 + %d 张已确认的风格样张 + %d 张本风格来源视频的原片真迹（底部字幕带是画面一部分，字幕文字不算元素）。", samplesN, keysN)
	case samplesN > 0:
		header = fmt.Sprintf("这是同一段视频的四帧渲染画面 + %d 张已确认的风格样张（底部字幕带是画面一部分，字幕文字不算元素）。", samplesN)
	case keysN > 0:
		header = fmt.Sprintf("这是同一段视频的四帧渲染画面 + %d 张本风格来源视频的原片真迹（底部字幕带是画面一部分，字幕文字不算元素）。", keysN)
	}
	if boundaryN > 0 {
		header += fmt.Sprintf("另附 %d 张段边界前后帧序列（转场判据的证据）。", boundaryN)
	}
	b.WriteString(fmt.Sprintf(`%s
图1 = 首帧（0s，段的第一帧）；图2 = 开场帧（约 0.8s，开场元素刚落定）；
图3 = 落定帧（段尾稍前，元素齐全）；图4 = 尾帧（段最后一帧）。

## 段信息
- 标题：%s
- 旁白：%s
- 画面提示（本段该画什么，以此为准）：%s
`, header, seg.Key, seg.Narration, seg.VisualBrief))
	// 节拍表（导演定调产物）：frameQA 的预期考卷维度——查"该出现的没出现"
	// （内容缺失型真 bug 静态检查全盲，只有对照预期看帧能抓，拉普拉斯妖实验实证）。
	if len(seg.Beats) > 0 {
		b.WriteString("- 本段节拍（导演定的逐拍画面预期）\n")
		for _, bt := range seg.Beats {
			fmt.Fprintf(&b, "  - 「%s」→ %s\n", bt.Quote, bt.Expect)
		}
		b.WriteString("  落定帧（图3）对照节拍表：此刻应有的画面主体没出现或货不对板 = high（内容缺失——代码不报错但少演了内容）\n")
	}
	// scene 段（P1-1）：全画幅画布构图——大片深底/留白/暗调层次是设计不是缺陷，
	// 卡片式「空白>25%/重心失衡」判定整组关闭；canvas 像素不参与 DOM 检查。
	if specHasScene(p, seg.ID) {
		b.WriteString(`
## 本段含程序化画布（scene）
画面主体是全画幅 canvas 绘制（粒子/曲线/线框等，深底+发光前景是常态）：
- 大片深色留白、单主体居中、暗调层次 —— 都是设计，一律不报
- 只查：整屏全黑（绘制崩了）、画面撕裂/明显错位、叠加的文字元素不可读
`)
		b.WriteString(`
## 检查（宁严勿松，只报画面上真实可见的问题）
图1 首帧：
1. 提前穿帮：清单外的元素在首帧就清晰可见（内容元素开场即在画面 = high；canvas 本身 0s 即可见是设计）
2. 裂图：图片未加载/占位符裸奔/空白主体
图2 开场帧：
1. 提前穿帮：清单外的元素提前出现
2. 文字截断、元素出画、明显错位
图3 落定帧：
1. 排列：叠加元素重叠遮挡（文字压文字）、越界出画、贴边过紧
2. 可读性：叠加文字过小、对比不足；裂图/截断
3. 画布崩坏：整屏全黑/全白、画面静止无演化迹象（代码未生效）
图4 尾帧：
1. 退场失败：应已退场清单里的元素仍可见
2. 画面空洞：大片元素消失后无内容承接（只剩字幕带）
通用：动效中间态（半透明/位移中）不算问题；背景纹理/纯色底/装饰线/canvas 绘制内容不算「元素」；轻微审美偏好不算问题。
`)
	} else {
		b.WriteString(`
## 检查（宁严勿松，只报画面上真实可见的问题）
图1 首帧：
1. 提前穿帮：清单外的元素在首帧就清晰可见（内容元素开场即在画面 = high）
2. 裂图：图片未加载/占位符裸奔/空白主体
图2 开场帧：
1. 提前穿帮：清单外的元素提前出现
2. 文字截断、元素出画、明显错位
图3 落定帧：
1. 排列：元素重叠遮挡（文字压文字/压图形主体）、越界出画、贴边过紧
2. 布局（只报失衡不是报留白）：大面积空白与不对称构图可以是设计——除非画面提示或设计系统要求满构图，否则不报；元素挤在一角且互无组织才算失衡
3. 可读性：文字过小、对比不足；裂图/截断
图4 尾帧：
1. 退场失败：应已退场清单里的元素仍可见
2. 画面空洞：大片元素消失后无内容承接（只剩字幕带）
通用：动效中间态（半透明/位移中）不算问题；背景纹理/纯色底/装饰线不算元素；轻微审美偏好不算问题。
`)
	}
	b.WriteString(styleBlock)
	b.WriteString(criteriaBlock(crits, boundaryN))
	b.WriteString(`
## 输出 JSON
{"pass": true/false, "issues": [{"frame": "first|open|last|final|sample", "severity": "high|medium|low", "element": "哪个元素", "problem": "什么问题", "suggestion": "怎么改"}]`)
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

// styleCheckBlock 风格一致性检查块（图 base+1 起 = 样张/原片真迹）：笼子二的回路，
// 两条判据——风格漂移（Tier1 非题，「这段和样张像不像同一部片」，只报「像另一部片」
// 级漂移，medium 报告项不阻断，plan-v2 §4）与题材中毒（high，进修复轮回喂）：渲染
// 画面出现真迹里的题动物件且不是画面提示要求的主体。base = 考卷内容帧数（frameQA
// 四帧传 4，预审三帧传 3）。双皮肤与任一皮肤气质一致即算一致；两者都无返回 ""。
// 真迹只当气质参照（配色/字体/质感）——内容与真迹不同是应该的，真迹是另一部片。
func styleCheckBlock(ss *contract.StyleSamples, samples, keys, base int) string {
	if samples <= 0 && keys <= 0 {
		return ""
	}
	var b strings.Builder
	switch {
	case samples > 0 && keys > 0:
		fmt.Fprintf(&b, "\n## 风格一致性（图%d–图%d 风格样张；图%d–图%d 原片真迹）\n最后 %d 张是本片确认过的风格样张（网页转译），随后 %d 张是本风格来源视频的原片真迹（关键帧）。\n",
			base+1, base+samples, base+samples+1, base+samples+keys, samples, keys)
	case keys > 0:
		fmt.Fprintf(&b, "\n## 风格一致性（对照原片真迹，图%d–图%d）\n最后 %d 张是本风格来源视频的原片真迹（关键帧）。\n", base+1, base+keys, keys)
	default:
		fmt.Fprintf(&b, "\n## 风格一致性（对照样张，图%d–图%d）\n最后 %d 张是本片确认过的风格样张。\n", base+1, base+samples, samples)
	}
	if ss != nil && len(ss.EffectiveSkins()) == 2 && strings.TrimSpace(ss.SkinRule) != "" {
		b.WriteString("- 本片是双皮肤（切换规则：" + ss.SkinRule + "）；与任一皮肤气质一致即算一致\n")
	}
	b.WriteString(`- 参照物只提供「怎么画」的气质：配色/字体/质感/构图密度。本段画的内容与真迹不同是应该的——真迹是另一部片，内容本来就不该像，不算偏离
- 风格漂移（medium）：色系/字体/质感明显不同族，像另一部片 → frame="sample"、element="风格漂移"，problem 写偏在哪（色系/字体/质感），suggestion 写怎么靠回参照
- 题材中毒（high）：画面里出现真迹中的题材物件（真迹画的楼宇/人物/器物等），且不是上面「画面提示」要求的主体 → frame="sample"、element="题材中毒"、severity="high"，problem 写哪个物件来自原片题材，suggestion 写换成画面提示要求的主体
- 正常设计内变化不算：同色系深浅、版式差异、背景纹理不同、内容不同带来的密度差异
`)
	return b.String()
}

// designOf 项目设计系统（style_samples.json；缺失/未出返回 nil——风格检查静默跳过）。
func designOf(p *pipeline.Project) *contract.StyleSamples {
	var ss contract.StyleSamples
	if b, err := os.ReadFile(p.Artifact("style/style_samples.json")); err == nil && json.Unmarshal(b, &ss) == nil {
		return &ss
	}
	return nil
}

// issuesFeedback high/medium 问题 → 回喂 sceneGen 的修正要求（low 噪音不回喂）。
// 渲后 frameQA 与渲染前预审共用（预审的帧位是 open/mid/last，"mid" 映射中段帧）。
func issuesFeedback(v frameVerdict) string {
	frameNames := map[string]string{"first": "首帧", "open": "开场帧", "mid": "中段帧", "last": "落定帧", "final": "尾帧", "sample": "风格样张对照"}
	var b strings.Builder
	b.WriteString("画面审查发现以下问题，重出 spec 时必须逐条修正（调整坐标/大小/删减元素/给提前可见的元素安排入场动画）:\n")
	for _, i := range v.Issues {
		if i.Severity != "high" && i.Severity != "medium" {
			continue
		}
		frame := frameNames[i.Frame]
		if frame == "" {
			frame = "落定帧"
		}
		b.WriteString(fmt.Sprintf("- [%s][%s] %s: %s → %s\n", frame, i.Severity, i.Element, i.Problem, i.Suggestion))
	}
	return b.String()
}

func failSummary(failed map[string]string) string {
	segs := make([]string, 0, len(failed))
	for id := range failed {
		segs = append(segs, id)
	}
	return "两轮修复后仍未过审查: " + strings.Join(segs, ",")
}

func existsFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}
