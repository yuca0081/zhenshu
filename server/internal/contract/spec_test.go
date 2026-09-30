package contract

import (
	"strings"
	"testing"
)

func TestValidateSpec16x9(t *testing.T) {
	cv := CanvasFor("16:9")
	if cv.W != 1920 || cv.H != 1080 || cv.CapTop != 896 {
		t.Fatalf("16:9 画幅几何不对: %+v", cv)
	}
	s := &CompSpec{Elements: []SpecElement{
		{Kind: "title", Y: 200, Text: "为什么猫摔不死"},
		{Kind: "note", X: 300, Y: 400, Text: "翻正反射", Bg: "butter"},
		{Kind: "disc", Cx: 1200, Cy: 500, R: 120, Bg: "mint"},
	}}
	if errs := ValidateSpec(s, 20, cv); len(errs) != 0 {
		t.Fatalf("合法布局被拒: %v", errs)
	}
	// 越界（9:16 坐标放进 16:9 画布）
	bad := &CompSpec{Elements: []SpecElement{
		{Kind: "note", X: 1000, Y: 1400, Text: "越界便签"},
		{Kind: "label", X: 300, Y: 400, Text: "标注"},
		{Kind: "big", X: 500, Y: 600, Text: "100"},
	}}
	errs := ValidateSpec(bad, 20, cv)
	if len(errs) != 1 || !strings.Contains(errs[0], "越出安全区") {
		t.Fatalf("应检出 1 处越界，得到: %v", errs)
	}
}

func TestValidateSpec9x16(t *testing.T) {
	cv := CanvasFor("9:16")
	if cv.W != 1080 || cv.H != 1920 || cv.CapTop != 1594 {
		t.Fatalf("9:16 画幅几何不对: %+v", cv)
	}
	// 纵向铺排合法
	s := &CompSpec{Elements: []SpecElement{
		{Kind: "title", Y: 260, Text: "三分钟看懂"},
		{Kind: "note", X: 160, Y: 600, Text: "第一层", Bg: "sky"},
		{Kind: "note", X: 160, Y: 900, Text: "第二层", Bg: "butter"},
		{Kind: "arrow", X1: 400, Y1: 700, X2: 400, Y2: 880},
	}}
	if errs := ValidateSpec(s, 30, cv); len(errs) != 0 {
		t.Fatalf("竖屏合法布局被拒: %v", errs)
	}
	// 16:9 的横向坐标在竖屏越界
	bad := &CompSpec{Elements: []SpecElement{
		{Kind: "note", X: 1200, Y: 400, Text: "横屏便签"},
		{Kind: "label", X: 200, Y: 700, Text: "标注"},
		{Kind: "big", X: 300, Y: 1000, Text: "42"},
	}}
	errs := ValidateSpec(bad, 30, cv)
	if len(errs) == 0 || !strings.Contains(errs[0], "越出安全区") {
		t.Fatalf("竖屏应检出越界，得到: %v", errs)
	}
}

func TestSanitizeSpecClamps(t *testing.T) {
	cv := CanvasFor("9:16")
	s := &CompSpec{Elements: []SpecElement{
		{Kind: "note", X: 5000, Y: -300, Text: "飞出画布", Reveal: 3},
		{Kind: "disc", Cx: 9000, Cy: 9000, R: 100, Reveal: 5},
		{Kind: "label", X: 200, Y: 300, Text: "正常", Reveal: 1},
	}}
	_ = SanitizeSpec(s, 10, cv)
	if s.Elements[0].X > cv.X1 || s.Elements[1].Cx > cv.X1 {
		t.Fatalf("清洗后仍越界: %+v %+v", s.Elements[0], s.Elements[1])
	}
}

func TestValidateSpecPanelChip(t *testing.T) {
	cv := CanvasFor("16:9")
	// panel + chip 合法布局
	s := &CompSpec{Elements: []SpecElement{
		{Kind: "panel", X: 300, Y: 300, W: 620, H: 380, Title: "必须保留的部分",
			Text: "厚描边卡片承载说明文字", Bg: "green", Reveal: 1},
		{Kind: "chip", X: 1100, Y: 340, Text: "虚拟机", Bg: "navy", Reveal: 3},
		{Kind: "arrow", X1: 1050, Y1: 300, X2: 1180, Y2: 380, Reveal: 4},
	}}
	if errs := ValidateSpec(s, 20, cv); len(errs) != 0 {
		t.Fatalf("panel/chip 合法布局被拒: %v", errs)
	}
	// panel 缺 title/text；title 超长；chip 缺 text；w 超范围
	bad := &CompSpec{Elements: []SpecElement{
		{Kind: "panel", X: 300, Y: 300, W: 2000, Reveal: 1},
		{Kind: "chip", X: 1100, Y: 340, Reveal: 2},
		{Kind: "label", X: 300, Y: 800, Text: "占位", Reveal: 3},
	}}
	errs := ValidateSpec(bad, 20, cv)
	joined := strings.Join(errs, "\n")
	for _, want := range []string{"缺 title/text", "超范围 240–1400", "缺 text"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("应包含 %q，得到: %v", want, errs)
		}
	}
}

