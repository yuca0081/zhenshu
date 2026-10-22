package pipeline

// brief_evidence_test.go —— 设计阶段真迹附图（2026-10-22 样张与风格库两张皮修复）：
// BriefEvidenceImages 把简报证据帧压成多模态 dataURL；缺图跳过、全缺返回空
// （调用方回退纯文本，文案降级不谎称已附）。

import (
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeEvidencePNG(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 24, 12))
	for y := 0; y < 12; y++ {
		for x := 0; x < 24; x++ {
			img.Set(x, y, color.RGBA{R: 20, G: 200, B: 120, A: 255})
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

func writeTestBrief(p *Project, evidence []string) {
	brief := map[string]any{"entry_id": "st1", "entry_name": "测试条目", "evidence": evidence}
	b, _ := json.Marshal(brief)
	os.MkdirAll(filepath.Dir(p.Artifact("style/style_brief.json")), 0o755)
	os.WriteFile(p.Artifact("style/style_brief.json"), b, 0o644)
}

func TestBriefEvidenceImages(t *testing.T) {
	p := NewProject(t.TempDir(), "ptest")
	dir := t.TempDir()
	ok1 := filepath.Join(dir, "f0.png")
	ok2 := filepath.Join(dir, "f1.png")
	missing := filepath.Join(dir, "gone.png")
	writeEvidencePNG(t, ok1)
	writeEvidencePNG(t, ok2)

	// 无简报：空
	if imgs := BriefEvidenceImages(p, 6, 320); imgs != nil {
		t.Fatal("无简报应返回空")
	}
	writeTestBrief(p, []string{ok1, missing, ok2})
	imgs := BriefEvidenceImages(p, 6, 320)
	if len(imgs) != 2 {
		t.Fatalf("缺失帧应跳过、实存帧应附，got %d", len(imgs))
	}
	for _, u := range imgs {
		if !strings.HasPrefix(u, "data:image/jpeg;base64,") {
			t.Fatalf("应为 JPEG dataURL，got %.40s", u)
		}
	}
	// maxN 截断
	if imgs := BriefEvidenceImages(p, 1, 320); len(imgs) != 1 {
		t.Fatalf("maxN=1 应只附 1 张，got %d", len(imgs))
	}
	// 全部缺失：空（调用方降级纯文本）
	writeTestBrief(p, []string{missing})
	if imgs := BriefEvidenceImages(p, 6, 320); len(imgs) != 0 {
		t.Fatalf("全缺应返回空，got %d", len(imgs))
	}
}
