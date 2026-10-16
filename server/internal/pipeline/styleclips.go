// styleclips.go —— 迷你真段（样张升级）：静态样张看不到动效/字幕带/字体渲染，
// 而确认门拍板的恰恰是「成片长什么样」。每条样张配一段 3s 动态小样：
// 同一份场景契约（与 produce/scenegen.go sceneUserPrompt 的正式段规则同源对齐；
// 改场景契约必须同步这里）→ ai/assemble_samples.py 套工程壳（字幕带/字体声明/
// 资产，与 assemble.py 同源逻辑）→ hyperframes 渲 mp4。
// --no-browser-gpu 强制软件光栅：三条小样走同一确定性档，互比与「所见」才成立
// （同 7f3b61d 回放门禁的教训：渲染器 auto-detect 会掷硬币）。
// 铁律：任何一环失败只把该条降级为静态预览（ClipNote 记原因），绝不阻塞确认门。
package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/llm"
)

// clipConcurrency 小样渲染并行度：SwiftShader 纯 CPU，2 并行是耗时/内存平衡点
// （3s@1080×1920 单条约几十秒，2-3 条并行控制在分钟级）。
const clipConcurrency = 2

// clipDuration 小样时长：3s 够走完一次完整揭示节拍，又把渲染压在几十秒。
const clipDuration = 3.0

type clipJob struct {
	idx       int    // 对应 ss.Samples 下标
	id        string // clip-0 / clip-1 …
	narration string // 字幕带文案（分镜真实旁白截句，不是模型编的）
}

type clipDraft struct {
	Tag  string `json:"tag"`
	Note string `json:"note"`
	HTML string `json:"html"`
}

type clipDrafts struct {
	Clips []clipDraft `json:"clips"`
}

// EnsureSampleClips 为样张补迷你真段：ss.Samples[i] ↔ clip-i 一一对应。
// 幂等：已有 mp4 的条目跳过，中途断电重跑只补缺的。返回是否有字段更新
// （调用方据此回写 style_samples.json）。
func EnsureSampleClips(p *Project, ss *contract.StyleSamples, sb *contract.Storyboard) bool {
	if len(ss.Samples) == 0 {
		return false
	}
	need := []int{}
	for i := range ss.Samples {
		if ss.Samples[i].Video == "" {
			need = append(need, i)
		}
	}
	if len(need) == 0 {
		return false
	}
	fail := func(why string) {
		p.Manifest("style.clips.error", why+"——样张保持静态，确认门不受影响")
	}
	prov, perr := llm.FromEnv(llm.RolePlan)
	if perr != nil {
		fail("LLM 不可用: " + perr.Error())
		return false
	}
	if sb == nil {
		sb, _ = p.LoadStoryboard() // 无分镜时字幕带没文案种子——小样仍可出（旁白留空）
	}
	cv := ProjectCanvas(p)
	p.Manifest("style.clips.start", fmt.Sprintf("%d 条 × %.1fs @%s", len(need), clipDuration, cv.Aspect))
	var drafts clipDrafts
	if _, err := prov.GenerateJSONNoThink(pctx(p), clipSystemPrompt(ss), clipUserPrompt(ss, sb, need, cv), &drafts); err != nil {
		fail("小样生成失败: " + err.Error())
		return false
	}

	clipsDir := p.Artifact("style/clips")
	if err := os.MkdirAll(clipsDir, 0o755); err != nil {
		fail("clips 目录创建失败: " + err.Error())
		return false
	}
	jobs := []clipJob{}
	for k, idx := range need {
		if k >= len(drafts.Clips) {
			break
		}
		d := drafts.Clips[k]
		if !clipHTMLPlausible(d.HTML) {
			ss.Samples[idx].ClipNote = "小样 HTML 没过机械校验（缺 template 片段/时间轴注册/含外链），这条显示静态版"
			continue
		}
		id := "clip-" + strconv.Itoa(idx)
		if err := writeFile(p.Artifact("style/clips/"+id+".html"), normalizeClipHTML(d.HTML, id)); err != nil {
			ss.Samples[idx].ClipNote = "小样落盘失败: " + err.Error()
			continue
		}
		ss.Samples[idx].ClipHTML = "style/clips/" + id + ".html"
		jobs = append(jobs, clipJob{idx: idx, id: id, narration: clipNarration(sb, idx, ss.Samples[idx].Desc)})
	}
	if len(jobs) == 0 {
		fail("没有一条小样通过机械校验")
		return true
	}

	// 装配清单 → python 套工程壳（字幕带/字体声明/资产，与正式 assemble.py 同源）
	list := make([]map[string]any, 0, len(jobs))
	for _, j := range jobs {
		list = append(list, map[string]any{"file": j.id + ".html", "narration": j.narration, "duration": clipDuration})
	}
	if err := writeFile(p.Artifact("style/clips/clips.json"), mustJSON(list)); err != nil {
		fail("装配清单写失败: " + err.Error())
		return true
	}
	if err := runPython(p, filepath.Join("ai", "assemble_samples.py"), p.Dir, 2*time.Minute); err != nil {
		fail("工程壳装配失败: " + err.Error())
		return true
	}

	// 渲染（并行度 2）：单条失败降级该条，成功记 Video；任务取消即整体收摊续渲
	sem := make(chan struct{}, clipConcurrency)
	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		go func(j clipJob) {
			defer wg.Done()
			if pctx(p).Err() != nil {
				return
			}
			sem <- struct{}{}
			defer func() { <-sem }()
			out := "style/clips/" + j.id + ".mp4"
			outLog, err := runCLI(p, "npx", "hyperframes@0.8.55", "render", ".",
				"-c", ".hf-clip/"+j.id+".html", "-o", out, "--no-browser-gpu")
			if err != nil {
				if pctx(p).Err() != nil { // 被取消：不算小样失败，下次续渲补上
					return
				}
				ss.Samples[j.idx].ClipNote = "小样渲染失败，这条显示静态版: " + FirstLine(tailLines(outLog, 6), 200)
				_ = os.Remove(p.Artifact(out))
				return
			}
			ss.Samples[j.idx].Video = out
			ss.Samples[j.idx].ClipNote = ""
		}(j)
	}
	wg.Wait()

	ok := 0
	for i := range ss.Samples {
		if ss.Samples[i].Video != "" {
			ok++
		}
	}
	if ok == 0 {
		fail("全部小样渲染失败")
	} else {
		p.Manifest("style.clips", fmt.Sprintf("%d/%d 条小样就绪（%s）", ok, len(ss.Samples), ss.Direction))
	}
	return true
}

