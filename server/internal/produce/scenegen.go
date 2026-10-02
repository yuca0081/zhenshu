// scenegen.go —— v2 场景生成：LLM 直写场景文件（模型当创意总监，平台当工厂）。
//
// 替换 v1 的「语义 spec → render_spec 翻译」中间层（plan-v2 §2.5）：每段一次
// LLM 调用直接产出符合 docs/contract.md 的自包含场景 HTML；契约 lint
// （ai/scene_lint.py）做 Tier 0 静态门禁，违规逐条回喂外科修复（≤2 轮）。
// 产物 = llm/comp-segNN.json {html, elements}——与 v1 render_spec 产物同构，
// compositions 落盘阶段 / assemble / check / render / stitch 下游零改动。
package produce

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	openai "github.com/sashabaranov/go-openai"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/pipeline"
)

// sceneFingerprint 段级场景文件缓存键（内容指纹）：分镜改稿后段 id 复用而
// 旁白/画面提示已换，只看「产物存在」跳过会把旧画面静默复用进新成片。
func sceneFingerprint(seg contract.Segment) string {
	h := sha256.Sum256([]byte(seg.Key + "\x00" + seg.Narration + "\x00" + seg.VisualBrief + "\x00" + strconv.Itoa(int(seg.DurationHint))))
	return hex.EncodeToString(h[:8])
}

// sceneSidecarPath 指纹旁车（llm/comp-segNN.scene.src）：场景文件落盘时的输入指纹。
func sceneSidecarPath(p *pipeline.Project, segID string) string {
	return p.Artifact("llm/comp-" + segID + ".scene.src")
}

// sceneSidecarFingerprint 段内容指纹 + 设计系统版本（笼子二版本戳）：
// 设计一改（确认重出/字段变化）designFingerprint 变 → 按旧版画的段自动过期重生成；
// 设计未变时指纹不变，缓存照常命中。旧旁车（无冒号后缀）与新格式必然不等 → 首跑全量刷新一次。
func sceneSidecarFingerprint(p *pipeline.Project, seg contract.Segment) string {
	return sceneFingerprint(seg) + ":" + designFingerprint(p)
}

// designFingerprint 设计系统内容指纹（style/style_samples.json 文件字节哈希）。
// 文件缺失返回 "-"——样张落地后指纹变化，任何已生成段自然过期。
func designFingerprint(p *pipeline.Project) string {
	b, err := os.ReadFile(p.Artifact("style/style_samples.json"))
	if err != nil {
		return "-"
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:4])
}

// sceneConcurrency 场景生成并发度（段间零依赖；默认 3，SCENE_CONCURRENCY/SPEC_CONCURRENCY 可调）。
func sceneConcurrency() int {
	for _, key := range []string{"SCENE_CONCURRENCY", "SPEC_CONCURRENCY"} {
		if n, err := strconv.Atoi(os.Getenv(key)); err == nil && n >= 1 && n <= 9 {
			return n
		}
	}
	return 3
}

