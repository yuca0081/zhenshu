package produce

// required_images_test.go —— 必需素材落地闸门（2026-11-02 事故回归）。
//
// 事故成片里唯一「用到」素材的那一处，是一张 300×97 的角标（占竖屏 1.4% 画面）。
// 闸门必须把这种形态判成没落地，否则「素材在片里」和「观众看得见」还是两回事。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/pipeline"
)

// fixture 造一个有 storyboard（含认领素材）的项目目录。
func fixture(t *testing.T, imgs []contract.ShotImage) *pipeline.Project {
	t.Helper()
	dir := t.TempDir()
	p := &pipeline.Project{ID: "t", Dir: dir}
	sb := &contract.Storyboard{Segments: []contract.Segment{{
		ID: "seg01", Idx: 1, Key: "架构", Narration: "旁白",
		Shot: &contract.ShotDirection{
			Size: "wide", Camera: "推近", Stage: "图纸", Action: "指着图",
			Cast:   []contract.ShotCast{{Who: "管家", Pose: "站"}},
			Images: imgs,
		},
	}}}
	b, _ := json.MarshalIndent(sb, "", " ")
	if err := os.MkdirAll(filepath.Join(dir, "storyboards"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "storyboards", "storyboard.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// 竖屏 1080×1920 下：300×97 的 inset 必须被拒（事故形态），700×520 必须放过。
func TestRequiredImageCheckArea(t *testing.T) {
	p := fixture(t, []contract.ShotImage{{
		File: "assets/img/arch.jpg", Role: "inset", Present: "右上角",
	}})
	root := `<div data-composition-id="seg01" data-width="1080" data-height="1920">`

	// 事故形态：300×97 = 1.4% 面积
	tiny := root + `<img src="assets/img/arch.jpg" style="width:300px;height:97px;opacity:0;">`
	errs := requiredImageCheck(p, tiny, "seg01")
	if len(errs) == 0 {
		t.Fatal("300×97 的角标应被判为没落地")
	}
	if !strings.Contains(strings.Join(errs, "；"), "只占画面") {
		t.Fatalf("错误应指出占比不足: %v", errs)
	}

	// 正常 inset：700×520 = 17.6% 面积
	ok := root + `<img src="assets/img/arch.jpg" style="width:700px;height:520px;">`
	if errs := requiredImageCheck(p, ok, "seg01"); len(errs) != 0 {
		t.Fatalf("17.6%% 的 inset 应通过: %v", errs)
	}
}

// 缺图 / 缺显式宽高都要报：没有声明尺寸就算不出占比，渲染也没法保证不变形。
func TestRequiredImageCheckMissing(t *testing.T) {
	p := fixture(t, []contract.ShotImage{{File: "assets/img/arch.jpg", Role: "hero"}})
	root := `<div data-composition-id="seg01" data-width="1080" data-height="1920">`

	if errs := requiredImageCheck(p, root+`<div>纯几何</div>`, "seg01"); len(errs) == 0 ||
		!strings.Contains(errs[0], "没有出现在画面里") {
		t.Fatalf("缺图应报错: %v", errs)
	}
	noDim := root + `<img src="assets/img/arch.jpg">`
	if errs := requiredImageCheck(p, noDim, "seg01"); len(errs) == 0 ||
		!strings.Contains(errs[0], "width/height") {
		t.Fatalf("缺显式宽高应报错: %v", errs)
	}
}

// hero 的门槛比 inset 高：同样尺寸，hero 不过、inset 过。
func TestRequiredImageCheckHeroStricter(t *testing.T) {
	root := `<div data-composition-id="seg01" data-width="1080" data-height="1920">`
	img := root + `<img src="assets/img/arch.jpg" style="width:700px;height:520px;">` // 17.6%

	hero := fixture(t, []contract.ShotImage{{File: "assets/img/arch.jpg", Role: "hero"}})
	if errs := requiredImageCheck(hero, img, "seg01"); len(errs) == 0 {
		t.Fatal("hero 17.6%% 不够 25%%，应报错")
	}
	inset := fixture(t, []contract.ShotImage{{File: "assets/img/arch.jpg", Role: "inset"}})
	if errs := requiredImageCheck(inset, img, "seg01"); len(errs) != 0 {
		t.Fatalf("inset 17.6%% 应通过: %v", errs)
	}
}

// 宽扁素材（官方架构图/表格截图 3:1）在竖屏下面积占比够不到 hero 线，但铺满宽度
// 就是主画面——并列判据必须放过，否则模型会在「选 hero 又永远不达标」里死循环。
func TestRequiredImageCheckWideStripAsHero(t *testing.T) {
	p := fixture(t, []contract.ShotImage{{File: "assets/img/arch.jpg", Role: "hero"}})
	root := `<div data-composition-id="seg01" data-width="1080" data-height="1920">`
	// 1000×250：铺满宽度（92.6%）但面积只有 12.1%
	wide := root + `<img src="assets/img/arch.jpg" style="width:1000px;height:250px;">`
	if errs := requiredImageCheck(p, wide, "seg01"); len(errs) != 0 {
		t.Fatalf("铺满宽度的宽扁图应通过 hero: %v", errs)
	}
	// 700×250：既不够宽（64.8%）也不够面积（8.4%）→ 仍要拦
	narrow := root + `<img src="assets/img/arch.jpg" style="width:700px;height:250px;">`
	if errs := requiredImageCheck(p, narrow, "seg01"); len(errs) == 0 {
		t.Fatal("又不够宽又不够面积的 hero 应被拦")
	}
}

// 没认领素材的段（老项目/纯几何段）闸门必须全静默。
func TestRequiredImageCheckSilent(t *testing.T) {
	p := fixture(t, nil)
	if errs := requiredImageCheck(p, `<div data-width="1080" data-height="1920"></div>`, "seg01"); len(errs) != 0 {
		t.Fatalf("无素材段应静默: %v", errs)
	}
}

// 必需素材的提示块必须是硬约束措辞，不能留「没有合适的就不用」的出口。
func TestRequiredMaterialsNoteHardWording(t *testing.T) {
	note := requiredMaterialsNote([]RequiredMaterial{{
		Material: Material{File: "assets/img/arch.jpg", Title: "官方架构图", Note: "双层 Router/Worker"},
		Role:     "hero", At: 0.6, Hold: 3.0, Present: "整幅横铺",
	}})
	for _, want := range []string{"本段必需素材", "必须出现", "≥ 画面 25%", "0.6s 入场", "至少停留 3.0s", "整幅横铺", "width/height",
		// 2026-11-03「声明达标但被遮罩盖住」事故：必须写明可见面积口径与禁叠字代证
		"没被盖住的部分", "遮罩", "叠字"} {
		if !strings.Contains(note, want) {
			t.Fatalf("必需素材块缺 %q：\n%s", want, note)
		}
	}
	if strings.Contains(note, "一张都不用") {
		t.Fatal("必需素材块不能保留「一张都不用」的出口")
	}
}

// 分镜认领的素材必须进本段素材全集（视觉副驾细读要用），已认领的不再重复走可选语气。
func TestMaterialsForShotMerges(t *testing.T) {
	p := fixture(t, []contract.ShotImage{{File: "assets/img/arch.jpg", Role: "hero"}})
	seg := &contract.Segment{ID: "seg01", Shot: &contract.ShotDirection{
		Images: []contract.ShotImage{{File: "assets/img/arch.jpg", Role: "hero"}},
	}}
	got := materialsForShot(p, seg)
	if len(got) != 1 || got[0].File != "assets/img/arch.jpg" {
		t.Fatalf("分镜认领的素材应进本段全集: %+v", got)
	}
	req := requiredMaterialsForSeg(p, seg)
	if len(req) != 1 || req[0].Role != "hero" {
		t.Fatalf("必需素材应带角色: %+v", req)
	}
	if opt := optionalMaterials(got, req); len(opt) != 0 {
		t.Fatalf("已认领的素材不应再按可选注入: %+v", opt)
	}
}
