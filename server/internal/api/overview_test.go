package api

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"webpage-video-agent/server/internal/pipeline"
)

func TestScanManifest(t *testing.T) {
	const base = "2026-09-30T10:0"
	lines := []string{
		`{"event":"project.created","detail":"测试项目（16:9）","ts":"` + base + `0:00+08:00"}`,
		`{"event":"stage.done","detail":"manuscript","ts":"` + base + `0:01+08:00"}`,
		`{"event":"stage.await","detail":"storyboard: 先有文稿","ts":"` + base + `0:02+08:00"}`,
		`{"event":"llm.usage","detail":"storyboard 100 tokens","ts":"` + base + `0:03+08:00"}`,
		`{"event":"produce.start","detail":"","ts":"` + base + `0:10+08:00"}`,
		`{"event":"stage.start","detail":"tts","ts":"` + base + `0:10+08:00"}`,
		`{"event":"stage.finish","detail":"tts","ts":"` + base + `0:40+08:00"}`,
		`{"event":"stage.done","detail":"tts","ts":"` + base + `0:41+08:00"}`, // pipeline 重放秒写 done
		`{"event":"stage.start","detail":"check","ts":"` + base + `1:10+08:00"}`,
		`{"event":"stage.finish","detail":"check","ts":"` + base + `1:30+08:00"}`, // 第一轮失败
		`{"event":"stage.start","detail":"check","ts":"` + base + `1:40+08:00"}`,
		`{"event":"stage.finish","detail":"check","ts":"` + base + `2:20+08:00"}`, // 第二轮过
		`{"event":"produce.done","detail":"renders/main.mp4","ts":"` + base + `3:10+08:00"}`,
		`{"event":"rework.start","detail":"seg01: 改标题","ts":"` + base + `4:00+08:00"}`,
		`not json`,
		``,
	}
	stages, run, durations, tail := scanManifest([]byte(joinLines(lines)))
	// stages 只认终态四值：stage.start/finish 不进表
	if stages["manuscript"] != "done" || stages["storyboard"] != "await" || stages["tts"] != "done" {
		t.Fatalf("阶段终态不符: %v", stages)
	}
	if _, has := stages["check"]; has {
		t.Fatalf("start/finish 不该进 stages: %v", stages)
	}
	// rework.start 后无终态 → running
	if run == nil || run.Kind != "rework" || run.State != "running" {
		t.Fatalf("last_run 应为 rework/running: %+v", run)
	}
	// durations 恒指最近一轮：rework.start 后无 stage 事件 → 空（上一轮 produce 的 30s/90s 不算数）
	if len(durations) != 0 {
		t.Fatalf("新一轮无计时事件，durations 应空: %v", durations)
	}
	if len(tail) != 14 { // 坏行/空行剔除
		t.Fatalf("事件尾部应 14 条，得 %d", len(tail))
	}

	// 完结轮：耗时配对 + check 多轮累加 + 轮总时长
	lines2 := []string{
		`{"event":"produce.start","detail":"","ts":"2026-09-30T10:00:00+08:00"}`,
		`{"event":"stage.start","detail":"tts","ts":"2026-09-30T10:00:00+08:00"}`,
		`{"event":"stage.finish","detail":"tts","ts":"2026-09-30T10:00:30+08:00"}`,
		`{"event":"stage.start","detail":"check","ts":"2026-09-30T10:01:00+08:00"}`,
		`{"event":"stage.finish","detail":"check","ts":"2026-09-30T10:01:20+08:00"}`,
		`{"event":"stage.start","detail":"check","ts":"2026-09-30T10:01:30+08:00"}`,
		`{"event":"stage.finish","detail":"check","ts":"2026-09-30T10:02:10+08:00"}`,
		`{"event":"produce.done","detail":"renders/main.mp4","ts":"2026-09-30T10:03:00+08:00"}`,
	}
	_, run2, durations2, _ := scanManifest([]byte(joinLines(lines2)))
	if run2 == nil || run2.Kind != "produce" || run2.State != "done" || run2.Seconds != 180 {
		t.Fatalf("last_run 应为 produce/done/180s: %+v", run2)
	}
	if durations2["tts"] != 30 {
		t.Fatalf("tts 应 30s: %v", durations2)
	}
	if durations2["check"] != 60 { // 两轮修复累加：10:01:00→10:01:20 = 20s，10:01:30→10:02:10 = 40s
		t.Fatalf("check 两轮应共 60s: %v", durations2)
	}
}

