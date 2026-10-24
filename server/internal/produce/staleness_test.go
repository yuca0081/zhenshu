package produce

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/events"
	"webpage-video-agent/server/internal/pipeline"
	"webpage-video-agent/server/internal/prep"
	"webpage-video-agent/server/internal/review"
)

// writeJournal 落一份「上次制作成功」的账本：全部段 audited + 终审通过——
// 与真实出片后的 journal 状态一致（断点续跑与二次制作共用的恢复输入）。
func writeJournal(t *testing.T, p *pipeline.Project, segIDs []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(journalPath(p)), 0o755); err != nil {
		t.Fatal(err)
	}
	b := ""
	for _, id := range segIDs {
		b += fmt.Sprintf(`{"kind":"seg","id":%q,"state":"audited","round":1,"gen":1}`, id) + "\n"
	}
	b += `{"kind":"final","id":"all","state":"audited","round":1,"detail":"全片终审通过"}` + "\n"
	if err := os.WriteFile(journalPath(p), []byte(b), 0o644); err != nil {
		t.Fatal(err)
	}
}

func storyboardWith(narr []string) *contract.Storyboard {
	sb := &contract.Storyboard{}
	for i, s := range narr {
		sb.Segments = append(sb.Segments, contract.Segment{
			ID: fmt.Sprintf("seg%02d", i+1), Idx: i + 1, Key: fmt.Sprintf("段%d", i+1), Narration: s,
		})
	}
	return sb
}

