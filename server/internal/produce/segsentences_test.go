package produce

// segsentences_test.go —— segSentences 段首句定位回归：旧实现从 N1 起前缀匹配，
// seg02 起全部返回空（段审旁白节对非首段恒空，seg10 实测审片员把本段烧入字幕
// 判成上一段残留 → 不可解 → 签名刹车）。锁：任意段的旁白都要能还原出 N 编号。

import (
	"testing"

	"webpage-video-agent/server/internal/contract"
)

func segSentencesScript() *contract.Script {
	mk := func(id, text string) contract.Sentence {
		return contract.Sentence{ID: id, Text: text}
	}
	return &contract.Script{Sentences: []contract.Sentence{
		mk("N1", "你有没有想过：人是先带着说明书来的吗？"),
		mk("N2", "答案就叫存在主义。"),
		mk("N3", "代表人物是萨特和加缪。"),
		mk("N4", "那是个信仰塌了的年代。"),
		mk("N14", "这，就是存在先于本质。"),
	}}
}

func TestSegSentencesMiddleSegment(t *testing.T) {
	sc := segSentencesScript()
	got := segSentences(sc, contract.Segment{ID: "seg03", Narration: "代表人物是萨特和加缪。那是个信仰塌了的年代。"})
	if len(got) != 2 || got[0].ID != "N3" || got[1].ID != "N4" {
		t.Fatalf("中段应还原 N3,N4，实得 %v", got)
	}
}

func TestSegSentencesSingleSentence(t *testing.T) {
	sc := segSentencesScript()
	got := segSentences(sc, contract.Segment{ID: "seg10", Narration: "这，就是存在先于本质。"})
	if len(got) != 1 || got[0].ID != "N14" {
		t.Fatalf("后段应还原 N14，实得 %v", got)
	}
}

func TestSegSentencesFirstSegment(t *testing.T) {
	sc := segSentencesScript()
	got := segSentences(sc, contract.Segment{ID: "seg01", Narration: "你有没有想过：人是先带着说明书来的吗？答案就叫存在主义。"})
	if len(got) != 2 || got[0].ID != "N1" || got[1].ID != "N2" {
		t.Fatalf("首段应还原 N1,N2，实得 %v", got)
	}
}

func TestSegSentencesNoNarration(t *testing.T) {
	sc := segSentencesScript()
	if got := segSentences(sc, contract.Segment{ID: "seg09", Narration: ""}); len(got) != 0 {
		t.Fatalf("空旁白应返回空，实得 %v", got)
	}
	if got := segSentences(sc, contract.Segment{ID: "segX", Narration: "完全对不上的手改稿"}); len(got) != 0 {
		t.Fatalf("对不上应返回空（断点截断语义），实得 %v", got)
	}
}