func TestValidateSpecDiagramKinds(t *testing.T) {
	cv := CanvasFor("16:9")
	// zone 垫底 + timeline/bracket/strip/barrow/table 合法布局；
	// 卡片在 zone 内部不算重叠（zone 豁免）
	s := &CompSpec{Elements: []SpecElement{
		{Kind: "zone", X: 1060, Y: 260, W: 700, H: 560, Bg: "mint", Reveal: 0},
		{Kind: "timeline", X: 1160, Y: 300, Nodes: []string{"初始化服务", "启动操作系统", "执行命令"},
			Color: "green", Reveal: 1},
		{Kind: "bracket", X: 1540, Y: 300, H: 420, Text: "多次重复执行", Color: "green", Reveal: 4},
		{Kind: "strip", X: 300, Y: 300, N: 6, Color: "sky", Text: "512维", Reveal: 2},
		{Kind: "barrow", X: 420, Y: 450, W: 300, H: 100, Color: "sky", Reveal: 3},
		{Kind: "table", X: 300, Y: 620, W: 560,
			Rows: [][]string{{"0.21", "-0.11"}, {"0.73", "-0.23"}, {"0.56", "0.22"}}, Reveal: 5},
	}}
	if errs := ValidateSpec(s, 30, cv); len(errs) != 0 {
		t.Fatalf("图解积木合法布局被拒: %v", errs)
	}
	// 违规：timeline 节点超 6；table 行列不一致；strip n 超范围
	bad := &CompSpec{Elements: []SpecElement{
		{Kind: "timeline", X: 300, Y: 200,
			Nodes: []string{"一", "二", "三", "四", "五", "六", "七"}, Reveal: 1},
		{Kind: "table", X: 300, Y: 400, Rows: [][]string{{"a", "b"}, {"c"}}, Reveal: 2},
		{Kind: "strip", X: 300, Y: 600, N: 9, Reveal: 3},
	}}
	errs := ValidateSpec(bad, 30, cv)
	joined := strings.Join(errs, "\n")
	for _, want := range []string{"nodes 7 条不在 2–6", "列数须一致", "n 9 不在 2–8"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("应包含 %q，得到: %v", want, errs)
		}
	}
}

func TestValidateSpecIconChart(t *testing.T) {
	cv := CanvasFor("16:9")
	// icon + 图表合法布局
	s := &CompSpec{Elements: []SpecElement{
		{Kind: "icon", X: 700, Y: 380, Size: 200, Name: "rocket", Reveal: 2},
		{Kind: "label", X: 300, Y: 250, Text: "运载火箭", Reveal: 1},
		{Kind: "chart_bar", X: 160, Y: 480, W: 520, H: 320,
			Values: []float64{12, 30, 8}, Labels: []string{"甲", "乙", "丙"}, Reveal: 5},
	}}
	if errs := ValidateSpec(s, 20, cv); len(errs) != 0 {
		t.Fatalf("icon/图表合法布局被拒: %v", errs)
	}
	// icon 缺 name；图表 values/labels 不一致 + 单值折线
	bad := &CompSpec{Elements: []SpecElement{
		{Kind: "icon", X: 700, Y: 380, Size: 200, Reveal: 1},
		{Kind: "chart_bar", X: 160, Y: 480, Values: []float64{1, 2}, Labels: []string{"a"}, Reveal: 2},
		{Kind: "chart_line", X: 160, Y: 480, Values: []float64{5}, Reveal: 3},
	}}
	errs := ValidateSpec(bad, 20, cv)
	joined := strings.Join(errs, "\n")
	for _, want := range []string{"缺 name", "labels 数 1 与 values 数 2 不一致", "数量 1 不在 3–8"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("应包含 %q，得到: %v", want, errs)
		}
	}
}

