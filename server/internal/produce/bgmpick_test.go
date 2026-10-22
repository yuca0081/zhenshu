package produce

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

func mkChoices() []pipeline.BGMChoice {
	return []pipeline.BGMChoice{
		{Name: "calm", Mood: "温暖平静", Bpm: 72, Tempo: "slow", Energy: 2},
		{Name: "bright", Mood: "明快轻亮", Bpm: 88, Tempo: "mid", Energy: 3},
		{Name: "upbeat", Mood: "欢快大调", Bpm: 116, Tempo: "fast", Energy: 4},
		{Name: "epic", Mood: "史诗推进", Bpm: 60, Tempo: "mid", Energy: 5},
	}
}

func TestTempoForPace(t *testing.T) {
	cases := map[float64]string{
		0:   "", // 无实测数据：不设限
		-1:  "",
		3.0: "slow",
		3.6: "slow",
		4.5: "mid",
		5.5: "mid",
		5.6: "fast",
		7.0: "fast",
	}
	for cps, want := range cases {
		if got := tempoForPace(cps); got != want {
			t.Errorf("tempoForPace(%.1f)=%q want %q", cps, got, want)
		}
	}
}

func TestSpeechPace(t *testing.T) {
	sb := &contract.Storyboard{Segments: []contract.Segment{
		{ID: "seg01", Narration: "一二三四五六七八九十"}, // 10 字
		{ID: "seg02", Narration: "甲乙丙丁 戊己庚辛"},  // 8 字（空格不计）
		{ID: "seg03", Narration: "没有配音的段不计入"},
	}}
	voices := map[string]voiceMeta{
		"seg01": {DurationS: 4},
		"seg02": {DurationS: 2},
		// seg03 缺配音 → 整段剔除（分母不含）
	}
	cps, ok := speechPace(sb, voices)
	if !ok {
		t.Fatal("期望可计算")
	}
	// 18 字 / 6 秒 = 3.0
	if cps < 2.99 || cps > 3.01 {
		t.Errorf("cps=%v want 3.0", cps)
	}
	if _, ok := speechPace(&contract.Storyboard{}, nil); ok {
		t.Error("无 voices 应不可计算")
	}
}

func TestWantEnergy(t *testing.T) {
	if got := wantEnergy(nil); got != 3 {
		t.Errorf("无弧线 wantEnergy=%d want 3", got)
	}
	sb := &contract.Storyboard{Arc: &contract.Arc{Chapters: []contract.ArcChapter{
		{Tension: 1}, {Tension: 5},
	}}}
	if got := wantEnergy(sb); got != 3 {
		t.Errorf("张力均值 3 got %d", got)
	}
	sb.Arc.Chapters = []contract.ArcChapter{{Tension: 4}, {Tension: 5}, {Tension: 5}}
	if got := wantEnergy(sb); got != 5 {
		t.Errorf("张力均值 4.67 → 5 got %d", got)
	}
}

func TestTempoConflict(t *testing.T) {
	mk := func(tempo string) pipeline.BGMChoice { return pipeline.BGMChoice{Tempo: tempo} }
	if !tempoConflict(mk("slow"), "fast") || !tempoConflict(mk("fast"), "slow") {
		t.Error("快稿慢垫/慢稿快曲必须判冲突")
	}
	for _, c := range []struct {
		tempo, want string
	}{
		{"slow", "mid"}, {"mid", "fast"}, {"mid", "slow"}, {"", "fast"}, {"fast", ""},
	} {
		if tempoConflict(mk(c.tempo), c.want) {
			t.Errorf("tempo=%q want=%q 不应判冲突", c.tempo, c.want)
		}
	}
}

// off 哨兵不推翻（2026-10-21 巡检中危#3 回归）：用户 set_bgm off 后，pickBGM 必须
// 跳过自动选曲且不把 bgm 字段改掉；已有曲名照旧保留。两条都在守卫路径返回，
// 不碰曲库与 LLM，纯本地可测。
func TestPickBGMRespectsOffAndExisting(t *testing.T) {
	for _, tc := range []struct {
		name, bgm string
	}{
		{"off 哨兵不被自动选曲推翻", "off"},
		{"已有曲名保留", "calm"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := &pipeline.Project{ID: "t", Dir: dir, Ctx: context.Background()}
			if err := os.WriteFile(filepath.Join(dir, "project.json"),
				[]byte(`{"name":"t","bgm":"`+tc.bgm+`"}`), 0o644); err != nil {
				t.Fatal(err)
			}
			r := &Runner{Hub: events.NewHub()}
			r.pickBGM(context.Background(), p, &contract.Storyboard{}, nil)
			b, err := os.ReadFile(filepath.Join(dir, "project.json"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(b), `"bgm":"`+tc.bgm+`"`) {
				t.Fatalf("bgm 字段被改写: %s", b)
			}
		})
	}
}

