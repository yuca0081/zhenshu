package prep

// music_test.go —— 音乐工位的决策核单测：brief 白名单 lint 逐条 + 工位闭环
// （compose_track → submit_music，composeRun 注入桩免跑 python）。

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	openai "github.com/sashabaranov/go-openai"

	"webpage-video-agent/server/internal/pipeline"
)

func validBrief() pvBrief {
	return pvBrief{
		Name: "", Key: "Am", Bpm: 120, Bars: 40, // name 留空：测工具侧默认补名
		Sections: []pvSection{
			{Name: "intro", Bars: 4, Energy: 1},
			{Name: "verse", Bars: 8, Energy: 2},
			{Name: "build", Bars: 4, Energy: 3},
			{Name: "chorus", Bars: 8, Energy: 5},
			{Name: "break", Bars: 4, Energy: 3},
			{Name: "chorus2", Bars: 8, Energy: 5},
			{Name: "outro", Bars: 4, Energy: 2},
		},
	}
}

func TestValidatePVBrief(t *testing.T) {
	b0 := validBrief()
	if errs := validatePVBrief(&b0); len(errs) != 0 {
		t.Fatalf("合法 brief 不该报错：%v", errs)
	}
	cases := []struct {
		name  string
		mut   func(*pvBrief)
		wants string // 期望报错里包含的片段
	}{
		{"bpm 超带", func(b *pvBrief) { b.Bpm = 200 }, "bpm"},
		{"bars 超带", func(b *pvBrief) { b.Bars = 128 }, "bars"},
		{"时长出 PV 带", func(b *pvBrief) { b.Bars = 16 }, "不在 PV 带"},
		{"段落合计不铺满", func(b *pvBrief) { b.Sections[0].Bars = 8 }, "合计"},
		{"未知段落名", func(b *pvBrief) { b.Sections[1].Name = "drop" }, "词汇表"},
		{"缺副歌", func(b *pvBrief) {
			for i := range b.Sections {
				if strings.HasPrefix(b.Sections[i].Name, "chorus") {
					b.Sections[i].Name = "build"
				}
			}
		}, "缺副歌"},
		{"副歌能量不足", func(b *pvBrief) { b.Sections[3].Energy = 3; b.Sections[5].Energy = 3 }, "能量弧"},
		{"副歌主歌无对比", func(b *pvBrief) { b.Sections[3].Energy = 3; b.Sections[5].Energy = 3; b.Sections[1].Energy = 3 }, "对比"},
		{"未知调性无 prog", func(b *pvBrief) { b.Key = "F#" }, "没有内置和弦进行"},
		{"非法和弦", func(b *pvBrief) { b.Prog = []pvProgChord{{Sym: "Xm", Bars: 4}} }, "不认识"},
		{"进行超全曲", func(b *pvBrief) {
			b.Prog = []pvProgChord{{Sym: "Am", Bars: 8}, {Sym: "F", Bars: 8}, {Sym: "C", Bars: 8}, {Sym: "G", Bars: 8}, {Sym: "Dm", Bars: 8}, {Sym: "Em", Bars: 8}}
		}, "一轮"},
		{"坏文件名", func(b *pvBrief) { b.Name = "a/b" }, "文件名"},
	}
	for _, tc := range cases {
		b := validBrief()
		tc.mut(&b)
		errs := validatePVBrief(&b)
		if len(errs) == 0 {
			t.Errorf("%s：应报错没报", tc.name)
			continue
		}
		if !strings.Contains(strings.Join(errs, "\n"), tc.wants) {
			t.Errorf("%s：报错 %v 里找不到 %q", tc.name, errs, tc.wants)
		}
	}
	// 自带合法 prog 时未知调性放行
	b := validBrief()
	b.Key = "Bm"
	b.Prog = []pvProgChord{{Sym: "Bm", Bars: 2}, {Sym: "G", Bars: 2}, {Sym: "D", Bars: 2}, {Sym: "A", Bars: 2}}
	if errs := validatePVBrief(&b); len(errs) != 0 {
		t.Errorf("自带 prog 的未知调性应放行：%v", errs)
	}
}