// sceneGen 每段一个场景文件。instruction = 用户改画要求（最高优先级位，修复轮必须
// 带上否则会丢）；feedback = lint/check/审查回喂（必须修正位）；only 非空只处理该段。
func (r *Runner) sceneGen(ctx context.Context, p *pipeline.Project, instruction, feedback, only string) error {
	sb, err := p.LoadStoryboard()
	if err != nil {
		return fmt.Errorf("先完成 storyboard: %w", err)
	}
	voices, err := loadAudioMeta(p)
	if err != nil {
		return err
	}
	prov, perr := llm.FromEnv(llm.RoleVisual)
	if perr != nil {
		return fmt.Errorf("场景生成需要 API 模式（LLM_API_KEY）: %w", perr)
	}
	cv := ProjectCanvas(p)
	golden := goldenExample(r.RootDir)
	// 样张截图（ai/snap_samples.py）→ 多模态注入：模型看着真图写画面（失败退纯文本）。
	if err := r.python(ctx, "ai/snap_samples.py", p.Dir, time.Minute); err != nil {
		r.emit(p.ID, "progress", "compositions", "样张截图失败，风格注入退回纯文本")
	}
	sampleImgs := styleSampleImages(p)
	// 真迹关键帧（风格来源视频原片帧）全段注入：每段都看着真迹画——课堂通道，
	// 与考卷（frameqa 对照真迹）配套；无媒体/压图失败静默缺省，退回纯样张。
	keyImgs := styleKeyframeImages(ctx, r.DataDir, p)
	if len(keyImgs) > 0 {
		sampleImgs = append(sampleImgs, keyImgs...)
	}
	// NoThink：画面是结构化 HTML 作业，开思考单段 5-13 分钟；关思考配合段间并发
	// 把全片压进分钟级（v1 spec 路径同款取舍）。
	gen := func(user string, out any) (openai.Usage, error) {
		if len(sampleImgs) > 0 {
			return prov.GenerateJSONVisionNoThink(ctx, sceneSystemFor(p), user, sampleImgs, 0.7, out)
		}
		return prov.GenerateJSONNoThink(ctx, sceneSystemFor(p), user, out)
	}
	if feedback != "" && only == "" { // 全片重生成：先清旧产物
		for _, seg := range sb.Segments {
			_ = os.Remove(p.Artifact("llm/comp-" + seg.ID + ".json"))
		}
	}
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
		out := p.Artifact("llm/comp-" + seg.ID + ".json")
		if _, err := os.Stat(out); err == nil && feedback == "" && instruction == "" {
			// 已有产物还要过指纹关：旁车指纹（段内容+设计版本）与当前一致才复用
			// （分镜改稿/设计改稿生效的关键闸）。
			if fp, ferr := os.ReadFile(sceneSidecarPath(p, seg.ID)); ferr == nil && string(fp) == sceneSidecarFingerprint(p, seg) {
				continue
			}
		}
		todo = append(todo, seg)
	}
	sem := make(chan struct{}, sceneConcurrency())
	var wg sync.WaitGroup
	var errMu sync.Mutex
	firstErr := error(nil)
	fail := func(err error) {
		errMu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		errMu.Unlock()
	}
	work := func(seg contract.Segment) {
		v := voices[seg.ID]
		sceneM, compM := methodsFor(r.RootDir, seg)
		style := designNoteFor(p, r.DataDir, seg.Idx) + keyframeNote(len(keyImgs))
		user := sceneUserPrompt(seg, v, instruction, feedback, cv, style, golden, sceneM, compM)
		var payload struct {
			HTML     string                  `json:"html"`
			Elements []contract.NamedElement `json:"elements"`
		}
		u1, gerr := gen(user, &payload)
		if gerr != nil {
			fail(fmt.Errorf("%s 场景生成失败: %w", seg.ID, gerr))
			return
		}
		errs := sceneValidate(r, payload.HTML, seg.ID)
		for repair := 0; repair < 2 && len(errs) > 0; repair++ { // 外科修复：违规逐条回喂
			fb := instruction + feedback + "\n上一版场景文件被契约检查拒绝，必须逐条修正：\n- " + strings.Join(errs, "\n- ")
			var fixed struct {
				HTML     string                  `json:"html"`
				Elements []contract.NamedElement `json:"elements"`
			}
			u2, err2 := gen(sceneUserPrompt(seg, v, "", fb, cv, designNoteFor(p, r.DataDir, seg.Idx)+keyframeNote(len(keyImgs)), golden, sceneM, compM), &fixed)
			if err2 != nil {
				fail(fmt.Errorf("%s 场景修复失败: %w", seg.ID, err2))
				return
			}
			u1, payload = u2, fixed
			errs = sceneValidate(r, payload.HTML, seg.ID)
		}
		if len(errs) > 0 {
			fail(fmt.Errorf("%s 契约检查三轮未过：\n- %s", seg.ID, strings.Join(errs, "\n- ")))
			return
		}
		if len(payload.Elements) < 3 {
			fail(fmt.Errorf("%s 元素注册表不足 3 条（元素级指认依赖它）", seg.ID))
			return
		}
		b, _ := json.MarshalIndent(payload, "", "  ")
		if err := pipeline.WriteFileAtomic(p.Artifact("llm/comp-"+seg.ID+".json"), b, 0o644); err != nil {
			fail(err)
			return
		}
		// 指纹旁车紧随产物落盘（写失败按过期处理：下次重生成，宁重复不陈旧）。
		_ = os.WriteFile(sceneSidecarPath(p, seg.ID), []byte(sceneSidecarFingerprint(p, seg)), 0o644)
		p.Manifest("llm.usage", fmt.Sprintf("scene %s prompt=%d completion=%d tokens", seg.ID, u1.PromptTokens, u1.CompletionTokens))
		r.emit(p.ID, "progress", "compositions", fmt.Sprintf("%s 场景文件 ✓（%d 元素）", seg.ID, len(payload.Elements)))
	}
	for _, seg := range todo {
		wg.Add(1)
		sem <- struct{}{}
		go func(seg contract.Segment) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := ctx.Err(); err != nil {
				fail(err)
				return
			}
			work(seg)
		}(seg)
	}
	wg.Wait()
	return firstErr
}

