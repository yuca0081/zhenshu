package produce

// 段级画面审查（plan §4.5「快照审查」）：渲出的每段段片抽 首帧/开场帧/落定帧/尾帧，
// 视觉模型按 rubric 检查（提前穿帮/裂图/重叠/越界/截断/失衡/退场失败），不过的段
// 带问题回喂重出 spec → 只重渲该段（自修复 ≤2 轮）。仍不过不阻塞成片：
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
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/pipeline"
)

const (
	firstFrameAt    = "0.8"   // 开场帧时刻：首元素 reveal 最早 0.05s + 入场 ~0.35s，0.8s 已落定
	lastFrameFrom   = "-0.3"  // 落定帧：段尾倒数 0.3s（同 M0 visual_qa 的「段落定状态」口径）
	finalFrameFrom  = "-0.08" // 尾帧：段最后一帧（退场是否退净以此为准）
	openPlanThrough = 0.85    // 开场清单阈值：reveal ≤ 此值的元素开场帧应已落定
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
}

// frameQA 对 only 段（空=全片）做视觉审查 + 自修复。instruction 是 rework 的
// 用户改画要求：修复轮重出 spec 时必须原样带上，否则用户要求会在修复轮被丢掉。
func (r *Runner) frameQA(ctx context.Context, p *pipeline.Project, instruction, only string) error {
	id := p.ID
	r.emit(id, "stage", "frameqa", "running")
	r.Manifest(p, "frameqa.start", map[bool]string{true: only, false: "全片"}[only != ""])
	for round := 0; ; round++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		failed, err := r.reviewSegments(ctx, p, only)
		if err != nil {
			return err
		}
		if len(failed) == 0 {
			r.emit(id, "stage", "frameqa", "done")
			r.Manifest(p, "frameqa.pass", only)
			return nil
		}
		if round >= 2 { // 修复上限：不阻塞成片，标记问题交给用户决定（plan §4.5）
			summary := failSummary(failed)
			r.emit(id, "stage", "frameqa", "error: "+summary)
			r.Manifest(p, "frameqa.failed", summary)
			return nil
		}
		r.emit(id, "progress", "frameqa",
			fmt.Sprintf("%d 段审查未过，带问题重出画面（第 %d 轮）", len(failed), round+1))
		for segID, issues := range failed {
			if err := r.genSpecs(ctx, p, instruction, issues, segID); err != nil {
				return err
			}
			if err := r.fetchImages(ctx, p); err != nil {
				return err
			}
			if err := r.renderSpecs(ctx, p); err != nil {
				return err
			}
			r.clearFrames(p, segID)
			if err := pipeline.Run(ctx, p, "compositions"); err != nil {
				return fmt.Errorf("合成物落盘失败: %w", err)
			}
			_ = os.Remove(p.Artifact("renders/segs/" + segID + ".mp4"))
		}
		if err := r.python(ctx, "ai/assemble.py", p.Dir, 2*time.Minute); err != nil {
			return fmt.Errorf("组装失败: %w", err)
		}
		if err := pipeline.Run(ctx, p, "render"); err != nil {
			return fmt.Errorf("重渲失败: %w", err)
		}
		// 修复轮动了段片 → 必须重缝合成片，否则交付的 main.mp4 还是修复前画面
		// （run/runRework 的 stitch 在 frameQA 之前，救不了这里）。
		if err := pipeline.Run(ctx, p, "stitch"); err != nil {
			return fmt.Errorf("重缝合失败: %w", err)
		}
	}
}

