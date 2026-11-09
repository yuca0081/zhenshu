package contract

// prep_images_test.go —— 分镜稿的「必需素材」落地校验（2026-11-02 事故回归）：
// 素材只在写作期以旁挂备注出现时模型会整段忽略，所以必需素材必须在分镜阶段就被
// 认领、且写进该段画面提示。这两条是本次修复的判据。

import (
	"strings"
	"testing"
)

func draftWithImage(file, role, scene string) *StoryboardDraft {
	return &StoryboardDraft{
		Segments: []DraftSegment{{
			ID: "seg01", Idx: 1, Key: "架构",
			Brief: VisualBrief{Scene: scene, FirstFrame: "推近", LastFrame: "停住"},
			Shot: &ShotDirection{
				Size: "wide", Camera: "推近", Stage: "图纸", Action: "指着图",
				Cast:   []ShotCast{{Who: "管家", Pose: "站"}},
				Images: []ShotImage{{File: file, Role: role, Present: "横铺"}},
			},
		}},
	}
}

// 必需素材必须被某一镜认领——事故里 10 段有 9 段整段没写 <img>，分镜阶段就该报错。
func TestValidateShotImagesRequiresClaim(t *testing.T) {
	d := draftWithImage("assets/img/a.jpg", "hero", "官方架构图摊在图纸中央")
	// 已认领：通过
	if err := ValidateShotImages(d, []string{"assets/img/a.jpg"}, []string{"assets/img/a.jpg"}); err != nil {
		t.Fatalf("已认领应通过: %v", err)
	}
	// 没认领：必须报「必需素材没有被任何段认领」
	empty := draftWithImage("assets/img/a.jpg", "hero", "官方架构图摊在图纸中央")
	empty.Segments[0].Shot.Images = nil
	err := ValidateShotImages(empty, []string{"assets/img/a.jpg"}, []string{"assets/img/a.jpg"})
	if err == nil {
		t.Fatal("必需素材没认领应报错")
	}
	if !contains(err.Error(), "必需素材") || !contains(err.Error(), "assets/img/a.jpg") {
		t.Fatalf("错误文案应点名未认领的必需素材: %v", err)
	}
}

// 认领了图但画面提示里没写它 = 写作期仍会画纯几何，必须拦。
func TestValidateShotImagesNeedsSceneMention(t *testing.T) {
	d := draftWithImage("assets/img/a.jpg", "hero", "米纸网格上一群方块各干各的")
	err := ValidateShotImages(d, nil, []string{"assets/img/a.jpg"})
	if err == nil {
		t.Fatal("画面提示没写到图应报错")
	}
	if !contains(err.Error(), "visual_brief.scene") {
		t.Fatalf("应指出场景散文缺图: %v", err)
	}
	// 写文件名也算认到
	ok := draftWithImage("assets/img/a.jpg", "hero", "把 a.jpg 整幅摊在中央")
	if err := ValidateShotImages(ok, nil, []string{"assets/img/a.jpg"}); err != nil {
		t.Fatalf("散文写了文件名应通过: %v", err)
	}
}

// 清单外的文件一律拒绝（渲染必裂图）。
func TestValidateShotImagesWhitelist(t *testing.T) {
	d := draftWithImage("assets/img/hack.jpg", "hero", "把截图摊在中央")
	err := ValidateShotImages(d, nil, []string{"assets/img/a.jpg"})
	if err == nil || !contains(err.Error(), "不在素材清单") {
		t.Fatalf("清单外引用应被拒: %v", err)
	}
}

// 角色只有 hero/inset 两档，乱填必须拦（闸门靠它算面积下限）。
func TestShotImageValidateRole(t *testing.T) {
	im := &ShotImage{File: "assets/img/a.jpg", Role: "big"}
	if err := im.Validate(); err == nil {
		t.Fatal("非法 role 应报错")
	}
	im.Role = "hero"
	if err := im.Validate(); err != nil {
		t.Fatalf("hero 应合法: %v", err)
	}
	if ImageAreaMin("hero") <= ImageAreaMin("inset") {
		t.Fatal("hero 的面积下限必须高于 inset")
	}
}

// Translate 要把素材行焊进 visual_brief 文本：写作阶段读的是这段散文。
func TestTranslateInjectsImageLine(t *testing.T) {
	d := draftWithImage("assets/img/a.jpg", "hero", "官方架构图摊在中央")
	d.Paragraphs = []DraftParagraph{{Title: "开场", Segs: []string{"seg01"}}}
	script := &Script{Sentences: []Sentence{{ID: "N1", Text: "开场白。"}}}
	d.Segments[0].NarrationRefs = "N1"
	sb := d.Translate(script, nil)
	if len(sb.Segments) != 1 {
		t.Fatalf("段数: %d", len(sb.Segments))
	}
	if !contains(sb.Segments[0].VisualBrief, "必须出现素材：assets/img/a.jpg") {
		t.Fatalf("visual_brief 应焊入素材行: %q", sb.Segments[0].VisualBrief)
	}
	if sb.Segments[0].Shot == nil || len(sb.Segments[0].Shot.Images) != 1 {
		t.Fatalf("素材应随 shot 翻译进制作契约: %+v", sb.Segments[0].Shot)
	}
	// Clone 必须深拷贝 Images（草稿与契约不共享切片）
	cp := sb.Segments[0].Shot.Clone()
	cp.Images[0].File = "changed"
	if sb.Segments[0].Shot.Images[0].File != "assets/img/a.jpg" {
		t.Fatal("Clone 应深拷贝 images")
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
