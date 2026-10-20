package produce

import (
	"image"
	"image/color"
	"strings"
	"testing"
)

// seamLumaVerdict 首/末帧亮度闸（D5 接缝契约）：黑场开场/淡出收黑两条都抓；
// 暗色设计（中段 <40）只看相对塌陷，不拿绝对黑线误杀正常暗画面。
func TestSeamLumaVerdict(t *testing.T) {
	cases := []struct {
		name    string
		mid     float64
		first   []float64
		last    []float64
		wantN   int
		wantSub []string
	}{
		{"正常段", 120, []float64{118, 119}, []float64{117, 118}, 0, nil},
		{"黑场开场·快启动", 120, []float64{5, 90}, []float64{118, 119}, 1, []string{"首帧黑场开场"}},
		{"黑场开场·慢启动", 120, []float64{110, 40}, []float64{118, 119}, 1, []string{"首帧黑场开场"}},
		{"淡出收黑", 120, []float64{118, 119}, []float64{110, 8}, 1, []string{"末帧淡出收黑"}},
		{"首尾都黑", 120, []float64{3, 4}, []float64{2, 3}, 2, []string{"首帧黑场开场", "末帧淡出收黑"}},
		{"暗色设计不误杀", 30, []float64{26, 27}, []float64{25, 26}, 0, nil},
		{"暗色设计的塌陷照抓", 30, []float64{2, 20}, []float64{25, 26}, 1, []string{"首帧黑场开场"}},
	}
	for _, tc := range cases {
		got := seamLumaVerdict(tc.mid, tc.first, tc.last)
		if len(got) != tc.wantN {
			t.Fatalf("%s: 应 %d 条违规，实得 %v", tc.name, tc.wantN, got)
		}
		for i, sub := range tc.wantSub {
			if !strings.Contains(got[i], sub) {
				t.Fatalf("%s: 违规[%d] 应含 %q，实得 %q", tc.name, i, sub, got[i])
			}
		}
	}
}

// frameNameT snapshot 帧名 → 时刻：文件名里的时刻是接缝闸对号入座的依据
// （字典序 ≠ 时刻序，不能按文件名排序配位——段时长过 10s 就会乱序）。
func TestFrameNameT(t *testing.T) {
	for name, want := range map[string]float64{
		"frame-00-at-0.5s.png":   0.5,
		"frame-02-at-3.93s.png":  3.93,
		"frame-04-at-10.20s.png": 10.2,
		"contact-sheet.jpg":      -1,
		"frame-x-at-abc.png":     -1,
	} {
		got, ok := frameNameT(name)
		if want < 0 {
			if ok {
				t.Fatalf("%s 不应解析出时刻", name)
			}
			continue
		}
		if !ok || got != want {
			t.Fatalf("%s: 应 %v，实得 %v (ok=%v)", name, want, got, ok)
		}
	}
}

// meanLuma Rec.709 加权均值亮度（接缝闸的度量底座）。
func TestMeanLuma(t *testing.T) {
	px := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	px.Set(0, 0, color.RGBA{255, 255, 255, 255})
	px.Set(1, 0, color.RGBA{0, 0, 0, 255})
	if got := meanLuma(px); got < 127 || got > 128 {
		t.Fatalf("半白半黑均值应 ≈127.5，实得 %v", got)
	}
	if got := meanLuma(image.NewNRGBA(image.Rect(0, 0, 0, 0))); got != 0 {
		t.Fatalf("空图应 0，实得 %v", got)
	}
}