// clipHTMLPlausible 机械校验：渲染必炸的三种形状直接不要（降级静态），不做修复轮回——
// 小样是加分项，为它烧修复轮不值；真想修，重跑阶段即重出。
func clipHTMLPlausible(html string) bool {
	h := strings.TrimSpace(html)
	if l := len(h); l < 200 || l > 80000 {
		return false
	}
	return strings.Contains(h, "data-composition-id") &&
		strings.Contains(h, "__timelines") &&
		!strings.Contains(h, "http://") && !strings.Contains(h, "https://")
}

// clipTplAttrRe 匹配被误写在 <template> 开标签上的组合物属性。
var clipTplAttrRe = regexp.MustCompile(`\s+(?:data-composition-id|data-width|data-height)\s*=\s*("[^"]*"|'[^']*')`)

// normalizeClipHTML 落盘侧机械纠偏：模型偶发把 data-composition-id/尺寸写在
// <template> 开标签上，而渲染器要求它们在 template 内部的第一个元素上
// （正式段形状：裸 <template> + 内部根 div 带 id，见 pipeline.go stageCompositions）。
// 这类形状错误不值得烧修复轮——落盘前直接搬正；形状本来对的原样返回。
func normalizeClipHTML(html, id string) string {
	t := strings.Index(strings.ToLower(html), "<template")
	if t < 0 {
		return html
	}
	te := strings.Index(html[t:], ">")
	if te < 0 {
		return html
	}
	te += t
	tag := html[t : te+1]
	if !strings.Contains(tag, "data-composition-id") {
		return html
	}
	props := ` data-composition-id="` + id + `"`
	if w := clipTplAttrVal(tag, "data-width"); w != "" {
		props += ` data-width="` + w + `"`
	}
	if h := clipTplAttrVal(tag, "data-height"); h != "" {
		props += ` data-height="` + h + `"`
	}
	// template 后跳过空白，找到第一个元素开标签的元素名（<后紧跟字母），属性插在元素名后
	ns, ne := -1, -1
	for i := te + 1; i < len(html)-1; i++ {
		c := html[i]
		if c == ' ' || c == '\n' || c == '\r' || c == '\t' {
			continue
		}
		if c == '<' && isASCIILetter(html[i+1]) {
			ns, ne = i+1, i+2
			for ne < len(html) && isNameChar(html[ne]) {
				ne++
			}
		}
		break
	}
	if ns < 0 {
		return html
	}
	return html[:t] + clipTplAttrRe.ReplaceAllString(tag, "") + html[te+1:ne] + props + html[ne:]
}

