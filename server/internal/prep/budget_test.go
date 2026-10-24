package prep

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"webpage-video-agent/server/internal/pipeline"
)

// TestStationBudgets 工位分时预算（中-2）：三站都有账、和不超过总闸 60 分钟。
func TestStationBudgets(t *testing.T) {
	total := time.Duration(0)
	for _, name := range []string{StationScript, StationStyle, StationStoryboard} {
		b := stationBudget(name)
		if b <= 0 || b > 45*time.Minute {
			t.Fatalf("%s 预算异常: %s", name, b)
		}
		total += b
	}
	if total > 60*time.Minute {
		t.Fatalf("工位预算和 %s 超过总闸 60 分钟", total)
	}
	if stationBudget("unknown") <= 0 {
		t.Fatal("未知工位应有兜底预算")
	}
}

// TestDoneStationsReport 预算收口的进度报告：按已落盘工件报人话清单。
func TestDoneStationsReport(t *testing.T) {
	dir := t.TempDir()
	p := pipeline.NewProject(dir, "p1")
	if got := doneStations(p); got != "无" {
		t.Fatalf("无工件应报「无」: %q", got)
	}
	for _, st := range []string{StationScript, StationStyle} {
		if err := writeJSONFile(artifactPath(p, st), map[string]string{}); err != nil {
			t.Fatal(err)
		}
	}
	got := doneStations(p)
	if !strings.Contains(got, "①文字稿") || !strings.Contains(got, "②风格包") || strings.Contains(got, "③分镜稿") {
		t.Fatalf("进度报告不符: %q", got)
	}
}

// TestStyleGrabGate 抢收门（中-2）：过线后 render_sample 拒起新活并指路 submit；
// 未过线不拦。
func TestStyleGrabGate(t *testing.T) {
	dir := t.TempDir()
	p := pipeline.NewProject(dir, "p1")
	if err := os.MkdirAll(p.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	r := &styleRun{p: p}
	snapLeft := styleSampleMax
	tool := r.renderSampleTool(&snapLeft, &Loop{})

	r.grabAt = time.Now().Add(-time.Minute) // 已过线
	obs := tool.Run(context.Background(), `{"scene_brief":"x","html":"<div></div>"}`)
	if !strings.Contains(obs.Text, "submit_style") || !strings.Contains(obs.Text, "时间预算") {
		t.Fatalf("过线应拒渲并指路 submit_style: %q", obs.Text)
	}

	r.grabAt = time.Now().Add(time.Hour) // 未过线：不拦（坏 JSON 走解码失败路径，不进真实渲染）
	obs = tool.Run(context.Background(), `{bad json`)
	if strings.Contains(obs.Text, "时间预算将尽") {
		t.Fatalf("未过线不应触发抢收拒渲: %q", obs.Text)
	}
}
