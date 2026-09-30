package produce

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"webpage-video-agent/server/internal/pipeline"
)

func TestApplyContrastFixesRealOutput(t *testing.T) {
	out := `
Contrast
  ✗ #seg01-big3 2.63:1 (need 3:1, t=5.946s)
    Try rgb(166,76,8); source compositions/frames/seg01.html
  ✗ div > div:nth-of-type(5) > div > div > div:nth-of-type(2) > div:nth-of-type(3) > div:nth-of-type(1) 1.32:1 (need 3:1, t=53.511s)
    Try rgb(99,99,99); source compositions/frames/seg05.html
  ✗ #seg09-label6 1.38:1 (need 3:1, t=101.076s)
    Try rgb(96,96,96); source compositions/frames/seg09.html
  0 error(s), 7 warning(s), 0 info(s)
`
	dir := t.TempDir()
	p := pipeline.NewProject(filepath.Dir(dir), filepath.Base(dir))
	for _, f := range []string{"seg01", "seg05", "seg09"} {
		path := p.Artifact("compositions/frames/" + f + ".html")
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, []byte("<html><body><div>x</div></body></html>"), 0o644)
	}
	if n := applyContrastFixes(p, out); n != 3 {
		t.Fatalf("want 3 fixes, got %d", n)
	}
	b, _ := os.ReadFile(p.Artifact("compositions/frames/seg05.html"))
	s := string(b)
	if !strings.Contains(s, "div:nth-of-type(2) > div:nth-of-type(3) > div:nth-of-type(1){color:rgb(99,99,99)!important}") {
		t.Fatalf("seg05 patch missing: %s", s)
	}
	if !strings.Contains(s, "<style data-contrast-fix=\"1\">") || !strings.Contains(s, "</body>") {
		t.Fatalf("style block malformed: %s", s)
	}
	// 同选择器二次报 → 规则已在 HTML 里（内容去重），不再回填
	if n := applyContrastFixes(p, out); n != 0 {
		t.Fatalf("patched map should dedupe, got %d", n)
	}
	// 已有补丁块时：同色规则被内容去重跳过，只有换名的新选择器回填（1 处）
	out2 := strings.Replace(out, "#seg01-big3", "#seg01-new", 1)
	if n := applyContrastFixes(p, out2); n != 1 {
		t.Fatalf("want 1 fix on second run, got %d", n)
	}
	b2, _ := os.ReadFile(p.Artifact("compositions/frames/seg01.html"))
	if strings.Count(string(b2), "data-contrast-fix") != 1 || !strings.Contains(string(b2), "#seg01-new") {
		t.Fatalf("style block should be replaced with new selector: %s", b2)
	}
	// 非段帧路径不碰（新文件，避开上面几轮已写入的规则）
	out3 := strings.Replace(out, "compositions/frames/seg01.html", "index.html", 1)
	for _, f := range []string{"seg05", "seg09"} {
		os.WriteFile(p.Artifact("compositions/frames/"+f+".html"), []byte("<html><body><div>x</div></body></html>"), 0o644)
	}
	if n := applyContrastFixes(p, out3); n != 2 {
		t.Fatalf("want 2 (index.html skipped), got %d", n)
	}
}
