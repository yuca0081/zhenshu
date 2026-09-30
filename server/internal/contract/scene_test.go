package contract

import (
	"strings"
	"testing"
)

// sceneTmpl 合法 scene 段样板（单元素即可过元素数下限）。
func sceneTmpl() *CompSpec {
	return &CompSpec{Elements: []SpecElement{{
		Kind: "scene", Seed: 42,
		Code: "var p=Math.min(1,t/6);ctx.fillStyle='hsl('+Math.round(p*300)+',80%,55%)';ctx.beginPath();ctx.arc(W/2,H/2,100+p*300,0,6.28);ctx.fill();",
	}}}
}

// TestSceneValidate scene 契约：合法样板零违规；禁令/预算/数量/anim 拒绝；元素下限放宽。
func TestSceneValidate(t *testing.T) {
	cv := CanvasFor("16:9")

	// 1. 合法：单 scene 元素 + 无 camera 等，应零违规（元素下限 1）
	if errs := ValidateSpec(sceneTmpl(), 20, cv); len(errs) > 0 {
		t.Fatalf("合法 scene 段不应有违规: %v", errs)
	}

	// 2. 禁令：Math.random / document / setTimeout 各自命中
	for _, ban := range []string{"Math.random()", "document.getElementById('x')", "setTimeout(f,10)", "fetch('http://x')"} {
		s := sceneTmpl()
		s.Elements[0].Code = "var v=" + ban + ";"
		errs := ValidateSpec(s, 20, cv)
		if !strings.Contains(strings.Join(errs, "\n"), "禁用模式") {
			t.Fatalf("code 含 %q 应被禁令拦下，得到: %v", ban, errs)
		}
	}

	// 3. 预算：> SceneCodeMax 字报超长
	s := sceneTmpl()
	s.Elements[0].Code = strings.Repeat("甲", SceneCodeMax+1)
	if errs := ValidateSpec(s, 20, cv); !strings.Contains(strings.Join(errs, "\n"), "超长") {
		t.Fatalf("code 超长应报错")
	}

	// 4. 每段 ≤1；anim/exit 拒绝
	s = sceneTmpl()
	s.Elements = append(s.Elements, s.Elements[0])
	errs := ValidateSpec(s, 20, cv)
	if !strings.Contains(strings.Join(errs, "\n"), "scene 元素最多 1 个") {
		t.Fatalf("双 scene 应报错: %v", errs)
	}
	s = sceneTmpl()
	s.Elements[0].Anim = "pop"
	if errs := ValidateSpec(s, 20, cv); !strings.Contains(strings.Join(errs, "\n"), "不支持 anim") {
		t.Fatalf("scene+anim 应报错: %v", errs)
	}

	// 5. 非 scene 段仍要 3 个起（回归：下限放宽只对 scene 段生效）
	s = &CompSpec{Elements: []SpecElement{
		{Kind: "note", X: 300, Y: 300, Text: "一条", Reveal: 0},
		{Kind: "note", X: 300, Y: 500, Text: "两条", Reveal: 1},
	}}
	if errs := ValidateSpec(s, 20, cv); !strings.Contains(strings.Join(errs, "\n"), "元素数") {
		t.Fatalf("非 scene 段两条元素应报元素数: %v", errs)
	}
}

// TestSceneSanitize scene 缺 code 直接删；seed 越界钳 42。
func TestSceneSanitize(t *testing.T) {
	cv := CanvasFor("16:9")
	s := &CompSpec{Elements: []SpecElement{
		{Kind: "scene", Seed: -5, Code: "ctx.fillRect(0,0,10,10);"},
		{Kind: "note", X: 300, Y: 300, Text: "标题", Reveal: 0},
		{Kind: "note", X: 300, Y: 500, Text: "副题", Reveal: 1},
		{Kind: "note", X: 300, Y: 700, Text: "三号", Reveal: 2},
	}}
	SanitizeSpec(s, 20, cv)
	if len(s.Elements) != 4 || s.Elements[0].Kind != "scene" {
		t.Fatalf("scene 应保留且无误删: %+v", s.Elements)
	}
	if s.Elements[0].Seed != 42 {
		t.Fatalf("seed 越界应钳 42，得到 %d", s.Elements[0].Seed)
	}
	// 缺 code → 删
	s2 := &CompSpec{Elements: []SpecElement{
		{Kind: "scene"},
		{Kind: "note", X: 300, Y: 300, Text: "标题", Reveal: 0},
		{Kind: "note", X: 300, Y: 500, Text: "副题", Reveal: 1},
	}}
	SanitizeSpec(s2, 20, cv)
	for _, e := range s2.Elements {
		if e.Kind == "scene" {
			t.Fatal("缺 code 的 scene 应被删除")
		}
	}
}

