package produce

// 同题 bake-off 第一场（M0'-b）：v1 老项目（真 TTS/真风格/真分镜）只重跑 v2 中间层。
// BAKEOFF=1 触发；项目=data/projects/bakeoff01（上游产物由 shell 从 v1 复制，llm/compositions/renders 已剔除）。
// 链路与 produce.run 一致（tts 已有产物跳过）：sceneGen → compositions 落盘 → assemble
// → checkWithRepair → render → stitch → frameQA。产物 renders/main.mp4 与 v1 基线并排比。

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"webpage-video-agent/server/internal/events"
	"webpage-video-agent/server/internal/pipeline"
)

func TestBakeoffFirstRun(t *testing.T) {
	if os.Getenv("BAKEOFF") != "1" {
		t.Skip("BAKEOFF=1 才跑（真实 LLM + 渲染）")
	}
	root, _ := filepath.Abs(filepath.Join("..", "..", ".."))
	loadDotEnv(t, filepath.Join(root, ".env"))
	if os.Getenv("LLM_API_KEY") == "" {
		t.Fatal("LLM_API_KEY 未配置")
	}
	pipeline.EnsureFFmpeg()

	projID := "bakeoff01"
	r := &Runner{DataDir: filepath.Join(root, "data"), RootDir: root, Hub: events.NewHub()}
	p := &pipeline.Project{ID: projID, Dir: filepath.Join(root, "data", "projects", projID), Ctx: context.Background()}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()

	t0 := time.Now()
	t.Log("① sceneGen（LLM 直写 8 段场景文件）…")
	if err := r.sceneGen(ctx, p, "", "", ""); err != nil {
		t.Fatalf("sceneGen: %v", err)
	}
	t.Logf("   sceneGen 完成 %.1f 分钟", time.Since(t0).Minutes())

	r.clearFrames(p, "")
	if err := pipeline.Run(ctx, p, "compositions"); err != nil {
		t.Fatalf("compositions 落盘: %v", err)
	}
	t.Log("② assemble …")
	if err := r.python(ctx, "ai/assemble.py", p.Dir, 2*time.Minute); err != nil {
		t.Fatalf("assemble: %v", err)
	}
	t.Log("③ check 门禁（含确定性回放探针，失败外科修复）…")
	if err := r.checkWithRepair(ctx, p, "", ""); err != nil {
		t.Fatalf("check: %v", err)
	}
	t.Log("④ render + stitch …")
	if err := pipeline.Run(ctx, p, "render"); err != nil {
		t.Fatalf("render: %v", err)
	}
	if err := pipeline.Run(ctx, p, "stitch"); err != nil {
		t.Fatalf("stitch: %v", err)
	}
	t.Log("⑤ frameQA（视觉抽检+自修复）…")
	if _, err := r.frameQA(ctx, p, "", ""); err != nil {
		t.Fatalf("frameQA: %v", err)
	}
	t.Logf("✅ bakeoff01 完成，总耗时 %.1f 分钟 → %s", time.Since(t0).Minutes(), p.Artifact("renders/main.mp4"))
}
