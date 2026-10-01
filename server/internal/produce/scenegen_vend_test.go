package produce

import (
	"reflect"
	"strings"
	"testing"
)

// vend-stage.md §3:lint 违规清单里的 A3 缺库消息即采购申请单,
// 解析必须去重、排序(采购顺序确定)、对非缺库违规免疫。
func TestMissingLibSpecs(t *testing.T) {
	errs := []string{
		"[A3] 缺库:three@0.152.2 未入 vendor manifest——运行 python ai/vendor_tool.py ensure three@0.152.2 --file <tarball内路径> 采购",
		"[T5] 第 12 行:禁 @keyframes(合成器时钟,seek 不到)",
		"[A3] 缺库:d3@7.9.0 未入 vendor manifest——运行 python ai/vendor_tool.py ensure d3@7.9.0 采购",
		"[A3] 缺库:three@0.152.2 未入 vendor manifest——运行 python ai/vendor_tool.py ensure three@0.152.2 采购",
	}
	got := missingLibSpecs(errs)
	want := []string{"d3@7.9.0", "three@0.152.2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("missingLibSpecs = %v, want %v", got, want)
	}
	if got := missingLibSpecs([]string{"[T2] 第 3 行:禁 Date.now"}); got != nil {
		t.Fatalf("无非缺库违规时应返回 nil, got %v", got)
	}
}

// TestSceneLintWithVendRegistered Go 胶水:场景声明已登记库(d3@7.9.0,由 E2E 预先入册)
// 时,sceneLintWithVend 走 lint→无缺库→直接放行,不触发采购(离线快路径)。
func TestSceneLintWithVendRegistered(t *testing.T) {
	r := &Runner{RootDir: "../../.."}
	html := `<template>
  <!-- vend: d3@7.9.0 -->
  <style>@font-face{font-family:"X";src:local("X");}</style>
  <div data-composition-id="seg99" data-width="1920" data-height="1080"><div id="root">
    <div class="clip" data-start="0" data-duration="1" data-track-index="0" data-hf-name="a"></div>
    <div class="clip" data-start="0" data-duration="1" data-track-index="1" data-hf-name="b"></div>
    <div class="clip" data-start="0" data-duration="1" data-track-index="2" data-hf-name="c"></div>
  </div></div>
  <script>(function(){var tl=gsap.timeline({paused:true});window.__timelines["seg99"]=tl;})();</script>
</template>`
	errs := r.sceneLintWithVend(html)
	for _, e := range errs {
		if strings.Contains(e, "缺库") || strings.Contains(e, "A3") {
			t.Fatalf("已登记库不应再报缺库: %v", errs)
		}
	}
}
