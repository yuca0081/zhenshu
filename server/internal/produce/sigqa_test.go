package produce

import (
	"strings"
	"testing"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/pipeline"
)

func testBrief() *contract.StyleBrief {
	return &contract.StyleBrief{
		EntryID: "vid-x", EntryName: "故障风数据片",
		Tags: contract.StyleTags{Main: "glitch"},
		Signatures: []contract.Signature{
			{ID: "sig1", Kind: contract.SigKindTransition, Name: "道具递接转场",
				Mechanism: "前一画面主体推出画外，后一画面同位置跟进", UseHint: "段间切换"},
			{ID: "sig2", Kind: contract.SigKindPalette, Name: "紫青撞色", Mechanism: "双色对撞铺底"},
			{ID: "sig3", Kind: contract.SigKindCamera, Name: "匀速横移", Mechanism: "镜头匀速横向推移"},
		},
	}
}

// 判据生成：mechanism+use_hint 套模板；落点表配对；camera 带防误杀口径；
// 边界帧提示只在有 transition 判据时出现（§4.6 成本策略的可见面）。
func TestSignatureCriteria(t *testing.T) {
	ss := &contract.StyleSamples{SignatureAbsorption: []contract.SigAbsorption{
		{SigID: "sig1", LandsIn: contract.LandsTransitionPolicy, How: "章节数字卡被上一段柱状图推出画外"},
	}}
	crits := signatureCriteria(testBrief(), ss)
	if len(crits) != 3 {
		t.Fatalf("判据 = 简报特色全量: %+v", crits)
	}
	q1 := crits[0].Question()
	for _, want := range []string{
		"段间切换·道具递接转场",
		"前一画面主体推出画外，后一画面同位置跟进",
		"设计落点：transition_policy：章节数字卡被上一段柱状图推出画外",
		"适用：段间切换",
	} {
		if !strings.Contains(q1, want) {
			t.Fatalf("transition 判据缺 %q: %s", want, q1)
		}
	}
	if !strings.Contains(crits[2].Question(), "拿不准判 absorbed") {
		t.Fatal("camera 判据应带防误杀口径（静帧只能粗判）")
	}
	if strings.Contains(crits[1].Question(), "拿不准判 absorbed") {
		t.Fatal("palette 判据不该有 camera 口径")
	}
	if !hasDynamicCrit(crits) {
		t.Fatal("含 transition/camera = 动态")
	}
	if hasDynamicCrit(crits[1:2]) {
		t.Fatal("纯 palette 不算动态")
	}
	// 无简报 = 无判据
	if crits := signatureCriteria(nil, ss); crits != nil {
		t.Fatalf("无简报无判据: %+v", crits)
	}
	// 无落点表（重做后设计未登记）：判据仍在，只是不带落点
	c2 := signatureCriteria(testBrief(), nil)
	if strings.Contains(c2[0].Question(), "设计落点") {
		t.Fatal("无落点表不该渲染落点")
	}
}

// 判定处理：missing/drifted 触发失败；判据原句进回喂文本（含落点提示）。
func TestSigVerdictHandling(t *testing.T) {
	crits := signatureCriteria(testBrief(), &contract.StyleSamples{SignatureAbsorption: []contract.SigAbsorption{
		{SigID: "sig1", LandsIn: contract.LandsTransitionPolicy, How: "数字卡推出"},
	}})
	if sigFailed([]sigVerdictRow{{SigID: "sig1", Verdict: "absorbed"}}) {
		t.Fatal("absorbed 不算失败")
	}
	rows := []sigVerdictRow{
		{SigID: "sig1", Verdict: "missing", Note: "边界是硬切"},
		{SigID: "sig2", Verdict: "absorbed"},
	}
	if !sigFailed(rows) {
		t.Fatal("missing 应失败")
	}
	fb := sigFeedback(rows, crits)
	for _, want := range []string{
		"强参考特色判定未过",
		"[特色·缺失] 道具递接转场",
		"段间切换 是否实现了「道具递接转场」", // 判据原句
		"（审查备注：边界是硬切）",
		"→ 落点 transition_policy：数字卡推出",
	} {
		if !strings.Contains(fb, want) {
			t.Fatalf("回喂缺 %q:\n%s", want, fb)
		}
	}
	if strings.Contains(fb, "紫青撞色") {
		t.Fatal("absorbed 的特色不该进回喂")
	}
	drifted := []sigVerdictRow{{SigID: "sig2", Verdict: "drifted"}}
	if !sigFailed(drifted) {
		t.Fatal("drifted 应失败")
	}
	if !strings.Contains(sigFeedback(drifted, crits), "[特色·走样] 紫青撞色") {
		t.Fatal("drifted 标签应为走样")
	}
}

// 判据章节：输出形状带 signatures 段；边界帧说明按张数出现；无判据返回空。
func TestCriteriaBlock(t *testing.T) {
	crits := signatureCriteria(testBrief(), nil)
	blk := criteriaBlock(crits, 6)
	for _, want := range []string{
		"特色判据（用户选定的强参考",
		"absorbed/drifted/missing",
		`"signatures": [{"sig_id":"sig1"`,
		"6 张段边界前后帧序列",
		"判的是手法不是内容",
	} {
		if !strings.Contains(blk, want) {
			t.Fatalf("判据章节缺 %q:\n%s", want, blk)
		}
	}
	if strings.Contains(criteriaBlock(crits, 0), "段边界前后帧序列") {
		t.Fatal("无边界帧不该有该说明")
	}
	if criteriaBlock(nil, 0) != "" {
		t.Fatal("无判据应返回空")
	}
}

// extractBoundaryFrames 的成本纪律（不跑 ffmpeg，只验路径策略）：
// 首段（idx=1）无上边界 → nil；上一段段片缺失 → nil。
func TestBoundaryFramesGate(t *testing.T) {
	p := pipeline.NewProject(t.TempDir(), "ptest")
	if got := extractBoundaryFrames(t.Context(), p, 1); got != nil {
		t.Fatal("首段无上边界应返回 nil")
	}
	if got := extractBoundaryFrames(t.Context(), p, 3); got != nil {
		t.Fatal("无段片应返回 nil")
	}
}