func TestValidateSpecQuoteChecklistStatDonut(t *testing.T) {
	cv := CanvasFor("16:9")
	// quote/checklist/stat/chart_donut 合法布局
	s := &CompSpec{Elements: []SpecElement{
		{Kind: "title", Y: 100, Text: "风格样张"},
		{Kind: "quote", X: 170, Y: 250, W: 940, Text: "每一种风格都是一套可复用的语言", Name: "注册表", Reveal: 2},
		{Kind: "stat", X: 640, Y: 620, W: 420, Text: "13套", Title: "注册风格包", Reveal: 6},
		{Kind: "checklist", X: 170, Y: 620, Nodes: []string{"暗底反白", "系列色", "相框质感"}, Reveal: 4},
		{Kind: "chart_donut", X: 1120, Y: 140, W: 640, H: 380,
			Values: []float64{45, 30, 25}, Labels: []string{"甲", "乙", "丙"}, Title: "占比", Reveal: 2},
	}}
	if errs := ValidateSpec(s, 20, cv); len(errs) != 0 {
		t.Fatalf("新元素合法布局被拒: %v", errs)
	}
	// 违规：quote 超长+署名超长；stat 缺 text；checklist 节点数越界+单条超长；donut values 越界
	bad := &CompSpec{Elements: []SpecElement{
		{Kind: "quote", X: 170, Y: 250, Text: "这句话远远超过了二十两个字的上限所以必须被拒绝才对", Name: "一个特别特别长的署名字", Reveal: 1},
		{Kind: "stat", X: 170, Y: 560, Title: "缺数值", Reveal: 2},
		{Kind: "checklist", X: 700, Y: 200, Nodes: []string{"一", "二", "三", "四", "五", "六"}, Reveal: 3},
		{Kind: "chart_donut", X: 1120, Y: 140, Values: []float64{1, 2, 3, 4, 5, 6}, Reveal: 4},
	}}
	errs := ValidateSpec(bad, 20, cv)
	joined := strings.Join(errs, "\n")
	for _, want := range []string{"quote", "署名", "缺 text", "nodes 6 条不在 2–5", "数量 6 不在 2–5"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("应包含 %q，得到: %v", want, errs)
		}
	}
}

func TestValidateSpecCustomAnimExitCamera(t *testing.T) {
	cv := CanvasFor("16:9")
	// custom + anim/exit/camera 合法
	s := &CompSpec{
		Camera: "zoom_in",
		Elements: []SpecElement{
			{Kind: "custom", X: 660, Y: 160, W: 600, H: 200, Text: "斜切大字板", Reveal: 0,
				Html: `<div class="band"></div>`,
				Css:  `.band{position:absolute;left:0;top:20px;width:100%;height:120px;background:#E30050;transform:skewY(-6deg);}`},
			{Kind: "title", Y: 420, Text: "画面表达力", Reveal: 2, Anim: "chars"},
			{Kind: "note", X: 300, Y: 560, Text: "讲完即退", Bg: "sky", Reveal: 3, Exit: 9, Anim: "slide"},
		},
	}
	if errs := ValidateSpec(s, 20, cv); len(errs) != 0 {
		t.Fatalf("custom/anim/exit/camera 合法布局被拒: %v", errs)
	}
	// custom 计入重叠豁免之外仍须守安全区：越界 custom 被拒
	oob := &CompSpec{Elements: []SpecElement{
		{Kind: "custom", X: 60, Y: 940, W: 600, H: 200, Text: "越界板", Html: "<b>x</b>"},
		{Kind: "label", X: 300, Y: 400, Text: "标注"},
		{Kind: "big", X: 500, Y: 600, Text: "100"},
	}}
	if errs := ValidateSpec(oob, 20, cv); len(errs) == 0 || !strings.Contains(strings.Join(errs, "\n"), "越出安全区") {
		t.Fatalf("custom 越界应被拒: %v", errs)
	}
	// 违规矩阵：禁用模式/缺字段/超个数/anim 白名单/exit 顺序/camera 词表
	bad := &CompSpec{
		Camera: "fly_by",
		Elements: []SpecElement{
			{Kind: "custom", X: 100, Y: 100, W: 600, H: 160, Reveal: 0,
				Html: `<div onclick="x()">hi</div>`},
			{Kind: "custom", X: 100, Y: 300, W: 600, H: 160, Text: "坏js", Reveal: 1, Html: "<b>x</b>",
				Css: `.b{position:fixed;top:0;}`,
				Js:  `tl.to(ID,{opacity:1,repeat:3},T);`},
			{Kind: "custom", X: 100, Y: 500, W: 600, H: 160, Text: "第三个", Reveal: 2, Html: "<b>x</b>"},
			{Kind: "custom", X: 100, Y: 700, W: 600, H: 160, Text: "第四个", Reveal: 2, Html: "<b>x</b>"},
			{Kind: "note", X: 900, Y: 300, Text: "普通", Reveal: 3, Anim: "spin", Exit: 2},
			{Kind: "label", X: 1400, Y: 600, Text: "chars 用错对象", Anim: "chars"},
			{Kind: "big", X: 500, Y: 700, Text: "100", Anim: "none"},
		},
	}
	errs := ValidateSpec(bad, 20, cv)
	joined := strings.Join(errs, "\n")
	for _, want := range []string{"camera", "onclick", "position:fixed", "repeat", "缺 text", "超上限", "anim", "chars 仅", "须晚于 reveal"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("应包含 %q，得到: %v", want, errs)
		}
	}
}