// 进行中：未配对的 stage.start 按现在时刻给已耗时；run.seconds 同理。
func TestScanManifestRunning(t *testing.T) {
	now := time.Now()
	lines := []string{
		`{"event":"produce.start","detail":"","ts":"` + now.Add(-90*time.Second).Format(time.RFC3339) + `"}`,
		`{"event":"stage.start","detail":"render","ts":"` + now.Add(-40*time.Second).Format(time.RFC3339) + `"}`,
	}
	_, run, durations, _ := scanManifest([]byte(joinLines(lines)))
	if run == nil || run.State != "running" {
		t.Fatalf("应为 running: %+v", run)
	}
	if run.Seconds < 85 || run.Seconds > 95 {
		t.Fatalf("run 应 ~90s: %v", run.Seconds)
	}
	if d := durations["render"]; d < 35 || d > 45 {
		t.Fatalf("render 已耗时应 ~40s: %v", d)
	}
}

func joinLines(ls []string) string {
	s := ""
	for _, l := range ls {
		s += l + "\n"
	}
	return s
}

// tailManifest 是 activity 接口（事件流面板）的数据口：坏行剔除、尾部截断、ms 字段透传。
func TestTailManifest(t *testing.T) {
	lines := []string{
		`{"event":"produce.start","detail":"","ts":"2026-09-30T10:00:00+08:00","ms":1791000000000}`,
		`not json`,
		`{"event":"stage.start","detail":"tts","ts":"2026-09-30T10:00:05+08:00"}`, // 老条目：无 ms
		``,
	}
	got := tailManifest([]byte(joinLines(lines)), 500)
	if len(got) != 2 {
		t.Fatalf("坏行/空行应剔除，得 %d 条: %+v", len(got), got)
	}
	if got[0].MS != 1791000000000 {
		t.Fatalf("ms 字段应透传: %+v", got[0])
	}
	if got[1].MS != 0 || got[1].Event != "stage.start" {
		t.Fatalf("老条目 ms 缺省、事件保留: %+v", got[1])
	}
	// 尾部截断：只要最后 1 条
	got = tailManifest([]byte(joinLines(lines)), 1)
	if len(got) != 1 || got[0].Event != "stage.start" {
		t.Fatalf("应只留尾部 1 条 stage.start: %+v", got)
	}
	// 空 manifest（新建项目没开过工）：空切片而非 nil（JSON 必须 [] 不能 null）
	if got := tailManifest(nil, 500); got == nil || len(got) != 0 {
		t.Fatalf("空输入应为空切片: %+v", got)
	}
}

func TestScanArtifacts(t *testing.T) {
	dir := t.TempDir()
	p := pipeline.NewProject(dir, "p1")
	mk := func(rel string, content string) {
		abs := p.Artifact(rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("renders/main.mp4", "x")
	mk("renders/segs/seg01.mp4", "yy")
	mk("renders/segs/seg02.mp4", "yy")
	mk("audio/seg01.wav", "z")
	mk("reports/frameqa/f1.png", "png")

	got := map[string]artifactEntry{}
	for _, a := range scanArtifacts(p) {
		got[a.Path] = a
	}
	if e, ok := got["renders/main.mp4"]; !ok || e.Kind != "final_video" || e.Bytes != 1 {
		t.Fatalf("final_video 缺失或字段错: %+v", e)
	}
	if e, ok := got["renders/segs/seg01.mp4"]; !ok || e.Kind != "segment_video" {
		t.Fatalf("segment_video 缺失: %+v", e)
	}
	if e, ok := got["audio/seg01.wav"]; !ok || e.Kind != "audio" {
		t.Fatalf("audio 缺失: %+v", e)
	}
	// report 按子目录聚合：path 是目录，bytes 是总量
	if e, ok := got["reports/frameqa"]; !ok || e.Kind != "report" || e.Bytes != 3 {
		t.Fatalf("report 聚合不符: %+v", e)
	}
	// 不存在的产物不占位（目录不撒谎）
	if _, ok := got["storyboards/storyboard.json"]; ok {
		t.Fatal("不存在的 storyboard 不该出现")
	}
}
