package produce

// audit_materials_test.go —— 段审/终审的「真图素材」装配（2026-11-03 事故回归）。
//
// 事故：素材在片里（声明 28.8%）但观众看不见（遮罩盖掉 79%、证据是叠字）。
// 落地闸门只核声明面积，判卷官要判「看不看得见」就得拿到原图与落地要求——
// 本文件锁死装配口径：逐条对应、档位换算、认领段带出、原图缩成多模态 data URL。

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/pipeline"
)

// writeMaterialPNG 在项目里落一张真图（64×48 纯色 PNG）。
func writeMaterialPNG(t *testing.T, p *pipeline.Project, rel string) {
	t.Helper()
	path := p.Artifact(rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	im := image.NewRGBA(image.Rect(0, 0, 64, 48))
	for x := 0; x < 64; x++ {
		for y := 0; y < 48; y++ {
			im.Set(x, y, color.RGBA{R: 200, G: 180, B: 120, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, im); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// segAuditMaterials 逐条对应分镜认领：hero 0.25 / inset 0.06、带认领段与原图。
func TestSegAuditMaterialsCarriesRequirementsAndOriginal(t *testing.T) {
	p := fixture(t, []contract.ShotImage{
		{File: "assets/img/hero.png", Role: "hero", At: 0.6, Hold: 3.0, Present: "幕布正中"},
		{File: "assets/img/inset.png", Role: "inset"},
	})
	writeMaterialPNG(t, p, "assets/img/hero.png")
	// inset 故意不给文件：原图读不出来时 URL 留空，由段审侧跳过图号指针
	sb, err := p.LoadStoryboard()
	if err != nil {
		t.Fatal(err)
	}
	mats, imgs := segAuditMaterials(p, &sb.Segments[0])
	if len(mats) != 2 || len(imgs) != 2 {
		t.Fatalf("装配条数 mats=%d imgs=%d，want 2/2（必须逐条对应）", len(mats), len(imgs))
	}
	if mats[0].File != "assets/img/hero.png" || mats[0].Role != "hero" || mats[0].AreaMin != 0.25 {
		t.Fatalf("hero 条目不对: %+v", mats[0])
	}
	if mats[0].SegID != "seg01" || mats[0].Present != "幕布正中" || mats[0].Hold != 3.0 {
		t.Fatalf("落地要求没带全: %+v", mats[0])
	}
	if mats[1].AreaMin != 0.06 {
		t.Fatalf("inset 面积下限 %.2f，want 0.06", mats[1].AreaMin)
	}
	if imgs[0].Path != "assets/img/hero.png" || !strings.HasPrefix(imgs[0].URL, "data:image/jpeg;base64,") {
		t.Fatalf("真图应缩成多模态 data URL: %+v", imgs[0])
	}
	if imgs[1].URL != "" {
		t.Fatalf("缺图的原图 URL 应为空（段审侧据此跳过图号指针），实得 %q", imgs[1].URL)
	}
}

// filmAuditMaterials 全片清单：每条带认领段，供终审兜底「素材进没进片」。
func TestFilmAuditMaterialsRoster(t *testing.T) {
	p := fixture(t, []contract.ShotImage{{File: "assets/img/hero.png", Role: "hero"}})
	writeMaterialPNG(t, p, "assets/img/hero.png")
	sb, err := p.LoadStoryboard()
	if err != nil {
		t.Fatal(err)
	}
	got := filmAuditMaterials(p, sb)
	if len(got) != 1 || got[0].SegID != "seg01" || got[0].AreaMin != 0.25 {
		t.Fatalf("全片清单不对: %+v", got)
	}
}
