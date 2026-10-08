package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"webpage-video-agent/server/internal/contract"
)

// 简报存在时：设计作业必须注入「强参考」块（气质基调 + 必答特色清单 + 落点登记
// 要求）；DesignDoc 必须渲染落点表。简报不存在：行为与旧版一致（裸发明）。
func TestDesignBriefInjection(t *testing.T) {
	p := NewProject(t.TempDir(), "ptest")
	sb := &contract.Storyboard{TimingBasis: "narration", Segments: []contract.Segment{
		{ID: "seg01", Idx: 1, Key: "k", Narration: "n", DurationHint: 9},
	}}
	// 无简报：不得出现强参考块
	if s := designBrief(p, sb); strings.Contains(s, "本片风格参考") {
		t.Fatalf("无简报不该注入强参考块")
	}
	if s := designUserPrompt(p, sb); strings.Contains(s, "signature_absorption：简报里每条开启的特色") {
		t.Fatalf("无简报不该提落点登记要求")
	}

	// 落简报
	brief := &contract.StyleBrief{
		EntryID: "vid-x", EntryName: "故障风数据片",
		Tags:      contract.StyleTags{Main: "glitch", Aux: []string{"iso"}},
		TagBriefs: []contract.TagBrief{
			{ID: "glitch", Name: "故障风", Brief: "数字撕裂与 RGB 错位"},
			{ID: "iso", Name: "等距", Brief: "轴测视角"},
		},
		Signatures: []contract.Signature{
			{ID: "sig1", Kind: contract.SigKindTransition, Name: "道具递接转场",
				Mechanism: "前一画面主体推出画外，后一画面同位置跟进", UseHint: "段间切换"},
			{ID: "sig2", Kind: contract.SigKindPalette, Name: "紫青撞色", Mechanism: "双色对撞铺底"},
		},
	}
	bj, _ := json.Marshal(brief)
	if err := os.MkdirAll(filepath.Dir(p.Artifact("style/style_brief.json")), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Artifact("style/style_brief.json"), bj, 0o644); err != nil {
		t.Fatal(err)
	}

	s := designBrief(p, sb)
	for _, want := range []string{
		"本片风格参考（用户选定，硬要求）",
		"气质基调：故障风——数字撕裂与 RGB 错位", // main 词条 name + brief
		"；辅：等距——轴测视角",                  // aux 词条
		"必答特色（每条都必须被设计系统吸收，并在 signature_absorption 中登记落点）",
		"[transition] 道具递接转场——机制：前一画面主体推出画外，后一画面同位置跟进（适用：段间切换）（证据帧已附）",
		"[palette] 紫青撞色——机制：双色对撞铺底（证据帧已附）",
		"吸收的是机制不是物件",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("设计作业缺 %q\n---\n%s", want, s)
		}
	}
	if got := designUserPrompt(p, sb); !strings.Contains(got, "sig1,sig2") ||
		!strings.Contains(got, contract.AbsorptionLandNames()) {
		t.Fatalf("输出要求应带落点登记说明（sig 清单+枚举）")
	}

	// DesignDoc 落点表：简报特色 × 交卷落点
	ss := &contract.StyleSamples{Direction: "d", Concept: "c", SignatureAbsorption: []contract.SigAbsorption{
		{SigID: "sig1", LandsIn: contract.LandsTransitionPolicy, How: "章节数字卡被上一段柱状图推出画外"},
		{SigID: "sig2", LandsIn: contract.LandsSkins, How: "底色双色对撞"},
	}}
	doc := DesignDocWithBrief(ss, 1, brief)
	for _, want := range []string{
		"本片强参考特色",
		"落点 transition_policy：章节数字卡被上一段柱状图推出画外",
		"落点 skins：底色双色对撞",
	} {
		if !strings.Contains(doc, want) {
			t.Fatalf("design.md 落点表缺 %q\n---\n%s", want, doc)
		}
	}
	// 无简报（老项目）：DesignDoc 不出现该章节
	if doc2 := DesignDoc(ss, 1); strings.Contains(doc2, "强参考特色") {
		t.Fatal("无简报不该渲染落点表")
	}
}

// LoadStyleBrief：无文件/损坏/缺 EntryID 都按 nil 处理（裸发明合法态）。
func TestLoadStyleBriefNilPaths(t *testing.T) {
	p := NewProject(t.TempDir(), "ptest")
	if b := LoadStyleBrief(p); b != nil {
		t.Fatal("无文件应 nil")
	}
	os.MkdirAll(filepath.Dir(p.Artifact("style/style_brief.json")), 0o755)
	os.WriteFile(p.Artifact("style/style_brief.json"), []byte("{broken"), 0o644)
	if b := LoadStyleBrief(p); b != nil {
		t.Fatal("损坏应 nil")
	}
	os.WriteFile(p.Artifact("style/style_brief.json"), []byte(`{"entry_name":"x"}`), 0o644)
	if b := LoadStyleBrief(p); b != nil {
		t.Fatal("缺 entry_id 应 nil")
	}
}
