package pipeline

// samplecheck.go —— 样张 HTML 机械检查（prep② 渲染验证管线的机检位）。
// 与成片侧 hyperframes check 不同源：样张是完整文档（非 <template> 段工程），
// 整套 lint 跑不了；这里只搬对样张有意义的确定性检查 + 机械修复：
//   1. 字体声明补齐（ensureFontFaces，与成片同源；目录字体补真字体 data URL）
//   2. 视口单位禁令（scene_lint F6 同规则：样张画幅固定像素，vw/vh 必错）
//   3. canvas 纯函数模式（scene_lint T4 同规则）；Edge 截图不走平台渲染器、
//      没人驱动 __draw_——自动补一段 t=0 直调 bootstrap，保截图有画面
//   4. 字体族可解析（目录字体/系统白名单/通用族之外 = 渲染端静默回退，齐马蓝教训）
//   5. 文字盒重叠检测（绝对定位文本元素两两求交——2026-10-21 呼啸骑士战实测
//      状态栏三层叠字三张全中且模型看图自检没抓住，必须机械拦）
// 修不了的进 issues 清单回喂修复轮。启发式局限：只认内联 style 的绝对定位元素，
// class 布局查不到——样张由模型现写、以行内样式为主，实测够用。

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"webpage-video-agent/server/internal/contract"
)

// 样张画幅（CheckSampleHTML 用于 right/bottom 定位换算；与 snap_samples 同口径）
type SampleCanvas struct{ W, H int }

var (
	reScriptBlock = regexp.MustCompile(`(?is)<script.*?</script>`)
	reViewportUse = regexp.MustCompile(`\b\d+(?:\.\d+)?(?:vw|vh)\b`)
	reCanvasTag   = regexp.MustCompile(`(?i)<canvas\b`)
	// function/箭头都认（function(t)/(time)、(t)=>、t=>、async 变体）——只认
	// 「function(t」会把合法纯函数误判成没有，打回重修（2026-10-21 巡检）。
	reDrawDef  = regexp.MustCompile(`(?i)window\s*(?:\.\s*__draw_\w+|\[\s*["']__draw_\w+["']\s*\])\s*=\s*(?:async\s+)?(?:function\b|\([^)]*\)\s*=>|[A-Za-z_$][\w$]*\s*=>)`)
	reCloseTag = regexp.MustCompile(`(?i)</([a-z][a-z0-9]*)\s*>`)
	// 元素抓取：RE2 没有反向引用，开标签扫出后手找同名闭合。样张以扁平行内样式
	// 为主，取第一个同名闭合（不嵌套）——启发式够用。
	reOpenTag   = regexp.MustCompile(`(?is)<([a-z][a-z0-9]*)\b([^>]*)>`)
	reCSSRule   = regexp.MustCompile(`(?is)\.([A-Za-z][\w-]*)\s*\{([^}]*)\}`)
	reClassAttr = regexp.MustCompile(`(?i)class\s*=\s*"([^"]*)"`)
	reStyleAttr = regexp.MustCompile(`(?i)style\s*=\s*"([^"]*)"`)
	reAbsPos    = regexp.MustCompile(`(?i)position\s*:\s*(?:absolute|fixed)`)
	reOverlapOK = regexp.MustCompile(`(?i)data-layout-allow-overlap(?:\s*=|\s|$)`)
	reCSSLenPx  = regexp.MustCompile(`(?i)(left|top|width|height|right|bottom)\s*:\s*(-?[\d.]+)(px|%)`)
	reTranslate = regexp.MustCompile(`(?i)translate\s*\(\s*(-?[\d.]+)(%|px)?\s*[,\s]\s*(-?[\d.]+)(%|px)?\s*\)`)
	reTextWord  = regexp.MustCompile(`[\p{Han}\p{Latin}\p{Nd}]`)
	// 文本盒取词前剥标签/实体：img 的 src 属性字母、「&nbsp;」不该算文字
	//（不剥会把装饰图属性误判成文本参与压叠配对）。
	reAnyTag = regexp.MustCompile(`(?s)<[^>]*>`)
	reEntity = regexp.MustCompile(`&[a-zA-Z][a-zA-Z0-9]*;|&#\d+;|&#x[0-9a-fA-F]+;`)
)

// genericFontFamilies CSS 通用族：任何渲染端都有，永不告警。
var genericFontFamilies = map[string]bool{
	"monospace": true, "serif": true, "sans-serif": true,
	"cursive": true, "fantasy": true, "system-ui": true, "ui-monospace": true,
}

