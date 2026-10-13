package contract

import (
	"strings"
	"testing"
)

// 导演统筹字段校验：合法全量样例过、每类违规各自被拦、老项目（零字段）零感知。
func directorTestStoryboard() *Storyboard {
	return &Storyboard{
		TimingBasis: "narration",
		Segments: []Segment{
			{ID: "seg01", Idx: 1, Key: "钩子", Narration: "你此刻的念头，可能早已被写定。", DurationHint: 10},
			{ID: "seg02", Idx: 2, Key: "诞生", Narration: "一八一四年，拉普拉斯写下一段话。", DurationHint: 12},
			{ID: "seg03", Idx: 3, Key: "代价", Narration: "但这只需要一个前提。", DurationHint: 11},
			{ID: "seg04", Idx: 4, Key: "终章", Narration: "而宇宙没有回答。", DurationHint: 9},
		},
	}
}

func fullDirector() (*Arc, []Motif) {
	arc := &Arc{
		Spine: "宿命论从诞生到崩塌",
		Chapters: []ArcChapter{
			{Title: "立论", Mood: "挑衅", Tension: 3, Segs: []string{"seg01", "seg02"}},
			{Title: "反噬", Mood: "递进", Tension: 5, Segs: []string{"seg03"}},
			{Title: "回响", Mood: "收束", Tension: 2, Segs: []string{"seg04"}},
		},
	}
	motifs := []Motif{
		{Name: "全知之眼", Desc: "粒子汇聚成的金色眼睛",
			Appearances: []MotifApp{
				{SegID: "seg01", Role: "establish"},
				{SegID: "seg04", Role: "echo"},
			}},
	}
	return arc, motifs
}

func beatsFor(sb *Storyboard) {
	sb.Segments[0].Beats = []Beat{
		{Quote: "你此刻的念头", Expect: "粒子从四方散点开始汇聚"},
		{Quote: "可能早已被写定", Expect: "金色眼睛睁开的定格"},
	}
	sb.Segments[1].Beats = []Beat{
		{Quote: "一八一四年", Expect: "仿宋年份大字压底"},
		{Quote: "拉普拉斯写下一段话", Expect: "引文逐行浮现"},
	}
	sb.Segments[2].Beats = []Beat{
		{Quote: "但这只需要一个前提", Expect: "计数器开始爬升"},
		{Quote: "一个前提", Expect: "刻度尺无限细分"},
	}
	sb.Segments[3].Beats = []Beat{
		{Quote: "而宇宙没有回答", Expect: "金眼回归后暗场"},
		{Quote: "没有回答", Expect: "落版字"},
	}
}

func TestValidateDirectorFullPass(t *testing.T) {
	sb := directorTestStoryboard()
	sb.Arc, sb.Motifs = fullDirector()
	beatsFor(sb)
	if err := ValidateStoryboard(sb); err != nil {
		t.Fatalf("合法全量样例应通过: %v", err)
	}
}

func TestValidateDirectorLegacyZeroFields(t *testing.T) {
	if err := ValidateStoryboard(directorTestStoryboard()); err != nil {
		t.Fatalf("老项目零导演字段应照常通过: %v", err)
	}
}

