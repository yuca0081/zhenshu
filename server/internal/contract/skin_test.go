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
		"缺槽位":       func(s *Skin) { s.Slots.TXT = "" },
		"非hex强调色":   func(s *Skin) { s.Slots.Accent = "gold" },
		"无中文字体":     func(s *Skin) { s.Slots.Font = "'Helvetica',sans-serif" },
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

// ── v2 设计系统（笼子二）三档校验 ─────────────────────────────────

func goodDesign() *StyleSamples {
	return &StyleSamples{
		Direction: "黑金史诗·交界地挽歌",
		Concept:   "低饱和黑金配衬线大字——为文明兴衰的厚重感服务",
		Samples:   []Sample{{Tag: "样张 A · 标题帧", Desc: "大字海报", HTML: "<!doctype html><html></html>"}},
		Skins:     []Skin{*goodSkin()},
		Fonts: &FontRoles{
			Display: FontRole{Stack: "'SimHei','Microsoft YaHei',sans-serif", Weight: "900", Tracking: "0.06em"},
			Body:    FontRole{Stack: "'KaiTi','Microsoft YaHei',serif", Weight: "400"},
			Data:    FontRole{Stack: "Consolas,'Microsoft YaHei',monospace", Weight: "700"},
		},
		Motion: &MotionLang{Ease: "power3.out", DurIn: 0.5, DurSettle: 0.25, Stagger: 0.08, Notes: "大字浮入，禁弹跳",
			Entrances: []MotionMove{
				{Name: "逐行点亮", How: "opacity 0→1 + y:14→0 逐个 stagger", UseFor: "标题/面板组"},
				{Name: "滑入揭示", How: "x:-60→0 配 power2.inOut", UseFor: "分栏主画面"},
				{Name: "字卡打字机", How: "按字符 span 化逐字点亮", UseFor: "等宽日志"},
			},
			Rhythm:     "每 3–5 秒一个可见事件，旁白信息点必有画面响应",
			Exits:      "旁白收尾前 0.3s 淡出，禁拖尾",
			Camera:     "10 秒内 scale≥1.08 或位移≥画面宽 5%",
			Transition: "章节门牌翻转切入，禁无论证黑场",
		},
		Device:    &DeviceSpec{Name: "章节门牌", Desc: "每章开场的金属铭牌", PerSeg: "seg01 亮相，此后每章复现并换章名"},
		EntryExit: &EntryExitSpec{Entry: "金属铭牌翻起显影 0.4s", Exit: "铭牌合拢收场", EntryDur: 0.4},
	}
}

func TestValidateDesignDraftOK(t *testing.T) {
	if err := ValidateStyleSamplesDraft(goodDesign()); err != nil {
		t.Fatalf("完整设计系统被拒: %v", err)
	}
	// 旧落法（单 skin 字段）+ 完整设计块也应通过
	ss := goodDesign()
	ss.Skins = nil
	ss.Skin = goodSkin()
	if err := ValidateStyleSamplesDraft(ss); err != nil {
		t.Fatalf("旧 skin 字段读兼容失败: %v", err)
	}
}

func TestValidateDesignDraftMissingBlocks(t *testing.T) {
	for name, mutate := range map[string]func(*StyleSamples){
		"缺concept":       func(s *StyleSamples) { s.Concept = "" },
		"缺fonts":         func(s *StyleSamples) { s.Fonts = nil },
		"缺motion":        func(s *StyleSamples) { s.Motion = nil },
		"缺device":        func(s *StyleSamples) { s.Device = nil },
		"device缺per_seg": func(s *StyleSamples) { s.Device.PerSeg = "" },
		"motion缺notes":   func(s *StyleSamples) { s.Motion.Notes = "" },
		"dur_in越界":       func(s *StyleSamples) { s.Motion.DurIn = 3 },
		"stagger越界":      func(s *StyleSamples) { s.Motion.Stagger = 0.9 },
		"display无中文字体":   func(s *StyleSamples) { s.Fonts.Display.Stack = "'Helvetica',sans-serif" },
		"data无中文回退":      func(s *StyleSamples) { s.Fonts.Data.Stack = "Consolas,monospace" },
		"双皮肤无切换规则": func(s *StyleSamples) {
			s.Skins = append(s.Skins, *goodSkin())
		},
		"皮肤超两套": func(s *StyleSamples) {
			s.Skins = append(s.Skins, *goodSkin(), *goodSkin())
		},
		"负面清单超8条": func(s *StyleSamples) {
			for i := 0; i < 9; i++ {
				s.AntiPatterns = append(s.AntiPatterns, "俗手")
			}
		},
		"版式只有1个": func(s *StyleSamples) {
			s.Layouts = []DesignLayout{{ID: "a", Name: "A", Desc: "唯一版式"}}
		},
	} {
		ss := goodDesign()
		mutate(ss)
		if err := ValidateStyleSamplesDraft(ss); err == nil {
			t.Fatalf("%s 应被拒", name)
		}
	}
}

func TestValidateDesignBaseLenient(t *testing.T) {
	// 宽松档（落定/库内老包）：只有 direction+samples+skin，无设计块也放行——兜底路径不被升级堵死。
	ss := &StyleSamples{
		Direction: "旧项目方向",
		Samples:   []Sample{{Tag: "A", Desc: "d", HTML: "<html></html>"}},
		Skin:      goodSkin(),
		Confirmed: true,
	}
	if err := ValidateStyleSamples(ss); err != nil {
		t.Fatalf("宽松档不应要求设计块: %v", err)
	}
	// 但缺确认门仍然拒
	ss.Confirmed = false
	if err := ValidateStyleSamples(ss); err == nil || !strings.Contains(err.Error(), "确认") {
		t.Fatalf("宽松档确认门失效: %v", err)
	}
	// 皮肤彻底缺失（新旧字段都空）必须拒
	ss.Confirmed = true
	ss.Skin = nil
	if err := ValidateStyleSamples(ss); err == nil {
		t.Fatalf("无皮肤应被拒")
	}
}

func TestEffectiveSkins(t *testing.T) {
	ss := goodDesign()
	if n := len(ss.EffectiveSkins()); n != 1 {
		t.Fatalf("新字段应返回 1 套，得到 %d", n)
	}
	ss.Skins = nil
	ss.Skin = goodSkin()
	if n := len(ss.EffectiveSkins()); n != 1 {
		t.Fatalf("旧字段读兼容应返回 1 套，得到 %d", n)
	}
	ss.Skin = nil
	if n := len(ss.EffectiveSkins()); n != 0 {
		t.Fatalf("无皮肤应返回 0，得到 %d", n)
	}
}
