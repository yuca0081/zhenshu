package agent

import (
	"os"
	"path/filepath"
	"testing"

	"webpage-video-agent/server/internal/config"
	"webpage-video-agent/server/internal/methodlib"
	"webpage-video-agent/server/internal/pipeline"
)

// 手工端到端验证（打真实 LLM，平时跳过）：
// REGEN_PROJ=p20260926-140432 REGEN_INSTRUCTION="更暖色调，加猫爪猫耳Q版点缀" \
//   go test ./internal/agent -run TestManualStyleRegen -v
func TestManualStyleRegen(t *testing.T) {
	projID := os.Getenv("REGEN_PROJ")
	if projID == "" {
		t.Skip("manual-only: set REGEN_PROJ / REGEN_INSTRUCTION")
	}
	cfg, err := func() (*config.Config, error) {
		// go test 的 CWD 是包目录，向上找到仓库根（.env 所在）再 Load
		dir, _ := os.Getwd()
		for i := 0; i < 6; i++ {
			if _, err := os.Stat(filepath.Join(dir, ".env")); err == nil {
				break
			}
			dir = filepath.Dir(dir)
		}
		if err := os.Chdir(dir); err != nil {
			return nil, err
		}
		return config.Load()
	}()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(cfg.DataDir)
	a := &Agent{
		DataDir: cfg.DataDir,
		RootDir: root,
		Lib:     methodlib.New(cfg.DataDir),
		Emit:    func(projectID, typ, stage, detail string) {},
		OnEvent: func(projectID, event, detail string) { t.Logf("event %s: %s", event, detail) },
	}
	p := pipeline.NewProject(cfg.DataDir, projID)
	got := a.regenerateStyle(p, os.Getenv("REGEN_INSTRUCTION"), p.Artifact("style/style_samples.json"))
	t.Log(got)
}
