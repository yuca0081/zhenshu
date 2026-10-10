package produce

// pure tests：preReviewTimes 时刻映射 / preReviewOn 开关 / staticCrits 过滤 /
// issuesFeedback 对预审帧位的回喂。LLM/python/CLI 无 mock 先例，真链路走
// TestPreReviewManual（PRE_REVIEW_TEST=1）。
//
// 时刻期望值一律用与实现相同的公式现算（round2(dur/2) 等），不手抄两位小数——
// dur=DurationS+SegTail 是二进制不精确数，手抄值会因浮点取整方向翻车。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/events"
	"webpage-video-agent/server/internal/pipeline"
)

func TestPreReviewTimes(t *testing.T) {
	dir := t.TempDir()
	p := &pipeline.Project{ID: "t", Dir: dir, Ctx: context.Background()}
	// seg01/seg03 有场景文件；seg02 缺场景文件、seg04 缺旁白 → 都跳过但时刻轴照累计
	if err := os.MkdirAll(filepath.Join(dir, "compositions", "frames"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"seg01", "seg03"} {
		if err := os.WriteFile(filepath.Join(dir, "compositions", "frames", id+".html"), []byte("<template/>"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sb := &contract.Storyboard{Segments: []contract.Segment{
		{ID: "seg01"}, {ID: "seg02"}, {ID: "seg03"}, {ID: "seg04"},
	}}
	voices := map[string]voiceMeta{
		"seg01": {ID: "seg01", DurationS: 4},
		"seg02": {ID: "seg02", DurationS: 10},
		"seg03": {ID: "seg03", DurationS: 5},
		"seg04": {ID: "seg04", DurationS: 3},
	}
	ats, t2seg := preReviewTimes(p, sb, voices)
	d1, d2, d3 := 4+contract.SegTail, 10+contract.SegTail, 5+contract.SegTail
	acc3 := d1 + d2 // seg02 被跳过但 acc 照加
	want := []float64{
		round2(0.5), round2(d1 / 2), round2(d1 - 0.5),
		round2(acc3 + 0.5), round2(acc3 + d3/2), round2(acc3 + d3 - 0.5),
	}
	if len(ats) != len(want) {
		t.Fatalf("ats = %v, want %v", ats, want)
	}
	for i, w := range want {
		if ats[i] != w {
			t.Errorf("ats[%d] = %v, want %v", i, ats[i], w)
		}
	}
	for _, at := range ats {
		id := t2seg[at]
		if id != "seg01" && id != "seg03" {
			t.Errorf("t2seg[%v] = %q, 想要 seg01/seg03（缺场景文件的段不进预审）", at, id)
		}
	}
	if got := segCount(t2seg); got != 2 {
		t.Errorf("segCount = %d, want 2", got)
	}
}

func TestPreReviewTimesTinySeg(t *testing.T) {
	dir := t.TempDir()
	p := &pipeline.Project{ID: "t", Dir: dir, Ctx: context.Background()}
	if err := os.MkdirAll(filepath.Join(dir, "compositions", "frames"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "compositions", "frames", "seg01.html"), []byte("<template/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 极短段（dur=0.45）：0.5 钳到段尾渐变前，中点/尾点落在黑场渐变区被丢弃 → 单点
	sb := &contract.Storyboard{Segments: []contract.Segment{{ID: "seg01"}}}
	ats, t2seg := preReviewTimes(p, sb, map[string]voiceMeta{"seg01": {ID: "seg01", DurationS: 0.1}})
	want := round2(0.1 + contract.SegTail - 0.05)
	if len(ats) != 1 || ats[0] != want {
		t.Fatalf("极短段 ats = %v, want [%v]", ats, want)
	}
	if t2seg[ats[0]] != "seg01" {
		t.Errorf("t2seg 归段错误: %v", t2seg)
	}
}

func TestPreReviewOn(t *testing.T) {
	dir := t.TempDir()
	p := &pipeline.Project{ID: "t", Dir: dir, Ctx: context.Background()}
	if !preReviewOn(p) { // 无 project.json：缺省开
		t.Error("缺省应开启")
	}
	write := func(json string) {
		if err := os.WriteFile(filepath.Join(dir, "project.json"), []byte(json), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"bgm":"calm"}`)
	if !preReviewOn(p) {
		t.Error("字段缺省应开启")
	}
	write(`{"pre_review":false}`)
	if preReviewOn(p) {
		t.Error("显式 false 应关闭")
	}
	write(`{"pre_review":true}`)
	if !preReviewOn(p) {
		t.Error("显式 true 应开启")
	}
}

func TestStaticCrits(t *testing.T) {
	crits := []sigCriterion{
		{Sig: contract.Signature{ID: "sig1", Kind: contract.SigKindTransition}},
		{Sig: contract.Signature{ID: "sig2", Kind: contract.SigKindCamera}},
		{Sig: contract.Signature{ID: "sig3", Kind: contract.SigKindPalette}},
		{Sig: contract.Signature{ID: "sig4", Kind: contract.SigKindMotion}},
	}
	got := staticCrits(crits)
	if len(got) != 2 || got[0].Sig.ID != "sig3" || got[1].Sig.ID != "sig4" {
		t.Errorf("staticCrits = %+v, want 只留 sig3/sig4（转场/运镜留渲后）", got)
	}
}

func TestIssuesFeedbackMidFrame(t *testing.T) {
	v := frameVerdict{Issues: []struct {
		Frame      string `json:"frame"`
		Severity   string `json:"severity"`
		Element    string `json:"element"`
		Problem    string `json:"problem"`
		Suggestion string `json:"suggestion"`
	}{
		{Frame: "mid", Severity: "high", Element: "标题", Problem: "重叠", Suggestion: "拉开"},
	}}
	fb := issuesFeedback(v)
	if !strings.Contains(fb, "中段帧") {
		t.Errorf("回喂文本应含「中段帧」: %s", fb)
	}
	if strings.Contains(fb, "渲染后") {
		t.Errorf("回喂文本不应再写「渲染后」（预审也共用）: %s", fb)
	}
}

// 手动实测：PRE_REVIEW_TEST=1 时对现有项目跑一次预审链路（真 snapshot + 真视觉模型，
// 会重出问题段的场景文件——只在测试项目上跑）。
//   cd server && PRE_REVIEW_TEST=1 PRE_REVIEW_PROJECT=<项目ID> go test ./internal/produce -run TestPreReviewManual -v
func TestPreReviewManual(t *testing.T) {
	if os.Getenv("PRE_REVIEW_TEST") == "" {
		t.Skip("手动测试：设 PRE_REVIEW_TEST=1 运行")
	}
	dataDir := os.Getenv("PRE_REVIEW_DATA")
	if dataDir == "" {
		dataDir = "../../../data"
	}
	proj := os.Getenv("PRE_REVIEW_PROJECT")
	if proj == "" {
		t.Skip("设 PRE_REVIEW_PROJECT=<项目ID> 指定测试项目")
	}
	r := &Runner{DataDir: dataDir, RootDir: "../../../", Hub: events.NewHub()}
	p := pipeline.NewProject(dataDir, proj)
	res, err := r.preReview(t.Context(), p)
	if err != nil {
		t.Fatalf("preReview: %v", err)
	}
	t.Logf("ran=%v reviewAll=%v repaired=%v", res.Ran, res.ReviewAll, res.Repaired)
}