// fontFamiliesUsable 样张里能落到真字体的字体族：通用族/系统白名单/目录字体/
// font_assets 已解析产物。返回 false = 渲染端必静默回退。
func fontFamiliesUsable(name string, assets map[string]string) bool {
	n := normalizeFontName(strings.Trim(name, "'\""))
	if n == "" || genericFontFamilies[n] || osFontWhitelist[n] {
		return true
	}
	if _, ok := assets[n]; ok {
		return true
	}
	return matchCatalogFont(n) != nil
}

// sampleBoxAbsolute 定位盒（px，右下锚点已换算）；ok=false=信息不全不判。
type sampleBox struct {
	x, y, w, h float64
	text       string
	skip       bool // 带 data-layout-allow-overlap
}

func cssLen(v, unit string, total int) float64 {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0
	}
	if unit == "%" {
		return f / 100 * float64(total)
	}
	return f
}

// sampleBoxes 抓绝对定位元素（行内样式或 <style> 单类规则——2026-10-21 验收
// 实测模型爱用 class 布局，.vs 徽章压字只查行内会漏）。子元素文本并入父盒
// （重叠按外盒判）。
func sampleBoxes(html string, cv SampleCanvas) []sampleBox {
	// <style> 块里的单类规则（.vs{position:absolute;...}）；嵌套/组合选择器不认。
	css := map[string]string{}
	for _, m := range reCSSRule.FindAllStringSubmatch(html, -1) {
		if _, dup := css[m[1]]; !dup {
			css[m[1]] = m[2]
		}
	}
	var out []sampleBox
	for _, m := range reOpenTag.FindAllStringSubmatchIndex(html, -1) {
		tag := strings.ToLower(html[m[2]:m[3]])
		attrs := html[m[4]:m[5]]
		// 定位样式来源：行内 style 与 <style> 单类规则**合并**（行内在前=同属性
		// 优先）——实测模型常把 left 写行内、top/width/height 留在类里（.col 实录），
		// 只取其一盒子凑不齐就漏检。
		style := ""
		if sm := reStyleAttr.FindStringSubmatch(attrs); sm != nil {
			style = sm[1]
		}
		if cm := reClassAttr.FindStringSubmatch(attrs); cm != nil {
			for _, cls := range strings.Fields(cm[1]) {
				if rule, ok := css[cls]; ok {
					style = style + ";" + rule
				}
			}
		}
		if style == "" || !reAbsPos.MatchString(style) {
			continue
		}
		// 同名闭合（不嵌套假设）：开标签后第一个同名闭合。必须在原文上做大小写
		// 不敏感扫描——整篇 ToLower 会改字节长度（İ 类字符），拿它当 html 坐标
		// 会错位甚至越界（2026-10-21 巡检 a 项）。
		rest := html[m[1]:]
		closeAt := -1
		for _, cm := range reCloseTag.FindAllStringSubmatchIndex(rest, -1) {
			if strings.EqualFold(rest[cm[2]:cm[3]], tag) {
				closeAt = cm[0]
				break
			}
		}
		if closeAt < 0 {
			continue
		}
		inner := html[m[1] : m[1]+closeAt]
		box := sampleBox{skip: reOverlapOK.MatchString(attrs)}
		get := func(prop string) (float64, string, bool) {
			mm := reCSSLenPx.FindAllStringSubmatch(style, -1)
			for _, g := range mm {
				if strings.EqualFold(g[1], prop) {
					return cssLen(g[2], g[3], 100), g[3], true
				}
			}
			return 0, "", false
		}
		left, leftU, hasL := get("left")
		top, topU, hasT := get("top")
		w, wU, hasW := get("width")
		h, hU, hasH := get("height")
		// 锚点换算：left/top 直读左上角；right/bottom 锚给的是右/下缘，左上角
		// 必须反推（x=W-r-w）——旧代码 x=W-r 把整盒平移出画布右缘，缺宽时更
		// 得出贴右缘的幽灵盒（2026-10-21 巡检）。
		px := func(v float64, unit string, total int) float64 {
			if unit == "%" {
				return v / 100 * float64(total)
			}
			return v
		}
		if hasL && hasT {
			box.x, box.y = px(left, leftU, cv.W), px(top, topU, cv.H)
			if hasW {
				box.w = px(w, wU, cv.W)
			} else {
				box.w = float64(cv.W) - box.x // 块级元素缺 width 撑满到右缘
			}
			if !hasH {
				continue // 无高度无法判交叠（自适应文本盒），放过
			}
			box.h = px(h, hU, cv.H)
		} else {
			r, rU, hasR := get("right")
			b, bU, hasB := get("bottom")
			// 右/下锚缺宽高：真实盒从锚点向左/上伸展，位置不可知——硬算必出
			// 幽灵盒（误报漏报都来），同无高度一样放过。
			if !hasR || !hasB || !hasW || !hasH {
				continue
			}
			box.w, box.h = px(w, wU, cv.W), px(h, hU, cv.H)
			box.x = float64(cv.W) - px(r, rU, cv.W) - box.w
			box.y = float64(cv.H) - px(b, bU, cv.H) - box.h
		}
		if t := reTranslate.FindStringSubmatch(style); t != nil {
			dx, dy := cssLen(t[2], t[3], int(box.w)), cssLen(t[4], t[5], int(box.h))
			box.x += dx
			box.y += dy
		}
		box.text = strings.TrimSpace(reEntity.ReplaceAllString(
			reAnyTag.ReplaceAllString(reScriptBlock.ReplaceAllString(inner, ""), " "), " "))
		out = append(out, box)
	}
	return out
}

