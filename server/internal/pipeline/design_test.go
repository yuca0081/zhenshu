package pipeline

import (
	"strings"
	"testing"

	"webpage-video-agent/server/internal/contract"
)

func designFixture() *contract.StyleSamples {
	sk := contract.Skin{
		Name:  "黑金史诗",
		Slots: contract.SkinSlots{BG: "#0D0A07", TXT: "#E8DCC0", Accent: "#C9A227", Font: "'SimSun','Microsoft YaHei',serif"},
		CSS:   ".skin-paper{background:radial-gradient(#1A1410,#0D0A07);}",
	}
	return &contract.StyleSamples{
		Direction: "黑金史诗·交界地挽歌",
		Concept:   "低饱和黑金配衬线大字——为文明兴衰的厚重感服务",
		Skins:     []contract.Skin{sk, sk},
		SkinRule:  "机制讲解段穿主皮肤（黑），史实叙事段穿副皮肤（米色）",
		Fonts: &contract.FontRoles{
			Display: contract.FontRole{Stack: "'SimHei','Microsoft YaHei',sans-serif", Weight: "900", Tracking: "0.06em"},
			Body:    contract.FontRole{Stack: "'KaiTi','Microsoft YaHei',serif"},
			Data:    contract.FontRole{Stack: "Consolas,'Microsoft YaHei',monospace", Weight: "700"},
		},
		Motion: &contract.MotionLang{Ease: "power3.out", DurIn: 0.5, DurSettle: 0.25, Stagger: 0.08, Notes: "大字浮入，禁弹跳"},
		Device: &contract.DeviceSpec{Name: "章节门牌", Desc: "每章开场的金属铭牌", PerSeg: "seg01 亮相，此后每章复现并换章名"},
		Layouts: []contract.DesignLayout{
			{ID: "hero-title", Name: "大字海报", Desc: "主体居中偏左"},
			{ID: "data-focus", Name: "数据焦点", Desc: "大数字右置"},
		},
		AntiPatterns: []string{"大面积辉光", "右半空版式"},
	}
}

func TestDesignDocFull(t *testing.T) {
	doc := DesignDocWithBrief(designFixture(), 2, nil)
	for _, want := range []string{
		"黑金史诗·交界地挽歌",
		"低饱和黑金",               // concept
		"### 主皮肤：黑金史诗",        // skins[0]
		"### 副皮肤：黑金史诗",        // skins[1]
		"皮肤切换规则", "机制讲解段穿主皮肤", // 双皮肤规则
		"大标题/大数字 display", "0.06em", // 字体角色
		"缓动族 power3.out", "0.50s", "0.08s", // 动效档位
		"贯穿装置：章节门牌", "seg01 亮相", // 装置
		"**本段指定版式：data-focus", // segIdx=2 → layouts[(2-1)%2]=第2个
		"大面积辉光",               // 负面清单
		"hf.count",            // 动效物理提示
		"风格≠题材",               // 注入首条：语法与题材分离（库内包题材元素入画的回归钉）
		"画什么**永远由本段画面提示说了算", // 主体跟 visual_brief，版式示例物不是规定物
		"品牌字样", // 原片名/编号/Logo 类字串禁入画（ISOPOLIS 事故）
	} {
		if !strings.Contains(doc, want) {
			t.Fatalf("DesignDoc 缺 %q\n---\n%s", want, doc)
		}
	}
}

func TestDesignDocSegRotation(t *testing.T) {
	ss := designFixture()
	if !strings.Contains(DesignDocWithBrief(ss, 1, nil), "本段指定版式：hero-title") {
		t.Fatalf("segIdx=1 应指派第 1 个版式")
	}
	if !strings.Contains(DesignDocWithBrief(ss, 3, nil), "本段指定版式：hero-title") {
		t.Fatalf("segIdx=3 应轮换回第 1 个版式")
	}
}

// 库内条目（StyleID 锚定）的 device.per_seg 是对原视频分镜写的，段号与本片错位，
// 注入等于误导——只注装置机制，逐段变奏由各段对画面提示自行发挥；本片自己的设计
// 系统（无 StyleID）才逐段指派。
func TestDesignDocLibrarySkipsPerSeg(t *testing.T) {
	ss := designFixture()
	ss.StyleID = "vid-iso-city"
	doc := DesignDocWithBrief(ss, 2, nil)
	if strings.Contains(doc, "seg01 亮相") {
		t.Fatalf("库条目 per_seg 不应注入（原视频段号与本片错位）：%s", doc)
	}
	if !strings.Contains(doc, "贯穿装置：章节门牌") || !strings.Contains(doc, "每章开场的金属铭牌") {
		t.Fatalf("库条目仍应注入装置机制：%s", doc)
	}
	if !strings.Contains(doc, "样张与原片真迹同理") {
		t.Fatalf("风格≠题材块应含样张/真迹图片口径：%s", doc)
	}
}

func TestDesignDocLegacy(t *testing.T) {
	// 库内老包/旧项目：只有 direction+skin，渲染不炸、只出已有部分
	ss := &contract.StyleSamples{
		Direction: "旧方向",
		Skin: &contract.Skin{
			Name:  "旧皮肤",
			Slots: contract.SkinSlots{BG: "#111111", TXT: "#EEEEEE", Accent: "#FF0000", Font: "'SimHei',sans-serif"},
		},
	}
	doc := DesignDocWithBrief(ss, 0, nil)
	if !strings.Contains(doc, "旧方向") || !strings.Contains(doc, "旧皮肤") {
		t.Fatalf("legacy 渲染缺方向/皮肤：%s", doc)
	}
	if strings.Contains(doc, "本段指定版式") || strings.Contains(doc, "贯穿装置") {
		t.Fatalf("legacy 不应渲染设计系统块：%s", doc)
	}
}