// sceneSystemFor 系统提示跟随项目已确认的设计系统（笼子二：方向 + 贯穿装置一句话，
// 细则由 designNoteFor 注入正文；无设计文件的老项目退回手绘缺省）。
func sceneSystemFor(p *pipeline.Project) string {
	var ss contract.StyleSamples
	if b, err := os.ReadFile(p.Artifact("style/style_samples.json")); err == nil && json.Unmarshal(b, &ss) == nil && ss.Direction != "" {
		s := "你是视频的画面工程师，直接写可渲染的场景 HTML。只输出 JSON。风格：" + ss.Direction + "。"
		if ss.Device != nil && ss.Device.Name != "" {
			s += "贯穿装置「" + ss.Device.Name + "」是全片签名，本段按注入的设计系统细则呼应它。"
		}
		return s
	}
	return "你是视频的画面工程师，直接写可渲染的场景 HTML。只输出 JSON。风格：手绘叙事（纸面·马克笔）——米黄纸面、深灰线稿、彩色便签、楷体。"
}

// goldenExample 契约金样例（ai/contract-example/seg-golden.html）作为结构 few-shot。
func goldenExample(rootDir string) string {
	b, err := os.ReadFile(filepath.Join(rootDir, "ai", "contract-example", "seg-golden.html"))
	if err != nil {
		return ""
	}
	return string(b)
}

// sceneValidate 契约校验：Go 结构检查 + ai/scene_lint.py 静态门禁（Tier 0）。
func sceneValidate(r *Runner, html, segID string) []string {
	var errs []string
	if strings.TrimSpace(html) == "" {
		return []string{"html 为空"}
	}
	if !strings.Contains(html, "<template") {
		errs = append(errs, "文件必须是 <template> 片段（无 DOCTYPE/html/head/body）")
	}
	if !strings.Contains(html, `data-composition-id="`+segID+`"`) {
		errs = append(errs, fmt.Sprintf("缺 data-composition-id=%q（根元素，逐字一致）", segID))
	}
	if !strings.Contains(html, `window.__timelines["`+segID+`"]`) {
		errs = append(errs, fmt.Sprintf("未注册 window.__timelines[%q]", segID))
	}
	if n := strings.Count(html, "data-hf-name="); n < 3 {
		errs = append(errs, fmt.Sprintf("data-hf-name 语义标注仅 %d 处（<3；元素级指认依赖它）", n))
	}
	// 字幕是平台层（assemble 注入）：模型写逐词标注 = 越权且必然与 TTS 时间戳冲突。
	if strings.Contains(html, "data-ph=") || strings.Contains(html, "data-w=") {
		errs = append(errs, "禁止字幕标注（data-ph/data-w）——字幕带由平台按 TTS 时间戳机械装配，内容留在上 83%")
	}
	errs = append(errs, r.sceneLintWithVend(html)...)
	return errs
}

// sceneLintRun 跑 ai/scene_lint.py（临时文件进、✗ 行出）。
func (r *Runner) sceneLintRun(html string) []string {
	tmp, err := os.CreateTemp("", "scene-lint-*.html")
	if err != nil {
		return []string{"lint 无法建临时文件: " + err.Error()}
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.WriteString(html); err != nil {
		tmp.Close()
		return []string{"lint 临时文件写失败: " + err.Error()}
	}
	tmp.Close()
	cmd := exec.Command(pythonExe(), filepath.Join(r.RootDir, "ai", "scene_lint.py"), name)
	cmd.Dir = r.RootDir
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	var errs []string
	for _, ln := range strings.Split(string(out), "\n") {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "✗") || strings.HasPrefix(ln, "  ✗") {
			errs = append(errs, strings.TrimPrefix(strings.TrimSpace(ln), "✗ "))
		}
	}
	if len(errs) == 0 { // lint 崩了（非违规）：报原文，别静默放行
		errs = append(errs, "scene_lint 异常: "+firstLine(string(out), 3))
	}
	return errs
}

