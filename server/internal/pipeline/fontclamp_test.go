package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"webpage-video-agent/server/internal/contract"
)

// 2026-10-11 夜跑实录的钉子：设计发明选了渲染端没有的字体（站酷庆科黄油体），
// skin.json 原样下发，字幕带机械装配没有「引用即声明」纪律，21 个字幕文件被
// 字体硬门一票全否。LockDesign 落盘前必须把花名过滤成渲染端可用的系统字体。
func TestClampFontStack(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"目录外花名过滤掉留雅黑", "'PingFang SC','Microsoft YaHei',sans-serif", "'Microsoft YaHei',sans-serif"},
		{"思源过滤留黑体", "'Source Han Serif SC','SimHei',serif", "'SimHei',serif"},
		{"干净栈原样", "'SimHei','Microsoft YaHei',sans-serif", "'SimHei','Microsoft YaHei',sans-serif"},
		{"全花名回退雅黑", "'PingFang SC','FZLanTingHei',serif", "'Microsoft YaHei',sans-serif"},
		{"去重保序", "SimHei, Arial, 'arial', Georgia, sans-serif", "'SimHei','Arial','Georgia',sans-serif"},
		{"纯西文栈补中文锚点", "Arial, Georgia, sans-serif", "'Microsoft YaHei',sans-serif"},
		{"空栈回退雅黑", "", "'Microsoft YaHei',sans-serif"},
	}
	for _, c := range cases {
		if got := clampFontStack(c.in, nil, nil); got != c.want {
			t.Errorf("%s: clampFontStack(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// 目录字体是钳制的例外：设计点名目录内字体（中文别名/大小写不限）时保留并
// 规范化为 CSS family 写法，登记进 matched 供 LockDesign 写准备申请书。
func TestClampFontStackCatalog(t *testing.T) {
	matched := map[string]*CatalogFont{}
	got := clampFontStack("'Zcool QingKe HuangYou','Microsoft YaHei',sans-serif", matched, nil)
	if got != "'ZCOOL QingKe HuangYou','Microsoft YaHei',sans-serif" {
		t.Errorf("目录字体应保留并规范化: %q", got)
	}
	if matched["ZCOOL QingKe HuangYou"] == nil || matched["ZCOOL QingKe HuangYou"].ID != "zcool-qingke-huangyou" {
		t.Errorf("目录字体未登记: %+v", matched)
	}
	got = clampFontStack("'站酷快乐体','SimHei',serif", matched, nil)
	if got != "'ZCOOL KuaiLe','SimHei',serif" {
		t.Errorf("中文别名应接得住: %q", got)
	}
	if matched["ZCOOL KuaiLe"] == nil {
		t.Errorf("中文别名未登记: %+v", matched)
	}
}

// LockDesign 落盘的 skin.json 是字幕字体源：带花名的样张确认后，落盘产物必须干净。
func TestLockDesignClampsFonts(t *testing.T) {
	p := NewProject(t.TempDir(), "clamp1")
	ss := designFixture()
	ss.Skins[0].Slots.Font = "'FZLanTingHei','Microsoft YaHei',sans-serif"
	ss.Fonts.Display.Stack = "'Source Han Serif SC','SimHei',sans-serif"
	ss.Confirmed = true
	if err := LockDesign(p, ss); err != nil {
		t.Fatal(err)
	}
	skinJSON := mustRead(t, p.Artifact("style/skin.json"))
	if strings.Contains(skinJSON, "FZLanTingHei") || strings.Contains(skinJSON, "Source Han") {
		t.Errorf("skin.json 仍含渲染端不可用字体：\n%s", skinJSON)
	}
	if !strings.Contains(skinJSON, "'Microsoft YaHei',sans-serif") {
		t.Errorf("skin.json 字体栈不是预期的钳制结果：\n%s", skinJSON)
	}
	designMD := mustRead(t, p.Artifact("style/design.md"))
	if strings.Contains(designMD, "Source Han") {
		t.Errorf("design.md 仍含渲染端不可用字体")
	}
	if _, err := os.Stat(p.Artifact("style/font_assets.request.json")); !os.IsNotExist(err) {
		t.Errorf("无目录字体时不应留准备申请书")
	}
	if !ss.Confirmed {
		t.Errorf("钳制不得动 confirmed 标志")
	}
	// 幂等：对已干净的栈再解析一次，designVersion 不得漂移
	v1 := designVersion(ss)
	if changed, matched := clampDesignFonts(ss); changed || len(matched) > 0 {
		t.Errorf("已干净的栈二次解析不应有改动")
	}
	if designVersion(ss) != v1 {
		t.Errorf("二次解析后 designVersion 漂移")
	}
}

// 设计点名目录字体：栈保留规范名 + 准备申请书落盘（font_tool.py 的输入）。
func TestLockDesignCatalogFont(t *testing.T) {
	p := NewProject(t.TempDir(), "catalog1")
	ss := designFixture()
	ss.Skins[0].Slots.Font = "'Zcool QingKe HuangYou','Microsoft YaHei',sans-serif"
	ss.Fonts.Display.Stack = "'站酷快乐体','Microsoft YaHei',sans-serif"
	ss.Confirmed = true
	if err := LockDesign(p, ss); err != nil {
		t.Fatal(err)
	}
	skinJSON := mustRead(t, p.Artifact("style/skin.json"))
	if !strings.Contains(skinJSON, "'ZCOOL QingKe HuangYou'") {
		t.Errorf("目录字体应保留规范名：\n%s", skinJSON)
	}
	req := mustRead(t, p.Artifact("style/font_assets.request.json"))
	for _, want := range []string{"zcool-qingke-huangyou", "zcool-kuaile", "api.github.com"} {
		if !strings.Contains(req, want) {
			t.Errorf("申请书缺 %q：\n%s", want, req)
		}
	}
	if !strings.Contains(mustRead(t, p.Artifact("style/design.md")), "ZCOOL KuaiLe") {
		t.Errorf("design.md 应带目录字体规范名")
	}
}

// 样张自装字体（custom）：clampFontStack 必须放行——否则 LockDesign 一钳，
// 自装字体进不了 design.md/skin.json，制作端丢失（样张与成片两张皮）。
func TestClampFontStackCustom(t *testing.T) {
	custom := map[string]bool{"fusion pixel test zh": true}
	got := clampFontStack("'Fusion Pixel Test zh','Microsoft YaHei',sans-serif", nil, custom)
	if got != "'Fusion Pixel Test zh','Microsoft YaHei',sans-serif" {
		t.Errorf("自装字体应按声明写法保留: %q", got)
	}
	// 未记账的同名变体（大小写不同不等于没记账——normalizeFontName 归一匹配）；
	// 真没记账的花名照剥
	got = clampFontStack("'SomeWildFont','Microsoft YaHei',sans-serif", nil, custom)
	if got != "'Microsoft YaHei',sans-serif" {
		t.Errorf("未记账花名应剥除: %q", got)
	}
}

// LockDesign 重写申请书时必须保留自装条目（并把 custom_fonts 的家族留在栈里），
// 只写目录匹配、整写覆盖 = 制作段 font_tool 空转、自装字体全丢。
func TestLockDesignKeepsCustomFonts(t *testing.T) {
	p := NewProject(t.TempDir(), "custom1")
	ss := designFixture()
	ss.Skins[0].Slots.Font = "'Fusion Pixel Test zh','Microsoft YaHei',sans-serif"
	ss.Fonts.Display.Stack = "'Zcool KuaiLe','Microsoft YaHei',sans-serif"
	ss.CustomFonts = []contract.CustomFont{{
		Family: "Fusion Pixel Test zh", License: "OFL",
		URL: "https://example.com/fonts/fusion-test.zip",
	}}
	ss.Confirmed = true
	if err := LockDesign(p, ss); err != nil {
		t.Fatal(err)
	}
	req := mustRead(t, p.Artifact("style/font_assets.request.json"))
	if !strings.Contains(req, "custom-") || !strings.Contains(req, "example.com/fonts/fusion-test.zip") {
		t.Errorf("申请书丢了自装条目：\n%s", req)
	}
	if !strings.Contains(mustRead(t, p.Artifact("style/design.md")), "'Fusion Pixel Test zh'") {
		t.Errorf("design.md 丢了自装字体")
	}
	if !strings.Contains(mustRead(t, p.Artifact("style/skin.json")), "'Fusion Pixel Test zh'") {
		t.Errorf("skin.json 丢了自装字体")
	}
	// 幂等：再来一次不漂移
	if err := LockDesign(p, ss); err != nil {
		t.Fatal(err)
	}
	if req2 := mustRead(t, p.Artifact("style/font_assets.request.json")); req2 != req {
		t.Errorf("申请书二次落盘漂移：\n%s\n%s", req, req2)
	}
}

// ClampSegmentFonts 是制作段的落盘钳：自装字体没放行会被剥掉，段帧回退雅黑。
func TestClampSegmentFontsKeepsCustom(t *testing.T) {
	writeFrame := func(p *Project, withLedger bool) {
		t.Helper()
		if withLedger {
			ssBytes, err := json.Marshal(contract.StyleSamples{CustomFonts: []contract.CustomFont{{
				Family: "Fusion Pixel Test zh", License: "OFL", URL: "https://example.com/f.ttf",
			}}})
			if err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, p.Artifact("style/style_samples.json"), string(ssBytes))
		}
		writeTestFile(t, p.Artifact("compositions/frames/seg01.html"),
			"<div style=\"font-family:'Fusion Pixel Test zh','Microsoft YaHei',sans-serif\">字</div>")
	}
	p := NewProject(t.TempDir(), "custom2")
	writeFrame(p, true)
	ClampSegmentFonts(p, "seg01")
	if out := mustRead(t, p.Artifact("compositions/frames/seg01.html")); !strings.Contains(out, "'Fusion Pixel Test zh'") {
		t.Errorf("段帧把自装字体剥掉了：%s", out)
	}
	// 无记账项目：同名字体照剥（旧行为不变）
	p2 := NewProject(t.TempDir(), "custom3")
	writeFrame(p2, false)
	ClampSegmentFonts(p2, "seg01")
	if out := mustRead(t, p2.Artifact("compositions/frames/seg01.html")); strings.Contains(out, "'Fusion Pixel Test zh'") {
		t.Errorf("未记账的自装字体应被剥除：%s", out)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
