package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const ffFixture = `<template><style>
@font-face{font-family:'Segoe UI';src:local('Segoe UI');}
#t{font-family:'Segoe UI','SimHei',sans-serif;}
#n{font-family: "Arial Narrow", Georgia, serif;}
#u{font-family:'MyCustomFont',cursive;}
</style></template>`

func TestEnsureFontFacesAddsMissing(t *testing.T) {
	out := ensureFontFaces(ffFixture, nil)
	for _, want := range []string{
		"@font-face{font-family:'SimHei';src:local('SimHei');}",
		"@font-face{font-family:'Arial Narrow';src:local('Arial Narrow');}",
		"Georgia", // 白名单内也补
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("缺 %q\n---\n%s", want, out)
		}
	}
	// 已声明的 Segoe UI 不重复；白名单外的自定义字体不碰
	if strings.Count(out, "'Segoe UI';src:local") != 1 {
		t.Fatalf("Segoe UI 声明应保持 1 处:\n%s", out)
	}
	if strings.Contains(out, "MyCustomFont';src:local") {
		t.Fatalf("白名单外字体不应被补声明:\n%s", out)
	}
	// 插在第一个 <style> 块内部（紧跟标签，族名按字典序）
	if !strings.Contains(out, "<style>@font-face{") {
		t.Fatalf("声明应插在 <style> 开头:\n%s", out)
	}
}

func TestEnsureFontFacesIdempotent(t *testing.T) {
	once := ensureFontFaces(ffFixture, nil)
	twice := ensureFontFaces(once, nil)
	if once != twice {
		t.Fatalf("幂等被破坏:\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
}

func TestEnsureFontFacesNoStyle(t *testing.T) {
	out := ensureFontFaces(`<div style="font-family:'SimHei'">x</div>`, nil)
	if !strings.HasPrefix(out, "<style>") || !strings.Contains(out, "local('SimHei')") {
		t.Fatalf("无 <style> 块时应前置一个:\n%s", out)
	}
}

// 目录字体（fontCatalog）：解析产物里有 data URL 时，场景 HTML 引用该家族
// （大小写/空格写法不限）就补真字体声明；没有产物则不补（font_tool 失败剥族兜底）。
func TestEnsureFontFacesCatalogAsset(t *testing.T) {
	html := `<template><style>#s{font-family:'zcool qingke huangyou','SimHei',sans-serif;}</style></template>`
	assets := map[string]string{
		"zcool qingke huangyou": "data:font/woff2;base64,AAECAw==",
	}
	out := ensureFontFaces(html, assets)
	if !strings.Contains(out, `@font-face{font-family:'zcool qingke huangyou';src:url("data:font/woff2;base64,AAECAw==") format('woff2');}`) {
		t.Fatalf("目录字体未补真字体声明:\n%s", out)
	}
	// 幂等：声明已存在不再追加
	again := ensureFontFaces(out, assets)
	if strings.Count(again, "data:font/woff2") != 1 {
		t.Fatalf("目录字体声明应保持 1 处:\n%s", again)
	}
	// 无产物：不补声明（font_tool 失败时家族已被剥除，不会走到这）
	bare := ensureFontFaces(html, nil)
	if strings.Contains(bare, "zcool qingke huangyou';src:url") {
		t.Fatalf("无产物时不应补目录字体声明:\n%s", bare)
	}
}

// fixupFrameFonts 必须连 caps/ 一起扫：字幕由 assemble.py 在 compositions 之后
// 确定性重写，目录字体没解析成功时字幕带着未声明字体族进 check，而修复轮只会
// 重出场景、永远修不到字幕（结构性死锁）。
func TestFixupFrameFontsCoversCaps(t *testing.T) {
	dir := t.TempDir()
	p := &Project{ID: "t", Dir: dir}
	files := map[string]string{
		"compositions/frames/seg01.html":   `<template><style>#s{font-family:'SimHei',sans-serif;}</style></template>`,
		"compositions/caps/cap-seg01.html": `<template><style>#c{font-family:'ZCOOL QingKe HuangYou','Microsoft YaHei',sans-serif;}</style></template>`,
	}
	for rel, html := range files {
		if err := os.MkdirAll(filepath.Dir(p.Artifact(rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p.Artifact(rel), []byte(html), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fixupFrameFonts(p)
	b, err := os.ReadFile(p.Artifact("compositions/caps/cap-seg01.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "local('Microsoft YaHei')") {
		t.Fatalf("caps 内白名单字体未补声明:\n%s", b)
	}
	if strings.Contains(string(b), "zcool qingke huangyou';src:") {
		t.Fatalf("caps 内目录字体无解析产物时不应补声明:\n%s", b)
	}
	fb, err := os.ReadFile(p.Artifact("compositions/frames/seg01.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(fb), "local('SimHei')") {
		t.Fatalf("frames 补声明被回归破坏:\n%s", fb)
	}
}

// 目录字体的 local() 空声明必须被纠错：模型写了声明但指向渲染端没装的字体，
// 空声明骗过了「已声明」检查，真字体永远进不了正片（2026-10-13 齐马蓝实录：
// 样张与正片双双回退雅黑）。纠错 = 该块重写为 url() 真声明；已带 url( 的不动。
func TestRewriteAssetFontDecls(t *testing.T) {
	html := `<template><style>` +
		`@font-face{font-family:'ZCOOL QingKe HuangYou';src:local('ZCOOL QingKe HuangYou');}` +
		`@font-face{font-family:'KaiTi';src:local('KaiTi');}` +
		`#t{font-family:'ZCOOL QingKe HuangYou','Microsoft YaHei',sans-serif;}` +
		`</style></template>`
	assets := map[string]string{"zcool qingke huangyou": "data:font/woff2;base64,AAECAw=="}
	out, n := rewriteAssetFontDecls(html, assets)
	if n != 1 {
		t.Fatalf("应改写 1 块，得到 %d:\n%s", n, out)
	}
	if !strings.Contains(out, `@font-face{font-family:'ZCOOL QingKe HuangYou';src:url("data:font/woff2;base64,AAECAw==") format('woff2');}`) {
		t.Fatalf("空声明未重写为真字体:\n%s", out)
	}
	if strings.Contains(out, "src:local('ZCOOL") {
		t.Fatalf("空声明应被替换掉:\n%s", out)
	}
	if !strings.Contains(out, `@font-face{font-family:'KaiTi';src:local('KaiTi');}`) {
		t.Fatalf("系统字体 local 声明不应被动:\n%s", out)
	}
	// 幂等：url() 声明再跑不动
	again, n2 := rewriteAssetFontDecls(out, assets)
	if n2 != 0 || again != out {
		t.Fatalf("纠错应幂等: n=%d", n2)
	}
}
