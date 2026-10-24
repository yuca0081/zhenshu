package prep

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"webpage-video-agent/server/internal/pipeline"
)

// writeManifestRow 追加一行 manifest 事件。
func writeManifestRow(t *testing.T, p *pipeline.Project, event string) {
	t.Helper()
	f, err := os.OpenFile(p.Artifact("manifest.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b, _ := json.Marshal(map[string]string{"event": event})
	f.Write(append(b, '\n'))
}

// TestReconcileMidflightDeath 中-4：进程死在中途的项目（只有 prep.start）被补
// interrupted 终态并通告；有终态的项目不动。
func TestReconcileMidflightDeath(t *testing.T) {
	dir := t.TempDir()
	r := NewRunner(dir, Deps{})

	dead := pipeline.NewProject(dir, "p_dead")
	if err := os.MkdirAll(dead.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeManifestRow(t, dead, "prep.start")
	writeManifestRow(t, dead, "prep.step")

	done := pipeline.NewProject(dir, "p_done")
	if err := os.MkdirAll(done.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeManifestRow(t, done, "prep.start")
	writeManifestRow(t, done, "prep.done")

	notified := map[string]string{}
	r.Reconcile(func(id, msg string) { notified[id] = msg })

	if _, ok := notified["p_dead"]; !ok {
		t.Fatal("死在中途的项目应收到通告")
	}
	// 补账后 manifest 有 prep.interrupted
	b, err := os.ReadFile(dead.Artifact("manifest.jsonl"))
	if err != nil || !contains0(string(b), "prep.interrupted") {
		t.Fatal("应补写 prep.interrupted 终态")
	}
	if _, ok := notified["p_done"]; ok {
		t.Fatal("有终态的项目不应被通告")
	}
	if b2, err := os.ReadFile(done.Artifact("manifest.jsonl")); err != nil || contains0(string(b2), "prep.interrupted") {
		t.Fatal("有终态的项目不应被补账")
	}
}

func contains0(s, sub string) bool { return strings.Contains(s, sub) }
