package produce

import (
	"strings"
	"testing"
)

const patchTestHTML = `<template data-composition-id="seg01" data-width="1080" data-height="1920">
<div id="root">
  <h1 id="title" data-hf-name="主标题" style="color: red; font-weight: 700;">旧标题</h1>
  <div id="orbit"><span>轨道</span></div>
</div>
</template>`

// patch 套用：text 替换 / style 合并（旧声明保留、新值覆盖）/ html 外层替换，
// 且 template 头属性原样保留（data-composition-id 是渲染端身份）。
func TestApplyScenePatches(t *testing.T) {
	newTitle := "量子纠缠"
	newOrbit := `<div id="orbit" class="ring"><b>新轨道</b></div>`
	got, err := applyScenePatches(patchTestHTML, []scenePatch{
		{Target: "#title", Text: &newTitle},
		{Target: "#title", Style: map[string]string{"color": "#fff", "letter-spacing": "2px"}},
		{Target: "#orbit", HTML: &newOrbit},
	})
	if err != nil {
		t.Fatalf("套用失败: %v", err)
	}
	if !strings.Contains(got, `data-composition-id="seg01"`) {
		t.Fatal("template 头属性丢了")
	}
	if strings.Contains(got, "旧标题") || !strings.Contains(got, "量子纠缠") {
		t.Fatalf("text 替换未生效: %s", got)
	}
	if !strings.Contains(got, "color: #fff") || !strings.Contains(got, "font-weight: 700") ||
		!strings.Contains(got, "letter-spacing: 2px") {
		t.Fatalf("style 合并不符（旧声明应保留）: %s", got)
	}
	if !strings.Contains(got, `<div id="orbit" class="ring">`) || !strings.Contains(got, "新轨道") {
		t.Fatalf("html 外层替换未生效: %s", got)
	}
	if strings.Contains(got, "<span>轨道</span>") {
		t.Fatalf("旧外层应被整体换掉: %s", got)
	}
}

// 打不中 / 空改动 / 一条多做：整单失败——宁回退整份重写，不出改了一半的产物。
func TestApplyScenePatchesRejects(t *testing.T) {
	txt := "x"
	frag := "<div id=\"orbit\"></div>"
	renamed := `<div id="别的" class="ring"><b>新轨道</b></div>` // id 被换：GSAP 补间将找不着元素，必须整单作废
	cases := []struct {
		name    string
		patches []scenePatch
		wantErr string
	}{
		{"打不中", []scenePatch{{Target: "#nope", Text: &txt}}, "打不中"},
		{"空改动单", nil, "空改动单"},
		{"空改动", []scenePatch{{Target: "#title"}}, "空改动"},
		{"一条多做", []scenePatch{{Target: "#title", Text: &txt, HTML: &frag}}, "一件事"},
		{"换门牌", []scenePatch{{Target: "#orbit", HTML: &renamed}}, "丢了原 id"},
	}
	for _, c := range cases {
		if _, err := applyScenePatches(patchTestHTML, c.patches); err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Fatalf("%s: 期望错误含 %q, got %v", c.name, c.wantErr, err)
		}
	}
	// 整单失败时原文件必须原样不动（失败返回空串，调用方保留旧版）
	if got, err := applyScenePatches(patchTestHTML, []scenePatch{{Target: "#nope", Text: &txt}}); err == nil || got != "" {
		t.Fatalf("失败套用不应产出半成品: %q %v", got, err)
	}
}

// 非 template 片段拒收（契约文件被上游弄坏时如实报错，不硬套）。
func TestApplyScenePatchesNonTemplate(t *testing.T) {
	txt := "x"
	if _, err := applyScenePatches("<html><body>hi</body></html>", []scenePatch{{Target: "#a", Text: &txt}}); err == nil {
		t.Fatal("非 template 片段应拒收")
	}
}
