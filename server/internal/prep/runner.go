package prep

// runner.go —— 准备阶段的异步运行器（「想法」页签的自动完成按钮底座）：
// 三工位链在后台 goroutine 跑（分钟级），SSE 播报进度、manifest 记账；
// 同项目互斥（与 produce 互斥表同款模式）、支持取消。
// Start 是全量入口：主题项目从①起步、粘贴稿件项目同样走这里——对话工具与
// 想法页签两条路共用同一条链。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"webpage-video-agent/server/internal/artifacts"
	"webpage-video-agent/server/internal/pipeline"
)

// IdeaFile 用户想法的落盘位置：①kickoff 当选题意图、②kickoff 当导演意图参考。
const IdeaFile = "input/prep_idea.txt"

// ErrPrepRunning / ErrPrepLegacy / ErrPrepNoLLM Start 的三类拒绝（api 层转人话）。
var (
	ErrPrepRunning = errors.New("准备阶段已在运行中")
	ErrPrepLegacy  = errors.New("老项目（legacy 分镜已在）不走准备工位——改稿用对话")
	ErrPrepNoLLM   = errors.New("LLM 未配置（LLM_API_KEY），准备工位无法启动")
)

// Runner 准备阶段运行器。main 构造一次，api 与 OnDone 回调共用。
type Runner struct {
	DataDir string
	Deps    Deps
	// OnDone 链终态回调（err==nil 成功；ctx.Canceled 取消）：宿主接线做状态
	// 回写与聊天通知。可空。
	OnDone func(projectID string, err error)

	mu      sync.Mutex
	running map[string]context.CancelFunc
}

// NewRunner 构造运行器（Deps 与管线钩子共用同一份，emit/账本单源）。
func NewRunner(dataDir string, deps Deps) *Runner {
	return &Runner{DataDir: dataDir, Deps: deps, running: map[string]context.CancelFunc{}}
}

// IsRunning 该项目的准备链是否在跑。
func (r *Runner) IsRunning(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, live := r.running[id]
	return live
}

// Start 启动三工位链。idea 非空落 input/prep_idea.txt（①②kickoff 消费）；
// force=true 先按「改文字」级联作废既有工件再跑（按新想法重跑全部）。
// 返回 (false, nil) = 已在跑（幂等）；非 nil 错误 = 未启动。
// 注：登记 running 故意放在最前——登记即互斥，先检查后登记会开并发窗口；
// 代价只是检查失败到 release 的短暂窗口内 IsRunning 误报 true（无害）。
func (r *Runner) Start(id, idea string, force bool) (bool, error) {
	r.mu.Lock()
	if _, live := r.running[id]; live {
		r.mu.Unlock()
		return false, nil
	}
	p := pipeline.NewProject(r.DataDir, id)
	if _, err := os.Stat(p.Dir); err != nil {
		r.mu.Unlock()
		return false, fmt.Errorf("项目不存在: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.running[id] = cancel
	r.mu.Unlock()
	release := func() {
		r.mu.Lock()
		delete(r.running, id)
		r.mu.Unlock()
	}

	if LegacyProject(p) {
		release()
		return false, ErrPrepLegacy
	}
	if !Available() {
		release()
		return false, ErrPrepNoLLM
	}
	if HasArtifact(p, StationStoryboard) && !force {
		release()
		return false, errors.New("准备工件已生成——去「终审」页签确认，或勾选「按新想法重跑」")
	}
	if force {
		// force 无条件作废三工件：上轮失败停在①②（分镜工件缺）时，旧写法只在
		// HasArtifact(Storyboard) 为真才 InvalidateFrom，新想法只落 input 不被
		// 消费（2026-10-19 中-1）。无任何工件时 InvalidateFrom 是幂等空操作。
		InvalidateFrom(p, StationScript)
	}
	if idea = strings.TrimSpace(idea); idea != "" {
		if err := os.MkdirAll(filepath.Dir(p.Artifact(IdeaFile)), 0o755); err != nil {
			release()
			return false, err
		}
		if err := os.WriteFile(p.Artifact(IdeaFile), []byte(idea), 0o644); err != nil {
			release()
			return false, err
		}
		applyIdeaDuration(p, idea)
	}
	p.Manifest("prep.start", "启动三工位链（想法页/对话入口）")
	r.Deps.emit(id, "stage", "prep", "running")
	// 总闸：回合预算放宽后（①30/②12/③10），单站烧穿预算的最坏墙钟很可观，
	// 再叠一次冷重试必须有个链级上限兜底——60 分钟覆盖正常长链绰绰有余，
	// 卡死（端点挂住/无限重试）到点收口，不无限拖。
	runCtx, cancelRun := context.WithTimeout(ctx, 60*time.Minute)
	go func() {
		defer release()
		defer cancelRun()
		// 清理过的老项目先回源工作区；链终了全量补传（工件/样张/manifest 上正本）
		if r.Deps.Artifacts != nil && r.Deps.Artifacts.Available() {
			if n, err := artifacts.Restore(r.Deps.Artifacts, r.DataDir, artifacts.ProjectKey(id, "")); err != nil {
				fmt.Printf("[artifacts] %s 准备前回源中断: %v\n", id, err)
			} else if n > 0 {
				fmt.Printf("[artifacts] %s 回源 %d 个文件\n", id, n)
			}
		}
		err := RunStrict(runCtx, p, r.Deps)
		if r.Deps.Artifacts != nil && r.Deps.Artifacts.Available() {
			if n, err := artifacts.SyncUp(r.Deps.Artifacts, r.DataDir, artifacts.ProjectKey(id, "")); err != nil {
				// 补传失败必须留痕：链终了的「全量补传」静默丢了，云端正本会
				// 缺一截而无人知晓（后续检查点/-storage-sync 会再试）
				fmt.Printf("[artifacts] %s 准备产物补传失败: %v\n", id, err)
			} else if n > 0 {
				fmt.Printf("[artifacts] %s 上传 %d 个准备产物\n", id, n)
			}
		}
		if err != nil && runCtx.Err() != nil {
			p.Manifest("prep.cancelled", "用户取消或总闸超时（60 分钟）")
			r.Deps.emit(id, "stage", "prep", "cancelled")
		}
		if r.OnDone != nil {
			r.OnDone(id, err)
		}
	}()
	return true, nil
}

// RunInline 对话侧同步执行（agent 的 rerun_prep / draft_* 工具用）：与异步链
// 共用 running 互斥——已有链在跑返回 ErrPrepRunning；执行期间登记 running，
// 想法页 runPrep / cancelPrep / deleteProject 因此都能看到这条「链」在跑
// （2026-10-19 高-6 收口：对话侧原先直接 RunStrict 不登记，与想法页可双开
// 两条链互踩工件）。run 闭包里放作废+执行，ErrPrepRunning 时不会白作废。
func (r *Runner) RunInline(ctx context.Context, id string, run func(ctx context.Context) error) error {
	r.mu.Lock()
	if _, live := r.running[id]; live {
		r.mu.Unlock()
		return ErrPrepRunning
	}
	ictx, cancel := context.WithCancel(ctx)
	r.running[id] = cancel
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.running, id)
		r.mu.Unlock()
	}()
	return run(ictx)
}

