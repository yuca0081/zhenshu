package produce

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"webpage-video-agent/server/internal/config"
	"webpage-video-agent/server/internal/events"
)

// 手工 A/B 验证版式注入（打真实 LLM + 渲染，平时跳过）：
//   LAYOUTAB_PROJ=p20260927-layoutab go test ./internal/produce -run TestManualLayoutAB -v -timeout 80m
func TestManualLayoutAB(t *testing.T) {
	projID := os.Getenv("LAYOUTAB_PROJ")
	if projID == "" {
		t.Skip("manual-only: set LAYOUTAB_PROJ")
	}
	dir, _ := os.Getwd()
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, ".env")); err == nil {
			break
		}
		dir = filepath.Dir(dir)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(cfg.DataDir)
	hub := events.NewHub()
	done := make(chan string, 1)
	r := &Runner{
		DataDir: cfg.DataDir,
		RootDir: root,
		Hub:     hub,
		OnDone:  func(projectID, status, detail string) { done <- status },
	}
	ch, _, cancel := hub.Subscribe(projID)
	defer cancel()
	go func() {
		for ev := range ch {
			t.Logf("[%s/%s] %s", ev.Type, ev.Stage, ev.Detail)
		}
	}()
	ok, err := r.Start(projID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("任务已在跑（不应发生）")
	}
	select {
	case status := <-done:
		if status != "video" {
			t.Fatalf("最终状态: %s", status)
		}
		t.Log("成片: data/projects/" + projID + "/renders/main.mp4")
	case <-time.After(75 * time.Minute):
		t.Fatal("超时")
	}
}