// CheckSampleHTML 样张机械检查：返回机械修复后的 HTML 与修不了的 issue 清单
// （人话，直接回喂修复轮）。assets = 目录字体解析产物（可 nil——提交前的
// 草稿渲染还没有真字体，目录字体按可解析放行，截图侧补 local 兜底）。
func CheckSampleHTML(html string, cv SampleCanvas, assets map[string]string) (string, []string) {
	var issues []string

	// 1) 字体声明补齐（机械修复，幂等）
	html = ensureFontFaces(html, assets)

	// 2) 视口单位（script 内合法，剥掉再查）
	if reViewportUse.MatchString(reScriptBlock.ReplaceAllString(html, "")) {
		found := reViewportUse.FindAllString(reScriptBlock.ReplaceAllString(html, ""), 5)
		issues = append(issues, fmt.Sprintf("禁 vw/vh 视口单位（画幅固定 %d×%d px）：%v——全部换成 px", cv.W, cv.H, found))
	}

	// 3) canvas 纯函数 + 自动补 t=0 直调（Edge 截图没人驱动时间轴）
	if reCanvasTag.MatchString(html) {
		if !reDrawDef.MatchString(html) {
			issues = append(issues, "有 <canvas> 但没有 window.__draw_xxx=function(t,…) 参数化绘制纯函数——把绘制逻辑改成 __draw_ 纯函数（t=像素画帧参数）")
		} else if !strings.Contains(html, "__draw_autoshot__") {
			boot := `<script>/*__draw_autoshot__*/window.addEventListener('load',function(){` +
				`Object.keys(window).filter(function(k){return k.indexOf('__draw_')===0}).` +
				`forEach(function(k){try{window[k](0)}catch(e){}})});</script>`
			if i := strings.LastIndex(strings.ToLower(html), "</body>"); i >= 0 {
				html = html[:i] + boot + html[i:]
			} else {
				html += boot
			}
		}
	}

	// 4) 字体族可解析
	bad := map[string]bool{}
	for _, re := range []*regexp.Regexp{reFontFamilyUse, reFontFamilyUseDQ} {
		for _, m := range re.FindAllStringSubmatch(html, -1) {
			for _, name := range strings.Split(m[1], ",") {
				name = strings.TrimSpace(strings.Trim(name, "'\""))
				if name != "" && !fontFamiliesUsable(name, assets) {
					bad[name] = true
				}
			}
		}
	}
	if len(bad) > 0 {
		names := make([]string, 0, len(bad))
		for n := range bad {
			names = append(names, n)
		}
		sort.Strings(names)
		issues = append(issues, fmt.Sprintf("字体族渲染端没有（会静默回退雅黑，设计字体进不了画面）：%s——只用目录字体：%s，或系统字体：微软雅黑/黑体/宋体/楷体，或 monospace",
			strings.Join(names, "、"), FontCatalogList()))
	}

	// 5) 文字盒重叠（只判文本元素 × 文本元素；声明叠层的放过）
	boxes := sampleBoxes(html, cv)
	type pair struct{ a, b sampleBox }
	var hits []pair
	for i := range boxes {
		if boxes[i].skip || !reTextWord.MatchString(boxes[i].text) {
			continue
		}
		for j := i + 1; j < len(boxes); j++ {
			if boxes[j].skip || !reTextWord.MatchString(boxes[j].text) {
				continue
			}
			ox := math_min(boxes[i].x+boxes[i].w, boxes[j].x+boxes[j].w) - math_max(boxes[i].x, boxes[j].x)
			oy := math_min(boxes[i].y+boxes[i].h, boxes[j].y+boxes[j].h) - math_max(boxes[i].y, boxes[j].y)
			if ox <= 6 || oy <= 6 { // 允许贴边描边级别的接触
				continue
			}
			smaller := math_min(boxes[i].w*boxes[i].h, boxes[j].w*boxes[j].h)
			if smaller <= 0 || ox*oy < smaller*0.2 { // 压住小盒 20% 以上才算真叠
				continue
			}
			hits = append(hits, pair{boxes[i], boxes[j]})
		}
	}
	if len(hits) > 0 {
		msgs := make([]string, 0, 3)
		for _, p := range hits {
			if len(msgs) == 3 {
				break
			}
			msgs = append(msgs, fmt.Sprintf("「%s」×「%s」",
				firstRunes(p.a.text, 16), firstRunes(p.b.text, 16)))
		}
		more := ""
		if len(hits) > 3 {
			more = fmt.Sprintf("（共 %d 处）", len(hits))
		}
		issues = append(issues, "文字元素互相压叠"+more+"："+strings.Join(msgs, "；")+"——错开位置或缩小字号，任何文字被别的文字压住都不可读")
	}
	return html, issues
}