// reviewSegments 范围内每段抽四帧送审，返回 未过段 → 问题回喂文本。
// 段间零依赖（各读各的段片、各写各的帧图、verdict 独立）→ 有界并发，模式同 genSpecs：
// 抽帧 + 视觉调用都在并发区，429/网络抖动由 provider 重试兜底。
// 单段审查调用失败视为审查器抖动：manifest 记账后跳过（宁可放过不误杀管线）。
func (r *Runner) reviewSegments(ctx context.Context, p *pipeline.Project, only string) (map[string]string, error) {
	sb, err := p.LoadStoryboard()
	if err != nil {
		return nil, err
	}
	voices, err := loadAudioMeta(p)
	if err != nil {
		return nil, err
	}
	prov, err := llm.FromEnv(llm.RoleVisual)
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
		_, rerr := prov.GenerateJSONVision(ctx,
			"你是科普视频的画面质检员，只输出 JSON。",
			reviewPrompt(p, seg, voices[seg.ID]),
			[]string{dataURL(sf.first), dataURL(sf.open), dataURL(sf.last), dataURL(sf.final)}, 0.2, &vd)
		if rerr != nil {
			r.Manifest(p, "frameqa.review_error", seg.ID+": "+rerr.Error())
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if vd.Pass && !hasHigh(vd) {
			r.Manifest(p, "frameqa.seg", fmt.Sprintf("%s: pass", seg.ID))
			return
		}
		r.Manifest(p, "frameqa.seg", fmt.Sprintf("%s: fail（%d 个问题）", seg.ID, len(vd.Issues)))
		failed[seg.ID] = issuesFeedback(vd)
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
		cmd := exec.CommandContext(ctx, ff, args...)
		var buf bytes.Buffer
		cmd.Stdout, cmd.Stderr = &buf, &buf
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%w\n%s", err, tail(buf.String(), 5))
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

// revealPlan 从 spec + 词级时间戳算出的三份判定清单，给「提前穿帮/退场失败」客观对照物：
//   - AtZero：anim=none 垫底板（custom 直出容器等），设计上从 0s 可见——首帧白名单
//   - AtOpen：reveal ≤ 0.85s，开场帧应已落定；清单外的元素尚未出现是正常渐入
//   - GoneAtEnd：已退场且退净时刻早于尾帧，尾帧不应再见
//
// spec 缺失（异常）返回空清单，提示词降级为无对照。
type revealPlan struct {
	AtZero    []string
	AtOpen    []string
	GoneAtEnd []string
}

func revealPlanOf(p *pipeline.Project, segID string, v voiceMeta) (plan revealPlan) {
	b, err := os.ReadFile(p.Artifact("llm/comp-" + segID + ".spec.json"))
	if err != nil {
		return
	}
	var spec contract.CompSpec
	if json.Unmarshal(b, &spec) != nil {
		return
	}
	for i := range spec.Elements {
		e := &spec.Elements[i]
		label := elemLabel(e)
		if label == "" {
			continue
		}
		if e.Anim == "none" && (e.Kind == "custom" || simpleAnimKind(e.Kind)) {
			plan.AtZero = append(plan.AtZero, label)
		}
		if revealAt(e, v) <= openPlanThrough {
			plan.AtOpen = append(plan.AtOpen, label)
		}
		if ex := int(e.Exit); ex > 0 && len(v.Words) > 0 && v.DurationS > 0 {
			idx := ex
			if idx > len(v.Words)-1 {
				idx = len(v.Words) - 1
			}
			t := v.Words[idx].End + 0.05 // 与 ai/render_spec.py exit_t 同口径（词尾 +0.05s）
			if t+0.45 <= v.DurationS {   // 淡出 0.35s + 余量：尾帧（段尾 -0.08s）前已退净
				plan.GoneAtEnd = append(plan.GoneAtEnd, label)
			}
		}
	}
	return
}

// revealAt 揭示时刻（口径与 ai/render_spec.py reveal_t 一致：max(0.05, 词start - 0.12)）。
func revealAt(e *contract.SpecElement, v voiceMeta) float64 {
	t := 0.05
	if len(v.Words) > 0 {
		idx := e.Reveal
		if idx < 0 {
			idx = 0
		}
		if idx > len(v.Words)-1 {
			idx = len(v.Words) - 1
		}
		if s := v.Words[idx].Start - 0.12; s > 0.05 {
			t = s
		}
	}
	return t
}

// simpleAnimKind 与 ai/render_spec.py SIMPLE_KINDS 同口径（custom 由调用方另行豁免）：
// 单根、无隐藏子元素的 kind，anim 覆盖安全。
func simpleAnimKind(k string) bool {
	switch k {
	case "title", "note", "label", "big", "beam", "disc", "circle", "chip",
		"icon", "image", "emoji", "panel", "zone", "bracket", "barrow", "stat":
		return true
	}
	return false
}

func elemLabel(e *contract.SpecElement) string {
	main := e.Text
	if main == "" {
		main = e.Title
	}
	if main == "" && len(e.Nodes) > 0 {
		main = strings.Join(e.Nodes, "、")
	}
	if main == "" && len(e.Labels) > 0 {
		main = strings.Join(e.Labels, "、")
	}
	if main == "" && e.Name != "" {
		main = e.Name
	}
	if main == "" && e.Query != "" {
		main = "照片:" + e.Query
	}
	if main == "" {
		return e.Kind
	}
	return e.Kind + ":" + runeTrunc(main, 16)
}

func runeTrunc(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
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

// specHasScene 该段 spec 是否含 kind=scene（frameQA 提示词豁免 / 回放门禁选段共用）。
func specHasScene(p *pipeline.Project, segID string) bool {
	b, err := os.ReadFile(p.Artifact("llm/comp-" + segID + ".spec.json"))
	if err != nil {
		return false
	}
	var spec contract.CompSpec
	if json.Unmarshal(b, &spec) != nil {
		return false
	}
	for i := range spec.Elements {
		if spec.Elements[i].Kind == "scene" {
			return true
		}
	}
	return false
}

// reviewPrompt 四帧一调（省钱且模型可对照）：首帧查提前穿帮，开场帧查清单外提前
// 出现，落定帧查布局全项，尾帧查退场残留。图片按 图1=首帧、图2=开场帧、
// 图3=落定帧、图4=尾帧 顺序附在消息里。
func reviewPrompt(p *pipeline.Project, seg contract.Segment, v voiceMeta) string {
	plan := revealPlanOf(p, seg.ID, v)
	var b strings.Builder
	b.WriteString(fmt.Sprintf(`这是同一段视频的四帧渲染画面（底部字幕带是画面一部分，字幕文字不算元素）。
图1 = 首帧（0s，段的第一帧）；图2 = 开场帧（约 0.8s，开场元素刚落定）；
图3 = 落定帧（段尾稍前，元素齐全）；图4 = 尾帧（段最后一帧）。

## 段信息
- 标题：%s
- 旁白：%s
`, seg.Key, seg.Narration))
	if len(plan.AtZero) > 0 {
		b.WriteString("\n## 首帧（图1）允许可见的垫底元素（设计上从 0s 直出；清单外的元素出现在图1 就是穿帮）\n")
		for _, e := range plan.AtZero {
			b.WriteString("- " + e + "\n")
		}
	} else {
		b.WriteString("\n## 首帧（图1）本段无垫底元素：图1 上任何内容元素可见都是提前穿帮\n")
	}
	if len(plan.AtOpen) > 0 {
		b.WriteString("\n## 开场帧（图2）此时应有的元素（清单外的元素尚未出现是正常渐入设计，不算问题）\n")
		for _, e := range plan.AtOpen {
			b.WriteString("- " + e + "\n")
		}
	}
	if len(plan.GoneAtEnd) > 0 {
		b.WriteString("\n## 尾帧（图4）应已退场的元素（按设计此刻不应再出现在画面上）\n")
		for _, e := range plan.GoneAtEnd {
			b.WriteString("- " + e + "\n")
		}
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

## 输出 JSON
{"pass": true/false, "issues": [{"frame": "first|open|last|final", "severity": "high|medium|low", "element": "哪个元素", "problem": "什么问题", "suggestion": "怎么改"}]}
pass 标准：无 high 问题即 true；medium/low 记录但不算失败。`)
		return b.String()
	}
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
2. 布局：视觉重心失衡、大面积空白（>25% 画面）、元素挤在一角
3. 可读性：文字过小、对比不足；裂图/截断
图4 尾帧：
1. 退场失败：应已退场清单里的元素仍可见
2. 画面空洞：大片元素消失后无内容承接（只剩字幕带）
通用：动效中间态（半透明/位移中）不算问题；背景纹理/纯色底/装饰线不算元素；轻微审美偏好不算问题。

## 输出 JSON
{"pass": true/false, "issues": [{"frame": "first|open|last|final", "severity": "high|medium|low", "element": "哪个元素", "problem": "什么问题", "suggestion": "怎么改"}]}
pass 标准：无 high 问题即 true；medium/low 记录但不算失败。`)
	return b.String()
}

// issuesFeedback high/medium 问题 → 回喂 genSpecs 的修正要求（low 噪音不回喂）。
func issuesFeedback(v frameVerdict) string {
	frameNames := map[string]string{"first": "首帧", "open": "开场帧", "last": "落定帧", "final": "尾帧"}
	var b strings.Builder
	b.WriteString("渲染后的画面审查发现以下问题，重出 spec 时必须逐条修正（调整坐标/大小/删减元素/给提前可见的元素安排入场动画）:\n")
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
