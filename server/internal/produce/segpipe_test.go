package produce

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/events"
	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/pipeline"
	"webpage-video-agent/server/internal/review"
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

func TestFinalFramesSkipFadeInBlack(t *testing.T) {
	// 2026-10-20 实测回归：段片自带 ~0.25s 淡入，起点精确帧必黑——终审 9 段锚点
	// 全踩黑帧，被判「后半段全黑」来回重写好段直到刹车。段起点锚必须 +0.4s 越过
	// 淡入；短段按 60% 收，永不越段尾。
	sb := &contract.Storyboard{}
	var tl []pipeline.TimelineRow
	for i := 1; i <= 9; i++ {
		id := fmt.Sprintf("seg%02d", i)
		sb.Segments = append(sb.Segments, contract.Segment{ID: id, Key: "段" + id})
		tl = append(tl, pipeline.TimelineRow{ID: id, Start: float64(i-1) * 7, End: float64(i) * 7})
	}
	tl[8].End = tl[8].Start + 1.0 // 末段压成 1s 短段：夹住 +0.4 不越界
	frames, _ := finalFrames(nil, sb, tl)
	byStart := map[float64]bool{}
	for _, r := range tl {
		byStart[round2(r.Start)] = true
	}
	for _, f := range frames {
		if byStart[f.T] {
			t.Fatalf("锚点 t=%.2f 正踩段起点（淡入黑帧）: %v", f.T, f.Label)
		}
	}
	last := tl[len(tl)-1]
	for _, f := range frames {
		if f.T > last.End {
			t.Fatalf("锚点 t=%.2f 越过末段尾 %.2f", f.T, last.End)
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

// 段审开关（2026-10-21 恢复段审）：默认 on，off/0/false 关、非法值视为开。
// markSegAuditedNoAudit 机检直过（关闸路径）：round/gen 前进、journal 落 audited、
// segAudited 为真——边界跟踪的代际机制靠这套记账工作，语义保持。
func TestSegAuditSwitchAndMarkAudited(t *testing.T) {
	t.Setenv("PRODUCE_SEG_AUDIT", "")
	if !segAuditOn() {
		t.Fatal("未配置时默认应开（2026-10-21 恢复段审）")
	}
	for _, v := range []string{"off", "0", "false", "OFF"} {
		t.Setenv("PRODUCE_SEG_AUDIT", v)
		if segAuditOn() {
			t.Fatalf("PRODUCE_SEG_AUDIT=%s 应关", v)
		}
	}
	t.Setenv("PRODUCE_SEG_AUDIT", "garbage")
	if !segAuditOn() {
		t.Fatal("非法值应视为开")
	}

	t.Setenv("PRODUCE_SEG_AUDIT", "")
	dir := t.TempDir()
	r := &Runner{DataDir: dir, RootDir: dir, Hub: events.NewHub()}
	p := pipeline.NewProject(dir, "p1")
	if err := os.MkdirAll(p.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sb := &contract.Storyboard{Segments: []contract.Segment{{ID: "seg01"}, {ID: "seg02"}}}
	c := newCoordinator(r, p, &review.Service{P: p, Emit: r.Hub.Emit}, sb, nil, nil, nil)

	for _, s := range c.segs {
		c.markSegAuditedNoAudit(s)
	}
	for _, s := range c.segs {
		e := c.segEntry(s.seg.ID)
		if e.State != stAudited || e.Gen != 1 || e.Round != 1 {
			t.Fatalf("%s 记账不符: %+v", s.seg.ID, e)
		}
		if !c.segAudited(s) {
			t.Fatalf("%s segAudited 应为真", s.seg.ID)
		}
	}
	if _, err := os.Stat(journalPath(p)); err != nil {
		t.Fatalf("journal 未落盘（断点恢复依据）: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.boundaryTracker(ctx)
	select {
	case req := <-c.auditCh:
		if req.kind != "bnd" || req.bi != 0 {
			t.Fatalf("机检直过后应派 b01 边界审: %+v", req)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("机检直过后边界审未被派发")
	}
}

// 边界审开关（2026-10-21 D5 撤边界审）：默认 off，on/1/true 开。
// allPassed 关闸时只等段：两侧段机检直过后即全过，边界对不参与（终审 stitch
// 查项兜底）；开闸时恢复原语义——边界未审不算全过。
func TestBndAuditSwitchAndAllPassed(t *testing.T) {
	t.Setenv("PRODUCE_BND_AUDIT", "")
	if bndAuditOn() {
		t.Fatal("未配置时默认应关（D5 撤边界审）")
	}
	for _, v := range []string{"on", "1", "true", "ON"} {
		t.Setenv("PRODUCE_BND_AUDIT", v)
		if !bndAuditOn() {
			t.Fatalf("PRODUCE_BND_AUDIT=%s 应开", v)
		}
	}

	t.Setenv("PRODUCE_SEG_AUDIT", "off")
	t.Setenv("PRODUCE_BND_AUDIT", "")
	dir := t.TempDir()
	r := &Runner{DataDir: dir, RootDir: dir, Hub: events.NewHub()}
	p := pipeline.NewProject(dir, "p1")
	if err := os.MkdirAll(p.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sb := &contract.Storyboard{Segments: []contract.Segment{{ID: "seg01"}, {ID: "seg02"}}}
	c := newCoordinator(r, p, &review.Service{P: p, Emit: r.Hub.Emit}, sb, nil, nil, nil)
	for _, s := range c.segs {
		c.markSegAuditedNoAudit(s)
	}
	if !c.allPassed() {
		t.Fatal("边界审关闸：全部段 audited 后应即全过（不等边界对）")
	}
	t.Setenv("PRODUCE_BND_AUDIT", "on")
	if c.allPassed() {
		t.Fatal("边界审开闸：边界未审不应全过")
	}
}

// 首写错峰（七刀#4）：第 n 个领号的首写目标开跑 = 纪元 + (n-1)×间隔；
// 间隔 0/非法关闭。1 号领号即开跑不等，2 号起等一个间隔。
func TestStaggerSlot(t *testing.T) {
	t.Setenv("PRODUCE_WRITE_STAGGER", "bogus")
	if d := (&coordinator{}).staggerSlot(); d != 0 {
		t.Fatalf("非法间隔应回退默认（此测只验不 panic），实得 %v", d)
	}
	t.Setenv("PRODUCE_WRITE_STAGGER", "0")
	if d := (&coordinator{}).staggerSlot(); d != 0 {
		t.Fatalf("间隔 0 应关闭错峰，实得 %v", d)
	}
	t.Setenv("PRODUCE_WRITE_STAGGER", "400ms")
	c := &coordinator{}
	if d := c.staggerSlot(); d != 0 {
		t.Fatalf("1 号首写不该等，实得 %v", d)
	}
	if d := c.staggerSlot(); d <= 0 || d > 400*time.Millisecond {
		t.Fatalf("2 号首写应等 ≤400ms 的正间隔，实得 %v", d)
	}
}

// 复核轮帧格过滤（终审重进瘦身）：保留重写段及其相邻段的锚点，更远的段丢弃；
// 重写段覆盖全片时自然退化为全量。
func TestFramesInSegs(t *testing.T) {
	tl := []pipeline.TimelineRow{
		{ID: "seg01", Start: 0, End: 10},
		{ID: "seg02", Start: 10, End: 20},
		{ID: "seg03", Start: 20, End: 30},
		{ID: "seg04", Start: 30, End: 40},
		{ID: "seg05", Start: 40, End: 50},
	}
	frames := []review.AnchorFrame{
		{T: 5, Label: "seg01"}, {T: 15, Label: "seg02"}, {T: 25, Label: "seg03"},
		{T: 35, Label: "seg04"}, {T: 45, Label: "seg05"}, {T: 49.99, Label: "seg05尾"},
	}
	got := framesInSegs(frames, tl, []string{"seg03"})
	if len(got) != 3 || got[0].Label != "seg02" || got[1].Label != "seg03" || got[2].Label != "seg04" {
		t.Fatalf("重写 seg03 应保留邻段 seg02/03/04 的帧，实得 %+v", got)
	}
	all := framesInSegs(frames, tl, []string{"seg01", "seg02", "seg03", "seg04", "seg05"})
	if len(all) != len(frames) {
		t.Fatalf("全段重写应退化为全量帧格，实得 %d/%d", len(all), len(frames))
	}
	none := framesInSegs(frames, tl, nil)
	if len(none) != 0 {
		t.Fatalf("空清单应得空帧格，实得 %+v", none)
	}
}

// 内容忠实条款（2026-10-20 降本 D1 对冲）与段间接续契约（2026-10-21 D5）：
// 注入包必须带这些硬要求——锁文案在位，防后续改提示词时无声脱落。
// 顺带锁稳定前缀布局的形状：段信息（含段 id）必须出现在所有共享块（契约摘要/
// 内容忠实/布局规则/动效手感）之后——段变量插中间曾把 GLM 前缀缓存斩断在 2944。
func TestSceneUserPromptHasContentFidelityClause(t *testing.T) {
	got := sceneUserPrompt(contract.Segment{ID: "seg01", Key: "k", Narration: "n"},
		voiceMeta{}, "", "", contract.Canvas{W: 1080, H: 1920, Aspect: "9:16"},
		"", "", "", "", "", false, "")
	for _, want := range []string{"内容忠实", "首拍", "末拍", "逐字一致", "首帧即景", "末帧定格"} {
		if !strings.Contains(got, want) {
			t.Fatalf("提示词缺硬条款关键词 %q", want)
		}
	}
	tail := got[strings.Index(got, "## 段信息"):]
	for _, shared := range []string{"## 契约摘要", "## 内容忠实", "## 布局规则", "## 动效手感"} {
		if strings.Contains(tail, shared) {
			t.Fatalf("共享块 %q 出现在段信息之后（前缀布局破坏，段变量必须垫尾）", shared)
		}
	}
}
