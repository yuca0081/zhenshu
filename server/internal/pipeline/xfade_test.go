package pipeline

import (
	"errors"
	"strings"
	"testing"
)

// buildXfadeGraph 的数学：offset_i = Σ_{k<i}d_k − D·i（前 i 次收缩都已发生），
// 段起点同式。总时长 = Σd − D·(n−1)。
func TestBuildXfadeGraph(t *testing.T) {
	vc, ac, starts, err := buildXfadeGraph([]float64{5.0, 4.0, 6.0})
	if err != nil {
		t.Fatal(err)
	}
	// 起点：0、5−0.4=4.6、(5+4)−0.8=8.2
	want := []float64{0, 4.6, 8.2}
	for i, w := range want {
		if diff := starts[i] - w; diff > 0.01 || diff < -0.01 {
			t.Fatalf("start[%d]=%v want %v", i, starts[i], w)
		}
	}
	// 第二个边界的 offset：cum(0,1)=9 − 0.4×2 = 8.2（链尾直接落 [vout]）
	if !strings.Contains(vc, "offset=8.200[vout]") {
		t.Fatalf("第二个边界 offset 不对: %s", vc)
	}
	if !strings.Contains(vc, "[vout]") || !strings.Contains(ac, "[aout]") {
		t.Fatal("链尾必须落到 [vout]/[aout]")
	}
	if strings.Count(vc, "xfade=") != 2 || strings.Count(ac, "acrossfade=") != 2 {
		t.Fatalf("三段应有 2 个边界: %s | %s", vc, ac)
	}
}

// 段短于叠化窗口：拒绝并回退 concat（口径必须一致，不能硬叠）。
func TestBuildXfadeGraphShortSeg(t *testing.T) {
	if _, _, _, err := buildXfadeGraph([]float64{5.0, 0.3}); !errors.Is(err, errXfadeShort) {
		t.Fatalf("短段应报 errXfadeShort, got %v", err)
	}
	if _, _, _, err := buildXfadeGraph([]float64{5.0}); !errors.Is(err, errXfadeShort) {
		t.Fatalf("单段应报 errXfadeShort, got %v", err)
	}
}
