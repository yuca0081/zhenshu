package pipeline

// 2026-10-23 实测回归：模型样张行内样式写 transform:translate(...) 时
// sampleBoxes 读 t[5] 越界（reTranslate 只有 4 个捕获组），panic 打挂整个
// server 进程。锁死分组语义：dx=数字1×单位1、dy=数字2×单位2，px/% 都要认。
import "testing"

func TestSampleBoxesTranslateNoPanic(t *testing.T) {
	cv := SampleCanvas{W: 1080, H: 1920}
	cases := []string{
		`<div style="position:absolute;left:10px;top:20px;width:100px;height:50px;transform:translate(30px,40%)">甲</div>`,
		`<div style="position:absolute;left:10px;top:20px;width:100px;height:50px;transform:translate(-5.5px,10px)">乙</div>`,
		`<div style="position:absolute;left:0px;top:0px;width:100px;height:50px;transform:translate(50%,2%)">丙</div>`,
	}
	for i, html := range cases {
		boxes := sampleBoxes(html, cv)
		if len(boxes) != 1 {
			t.Fatalf("case %d: boxes=%d, want 1", i, len(boxes))
		}
	}
	// 分组语义抽查：translate(30px,40%) → x=10+30=40、y=20+0.4*50=40
	b := sampleBoxes(cases[0], cv)[0]
	if b.x != 40 || b.y != 40 {
		t.Fatalf("translate offsets wrong: x=%v y=%v, want 40/40", b.x, b.y)
	}
	// % 单位按盒自身宽高换算：translate(50%,2%) → x=0+0.5*100=50、y=0+0.02*50=1
	c := sampleBoxes(cases[2], cv)[0]
	if c.x != 50 || c.y != 1 {
		t.Fatalf("percent translate wrong: x=%v y=%v, want 50/1", c.x, c.y)
	}
}
