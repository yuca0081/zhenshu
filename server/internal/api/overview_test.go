package api

import (
	"os"
	"path/filepath"
	"testing"

	"webpage-video-agent/server/internal/pipeline"
)

func TestScanManifest(t *testing.T) {
	lines := []string{
		`{"event":"project.created","detail":"测试项目（16:9）","ts":"t0"}`,
		`{"event":"stage.done","detail":"manuscript","ts":"t1"}`,
		`{"event":"stage.error","detail":"tts: 缺 key","ts":"t2"}`,
		`{"event":"stage.await","detail":"storyboard: 先有文稿","ts":"t3"}`,
		`{"event":"llm.usage","detail":"storyboard 100 tokens","ts":"t4"}`,
		`{"event":"produce.start","detail":"","ts":"t5"}`,
		`{"event":"stage.done","detail":"tts","ts":"t6"}`,
		`{"event":"produce.error","detail":"渲染失败","ts":"t7"}`,
		`{"event":"rework.start","detail":"seg01: 改标题","ts":"t8"}`,
		`not json`,
		``,
	}
	stages, run, tail := scanManifest([]byte(joinLines(lines)))
	// last-wins：tts 先 error（t2）后 done（t6），终态以最后一条为准
	if stages["manuscript"] != "done" || stages["tts"] != "done" || stages["storyboard"] != "await" {
		t.Fatalf("阶段终态不符: %v", stages)
	}
	// rework.start 后无终态 → running
	if run == nil || run.Kind != "rework" || run.State != "running" || run.TS != "t8" {
		t.Fatalf("last_run 应为 rework/running: %+v", run)
	}
	if len(tail) != 9 { // 坏行/空行剔除
		t.Fatalf("事件尾部应 9 条，得 %d", len(tail))
	}

	// 有终态：produce.start → produce.done → last_run = done
	stages, run, _ = scanManifest([]byte(joinLines([]string{
		`{"event":"produce.start","ts":"t0"}`,
		`{"event":"produce.done","detail":"renders/main.mp4","ts":"t1"}`,
	})))
	if run == nil || run.Kind != "produce" || run.State != "done" || run.Detail != "renders/main.mp4" {
		t.Fatalf("last_run 应为 produce/done: %+v", run)
	}
	if _, has := stages["produce"]; has { // produce.* 不是 stage 事件
		t.Fatalf("produce.* 不该进 stages: %v", stages)
	}
}

func joinLines(ls []string) string {
	s := ""
	for _, l := range ls {
		s += l + "\n"
	}
	return s
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
