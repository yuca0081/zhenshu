package produce

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"webpage-video-agent/server/internal/events"
	"webpage-video-agent/server/internal/pipeline"
)

// 启动对账：manifest「有 start 无终态」= 进程死在制作中途，Reconcile 必须补写终态
// 并回调 OnDone（聊天通知）；已有终态的不能重复补写。
func TestReconcileInterruptedJobs(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	mk := func(id string, events ...string) {
		p := pipeline.NewProject(dir, id)
		if err := os.MkdirAll(p.Dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, ev := range events {
			p.Manifest(ev, "")
		}
	}
	mk("p-clean", "produce.start", "produce.done")        // 正常完结 → 不动
	mk("p-interrupted", "produce.done", "produce.start")  // 死在第二轮中途 → 补写
	mk("p-rework-interrupted", "rework.start")            // 重做中断 → 补写
	mk("p-cancelled", "rework.start", "rework.cancelled") // 已有终态 → 不动
	mk("p-nomanifest")                                    // 无 manifest → 跳过

	var notified []string
	r := &Runner{DataDir: dir, RootDir: dir, Hub: events.NewHub(),
		OnDone: func(id, status, detail string) { notified = append(notified, id+"/"+status) }}
	r.Reconcile()

	if len(notified) != 2 || notified[0] != "p-interrupted/failed" || notified[1] != "p-rework-interrupted/failed" {
		t.Fatalf("对账通知不符: %v", notified)
	}
	// 中断补写专用终态（不与真失败混用 *.error，agent/聊天话术说「中断」）
	wantTerm := map[string]string{
		"p-interrupted":        `"event":"produce.interrupted"`,
		"p-rework-interrupted": `"event":"rework.interrupted"`,
	}
	for id, want := range wantTerm {
		b, err := os.ReadFile(pipeline.NewProject(dir, id).Artifact("manifest.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), want) {
			t.Errorf("%s 应补写 %s 终态", id, want)
		}
	}
	for _, id := range []string{"p-clean", "p-cancelled"} {
		b, _ := os.ReadFile(pipeline.NewProject(dir, id).Artifact("manifest.jsonl"))
		if strings.Contains(string(b), ".error") || strings.Contains(string(b), ".interrupted") {
			t.Errorf("%s 已有终态，不该重复补写", id)
		}
	}
	// 幂等：补写过终态后再跑一轮，不再通知
	r.Reconcile()
	if len(notified) != 2 {
		t.Fatalf("重复对账又补写了: %v", notified)
	}
}

// LastTerminal 是磁盘终态审计的唯一读取点（agent 摘要 / api last_produce 共用），
// interrupted 必须被认作终态，否则中断项目在消费方眼里仍是"从未制作过"。
func TestLastTerminalReadsInterrupted(t *testing.T) {
	dir := t.TempDir()
	p := pipeline.NewProject(dir, "p1")
	if err := os.MkdirAll(p.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p.Manifest(EventProduceStart, "")
	p.Manifest(EventProduceInterrupted, "上次服务重启时制作中断")
	ev, detail, ts := LastTerminal(p)
	if ev != EventProduceInterrupted || detail != "上次服务重启时制作中断" || ts == "" {
		t.Fatalf("LastTerminal 应读到 interrupted 终态: %q %q %q", ev, detail, ts)
	}
}