func TestDesignVersionStableAndSensitive(t *testing.T) {
	ss := designFixture()
	v1 := designVersion(ss)
	if v1 != designVersion(ss) {
		t.Fatalf("同内容版本指纹应稳定")
	}
	ss.Motion.Ease = "back.out(1.7)"
	if v1 == designVersion(ss) {
		t.Fatalf("设计变更后版本指纹必须变化（段级过期的根基）")
	}
}

// 叠层声明（DesignLayout.Layered）注入：声明了的版式要渲染免检属性的使用规则
// （指定版式行 + 版式清单行）；没声明的不得出现声明行——防止其他段被教坏乱贴。
func TestDesignDocLayeredLayout(t *testing.T) {
	ss := designFixture()
	if strings.Contains(DesignDocWithBrief(ss, 1, nil), "声明叠层") {
		t.Fatalf("未声明叠层的版式不应出现声明行")
	}
	ss.Layouts[0].Layered = "大字衬底词压正文卡：衬底 20% 透明度、正文卡带实底，两层对比拉开"
	doc := DesignDocWithBrief(ss, 1, nil)
	for _, want := range []string{
		"本段指定版式：hero-title",
		"本段指定版式声明叠层：大字衬底词压正文卡",
		"data-layout-allow-overlap", // 免检属性规范随声明行下发
		"- hero-title（大字海报）：主体居中偏左\n  - 声明叠层：大字衬底词",
	} {
		if !strings.Contains(doc, want) {
			t.Fatalf("DesignDoc 缺 %q\n---\n%s", want, doc)
		}
	}
}

// 转场令牌映射：设计系统 motion.transition 的现场发明措辞 → 机械令牌。
// 缺省 cut（段间节奏交给场景编排）；黑场/闪黑类随 D5 退役一律 cut；
// 明说叠化/交叉→xfade；招牌转场（措辞自由）一律 cut——机械层不越权实现手法本体。
func TestTransitionTokenOf(t *testing.T) {
	cases := []struct {
		transition string
		want       string
	}{
		{"", "cut"},
		{"招牌转场：上段卡片旋出、下段同位旋入（场景内编排）", "cut"},
		{"段尾黑场一瞬（片尾挽歌气质，论证见 notes）", "cut"}, // D5：黑场类退役
		{"闪黑切换，新闻纪事感", "cut"},               // D5：同上
		{"相邻段交叉叠化，呼吸感", "xfade"},
		{"淡入淡出", "xfade"},
		{"每段首尾元素递进推入", "cut"},
		{"禁黑场翻页，全片蓝光扫屏换景（场景内实现）", "cut"}, // 否定词不算数
		{"不用黑场，也不用叠化，硬切带光效", "cut"},      // 多关键词全被否定
		{"叠化太软，禁叠化，改用闪黑硬转", "xfade"},     // 首个「叠化」未被否定（太软不在否定词表）→ xfade
	}
	for _, c := range cases {
		ss := designFixture()
		ss.Motion.Transition = c.transition
		if got := transitionTokenOf(ss); got != c.want {
			t.Fatalf("transition=%q 应映射 %s，得到 %s", c.transition, c.want, got)
		}
	}
	if got := transitionTokenOf(nil); got != "cut" {
		t.Fatalf("nil 设计应 cut，得到 %s", got)
	}
	noMotion := *designFixture()
	noMotion.Motion = nil
	if got := transitionTokenOf(&noMotion); got != "cut" {
		t.Fatalf("无 motion 应 cut，得到 %s", got)
	}
}

// clampBoundaryFades 两头对称 + 否定感知（2026-10-21 巡检中危#5 回归）：
// Entry 侧「整场淡入」同样要钳（接缝闸首帧禁黑场——只钳收尾侧等于把打回整链
// 刹车的路径放回来）；「叠化过渡、不用黑场」里的黑场被否定，是合法叠化，不钳。
func TestClampBoundaryFades(t *testing.T) {
	ss := designFixture()
	ss.EntryExit = &contract.EntryExitSpec{
		Entry: "整场淡入纯黑起幕，2 秒后主体浮现",
		Exit:  "定格收尾，留下的元素归位停稳",
	}
	ss.Motion = &contract.MotionLang{
		Exits:      "该退的元素旁白收尾前退完",
		Transition: "相邻段交叉叠化，不用黑场",
	}
	if n := clampBoundaryFades(ss); n != 1 {
		t.Fatalf("应只钳 Entry 1 处，实际 %d", n)
	}
	if !strings.Contains(ss.EntryExit.Entry, "首帧即景") {
		t.Fatalf("Entry 未被钳: %s", ss.EntryExit.Entry)
	}
	if !strings.Contains(ss.EntryExit.Exit, "定格收尾") {
		t.Fatalf("干净的 Exit 不该被动: %s", ss.EntryExit.Exit)
	}
	if !strings.Contains(ss.Motion.Transition, "叠化") {
		t.Fatalf("被否定的黑场不该连累合法叠化: %s", ss.Motion.Transition)
	}

	// 老伤回归：收尾侧黑场照旧要钳（87a97af 的原始案例）
	ss2 := designFixture()
	ss2.EntryExit = &contract.EntryExitSpec{Exit: "整场画面随旁白淡出归黑"}
	ss2.Motion = &contract.MotionLang{Exits: "整场淡出收黑", Transition: "闪黑翻页"}
	if n := clampBoundaryFades(ss2); n != 3 {
		t.Fatalf("Exit/Exits/Transition 三处应全钳，实际 %d", n)
	}
}