func isNameChar(c byte) bool {
	return isASCIILetter(c) || (c >= '0' && c <= '9') || c == '-' || c == '_'
}

// clipAttrRe 属性提取正则按属性名缓存（sync.Map）：clipTplAttrVal 每小样每属性
// 都调，包级 regexp.MustCompile 每次重编译是白付的。
var clipAttrCache sync.Map // 属性名 → *regexp.Regexp

func clipAttrRe(name string) *regexp.Regexp {
	if re, ok := clipAttrCache.Load(name); ok {
		return re.(*regexp.Regexp)
	}
	re := regexp.MustCompile(regexp.QuoteMeta(name) + `\s*=\s*("([^"]*)"|'([^']*)')`)
	clipAttrCache.Store(name, re)
	return re
}

// clipTplAttrVal 从开标签文本里取单个属性值（单双引号都接）。
func clipTplAttrVal(tag, name string) string {
	m := clipAttrRe(name).FindStringSubmatch(tag)
	if m == nil {
		return ""
	}
	if m[2] != "" {
		return m[2]
	}
	return m[3]
}

func isASCIILetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// segRefRe 样张 desc 里的展示段引用（「展示 seg06 裁判所成立章首页」）。
var segRefRe = regexp.MustCompile(`(?i)seg\s*0*(\d+)`)

// clipNarration 字幕带文案 = 分镜真实旁白截首句（超长截 32 rune 加省略号）。
// 小样的意义在「所见即所得」，字幕文案必须是真的，不许模型编。
// 段落取自样张 desc 点名的展示段（画面演的是那段，文案也得是那段——
// 2026-10-10 实测按样张下标直取会张冠李戴）；desc 没点名才退回样张下标。
func clipNarration(sb *contract.Storyboard, sampleIdx int, desc string) string {
	if sb == nil || len(sb.Segments) == 0 {
		return ""
	}
	i := sampleIdx
	if m := segRefRe.FindStringSubmatch(desc); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			id := fmt.Sprintf("seg%02d", n)
			for k, s := range sb.Segments {
				if s.ID == id {
					i = k
					break
				}
			}
		}
	}
	if i >= len(sb.Segments) {
		i = len(sb.Segments) - 1
	}
	n := strings.TrimSpace(strings.SplitN(sb.Segments[i].Narration, "\n", 2)[0])
	if j := strings.IndexAny(n, "。？！;；"); j >= 0 {
		n = strings.TrimSpace(n[:j])
	}
	r := []rune(n)
	if len(r) > 32 {
		n = string(r[:32]) + "……"
	}
	return n
}

// clipSegment 小样 i 的内容种子段（样张多于段数时收拢到最后一段）。
func clipSegment(sb *contract.Storyboard, sampleIdx int) *contract.Segment {
	if sb == nil || len(sb.Segments) == 0 {
		return nil
	}
	i := sampleIdx
	if i >= len(sb.Segments) {
		i = len(sb.Segments) - 1
	}
	return &sb.Segments[i]
}

// ProjectCanvas 项目画幅几何（与 produce.canvasFor 同一口径：project.json aspect，
// 缺省 16:9）。小样几何与风格工位的样张画布共用。
func ProjectCanvas(p *Project) contract.Canvas {
	aspect := "16:9"
	if b, err := os.ReadFile(p.Artifact("project.json")); err == nil {
		var meta struct {
			Aspect string `json:"aspect"`
		}
		if json.Unmarshal(b, &meta) == nil && meta.Aspect == "9:16" {
			aspect = "9:16"
		}
	}
	return contract.CanvasFor(aspect)
}

// runPython 跑 ai/ 侧装配脚本（与 produce.Runner.python 同款：仓库根为 CWD、
// 超时可取消、错误带尾部日志）。不走 runCLI——那是给 npx 的（会追加 npm 旗标）。
// 走 ctxCommand：取消时整棵进程树击杀，python 拉起的孙进程不残留。
func runPython(p *Project, script, projDir string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(pctx(p), timeout)
	defer cancel()
	cmd := ctxCommand(ctx, PythonExe(), filepath.Join(rootDir, script), projDir)
	cmd.Dir = rootDir
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil { // 被取消/超时杀掉：报可识别的 ctx 错误而非 killed
			return ctx.Err()
		}
		return fmt.Errorf("%s 退出: %w\n%s", script, err, TailLines(buf.String(), 15))
	}
	return nil
}

