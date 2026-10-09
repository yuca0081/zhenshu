package produce

import (
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

func mustWritePNG(t *testing.T, dir, name string, img image.Image) string {
	t.Helper()
	p := filepath.Join(dir, name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	return p
}

func gradientImg(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{uint8(x % 256), uint8(y % 256), 90, 255})
		}
	}
	return img
}

func addClamp(v uint8, d int) uint8 {
	r := int(v) + d
	if r < 0 {
		r = 0
	}
	if r > 255 {
		r = 255
	}
	return uint8(r)
}

// 纹理相位抖动（逐像素 ±8 噪声，必然打穿逐像素占比闸）必须被感知判据放行——
// 2026-10-09 seg07 事故：合规纹理场景被逐像素等值判据误杀，修复轮三连不过。
func TestPerceptualToleratesTextureNoise(t *testing.T) {
	dir := t.TempDir()
	a := gradientImg(192, 108)
	b := image.NewRGBA(a.Bounds())
	rng := rand.New(rand.NewSource(1984))
	for i := 0; i < len(a.Pix); i += 4 {
		for c := 0; c < 3; c++ {
			b.Pix[i+c] = addClamp(a.Pix[i+c], rng.Intn(17)-8)
		}
		b.Pix[i+3] = 255
	}
	pa := mustWritePNG(t, dir, "a.png", a)
	pb := mustWritePNG(t, dir, "b.png", b)
	// 前提自检：噪声强度确实打穿逐像素判据（二级判据不是摆设）
	maxD, n, tot, err := pngDiffStats(pa, pb)
	if err != nil {
		t.Fatal(err)
	}
	if maxD <= replayMaxChannelDiff && float64(n) <= replayDiffRatioMax*float64(tot) {
		t.Fatalf("测试噪声太弱，没触发逐像素判据（max=%d n=%d tot=%d）", maxD, n, tot)
	}
	bm, bp, err := pngDiffPerceptual(pa, pb)
	if err != nil {
		t.Fatal(err)
	}
	if bm > perceptualMeanMax || bp > perceptualPixMax {
		t.Fatalf("纹理噪声被感知判据误杀：块均差=%.1f 块峰值=%.1f", bm, bp)
	}
}

// 结构级差异（亮块平移 30px = 元素位置变化，LLM 作弊的实际形态）必须被拦下。
func TestPerceptualCatchesStructureShift(t *testing.T) {
	dir := t.TempDir()
	a := gradientImg(192, 108)
	b := gradientImg(192, 108)
	bright := color.RGBA{240, 244, 250, 255}
	for y := 20; y < 60; y++ {
		for x := 20; x < 60; x++ {
			a.SetRGBA(x, y, bright)
		}
	}
	for y := 50; y < 90; y++ {
		for x := 50; x < 90; x++ {
			b.SetRGBA(x, y, bright)
		}
	}
	pa := mustWritePNG(t, dir, "a.png", a)
	pb := mustWritePNG(t, dir, "b.png", b)
	bm, bp, err := pngDiffPerceptual(pa, pb)
	if err != nil {
		t.Fatal(err)
	}
	if bm <= perceptualMeanMax && bp <= perceptualPixMax {
		t.Fatalf("结构平移被漏放：块均差=%.1f 块峰值=%.1f", bm, bp)
	}
}

// 尺寸不一致与 pngDiffStats 同口径：按最严重处理（放行不了也报不崩）。
func TestPerceptualSizeMismatchIsWorst(t *testing.T) {
	dir := t.TempDir()
	pa := mustWritePNG(t, dir, "a.png", gradientImg(64, 64))
	pb := mustWritePNG(t, dir, "b.png", gradientImg(32, 32))
	bm, bp, err := pngDiffPerceptual(pa, pb)
	if err != nil {
		t.Fatal(err)
	}
	if bm < 1e8 || bp < 1e8 {
		t.Fatalf("尺寸不一致应按最严重处理：got %f/%f", bm, bp)
	}
}

// 手动校准（打真实项目帧对，平时跳过）：Go 实现与调研期 Python 原型的块统计须一致。
//	REPLAY_PROJ=p20261009-164621 go test ./internal/produce -run TestManualPerceptualRealFrames -v
func TestManualPerceptualRealFrames(t *testing.T) {
	proj := os.Getenv("REPLAY_PROJ")
	if proj == "" {
		t.Skip("manual-only: set REPLAY_PROJ")
	}
	base := filepath.Join("..", "..", "..", "data", "projects", proj, "reports", "replay")
	pairs := [][2]string{
		{"round1/frame-18-at-75.51s.png", "round2/frame-18-at-75.51s.png"}, // GPU 双会话（调研基线：≈6.2/10.3）
		{"sw1/frame-00-at-75.51s.png", "sw2/frame-00-at-75.51s.png"},       // SwiftShader 双会话（≈0.9/1.7）
	}
	for _, p := range pairs {
		bm, bp, err := pngDiffPerceptual(filepath.Join(base, p[0]), filepath.Join(base, p[1]))
		if err != nil {
			t.Logf("%s: 不可用（%v）", p[0], err)
			continue
		}
		t.Logf("%s vs %s: 块均差=%.2f 块峰值=%.2f", p[0], p[1], bm, bp)
	}
}
