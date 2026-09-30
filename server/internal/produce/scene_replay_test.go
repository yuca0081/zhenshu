package produce

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"webpage-video-agent/server/internal/events"
	"webpage-video-agent/server/internal/pipeline"
)

// TestSceneReplayGateOnFixture 端到端跑 Go 侧 scene 确定性回放门禁：
// 用链路闸夹具项目（p20260927-mbgate，含 scene/formula/camera_spec/overlay/xfade），
// snapshot 两轮全片截帧逐像素比对应通过。夹具不存在则跳过（出片机才有）。
func TestSceneReplayGateOnFixture(t *testing.T) {
	root, _ := filepath.Abs(filepath.Join("..", "..", ".."))
	projDir := filepath.Join(root, "data", "projects", "p20260927-mbgate")
	if _, err := os.Stat(filepath.Join(projDir, "index.html")); err != nil {
		t.Skipf("链路闸夹具项目不存在，跳过: %v", err)
	}
	r := &Runner{DataDir: filepath.Join(root, "data"), RootDir: root, Hub: events.NewHub()}
	p := &pipeline.Project{ID: "p20260927-mbgate", Dir: projDir, Ctx: context.Background()}
	if err := r.sceneReplayCheck(context.Background(), p); err != nil {
		t.Fatalf("确定性回放门禁应通过: %v", err)
	}
	// 反向：往 scene code 里塞真随机（模拟 LLM 绕过黑名单落到渲染层的病码），
	// 门禁必须拦下且报错带段 ID（failingSegs 可解析）。
	specPath := filepath.Join(projDir, "llm", "comp-seg02.spec.json")
	orig, _ := os.ReadFile(specPath)
	bad := strings.Replace(string(orig), `var r1 = rng()`, `var r1 = (Math.random()+rng())/2`, 1)
	if bad == string(orig) {
		t.Skip("夹具 spec 结构变化，反向用例注入点失效（更新注入串即可）")
	}
	if err := os.WriteFile(specPath, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	// 帧文件也要带上病码（回放走的是已组装帧，不是 spec——保持与管线一致）
	framePath := filepath.Join(projDir, "compositions", "frames", "seg02.html")
	origFrame, _ := os.ReadFile(framePath)
	badFrame := strings.Replace(string(origFrame), `var r1 = rng()`, `var r1 = (Math.random()+rng())/2`, 1)
	_ = os.WriteFile(framePath, []byte(badFrame), 0o644)
	defer func() {
		_ = os.WriteFile(specPath, orig, 0o644)
		_ = os.WriteFile(framePath, origFrame, 0o644)
	}()
	// 病帧写入后 index/.hf-seg 不必重组装（snapshot 走 compositions/frames 直读）
	if err := r.sceneReplayCheck(context.Background(), p); err == nil {
		t.Fatal("含 Math.random 的 scene 代码应被回放门禁拦下")
	} else if !strings.Contains(err.Error(), "seg02") {
		t.Fatalf("报错应带段 ID 供 failingSegs 解析: %v", err)
	}
}