func clipSystemPrompt(ss *contract.StyleSamples) string {
	s := "你是视频的画面工程师，直接写可渲染的场景 HTML。只输出 JSON。风格：" + ss.Direction + "。"
	if ss.Device != nil && ss.Device.Name != "" {
		s += "贯穿装置「" + ss.Device.Name + "」是全片签名，小样必须呼应它。"
	}
	return s
}

// clipUserPrompt 场景契约块与 produce/scenegen.go sceneUserPrompt 的正式段规则同源
// （画幅安全区、canvas 接线三行、字体声明、字幕带避让、确定性禁令）；差异只有三处：
// 时长固定 3s、旁白是截句种子（字幕文案）、构图按对应样张的版式指定。
func clipUserPrompt(ss *contract.StyleSamples, sb *contract.Storyboard, need []int, cv contract.Canvas) string {
	vert := cv.Aspect == "9:16"
	rules := "- 内容安全区 x∈[0,1080]、y∈[0,1594]；底部 1594px 起是平台字幕带，禁放内容\n" +
		"- 竖屏 1080×1920：元素横向少列（≤2 列），纵向多行铺排；主视觉放 y≈300–1200"
	corners := "内容避开左上(x<280,y<150)与右上(x>800,y<150)两个角域"
	if !vert {
		rules = "- 内容安全区 x∈[0,1920]、y∈[0,896]；底部 896px 起是平台字幕带，禁放内容\n" +
			"- 横屏 1920×1080：主视觉放 y≈260–560 一带，层次拉开（主体 300px+、次级 120–200px）"
		corners = "内容避开左上(x<500,y<150)与右上(x>1420,y<150)两个角域"
	}

	var list strings.Builder
	for k, idx := range need {
		ref, layout := "", ""
		if idx < len(ss.Samples) {
			ref = fmt.Sprintf("样张「%s」（%s）的动态版", ss.Samples[idx].Tag, ss.Samples[idx].Desc)
		}
		if len(ss.Layouts) > 0 {
			layout = fmt.Sprintf("｜构图按版式「%s」", ss.Layouts[idx%len(ss.Layouts)].Name)
		}
		seed := ""
		if seg := clipSegment(sb, idx); seg != nil && strings.TrimSpace(seg.Narration) != "" {
			seed = fmt.Sprintf("｜旁白种子（字幕带文案，逐字用）：「%s」", clipNarration(sb, idx, ss.Samples[idx].Desc))
		}
		fmt.Fprintf(&list, "%d. data-composition-id=\"clip-%d\" ← %s%s%s\n", k+1, idx, ref, layout, seed)
	}

	return fmt.Sprintf(`为这一部片的风格样张写 %d 条「迷你真段」场景 HTML：每条 %.1f 秒、真实画幅，平台会用与正式片完全相同的渲染器渲成 mp4 给用户预览。用户看小样决定整套风格是否确认——小样必须就是成片某一段开头 3 秒的真的样子，不许做简化演示版。

## 场景契约（与正式段同一份；违反会被渲染器拒收）
1. 整个文件 = 一个 <template> 片段，无 DOCTYPE/html/head/body；
   <template> 标签本身不带任何属性
2. template 内部第一个元素（通常是最外层 div）带 data-composition-id="clip-<N>"
   data-width="%d" data-height="%d"；它内部的 #root 为舞台
3. 恰好一个 gsap.timeline({paused:true})，末尾注册 window.__timelines["clip-<N>"]
4. 全屏轨道层（背景等）带 class="clip" + data-start="0" data-duration="%.1f" data-track-index
5. 每个有意义的可见元素带 data-hf-name="人话名"（≥3 个）
6. 确定性：禁 Date.now/new Date/setTimeout/setInterval/requestAnimationFrame/performance.now；
   禁 Math.random（随机一律种子化，如 __rng32(seed) 闭包）；禁 CSS animation/transition/@keyframes；
   禁网络（fetch/XHR/WebSocket/外链 http(s)）；禁 Worker；禁 <video>/<audio>
7. canvas 画布：绘制函数 = window["__draw_<id>"]=function(t,ctx,W,H,rng)——t 的纯函数，
   每帧从零重画；运动一律写成 t 的函数。接线三行逐字照抄（只换尖括号占位），漏注册=拒收：
   var tl=gsap.timeline({paused:true});
   tl.to({},{duration:<小样总时长秒>,ease:"none",onUpdate:function(){window["__draw_<id>"](tl.time(),ctx,W,H,rng);}},0);
   window.__timelines["<小样id>"]=tl;
8. 字体只用设计系统给的字体栈；@font-face{src:local()} 引用即声明；
   禁止在 CSS transform 里做居中（translateX(-50%%) 等）再让 GSAP 补间同一元素——
   GSAP 会整体覆盖 CSS transform，居中必须用 GSAP 的 xPercent/yPercent
9. 不写字幕——底部字幕带由平台注入；内容留在上 83%%；顶部两角是平台角标保留区，%s
10. %.1f 秒走完一次完整揭示节拍：开场视觉锚点即亮，揭示随旁白语感分布，结尾不拖尾
11. 需要新框架（如 three.js）：在 <template> 内写注释声明 `+"`<!-- vend: 包名@精确版本 -->`"+`；
    但 %.1f 秒小样优先纯 CSS/DOM+gsap，别为 3 秒引大库

## 布局规则
%s
- 每屏至少一个视觉锚点（大字/图表/程序化画布/大 icon 之一），禁止全屏小元素平铺；
  适合铺氛围时优先写背景层（渐变/纹理/程序化画布），别停在纯色底

## 本片设计系统（定稿，逐字遵守）
%s
## 小样清单（%d 条，按序一一对应）
%s
## 输出（只输出一个 JSON 对象：第一个字符必须是 {、最后一个字符必须是 }，前后不许有任何字、标签、围栏或解释）
{"clips":[{"tag":"clip-0","note":"画面思路一句话","html":"<template>…整段场景文件…</template>"}]}
`, len(need), clipDuration, int(cv.W), int(cv.H), clipDuration, corners, clipDuration, clipDuration,
		rules, clipStyleBlock(ss), len(need), list.String())
}