// newStalenessFixture 造一个「上次成功出片」的项目：账本全段 audited + 每段
// 场景文件带当刻指纹旁车 + 设计系统文件。返回协调器与项目句柄。
func newStalenessFixture(t *testing.T, narr []string) (*coordinator, *pipeline.Project) {
	t.Helper()
	dir := t.TempDir()
	r := &Runner{DataDir: dir, RootDir: dir, Hub: events.NewHub()}
	p := pipeline.NewProject(dir, "p1")
	if err := os.MkdirAll(filepath.Join(p.Dir, "input"), 0o755); err != nil {
		t.Fatal(err)
	}
	sb := storyboardWith(narr)
	writeJournal(t, p, segIDs(sb))
	if err := os.MkdirAll(p.Artifact("style"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Artifact("style/style_samples.json"), []byte(`{"direction":"v1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	c := newCoordinator(r, p, &review.Service{P: p, Emit: r.Hub.Emit}, sb, nil, nil, nil)
	// 每段落场景文件 + 当刻指纹旁车（真实写画面路径的落盘产物）
	if err := os.MkdirAll(p.Artifact("llm"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, seg := range sb.Segments {
		if err := os.WriteFile(p.Artifact("llm/comp-"+seg.ID+".json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(sceneSidecarPath(p, seg.ID), []byte(sceneSidecarFingerprint(p, sb, seg)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return c, p
}

func segIDs(sb *contract.Storyboard) []string {
	ids := make([]string, len(sb.Segments))
	for i, s := range sb.Segments {
		ids[i] = s.ID
	}
	return ids
}

// TestNarrationFingerprintDelta TTS 复用闸的判旧输入：旁白变/段集变必须翻指纹，
// 没变必须稳定（翻不了闸就会拿旧配音配新稿）。
func TestNarrationFingerprintDelta(t *testing.T) {
	base := storyboardWith([]string{"甲段旁白", "乙段旁白", "丙段旁白"})
	fp := narrationFingerprint(base)
	if fp == "" {
		t.Fatal("指纹为空")
	}
	if again := narrationFingerprint(storyboardWith([]string{"甲段旁白", "乙段旁白", "丙段旁白"})); again != fp {
		t.Fatal("同内容指纹应稳定")
	}
	if changed := narrationFingerprint(storyboardWith([]string{"甲段旁白改了", "乙段旁白", "丙段旁白"})); changed == fp {
		t.Fatal("改旁白指纹必须变")
	}
	if dropped := narrationFingerprint(storyboardWith([]string{"甲段旁白", "乙段旁白"})); dropped == fp {
		t.Fatal("段集变化指纹必须变")
	}
}

// TestRestoredSegmentsContentGate —— 2026-10-23 高-1 的回归钉（修复版）。
//
// 场景：项目已成功出片（journal 全段 audited + 场景指纹旁车齐）→ 改稿 →
// 分镜同 ID 段内容已换 → 二次 start_production。
//
// 锁定行为：恢复判定认内容（场景指纹旁车与 sceneGen 复用闸同源）——
//
//	旁白/画面提示/设计系统/导演统筹任一变 → 该段不恢复，按新输入重走链；
//	什么都没变（断点续跑/中断重启）→ 照常恢复，不重做。
//
// 修复前的行为（账本 audited 即恢复、画面静默复用旧稿）由此测试防回归。
func TestRestoredSegmentsContentGate(t *testing.T) {
	old := []string{"甲段旧旁白", "乙段旧旁白", "丙段旧旁白"}
	c, p := newStalenessFixture(t, old)

	// ① 什么都没变（断点续跑/重启续跑）：全部照常恢复
	for _, s := range c.segs {
		if !c.restorable(s.seg) {
			t.Fatalf("%s 内容未变应可恢复（断点续跑是恢复的本职）", s.seg.ID)
		}
	}

	// ② 用户改稿：真源替换 + ①级联作废（prep 真函数），③重跑产物落新分镜
	// （同 ID 段、seg02 旁白已换——正是「改稿」语义）
	if _, err := prep.ReplaceManuscript(p, "改稿后的全新正文"); err != nil {
		t.Fatal(err)
	}
	newSB := storyboardWith([]string{"甲段旧旁白", "乙段改稿后旁白", "丙段旧旁白"})
	if err := os.MkdirAll(filepath.Dir(p.Artifact("storyboards/storyboard.json")), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Artifact("storyboards/storyboard.json"), []byte(`{"segments":[{"id":"seg01"},{"id":"seg02"},{"id":"seg03"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// 新分镜装载进协调器（③重跑后的二次制作入口）
	r := &Runner{DataDir: p.Dir[:len(p.Dir)-len("/projects/p1")], RootDir: p.Dir[:len(p.Dir)-len("/projects/p1")], Hub: events.NewHub()}
	c2 := newCoordinator(r, p, &review.Service{P: p, Emit: r.Hub.Emit}, newSB, nil, nil, nil)
	for _, s := range c2.segs {
		e := c2.segEntry(s.seg.ID)
		if e.State != stAudited {
			t.Fatalf("%s 账本状态 %q，复现前提破坏", s.seg.ID, e.State)
		}
		switch s.seg.ID {
		case "seg02":
			if c2.restorable(s.seg) {
				t.Fatal("seg02 旁白已改：不得按旧账恢复（高-1 主断言）——必须重走链重画")
			}
		case "seg01", "seg03":
			if !c2.restorable(s.seg) {
				t.Fatalf("%s 旁白未变应照常恢复（逐段粒度：改一段不该连坐全片）", s.seg.ID)
			}
		}
	}

	// ③ 设计系统变了（换风格重出样张）：所有段一律不恢复
	if err := os.WriteFile(p.Artifact("style/style_samples.json"), []byte(`{"direction":"v2-全新设计"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	c3 := newCoordinator(r, p, &review.Service{P: p, Emit: r.Hub.Emit}, newSB, nil, nil, nil)
	for _, s := range c3.segs {
		if c3.restorable(s.seg) {
			t.Fatalf("%s 设计系统已变仍按旧账恢复——旧画风会混进新片", s.seg.ID)
		}
	}

	// ④ 指纹旁车缺失（老项目首遇新闸）：按失配处理，重走链落新旁车
	if err := os.Remove(sceneSidecarPath(p, "seg01")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Artifact("style/style_samples.json"), []byte(`{"direction":"v1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	c4 := newCoordinator(r, p, &review.Service{P: p, Emit: r.Hub.Emit}, newSB, nil, nil, nil)
	for _, s := range c4.segs {
		if s.seg.ID == "seg01" && c4.restorable(s.seg) {
			t.Fatal("旁车缺失应按失配处理（不可恢复），不得凭账本裸恢复")
		}
	}
}
