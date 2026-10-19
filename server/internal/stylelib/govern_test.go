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
