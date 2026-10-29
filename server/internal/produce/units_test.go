package produce

import (
	"os"
	"path/filepath"
	"testing"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/pipeline"
)

func segN(id, narr string, d float64) contract.Segment {
	return contract.Segment{ID: id, Key: id, Narration: narr, VisualBrief: id + " 画面提示"}
}

func voiceD(d float64) voiceMeta { return voiceMeta{DurationS: d} }

// BuildRenderUnits：段落聚合、格子数学（Σ(旁白+尾垫)）、拆分上限（>5 分镜或 >45s）、
// 编号（未拆 paraNN / 拆分 paraNNa/b）与孤儿分镜兜底（草稿引用不到的分镜挂最后段落）。
func TestBuildRenderUnits(t *testing.T) {
	paras := []contract.DraftParagraph{
		{Title: "甲", Segs: []string{"seg01", "seg02", "seg03"}},
		{Title: "乙", Segs: []string{"seg04", "seg05", "seg06", "seg07", "seg08", "seg09", "seg10", "seg11"}}, // 8 段超限 → 拆
	}
	sb := &contract.Storyboard{Segments: []contract.Segment{
		segN("seg01", "一一", 6), segN("seg02", "二二", 6), segN("seg03", "三三", 6),
		segN("seg04", "四", 6), segN("seg05", "五", 6), segN("seg06", "六", 6), segN("seg07", "七", 6),
		segN("seg08", "八", 6), segN("seg09", "九", 6), segN("seg10", "十", 6),
		segN("seg99", "孤儿", 6), // 草稿没引用 → 挂最后段落
	}}
	voices := map[string]voiceMeta{}
	for _, s := range sb.Segments {
		voices[s.ID] = voiceD(6.0)
	}
	units := BuildRenderUnits(paras, sb, voices)
	if len(units) != 3 { // 甲一段 + 乙拆两段
		t.Fatalf("应 3 单元（1+拆2），得到 %d: %+v", len(units), idsOf(units))
	}
	if units[0].ID != "para01" || units[1].ID != "para02a" || units[2].ID != "para02b" {
		t.Fatalf("编号应 para01/para02a/para02b: %+v", idsOf(units))
	}
	// 甲：3 格，每格 6+0.35，dur=19.05；乙拆两半各 ≤5 段；孤儿挂乙（后拆）
	if d := units[0].Dur - 3*(6+contract.SegTail); d > 1e-9 || d < -1e-9 {
		t.Fatalf("para01 时长 %v", units[0].Dur)
	}
	c := units[0].Segs[1]
	if d := c.Start - (6 + contract.SegTail); d > 1e-9 || d < -1e-9 {
		t.Fatalf("格子数学不对: %+v", c)
	}
	if units[0].Narration != "一一二二三三" {
		t.Fatalf("合并旁白 %q", units[0].Narration)
	}
	// 乙 8 段（含孤儿）拆两半：a=5 段、b=3 段（贪心装满）
	if len(units[1].Segs) != 5 || len(units[2].Segs) != 3 {
		t.Fatalf("拆分应 5+3: %d+%d", len(units[1].Segs), len(units[2].Segs))
	}
	last := units[2].Segs[len(units[2].Segs)-1].ID
	if last != "seg99" {
		t.Fatalf("孤儿分镜应挂最后单元末尾，得到 %s", last)
	}
	if units[1].Parts != 2 || units[2].Part != 2 || units[1].ParaIdx != units[2].ParaIdx {
		t.Fatalf("拆分元数据不对: %+v", units[1])
	}
}

// 45 秒上限独立于分镜数触发（4 段 × 12s = 48s > 45 → 拆）。
func TestBuildRenderUnitsDurSplit(t *testing.T) {
	paras := []contract.DraftParagraph{{Title: "长段", Segs: []string{"seg01", "seg02", "seg03", "seg04"}}}
	sb := &contract.Storyboard{Segments: []contract.Segment{
		segN("seg01", "a", 12), segN("seg02", "b", 12), segN("seg03", "c", 12), segN("seg04", "d", 12),
	}}
	voices := map[string]voiceMeta{"seg01": voiceD(12), "seg02": voiceD(12), "seg03": voiceD(12), "seg04": voiceD(12)}
	units := BuildRenderUnits(paras, sb, voices)
	if len(units) != 2 {
		t.Fatalf("48s 应拆 2: %d", len(units))
	}
	for _, u := range units {
		if u.Dur > unitMaxDur {
			t.Fatalf("单元超 45s: %v", u.Dur)
		}
	}
}

// UnitStoryboard：结构复制品（Arc/Motifs/Overlay 保留），Segments 换单元。
func TestUnitStoryboard(t *testing.T) {
	sb := &contract.Storyboard{Arc: &contract.Arc{}, Segments: []contract.Segment{segN("seg01", "a", 6), segN("seg02", "b", 6)}}
	units := []contract.RenderUnit{{
		ID: "para01", ParaIdx: 0, Part: 1, Parts: 1, Title: "甲", Narration: "ab", Dur: 12.7,
		Segs: []contract.UnitSeg{{ID: "seg01", Start: 0, End: 6.35, NarrDur: 6, Key: "seg01", Narration: "a", Brief: "seg01 画面提示"}},
	}}
	out := UnitStoryboard(sb, units)
	if out.Arc == nil || len(out.Segments) != 1 || out.Segments[0].ID != "para01" {
		t.Fatalf("单元分镜表不对: %+v", out)
	}
	if out.Segments[0].Narration != "ab" || !contains(out.Segments[0].VisualBrief, "seg01") {
		t.Fatalf("单元段内容不对: %+v", out.Segments[0])
	}
	if len(sb.Segments) != 2 {
		t.Fatal("原分镜表被改（应只读）")
	}
}

