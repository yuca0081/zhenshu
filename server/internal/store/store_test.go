package store

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// ListMsgsTail 取最近 limit 条且保持时间正序：对话历史窗口依赖这个语义——
// 取最旧 100 条会让超量后的最新对话永远进不了 LLM 上下文。
func TestMemListMsgsTail(t *testing.T) {
	s := newMem()
	_ = s.CreateProject("p1", "t", "")
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

// 项目账行加厚（0006）：upsert 后 UpdateProjectMeta 覆盖可检索字段与存储前缀；
// 未同步的项目字段保持零值。
func TestMemUpdateProjectMeta(t *testing.T) {
	s := newMem()
	if err := s.CreateProject("p1", "测试", "u1"); err != nil {
		t.Fatal(err)
	}
	meta := ProjectMeta{
		Topic: "榫卯工艺", Aspect: "9:16",
		Status: "style", StoragePrefix: "projects/p1/",
	}
	if err := s.UpdateProjectMeta("p1", meta); err != nil {
		t.Fatal(err)
	}
	if s.projects[0].Topic != meta.Topic || s.projects[0].Aspect != meta.Aspect ||
		s.projects[0].StoragePrefix != meta.StoragePrefix || s.projects[0].Status != meta.Status {
		t.Fatalf("账行未按盘上值覆盖: %+v", s.projects[0])
	}
	// 不存在的项目：静默 no-op（列表自愈场景下行可能还没建）
	if err := s.UpdateProjectMeta("p-none", meta); err != nil {
		t.Fatalf("不存在项目的 meta 同步应 no-op: %v", err)
	}
}

// 账本读取面（0008 列表事实源）：ListProjects/GetProject/SetLastEvent——
// listProjects 直出、云端项目首触归属判定、制作终态落账都吃这三个方法。
func TestMemProjectLedger(t *testing.T) {
	s := newMem()
	if err := s.CreateProject("p1", "甲", "u1"); err != nil {
		t.Fatal(err)
	}
	_ = s.CreateProject("p2", "乙", "")
	if err := s.UpdateProjectMeta("p1", ProjectMeta{Mode: "music", Status: "created"}); err != nil {
		t.Fatal(err)
	}
	ts := time.Now().Add(-time.Hour)
	if err := s.SetLastEvent("p1", "produce.done", ts); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateStatus("p1", "video"); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListProjects()
	if err != nil || len(rows) != 2 {
		t.Fatalf("ListProjects = %v, %v", rows, err)
	}
	row, err := s.GetProject("p1")
	if err != nil {
		t.Fatal(err)
	}
	if row.Mode != "music" || row.LastEvent != "produce.done" || !row.LastEventTs.Equal(ts) {
		t.Fatalf("终态/模式落账不符: %+v", row)
	}
	if row.Status != "video" {
		t.Fatalf("status 转移不符: %s", row.Status)
	}
	// 无行项目：GetProject 报 ErrNoProjectRow（云端首触据此判定真不存在）
	if _, err := s.GetProject("p-none"); !errors.Is(err, ErrNoProjectRow) {
		t.Fatalf("无行应回 ErrNoProjectRow: %v", err)
	}
}

// 制作租约（串行换机，Q5）：同项目二占拒绝、释放后可再占；释放幂等。
func TestMemProductionLease(t *testing.T) {
	s := newMem()
	_ = s.CreateProject("p1", "t", "")
	r1, ok, err := s.TryProductionLease("p1")
	if err != nil || !ok {
		t.Fatalf("首占应成功: %v %v", ok, err)
	}
	if _, ok, _ := s.TryProductionLease("p1"); ok {
		t.Fatal("租约未释放前二占应被拒")
	}
	r2, ok, err := s.TryProductionLease("p2")
	if err != nil || !ok {
		t.Fatalf("其他项目占坑应成功: %v %v", ok, err)
	}
	r1()
	r1() // 幂等：重复释放不炸
	if _, ok, _ := s.TryProductionLease("p1"); !ok {
		t.Fatal("释放后应可再占")
	}
	r2()
}

// 通用设置 KV（0009）：整存整取 + 覆盖写 + 无行=ok=false（与库报错分开）。
func TestMemKVRoundtrip(t *testing.T) {
	s := newMem()
	if _, _, ok, err := s.GetKV("llm_settings"); err != nil || ok {
		t.Fatalf("未设置应 ok=false 无错: ok=%v err=%v", ok, err)
	}
	if err := s.PutKV("llm_settings", []byte(`{"version":2}`)); err != nil {
		t.Fatal(err)
	}
	val, ts, ok, err := s.GetKV("llm_settings")
	if err != nil || !ok {
		t.Fatalf("写入后应可读回: ok=%v err=%v", ok, err)
	}
	if string(val) != `{"version":2}` || ts.IsZero() {
		t.Fatalf("读回不符: %s ts=%v", val, ts)
	}
	oldTS := ts
	time.Sleep(2 * time.Millisecond) // Windows 时钟精度粗，连续写可能同 tick
	if err := s.PutKV("llm_settings", []byte(`{"version":2,"providers":{}}`)); err != nil {
		t.Fatal(err)
	}
	val, ts, _, _ = s.GetKV("llm_settings")
	if string(val) != `{"version":2,"providers":{}}` {
		t.Fatalf("覆盖写不符: %s", val)
	}
	if !ts.After(oldTS) {
		t.Fatalf("覆盖写应推进 updated_at: %v -> %v", oldTS, ts)
	}
	// 键隔离
	if _, _, ok, _ := s.GetKV("other"); ok {
		t.Fatal("其他键不应串台")
	}
}
