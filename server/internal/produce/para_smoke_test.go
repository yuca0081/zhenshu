package produce

// para_smoke_test.go —— 段落制生产路径冒烟（零 LLM）：沙盒处女项目上走
// BuildUnitsIfEligible（钉 para+落单元表）→ segCompositions（画面产物落帧）→
// assemble.py 单元模式（合并 wav/单元字幕/单段工程/index）→ RenderSeg →
// RunFrom(render)（单元拼接+segmap）。画面产物复用比武沙盒的 comp-para01.json。
//
//	PARA_SMOKE=1 go test ./internal/produce -run TestParaSmokeProduction -v -timeout 20m
import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"webpage-video-agent/server/internal/events"
	"webpage-video-agent/server/internal/pipeline"
)

func TestParaSmokeProduction(t *testing.T) {
	if os.Getenv("PARA_SMOKE") == "" {
		t.Skip("未指定 PARA_SMOKE=1，跳过段落制生产路径冒烟")
	}
	root, _ := filepath.Abs(filepath.Join("..", "..", ".."))
	pipeline.SetRootDir(root)
	pipeline.SetDataDir(filepath.Join(root, "data"))
	pipeline.EnsureFFmpeg()
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Minute)
	defer cancel()

	src := pipeline.NewProject(filepath.Join(root, "data"), "p20261023-110234")
	p := pipeline.NewProject(filepath.Join(root, "data", "paracontest"), "p-smoke")
	for _, rel := range []string{"project.json", "meta.json", "package.json", "audio_meta.json",
		"storyboards", "style", "scripts", "audio", "assets"} {
		if err := copyTree(filepath.Join(src.Dir, rel), filepath.Join(p.Dir, rel)); err != nil {
			t.Fatalf("拷 %s: %v", rel, err)
		}
	}
	// 比武画面产物当 para01 的「已写」场景（冒烟不烧 LLM）
	if err := copyTree(filepath.Join(root, "data", "paracontest", "projects", "p20261023-110234", "llm", "comp-para01.json"),
		filepath.Join(p.Dir, "llm", "comp-para01.json")); err != nil {
		t.Fatal(err)
	}

	// 截到段落1 的三个分镜（冒烟只跑一个单元）
	sbFull, err := p.LoadStoryboard()
	if err != nil {
		t.Fatal(err)
	}
	sb := *sbFull
	sb.Segments = sbFull.Segments[:3]
	voices, err := loadAudioMeta(p)
	if err != nil {
		t.Fatal(err)
	}
	units := BuildUnitsIfEligible(p, &sb, voices)
	if len(units) != 1 || units[0].ID != "para01" {
		t.Fatalf("应建单单元 para01: %+v", units)
	}
	if !p.UnitMode() || pipeline.ReadProjectField(p, "unit") != "para" {
		t.Fatal("处女项目应钉 para 且 UnitMode 开")
	}

	if err := segCompositions(p, "para01"); err != nil {
		t.Fatal(err)
	}
	pipeline.ClampSegmentFonts(p, "para01")
	pipeline.FixupSegmentFrame(p, "para01")
	pipeline.ClampSegmentFrame(p, "para01")

	r := &Runner{DataDir: filepath.Join(root, "data"), RootDir: root, Hub: events.NewHub()}
	if err := r.assemble(ctx, p); err != nil {
		t.Fatalf("assemble 单元模式: %v", err)
	}
	for _, f := range []string{"audio/para01.wav", ".hf-seg/para01.html", "compositions/caps/cap-para01.html", "index.html"} {
		if _, err := os.Stat(p.Artifact(f)); err != nil {
			t.Errorf("缺 %s: %v", f, err)
		}
	}
	if err := pipeline.RenderSeg(p, "para01"); err != nil {
		t.Fatalf("段落渲染: %v", err)
	}
	dur := pipeline.SegVideoDuration(p, "para01")
	if d := dur - units[0].Dur; d > 0.5 || d < -0.5 {
		t.Errorf("段落片 %.2fs 与网格 %.2fs 偏差 %.2f", dur, units[0].Dur, d)
	}
	if err := pipeline.SetProjectField(p, "transition", "para"); err != nil {
		t.Fatal(err)
	}
	if err := pipeline.RunFrom(ctx, p, "render"); err != nil {
		t.Fatalf("render 阶段: %v", err)
	}
	// 单元拼接 + segmap 断言
	tl := pipeline.LoadTimeline(p)
	if len(tl) != 1 || tl[0].ID != "para01" {
		t.Fatalf("timeline 应单单元行: %+v", tl)
	}
	b, err := os.ReadFile(p.Artifact("renders/segmap.json"))
	if err != nil {
		t.Fatal("segmap.json 未写")
	}
	var rows []pipeline.TimelineRow
	if json.Unmarshal(b, &rows) != nil || len(rows) != 3 {
		t.Fatalf("segmap 应 3 分镜行: %s", string(b))
	}
	for i, want := range units[0].Segs {
		if rows[i].ID != want.ID {
			t.Fatalf("segmap[%d] 应 %s: %+v", i, want.ID, rows[i])
		}
		if d := rows[i].Start - want.Start; d > 0.05 || d < -0.05 {
			t.Errorf("%s 起点 %.3f 与格子 %.3f 不符", want.ID, rows[i].Start, want.Start)
		}
	}
	if _, err := os.Stat(p.Artifact("renders/main_voice.mp4")); err != nil {
		t.Fatal("main_voice 未拼出")
	}
	// 分镜画面载体定位（帧路由）：seg02 → para01 片 + 格子偏移
	mp4, off := pipeline.SegVideoLocate(p, "seg02")
	if filepath.Base(mp4) != "para01.mp4" {
		t.Fatalf("SegVideoLocate(seg02) 载体不对: %s", mp4)
	}
	if d := off - units[0].Segs[1].Start; d > 1e-9 || d < -1e-9 {
		t.Fatalf("SegVideoLocate(seg02) 偏移 %.3f", off)
	}
	t.Logf("★ 段落制生产路径冒烟过：para01 %.2fs（格子 %.2fs），segmap 3 行，%s", dur, units[0].Dur, p.Dir)
}
