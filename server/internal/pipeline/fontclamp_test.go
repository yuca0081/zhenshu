package pipeline

import (
	"os"
	"strings"
	"testing"
)

// 2026-10-11 夜跑实录的钉子：设计发明选了渲染端没有的字体（站酷庆科黄油体），
// skin.json 原样下发，字幕带机械装配没有「引用即声明」纪律，21 个字幕文件被
// 字体硬门一票全否。LockDesign 落盘前必须把花名过滤成渲染端可用的系统字体。
func TestClampFontStack(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"站酷花名过滤掉留雅黑", "'Zcool QingKe HuangYou','Microsoft YaHei',sans-serif", "'Microsoft YaHei',sans-serif"},
		{"思源过滤留黑体", "'Source Han Serif SC','SimHei',serif", "'SimHei',serif"},
		{"干净栈原样", "'SimHei','Microsoft YaHei',sans-serif", "'SimHei','Microsoft YaHei',sans-serif"},
		{"全花名回退雅黑", "'Zcool QingKe HuangYou','FZLanTingHei'", "'Microsoft YaHei',sans-serif"},
		{"去重保序", "Arial, 'arial', Georgia, sans-serif", "'Arial','Georgia',sans-serif"},
		{"空栈回退雅黑", "", "'Microsoft YaHei',sans-serif"},
	}
	for _, c := range cases {
		if got := clampFontStack(c.in); got != c.want {
			t.Errorf("%s: clampFontStack(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// LockDesign 落盘的 skin.json 是字幕字体源：带花名的样张确认后，落盘产物必须干净。
func TestLockDesignClampsFonts(t *testing.T) {
	p := NewProject(t.TempDir(), "clamp1")
	ss := designFixture()
	ss.Skins[0].Slots.Font = "'Zcool QingKe HuangYou','Microsoft YaHei',sans-serif"
	ss.Fonts.Display.Stack = "'Zcool QingKe HuangYou','SimHei',sans-serif"
	ss.Confirmed = true
	if err := LockDesign(p, ss); err != nil {
		t.Fatal(err)
	}
	skinJSON := mustRead(t, p.Artifact("style/skin.json"))
	if strings.Contains(skinJSON, "Zcool") {
		t.Errorf("skin.json 仍含渲染端不可用字体：\n%s", skinJSON)
	}
	if !strings.Contains(skinJSON, "'Microsoft YaHei',sans-serif") {
		t.Errorf("skin.json 字体栈不是预期的钳制结果：\n%s", skinJSON)
	}
	designMD := mustRead(t, p.Artifact("style/design.md"))
	if strings.Contains(designMD, "Zcool") {
		t.Errorf("design.md 仍含渲染端不可用字体")
	}
	if !ss.Confirmed {
		t.Errorf("钳制不得动 confirmed 标志")
	}
	// 幂等：对已干净的栈再钳一次，designVersion 不得漂移
	v1 := designVersion(ss)
	if clampDesignFonts(ss) {
		t.Errorf("已干净的栈二次钳制不应有改动")
	}
	if designVersion(ss) != v1 {
		t.Errorf("二次钳制后 designVersion 漂移")
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
