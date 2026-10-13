package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"webpage-video-agent/server/internal/pipeline"
	"webpage-video-agent/server/internal/store"
)

// recStore 记账式 store：create_project 会落目录行，其余方法走内嵌 nil 接口
// （调用即 panic——正好钉住「工具不该碰没实现的方法」）。
type recStore struct {
	store.Store
	storeCalls []string
}

func (s *recStore) UpdateStatus(id, status string) error {
	s.storeCalls = append(s.storeCalls, "status:"+id+":"+status)
	return nil
}

func (s *recStore) CreateProject(id, name, userID string) error {
	s.storeCalls = append(s.storeCalls, "create:"+id+":"+name+":"+userID)
	return nil
}

func writeStoryboard(t *testing.T, p *pipeline.Project) {
	t.Helper()
	sb := `{"timing_basis":"narration","segments":[` +
		`{"id":"seg01","idx":1,"key":"开场","narration":"第一段旁白。","visual_brief":"画面A","duration_hint":10},` +
		`{"id":"seg02","idx":2,"key":"展开","narration":"第二段旁白。","visual_brief":"画面B","duration_hint":8}]}`
	if err := os.WriteFile(p.Artifact("storyboards/storyboard.json"), []byte(sb), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newToolProject(t *testing.T) (*Agent, *pipeline.Project) {
	t.Helper()
	dataDir := t.TempDir()
	a := &Agent{DataDir: dataDir, Store: &recStore{}}
	p := pipeline.NewProject(dataDir, "t1")
	for _, dir := range []string{"input", "manuscripts", "storyboards", "style", "llm", "audio", "renders"} {
		if err := os.MkdirAll(p.Artifact(dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(p.Artifact("project.json"), []byte(`{"name":"测试片"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return a, p
}

// edit_segment：旁白/段名/时长的精准编辑——落盘、备份清理、重配提示、坏参拒绝。
func TestEditSegmentTool(t *testing.T) {
	a, p := newToolProject(t)
	writeStoryboard(t, p)
	tl := findTool("edit_segment")
	if tl == nil {
		t.Fatal("edit_segment 工具未注册")
	}
	if got := tl.gate(State{Producing: true}); got == "" {
		t.Fatal("制作中应被门禁拒绝")
	}
	if got := tl.gate(State{}); got == "" {
		t.Fatal("无分镜应被门禁拒绝")
	}

	// 空改：拒绝
	if out := tl.run(context.Background(), a, p, `{"segment":"1"}`); !strings.Contains(out, "至少给一个") {
		t.Fatalf("空改应被拒: %q", out)
	}
	// 空旁白：拒绝（TTS 合不出声）
	if out := tl.run(context.Background(), a, p, `{"segment":"1","narration":"  "}`); !strings.Contains(out, "不能为空") {
		t.Fatalf("空旁白应被拒: %q", out)
	}
	// 非法时长：拒绝且不留 .bak
	if out := tl.run(context.Background(), a, p, `{"segment":"1","duration_hint":0}`); !strings.Contains(out, "600") {
		t.Fatalf("非法时长应被拒: %q", out)
	}
	if _, err := os.Stat(p.Artifact("storyboards/storyboard.json.bak")); !os.IsNotExist(err) {
		t.Fatal("拒绝路径不应留 .bak")
	}
	// 不存在的段：拒绝
	if out := tl.run(context.Background(), a, p, `{"segment":"9","key":"x"}`); !strings.Contains(out, "不在分镜里") {
		t.Fatalf("未知段应被拒: %q", out)
	}

	// 正常改旁白 + 段名
	out := tl.run(context.Background(), a, p, `{"segment":"seg02","narration":"改后的第二段。","key":"新段名"}`)
	if !strings.Contains(out, "段2") || !strings.Contains(out, "重配") {
		t.Fatalf("改旁白应报重配提示: %q", out)
	}
	var sb struct {
		Segments []struct {
			ID        string `json:"id"`
			Key       string `json:"key"`
			Narration string `json:"narration"`
		} `json:"segments"`
	}
	b, err := os.ReadFile(p.Artifact("storyboards/storyboard.json"))
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(b, &sb) != nil {
		t.Fatalf("分镜写坏: %s", b)
	}
	if sb.Segments[1].Narration != "改后的第二段。" || sb.Segments[1].Key != "新段名" {
		t.Fatalf("修改未落盘: %s", b)
	}
	if sb.Segments[0].Narration != "第一段旁白。" {
		t.Fatalf("其他段不应被动: %s", b)
	}
	if _, err := os.Stat(p.Artifact("storyboards/storyboard.json.bak")); !os.IsNotExist(err) {
		t.Fatal("成功后 .bak 应清理")
	}

	// 只改时长：提示「重新制作」而不是「重配」
	out = tl.run(context.Background(), a, p, `{"segment":"2","duration_hint":12.5}`)
	if !strings.Contains(out, "重新制作") || strings.Contains(out, "重配") {
		t.Fatalf("纯时长改动不应报重配: %q", out)
	}
}

// read_manuscript：全文直读；缺文稿门禁拒绝。
func TestReadManuscriptTool(t *testing.T) {
	a, p := newToolProject(t)
	tl := findTool("read_manuscript")
	if tl == nil {
		t.Fatal("read_manuscript 工具未注册")
	}
	if got := tl.gate(State{}); got == "" {
		t.Fatal("无文稿应被门禁拒绝")
	}
	if err := os.WriteFile(p.Artifact("input/article.txt"), []byte("这是全文内容。"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := tl.run(context.Background(), a, p, `{}`)
	if !strings.Contains(out, "这是全文内容。") || !strings.Contains(out, "全文（7 字）") {
		t.Fatalf("应返回全文与字数: %q", out)
	}
}

// State 设置注入：音色别名翻译、BGM/音效/双语/风格 id 都进摘要。
func TestStateSettingsSummary(t *testing.T) {
	a, p := newToolProject(t)
	meta := `{"name":"测试片","voice":"longanlingxin","bgm":"ambient-01","transition_sfx":true,"bi_subtitle":true,"style_id":"sty_abc"}`
	if err := os.WriteFile(p.Artifact("project.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := a.State(p.ID).summary()
	for _, want := range []string{"龙安灵心", "BGM ambient-01", "转场音效 开", "双语字幕 开", "sty_abc"} {
		if !strings.Contains(sum, want) {
			t.Fatalf("摘要缺设置项 %q:\n%s", want, sum)
		}
	}
	// 空设置走默认措辞
	a2, p2 := newToolProject(t)
	sum2 := a2.State(p2.ID).summary()
	if !strings.Contains(sum2, "音色 引擎默认") || !strings.Contains(sum2, "BGM 关") {
		t.Fatalf("空设置摘要不对:\n%s", sum2)
	}
}

// cancel_production：没跑时如实说、接线时真取消、未接线也交代。
func TestCancelProductionTool(t *testing.T) {
	a, p := newToolProject(t)
	tl := findTool("cancel_production")
	if tl == nil {
		t.Fatal("cancel_production 工具未注册")
	}
	if out := tl.run(context.Background(), a, p, `{}`); !strings.Contains(out, "未接线") {
		t.Fatalf("未接线应如实说: %q", out)
	}
	a.Producer = fakeProducer{cancelOK: false}
	if out := tl.run(context.Background(), a, p, `{}`); !strings.Contains(out, "没有正在进行的") {
		t.Fatalf("无任务时应如实说: %q", out)
	}
	a.Producer = fakeProducer{cancelOK: true}
	if out := tl.run(context.Background(), a, p, `{}`); !strings.Contains(out, "已请求取消") {
		t.Fatalf("取消应确认: %q", out)
	}
}

type fakeProducer struct{ cancelOK bool }

func (f fakeProducer) Start(string) (bool, error)                    { return true, nil }
func (f fakeProducer) Rework(string, []string, string) (bool, error) { return true, nil }
func (f fakeProducer) Cancel(string) bool                            { return f.cancelOK }
func (f fakeProducer) IsRunning(string) bool                         { return false }

// read_check：manifest 里的 frameqa 逐段结果与过程事件都要读得出来。
func TestReadCheckTool(t *testing.T) {
	a, p := newToolProject(t)
	tl := findTool("read_check")
	if tl == nil {
		t.Fatal("read_check 工具未注册")
	}
	if out := tl.run(context.Background(), a, p, `{}`); !strings.Contains(out, "没有制作记录") {
		t.Fatalf("无 manifest 应如实说: %q", out)
	}
	manifest := strings.Join([]string{
		`{"ts":"2026-10-13T09:00:00+08:00","event":"produce.start","detail":"整片"}`,
		`{"ts":"2026-10-13T09:01:00+08:00","event":"frameqa.seg","detail":"seg01: pass"}`,
		`{"ts":"2026-10-13T09:02:00+08:00","event":"frameqa.seg","detail":"seg02: fail（2 个问题）"}`,
		`{"ts":"2026-10-13T09:02:10+08:00","event":"frameqa.sig","detail":"seg02 sig1: fail 文字溢出画面"}`,
		`{"ts":"2026-10-13T09:03:00+08:00","event":"produce.error","detail":"check 失败"}`,
	}, "\n")
	if err := os.WriteFile(p.Artifact("manifest.jsonl"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	out := tl.run(context.Background(), a, p, `{}`)
	for _, want := range []string{"seg01: pass", "seg02: fail", "文字溢出画面", "produce.error"} {
		if !strings.Contains(out, want) {
			t.Fatalf("报告缺 %q:\n%s", want, out)
		}
	}
}

// create_project：目录骨架 + project.json + 目录行落库，归属带上下文用户。
func TestCreateProjectTool(t *testing.T) {
	a, p := newToolProject(t)
	tl := findTool("create_project")
	if tl == nil {
		t.Fatal("create_project 工具未注册")
	}
	// 无主题无稿：拒绝
	if out := tl.run(context.Background(), a, p, `{}`); !strings.Contains(out, "主题必填") {
		t.Fatalf("缺主题应被拒: %q", out)
	}
	ctx := WithUser(context.Background(), "u1", "user")
	out := tl.run(ctx, a, p, `{"name":"新片","topic":"蚊子为什么嗡嗡叫"}`)
	if !strings.Contains(out, "项目已建") {
		t.Fatalf("应成功: %q", out)
	}
	entries, err := os.ReadDir(filepath.Join(a.DataDir, "projects"))
	if err != nil || len(entries) != 2 {
		t.Fatalf("应多出一个项目目录: %v %d", err, len(entries))
	}
	var newID string
	for _, e := range entries {
		if e.Name() != "t1" {
			newID = e.Name()
		}
	}
	b, err := os.ReadFile(filepath.Join(a.DataDir, "projects", newID, "project.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"user_id": "u1"`) || !strings.Contains(string(b), "新片") {
		t.Fatalf("归属/名称未落 project.json: %s", b)
	}
	rs := a.Store.(*recStore)
	if len(rs.storeCalls) == 0 || !strings.Contains(rs.storeCalls[0], "create:"+newID+":新片:u1") {
		t.Fatalf("目录行未落库: %v", rs.storeCalls)
	}
}

// list_projects：归属过滤——非 admin 只看自己的。
func TestListProjectsTool(t *testing.T) {
	a, p := newToolProject(t)
	tl := findTool("list_projects")
	if tl == nil {
		t.Fatal("list_projects 工具未注册")
	}
	for id, uid := range map[string]string{"t1": "u1", "t2": "u2"} {
		dir := filepath.Join(a.DataDir, "projects", id)
		if id != "t1" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		meta, _ := json.Marshal(map[string]string{"name": "项目" + id, "status": "created", "user_id": uid})
		if err := os.WriteFile(filepath.Join(dir, "project.json"), meta, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := tl.run(WithUser(context.Background(), "u1", "user"), a, p, `{}`)
	if strings.Contains(out, "t2") {
		t.Fatalf("非 admin 不应看到别人项目:\n%s", out)
	}
	if !strings.Contains(out, "t1") {
		t.Fatalf("应看到自己项目:\n%s", out)
	}
	// admin / 开放模式（无用户上下文）看全部
	out = tl.run(context.Background(), a, p, `{}`)
	if !strings.Contains(out, "t2") {
		t.Fatalf("无用户上下文应看全部:\n%s", out)
	}
}