func TestSanitizeSpecCustom(t *testing.T) {
	cv := CanvasFor("16:9")
	s := &CompSpec{Elements: []SpecElement{
		{Kind: "custom", X: 5000, Y: 5000, Html: "<b>夹取</b>", Text: "板"},       // 越界 → 夹回
		{Kind: "custom", X: 100, Y: 100, Html: "", Text: "缺html"},            // 缺 html → 删
		{Kind: "unknown", X: 100, Y: 100},                                    // 未知 kind → 删
		{Kind: "note", X: 300, Y: 400, Text: "普通便签", Reveal: 5, Exit: 99}, // exit 越界 → 夹到词尾
	}}
	SanitizeSpec(s, 20, cv)
	n := 0
	for _, e := range s.Elements {
		if e.Kind == "custom" {
			n++
			if e.X < cv.X0 || e.Y < cv.Y0 {
				t.Fatalf("custom 坐标未夹回安全区: %v,%v", e.X, e.Y)
			}
			if e.W == 0 || e.H == 0 {
				t.Fatalf("custom 缺省尺寸未补: %v×%v", e.W, e.H)
			}
		}
	}
	if n != 1 {
		t.Fatalf("应剩 1 个 custom，得到 %d（%+v）", n, s.Elements)
	}
	for _, e := range s.Elements {
		if e.Kind == "note" && e.Exit != 19 {
			t.Fatalf("note exit 应夹到词数-1=19，得到 %d", e.Exit)
		}
	}
}

func TestValidateSpecRoleBg(t *testing.T) {
	cv := CanvasFor("16:9")
	// image 背景满画幅：不占安全区（坐标无意义）、不与内容元素判重叠；custom 背景同理
	s := &CompSpec{Elements: []SpecElement{
		{Kind: "image", Role: "bg", Query: "星空 银河", Source: "gen", Dim: 0.6, Blur: 4, Reveal: 0},
		{Kind: "custom", Role: "bg", Text: "氛围粒子", W: 1800, H: 1000, Reveal: 0,
			Html: `<div class="p"></div>`, Css: `.p{width:10px;height:10px;border-radius:50%;}`},
		{Kind: "title", Y: 200, Text: "背景上的大字"},
		{Kind: "note", X: 300, Y: 420, Text: "前景便签", Bg: "butter"},
		{Kind: "big", X: 500, Y: 600, Text: "42"},
	}}
	if errs := ValidateSpec(s, 20, cv); len(errs) != 0 {
		t.Fatalf("背景层合法布局被拒: %v", errs)
	}
	// 违规：role 用在不支持的 kind；image 背景超过 1 张；dim/blur 超范围
	bad := &CompSpec{Elements: []SpecElement{
		{Kind: "image", Role: "bg", Query: "星空", Dim: 1.5, Reveal: 0},
		{Kind: "image", Role: "bg", Query: "海洋", Blur: 30, Reveal: 0},
		{Kind: "note", Role: "bg", X: 300, Y: 300, Text: "便签当背景", Reveal: 1},
		{Kind: "label", X: 300, Y: 500, Text: "标注", Reveal: 2},
		{Kind: "big", X: 500, Y: 600, Text: "100", Reveal: 3},
	}}
	errs := ValidateSpec(bad, 20, cv)
	joined := strings.Join(errs, "\n")
	for _, want := range []string{"仅 image/custom 支持", "最多 1 个", "dim", "blur"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("应包含 %q，得到: %v", want, errs)
		}
	}
}

func TestSanitizeSpecKeepsBg(t *testing.T) {
	cv := CanvasFor("16:9")
	// 背景层满画幅必然与内容重叠：清洗不得删背景，也不得删内容
	s := &CompSpec{Elements: []SpecElement{
		{Kind: "custom", Role: "bg", Text: "全屏底", W: 1800, H: 1000, Html: "<b>b</b>", Reveal: 0},
		{Kind: "note", X: 300, Y: 300, Text: "内容一", Reveal: 1},
		{Kind: "label", X: 340, Y: 340, Text: "内容二重叠", Reveal: 2},
		{Kind: "big", X: 500, Y: 600, Text: "42", Reveal: 3},
	}}
	SanitizeSpec(s, 20, cv)
	hasBg := false
	for _, e := range s.Elements {
		if e.Role == "bg" {
			hasBg = true
		}
	}
	if !hasBg {
		t.Fatalf("清洗误删了背景层: %+v", s.Elements)
	}
}
