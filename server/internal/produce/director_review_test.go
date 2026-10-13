package produce

// M2 审片 pure tests：节拍锚定（词级换算与 ASR 漂移兜底）/ 抽帧计划（预算与母题
// 钉住）/ 考卷与回喂文本 / 门禁分支。LLM/snapshot 无 mock 先例，真链路走
// TestDirectorReviewManual（DIRECTOR_TEST=1，同 TestPreReviewManual 模式）。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/pipeline"
)

func anchorSeg() contract.Segment {
	return contract.Segment{ID: "seg01", Idx: 1, Key: "k", DurationHint: 10,
		Narration: "你此刻的念头，可能早已被写定。",
		Beats: []contract.Beat{
			{Quote: "你此刻的念头", Expect: "粒子汇聚"},
			{Quote: "可能早已被写定", Expect: "金眼定格"},
			{Quote: "而这句话不在旁白里也不会出现", Expect: "兜底"},
		}}
}

func anchorVoices() voiceMeta {
	return voiceMeta{ID: "seg01", DurationS: 9, Words: []wordTiming{
		{Text: "你", Start: 0.1, End: 0.3}, {Text: "此刻", Start: 0.3, End: 0.6},
		{Text: "的念头", Start: 0.6, End: 1.1}, {Text: "，", Start: 1.1, End: 1.1},
		{Text: "可能", Start: 4.0, End: 4.4}, {Text: "早已被写定", Start: 4.4, End: 5.2},
		{Text: "。", Start: 5.2, End: 5.2},
	}}
}

// 锚句命中词流 → 起始词 start；ASR 漂移找不到 → 节拍序均分兜底。
func TestBeatAnchor(t *testing.T) {
	seg, v := anchorSeg(), anchorVoices()
	dur := v.DurationS + contract.SegTail
	if got := beatAnchor(seg, v, 0, 3, dur); got != 0.1 {
		t.Fatalf("首拍应锚在首词 start=0.1，得到 %v", got)
	}
	if got := beatAnchor(seg, v, 1, 3, dur); got != 4.0 {
		t.Fatalf("次拍应锚在「可能」start=4.0，得到 %v", got)
	}
	// 第三拍锚句不在词流（模拟 ASR 数字写法漂移）→ 均分兜底 dur*3/4
	want := dur * 3.0 / 4.0
	if got := beatAnchor(seg, v, 2, 3, dur); got != want {
		t.Fatalf("漂移锚句应均分兜底 %v，得到 %v", want, got)
	}
	// 单拍且锚句漂移（无弧线的手工节拍遇 ASR 差异）→ 段中点
	one := seg
	one.Beats = []contract.Beat{{Quote: "这句在词流里找不到", Expect: "x"}}
	if got := beatAnchor(one, v, 0, 1, dur); got != dur/2 {
		t.Fatalf("单拍应取中点 %v，得到 %v", dur/2, got)
	}
}

func reviewStoryboard(nSeg int) *contract.Storyboard {
	sb := &contract.Storyboard{TimingBasis: "narration"}
	ids := make([]string, nSeg)
	for i := 0; i < nSeg; i++ {
		ids[i] = fmt.Sprintf("seg%02d", i+1)
		sb.Segments = append(sb.Segments, contract.Segment{
			ID: ids[i], Idx: i + 1, Key: fmt.Sprintf("段%d", i+1), DurationHint: 10,
			Narration: fmt.Sprintf("第%d段的旁白，从开头讲到结尾。", i+1),
			Beats: []contract.Beat{
				{Quote: fmt.Sprintf("第%d段的旁白", i+1), Expect: "开场主体"},
				{Quote: "从开头讲到结尾", Expect: "收尾画面"},
			},
		})
	}
	half := nSeg / 2
	if half < 1 {
		half = 1
	}
	sb.Arc = &contract.Arc{Spine: "从立到破", Chapters: []contract.ArcChapter{
		{Title: "立", Mood: "平缓", Tension: 2, Segs: ids[:half]},
		{Title: "破", Mood: "爆发", Tension: 5, Segs: ids[half:]},
	}}
	sb.Motifs = []contract.Motif{{Name: "全知之眼", Desc: "金色眼睛", Appearances: []contract.MotifApp{
		{SegID: ids[0], Role: "establish"}, {SegID: ids[nSeg-1], Role: "echo"},
	}}}
	return sb
}

func reviewVoices(sb *contract.Storyboard) map[string]voiceMeta {
	out := map[string]voiceMeta{}
	for _, seg := range sb.Segments {
		out[seg.ID] = voiceMeta{ID: seg.ID, DurationS: 9.5, Words: []wordTiming{
			{Text: "词", Start: 0.2, End: 9.0},
		}}
	}
	return out
}

