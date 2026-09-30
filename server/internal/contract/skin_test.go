package contract

import (
	"strings"
	"testing"
)

func goodSkin() *Skin {
	return &Skin{
		Name: "黑金史诗",
		Slots: SkinSlots{
			BG: "#0D0A07", TXT: "#E8DCC0", Accent: "#C9A227",
			Font: "'SimSun','Microsoft YaHei',serif", Radius: 2, BorderW: 1,
		},
		CSS: ".skin-paper{background-image:radial-gradient(ellipse at 50% 0%,rgba(201,162,39,.12) 0%,transparent 55%);}",
	}
}

func TestValidateSkinOK(t *testing.T) {
	if err := ValidateSkin(goodSkin()); err != nil {
		t.Fatalf("合格皮肤被拒: %v", err)
	}
	if err := ValidateSkin(nil); err != nil {
		t.Fatalf("nil 皮肤（走注册表情形）应放行: %v", err)
	}
}

func TestValidateSkinSlots(t *testing.T) {
	for name, mutate := range map[string]func(*Skin){
		"缺槽位":     func(s *Skin) { s.Slots.TXT = "" },
		"非hex强调色": func(s *Skin) { s.Slots.Accent = "gold" },
		"无中文字体":   func(s *Skin) { s.Slots.Font = "'Helvetica',sans-serif" },
		"radius超范围": func(s *Skin) { s.Slots.Radius = 99 },
	} {
		sk := goodSkin()
		mutate(sk)
		if err := ValidateSkin(sk); err == nil {
			t.Fatalf("%s 应报错", name)
		}
	}
}

func TestValidateSkinContrast(t *testing.T) {
	sk := goodSkin()
	sk.Slots.TXT = "#2A2118" // 深字深底：过不了 3:1
	if err := ValidateSkin(sk); err == nil || !strings.Contains(err.Error(), "对比度") {
		t.Fatalf("低对比应报「对比度」: %v", err)
	}
	if r := ContrastRatio("#FFFFFF", "#000000"); r < 20.9 || r > 21.1 {
		t.Fatalf("白黑对比度应为 21，得到 %.2f", r)
	}
	if r := ContrastRatio("#D8CFC6", "#120A16"); r < 10 {
		t.Fatalf("米白/暗紫对比度异常偏低: %.2f", r)
	}
}

func TestValidateSkinCSSBans(t *testing.T) {
	for _, ban := range []string{
		"url(https://x)", "@import 'a.css';", "<script>", "animation:spin 2s;",
		"transition:opacity .3s;", "position:fixed;", "width:50vw;",
	} {
		sk := goodSkin()
		sk.CSS = ".skin-paper{" + ban + "}"
		if err := ValidateSkin(sk); err == nil {
			t.Fatalf("禁区 %q 应被拒", ban)
		}
	}
}
