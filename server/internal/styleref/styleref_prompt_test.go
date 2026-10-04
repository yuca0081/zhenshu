package styleref

import (
	"encoding/json"
	"strings"
	"testing"
)

// 入库作业必须带「风格/题材分离」纪律：这份风格包会被用来制作题材完全不同的新片，
// 除 subject 外所有字段只准写「怎么画」，样张用抽象几何演示而非临摹原片。
// 历史事故：城市宣传片解析出的「2D等距」条目连样张带描述全是城市物件，套到
// 苏格拉底生平片上整片画出城市元素。
func TestSynthesizePromptSeparatesSubjectFromStyle(t *testing.T) {
	p := synthesizeUserPrompt("城市宣传片", "", videoFeatures{DurationS: 60, SpanS: [2]float64{0, 60}, NShots: 8}, "帧0: 主体居中")
	for _, want := range []string{
		"风格/题材分离",                   // 纪律章节
		"抽象成「色块/卡片/线条/图形」",          // 物件抽象化措辞
		"禁止临摹原片里的具体物件",              // 样张不再是原片临摹
		"题材词至多一个",                   // genre/keywords 降级为检索词
		`"subject":"原片题材一句话（≤30字）"`, // 输出形状带 subject
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("合成作业缺纪律 %q\n---\n%s", want, p)
		}
	}
	if strings.Contains(p, "气质尽量贴近附上的关键帧") {
		t.Fatalf("样张不应再要求贴近原片关键帧（题材临摹口子）:\n%s", p)
	}
}

// subject 字段从 LLM 输出解码贯通（StyleDraft）。
func TestStyleDraftSubjectDecoded(t *testing.T) {
	var d StyleDraft
	if err := json.Unmarshal([]byte(`{"direction":"等距视图","subject":"等距视角的城市宣传动画","genre":"城市","keywords":["等距"]}`), &d); err != nil {
		t.Fatal(err)
	}
	if d.Subject != "等距视角的城市宣传动画" {
		t.Fatalf("subject 未解码: %+v", d)
	}
}
