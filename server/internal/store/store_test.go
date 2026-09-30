package store

import (
	"testing"
)

// 内存实现 render_jobs 生命周期：起跑 running → 终态回写最近一行 running；
// 历史行保留（尝试次数可查）；无 running 行时终态回写是 no-op 不虚报。
func TestMemRenderJobs(t *testing.T) {
	s := newMem()
	if err := s.StartRenderJob("p1", "produce"); err != nil {
		t.Fatal(err)
	}
	if err := s.StartRenderJob("p1", "rework"); err != nil {
		t.Fatal(err)
	}
	// 回写只关最晚的 running（rework），先起的 produce 仍 running
	if err := s.FinishRenderJob("p1", "done", "renders/main.mp4", ""); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.ListRenderJobs("p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("应 2 行，得 %d", len(jobs))
	}
	// 新在前
	if jobs[0].Kind != "rework" || jobs[0].Status != "done" || jobs[0].OutputPath != "renders/main.mp4" {
		t.Fatalf("rework 行终态不符: %+v", jobs[0])
	}
	if jobs[1].Status != "running" || jobs[1].FinishedAt != nil {
		t.Fatalf("produce 行不该被动: %+v", jobs[1])
	}
	// 再回写关掉 produce 行
	if err := s.FinishRenderJob("p1", "failed", "", "boom"); err != nil {
		t.Fatal(err)
	}
	jobs, _ = s.ListRenderJobs("p1")
	if jobs[0].Kind != "rework" || jobs[1].Status != "failed" || jobs[1].Error != "boom" {
		t.Fatalf("二次回写不符: %+v", jobs)
	}
	// 项目隔离 + 无 running 行时 no-op
	if jobs, _ = s.ListRenderJobs("p2"); len(jobs) != 0 {
		t.Fatalf("p2 不该有行: %+v", jobs)
	}
	if err := s.FinishRenderJob("p2", "done", "", ""); err != nil {
		t.Fatal(err)
	}
}
