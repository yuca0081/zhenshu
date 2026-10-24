package agent

// finalscore_test.go —— finalAuditScore 折算回归锁（2026-10-24）：最终轮意见单
// findings 密度折 0–5 分（high -1 / low -0.25 / 下限 0.5），意见单缺失 = 0（未评）。
// 使用记账的 LastScore 此前恒为 0——这里是闭环下半截的护栏。
import (
	"os"
	"path/filepath"
	"testing"

	"webpage-video-agent/server/internal/pipeline"
)

func writeVerdict(t *testing.T, p *pipeline.Project, round int, sev ...string) {
	t.Helper()
	b := []byte(`{"dims":{},"findings":[`)
	for i, s := range sev {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, `{"id":"f`+string(rune('0'+i))+`","category":"read","anchor":"t=1.0","severity":"`+s+`","evidence":"t=1.0 x","fix":""}`...)
	}
	b = append(b, ']', '}')
	path := p.Artifact(filepath.Join("reviews", "final-all-r"+string(rune('0'+round))+".json"))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFinalAuditScore(t *testing.T) {
	p := pipeline.NewProject(t.TempDir(), "pj1")

	// 无意见单（终审降级放行/异常路径）= 0，维持「0=未评」语义
	if got := finalAuditScore(p); got != 0 {
		t.Fatalf("无意见单应记 0（未评），实际 %v", got)
	}

	// 干净一轮 = 满分
	writeVerdict(t, p, 1)
	if got := finalAuditScore(p); got != 5 {
		t.Fatalf("零 findings 应 5 分，实际 %v", got)
	}

	// 多轮取最终轮：r1 带 2 high 1 low，r2 干净 → 按 r2 记满分
	q := pipeline.NewProject(t.TempDir(), "pj2")
	writeVerdict(t, q, 1, "high", "high", "low")
	writeVerdict(t, q, 2)
	if got := finalAuditScore(q); got != 5 {
		t.Fatalf("最终轮干净应 5 分，实际 %v", got)
	}

	// 折算：3 high + 2 low = 5-3-0.5 = 1.5
	w := pipeline.NewProject(t.TempDir(), "pj3")
	writeVerdict(t, w, 1, "high", "high", "high", "low", "low")
	if got := finalAuditScore(w); got != 1.5 {
		t.Fatalf("3 high 2 low 应 1.5 分，实际 %v", got)
	}

	// 下限：6 high 折到负数也要夹回 0.5（能出片就非 0 分）
	z := pipeline.NewProject(t.TempDir(), "pj4")
	writeVerdict(t, z, 1, "high", "high", "high", "high", "high", "high")
	if got := finalAuditScore(z); got != 0.5 {
		t.Fatalf("6 high 应夹到下限 0.5，实际 %v", got)
	}
}
