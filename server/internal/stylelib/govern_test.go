package stylelib

// govern_test.go —— 治理路径单测：自定义登记在词表冷启动（空表）时自动种词，
// 保持「标签必在词表」不变量；同标签二次登记归一化复用同一词条。
import (
	"testing"

	"webpage-video-agent/server/internal/contract"
)

func TestDeriveSeedsVocabOnColdStart(t *testing.T) {
	lib := New(t.TempDir()) // 空词表 + 空风格库（冷启动）
	in := DeriveInput{
		Name:    "墨宣木语 · 古雅",
		Delta:   "测试：宣纸米白底、浓墨衬线字，中轴对称留白",
		Subject: "测试题材",
		Tags:    &contract.StyleTags{Main: "国风"},
		Facets:  &StyleFacets{Mood: "沉稳", Tone: "低饱和暖色", Density: "疏朗"},
	}
	st, err := lib.Derive(in)
	if err != nil {
		t.Fatalf("空词表下的自定义登记被拒（应自动种词）: %v", err)
	}
	if st.Tags.Main == "" {
		t.Fatal("登记条目主标签为空")
	}
	v, err := lib.LoadVocab()
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := v.Find("国风")
	if !ok {
		t.Fatal("「国风」未自动种入词表")
	}
	if st.Tags.Main != entry.ID {
		t.Fatalf("条目主标签 %s ≠ 种入词条 id %s", st.Tags.Main, entry.ID)
	}
	// 二次同标签登记：归一化复用同一词条，不再新建
	st2, err := lib.Derive(DeriveInput{
		Name: "另一条", Delta: "测试2", Tags: &contract.StyleTags{Main: "国风"},
	})
	if err != nil {
		t.Fatalf("二次登记被拒: %v", err)
	}
	if st2.Tags.Main != st.Tags.Main {
		t.Fatalf("同标签两次登记 id 漂移: %s vs %s", st.Tags.Main, st2.Tags.Main)
	}
	if got := len(lib.VocabList()); got != 1 {
		t.Fatalf("词表应有 1 条，实际 %d", got)
	}
}

// TestDeriveLocalStaysOutOfLibrary 方案乙（2026-10-30）：临时条目组装成功、
// 继承基风格，但不进账本、不种词表——公用库不再吃 agent 自动登记。
func TestDeriveLocalStaysOutOfLibrary(t *testing.T) {
	lib := New(t.TempDir())
	base, err := lib.Derive(DeriveInput{
		Name: "基风格", Delta: "测试基", Tags: &contract.StyleTags{Main: "国风"},
	})
	if err != nil {
		t.Fatalf("基风格登记被拒: %v", err)
	}
	vocabBefore := len(lib.VocabList())
	loc, err := lib.DeriveLocal(DeriveInput{
		BaseID: base.ID, Name: "本片临时款", Delta: "更哑光、密度更低",
	})
	if err != nil {
		t.Fatalf("临时条目组装被拒: %v", err)
	}
	if loc.ID == "" || loc.ID == base.ID {
		t.Fatalf("临时条目 id 异常: %q", loc.ID)
	}
	if loc.Tags.Main != base.Tags.Main {
		t.Fatalf("衍生未继承主标签: %s ≠ %s", loc.Tags.Main, base.Tags.Main)
	}
	if _, ok := lib.Get(loc.ID); ok {
		t.Fatal("临时条目进了账本（应只在 run 内）")
	}
	if n := len(lib.VocabList()); n != vocabBefore {
		t.Fatalf("临时条目动了词表: %d → %d", vocabBefore, n)
	}
	// 自定义（无基风格）允许词表外标签：条目不进库，词表纪律与它无关
	loc2, err := lib.DeriveLocal(DeriveInput{
		Name: "自造款", Delta: "测试", Tags: &contract.StyleTags{Main: "词表外标签"},
	})
	if err != nil {
		t.Fatalf("词表外标签的自定义临时条目被拒: %v", err)
	}
	if loc2.Tags.Main != "词表外标签" {
		t.Fatalf("自定义主标签被改写: %s", loc2.Tags.Main)
	}
	// BriefFrom：临时条目交卷落简报的唯一通道
	b := lib.BriefFrom(loc, nil)
	if b.EntryID != loc.ID || b.EntryName != loc.Name {
		t.Fatalf("BriefFrom 条目信息不对: %+v", b)
	}
}
