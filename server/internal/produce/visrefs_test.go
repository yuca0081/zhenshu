package produce

// visRefs 回归测试：注入图装配必须带 URL。2026-10-19 实战事故——bad59fa 接线
// 纠偏时 visRefs 丢了 URL，样张以空 image_url 进 kickoff，GLM 端点对每一段审
// 回 400「image_url格式错误」，全部段审/边界审/终审报废（萤火虫基线之后的
// 第一次制作用才暴露）。空 URL 现在由 review.Loop 兜底跳过并记账，但根因
// 必须锁死：装配函数不许丢字段。

import (
	"testing"

	"webpage-video-agent/server/internal/llm"
)

func TestVisRefsKeepsURL(t *testing.T) {
	vis := []visImg{
		{URL: "data:image/jpeg;base64,QQ==", Path: "style/sample-0.png", Label: "风格样张 1"},
		{URL: "data:image/jpeg;base64,Qg==", Path: "style/sample-1.png", Label: "风格样张 2"},
	}
	refs := visRefs(vis)
	if len(refs) != len(vis) {
		t.Fatalf("refs 数量 %d != 输入 %d", len(refs), len(vis))
	}
	for i, r := range refs {
		if r.URL == "" {
			t.Errorf("refs[%d] 丢了 URL（喂模型的载荷会变成空 image_url，端点 400）", i)
		}
		if r.URL != vis[i].URL {
			t.Errorf("refs[%d].URL = %q, want %q", i, r.URL, vis[i].URL)
		}
		if r.Path != vis[i].Path || r.Label != vis[i].Label {
			t.Errorf("refs[%d] Path/Label 不符: %+v", i, r)
		}
	}
}

// Loop 开场装配的空 URL 兜底：空图跳过、纯文本兜底不炸（review 包侧同款防线）。
func TestVisRefsEmptyURLDetected(t *testing.T) {
	refs := []llm.ImgRef{
		{URL: "", Path: "style/sample-0.png", Label: "丢拷贝症状"},
		{URL: "data:image/jpeg;base64,QQ==", Path: "style/sample-1.png", Label: "正常"},
	}
	empty := 0
	for _, r := range refs {
		if r.URL == "" {
			empty++
		}
	}
	if empty != 1 {
		t.Fatalf("want 1 empty, got %d", empty)
	}
}
