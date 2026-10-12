package store

import (
	"fmt"
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

// ListMsgsTail 取最近 limit 条且保持时间正序：对话历史窗口依赖这个语义——
// 取最旧 100 条会让超量后的最新对话永远进不了 LLM 上下文。
func TestMemListMsgsTail(t *testing.T) {
	s := newMem()
	_ = s.CreateProject("p1", "t")
	for i := 1; i <= 7; i++ {
		_ = s.SaveMsg(&Msg{ProjectID: "p1", Role: "user", Type: "text", Content: fmt.Sprintf("m%d", i)})
	}
	_ = s.SaveMsg(&Msg{ProjectID: "p1", Role: "agent", Type: "tool_call", Content: "read_project"}) // 尾部混入 tool_call
	_ = s.SaveMsg(&Msg{ProjectID: "p2", Role: "user", Type: "text", Content: "别家"})                 // 项目隔离

	// 与 ListMsgs 同过滤（text/tool_call/error 都算消息行）：尾部窗口 3 条按插入序返回
	got, err := s.ListMsgsTail("p1", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("应取最近 3 条，得 %d: %+v", len(got), got)
	}
	for i, want := range []string{"m6", "m7", "read_project"} {
		if got[i].Content != want {
			t.Fatalf("第 %d 条应为 %s，得 %s（窗口须是最新且正序）", i, want, got[i].Content)
		}
	}
	// limit 大于存量：全量正序返回，不越界不重复
	got, _ = s.ListMsgsTail("p1", 100)
	if len(got) != 8 || got[0].Content != "m1" || got[7].Content != "read_project" {
		t.Fatalf("全量窗口不符: %+v", got)
	}
	// 项目隔离：p2 只有自己那条
	if got, _ = s.ListMsgsTail("p2", 10); len(got) != 1 || got[0].Content != "别家" {
		t.Fatalf("项目隔离不符: %+v", got)
	}
}
