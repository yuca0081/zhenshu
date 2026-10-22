package pipeline

import (
	"strings"
	"testing"

	"webpage-video-agent/server/internal/contract"
)

func passStoryboard() *contract.Storyboard {
	return &contract.Storyboard{TimingBasis: "narration", Segments: []contract.Segment{
		{ID: "seg01", Idx: 1, Key: "a", Narration: "第一段旁白内容。", DurationHint: 9},
		{ID: "seg02", Idx: 2, Key: "b", Narration: "第二段旁白内容。", DurationHint: 9},
	}}
}

func validPlan() *contract.DirectorPlan {
	return &contract.DirectorPlan{
		Arc: &contract.Arc{Spine: "从立到破",
			Chapters: []contract.ArcChapter{
				{Title: "立", Tension: 2, Segs: []string{"seg01"}},
				{Title: "破", Tension: 4, Segs: []string{"seg02"}},
			}},
		Segs: []contract.DirectorSegBeats{
			{ID: "seg01", Beats: []contract.Beat{{Quote: "第一段旁白内容", Expect: "开场钩子"}}},
			{ID: "seg02", Beats: []contract.Beat{{Quote: "第二段旁白内容", Expect: "反转"}, {Quote: "第二段旁白内容", Expect: "收束"}}},
		},
	}
}

// applyDirectorPlan：本体字段不经导演之手；合入后再合入（重试轮）不残留旧节拍。
func TestApplyDirectorPlan(t *testing.T) {
	sb := passStoryboard()
	if err := applyDirectorPlan(sb, validPlan()); err != nil {
		t.Fatalf("合入失败: %v", err)
	}
	if sb.Arc == nil || len(sb.Motifs) != 0 || len(sb.Segments[0].Beats) != 1 {
		t.Fatalf("合入结果不对: %+v", sb)
	}
	if sb.Segments[0].Narration != "第一段旁白内容。" || sb.Segments[0].DurationHint != 9 {
		t.Fatal("本体字段被导演作业动了")
	}
	// 第二轮（修复重试）换一套节拍：旧 beats 必须被清干净
	plan2 := validPlan()
	plan2.Arc.Spine = "从破到立"
	plan2.Segs = plan2.Segs[:1] // 这轮只给 seg01
	if err := applyDirectorPlan(sb, plan2); err != nil {
		t.Fatalf("二轮合入失败: %v", err)
	}
	if len(sb.Segments[1].Beats) != 0 {
		t.Fatalf("重试轮残留了上一版 seg02 节拍: %+v", sb.Segments[1].Beats)
	}
	if sb.Arc.Spine != "从破到立" {
		t.Fatal("二轮没有覆盖弧线")
	}
	// 引用不存在的段：拒绝
	bad := validPlan()
	bad.Segs = append(bad.Segs, contract.DirectorSegBeats{ID: "seg99"})
	if err := applyDirectorPlan(sb, bad); err == nil || !strings.Contains(err.Error(), "seg99") {
		t.Fatalf("引用不存在段应拒绝，得到: %v", err)
	}
}

// 无 LLM 配置：导演定调降级跳过，分镜本体零改动、不报错（老路径行为）。
func TestDirectorPassDegradesWithoutLLM(t *testing.T) {
	t.Setenv("LLM_API_KEY", "")
	t.Setenv("DEEPSEEK_API_KEY", "")
	p := NewProject(t.TempDir(), "dtest")
	sb := passStoryboard()
	directorPass(p, sb)
	if sb.Arc != nil || sb.Motifs != nil {
		t.Fatal("降级路径不该动分镜")
	}
	for i, seg := range sb.Segments {
		if seg.Beats != nil {
			t.Fatalf("段 %d 不该被塞节拍", i+1)
		}
	}
}

// 导演作业正文：全文旁白（锚句要逐字抄，摘要截断会害锚句）+ 输出形态 + 拒绝回喂。
func TestDirectorBrief(t *testing.T) {
	sb := passStoryboard()
	s := directorUserPrompt(sb, nil)
	for _, want := range []string{
		"第一段旁白内容。", // 全文旁白
		"逐字抄本段旁白的原句",
		"\"segments\"",
		"2–5 章",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("导演作业正文缺 %q", want)
		}
	}
	s2 := directorUserPrompt(sb, errFake("章没铺满全片"))
	if !strings.Contains(s2, "上一版被自动校验拒绝") || !strings.Contains(s2, "章没铺满全片") {
		t.Fatal("修复轮应回喂拒绝理由")
	}
}

type errFake string

func (e errFake) Error() string { return string(e) }

// 设计作业注入：有导演字段出「导演统筹」块，无字段不出现（老项目零感知）。
func TestDesignBriefDirectorBlock(t *testing.T) {
	p := NewProject(t.TempDir(), "dtest")
	sb := passStoryboard()
	if s := designBrief(p, sb, 0); strings.Contains(s, "导演统筹") {
		t.Fatal("无导演字段不该注入统筹块")
	}
	if err := applyDirectorPlan(sb, validPlan()); err != nil {
		t.Fatal(err)
	}
	sb.Motifs = []contract.Motif{{Name: "全知之眼", Desc: "金色眼睛",
		Appearances: []contract.MotifApp{{SegID: "seg01", Role: "establish"}, {SegID: "seg02", Role: "echo"}}}}
	s := designBrief(p, sb, 0)
	for _, want := range []string{
		"导演统筹（本片叙事结构——设计系统要呼应它）",
		"从立到破",
		"「立」（张力 2/5",
		"母题「全知之眼」：金色眼睛——出场 seg01(establish)、seg02(echo)",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("设计作业缺 %q", want)
		}
	}
}