// TestFormulaValidate formula 契约：tex 必填/限长/禁外链命令。
func TestFormulaValidate(t *testing.T) {
	cv := CanvasFor("16:9")
	ok := &CompSpec{Elements: []SpecElement{
		{Kind: "formula", X: 400, Y: 200, Tex: `e^{i\pi}+1=0`, Fs: 64, Reveal: 0},
		{Kind: "note", X: 300, Y: 500, Text: "说明", Reveal: 1},
		{Kind: "note", X: 300, Y: 700, Text: "再来", Reveal: 2},
	}}
	if errs := ValidateSpec(ok, 20, cv); len(errs) > 0 {
		t.Fatalf("合法 formula 段不应有违规: %v", errs)
	}
	bad := &CompSpec{Elements: []SpecElement{
		{Kind: "formula", X: 400, Y: 200, Tex: `\href{http://x}{y}`, Reveal: 0},
		{Kind: "note", X: 300, Y: 500, Text: "说明", Reveal: 1},
		{Kind: "note", X: 300, Y: 700, Text: "再来", Reveal: 2},
	}}
	if errs := ValidateSpec(bad, 20, cv); !strings.Contains(strings.Join(errs, "\n"), "禁用命令") {
		t.Fatalf("\\href 应被拦: %v", errs)
	}
	empty := &CompSpec{Elements: []SpecElement{
		{Kind: "formula", X: 400, Y: 200, Reveal: 0},
		{Kind: "note", X: 300, Y: 500, Text: "说明", Reveal: 1},
		{Kind: "note", X: 300, Y: 700, Text: "再来", Reveal: 2},
	}}
	if errs := ValidateSpec(empty, 20, cv); !strings.Contains(strings.Join(errs, "\n"), "缺 tex") {
		t.Fatalf("缺 tex 应报错: %v", errs)
	}
}

// TestCameraSpecValidate 关键帧镜头：越界报错回喂；Sanitize 钳制进界。
func TestCameraSpecValidate(t *testing.T) {
	cv := CanvasFor("16:9")
	bad := sceneTmpl()
	bad.CameraSpec = &CameraSpec{From: CameraKey{Zoom: 2.0}, To: CameraKey{PanX: 300}, Ease: "bogus"}
	errs := ValidateSpec(bad, 20, cv)
	joined := strings.Join(errs, "\n")
	for _, want := range []string{"zoom", "pan_x", "ease"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("越界 camera_spec 应报 %q: %v", want, errs)
		}
	}
	// Sanitize 钳制
	SanitizeSpec(bad, 20, cv)
	if z := bad.CameraSpec.From.Zoom; z != 1.6 {
		t.Fatalf("zoom 应钳 1.6，得到 %v", z)
	}
	if p := bad.CameraSpec.To.PanX; p != 120 {
		t.Fatalf("pan_x 应钳 120，得到 %v", p)
	}
	if bad.CameraSpec.Ease != "" {
		t.Fatalf("非法 ease 应清空，得到 %q", bad.CameraSpec.Ease)
	}
	// 合法值
	good := sceneTmpl()
	good.CameraSpec = &CameraSpec{From: CameraKey{Zoom: 1}, To: CameraKey{Zoom: 1.3, PanX: -80, Rot: 3}, Ease: "power2.inOut"}
	if errs := ValidateSpec(good, 20, cv); len(errs) > 0 {
		t.Fatalf("合法 camera_spec 不应有违规: %v", errs)
	}
}
