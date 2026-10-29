package api

// 项目解析路（两路解析入口二）的功能冒烟（不打 LLM）：parse 端点建暂存条目 +
// 无 key 报错落 ref.json + 幂等复用 + 确认入库（OriginProject=项目 ID）。
// 走真实 HTTP 路由 + 真实磁盘，钉住「项目解析侧与入库侧的文件契约」。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/stylelib"
	"webpage-video-agent/server/internal/styleref"
)

// 预置「已定稿设计系统」的项目：style_samples.json + 一张样张 poster。
func mkStyledProject(t *testing.T, dataDir, id string) {
	t.Helper()
	mkProjectDir(t, dataDir, id)
	styleDir := filepath.Join(dataDir, "projects", id, "style")
	if err := os.WriteFile(filepath.Join(styleDir, "style_samples.json"),
		[]byte(`{"direction":"纸面·马克笔","concept":"手绘叙事","confirmed":true,
		  "skins":[{"name":"主","slots":{"bg":"#f5f0e6","txt":"#2b2b33","accent":"#d9a441"}}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(styleDir, "sample-1.png"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 等解析 goroutine 落到终态（done|error）——解析是异步的，无 key 时秒级即 error。
func waitRefDone(t *testing.T, s *Server, id string) *styleref.Ref {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		ref, err := s.Refs.Get(id)
		if err == nil && (ref.Status == "done" || ref.Status == "error") {
			return ref
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("解析未在时限内落终态: %s", id)
	return nil
}

func TestProjectParseAndIngest(t *testing.T) {
	s, dataDir := newTestServer(t)
	r := s.Router()

	if _, err := llm.FromEnv(llm.RolePlan); err == nil {
		t.Skip("环境配了 LLM key——无 key 报错路径会真打模型，跳过")
	}

	// ① 没有设计系统的项目 → 400 预检拦截
	mkProjectDir(t, dataDir, "p0")
	w := doJSON(t, r, "POST", "/api/projects/p0/style/parse", nil)
	if w.Code != 400 {
		t.Fatalf("无设计系统应 400: %d %s", w.Code, w.Body.String())
	}

	// ② 正常项目 → 202 建条目 + 异步解析（无 key → error 落 ref.json）
	mkStyledProject(t, dataDir, "p1")
	w = doJSON(t, r, "POST", "/api/projects/p1/style/parse", nil)
	if w.Code != 202 {
		t.Fatalf("解析启动失败: %d %s", w.Code, w.Body.String())
	}
	var started struct {
		RefID string `json:"ref_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &started); err != nil || started.RefID == "" {
		t.Fatalf("响应缺 ref_id: %s", w.Body.String())
	}
	ref := waitRefDone(t, s, started.RefID)
	if ref.Status != "error" {
		t.Fatalf("无 key 应落 error: %+v", ref)
	}
	if ref.Source != styleref.SourceProject || ref.ProjectID != "p1" {
		t.Fatalf("来源字段错: %+v", ref)
	}
	if ref.Name != "测试片" { // project.json 快照里的陈列名
		t.Fatalf("条目名应取项目名: %q", ref.Name)
	}
	// 样张已拷为证据帧 + 合成 features.json（入库帧校验/封面的坐标系）
	if _, err := os.Stat(filepath.Join(dataDir, "references", ref.ID, "frame-01.png")); err != nil {
		t.Fatalf("样张未拷为证据帧: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "references", ref.ID, "features.json")); err != nil {
		t.Fatalf("合成 features.json 缺失: %v", err)
	}

	// ③ 幂等：重复点击复用既有条目，不建第二条
	w = doJSON(t, r, "POST", "/api/projects/p1/style/parse", nil)
	if w.Code != 200 {
		t.Fatalf("重复点击应 200 复用: %d %s", w.Code, w.Body.String())
	}
	var again struct {
		RefID    string `json:"ref_id"`
		Existing bool   `json:"existing"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &again); err != nil || again.RefID != started.RefID || !again.Existing {
		t.Fatalf("幂等复用失败: %s", w.Body.String())
	}

	// ④ 确认入库：手工把草稿推到 done（真实 LLM 打标链路由上传路测试覆盖形态）
	if _, err := s.Lib.VocabCreate(stylelib.VocabEntry{ID: "glitch", Name: "故障风", Brief: "测试词条"}); err != nil {
		t.Fatal(err)
	}
	draft := `{"id":"` + ref.ID + `","name":"测试片","status":"done","confirmed":false,
	  "source":"project","project_id":"p1",
	  "style":{"name":"冷晶仪象","subject":"测试题材","tags":{"main":"glitch"},
	    "tag_evidence":[{"tag":"glitch","note":"样张纸底","frames":["frame-01.png"]}],
	    "signatures":[{"kind":"palette","name":"米金对撞","mechanism":"双色对撞铺底"}]}}`
	if err := os.WriteFile(filepath.Join(dataDir, "references", ref.ID, "ref.json"), []byte(draft), 0o644); err != nil {
		t.Fatal(err)
	}
	w = doJSON(t, r, "POST", "/api/references/"+ref.ID+"/confirm", nil)
	if w.Code != 200 {
		t.Fatalf("入库失败: %d %s", w.Code, w.Body.String())
	}
	st, ok := s.Lib.Get("vid-" + ref.ID)
	if !ok || !st.Published {
		t.Fatalf("条目未落库: %+v", st)
	}
	if st.OriginProject != "p1" { // 项目解析路：出处=真实项目 ID（上传路是 video:<refID>）
		t.Fatalf("出处应记项目 ID: %q", st.OriginProject)
	}
	// 样张帧 + 封面快照进库媒体
	for _, f := range []string{"frame-01.png", "cover.png"} {
		if _, err := os.Stat(filepath.Join(s.Lib.MediaDir(st.ID), f)); err != nil {
			t.Fatalf("快照缺 %s: %v", f, err)
		}
	}
}

func stylerefVocabEntry() (string, string) { return "glitch", "glitch" }