// clipStyleBlock 设计系统摘要（样张 JSON 本体 → 文本注入）。
func clipStyleBlock(ss *contract.StyleSamples) string {
	var b strings.Builder
	b.WriteString("- 方向：" + ss.Direction + "\n")
	if ss.Concept != "" {
		b.WriteString("- 视觉概念：" + ss.Concept + "\n")
	}
	if skins := ss.EffectiveSkins(); len(skins) > 0 {
		sk := skins[0]
		sl := sk.Slots
		line := fmt.Sprintf("- 主皮肤「%s」槽位：bg=%s txt=%s accent=%s", sk.Name, sl.BG, sl.TXT, sl.Accent)
		if sl.Mute != "" {
			line += " mute=" + sl.Mute
		}
		if sl.Surface != "" {
			line += " surface=" + sl.Surface
		}
		b.WriteString(line + " font=" + sl.Font + "\n")
		if strings.TrimSpace(sk.CSS) != "" {
			b.WriteString("- 皮肤自由 CSS（质感/纹理/装饰按它来）：\n```css\n" + sk.CSS + "\n```\n")
		}
	}
	if ss.Fonts != nil {
		line := "- 字体三角色：display=" + ss.Fonts.Display.Stack + "；body=" + ss.Fonts.Body.Stack
		if ss.Fonts.Data.Stack != "" {
			line += "；data=" + ss.Fonts.Data.Stack
		}
		b.WriteString(line + "\n")
	}
	if ss.Motion != nil {
		b.WriteString(fmt.Sprintf("- 动效语言：缓动族 %s，进场 %.2fs/落定 %.2fs/错峰 %.2fs；%s\n",
			ss.Motion.Ease, ss.Motion.DurIn, ss.Motion.DurSettle, ss.Motion.Stagger, ss.Motion.Notes))
	}
	if ss.Device != nil {
		b.WriteString("- 贯穿装置「" + ss.Device.Name + "」：" + ss.Device.Desc +
			"；小样里的呼应：" + FirstLine(ss.Device.PerSeg, 120) + "\n")
	}
	if len(ss.Layouts) > 0 {
		parts := make([]string, 0, len(ss.Layouts))
		for _, l := range ss.Layouts {
			parts = append(parts, l.Name+"（"+l.Desc+"）")
		}
		b.WriteString("- 本片版式：" + strings.Join(parts, "；") + "\n")
	}
	if len(ss.AntiPatterns) > 0 {
		b.WriteString("- 负面清单（出现即返工）：" + strings.Join(ss.AntiPatterns, "；") + "\n")
	}
	return b.String()
}
