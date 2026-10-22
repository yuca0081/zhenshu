package prep

// ahash_test.go —— 构图近重复检测（返工识别）的感知哈希。

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func writePNG(t *testing.T, path string, fg color.Color, split bool) {
	t.Helper()
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			if split && x >= 32 {
				img.Set(x, y, color.RGBA{R: 220, G: 220, B: 220, A: 255})
				continue
			}
			img.Set(x, y, fg)
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

func TestAHashReworkDetection(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.png")
	a2 := filepath.Join(dir, "a2.png") // 同构图：整底色相同（模拟字体改动——低频结构不变）
	b := filepath.Join(dir, "b.png")   // 不同构图：半幅换色
	writePNG(t, a, color.RGBA{R: 30, G: 30, B: 40, A: 255}, false)
	writePNG(t, a2, color.RGBA{R: 30, G: 30, B: 40, A: 255}, false)
	writePNG(t, b, color.RGBA{R: 30, G: 30, B: 40, A: 255}, true)

	ha, _ := aHash(a)
	ha2, _ := aHash(a2)
	hb, _ := aHash(b)
	if d := hashDistance(ha, ha2); d != 0 {
		t.Fatalf("同图距离应为 0，got %d", d)
	}
	if d := hashDistance(ha, hb); d < 10 {
		t.Fatalf("半幅换色距离应显著（≥10），got %d", d)
	}
	// 字体级改动（局部高频变化）距离必须落在近重复阈值内：
	// a2 上叠一小块亮斑模拟换字
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{R: 30, G: 30, B: 40, A: 255})
		}
	}
	// 小面积文字块（真实比例：一行标题，非大亮斑）
	for y := 28; y < 34; y++ {
		for x := 26; x < 38; x++ {
			img.Set(x, y, color.RGBA{R: 240, G: 240, B: 230, A: 255})
		}
	}
	f, _ := os.Create(filepath.Join(dir, "a3.png"))
	_ = png.Encode(f, img)
	_ = f.Close()
	h3, _ := aHash(filepath.Join(dir, "a3.png"))
	if d := hashDistance(ha, h3); d > 5 {
		t.Fatalf("局部文字改动应判近重复（≤5），got %d", d)
	}
}
