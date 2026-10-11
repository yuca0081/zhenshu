package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"webpage-video-agent/server/internal/pipeline"
	"webpage-video-agent/server/internal/produce"
	"webpage-video-agent/server/internal/store"
)

// 门禁同权矩阵：start_production 在「成片过期/上次失败·中断」时必须放行整片重做
// （与舞台「按最新内容重做」按钮同权）；成片新鲜健康才挡回 rework。
func TestStartProductionGateRedoSymmetry(t *testing.T) {
	gate := findTool("start_production").gate
	ready := State{HasManuscript: true, HasStoryboard: true, HasStyle: true, StyleConfirmed: true}
	stale := ready
	stale.HasVideo = true
	stale.Stale.Manuscript = true
	failed := ready
	failed.HasVideo = true
	failed.LastEvent = produce.EventProduceError
	interrupted := ready
	interrupted.HasVideo = true
	interrupted.LastEvent = produce.EventProduceInterrupted
	fresh := ready
	fresh.HasVideo = true

	for _, c := range []struct {
		name  string
		s     State
		allOW bool // true = 应放行
	}{
		{"无成片首次制作", ready, true},
		{"成片过期", stale, true},
		{"上次失败", failed, true},
		{"上次中断", interrupted, true},
		{"成片新鲜健康", fresh, false},
	} {
		got := gate(c.s)
		if pass := got == ""; pass != c.allOW {
			t.Errorf("%s：放行=%v，期望 %v（拒绝语：%q）", c.name, pass, c.allOW, got)
		}
	}
	if got := gate(fresh); !strings.Contains(got, "rework") {
		t.Errorf("新鲜成片的拒绝语应指引 rework：%q", got)
	}
	if got := gate(State{}); !strings.Contains(got, "文稿") {
		t.Errorf("缺前置的拒绝语应点名缺项：%q", got)
	}
}

// export：旧版/失败前旧片必须带版本警告（地址照给，事实必须说）。
func TestExportWarnsOnOldVideo(t *testing.T) {
	dir := t.TempDir()
	p := pipeline.NewProject(dir, "p1")
	if err := os.MkdirAll(filepath.Join(p.Dir, "renders"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Artifact("renders/main.mp4"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Artifact("project.json"), []byte(`{"id":"p1","name":"t"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	p.Manifest(produce.EventProduceError, "boom")
	a := &Agent{DataDir: dir}
	got := findTool("export").run(context.Background(), a, p, "")
	if !strings.Contains(got, "失败前的旧片") || !strings.Contains(got, "下载地址") {
		t.Fatalf("export 应带旧片警告且保留地址：%s", got)
	}
}

// rework 边界提示：文稿/分镜过期时结果语必须点名「只重画不重配」。
func TestReworkStaleNote(t *testing.T) {
	s := State{HasVideo: true}
	s.Stale.Manuscript = true
	if got := reworkStaleNote(s); !strings.Contains(got, "只重画不重配") || !strings.Contains(got, "start_production") {
		t.Fatalf("stale 提示不符：%s", got)
	}
	fresh := State{HasVideo: true}
	if got := reworkStaleNote(fresh); got != "" {
		t.Fatalf("新鲜成片不该带提示：%s", got)
	}
}

// 记忆通道：agent 的失败通告（type=error）必须进历史；tool_call 仍排除。
type histStoreStub struct {
	store.Store // 只覆写 ListMsgs（history 唯一依赖）；其余方法不应被打到
	msgs        []store.Msg
}

func (s *histStoreStub) ListMsgs(projectID string, afterID int64, limit int) ([]store.Msg, error) {
	return s.msgs, nil
}

func TestHistoryKeepsAgentErrors(t *testing.T) {
	a := &Agent{Store: &histStoreStub{msgs: []store.Msg{
		{Role: "user", Type: "text", Content: "继续"},
		{Role: "agent", Type: "error", Content: "❌ 制作失败：seg18 重叠"},
		{Role: "agent", Type: "tool_call", Content: "read_project"},
		{Role: "agent", Type: "text", Content: "好的"},
	}}}
	hist := a.history("p1", 10)
	var joined strings.Builder
	for _, m := range hist {
		joined.WriteString(m.Content + "\n")
	}
	if !strings.Contains(joined.String(), "制作失败") {
		t.Fatalf("历史丢了失败通告：%s", joined.String())
	}
	if strings.Contains(joined.String(), "read_project") {
		t.Fatalf("tool_call 不该进历史：%s", joined.String())
	}
	if len(hist) != 3 {
		t.Fatalf("历史条数不符（want 3）：%d", len(hist))
	}
}