func math_min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func math_max(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// LoadSampleFontAssets 目录字体解析产物（family 小写 → woff2 data URL）。
// prep② 渲染验证管线用；无产物（font_assets.json 缺/未 ensure）返回 nil。
func LoadSampleFontAssets(p *Project) map[string]string {
	return loadFontAssets(p)
}

// UnresolvedDesignFonts 设计系统字体栈里渲染端解析不了的字体族（prep② submit_style
// 字体闸的回喂清单）。目录字体/系统白名单/通用族之外的一律算没有——渲染端会
// 静默回退雅黑，设计字体进不了画面（齐马蓝教训）。
func UnresolvedDesignFonts(ss *contract.StyleSamples) []string {
	bad := map[string]bool{}
	collect := func(stack string) {
		for _, name := range strings.Split(stack, ",") {
			name = strings.TrimSpace(name)
			name = strings.Trim(name, "'\"")
			if name == "" || fontFamiliesUsable(name, nil) {
				continue
			}
			bad[name] = true
		}
	}
	if ss.Fonts != nil {
		collect(ss.Fonts.Display.Stack)
		collect(ss.Fonts.Body.Stack)
		collect(ss.Fonts.Data.Stack)
	}
	for i := range ss.Skins {
		collect(ss.Skins[i].Slots.Font)
	}
	if ss.Skin != nil {
		collect(ss.Skin.Slots.Font)
	}
	out := make([]string, 0, len(bad))
	for n := range bad {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// firstRunes 前 n 个 rune（超长加 …）——回喂文案截断用。
func firstRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

// PrepareSamplePage 动态样张页面的机械整形（prep② 渲染前的确定性补齐，幂等）：
//   - gsap/hf.js 资产引用缺失时注入 <head>（模型漏写=渲染器没有 gsap 必炸，纯样板代码
//     不值得烧修复轮）；
//   - 根元素缺 data-duration 时补样张时长（hyperframes 按它定录制时长）。
//
// 动画本体（timeline/__timelines 注册）不在这里补——那是有内容的创作决定，缺了进
// 机检 issues 回喂模型重写。
func PrepareSamplePage(html string, dur float64) string {
	if !strings.Contains(html, "assets/gsap.min.js") {
		inject := "<script src=\"assets/gsap.min.js\"></script>\n    <script src=\"assets/hf.js\"></script>"
		if i := strings.Index(strings.ToLower(html), "<head>"); i >= 0 {
			j := i + len("<head>")
			html = html[:j] + "\n    " + inject + html[j:]
		} else if i := strings.Index(strings.ToLower(html), "<html"); i >= 0 {
			if j := strings.Index(html[i:], ">"); j >= 0 {
				j += i + 1
				html = html[:j] + "\n  <head>" + inject + "</head>" + html[j:]
			}
		}
	}
	if !sampleRootDurRe.MatchString(html) {
		html = sampleRootRe.ReplaceAllStringFunc(html, func(m string) string {
			if !strings.Contains(m, "data-duration") {
				return strings.TrimSuffix(m, ">") + fmt.Sprintf(" data-duration=\"%.2f\"", dur) + ">"
			}
			return m
		})
	}
	return html
}

// sampleRootRe/sampleRootDurRe 根元素定位（data-composition-id="main" 的开标签）与
// duration 已存在判定。
var (
	sampleRootRe    = regexp.MustCompile(`(?i)<div[^>]*data-composition-id=["']main["'][^>]*>`)
	sampleRootDurRe = regexp.MustCompile(`(?i)<div[^>]*data-composition-id=["']main["'][^>]*data-duration`)
)
