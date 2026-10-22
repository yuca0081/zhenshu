package prep

// style_vend_test.go —— 样张自采（vend）与设计系统货架的纯函数测试。

import (
	"strings"
	"testing"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/pipeline"
)

func TestVendSpecs(t *testing.T) {
	html := `<!-- vend: three@0.160.0 --> <div>画面</div> <!-- vend:matter-js@0.19.0-->` +
		`<!-- vend: three@0.160.0 -->` // 重复声明去重
	specs := vendSpecs(html)
	if len(specs) != 2 || specs[0] != "three@0.160.0" || specs[1] != "matter-js@0.19.0" {
		t.Fatalf("应去重保序提取 2 条，got %v", specs)
	}
	if specs := vendSpecs("<p>没有声明</p>"); len(specs) != 0 {
		t.Fatalf("无声明应空，got %v", specs)
	}
}

func TestSampleVendLibs(t *testing.T) {
	samples := []contract.Sample{
		{HTML: "<!-- vend: three@0.160.0 -->"},
		{HTML: "<!-- vend: d3@7.9.0 --> <!-- vend: three@0.160.0 -->"},
	}
	libs := sampleVendLibs(samples)
	if len(libs) != 2 || libs[0] != "d3@7.9.0" || libs[1] != "three@0.160.0" {
		t.Fatalf("应去重排序，got %v", libs)
	}
	if libs := sampleVendLibs(nil); libs != nil {
		t.Fatalf("空样张应 nil，got %v", libs)
	}
}

func TestDesignDocVendShelf(t *testing.T) {
	ss := &contract.StyleSamples{
		Direction: "d",
		VendLibs:  []string{"three@0.160.0", "matter-js@0.19.0"},
	}
	doc := pipeline.DesignDocShared(ss, nil)
	if !strings.Contains(doc, "本片样张自采库") || !strings.Contains(doc, "three@0.160.0、matter-js@0.19.0") {
		t.Fatalf("设计文档应渲染货架清单\n%s", doc)
	}
	if doc2 := pipeline.DesignDocShared(&contract.StyleSamples{Direction: "d"}, nil); strings.Contains(doc2, "样张自采库") {
		t.Fatalf("无自采库不该渲染货架节\n%s", doc2)
	}
}