// SetBGM off 写哨兵值而非删键（删键=空串=「没配乐」，自动选曲会填回来）。
func TestSetBGMSentinelOff(t *testing.T) {
	dir := t.TempDir()
	p := &pipeline.Project{ID: "t", Dir: dir, Ctx: context.Background()}
	if err := os.WriteFile(filepath.Join(dir, "project.json"),
		[]byte(`{"name":"t","bgm":"calm","bgm_reason":"旧选曲理由"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := pipeline.SetBGM(p, "off"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "project.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"bgm": "off"`) {
		t.Fatalf("off 应写哨兵值而非删键: %s", b)
	}
	if strings.Contains(string(b), "bgm_reason") {
		t.Fatalf("off 应清掉旧选曲理由: %s", b)
	}
}

func TestPickByRule(t *testing.T) {
	cs := mkChoices()
	if got := pickByRule(cs, "fast", 4).Name; got != "upbeat" {
		t.Errorf("快节奏高能量 → upbeat got %s", got)
	}
	if got := pickByRule(cs, "slow", 1).Name; got != "calm" {
		t.Errorf("慢节奏低能量 → calm got %s", got)
	}
	// 冲突重罚压过能量贴合：want fast 时 slow 高能量也不该选
	highESlow := []pipeline.BGMChoice{
		{Name: "sleepy", Tempo: "slow", Energy: 5},
		{Name: "mid1", Tempo: "mid", Energy: 2},
	}
	if got := pickByRule(highESlow, "fast", 5).Name; got != "mid1" {
		t.Errorf("冲突罚 +100 应弃 sleepy 选 mid1 got %s", got)
	}
	// 全未标注 → 库序第一首（稳定，不掷骰子）
	plain := []pipeline.BGMChoice{{Name: "a"}, {Name: "b"}}
	if got := pickByRule(plain, "", 3).Name; got != "a" {
		t.Errorf("全未标注取库序首个 got %s", got)
	}
}

func TestOverridePick(t *testing.T) {
	cs := mkChoices()
	// LLM 选了 calm（slow）而语速 fast：改判应落在 tempo 合规、能量贴近 2 的 bright
	got := overridePick(cs, "fast", 2)
	if got.Name != "bright" {
		t.Errorf("改判 want=fast anchor=2 → bright got %s", got.Name)
	}
	// 全员冲突时兜底不空转
	onlySlow := []pipeline.BGMChoice{{Name: "s1", Tempo: "slow", Energy: 2}}
	if got := overridePick(onlySlow, "fast", 2); got.Name == "" {
		t.Error("全员冲突也应返回候选")
	}
}

func TestBuildBGMPrompt(t *testing.T) {
	sb := &contract.Storyboard{
		Arc: &contract.Arc{Spine: "一个关于时间的故事", Chapters: []contract.ArcChapter{
			{Title: "开端", Mood: "平静", Tension: 2, Segs: []string{"seg01"}},
		}},
		Segments: []contract.Segment{{ID: "seg01", Narration: strings.Repeat("夜", 100)}},
	}
	voices := map[string]voiceMeta{"seg01": {DurationS: 10}}
	system, user := buildBGMPrompt(mkChoices(), sb, voices, 4.8, "mid")
	for _, want := range []string{"逐字", "calm", "时间的故事", "开端", "4.8 字/秒"} {
		if !strings.Contains(system+user, want) {
			t.Errorf("提示词缺 %q", want)
		}
	}
	// 节选封顶：100 字只取 60
	if strings.Count(user, "夜") > 61 {
		t.Error("节选未按 60 字截断")
	}
}

func TestStripSpaces(t *testing.T) {
	if got := stripSpaces("a b\tc\nd\r\ne"); got != "abcde" {
		t.Errorf("stripSpaces=%q", got)
	}
}
