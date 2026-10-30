package produce

// 手动实测：COVER_TEST=1 时对现有项目出一次封面（写 renders/cover.png +
// cover.json；整条 MakeCover 链路：抽帧→版式→无头截图）。
//   cd server && COVER_TEST=1 COVER_PROJ=<项目id> go test ./internal/produce -run TestManualCover -v
// 项目 id 缺省 p20261029-092526（本机常驻验证项目）。
import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"webpage-video-agent/server/internal/pipeline"
)

func TestManualCover(t *testing.T) {
	if os.Getenv("COVER_TEST") == "" {
		t.Skip("手动测试：设 COVER_TEST=1 运行")
	}
	id := os.Getenv("COVER_PROJ")
	if id == "" {
		id = "p20261029-092526"
	}
	root, err := filepath.Abs("../../..") // go test cwd = 本包目录：produce→internal→server→仓库根
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{RootDir: root}
	p := pipeline.NewProject(filepath.Join(root, "data"), id)
	if _, err := os.Stat(p.Artifact("renders/main.mp4")); err != nil {
		t.Skipf("项目 %s 无成片，跳过", id)
	}
	title := os.Getenv("COVER_TITLE")
	if title == "" {
		title = DefaultCoverTitle(p)
	}
	tr, ok := PickCoverT(p)
	if !ok {
		t.Skip("无 timeline，跳过")
	}
	if err := r.MakeCover(context.Background(), p, tr, title); err != nil {
		t.Fatal(err)
	}
	t.Logf("封面已出：t=%.2f「%s」→ %s", tr, title, coverPNGPath(p))
}
