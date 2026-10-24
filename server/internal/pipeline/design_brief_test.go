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
	if s := designBrief(p, sb, nil); strings.Contains(s, "本片风格参考") {
		t.Fatalf("无简报不该注入强参考块")
	}
	if s := designUserPrompt(p, sb, nil); strings.Contains(s, "signature_absorption：简报里每条开启的特色") {
		t.Fatalf("无简报不该提落点登记要求")
	}

	// 落简报
	brief := &contract.StyleBrief{
		EntryID: "vid-x", EntryName: "故障风数据片",
		Tags: contract.StyleTags{Main: "glitch", Aux: []string{"iso"}},
		TagBriefs: []contract.TagBrief{
			{ID: "glitch", Name: "故障风", Brief: "数字撕裂与 RGB 错位", Tells: "扫描线、错位色边"},
			{ID: "iso", Name: "等距", Brief: "轴测视角"},
		},
		Signatures: []contract.Signature{
			{ID: "sig1", Kind: contract.SigKindTransition, Name: "道具递接转场",
				Mechanism: "前一画面主体推出画外，后一画面同位置跟进", UseHint: "段间切换",
				Frames: []string{"frame-03.png"}},
			{ID: "sig2", Kind: contract.SigKindPalette, Name: "紫青撞色", Mechanism: "双色对撞铺底",
				Frames: []string{"frame-07.png"}},
		},
	}
	bj, _ := json.Marshal(brief)
	if err := os.MkdirAll(filepath.Dir(p.Artifact("style/style_brief.json")), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Artifact("style/style_brief.json"), bj, 0o644); err != nil {
		t.Fatal(err)
	}

	// evs=nil（会话/压图失败）：附图文案必须降级，不许谎称已附
	s := designBrief(p, sb, nil)
	for _, want := range []string{
		"本片风格参考（用户选定，硬要求）",
		"气质基调：故障风——数字撕裂与 RGB 错位（帧里认什么：扫描线、错位色边）", // main 词条 name+brief+tells
		"；辅：等距——轴测视角", // aux 词条（无 tells 不追加）
		"必答特色（每条都必须被设计系统吸收，并在 signature_absorption 中登记落点）",
		"证据帧本次未能附图",
		"吸收的是机制不是物件",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("设计作业缺 %q\n---\n%s", want, s)
		}
	}
	if strings.Contains(s, "随文附了") || strings.Contains(s, "证据帧已附") {
		t.Fatalf("未附图不得谎称已附\n---\n%s", s)
	}
	// 附真迹（sig1 的 frame-03 在附图清单里）：附图声明 + 按帧配字幕 + 特色指向附图
	evs := []BriefEvidence{
		{URL: "data:x", SrcPath: "/m/vid-x/e0.png"},
		{URL: "data:x", SrcPath: "/m/vid-x/frame-03.png", Note: "第2段：道具递接瞬间"},
		{URL: "data:x", SrcPath: "/m/vid-x/e2.png", Note: "高密度数据铺底"},
		{URL: "data:x", SrcPath: "/m/vid-x/e3.png"},
		{URL: "data:x", SrcPath: "/m/vid-x/e4.png"},
	}
	s = designBrief(p, sb, evs)
	for _, want := range []string{
		"随文附了 5 张该条目的证据帧",
		"配色、光影、质感、画面密度、字体气质",
		"附图对应内容：第2张：第2段：道具递接瞬间；第3张：高密度数据铺底", // 空说明的帧跳过
		"（真迹附图中可见）",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("附图模式设计作业缺 %q\n---\n%s", want, s)
		}
	}
	// 附图里没有任何特色帧：不许谎报「真迹附图中可见」（6 张上限挤掉特色帧的实况）
	none := []BriefEvidence{
		{URL: "data:x", SrcPath: "/m/vid-x/a.png"},
		{URL: "data:x", SrcPath: "/m/vid-x/b.png", Note: "气质对照"},
	}
	if s = designBrief(p, sb, none); strings.Contains(s, "真迹附图中可见") {
		t.Fatalf("特色帧未附图不得声称可见\n---\n%s", s)
	}
	if got := designUserPrompt(p, sb, evs); !strings.Contains(got, "sig1,sig2") ||
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
	if doc2 := DesignDocWithBrief(ss, 1, nil); strings.Contains(doc2, "强参考特色") {
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
