package pipeline

import (
	"os"
	"strings"
	"testing"

	"webpage-video-agent/server/internal/contract"
)

// 样张自装字体声明（<!-- font: Family | 协议 | 直链URL -->）解析的三钉子：
// 目录/系统同名的声明静默降级（目录钉版优先）、格式错回喂原文、家族去重。
func TestCustomFontDecls(t *testing.T) {
	html := `<html><head><style>
	body{font-family:'Fusion Pixel Test zh','Microsoft YaHei',sans-serif}
	<!-- font: Fusion Pixel Test zh | OFL | https://example.com/f.zip -->
	<!-- font: Fusion Pixel Test zh | OFL | https://example.com/f2.zip -->
	<!-- font: ZCOOL KuaiLe | OFL | https://example.com/dupe.ttf -->
	<!-- font: 微软雅黑 | OFL | https://example.com/yh.ttf -->
	<!-- font: Bad Font -->
	<!-- font: Bad Two | OFL | not-a-url -->
	</style></html>`
	customs, bad := CustomFontDecls(html)
	if len(customs) != 1 || customs[0].Family != "Fusion Pixel Test zh" ||
		customs[0].License != "OFL" || customs[0].URL != "https://example.com/f.zip" {
		t.Fatalf("声明解析不对: %+v", customs)
	}
	if len(bad) != 2 {
		t.Fatalf("格式错的声明应逐条回喂（缺段/非http 各一），得到 %d: %v", len(bad), bad)
	}
	// 目录字体（含中文别名）与系统字体的声明丢弃——首条去重取先声明者
	if strings.Contains(mustJSON(customs), "example.com/f2") {
		t.Errorf("重复家族应取首条声明")
	}
}

// 多张样张聚合：去重 + 按家族排序（design_system 记账顺序稳定，幂等可比）。
func TestCustomFontsFromHTMLs(t *testing.T) {
	out := CustomFontsFromHTMLs([]string{
		"<!-- font: Zpix Style | OFL | https://a.example/z.zip -->",
		"<!-- font: Alpha Pixel | OFL | https://a.example/a.zip -->",
		"<!-- font: Zpix Style | OFL | https://a.example/z.zip -->",
	})
	if len(out) != 2 || out[0].Family != "Alpha Pixel" || out[1].Family != "Zpix Style" {
		t.Fatalf("聚合去重排序不对: %+v", out)
	}
	if got := CustomFontsFromHTMLs([]string{"<div>无声明</div>"}); len(got) != 0 {
		t.Fatalf("无声明应返回空: %+v", got)
	}
}

// 申请书合并：目录匹配 + 自装条目同文件落盘；两者皆空才删文件——
// 只写目录、整写覆盖会把自装条目擦掉，制作段 font_tool 空转。
func TestWriteFontAssetRequestMerge(t *testing.T) {
	p := NewProject(t.TempDir(), "req1")
	matched := MatchSampleFonts([]string{"<style>body{font-family:'站酷快乐体'}</style>"})
	customs := []contract.CustomFont{{Family: "Fusion Pixel Test zh", License: "OFL", URL: "https://example.com/f.zip"}}
	if err := WriteFontAssetRequest(p, matched, customs); err != nil {
		t.Fatal(err)
	}
	req := mustRead(t, p.Artifact("style/font_assets.request.json"))
	for _, want := range []string{"zcool-kuaile", "custom-", "https://example.com/f.zip", "\"family\": \"Fusion Pixel Test zh\""} {
		if !strings.Contains(req, want) {
			t.Errorf("申请书缺 %q：\n%s", want, req)
		}
	}
	// 缓存键确定性：同 Family/URL 两次合成同 id/version
	id1, ver1 := customFontID("Fusion Pixel Test zh"), customFontVersion(customs[0].URL)
	if id2, ver2 := customFontID("fusion pixel test zh"), customFontVersion(" https://example.com/f.zip "); id1 != id2 || ver1 != ver2 {
		t.Errorf("缓存键不稳定: %s/%s vs %s/%s", id1, ver1, id2, ver2)
	}
	// 皆空删文件
	if err := WriteFontAssetRequest(p, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.Artifact("style/font_assets.request.json")); !os.IsNotExist(err) {
		t.Errorf("皆空时申请书应删除")
	}
	// 只有自装（无目录匹配）：申请书必须存在且不删
	if err := WriteFontAssetRequest(p, nil, customs); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.Artifact("style/font_assets.request.json")); err != nil {
		t.Errorf("仅自装时申请书应保留: %v", err)
	}
}

// 修复轮丢声明的钉子：submit 提取 = HTML 声明 ∪ 申请书账，声明侧 URL 优先。
func TestMergeCustomFontsWithRequest(t *testing.T) {
	p := NewProject(t.TempDir(), "req2")
	if err := WriteFontAssetRequest(p, nil, []contract.CustomFont{
		{Family: "ZCOOL XiaoWei", License: "OFL", URL: "https://example.com/xw.ttf"},
	}); err != nil {
		t.Fatal(err)
	}
	fromReq := CustomFontsFromRequest(p)
	if len(fromReq) != 1 || fromReq[0].Family != "ZCOOL XiaoWei" {
		t.Fatalf("申请书账读取不对: %+v", fromReq)
	}
	merged := MergeCustomFonts(
		[]contract.CustomFont{{Family: "Zpix Style", License: "OFL", URL: "https://a.example/z.zip"}},
		fromReq)
	if len(merged) != 2 {
		t.Fatalf("并集去重不对: %+v", merged)
	}
	// 同家族两源：声明侧 URL 优先
	merged = MergeCustomFonts(
		[]contract.CustomFont{{Family: "ZCOOL XiaoWei", License: "OFL", URL: "https://decl.example/xw.ttf"}},
		fromReq)
	if len(merged) != 1 || merged[0].URL != "https://decl.example/xw.ttf" {
		t.Fatalf("同家族应取声明侧: %+v", merged)
	}
}
