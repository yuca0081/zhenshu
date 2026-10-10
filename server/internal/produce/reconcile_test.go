package produce

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

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

// AutoResume 开启：整片制作中断 → 自动续跑（先补写 interrupted 终态保幂等，再走
// 与 start_production 同一条 Start 路）+ OnNotice 插话告知用户；重做中断不续跑
// （段级微调，旧画面仍在成片里，且 rework.start 只留 80 字指令）。空项目续跑必败，
// 重跑延迟缩成 0 让重试轮立刻烧完，最终仍以 failed 进 OnDone。
func TestReconcileAutoResume(t *testing.T) {
	oldDelays := autoRetryDelays
	autoRetryDelays = [maxAutoRetries]time.Duration{}
	defer func() { autoRetryDelays = oldDelays }()

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
	mk("p-produce-hang", "produce.start", "produce.done", "produce.start") // 第二轮死在中途
	mk("p-rework-hang", "rework.start")

	var notices, notified []string
	r := &Runner{DataDir: dir, RootDir: dir, Hub: events.NewHub(),
		AutoResume: true,
		OnDone:     func(id, status, detail string) { notified = append(notified, id+"/"+status) },
		OnNotice:   func(id, content string) { notices = append(notices, id) },
	}
	r.Reconcile()
	r.wg.Wait() // 等续跑 goroutine 烧完终态（空项目 TTS 即败，延迟为 0 不拖测试）

	// 整片中断自动续跑：插话一次；重做中断不续跑，不插话
	if len(notices) != 1 || notices[0] != "p-produce-hang" {
		t.Fatalf("自动续跑通知不符: %v", notices)
	}
	// 续跑项目空跑烧完 → failed 进 OnDone；重做中断走老路也 failed。
	// 续跑终态由 goroutine 异步上报，顺序不保证（重做是同步通知）。
	if len(notified) != 2 {
		t.Fatalf("终态通知数量不符: %v", notified)
	}
	sort.Strings(notified)
	if notified[0] != "p-produce-hang/failed" || notified[1] != "p-rework-hang/failed" {
		t.Fatalf("终态通知不符: %v", notified)
	}
	// 账本顺序：中断终态 → 续跑 start → retry 重跑记账 → 最终 error
	b, err := os.ReadFile(pipeline.NewProject(dir, "p-produce-hang").Artifact("manifest.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		`"event":"produce.interrupted"`, `"event":"produce.start"`,
		`"event":"produce.retry"`, `"event":"produce.error"`, "已自动重跑",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("续跑账本缺 %s", want)
		}
	}
	if strings.Contains(s, `"event":"rework.`) {
		t.Errorf("整片续跑不该混入重做事件")
	}
}
