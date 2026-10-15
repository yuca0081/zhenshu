package contract

import (
	"strings"
	"testing"
)

func TestSplitSentences(t *testing.T) {
	text := "蚊子为什么嗡嗡叫？其实那不是叫声。\n翅膀每秒振动六百次！这就是答案。"
	ss := SplitSentences(text)
	want := []string{
		"蚊子为什么嗡嗡叫？",
		"其实那不是叫声。",
		"翅膀每秒振动六百次！",
		"这就是答案。",
	}
	if len(ss) != len(want) {
		t.Fatalf("切出 %d 句， want %d: %+v", len(ss), len(want), ss)
	}
	for i, s := range ss {
		if s.ID != "N"+string(rune('1'+i)) {
			t.Errorf("句 %d 编号 = %s", i+1, s.ID)
		}
		if s.Text != want[i] {
			t.Errorf("句 %d = %q, want %q", i+1, s.Text, want[i])
		}
	}
	// 拼接还原正文（去空白后）
	var joined strings.Builder
	for _, s := range ss {
		joined.WriteString(s.Text)
	}
	if strings.ReplaceAll(joined.String(), "\n", "") != strings.ReplaceAll(text, "\n", "") {
		t.Errorf("拼接不能还原正文: %q", joined.String())
	}
}

func TestValidateScriptText(t *testing.T) {
	if err := ValidateScriptText("正常的旁白。第二句。"); err != nil {
		t.Errorf("正常文本被拒: %v", err)
	}
	for _, bad := range []string{
		"# 标题\n正文",
		"- 列表项",
		"| 表格 | 列 |",
		"```\ncode\n```",
		"看这里](https://example.com)混链接",
	} {
		if err := ValidateScriptText(bad); err == nil {
			t.Errorf("结构标记文本未被拦: %q", bad)
		}
	}
}

func TestParseNarrationRefs(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"N3", []string{"N3"}},
		{"N3-N4", []string{"N3", "N4"}},
		{"n2,n4", []string{"N2", "N4"}},
	}
	for _, c := range cases {
		got, err := ParseNarrationRefs(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if len(got) != len(c.want) {
			t.Fatalf("%s 展开为 %v, want %v", c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("%s 展开为 %v, want %v", c.in, got, c.want)
			}
		}
	}
	for _, bad := range []string{"", "N4-N3", "3", "N3-N5-N7", "NX", "N0"} {
		if _, err := ParseNarrationRefs(bad); err == nil {
			t.Errorf("非法引用未被拦: %q", bad)
		}
	}
}

func scriptForN(t *testing.T, n int) *Script {
	t.Helper()
	parts := make([]string, n)
	for i := range parts {
		parts[i] = "这是第" + string(rune('0'+i+1)) + "句。"
	}
	s := NewScript(strings.Join(parts, ""), "manuscript", nil, 60)
	if len(s.Sentences) != n {
		t.Fatalf("切句 %d, want %d", len(s.Sentences), n)
	}
	return s
}

func TestValidateStoryboardDraft(t *testing.T) {
	script := scriptForN(t, 4)
	pack := &StylePack{Grammar: EditGrammar{Density: "中", BandMin: 6, BandMax: 15, Continuity: "弱"}}
	pack.DesignSystem = minimalValidDesign(t)

	d := &StoryboardDraft{
		Paragraphs: []DraftParagraph{{Title: "开篇", Segs: []string{"seg01", "seg02"}}},
		Segments: []DraftSegment{
			{ID: "seg01", Idx: 1, Key: "起", NarrationRefs: "N1-N2",
				Brief: VisualBrief{Scene: "场景", FirstFrame: "首拍", LastFrame: "末拍"}, DurationHint: 8},
			{ID: "seg02", Idx: 2, Key: "承", NarrationRefs: "N3-N4",
				Brief: VisualBrief{Scene: "场景2", FirstFrame: "首拍2", LastFrame: "末拍2"}, DurationHint: 10},
		},
	}
	if err := ValidateStoryboardDraft(d, script, pack); err != nil {
		t.Fatalf("合法分镜稿被拒: %v", err)
	}

	// 丢句：N4 没被引用
	d2 := deepCopyDraft(d)
	d2.Segments[1].NarrationRefs = "N3"
	if err := ValidateStoryboardDraft(d2, script, pack); err == nil || !strings.Contains(err.Error(), "N4") {
		t.Errorf("丢句未被拦: %v", err)
	}
	// 段长带越界（band [6,15]，钳制后同）
	d3 := deepCopyDraft(d)
	d3.Segments[0].DurationHint = 18
	if err := ValidateStoryboardDraft(d3, script, pack); err == nil {
		t.Error("段长超带未被拦")
	}
	// 段落未铺满
	d4 := deepCopyDraft(d)
	d4.Paragraphs[0].Segs = []string{"seg01"}
	if err := ValidateStoryboardDraft(d4, script, pack); err == nil {
		t.Error("段落未铺满未被拦")
	}
	// 重叠引用
	d5 := deepCopyDraft(d)
	d5.Segments[1].NarrationRefs = "N2-N4"
	if err := ValidateStoryboardDraft(d5, script, pack); err == nil {
		t.Error("句重叠未被拦")
	}
}

