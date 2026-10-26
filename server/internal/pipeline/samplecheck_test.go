package pipeline

import (
	"strings"
	"testing"

	"webpage-video-agent/server/internal/contract"
)

func TestCheckSampleHTMLViewport(t *testing.T) {
	html := `<html><body><div style="position:absolute;left:10vw;top:100px">字</div></body></html>`
	_, issues := CheckSampleHTML(html, SampleCanvas{W: 1080, H: 1920}, nil)
	found := false
	for _, s := range issues {
		if strings.Contains(s, "vw/vh") {
			found = true
		}
	}
	if !found {
		t.Fatalf("视口单位没被抓到: %v", issues)
	}
	// script 内的 vw/vh 合法（JS 计算），不该报
	html2 := `<html><body><script>const x = el.clientWidth * 0.5 + 'vw'</script></body></html>`
	if _, issues2 := CheckSampleHTML(html2, SampleCanvas{W: 1080, H: 1920}, nil); len(issues2) != 0 {
		t.Fatalf("script 内视口单位误报: %v", issues2)
	}
}

func TestCheckSampleHTMLCanvas(t *testing.T) {
	noDraw := `<html><body><canvas id="c"></canvas><script>const g=c.getContext('2d')</script></body></html>`
	_, issues := CheckSampleHTML(noDraw, SampleCanvas{W: 1080, H: 1920}, nil)
	if len(issues) == 0 {
		t.Fatal("canvas 无 __draw_ 纯函数没被抓到")
	}
	withDraw := `<html><body><canvas id="c"></canvas><script>` +
		`window.__draw_grid=function(t){};window.__draw_grid(0)</script></body></html>`
	fixed, issues2 := CheckSampleHTML(withDraw, SampleCanvas{W: 1080, H: 1920}, nil)
	if len(issues2) != 0 {
		t.Fatalf("合法 __draw_ 被误报: %v", issues2)
	}
	// 模型自调也要补 bootstrap（截图靠 load 统一驱动）——恰好注入一次，且幂等
	if n := strings.Count(fixed, "__draw_autoshot__"); n != 1 {
		t.Fatalf("bootstrap 应恰好注入一次, got %d", n)
	}
	fixed2, _ := CheckSampleHTML(fixed, SampleCanvas{W: 1080, H: 1920}, nil)
	if strings.Count(fixed2, "__draw_autoshot__") != 1 {
		t.Fatal("已带标记的 html 被重复注入 bootstrap")
	}
	// 箭头函数/任意参数名也是合法纯函数（只认 function(t 会误报打回）
	arrow := `<html><body><canvas id="c"></canvas><script>window.__draw_grid=(t)=>{}</script></body></html>`
	if _, iss := CheckSampleHTML(arrow, SampleCanvas{W: 1080, H: 1920}, nil); len(iss) != 0 {
		t.Fatalf("箭头函数纯函数被误报: %v", iss)
	}
}

