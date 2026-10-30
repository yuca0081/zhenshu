package produce

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/pipeline"
)

// 一期镜头层的数据源是 Segment.Shot（shots.json 只作实验/导入）：导演块、
// 世界/角色表、以及「改一镜只过期该段」的指纹都要从这里长出来。
func TestShotPlanFromStoryboard(t *testing.T) {
	sb := &contract.Storyboard{
		World:      "纸剧场：米纸底、幕布、聚光灯、观众席",
		CastLegend: "剪辑师：炭黑三角、两条细腿、圆点眼；村民：小三角、细腿",
		Segments: []contract.Segment{
			{ID: "seg01", Key: "开场", Shot: &contract.ShotDirection{
				Size: "wide", Camera: "从观众席推上舞台", Stage: "幕布/木桌/屏幕",
				Cast:   []contract.ShotCast{{Who: "剪辑师", Pose: "站在桌后", Emotion: "得意"}},
				Action: "掀开幕布，村民围拢", Dialogue: "十块钱，十分钟", Transition: "直切",
			}},
			{ID: "seg02", Key: "无镜头"},
		},
	}
	sp := shotPlanFromStoryboard(sb)
	if sp == nil || len(sp.Shots) != 1 {
		t.Fatalf("镜头表构造不对: %+v", sp)
	}
	blk := sp.promptBlock("seg01")
	for _, want := range []string{"导演指令", "纸剧场", "剪辑师", "本镜", "从观众席推上舞台", "掀开幕布", "十块钱", "直切"} {
		if !strings.Contains(blk, want) {
			t.Errorf("导演块缺 %q:\n%s", want, blk)
		}
	}
	if got := sp.promptBlock("seg02"); got != "" {
		t.Errorf("无镜头段不应有导演块: %q", got)
	}
	if got := shotPlanFromStoryboard(&contract.Storyboard{}); got != nil {
		t.Errorf("无镜头分镜表应返回 nil: %+v", got)
	}
}

// 双源：新项目以 Segment.Shot 为准，shots.json 只补世界/角色表（导入/实验兜底）。
func TestLoadShotPlanPrefersSegment(t *testing.T) {
	p := pipeline.NewProject(t.TempDir(), "p-shot")
	if err := os.MkdirAll(filepath.Join(p.Dir, "storyboards"), 0o755); err != nil {
		t.Fatal(err)
	}
	sb := &contract.Storyboard{Segments: []contract.Segment{{
		ID: "seg01", Key: "新镜", Shot: &contract.ShotDirection{
			Size: "close", Camera: "段内机位", Stage: "桌", Action: "指",
			Cast: []contract.ShotCast{{Who: "甲", Pose: "站"}},
		},
	}}}
	if b, err := json.Marshal(sb); err != nil {
		t.Fatal(err)
	} else if err := os.WriteFile(p.Artifact("storyboards/storyboard.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	file := ShotPlan{
		World: "文件世界", CastLegend: "文件角色",
		Shots: []ShotBrief{{Seg: "seg01", Title: "旧镜", Size: "wide", Camera: "文件机位", Action: "旧戏"}},
	}
	if b, err := json.Marshal(file); err != nil {
		t.Fatal(err)
	} else if err := os.WriteFile(p.Artifact("storyboards/shots.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	sp := loadShotPlan(p)
	if sp == nil || len(sp.Shots) != 1 {
		t.Fatalf("镜头表读取失败: %+v", sp)
	}
	if sp.Shots[0].Camera != "段内机位" {
		t.Errorf("应以 Segment.Shot 为准，得到 %q", sp.Shots[0].Camera)
	}
	if sp.World != "文件世界" || sp.CastLegend != "文件角色" {
		t.Errorf("世界/角色表应从 shots.json 补齐: %+v", sp)
	}
}

func TestSceneFingerprintIncludesShot(t *testing.T) {
	seg := contract.Segment{ID: "seg01", Key: "k", Narration: "n", VisualBrief: "v", DurationHint: 5}
	base := sceneFingerprint(seg)
	if got := sceneFingerprint(seg); got != base {
		t.Fatalf("同内容指纹不稳定: %s != %s", got, base)
	}
	seg.Shot = &contract.ShotDirection{
		Size: "medium", Camera: "横摇", Stage: "木桌", Action: "点脚尖",
		Cast: []contract.ShotCast{{Who: "AI", Pose: "俯身"}},
	}
	withShot := sceneFingerprint(seg)
	if withShot == base {
		t.Error("加了镜头字段指纹没变——改镜不会过期该段场景")
	}
	seg.Shot.Camera = "推近"
	if got := sceneFingerprint(seg); got == withShot {
		t.Error("改了机位指纹没变——改镜不会过期该段场景")
	}
}