func TestTranslate(t *testing.T) {
	script := scriptForN(t, 2)
	pack := &StylePack{StyleID: "sp-x", Grammar: EditGrammar{BandMin: 5, BandMax: 20}}
	d := &StoryboardDraft{
		Plan:       "p",
		Paragraphs: []DraftParagraph{{Title: "全部", Segs: []string{"seg01"}}},
		Segments: []DraftSegment{{
			ID: "seg01", Idx: 1, Key: "k", NarrationRefs: "N1-N2",
			Brief:        VisualBrief{Scene: "s", FirstFrame: "f", LastFrame: "l"},
			DurationHint: 9,
		}},
	}
	if err := ValidateStoryboardDraft(d, script, pack); err != nil {
		t.Fatal(err)
	}
	sb := d.Translate(script, pack)
	if sb.TimingBasis != "narration" || sb.StyleID != "sp-x" {
		t.Errorf("翻译头不对: %+v", sb)
	}
	seg := sb.Segments[0]
	if seg.Narration != "这是第1句。这是第2句。" {
		t.Errorf("旁白展开 = %q", seg.Narration)
	}
	if !strings.Contains(seg.VisualBrief, "首拍：f") || !strings.Contains(seg.VisualBrief, "末拍：l") {
		t.Errorf("visual_brief 未带首末拍: %q", seg.VisualBrief)
	}
	if seg.Brief == nil || seg.Brief.FirstFrame != "f" || seg.Brief.LastFrame != "l" {
		t.Errorf("结构化 brief 未随翻译落段: %+v", seg.Brief)
	}
	if err := ValidateStoryboard(sb); err != nil {
		t.Errorf("翻译产物未过制作契约校验: %v", err)
	}
}

// minimalValidDesign 一个能过 ValidateStyleSamplesDraft 的最小设计系统（测试夹具）。
func minimalValidDesign(t *testing.T) *StyleSamples {
	t.Helper()
	ss := &StyleSamples{
		Direction: "测试方向",
		Concept:   "为什么配这部片",
		Skins: []Skin{{
			Name:  "主",
			Slots: SkinSlots{BG: "#101010", TXT: "#EDEDED", Accent: "#FFCC00", Font: "'Microsoft YaHei',sans-serif"},
			CSS:   ".skin-root{}",
		}},
		Fonts: &FontRoles{
			Display: FontRole{Stack: "'SimHei','Microsoft YaHei',sans-serif"},
			Body:    FontRole{Stack: "'KaiTi','Microsoft YaHei',serif"},
		},
		Motion: &MotionLang{
			Ease: "power2.out", DurIn: 0.5, DurSettle: 0.25, Stagger: 0.08,
			Entrances: []MotionMove{
				{Name: "滑入", How: "x:40→0", UseFor: "标题"},
				{Name: "点亮", How: "opacity:0→1", UseFor: "正文"},
				{Name: "落定", How: "scale:1.05→1", UseFor: "数据"},
			},
			Rhythm: "每 3 秒一个事件", Exits: "淡出", Camera: "微推", Transition: "直切",
			Notes: "克制",
		},
		Device: &DeviceSpec{Name: "卡片", Desc: "什么", PerSeg: "怎么变奏"},
		EntryExit: &EntryExitSpec{Entry: "晕染显影", Exit: "淡出收黑", EntryDur: 0.4},
		Samples: []Sample{{Tag: "样张 A", Desc: "测试", HTML: "<!doctype html><html></html>"}},
	}
	if err := ValidateStyleSamplesDraft(ss); err != nil {
		t.Fatalf("夹具设计系统不合法: %v", err)
	}
	return ss
}

func deepCopyDraft(d *StoryboardDraft) *StoryboardDraft {
	cp := *d
	cp.Segments = append([]DraftSegment(nil), d.Segments...)
	cp.Paragraphs = append([]DraftParagraph(nil), d.Paragraphs...)
	return &cp
}
