package api

// 删除项目 + 成片过期信号（video_stale）的功能冒烟（不打 LLM）：
// 删除 = 磁盘目录消失 + 项目列表不再出现；video_stale = 上游文件 mtime 晚于
// renders/main.mp4 → 对应位亮（前端页签黄灯的数据源）。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"webpage-video-agent/server/internal/agent"
)

func decodeBody(t *testing.T, w *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		t.Fatalf("响应解码失败：%v, body=%s", err, w.Body.String())
	}
}

func TestDeleteProject(t *testing.T) {
	s, dataDir := newTestServer(t)
	r := s.Router()
	mkProjectDir(t, dataDir, "p1")

	if w := doJSON(t, r, "DELETE", "/api/projects/p1", nil); w.Code != http.StatusOK {
		t.Fatalf("删除项目 = %d, body=%s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dataDir, "projects", "p1")); !os.IsNotExist(err) {
		t.Fatalf("项目目录应已删除：%v", err)
	}
	// 重复删除 → 404；列表不再出现
	if w := doJSON(t, r, "DELETE", "/api/projects/p1", nil); w.Code != http.StatusNotFound {
		t.Fatalf("重复删除 = %d, want 404", w.Code)
	}
	if w := doJSON(t, r, "GET", "/api/projects", nil); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	} else if got := w.Body.String(); got == "" || got == "null" {
		// 空列表必须序列化成 []（前端读 .length）
		t.Fatalf("空项目列表序列化异常：%s", got)
	}
}

func TestProjectViewVideoStale(t *testing.T) {
	s, dataDir := newTestServer(t)
	s.Agent = &agent.Agent{DataDir: dataDir} // projectView 走 Agent.State（Producer 留 nil = 不在制作）
	r := s.Router()
	mkProjectDir(t, dataDir, "p2")
	base := filepath.Join(dataDir, "projects", "p2")

	write := func(rel, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(base, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	setOld := func(rel string) {
		t.Helper()
		old := time.Now().Add(-2 * time.Hour)
		if err := os.Chtimes(filepath.Join(base, rel), old, old); err != nil {
			t.Fatal(err)
		}
	}
	write("input/article.txt", "正文")
	write("storyboards/storyboard.json", `{"segments":[]}`)
	write("style/style_brief.json", `{"name":"x"}`)
	if err := os.MkdirAll(filepath.Join(base, "renders"), 0o755); err != nil {
		t.Fatal(err)
	}
	write("renders/main.mp4", "x")
	// 成片最旧 → 三上游全 stale
	setOld("renders/main.mp4")

	w := doJSON(t, r, "GET", "/api/projects/p2", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("projectView = %d, body=%s", w.Code, w.Body.String())
	}
	var v struct {
		VideoStale struct {
			Manuscript bool `json:"manuscript"`
			Storyboard bool `json:"storyboard"`
			Style      bool `json:"style"`
		} `json:"video_stale"`
	}
	decodeBody(t, w, &v)
	if !v.VideoStale.Manuscript || !v.VideoStale.Storyboard || !v.VideoStale.Style {
		t.Fatalf("成片最旧应三上游全 stale，got %+v", v.VideoStale)
	}

	// 成片重渲（mtime 最新）→ 全灭
	write("renders/main.mp4", "x2")
	w = doJSON(t, r, "GET", "/api/projects/p2", nil)
	decodeBody(t, w, &v)
	if v.VideoStale.Manuscript || v.VideoStale.Storyboard || v.VideoStale.Style {
		t.Fatalf("成片最新应全不 stale，got %+v", v.VideoStale)
	}

	// 只改文稿 → 仅 manuscript 亮
	write("input/article.txt", "新正文")
	w = doJSON(t, r, "GET", "/api/projects/p2", nil)
	decodeBody(t, w, &v)
	if !v.VideoStale.Manuscript || v.VideoStale.Storyboard || v.VideoStale.Style {
		t.Fatalf("只改文稿应仅 manuscript stale，got %+v", v.VideoStale)
	}
}