// ── vend 自动闭环（docs/vend-stage.md §3）：模型只有申请权（场景内 <!-- vend: lib@ver -->），
// lint 报缺库 → 平台代码代为采购（npm 钉版+验收入册）→ 复检放行；采购失败按"路的问题"
// 原样上抛转人工（§5），不伪装成场景违规回喂模型。

var vendMu sync.Mutex // 段间并发（SCENE_CONCURRENCY）采购串行化：manifest 写与 tarball 落盘防竞态

var missingLibRe = regexp.MustCompile(`缺库:([A-Za-z0-9_.\-]+)@([A-Za-z0-9_.\-]+)\s`)

// missingLibSpecs 从 lint 违规清单提取缺库申请单（A3），去重排序（采购顺序确定）。
func missingLibSpecs(errs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range errs {
		if m := missingLibRe.FindStringSubmatch(e); m != nil {
			spec := m[1] + "@" + m[2]
			if !seen[spec] {
				seen[spec] = true
				out = append(out, spec)
			}
		}
	}
	sort.Strings(out)
	return out
}

// vendorEnsure 跑 ai/vendor_tool.py ensure（确定性代码执行；成员文件由 npm 元数据自动定位）。
func (r *Runner) vendorEnsure(spec string, timeout time.Duration) error {
	vendMu.Lock()
	defer vendMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, pythonExe(), filepath.Join(r.RootDir, "ai", "vendor_tool.py"),
		"ensure", spec, "--allow-pending-replay")
	cmd.Dir = r.RootDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", firstLine(string(out), 2))
	}
	return nil
}

// sceneLintWithVend = lint + 缺库自动采购：申请单全数采到 → 复检放行（以复检为准）；
// 有采购失败 → 非缺库违规 + 采购失败一起返回（转人工，不静默）。
func (r *Runner) sceneLintWithVend(html string) []string {
	errs := r.sceneLintRun(html)
	specs := missingLibSpecs(errs)
	if len(specs) == 0 {
		return errs
	}
	var nonLib []string
	for _, e := range errs {
		if !strings.Contains(e, "缺库:") {
			nonLib = append(nonLib, e)
		}
	}
	var buyErrs []string
	for _, spec := range specs {
		if err := r.vendorEnsure(spec, 3*time.Minute); err != nil {
			buyErrs = append(buyErrs, "vend 采购失败 "+spec+": "+err.Error())
		}
	}
	if len(buyErrs) > 0 {
		return append(nonLib, buyErrs...)
	}
	return r.sceneLintRun(html)
}

