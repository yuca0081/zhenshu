package produce

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/pipeline"
)

func TestSentenceEndTimes(t *testing.T) {
	sents := []contract.Sentence{
		{ID: "N1", Text: "有一种蓝先于名字存在。"},
		{ID: "N2", Text: "它出现在画家的正中央。"},
	}
	words := []wordTiming{
		{Text: "有", Start: 0.0, End: 0.2},
		{Text: "一种", Start: 0.2, End: 0.6},
		{Text: "蓝", Start: 0.6, End: 0.8},
		{Text: "先于", Start: 0.8, End: 1.2},
		{Text: "名字", Start: 1.2, End: 1.5},
		{Text: "存在", Start: 1.5, End: 1.9},
		{Text: "它", Start: 2.0, End: 2.2},
		{Text: "出现", Start: 2.2, End: 2.6},
		{Text: "在", Start: 2.6, End: 2.8},
		{Text: "画家", Start: 2.8, End: 3.2},
		{Text: "的", Start: 3.2, End: 3.3},
		{Text: "正中央", Start: 3.3, End: 3.8},
	}
	ends := sentenceEndTimes(sents, words)
	if ends["N1"] != 1.9 {
		t.Fatalf("N1 句末应为 1.9，得到 %v", ends["N1"])
	}
	if ends["N2"] != 3.8 {
		t.Fatalf("N2 句末应为 3.8，得到 %v", ends["N2"])
	}
}

func TestSentenceEndTimesWordCrossesBoundary(t *testing.T) {
	// 词跨句边界：最后一个词同时覆盖两句尾巴，分配给先到界的句
	sents := []contract.Sentence{
		{ID: "N1", Text: "第一种蓝。"},
		{ID: "N2", Text: "第二种灰。"},
	}
	words := []wordTiming{
		{Text: "第一种", Start: 0, End: 0.5},
		{Text: "蓝。第二", Start: 0.5, End: 1.0},
		{Text: "种灰。", Start: 1.0, End: 1.4},
	}
	ends := sentenceEndTimes(sents, words)
	if ends["N1"] != 1.0 {
		t.Fatalf("跨界词应归 N1（end=1.0），得到 %v", ends["N1"])
	}
	if ends["N2"] != 1.4 {
		t.Fatalf("N2 应为 1.4，得到 %v", ends["N2"])
	}
}

func TestAnchorFramesCapAndPins(t *testing.T) {
	// 10 句 → 首末帧钉死 + 句末帧均匀采样到 6 张总量
	var sents []contract.Sentence
	ends := map[string]float64{}
	for i := 1; i <= 10; i++ {
		id := contract.Sentence{ID: "N" + string(rune('0'+i))}.ID
		sents = append(sents, contract.Sentence{ID: id, Text: "第几句话。"})
		ends[id] = float64(i)
	}
	frames := anchorFrames(sents, ends, 12)
	if len(frames) != anchorFrameCap {
		t.Fatalf("锚点帧应封顶 %d，得到 %d", anchorFrameCap, len(frames))
	}
	if frames[0].Label != "首帧" || frames[len(frames)-1].Label != "末帧" {
		t.Fatalf("首末帧必须保留：%v / %v", frames[0].Label, frames[len(frames)-1].Label)
	}
	for i := 1; i < len(frames); i++ {
		if frames[i].T <= frames[i-1].T {
			t.Fatalf("帧时刻应递增: %v", frames)
		}
	}
}