func TestValidateDirectorRejects(t *testing.T) {
	cases := []struct {
		name string
		mut  func(sb *Storyboard)
		want string
	}{
		{"弧线一章", func(sb *Storyboard) {
			sb.Arc = &Arc{Spine: "s", Chapters: []ArcChapter{{Title: "唯一章", Tension: 3, Segs: []string{"seg01", "seg02", "seg03", "seg04"}}}}
		}, "2–6"},
		{"主线为空", func(sb *Storyboard) {
			sb.Arc = &Arc{Chapters: []ArcChapter{{Title: "a", Tension: 1, Segs: []string{"seg01"}}, {Title: "b", Tension: 2, Segs: []string{"seg02"}}}}
			beatsFor(sb)
		}, "spine"},
		{"张力越界", func(sb *Storyboard) {
			sb.Arc, _ = fullDirector()
			sb.Arc.Chapters[0].Tension = 7
			beatsFor(sb)
		}, "tension"},
		{"章没铺满", func(sb *Storyboard) {
			sb.Arc, _ = fullDirector()
			sb.Arc.Chapters = sb.Arc.Chapters[:2] // 砍掉末章，seg04 不属于任何章
			beatsFor(sb)
		}, "铺满全片"},
		{"章空覆盖", func(sb *Storyboard) {
			sb.Arc, _ = fullDirector()
			sb.Arc.Chapters[2].Segs = nil
			beatsFor(sb)
		}, "没有覆盖任何段"},
		{"章乱序", func(sb *Storyboard) {
			sb.Arc, _ = fullDirector()
			sb.Arc.Chapters[0].Segs = []string{"seg02", "seg01"}
			beatsFor(sb)
		}, "连续铺满"},
		{"段进两个章", func(sb *Storyboard) {
			sb.Arc, _ = fullDirector()
			sb.Arc.Chapters[1].Segs = []string{"seg02", "seg03"}
			beatsFor(sb)
		}, "重叠"},
		{"弧线在场但缺节拍", func(sb *Storyboard) {
			sb.Arc, _ = fullDirector()
			// 不打 beats
		}, "节拍表不足 2"},
		{"节拍锚句是幻觉", func(sb *Storyboard) {
			sb.Arc, _ = fullDirector()
			beatsFor(sb)
			sb.Segments[0].Beats[0].Quote = "这句话不在旁白里"
		}, "锚句不是旁白原文"},
		{"节拍超上限", func(sb *Storyboard) {
			sb.Arc, _ = fullDirector()
			beatsFor(sb)
			sb.Segments[0].Beats = append(sb.Segments[0].Beats,
				Beat{Quote: "你此刻的念头", Expect: "a"}, Beat{Quote: "可能早已被写定", Expect: "b"},
				Beat{Quote: "可能早已被写定", Expect: "c"}, Beat{Quote: "可能早已被写定", Expect: "d"},
				Beat{Quote: "可能早已被写定", Expect: "e"})
		}, "超上限"},
		{"母题只出场一次", func(sb *Storyboard) {
			_, sb.Motifs = fullDirector()
			sb.Motifs[0].Appearances = sb.Motifs[0].Appearances[:1]
		}, "≥2 才算贯穿"},
		{"母题角色非法", func(sb *Storyboard) {
			_, sb.Motifs = fullDirector()
			sb.Motifs[0].Appearances[1].Role = "cameo"
		}, "establish|callback|echo"},
		{"母题引用不存在段", func(sb *Storyboard) {
			_, sb.Motifs = fullDirector()
			sb.Motifs[0].Appearances[1].SegID = "seg99"
		}, "不存在的段"},
		{"母题超上限", func(sb *Storyboard) {
			_, sb.Motifs = fullDirector()
			sb.Motifs = append(sb.Motifs, sb.Motifs[0], sb.Motifs[0], sb.Motifs[0])
		}, "≤3"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sb := directorTestStoryboard()
			c.mut(sb)
			err := ValidateStoryboard(sb)
			if err == nil {
				t.Fatalf("%s 应被拒绝", c.name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("%s 拒绝理由应含 %q，得到: %v", c.name, c.want, err)
			}
		})
	}
}

// 无弧线但有节拍：合法（部分导演字段不强制成套出现）。
func TestValidateDirectorBeatsWithoutArc(t *testing.T) {
	sb := directorTestStoryboard()
	sb.Segments[0].Beats = []Beat{{Quote: "你此刻的念头", Expect: "散点"}}
	if err := ValidateStoryboard(sb); err != nil {
		t.Fatalf("无弧线的单段节拍应合法: %v", err)
	}
}

// 锚句允许空白差异（旁白排版空格不构成否决理由）。
func TestValidateDirectorQuoteIgnoresSpaces(t *testing.T) {
	sb := directorTestStoryboard()
	sb.Arc, _ = fullDirector()
	beatsFor(sb)
	sb.Segments[0].Beats[0].Quote = "你此刻的  念头"
	if err := ValidateStoryboard(sb); err != nil {
		t.Fatalf("空白差异的锚句应放行: %v", err)
	}
}
