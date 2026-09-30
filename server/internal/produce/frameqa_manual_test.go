package produce

// 手动实测：FRAMEQA_TEST=1 时对现有项目跑一次审查（只读，抽帧+视觉评审，不动 spec）。
//   cd server && FRAMEQA_TEST=1 go test ./internal/produce -run TestFrameQAReview -v
import (
	"os"
	"testing"

	"webpage-video-agent/server/internal/events"
	"webpage-video-agent/server/internal/pipeline"
)

func TestFrameQAReview(t *testing.T) {
	if os.Getenv("FRAMEQA_TEST") == "" {
		t.Skip("手动测试：设 FRAMEQA_TEST=1 运行")
	}
	dataDir := os.Getenv("FRAMEQA_DATA")
	if dataDir == "" {
		dataDir = "../../../data"
	}
	proj := os.Getenv("FRAMEQA_PROJECT")
	if proj == "" {
		proj = "p20260924-003034"
	}
	r := &Runner{DataDir: dataDir, RootDir: "../../../", Hub: events.NewHub()}
	p := pipeline.NewProject(dataDir, proj)
	failed, err := r.reviewSegments(t.Context(), p, "")
	if err != nil {
		t.Fatalf("reviewSegments: %v", err)
	}
	t.Logf("未过段数: %d", len(failed))
	for id, issues := range failed {
		t.Logf("--- %s ---\n%s", id, issues)
	}
}