func TestCheckSampleHTMLFontFamilies(t *testing.T) {
	html := `<html><style>body{font-family:'Zpix',monospace}</style><body><div>字</div></body></html>`
	_, issues := CheckSampleHTML(html, SampleCanvas{W: 1080, H: 1920}, nil)
	hit := false
	for _, s := range issues {
		if strings.Contains(s, "Zpix") && strings.Contains(s, "Fusion Pixel") {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("不可解析字体没被抓到或回喂没列目录: %v", issues)
	}
	// 目录字体按可解析放行
	ok := `<html><body><div style="font-family:'Fusion Pixel 12px Prop zh-Hans',monospace">字</div></body></html>`
	for _, s := range func() []string {
		_, iss := CheckSampleHTML(ok, SampleCanvas{W: 1080, H: 1920}, nil)
		return iss
	}() {
		if strings.Contains(s, "字体族") {
			t.Fatalf("目录字体被误报: %v", s)
		}
	}
}

func TestCheckSampleHTMLTextOverlap(t *testing.T) {
	// 复刻 2026-10-21 实测：状态栏三层文字互相压叠
	html := `<html><body>` +
		`<div style="position:absolute;left:40px;top:1800px;width:120px;height:60px;font-size:28px">07:05</div>` +
		`<div style="position:absolute;left:48px;top:1812px;width:110px;height:50px;font-size:24px">LV.1HP</div>` +
		`<div style="position:absolute;left:56px;top:1824px;width:100px;height:40px;font-size:20px">你 68/100</div>` +
		`</body></html>`
	_, issues := CheckSampleHTML(html, SampleCanvas{W: 1080, H: 1920}, nil)
	if len(issues) == 0 || !strings.Contains(issues[len(issues)-1], "压叠") {
		t.Fatalf("文字压叠没被抓到: %v", issues)
	}
	// 声明叠层的放过（声明元素不参与配对）
	htmlOK := `<html><body>` +
		`<div style="position:absolute;left:40px;top:1800px;width:120px;height:60px;font-size:28px">07:05</div>` +
		`<div data-layout-allow-overlap style="position:absolute;left:48px;top:1812px;width:110px;height:50px;font-size:24px">LV.1HP</div>` +
		`</body></html>`
	if _, issues2 := CheckSampleHTML(htmlOK, SampleCanvas{W: 1080, H: 1920}, nil); len(issues2) != 0 {
		t.Fatalf("声明叠层被误报: %v", issues2)
	}
	// 文字压在无字装饰块上不算叠
	htmlDeco := `<html><body>` +
		`<div style="position:absolute;left:40px;top:1800px;width:400px;height:80px;background:#111"></div>` +
		`<div style="position:absolute;left:56px;top:1812px;width:200px;height:40px;font-size:24px">你 68/100</div>` +
		`</body></html>`
	if _, issues3 := CheckSampleHTML(htmlDeco, SampleCanvas{W: 1080, H: 1920}, nil); len(issues3) != 0 {
		t.Fatalf("文字压装饰块被误报: %v", issues3)
	}
}

func TestCheckSampleHTMLClassLayoutOverlap(t *testing.T) {
	// 2026-10-21 验收实录：class 布局（.vs 徽章绝对定位压住两栏面板文字）只查
	// 行内样式会漏——<style> 单类规则必须一并解析。
	html := `<html><head><style>
		.colA{position:absolute;left:60px;top:340px;width:430px;height:1200px}
		.vs{position:absolute;left:420px;top:900px;width:108px;height:70px;font-size:40px}
		</style></head><body>` +
		`<section class="colA">睡够了的人 HP 20/20 第二天开局就是满血</section>` +
		`<div class="vs">VS</div>` +
		`</body></html>`
	_, issues := CheckSampleHTML(html, SampleCanvas{W: 1080, H: 1920}, nil)
	hit := false
	for _, s := range issues {
		if strings.Contains(s, "压叠") && strings.Contains(s, "VS") {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("class 布局的 VS 压字没被抓到: %v", issues)
	}
}

func TestCheckSampleHTMLCaseChangingChars(t *testing.T) {
	// İ 小写化后字节变长：整篇 ToLower 当坐标会错位/越界（2026-10-21 巡检 a 项）。
	// 大小写混淆字符在前、定位盒在后，闭合扫描必须还找得准。
	html := `<html><body><p>İİİ ŞŞŞ ĞĞĞ</p>` +
		`<div style="position:absolute;left:40px;top:100px;width:300px;height:80px">时间轴标题</div>` +
		`<div style="position:absolute;left:60px;top:130px;width:280px;height:70px">压住的字幕</div>` +
		`</body></html>`
	_, issues := CheckSampleHTML(html, SampleCanvas{W: 1080, H: 1920}, nil)
	hit := false
	for _, s := range issues {
		if strings.Contains(s, "压叠") {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("大小写变长字符之后的盒子定位失效（压叠没抓到）: %v", issues)
	}
}

func TestCheckSampleHTMLRightAnchorNoWidth(t *testing.T) {
	// 右锚缺宽的盒向左伸展、宽度不可知——旧逻辑硬算出贴右缘的幽灵盒。
	// 这里右锚装饰条与左上的文字盒实际不相交，不该报叠。
	html := `<html><body>` +
		`<div style="position:absolute;right:20px;bottom:30px;height:8px;background:#c33"></div>` +
		`<div style="position:absolute;left:40px;top:100px;width:300px;height:80px">正文文字</div>` +
		`</body></html>`
	if _, issues := CheckSampleHTML(html, SampleCanvas{W: 1080, H: 1920}, nil); len(issues) != 0 {
		t.Fatalf("右锚缺宽幽灵盒误报: %v", issues)
	}
	// 右下锚带宽高：左上角必须反推（x=W-r-w, y=H-b-h）。宽带盒真实横跨
	// [40,1040]，与左侧文字盒相交；旧算法 x=W-r 把它平移到 [1040,2040] 出画布，叠字漏报。
	htmlW := `<html><body>` +
		`<div style="position:absolute;right:40px;bottom:1710px;width:1000px;height:80px">右侧宽幅文字</div>` +
		`<div style="position:absolute;left:100px;top:130px;width:300px;height:70px">左侧文字</div>` +
		`</body></html>`
	_, issues := CheckSampleHTML(htmlW, SampleCanvas{W: 1080, H: 1920}, nil)
	hit := false
	for _, s := range issues {
		if strings.Contains(s, "压叠") {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("右锚带宽的盒子没参与判定: %v", issues)
	}
}

func TestCheckSampleHTMLStripTagAttrText(t *testing.T) {
	// 盒内是 <img>（属性里的字母不算文字）：装饰图盒不该被当文本盒参与压叠。
	html := `<html><body>` +
		`<div style="position:absolute;left:40px;top:100px;width:300px;height:80px"><img src="pic.png" alt="hero"></div>` +
		`<div style="position:absolute;left:60px;top:130px;width:280px;height:70px">正文文字</div>` +
		`</body></html>`
	if _, issues := CheckSampleHTML(html, SampleCanvas{W: 1080, H: 1920}, nil); len(issues) != 0 {
		t.Fatalf("img 属性字母被当文字误报叠: %v", issues)
	}
}

func TestUnresolvedDesignFonts(t *testing.T) {
	ss := &contract.StyleSamples{
		Fonts: &contract.FontRoles{
			Display: contract.FontRole{Stack: "'Zpix', monospace"},
			Body:    contract.FontRole{Stack: "Noto Sans SC, sans-serif"},
		},
	}
	bad := UnresolvedDesignFonts(ss, nil)
	if len(bad) != 1 || bad[0] != "Zpix" {
		t.Fatalf("字体闸清单不对: %v", bad)
	}
	// 点名目录字体不算没有
	ss.Fonts.Display.Stack = "'Fusion Pixel 12px Prop zh-Hans', monospace"
	if bad = UnresolvedDesignFonts(ss, nil); len(bad) != 0 {
		t.Fatalf("目录字体被误判: %v", bad)
	}
	// 样张自装字体：ensure 进 font_assets.json（assets）后放行——否则字体闸
	// assets=nil 误杀，自装字体进不了设计系统
	ss.Fonts.Display.Stack = "'Fusion Pixel Test zh', monospace"
	assets := map[string]string{"fusion pixel test zh": "data:font/woff2;base64,xxx"}
	if bad = UnresolvedDesignFonts(ss, assets); len(bad) != 0 {
		t.Fatalf("已 ensure 的自装字体被误杀: %v", bad)
	}
	if bad = UnresolvedDesignFonts(ss, nil); len(bad) != 1 || bad[0] != "Fusion Pixel Test zh" {
		t.Fatalf("未 ensure 的自装字体应打回: %v", bad)
	}
}