func TestSegFrameFileJunctionPath(t *testing.T) {
	cases := map[string]string{
		"compositions/frames/seg01.html":                         "compositions/frames/seg01.html",
		`D:\proj\.hf-check\seg01\compositions\frames\seg01.html`: "compositions/frames/seg01.html",
		"source index.html":                                      "",
		"compositions/caps/cap-seg01.html":                       "",
	}
	for in, want := range cases {
		got, ok := segFrameFile(in)
		if want == "" {
			if ok {
				t.Errorf("%q 应不命中", in)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("%q → %q,%v；期望 %q", in, got, ok, want)
		}
	}
}

func TestBrakeTracker(t *testing.T) {
	// 新语义（2026-10-17 实测后收紧）：跨轨道阈值 = max(3, 段数/4)，
	// 且过审轨道 clear 后撤销记账——随机性的单发 high 不再滚成整片死刑。
	b := newBrakeTracker(8)
	if r := b.check("seg01", []string{"fact@N3"}); r != "" {
		t.Fatalf("首轮不应刹车: %s", r)
	}
	if r := b.check("seg02", []string{"style@t=3.0"}); r != "" {
		t.Fatalf("不同类别不同段不应刹车: %s", r)
	}
	// 跨轮累计不再致命：另一段稍后出现同类别 high（第 2 个轨道 < 阈值 3）
	if r := b.check("seg03", []string{"fact@N5"}); r != "" {
		t.Fatalf("两段跨轮同类别 high 不应再触发系统性刹车（阈值 3）: %s", r)
	}
	// seg01 修复过审 → clear → 它不再算系统性证据
	b.clear("seg01")
	if r := b.check("seg04", []string{"fact@N7"}); r != "" {
		t.Fatalf("过审清除后剩余轨道数不应刹车: %s", r)
	}
	// 同轮/连续累计到阈值才刹车（第 3、4 个轨道）
	if r := b.check("seg05", []string{"fact@N9"}); r == "" {
		t.Fatal("达到阈值（max(3, 段数/4)）个轨道同类别 high 应触发系统性刹车")
	}
	// 同签名两轮（新 tracker）
	b2 := newBrakeTracker(8)
	_ = b2.check("seg01", []string{"fact@N3"})
	if r := b2.check("seg01", []string{"fact@N3", "style@t=1"}); r == "" {
		t.Fatal("同签名连续两轮 high 应触发签名刹车")
	}
}

func TestBndAnchorT(t *testing.T) {
	if v, ok := bndAnchorT("b02|t=-0.2"); !ok || v != -0.2 {
		t.Fatalf("b02|t=-0.2 → %v %v", v, ok)
	}
	if v, ok := bndAnchorT("b02|t=1.5"); !ok || v != 1.5 {
		t.Fatalf("b02|t=1.5 → %v %v", v, ok)
	}
	if _, ok := bndAnchorT("N3"); ok {
		t.Fatal("段 anchor 不应解析")
	}
}

func TestSegSentencesPrefix(t *testing.T) {
	script := &contract.Script{Sentences: []contract.Sentence{
		{ID: "N1", Text: "第一句。"},
		{ID: "N2", Text: "第二句。"},
		{ID: "N3", Text: "第三句。"},
	}}
	seg := contract.Segment{ID: "seg01", Narration: "第一句。第二句。"}
	got := segSentences(script, seg)
	if len(got) != 2 || got[0].ID != "N1" || got[1].ID != "N2" {
		t.Fatalf("应还原 N1/N2，得到 %v", got)
	}
}

func TestSegFactsRelevance(t *testing.T) {
	script := &contract.Script{
		FactSheet: []contract.FactItem{
			{Claim: "齐马蓝首次出现于1985年的短篇", URL: "https://example.com/a"},
			{Claim: "完全无关的条目讲的是别的事情"},
		},
	}
	seg := contract.Segment{ID: "seg01", Narration: "齐马蓝是一种蓝，1985 年诞生。"}
	got := segFacts(script, seg)
	if len(got) != 1 {
		t.Fatalf("应只命中相关条目，得到 %v", got)
	}
}

func TestFinalFramesDistribution(t *testing.T) {
	// 5 段 2 段落：段落界 2 帧 + 段界 5 帧 = 7 < 12 → 中点补到 12
	sb := &contract.Storyboard{}
	var tl []pipeline.TimelineRow
	for i := 1; i <= 5; i++ {
		id := "seg0" + string(rune('0'+i))
		sb.Segments = append(sb.Segments, contract.Segment{ID: id, Key: "段" + id})
		tl = append(tl, pipeline.TimelineRow{ID: id, Start: float64(i-1) * 10, End: float64(i) * 10})
	}
	frames, _ := finalFrames(nil, sb, tl)
	if len(frames) != 12 {
		t.Fatalf("帧格应补足 12，得到 %d", len(frames))
	}
	for i := 1; i < len(frames); i++ {
		if frames[i].T <= frames[i-1].T {
			t.Fatalf("终审帧时刻应递增: %v", frames)
		}
	}
}

func TestBoundaryTrackerRedispatchAfterRework(t *testing.T) {
	// 2026-10-19 萤火虫实测楔子回归：边界审打回路径不记账（genP/genN 留 -1），
	// 「在飞」标志必须由 auditBoundary 完成时自己释放（bndBusy）——旧版靠跟踪器在
	// 过审记账分支复位，打回路径永不复位：被打回段重写过审后该边界再无人重派，
	// waitAllPassed 干等看门狗。本测试锁定：busy 期间不重派；释放后代际失配即重派。
	c := &coordinator{
		journal:    map[string]journalLine{},
		auditCh:    make(chan auditReq, 8),
		passNotify: make(chan string, 8),
		bndBusy:    map[int]bool{},
	}
	ids := []string{"seg01", "seg02", "seg03"}
	for _, id := range ids {
		// gen=1 对齐 run() 恢复语义：账面 audited 时段代际取自 journal
		c.segs = append(c.segs, &segCtx{seg: contract.Segment{ID: id}, gen: 1, feedCh: make(chan string, 1)})
		c.journal["seg/"+id] = journalLine{Kind: "seg", ID: id, State: stAudited, Gen: 1}
	}
	for i := 0; i < 2; i++ {
		c.bounds = append(c.bounds, &boundaryCtx{
			id: fmt.Sprintf("b%02d", i+1), prev: ids[i], next: ids[i+1],
			genP: -1, genN: -1,
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.boundaryTracker(ctx)

	// ① 两侧都过审且未记账 → 两边界都派发
	got := map[int]bool{}
	for len(got) < 2 {
		select {
		case req := <-c.auditCh:
			if req.kind != "bnd" {
				t.Fatalf("应只派边界审: %+v", req)
			}
			if got[req.bi] {
				t.Fatalf("边界 %d 重复派发", req.bi)
			}
			got[req.bi] = true
		case <-time.After(3 * time.Second):
			t.Fatalf("边界审未被派发: got=%v", got)
		}
	}

	// ② b01 打回 seg02：busy 期间即使代际已失配也不得重派
	c.mu.Lock()
	if !c.bndBusy[0] {
		t.Fatal("派发后 bndBusy[0] 应为 true（在飞）")
	}
	c.segs[1].gen = 2 // 重写完成：dispatchRewrite→重写→下轮段审 gen+1
	c.journal["seg/seg02"] = journalLine{Kind: "seg", ID: "seg02", State: stAudited, Gen: 2}
	c.mu.Unlock()
	select {
	case req := <-c.auditCh:
		t.Fatalf("在飞期间不应重派: %+v", req)
	case <-time.After(2 * time.Second):
	}

	// ③ auditBoundary 完成（defer 释放 busy）→ b01 按新代际重派
	c.mu.Lock()
	c.bndBusy[0] = false
	c.mu.Unlock()
	select {
	case req := <-c.auditCh:
		if req.bi != 0 {
			t.Fatalf("应重派 b01（bi=0），实派 %+v", req)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("打回段重写过审后 b01 未被重派——旧楔子回归")
	}
}

func TestBoundaryTrackerWaitsForRewriteAfterRejection(t *testing.T) {
	// 2026-10-19 四路审查实锤回归：打回路径的 bndBusy 由 auditBoundary 完成时
	// 自释放（高-1 修复），但若被打回段的 journal 还停在 audited@旧代际（重写
	// 未落地），跟踪器 ≤800ms 内就用同一批像素重派边界审——假审计空烧、相同
	// 意见再打回把轮次烧穿后提前记「带伤放行」。修复=打回瞬间摘账 stRewriting
	// （同终审打回路径）。本测试锁定：摘账期间不重派；重写落地（同代际过审）后才派。
	c := &coordinator{
		journal:    map[string]journalLine{},
		auditCh:    make(chan auditReq, 8),
		passNotify: make(chan string, 8),
		bndBusy:    map[int]bool{},
	}
	c.segs = append(c.segs,
		&segCtx{seg: contract.Segment{ID: "seg01"}, gen: 1, feedCh: make(chan string, 1)},
		&segCtx{seg: contract.Segment{ID: "seg02"}, gen: 1, feedCh: make(chan string, 1)},
	)
	// seg01 已过审；seg02 刚被打回（摘账 rewriting，重写在飞）——auditBoundary
	// 打回路径落账后的真实瞬间状态。
	c.journal["seg/seg01"] = journalLine{Kind: "seg", ID: "seg01", State: stAudited, Gen: 1}
	c.journal["seg/seg02"] = journalLine{Kind: "seg", ID: "seg02", State: stRewriting}
	c.bounds = append(c.bounds, &boundaryCtx{id: "b01", prev: "seg01", next: "seg02", genP: -1, genN: -1})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.boundaryTracker(ctx)

	select {
	case req := <-c.auditCh:
		t.Fatalf("重写在飞期间不应重派边界审: %+v", req)
	case <-time.After(2 * time.Second):
	}

	// 重写落地：同代际过审记账 → 边界按失配重派
	c.mu.Lock()
	c.journal["seg/seg02"] = journalLine{Kind: "seg", ID: "seg02", State: stAudited, Gen: 1}
	c.mu.Unlock()
	select {
	case req := <-c.auditCh:
		if req.kind != "bnd" || req.bi != 0 {
			t.Fatalf("重写落地后应重派 b01: %+v", req)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("重写落地后 b01 未被重派")
	}
}

// 预算哨兵全链路：c.fail → tripErr → waitAllPassed → runWithAutoRetry 的
// errors.Is 必须认得（2026-10-20：waitAllPassed 字符串重建曾把链斩断，
// 预算闸触发后照样空跑两遍重试）。
func TestBudgetSentinelSurvivesWaitAllPassed(t *testing.T) {
	b := newBrakeTracker(4)
	b.tripErr("段链失败：", fmt.Errorf("seg08: %w", llm.ErrBudgetExceeded))
	err := b.failure()
	if !errors.Is(err, llm.ErrBudgetExceeded) {
		t.Fatalf("failure() 断链: %v", err)
	}
	if !strings.Contains(err.Error(), "段链失败：seg08") {
		t.Fatalf("文案变了: %v", err)
	}
	// 纯字符串刹车（看门狗/签名刹车）语义不变：可重跑，无哨兵
	b2 := newBrakeTracker(4)
	b2.trip("管线静默超过 32m0s 无进展")
	if errors.Is(b2.failure(), llm.ErrBudgetExceeded) {
		t.Fatal("字符串刹车不该冒充预算哨兵")
	}
	if b2.failure() == nil || !strings.Contains(b2.failure().Error(), "管线静默") {
		t.Fatal("字符串刹车文案丢失")
	}
}
