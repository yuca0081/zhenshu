package prep

// materials_required_test.go —— 分镜工位看到的素材清单（2026-11-02）：
// 「必需」必须显式标注并排在最前面，且清单全集要能喂给认领校验的白名单。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"webpage-video-agent/server/internal/pipeline"
)

func writeMaterials(t *testing.T, dir, body string) *pipeline.Project {
	t.Helper()
	p := &pipeline.Project{ID: "t", Dir: dir}
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "materials.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// 无素材库：清单工具必须明说「不要写 shot.images」，而不是给一段空清单让人猜。
func TestImageMaterialsEmpty(t *testing.T) {
	p := &pipeline.Project{ID: "t", Dir: t.TempDir()}
	if got := loadImageMaterials(p); got != nil {
		t.Fatalf("无文件应返回 nil: %+v", got)
	}
	if s := formatImageMaterialsForPeer(nil); !strings.Contains(s, "没有上传真图素材") || !strings.Contains(s, "不需要 shot.images") {
		t.Fatalf("空清单文案: %q", s)
	}
}

// 必需项排前 + 显式标注；必需/全集两个列表要能分开喂给校验。
func TestImageMaterialsRequiredFirst(t *testing.T) {
	p := writeMaterials(t, t.TempDir(), `{"materials":[
		{"file":"assets/img/opt.jpg","title":"可选图","source":"upload"},
		{"file":"assets/img/must.jpg","title":"官方架构图","source":"upload","required":true,"note":"双层 Router/Worker"}
	]}`)
	mats := loadImageMaterials(p)
	if len(mats) != 2 {
		t.Fatalf("应读到 2 条: %+v", mats)
	}
	req, all := imageFileSets(p)
	if len(req) != 1 || req[0] != "assets/img/must.jpg" {
		t.Fatalf("必需集: %v", req)
	}
	if len(all) != 2 {
		t.Fatalf("全集: %v", all)
	}
	s := formatImageMaterialsForPeer(mats)
	if i, j := strings.Index(s, "assets/img/must.jpg"), strings.Index(s, "assets/img/opt.jpg"); i < 0 || j < 0 || i > j {
		t.Fatalf("必需项应排在前面: %q", s)
	}
	for _, want := range []string{"必需 1 条", "必需 ⚠", "双层 Router/Worker", "shot.images", "hero", "inset"} {
		if !strings.Contains(s, want) {
			t.Fatalf("清单文案缺 %q：\n%s", want, s)
		}
	}
}
