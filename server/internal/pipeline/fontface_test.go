package pipeline

import (
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
	out := ensureFontFaces(ffFixture)
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
	once := ensureFontFaces(ffFixture)
	twice := ensureFontFaces(once)
	if once != twice {
		t.Fatalf("幂等被破坏:\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
}

func TestEnsureFontFacesNoStyle(t *testing.T) {
	out := ensureFontFaces(`<div style="font-family:'SimHei'">x</div>`)
	if !strings.HasPrefix(out, "<style>") || !strings.Contains(out, "local('SimHei')") {
		t.Fatalf("无 <style> 块时应前置一个:\n%s", out)
	}
}
