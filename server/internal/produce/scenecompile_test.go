package produce

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/pipeline"
)

func sampleIR() *contract.SceneIR {
	return &contract.SceneIR{
		Background: "#EFE7DA",
		CSS:        "#title .t{font-weight:900;color:#1E2A44;}",
		Elements: []contract.SceneIRElement{
			{ID: "paper", Name: "米纸底", HTML: `<div class="paper"></div>`, CSS: "#paper .paper{position:absolute;inset:0;background:#EFE7DA;}", Duration: 6},
			{ID: "tri", Name: "三角角色", HTML: `<svg viewBox="0 0 100 100"><polygon points="50,5 95,95 5,95" fill="#1E2A44"/></svg>`, Box: &contract.SceneIRBox{X: 300, Y: 260, W: 320, H: 320}, Duration: 6},
			{ID: "title", Name: "标题", HTML: `<div class="t">剪辑的寓言</div>`, Box: &contract.SceneIRBox{X: 120, Y: 120, W: 700, H: 160}, Duration: 6},
		},
		Cues: []contract.SceneIRCue{
			{Target: "#title", Kind: "fromTo", From: map[string]any{"opacity": 0, "y": 24}, To: map[string]any{"opacity": 1, "y": 0}, At: 0.3, Dur: 0.6, Ease: "power2.out"},
			{Target: "#tri", Kind: "to", To: map[string]any{"rotation": 8}, At: 1.2, Dur: 1.2, Ease: "sine.inOut"},
		},
	}
}

func TestCompileSceneIRStructure(t *testing.T) {
	cv := contract.CanvasFor("16:9")
	html, err := CompileSceneIR(sampleIR(), "seg01", cv, 6)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	for _, want := range []string{
		`<template>`,
		`data-composition-id="seg01"`,
		`data-width="1920"`,
		`data-height="1080"`,
		`id="root"`,
		`class="clip"`,
		`data-hf-name="米纸底"`,
		`data-hf-name="三角角色"`,
		`data-hf-name="标题"`,
		`var tl = gsap.timeline({ paused: true });`,
		`window.__timelines["seg01"] = tl;`,
		`tl.fromTo("#title"`,
		`tl.to("#tri"`,
		`duration: 6`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("编译产物缺 %q\n%s", want, html)
		}
	}
	// 编译器产出的元素注册表必须与 IR 一致。
	if got := SceneIRElements(sampleIR()); len(got) != 3 || got[0].ID != "paper" {
		t.Fatalf("elements 派生错误: %+v", got)
	}
}

func TestCompileSceneIRRejectsInvalid(t *testing.T) {
	ir := sampleIR()
	ir.Elements = ir.Elements[:2]
	if _, err := CompileSceneIR(ir, "seg01", contract.CanvasFor("16:9"), 6); err == nil {
		t.Fatal("少于 3 个元素应拒绝编译")
	}
	ir = sampleIR()
	ir.Cues[0].At = 99
	if _, err := CompileSceneIR(ir, "seg01", contract.CanvasFor("16:9"), 6); err == nil {
		t.Fatal("cue 超出段长应拒绝编译")
	}
}

func TestCompileSceneIRPassesSceneLint(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	html, err := CompileSceneIR(sampleIR(), "seg01", contract.CanvasFor("16:9"), 6)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	r := &Runner{RootDir: root}
	if errs := r.sceneLintRun(context.Background(), html); len(errs) > 0 {
		t.Fatalf("编译产物没过 scene_lint：\n- %s\n--- html ---\n%s", strings.Join(errs, "\n- "), html)
	}
}

func TestSceneIROnProjectField(t *testing.T) {
	dir := t.TempDir()
	p := pipeline.NewProject(dir, "ptest")
	if err := os.MkdirAll(p.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if sceneIROn(p) {
		t.Fatal("缺字段时不该开 IR")
	}
	if err := os.WriteFile(p.Artifact("project.json"), []byte(`{"scene_ir":"1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !sceneIROn(p) {
		t.Fatal("scene_ir=1 应开 IR")
	}
}

func TestApplySceneIROpsRetime(t *testing.T) {
	ir := sampleIR()
	at := 1.5
	out, err := ApplySceneIROps(ir, []SceneIROp{{Op: "set_cue", Target: "#title", Prop: "opacity", At: &at}})
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if out.Cues[0].At != 1.5 {
		t.Fatalf("cue at 未更新: %+v", out.Cues[0])
	}
	// 原 IR 不被修改（失败不产出半套用结果的前提）。
	if ir.Cues[0].At != 0.3 {
		t.Fatal("patch 修改了原 IR")
	}
	html, err := CompileSceneIR(out, "seg01", contract.CanvasFor("16:9"), 6)
	if err != nil {
		t.Fatalf("compile patched: %v", err)
	}
	if !strings.Contains(html, `, 1.500);`) {
		t.Fatalf("编译产物没带上新 at\n%s", html)
	}
}

func TestApplySceneIROpsNormalizesHash(t *testing.T) {
	ir := sampleIR()
	at := 1.5
	out, err := ApplySceneIROps(ir, []SceneIROp{{Op: "set_cue", Target: "##title", Prop: "opacity", At: &at}})
	if err != nil {
		t.Fatalf("##target 应被归一化: %v", err)
	}
	if out.Cues[0].At != 1.5 {
		t.Fatalf("cue 未更新: %+v", out.Cues[0])
	}
	out, err = ApplySceneIROps(ir, []SceneIROp{{Op: "remove_element", Target: "##paper"}})
	if err != nil {
		t.Fatalf("##target remove_element: %v", err)
	}
	if len(out.Elements) != 2 {
		t.Fatalf("remove_element 未生效: %d", len(out.Elements))
	}
}

func TestApplySceneIROpsElementAndCue(t *testing.T) {
	ir := sampleIR()
	out, err := ApplySceneIROps(ir, []SceneIROp{
		{Op: "set_element", Target: "#title", Element: &contract.SceneIRElement{ID: "title", Name: "标题2", HTML: `<div class="t">新标题</div>`}},
		{Op: "add_cue", Cue: &contract.SceneIRCue{Target: "#paper", Kind: "to", To: map[string]any{"opacity": 0.8}, At: 5, Dur: 0.5}},
		{Op: "remove_cue", Target: "#tri"},
	})
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if out.Elements[2].Name != "标题2" || !strings.Contains(out.Elements[2].HTML, "新标题") {
		t.Fatalf("set_element 未生效: %+v", out.Elements[2])
	}
	if len(out.Cues) != 2 {
		t.Fatalf("add/remove cue 后应有 2 条，实际 %d: %+v", len(out.Cues), out.Cues)
	}
	if _, err := ApplySceneIROps(ir, []SceneIROp{{Op: "nope"}}); err == nil {
		t.Fatal("未知 op 应报错")
	}
}