// applyIdeaDuration 想法里点了时长（如「3分钟左右」「约两分半」）就写成项目目标
// 时长——①的自检带、③的段长带都按它走，模型不用猜。只认 分钟/分半 两种说法；
// 范围外（<1 分钟或 >15 分钟）静默丢弃不写盘——不是钳制，是当没说。
func applyIdeaDuration(p *pipeline.Project, idea string) {
	re := regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?|[一两二三四五六七八九十]+)\s*(?:分半|分钟)`)
	m := re.FindStringSubmatch(idea)
	if len(m) < 2 {
		return
	}
	minutes := 0.0
	if v, err := strconv.ParseFloat(m[1], 64); err == nil {
		minutes = v
	} else {
		minutes = parseCJKNum(m[1])
	}
	if strings.Contains(m[0], "分半") {
		minutes += 0.5
	}
	sec := minutes * 60
	if sec < 60 || sec > 900 {
		return
	}
	_ = pipeline.SetProjectField(p, "target_seconds", sec)
	p.Manifest("prep.target", fmt.Sprintf("目标时长 %.0f 秒（取自用户想法）", sec))
}

// parseCJKNum 复合中文数字按十进制解析：「十二」=12、「二十」=20、「三十二」=32。
// 旧写法 minutes*10+v 只对个位数与整十数碰巧正确——「十二」会被算成 102 分钟，
// 超上限后被静默丢弃，用户点的目标时长悄悄失效（2026-10-19 中-35）。
func parseCJKNum(s string) float64 {
	cn := map[rune]float64{'一': 1, '两': 2, '二': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9}
	total, cur, seenUnit := 0.0, 0.0, false
	for _, r := range s {
		switch {
		case cn[r] > 0:
			cur = cn[r]
		case r == '十':
			if cur == 0 { // 裸「十」开头 = 1×10
				cur = 1
			}
			total += cur * 10
			cur, seenUnit = 0, true
		}
	}
	if !seenUnit {
		return cur // 纯个位数（「五」=5）
	}
	return total + cur // 十位 + 个位（「二十三」=20+3）
}

// Cancel 请求取消在跑的准备链（协作式：工位回合边界退出）。false = 没在跑。
func (r *Runner) Cancel(id string) bool {
	r.mu.Lock()
	cancel, live := r.running[id]
	r.mu.Unlock()
	if !live {
		return false
	}
	cancel()
	return true
}
