package prep

// style_refimg_test.go —— ②风格工位的真迹接线（2026-10-22 样张与风格库两张皮修复）：
// get_style_detail 必须随观察附条目真迹帧；kickoff 必须告知用户已选定条目；
// 判卷对照物（refImages）随 chosen 条目取。

import (
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"webpage-video-agent/server/internal/stylelib"
)

// writeTestFrame 落一张真 PNG（纯色 16×16，ShrinkImageDataURL 只要求能解码）。
func writeTestFrame(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 30, B: 30, A: 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

// testLib 带真迹帧的本地库：条目 st1「呼啸骑士战」挂 2 张标签证据帧。
func testLib(t *testing.T) *stylelib.Library {
	t.Helper()
	dir := t.TempDir()
	libDir := filepath.Join(dir, "library")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	entry := []map[string]any{{
		"id": "st1", "name": "呼啸骑士战", "published": true,
		"tags": map[string]any{"main": "pixel"},
		"tag_evidence": []map[string]any{{
			"tag": "pixel", "note": "像素战斗界面",
			"frames": []string{"frame-00.png", "frame-01.png"},
		}},
	}}
	b, _ := json.Marshal(entry)
	if err := os.WriteFile(filepath.Join(libDir, "styles.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	writeTestFrame(t, filepath.Join(libDir, "style_media", "st1", "frame-00.png"))
	writeTestFrame(t, filepath.Join(libDir, "style_media", "st1", "frame-01.png"))
	return stylelib.New(dir)
}

// get_style_detail：文字档案之外必须给真迹细读（2026-10-28 换脑后走视觉副驾文字
// 画像；无 LLM_API_KEY 时降级为计数提示——chosen 跟踪 + refImages 判卷对照物不变）。
func TestGetStyleDetailAttachesEvidenceFrames(t *testing.T) {
	p := testProject(t)
	r := &styleRun{p: p, d: Deps{Lib: testLib(t)}}
	obs := r.getStyleDetailTool(&Loop{}).Run(context.Background(), `{"id":"st1"}`)
	if r.chosen != "st1" {
		t.Fatalf("chosen 应跟踪到查看的条目，got %q", r.chosen)
	}
	// 换脑后会话纯文本：不再附图，文本必须声明真迹去向（测试环境无 key → 降级文案）
	if len(obs.Images) != 0 {
		t.Fatalf("换脑后不该再附图，got %d 张", len(obs.Images))
	}
	if !strings.Contains(obs.Text, "原片真迹 2 张") {
		t.Fatalf("文案应声明真迹计数\n%s", obs.Text)
	}
	if !strings.Contains(obs.Text, "视觉副驾") {
		t.Fatalf("文案应说明画像通道/降级\n%s", obs.Text)
	}
	// 判卷对照物同一批货（缓存复用）——判卷链仍对照真图，闭环没断
	if refs := r.refImages(refJudgeMax); len(refs) != 2 {
		t.Fatalf("refImages 应返回 2 张对照真迹，got %d", len(refs))
	}
}

// 无媒体条目：文字档案照常给，附图归零但不炸（图库计数走 MediaNames 兜底文案）。
func TestGetStyleDetailWithoutMedia(t *testing.T) {
	dir := t.TempDir()
	libDir := filepath.Join(dir, "library")
	os.MkdirAll(libDir, 0o755)
	b, _ := json.Marshal([]map[string]any{{"id": "st2", "name": "无媒体条目", "published": true, "tags": map[string]any{"main": "pixel"}}})
	os.WriteFile(filepath.Join(libDir, "styles.json"), b, 0o644)
	p := testProject(t)
	r := &styleRun{p: p, d: Deps{Lib: stylelib.New(dir)}}
	obs := r.getStyleDetailTool(&Loop{}).Run(context.Background(), `{"id":"st2"}`)
	if len(obs.Images) != 0 {
		t.Fatalf("无媒体不该有附图，got %d", len(obs.Images))
	}
	if !strings.Contains(obs.Text, "暂无") {
		t.Fatalf("应如实报无图库\n%s", obs.Text)
	}
}

// kickoff：用户已选定条目（简报在）必须告知②——硬要求 + 先看真迹。
func TestStyleKickoffMentionsSelectedEntry(t *testing.T) {
	p := testProject(t)
	brief := map[string]any{"entry_id": "st1", "entry_name": "呼啸骑士战", "evidence": []string{}}
	b, _ := json.Marshal(brief)
	os.MkdirAll(filepath.Dir(p.Artifact("style/style_brief.json")), 0o755)
	os.WriteFile(p.Artifact("style/style_brief.json"), b, 0o644)
	k := styleKickoff(p)
	if !strings.Contains(k, "呼啸骑士战") || !strings.Contains(k, "st1") {
		t.Fatalf("kickoff 应告知用户选定条目\n%s", k)
	}
	if !strings.Contains(k, "get_style_detail") {
		t.Fatalf("kickoff 应指路 get_style_detail 看真迹\n%s", k)
	}
}

// projectStyleID：project.json 的 style_id 读取（简报被反馈重写作废后的恢复线索）。
func TestProjectStyleID(t *testing.T) {
	p := testProject(t)
	if got := projectStyleID(p); got != "" {
		t.Fatalf("无 style_id 应空，got %q", got)
	}
	os.WriteFile(p.Artifact("project.json"), []byte(`{"id":"p1","style_id":"st1"}`), 0o644)
	if got := projectStyleID(p); got != "st1" {
		t.Fatalf("应读到 st1，got %q", got)
	}
}

// chosen 未定/库未接线：refImages 必须返回 nil（判卷降级旧口径，不炸）。
func TestRefImagesDegradPaths(t *testing.T) {
	r := &styleRun{p: testProject(t)}
	if refs := r.refImages(refJudgeMax); refs != nil {
		t.Fatal("无 chosen 应 nil")
	}
	r.chosen = "st1"
	if refs := r.refImages(refJudgeMax); refs != nil {
		t.Fatal("库未接线应 nil")
	}
	// 条目无媒体：缓存记空，反复取不重查也不炸
	dir := t.TempDir()
	libDir := filepath.Join(dir, "library")
	os.MkdirAll(libDir, 0o755)
	b, _ := json.Marshal([]map[string]any{{"id": "st2", "name": "无媒体", "published": true, "tags": map[string]any{"main": "pixel"}}})
	os.WriteFile(filepath.Join(libDir, "styles.json"), b, 0o644)
	r.d.Lib = stylelib.New(dir)
	if refs := r.refImages(refJudgeMax); len(refs) != 0 {
		t.Fatalf("空结果应缓存为空 slice，got %d", len(refs))
	}
}