// sceneUserPrompt 场景生成作业正文：契约摘要 + 段信息 + 风格/方法注入 + 词序表。
func sceneUserPrompt(seg contract.Segment, v voiceMeta, instruction, feedback string, cv contract.Canvas, style, golden, sceneTpl, compMethods string) string {
	var wb strings.Builder
	for i, w := range v.Words {
		if i > 0 && i%10 == 0 {
			wb.WriteString("\n")
		}
		fmt.Fprintf(&wb, "%d:%s@%.1fs ", i, w.Text, w.Start)
	}
	rules := "- 内容安全区 x∈[0,1080]、y∈[0,1594]；底部 1594px 起是平台字幕带，禁放内容\n" +
		"- 顶部两角是平台角标保留区（启用章节时左上放章名、右上放计数器）：内容避开左上(x<280,y<150)与右上(x>800,y<150)两个角域\n" +
		"- 竖屏 1080×1920：元素横向少列（≤2 列），纵向多行铺排；主视觉放 y≈300–1200"
	if cv.Aspect == "16:9" {
		rules = "- 内容安全区 x∈[0,1920]、y∈[0,896]；底部 896px 起是平台字幕带，禁放内容\n" +
			"- 顶部两角是平台角标保留区（启用章节时左上放章名、右上放计数器）：内容避开左上(x<500,y<150)与右上(x>1420,y<150)两个角域\n" +
			"- 横屏 1920×1080：主视觉放 y≈260–560 一带，层次拉开（主体 300px+、次级 120–200px）"
	}
	instrBlock := ""
	briefLabel := "画面提示"
	if instruction != "" {
		instrBlock = "\n## 用户修改要求（最高优先级——这是重画指令，与下方原画面提示冲突时一律以用户要求为准，逐字落实）\n" + instruction + "\n"
		briefLabel = "原画面提示（旧版，仅参考）"
	}
	goldenBlock := ""
	if golden != "" {
		goldenBlock = "\n## 结构金样例（结构/时序/canvas 写法照这个模式；视觉内容必须按本段重写）\n```html\n" + golden + "\n```\n"
	}
	return fmt.Sprintf(`为这一段视频直接写场景文件 HTML（你是画面工程师，版式/动效/图形程序全部自己写）。

## 契约摘要（完整版 docs/contract.md；违反任意一条会被 lint 拒收）
1. 整个文件 = 一个 <template> 片段，无 DOCTYPE/html/head/body
2. 根元素 data-composition-id="%s" data-width="%d" data-height="%d"；内部 #root 为舞台
3. 恰好一个 gsap.timeline({paused:true})，末尾注册 window.__timelines["%s"]
4. 全屏轨道层（背景等）带 class="clip" + data-start/data-duration/data-track-index
5. 每个有意义的可见元素带 data-hf-name="人话名"（≥3 个；元素级指认依赖它）
6. 确定性：禁 Date.now/new Date/setTimeout/setInterval/requestAnimationFrame/performance.now；
   禁 Math.random（随机一律种子化，如 __rng32(seed) 闭包）；禁 CSS animation/transition/@keyframes；
   禁网络（fetch/XHR/WebSocket/外链 http(s)）；禁 Worker；禁 <video>/<audio>
7. canvas 画布：绘制函数 = window["__draw_<id>"]=function(t,ctx,W,H,rng)——t 的纯函数，
   每帧从零重画；由 tl.to({},{duration,onUpdate:…调用 draw(t)…}},0) 驱动；运动一律写成 t 的函数
8. 字体：本地字体栈 + @font-face{src:local()} 声明（引用即声明）；
   禁止在 CSS transform 里做居中（translateX(-50%%) 等）再让 GSAP 补间同一元素——GSAP 会整体覆盖 CSS transform，居中必须用 GSAP 的 xPercent/yPercent
9. 不写字幕——底部字幕带由平台注入；内容留在上 83%%，禁止出现 data-ph=/data-w= 标注
10. 元素揭示随旁白节拍分布（对齐下方词序表，不前载不拖尾）；段总时长 %.3f 秒
11. 需要新框架（如 three.js）：在 <template> 内写注释声明 `+"`<!-- vend: 包名@精确版本 -->`"+`，
    平台自动采购并注入（模型不写任何 <script> 标签，含本地路径也不行）

## 段信息
- 标题：%s
- 旁白（%d 词，%.1f 秒）：%s
- %s：%s
%s
%s
%s
%s
## 词序表（reveal 对齐用）
%s

## 布局规则
%s
- 每屏至少一个视觉锚点（大字/图表/程序化画布/大 icon 之一），禁止全屏小元素平铺；
  段落适合铺氛围时优先写背景层（渐变/纹理/程序化画布），别停在纯色底
%s
%s
## 输出（只输出一个 JSON 对象：第一个字符必须是 {、最后一个字符必须是 }，前后不许有任何字、标签、围栏或解释）
{"note":"画面思路一句话","html":"<template>…整段场景文件…</template>","elements":[{"id":"元素DOM id","name":"人话名（与 data-hf-name 一致）"}]}
`, seg.ID, int(cv.W), int(cv.H), seg.ID, v.DurationS+contract.SegTail, seg.Key, len(v.Words), v.DurationS, seg.Narration, briefLabel, seg.VisualBrief,
		instrBlock, style, goldenBlock, sceneTpl, wb.String(), rules, compMethods, feedbackBlock(feedback))
}