func writeFrames(t *testing.T, p *pipeline.Project, sb *contract.Storyboard) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(p.Dir, "compositions", "frames"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, seg := range sb.Segments {
		if err := os.WriteFile(p.Artifact("compositions/frames/"+seg.ID+".html"), []byte("<template/>"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// 抽帧计划：时间正序；母题段两帧钉住；帧数超限按优先级截断。
func TestDirectorShots(t *testing.T) {
	p := &pipeline.Project{ID: "t", Dir: t.TempDir(), Ctx: context.Background()}
	sb := reviewStoryboard(9)
	voices := reviewVoices(sb)
	writeFrames(t, p, sb)

	shots := directorShots(p, sb, voices)
	if len(shots) != 9*2 {
		t.Fatalf("9 段×首尾拍应 18 帧，得到 %d", len(shots))
	}
	for i := 1; i < len(shots); i++ {
		if shots[i].At <= shots[i-1].At {
			t.Fatalf("帧应按时间正序: [%d]=%v ≤ [%d]=%v", i, shots[i].At, i-1, shots[i-1].At)
		}
	}
	for _, sh := range shots {
		if sh.SegID == "seg01" || sh.SegID == "seg09" {
			if sh.Motifs == "" {
				t.Fatalf("母题段 %s 的帧应带母题标记", sh.SegID)
			}
		} else if sh.Motifs != "" {
			t.Fatalf("非母题段 %s 不该带母题标记", sh.SegID)
		}
	}
	// 首段首拍带章标签
	var first *directorShot
	for i := range shots {
		if shots[i].SegID == "seg01" && shots[i].BeatIdx == 0 {
			first = &shots[i]
		}
	}
	if first == nil || !strings.Contains(first.Chapter, "立") || !first.First {
		t.Fatalf("seg01 首拍应带章「立」与开篇标记: %+v", first)
	}

	// 超限截断：15 段 = 30 候选 > 24 → 母题段（2 段×2 帧）钉住，其余按首拍优先
	p2 := &pipeline.Project{ID: "t2", Dir: t.TempDir(), Ctx: context.Background()}
	sb2 := reviewStoryboard(15)
	writeFrames(t, p2, sb2)
	shots2 := directorShots(p2, sb2, reviewVoices(sb2))
	if len(shots2) > directorMaxShots {
		t.Fatalf("帧数应 ≤ %d，得到 %d", directorMaxShots, len(shots2))
	}
	motifN := 0
	for _, sh := range shots2 {
		if sh.Motifs != "" {
			motifN++
		}
	}
	if motifN != 4 {
		t.Fatalf("母题两段的首尾 4 帧必须全部钉住，得到 %d", motifN)
	}
}

// 门禁分支：开关关 / 无导演字段 / 缺 audio_meta / 短片跳过（全部在 snapshot 前返回）。
func TestDirectorReviewGates(t *testing.T) {
	r := &Runner{}
	mkProject := func(writeProject, writeSB, writeMeta bool, sb *contract.Storyboard) *pipeline.Project {
		p := &pipeline.Project{ID: "t", Dir: t.TempDir(), Ctx: context.Background()}
		if writeProject {
			os.WriteFile(p.Artifact("project.json"), []byte(`{"director_review":false}`), 0o644)
		}
		if writeSB && sb != nil {
			os.MkdirAll(filepath.Join(p.Dir, "storyboards"), 0o755)
			b, _ := json.Marshal(sb)
			os.WriteFile(p.Artifact("storyboards/storyboard.json"), b, 0o644)
		}
		if writeMeta {
			meta := map[string]any{"voices": []any{map[string]any{"id": "seg01", "duration_s": 9.5}}}
			b, _ := json.Marshal(meta)
			os.WriteFile(p.Artifact("audio_meta.json"), b, 0o644)
		}
		return p
	}
	lastEvent := func(p *pipeline.Project) string {
		b, err := os.ReadFile(p.Artifact("manifest.jsonl"))
		if err != nil {
			t.Fatal("门禁分支必须已记账")
		}
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		var ev struct {
			Event string `json:"event"`
		}
		if json.Unmarshal([]byte(lines[len(lines)-1]), &ev) != nil {
			t.Fatal("manifest 行解析失败")
		}
		return ev.Event
	}

	sb := reviewStoryboard(3) // 3 段短片（带节拍）
	if err := r.directorReview(context.Background(), mkProject(true, true, true, sb)); err != nil {
		t.Fatal(err)
	}
	if p := mkProject(true, true, true, sb); func() bool { _ = r.directorReview(context.Background(), p); return false }() {
		t.Fatal("unreachable")
	}

	// 开关关（storyboard/meta 无关）
	pOff := mkProject(true, false, false, nil)
	if err := r.directorReview(context.Background(), pOff); err != nil {
		t.Fatal(err)
	}
	if ev := lastEvent(pOff); ev != "director.review_off" {
		t.Fatalf("开关关应记 review_off，得到 %s", ev)
	}
	// 无导演字段（普通分镜，无 beats/arc）
	plain := &contract.Storyboard{TimingBasis: "narration", Segments: []contract.Segment{
		{ID: "seg01", Idx: 1, Key: "k", Narration: "n", DurationHint: 9},
	}}
	pPlain := mkProject(false, true, true, plain)
	if err := r.directorReview(context.Background(), pPlain); err != nil {
		t.Fatal(err)
	}
	if ev := lastEvent(pPlain); ev != "director.skipped_no_plan" {
		t.Fatalf("无导演字段应记 skipped_no_plan，得到 %s", ev)
	}
	// 有节拍但缺 audio_meta → 审片跳过（不是 no_plan）
	pNoMeta := mkProject(false, true, false, reviewStoryboard(9))
	if err := r.directorReview(context.Background(), pNoMeta); err != nil {
		t.Fatal(err)
	}
	if ev := lastEvent(pNoMeta); ev != "director.review_skip" {
		t.Fatalf("缺 audio_meta 应记 review_skip，得到 %s", ev)
	}
	// 短片（9 段但配音总时长 <120s → 9×9.85≈88.6s 且段数 9≥8？段数条件独立：
	// 段数 <8 且总时长 <120 才跳。9 段 88s → 段数不满足 <8 → 不跳，会走到 shots；
	// 用 3 段验证短片跳过）
	pShort := mkProject(false, true, true, sb)
	if err := r.directorReview(context.Background(), pShort); err != nil {
		t.Fatal(err)
	}
	if ev := lastEvent(pShort); ev != "director.skipped_short" {
		t.Fatalf("3 段 29s 应记 skipped_short，得到 %s", ev)
	}
}

// 考卷：弧线/母题/帧身份表/四查枚举齐备；意见回喂带节拍重申与设计系统边界。
func TestDirectorReviewPromptAndFeedback(t *testing.T) {
	sb := reviewStoryboard(4)
	shots := []directorShot{
		{At: 0.5, SegID: "seg01", Key: "段1", BeatIdx: 0, Beat: sb.Segments[0].Beats[0], Chapter: "立", First: true, Motifs: "全知之眼"},
		{At: 19.7, SegID: "seg02", Key: "段2", BeatIdx: 1, Beat: sb.Segments[1].Beats[1], Chapter: "立"},
	}
	s := directorReviewPrompt(sb, shots)
	for _, want := range []string{
		"主线：从立到破",
		"「立」平缓（张力 2/5，seg01–seg02）",
		"- 「全知之眼」：金色眼睛——出场 seg01(establish)、seg04(echo)",
		"图1 = 0.5s「seg01」段1 第 1 拍「第1段的旁白」——此刻画面应有：开场主体（章「立」开篇）；母题「全知之眼」应在此可见",
		"expectation_miss", "motif_missing", "pacing", "deadair",
		"seg_id 必须取时刻表里出现过的段 id",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("审片考卷缺 %q\n---\n%s", want, s)
		}
	}
	fb := directorFeedback(sb.Segments[0], []directorIssue{
		{SegID: "seg01", Kind: "expectation_miss", Severity: "high", Note: "图1 无粒子", Suggestion: "补粒子汇聚"},
	})
	for _, want := range []string{
		"[expectation_miss] 图1 无粒子 → 补粒子汇聚",
		"设计系统/皮肤/字体不动",
		"「第1段的旁白」→ 开场主体",
	} {
		if !strings.Contains(fb, want) {
			t.Fatalf("意见回喂缺 %q\n---\n%s", want, fb)
		}
	}
}

// frameQA 考卷的节拍维度（M2 顺手项）：有节拍出预期对照检查项，无节拍零占位。
func TestReviewPromptBeats(t *testing.T) {
	p := &pipeline.Project{ID: "t", Dir: t.TempDir(), Ctx: context.Background()}
	seg := anchorSeg()
	s := reviewPrompt(p, seg, anchorVoices(), nil, 0, nil, 0)
	for _, want := range []string{
		"本段节拍（导演定的逐拍画面预期）",
		"「你此刻的念头」→ 粒子汇聚",
		"内容缺失——代码不报错但少演了内容",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("frameQA 考卷缺 %q\n---\n%s", want, s)
		}
	}
	plain := seg
	plain.Beats = nil
	if s2 := reviewPrompt(p, plain, anchorVoices(), nil, 0, nil, 0); strings.Contains(s2, "本段节拍") {
		t.Fatal("无节拍不该出现节拍块")
	}
}

// 手动实测：DIRECTOR_TEST=1 时对现有项目跑一次审片链路（真 snapshot + 真视觉模型；
// high 段会真的重出场景文件——只在测试项目上跑）。
//
//	cd server && DIRECTOR_TEST=1 DIRECTOR_PROJECT=<项目ID> go test ./internal/produce -run TestDirectorReviewManual -v
func TestDirectorReviewManual(t *testing.T) {
	if os.Getenv("DIRECTOR_TEST") == "" {
		t.Skip("手动测试：设 DIRECTOR_TEST=1 运行")
	}
	dataDir := os.Getenv("DIRECTOR_DATA")
	if dataDir == "" {
		dataDir = "../../../data"
	}
	proj := os.Getenv("DIRECTOR_PROJECT")
	if proj == "" {
		t.Skip("设 DIRECTOR_PROJECT=<项目ID> 指定测试项目")
	}
	r := &Runner{DataDir: dataDir, RootDir: "../../../"}
	p := pipeline.NewProject(dataDir, proj)
	if err := r.directorReview(t.Context(), p); err != nil {
		t.Fatalf("directorReview: %v", err)
	}
}
