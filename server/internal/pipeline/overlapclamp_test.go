package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const ocFrame = `<template><style>
#bg[data-layout-allow-overlap]{opacity:.2}
</style>
<div id="bg" class="clip" data-layout-allow-overlap>1984</div>
<div id="card" class="clip" data-layout-allow-overlap="deep">正文卡</div>
</template>`

// 设计无叠层声明：标签上的声明全剥；CSS 属性选择器紧贴 '[' 不带前导空白，不能误伤。
func TestClampOverlapAttr_StripsAllWithoutLayered(t *testing.T) {
	out, n := clampOverlapAttr(ocFrame, false)
	if n != 2 {
		t.Fatalf("应剥 2 处标签声明，剥了 %d:\n%s", n, out)
	}
	if strings.Contains(out, "div\" data-layout-allow-overlap") ||
		strings.Contains(out, `"clip" data-layout-allow-overlap`) {
		t.Fatalf("标签声明未剥净:\n%s", out)
	}
	if !strings.Contains(out, "[data-layout-allow-overlap]") {
		t.Fatalf("CSS 选择器不应被误剥:\n%s", out)
	}
}

// 设计声明了叠层：上限内全保留；超上限剥多余的（从尾部剥，先声明的优先）。
func TestClampOverlapAttr_Cap(t *testing.T) {
	one := `<div class="clip" data-layout-allow-overlap>x</div>`
	within := strings.Repeat(one, maxOverlapDeclsPerFrame)
	out, n := clampOverlapAttr(within, true)
	if n != 0 || out != within {
		t.Fatalf("上限内不应剥（剥了 %d）", n)
	}
	over := strings.Repeat(one, maxOverlapDeclsPerFrame+2)
	out, n = clampOverlapAttr(over, true)
	if n != 2 {
		t.Fatalf("超限 2 个应剥 2 处，剥了 %d", n)
	}
	if got := strings.Count(out, "data-layout-allow-overlap"); got != maxOverlapDeclsPerFrame {
		t.Fatalf("应保留 %d 处，剩 %d:\n%s", maxOverlapDeclsPerFrame, got, out)
	}
}

func TestClampOverlapAttr_NoDeclNoChange(t *testing.T) {
	out, n := clampOverlapAttr(`<div class="clip">x</div>`, false)
	if n != 0 {
		t.Fatalf("无声明不应有剥除动作，剥了 %d", n)
	}
	_ = out
}

// 端到端：设计文件缺失/无叠层版式 → 剥；设计声明叠层 → 保留。幂等。
func TestClampOverlapDecls_Project(t *testing.T) {
	dir := t.TempDir()
	p := &Project{ID: "t", Dir: dir}
	frame := p.Artifact("compositions/frames/seg01.html")
	if err := os.MkdirAll(filepath.Dir(frame), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(frame, []byte(ocFrame), 0o644); err != nil {
		t.Fatal(err)
	}
	// 无设计文件：读不到按无叠层处理，全剥
	if n := clampOverlapDecls(p); n != 2 {
		t.Fatalf("无设计文件应剥 2 处，剥了 %d", n)
	}
	// 声明了叠层版式的设计：保留
	if err := os.MkdirAll(filepath.Dir(p.Artifact("style/style_samples.json")), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Artifact("style/style_samples.json"),
		[]byte(`{"layouts":[{"id":"a","name":"A","desc":"d","layered":"衬底词压正文卡"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(frame, []byte(ocFrame), 0o644); err != nil {
		t.Fatal(err)
	}
	if n := clampOverlapDecls(p); n != 0 {
		t.Fatalf("声明叠层后不应剥，剥了 %d", n)
	}
	b, _ := os.ReadFile(frame)
	if !strings.Contains(string(b), `data-layout-allow-overlap="deep"`) {
		t.Fatalf("声明叠层时带值声明应保留:\n%s", b)
	}
}
