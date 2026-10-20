package produce

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"webpage-video-agent/server/internal/events"
	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/pipeline"
)

// 自动重跑：真失败重试至多 maxAutoRetries 次，烧完仍败才返回且带「已自动重跑」
// 标记（聊天话术据此说明）；中途成功不裹标记；ctx 已死（取消/停机）绝不重跑，
// 原样返回；%w 保错误链，finishJob 取消分支的 errors.Is 不误判。
func TestRunWithAutoRetry(t *testing.T) {
	oldDelays := autoRetryDelays
	autoRetryDelays = [maxAutoRetries]time.Duration{}
	defer func() { autoRetryDelays = oldDelays }()

	dir := t.TempDir()
	r := &Runner{DataDir: dir, RootDir: dir, Hub: events.NewHub()}
	p := pipeline.NewProject(dir, "p1")
	if err := os.MkdirAll(p.Dir, 0o755); err != nil {
		t.Fatal(err)
	}

	// 一直失败：首跑 + 2 次重跑 = 3 次尝试，最终 err 带标记
	n := 0
	err := r.runWithAutoRetry(context.Background(), p, "p1", EventProduceRetry, func(context.Context) error {
		n++
		return errors.New("boom")
	})
	if n != 1+maxAutoRetries || err == nil || !strings.Contains(err.Error(), "已自动重跑 2 次") {
		t.Fatalf("失败烧尽不符: n=%d err=%v", n, err)
	}

	// 第 2 次成功：不裹标记
	n = 0
	err = r.runWithAutoRetry(context.Background(), p, "p1", EventProduceRetry, func(context.Context) error {
		n++
		if n < 2 {
			return errors.New("boom")
		}
		return nil
	})
	if n != 2 || err != nil {
		t.Fatalf("重跑后成功不符: n=%d err=%v", n, err)
	}

	// ctx 已死：首轮失败即返回，不重跑、不裹标记
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	n = 0
	err = r.runWithAutoRetry(ctx, p, "p1", EventProduceRetry, func(context.Context) error {
		n++
		return errors.New("boom")
	})
	if n != 1 || err == nil || !strings.Contains(err.Error(), "boom") || strings.Contains(err.Error(), "已自动重跑") {
		t.Fatalf("取消后不该重跑: n=%d err=%v", n, err)
	}

	// 重跑轮里冒出 context.Canceled（用户取消）：标记照裹但错误链保真
	n = 0
	err = r.runWithAutoRetry(context.Background(), p, "p1", EventProduceRetry, func(context.Context) error {
		n++
		if n == 1 {
			return errors.New("boom")
		}
		return context.Canceled
	})
	if n != 1+maxAutoRetries || !errors.Is(err, context.Canceled) {
		t.Fatalf("取消错误链断了: n=%d err=%v", n, err)
	}

	// 预算耗尽（P0-1）：确定性失败不烧重跑轮次——首跑即返回且 manifest 落
	// budget.exhausted 账行
	n = 0
	err = r.runWithAutoRetry(context.Background(), p, "p1", EventProduceRetry, func(ctx context.Context) error {
		n++
		return llm.ErrBudgetExceeded
	})
	if n != 1 || !errors.Is(err, llm.ErrBudgetExceeded) || strings.Contains(err.Error(), "已自动重跑") {
		t.Fatalf("预算耗尽不该重跑: n=%d err=%v", n, err)
	}
	mb, _ := os.ReadFile(p.Artifact("manifest.jsonl"))
	if !strings.Contains(string(mb), "budget.exhausted") {
		t.Fatal("预算耗尽未落 manifest 账行")
	}

	// check 三轮未过（2026-10-19 成本刀）：确定性失败不烧重跑轮次——重跑只会
	// 换个变量名再死一次（seg07 实测白烧两条段链）。错误链必须保真（%w）。
	n = 0
	inner := fmt.Errorf("%w: seg07 ✗ Cannot access 'Ph'", errCheckNotConverged)
	err = r.runWithAutoRetry(context.Background(), p, "p1", EventProduceRetry, func(context.Context) error {
		n++
		return inner
	})
	if n != 1 || !errors.Is(err, errCheckNotConverged) || strings.Contains(err.Error(), "已自动重跑") {
		t.Fatalf("check 不收敛不该重跑: n=%d err=%v", n, err)
	}
}
