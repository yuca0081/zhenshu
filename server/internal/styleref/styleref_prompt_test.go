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
	p := synthesizeUserPrompt("城市宣传片", "", videoFeatures{DurationS: 60, SpanS: [2]float64{0, 60}, NShots: 8}, "帧0: 主体居中", false)
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
	if strings.Contains(p, "原视频直读") {
		t.Fatalf("纯帧路径不该出现视频直读章节:\n%s", p)
	}
}

// 视频直读路径（PUBLIC_BASE_URL 配置后）：作业必须声明动效/运镜以视频实测为准，
// 颜色/构图仍钉死在测得事实——视频是时间维度的证据源，不是全盘放飞的许可。
func TestSynthesizePromptVideoDirectRead(t *testing.T) {
	p := synthesizeUserPrompt("合集段", "", videoFeatures{DurationS: 30, SpanS: [2]float64{0, 30}, NShots: 4}, "帧0: 主体居中", true)
	for _, want := range []string{
		"原视频直读",                  // 视频附随声明
		"**以视频实测为准**",            // motion/运镜证据源切换
		"颜色仍以测得色板 hex 为准",        // 颜色不随视频偏色漂移
		"以附上的原视频实测为准（你能直接看到运动）", // motion 字段推断源切换
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("带视频作业缺 %q\n---\n%s", want, p)
		}
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
