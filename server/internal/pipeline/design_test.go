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
