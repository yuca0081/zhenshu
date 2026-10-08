package api

// v3 风格链路的功能冒烟（不打 LLM）：解析草稿入库 → 媒体快照 → 选条目写简报
// （特色开关）→ 词表端点 → 草稿编辑端点。走真实 HTTP 路由 + 真实磁盘，
// 钉住「入库侧与消费侧的文件契约」（styles.json / style_media / style_brief.json）。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"webpage-video-agent/server/internal/events"
	"webpage-video-agent/server/internal/store"
	"webpage-video-agent/server/internal/stylelib"
	"webpage-video-agent/server/internal/styleref"
)

func newTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dataDir := t.TempDir()
	rootDir := t.TempDir()
	st, err := store.Open("")
	if err != nil {
		t.Fatal(err)
	}
	hub := events.NewHub()
	lib := stylelib.New(dataDir)
	s := &Server{
		DataDir: dataDir, RootDir: rootDir, Store: st, Hub: hub,
		Lib: lib, Refs: styleref.NewStore(dataDir, rootDir),
	}
	return s, dataDir
}

func doJSON(t *testing.T, r *gin.Engine, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func mkProjectDir(t *testing.T, dataDir, id string) {
	t.Helper()
	for _, d := range []string{"style", "input", "storyboards"} {
		if err := os.MkdirAll(filepath.Join(dataDir, "projects", id, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	meta, _ := json.Marshal(map[string]any{"id": id, "name": "测试片", "status": "storyboard"})
	if err := os.WriteFile(filepath.Join(dataDir, "projects", id, "project.json"), meta, 0o644); err != nil {
		t.Fatal(err)
	}
}

// 预置一份「解析已完成」的引用（ref.json + 帧文件 + features.json）。
func mkParsedRef(t *testing.T, dataDir string) string {
	t.Helper()
	refDir := filepath.Join(dataDir, "references", "ref1")
	if err := os.MkdirAll(refDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"video.mp4", "frame-0.8.png", "frame-2.5.png"} {
		if err := os.WriteFile(filepath.Join(refDir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	feats := fmt.Sprintf(`{"duration_s":30,"span_s":[0,30],"n_shots":4,"frames":[
		{"path":%q,"t":0.8},{"path":%q,"t":2.5}],"palette":[]}`,
		filepath.Join(refDir, "frame-0.8.png"), filepath.Join(refDir, "frame-2.5.png"))
	if err := os.WriteFile(filepath.Join(refDir, "features.json"), []byte(feats), 0o644); err != nil {
		t.Fatal(err)
	}
	return refDir
}

func TestStyleFlowIngestApplyBrief(t *testing.T) {
	s, dataDir := newTestServer(t)
	r := s.Router()

	// 词表先落两个词条（模拟打标阶段申请新词的结果）
	mkVocab := func(id, name string) {
		if _, err := s.Lib.VocabCreate(stylelib.VocabEntry{ID: id, Name: name, Brief: "测试词条"}); err != nil {
			t.Fatal(err)
		}
	}
	mkVocab("glitch", "故障风")
	mkVocab("iso", "等距 2.5D")

	// ① 解析草稿（single）落 ref.json → 确认入库
	refDir := mkParsedRef(t, dataDir)
	refJSON := `{"id":"ref1","name":"BV1xx.mp4","status":"done","kind":"single",
	  "style":{"name":"故障数据剧场","subject":"数据可视化的演示动画",
	    "tags":{"main":"glitch","aux":["iso"]},
	    "tag_evidence":[{"tag":"glitch","note":"紫青撕裂+扫描线","frames":["frame-0.8.png"]}],
	    "signatures":[
	      {"kind":"transition","name":"递接转场","mechanism":"上段主体推出画外，下段同位跟进","frames":["frame-2.5.png"]},
	      {"kind":"palette","name":"紫青撞色","mechanism":"双色对撞铺底","frames":["frame-0.8.png"]}]}}`
	if err := os.WriteFile(filepath.Join(refDir, "ref.json"), []byte(refJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	w := doJSON(t, r, "POST", "/api/references/ref1/confirm", nil)
	if w.Code != 200 {
		t.Fatalf("入库失败: %d %s", w.Code, w.Body.String())
	}
	st, ok := s.Lib.Get("vid-ref1")
	if !ok || !st.Published {
		t.Fatalf("条目未落库: %+v", st)
	}
	if st.Tags.Main != "glitch" || len(st.Tags.Aux) != 1 || st.Tags.Aux[0] != "iso" {
		t.Fatalf("标签错: %+v", st.Tags)
	}
	if len(st.Signatures) != 2 || st.Signatures[0].ID != "sig1" {
		t.Fatalf("特色应统一编号: %+v", st.Signatures)
	}
	// 媒体快照：帧与原片进 style_media/<id>/
	for _, f := range []string{"video.mp4", "frame-0.8.png", "frame-2.5.png", "cover.png"} {
		if _, err := os.Stat(filepath.Join(s.Lib.MediaDir("vid-ref1"), f)); err != nil {
			t.Fatalf("快照缺 %s: %v", f, err)
		}
	}

	// ② 建项目 → 选条目（关掉 sig1 transition）→ 简报落盘
	mkProjectDir(t, dataDir, "p1")
	// 预置一份旧样张产物，验证 applyStyle 会清掉（强参考换了，设计必须重发明）
	if err := os.WriteFile(filepath.Join(dataDir, "projects", "p1", "style", "style_samples.json"),
		[]byte(`{"direction":"旧","confirmed":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	w = doJSON(t, r, "POST", "/api/projects/p1/style/apply", map[string]any{
		"style_id": "vid-ref1", "disabled": []string{"sig1"},
	})
	if w.Code != 200 {
		t.Fatalf("选用失败: %d %s", w.Code, w.Body.String())
	}
	briefPath := filepath.Join(dataDir, "projects", "p1", "style", "style_brief.json")
	b, err := os.ReadFile(briefPath)
	if err != nil {
		t.Fatalf("简报未落盘: %v", err)
	}
	var brief struct {
		EntryID   string `json:"entry_id"`
		Tags      struct{ Main string; Aux []string } `json:"tags"`
		TagBriefs []struct{ ID, Name string } `json:"tag_briefs"`
		Signatures []struct{ ID string } `json:"signatures"`
		Evidence  []string `json:"evidence"`
	}
	if err := json.Unmarshal(b, &brief); err != nil {
		t.Fatal(err)
	}
	if brief.EntryID != "vid-ref1" || brief.Tags.Main != "glitch" {
		t.Fatalf("简报身份错: %s", b)
	}
	if len(brief.TagBriefs) != 2 || brief.TagBriefs[0].Name != "故障风" {
		t.Fatalf("TagBriefs 应带词条名: %s", b)
	}
	if len(brief.Signatures) != 1 || brief.Signatures[0].ID != "sig2" {
		t.Fatalf("关掉的 sig1 不该进简报: %s", b)
	}
	for _, e := range brief.Evidence {
		if !filepath.IsAbs(e) || !strings.Contains(e, "style_media") {
			t.Fatalf("证据帧应取自 style_media 绝对路径: %v", brief.Evidence)
		}
	}
	// 旧样张已清、project.json 记了 style_id
	if _, err := os.Stat(filepath.Join(dataDir, "projects", "p1", "style", "style_samples.json")); !os.IsNotExist(err) {
		t.Fatal("applyStyle 应清掉旧样张（设计必须重发明）")
	}
	pj, _ := os.ReadFile(filepath.Join(dataDir, "projects", "p1", "project.json"))
	if !strings.Contains(string(pj), "vid-ref1") {
		t.Fatalf("project.json 未记 style_id: %s", pj)
	}

	// ③ 词表端点
	w = doJSON(t, r, "GET", "/api/vocab", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "故障风") {
		t.Fatalf("词表端点异常: %d %s", w.Code, w.Body.String())
	}

	// ④ 草稿编辑端点（入库前增删改特色——新起一个未入库引用）
	mkParsedRef2(t, dataDir)
	w = doJSON(t, r, "PUT", "/api/references/ref2/draft", map[string]any{
		"seg": 0,
		"style": map[string]any{
			"name": "编辑后", "tags": map[string]any{"main": "glitch"},
			"signatures": []map[string]any{
				{"id": "sig1", "kind": "motif", "name": "保留的", "mechanism": "m"},
			},
		},
	})
	if w.Code != 200 {
		t.Fatalf("草稿编辑失败: %d %s", w.Code, w.Body.String())
	}
	ref2, err := s.Refs.Get("ref2")
	if err != nil {
		t.Fatal(err)
	}
	if ref2.Style == nil || len(ref2.Style.Signatures) != 1 || ref2.Style.Signatures[0].Name != "保留的" {
		t.Fatalf("草稿未更新: %+v", ref2.Style)
	}
	// 非法 kind 拒收
	w = doJSON(t, r, "PUT", "/api/references/ref2/draft", map[string]any{
		"seg": 0,
		"style": map[string]any{
			"name": "x", "tags": map[string]any{"main": "glitch"},
			"signatures": []map[string]any{{"kind": "vibe", "name": "n", "mechanism": "m"}},
		},
	})
	if w.Code != 400 {
		t.Fatalf("非法 kind 应 400: %d %s", w.Code, w.Body.String())
	}
}

func mkParsedRef2(t *testing.T, dataDir string) string {
	t.Helper()
	refDir := filepath.Join(dataDir, "references", "ref2")
	if err := os.MkdirAll(refDir, 0o755); err != nil {
		t.Fatal(err)
	}
	refJSON := `{"id":"ref2","name":"b.mp4","status":"done","kind":"single",
	  "style":{"name":"待编辑","tags":{"main":"glitch"},"signatures":[]}}`
	if err := os.WriteFile(filepath.Join(refDir, "ref.json"), []byte(refJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	return refDir
}
