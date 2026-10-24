package pipeline

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSegStaleFingerprint 段渲染判旧闸：无指纹判旧；输入没变不复渲；
// 帧/音频任一变即判旧——rework 只重渲改动段、改稿重配只重渲受影响段的根。
func TestSegStaleFingerprint(t *testing.T) {
	dir := t.TempDir()
	p := NewProject(dir, "p1")
	frame := p.Artifact("compositions/frames/seg01.html")
	wav := p.Artifact("audio/seg01.wav")
	proj := p.Artifact(".hf-seg/seg01.html")
	out := p.Artifact("renders/segs/seg01.mp4")
	for _, f := range []string{frame, wav, proj, out} {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("content-"+filepath.Base(filepath.Dir(f))), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// ① 无指纹旁车 → 判旧（首次渲染/中断补渲的依据）
	if !SegStale(p, "seg01", out) {
		t.Fatal("无指纹应判旧")
	}
	// ② 渲完落指纹旁车（与 RenderSeg 同口径手工落）：输入没变 → 不判旧
	sum, err := segSrcSum(p, "seg01")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out+".sum", []byte(sum), 0o644); err != nil {
		t.Fatal(err)
	}
	if SegStale(p, "seg01", out) {
		t.Fatal("输入未变不应判旧")
	}
	// ③ 音频变（改旁白重配）→ 判旧
	if err := os.WriteFile(wav, []byte("new-narration-audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !SegStale(p, "seg01", out) {
		t.Fatal("音频变了应判旧")
	}
	// ④ 帧变（rework 重画）→ 判旧
	sum, _ = segSrcSum(p, "seg01")
	if err := os.WriteFile(out+".sum", []byte(sum), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(frame, []byte("reworked-frame-html"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !SegStale(p, "seg01", out) {
		t.Fatal("帧变了应判旧")
	}
}