// MergedVoice：词级时间戳按格子偏移换算成单元绝对时间。
func TestMergedVoice(t *testing.T) {
	voices := map[string]voiceMeta{
		"seg01": {DurationS: 2, Words: []wordTiming{{Text: "一", Start: 0.1, End: 0.4}}},
		"seg02": {DurationS: 2, Words: []wordTiming{{Text: "二", Start: 0.2, End: 0.5}}},
	}
	u := contract.RenderUnit{ID: "para01", Dur: 4.7, Segs: []contract.UnitSeg{
		{ID: "seg01", Start: 0, End: 2.35, NarrDur: 2},
		{ID: "seg02", Start: 2.35, End: 4.7, NarrDur: 2},
	}}
	mv := MergedVoice(u, voices)
	if d := mv.DurationS - 4.35; d > 1e-9 || d < -1e-9 || len(mv.Words) != 2 {
		t.Fatalf("合并配音不对: %+v", mv)
	}
	if d := mv.Words[1].Start - 2.55; d > 1e-9 || d < -1e-9 { // 0.2 + 格子偏移 2.35
		t.Fatalf("词偏移不对: %+v", mv.Words[1])
	}
}

// BuildUnitsIfEligible 钉选纪律：老项目（有分镜制产物）钉 seg 永不迁移；
// 处女项目钉 para；已钉 para 但段落层没了 → 退回钉 seg。
func TestUnitPinning(t *testing.T) {
	mk := func(withSegArts bool) *pipeline.Project {
		p := pipeline.NewProject(t.TempDir(), "ptest")
		os.MkdirAll(p.Artifact("storyboards"), 0o755)
		os.WriteFile(p.Artifact("storyboards/draft.json"),
			[]byte(`{"paragraphs":[{"title":"甲","segs":["seg01","seg02"]}]}`), 0o644)
		os.WriteFile(p.Artifact("storyboards/storyboard.json"),
			[]byte(`{"segments":[{"id":"seg01","narration":"a","visual_brief":"x"},{"id":"seg02","narration":"b","visual_brief":"y"}]}`), 0o644)
		os.WriteFile(p.Artifact("audio_meta.json"),
			[]byte(`{"voices":[{"id":"seg01","duration_s":6,"words":[]},{"id":"seg02","duration_s":6,"words":[]}]}`), 0o644)
		os.WriteFile(p.Artifact("project.json"), []byte(`{"id":"ptest"}`), 0o644)
		if withSegArts {
			os.MkdirAll(p.Artifact("renders/segs"), 0o755)
			os.WriteFile(p.Artifact("renders/segs/seg01.mp4"), []byte("x"), 0o644)
		}
		return p
	}
	sb := &contract.Storyboard{Segments: []contract.Segment{segN("seg01", "a", 6), segN("seg02", "b", 6)}}
	voices := map[string]voiceMeta{"seg01": voiceD(6), "seg02": voiceD(6)}

	pOld := mk(true)
	if got := BuildUnitsIfEligible(pOld, sb, voices); got != nil {
		t.Fatal("老项目应走分镜制")
	}
	if pipeline.ReadProjectField(pOld, "unit") != "seg" {
		t.Fatalf("老项目应钉 seg，得到 %q", pipeline.ReadProjectField(pOld, "unit"))
	}

	pNew := mk(false)
	units := BuildUnitsIfEligible(pNew, sb, voices)
	if len(units) != 1 {
		t.Fatalf("处女项目应建单元: %d", len(units))
	}
	if pipeline.ReadProjectField(pNew, "unit") != "para" {
		t.Fatal("处女项目应钉 para")
	}
	if !pNew.UnitMode() {
		t.Fatal("UnitMode 应开")
	}
	wsb, err := pNew.WorkStoryboard()
	if err != nil || wsb.Segments[0].ID != "para01" {
		t.Fatalf("WorkStoryboard 应给单元表: %v", err)
	}
	if _, err := os.Stat(filepath.Join(pNew.Dir, "storyboards/units.json")); err != nil {
		t.Fatal("units.json 未落盘")
	}

	// 钉 para 后段落层被抹（重跑准备丢草稿）→ 退回 seg
	os.Remove(pNew.Artifact("storyboards/draft.json"))
	if got := BuildUnitsIfEligible(pNew, sb, voices); got != nil {
		t.Fatal("无段落层应退分镜制")
	}
	if pipeline.ReadProjectField(pNew, "unit") != "seg" {
		t.Fatal("段落层丢失应改钉 seg")
	}
}

func idsOf(us []contract.RenderUnit) []string {
	out := make([]string, len(us))
	for i, u := range us {
		out[i] = u.ID
	}
	return out
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
