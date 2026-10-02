package contract

import "testing"

// 画幅几何是场景注入（安全区/字幕带）与前端时间轴的公共基准——v1 spec 校验
// 拆除后仍须钉住这几组数。
func TestCanvasGeometry(t *testing.T) {
	wide := CanvasFor("16:9")
	if wide.W != 1920 || wide.H != 1080 || wide.CapTop != 896 || wide.X1 != 1860 || wide.Y1 != 870 {
		t.Fatalf("16:9 画幅几何不对: %+v", wide)
	}
	vert := CanvasFor("9:16")
	if vert.W != 1080 || vert.H != 1920 || vert.CapTop != 1594 || vert.X1 != 1020 || vert.Y1 != 1550 {
		t.Fatalf("9:16 画幅几何不对: %+v", vert)
	}
	if CanvasFor("").Aspect != "16:9" {
		t.Fatalf("未知画幅应回落 16:9")
	}
}

// scene 禁令的存活面（v1 scene_test 随 spec 拆除，守卫测试还在用这个函数）。
func TestSceneCodeViolation(t *testing.T) {
	clean := "var p=Math.min(1,t/6);ctx.fillStyle='hsl('+Math.round(p*300)+',80%,55%)';ctx.beginPath();ctx.arc(W/2,H/2,100+p*300,0,6.28);ctx.fill();"
	if v := SceneCodeViolation(clean); v != "" {
		t.Fatalf("纯函数绘制误报: %s", v)
	}
	if v := SceneCodeViolation("ctx.fillStyle='#fff';setTimeout(draw,16)"); v != "setTimeout" {
		t.Fatalf("禁令未命中: %q", v)
	}
	if v := SceneCodeViolation("var r=Math.random()"); v != "Math.random" {
		t.Fatalf("真随机未命中: %q", v)
	}
}