// stubComposeRun 做曲桩：校验 brief 落盘 → 伪造地图+曲 → 返回自检 PASS 输出。
func stubComposeRun(t *testing.T, seenBrief *pvBrief) func(context.Context, Deps, *pipeline.Project, string) (string, int, error) {
	return func(_ context.Context, _ Deps, p *pipeline.Project, briefPath string) (string, int, error) {
		var br pvBrief
		b, err := os.ReadFile(briefPath)
		if err != nil {
			t.Fatalf("brief 未落盘: %v", err)
		}
		if err := json.Unmarshal(b, &br); err != nil {
			t.Fatalf("brief 不是合法 JSON: %v", err)
		}
		if seenBrief != nil {
			*seenBrief = br
		}
		mapJSON := `{"name":"pv_test","key":"Am","bpm":120,"beat_period":0.5,"bar_seconds":2.0,
			"duration":80.0,"score_duration":80.0,"beats_per_bar":4,"phase":0,"beats":[],"downbeats":[],
			"sections":[{"name":"intro","start_bar":0,"end_bar":4,"start":0,"end":8,"energy":1},
			{"name":"chorus","start_bar":16,"end_bar":24,"start":32,"end":48,"energy":5}],
			"energy_curve":[],"source":"test"}`
		if err := writeJSONFile(musicMapPath(p), json.RawMessage(mapJSON)); err != nil {
			t.Fatalf("地图桩写盘失败: %v", err)
		}
		if err := os.WriteFile(musicTrackPath(p), []byte("fake-mp3"), 0o644); err != nil {
			t.Fatalf("曲子桩写盘失败: %v", err)
		}
		return "自检环 PASS：BPM/时长带/能量弧/渲染时长 全对上", 0, nil
	}
}

func TestMusicStationComposeAndSubmit(t *testing.T) {
	p := testProject(t)
	var seen pvBrief
	old := composeRun
	composeRun = stubComposeRun(t, &seen)
	defer func() { composeRun = old }()

	bj, _ := json.Marshal(map[string]any{"brief": validBrief()})
	prov := &captureProv{scripted: []openai.ChatCompletionMessage{
		toolCall("compose_track", string(bj)),
		toolCall("submit_music", `{"summary":"测试曲：七段能量弧，副歌双爆发"}`),
	}}
	loop := &Loop{
		Prov: prov, System: "sys", Kickoff: "go",
		Tools: musicTools(p, Deps{}), Submit: "submit_music", MaxRounds: 4,
	}
	if err := loop.Run(context.Background()); err != nil {
		t.Fatalf("工位闭环失败: %v", err)
	}
	// 三件套齐 + brief 默认值补齐（name/key）
	for _, f := range []string{musicBriefPath(p), musicMapPath(p), musicTrackPath(p)} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("工件缺：%s", f)
		}
	}
	if seen.Name != "pv_p1" || seen.Key != "Am" {
		t.Errorf("brief 默认值未补齐：name=%q key=%q", seen.Name, seen.Key)
	}
	// 记账：manifest 有 music.saved；SSE 已在 stub 里无从断言，账行是硬凭据
	man, err := os.ReadFile(p.Artifact("manifest.jsonl"))
	if err != nil || !strings.Contains(string(man), "music.saved") {
		t.Errorf("manifest 缺 music.saved 账行：%v", err)
	}
}

func TestMusicComposeBudgetCap(t *testing.T) {
	p := testProject(t)
	old := composeRun
	composeRun = stubComposeRun(t, nil)
	defer func() { composeRun = old }()

	bj, _ := json.Marshal(map[string]any{"brief": validBrief()})
	prov := &captureProv{scripted: []openai.ChatCompletionMessage{
		toolCall("compose_track", string(bj)),
		toolCall("compose_track", string(bj)),
		toolCall("compose_track", string(bj)),
		toolCall("compose_track", string(bj)), // 第 4 次：应被护栏拦下
	}}
	loop := &Loop{
		Prov: prov, System: "sys", Kickoff: "go",
		Tools: musicTools(p, Deps{}), Submit: "submit_music", MaxRounds: 8,
	}
	if err := loop.Run(context.Background()); err == nil {
		t.Fatal("做曲封顶后循环不该当无事发生（应耗尽预算报错）")
	}
}

func TestMusicProjectModeAndInvalidate(t *testing.T) {
	p := testProject(t)
	if ProjectMode(p) != "script" {
		t.Fatal("无 mode 字段应回 script")
	}
	if err := os.WriteFile(p.Artifact("project.json"), []byte(`{"id":"p1","mode":"music"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if ProjectMode(p) != "music" {
		t.Fatal("mode=music 应识别")
	}
	// 三件套落盘 → InvalidateFrom(StationMusic) 全清 + 别误伤文稿工件
	if err := os.MkdirAll(p.Artifact("music"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(musicBriefPath(p), []byte(`{"bpm":120,"bars":40}`), 0o644); err != nil {
		t.Fatal(err)
	}
	composeRunOld := composeRun
	composeRun = stubComposeRun(t, nil)
	_, _, _ = composeRun(context.Background(), Deps{}, p, musicBriefPath(p))
	composeRun = composeRunOld
	if err := os.MkdirAll(p.Artifact("scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	scriptPath := p.Artifact("scripts/script.json")
	if err := os.WriteFile(scriptPath, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	InvalidateFrom(p, StationMusic)
	for _, f := range []string{musicBriefPath(p), musicMapPath(p), musicTrackPath(p)} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("music 失效应删 %s", f)
		}
	}
	if _, err := os.Stat(scriptPath); err != nil {
		t.Errorf("music 失效不该碰文稿工件: %v", err)
	}
}
