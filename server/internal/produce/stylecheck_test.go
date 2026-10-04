package produce

import (
	"strings"
	"testing"
)

// 风格考卷的两条判据钉死：真迹只当气质参照（内容不同是应该的），题材物件入画是
// 要修的 high（题材中毒）。历史事故：苏格拉底生平片套用城市等距风格，整片画出
// 城市元素——根因就是考卷「以真迹为最终准绳」把画面往原片题材推。
func TestStyleCheckBlockDetoxAndPoison(t *testing.T) {
	b := styleCheckBlock(nil, 2, 3)
	for _, want := range []string{
		"内容与真迹不同是应该的", // 真迹是另一部片，内容本来就不该像
		"题材中毒",        // 反向检查：真迹物件入画 = high，进修复轮回喂
		"风格漂移",        // 原有 medium 判据保留
		"画面提示",        // 中毒判定以本段画面提示为准绳（画面提示要画的物件不算中毒）
	} {
		if !strings.Contains(b, want) {
			t.Fatalf("styleCheckBlock 缺 %q\n---\n%s", want, b)
		}
	}
	if strings.Contains(b, "最终准绳") {
		t.Fatalf("真迹不应再是内容准绳（只当气质参照）：%s", b)
	}
}

// 真迹说明反转：只准学气质，禁模仿物件——旧文案「向真迹靠」是题材中毒的推手。
func TestKeyframeNoteDetox(t *testing.T) {
	n := keyframeNote(3)
	for _, want := range []string{"只学气质", "禁止模仿着画", "画面提示说了算"} {
		if !strings.Contains(n, want) {
			t.Fatalf("keyframeNote 缺 %q：%s", want, n)
		}
	}
	if strings.Contains(n, "向真迹靠") {
		t.Fatalf("真迹说明不应再推「向真迹靠」：%s", n)
	}
	if keyframeNote(0) != "" {
		t.Fatalf("无真迹应返回空")
	}
}
