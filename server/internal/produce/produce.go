// Package produce：制作任务运行器（确定性编排，LLM 只在需要的点上介入）。
//
// 双路分发：prep 新项目（10-16 起的全部新项目）走 runSegmentPipeline（见
// segpipe.go——逐段 写→审→渲 三池并发 + 边界审 + 全片终审）；本文件 run()
// 的 v2 六道链路（tts → 场景 → 合成物 → 组装 → check → 渲染 → 成片）只服务
// legacy 老项目重跑与段级 Rework 旁路。每步 SSE 广播 stage 事件；产物落盘可
// 从任意步重放（manifest 记账）。
package produce

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	openai "github.com/sashabaranov/go-openai"

	"webpage-video-agent/server/internal/agentlog"
	"webpage-video-agent/server/internal/artifacts"
	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/events"
	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/pipeline"
	"webpage-video-agent/server/internal/store"
)

type Runner struct {
	DataDir    string // 仓库根 data/（项目在 data/projects/<id>）
	RootDir    string // 仓库根（ai/ 脚本所在）
	Hub        *events.Hub
	Store      store.Store                            // render_jobs 记账（nil = 不记账，测试场景）；manifest 仍是事实源
	Artifacts  artifacts.Store                        // 产物正本后端（nil/本地 = 不上传，docs/storage-cloud-migration.md）
	OnDone     func(projectID, status, detail string) // 终态回写（status: video|failed|cancelled；detail 供聊天汇报失败原因等）
	OnNotice   func(projectID, content string)        // 非终态插话（如开机自动续跑通知），落聊天框但不改项目状态
	AutoResume bool                                   // 启动对账时是否自动续跑被重启打断的整片制作（main 现置 false：只补账通知，不自动起任务；测试默认关，防拖起真管线）

	wg       sync.WaitGroup // 在跑的制作/重做 goroutine（优雅停机等待终态落盘）
	stopping atomic.Bool    // 停机中：取消语义从「用户取消」改「服务停机中断」，并拒绝新任务
}

// manifest 制作事件协议（跨包契约：本包写、Reconcile 启动对账扫、agent 状态摘要与
// api last_produce 读——此前多处各写魔法字符串，改名一处即静默破坏对账与终态汇报，
// 收敛为唯一来源；终态判别走 terminalEvent，新增终态必须在此登记）。
const (
	EventProduceStart       = "produce.start"
	EventProduceDone        = "produce.done"
	EventProduceError       = "produce.error"
	EventProduceCancelled   = "produce.cancelled"
	EventProduceInterrupted = "produce.interrupted"
	EventProduceRetry       = "produce.retry"
	EventReworkStart        = "rework.start"
	EventReworkDone         = "rework.done"
	EventReworkError        = "rework.error"
	EventReworkCancelled    = "rework.cancelled"
	EventReworkInterrupted  = "rework.interrupted"
	EventReworkRetry        = "rework.retry"
)

// terminalEvent 该事件是否制作/重做终态。对账扫描、LastTerminal 共用同一判别，
// 防止「写入的新终态读取方不认」再次造成悬挂。
func terminalEvent(ev string) bool {
	switch ev {
	case EventProduceDone, EventProduceError, EventProduceCancelled, EventProduceInterrupted,
		EventReworkDone, EventReworkError, EventReworkCancelled, EventReworkInterrupted:
		return true
	}
	return false
}

// LastTerminal 读 manifest 最近一条制作/重做终态（done/error/cancelled/interrupted）。
// 磁盘审计口径的唯一读取点：agent 状态摘要、api last_produce 共用，不再各写一套扫描。
// 尾部窗口读取（ReadJSONLTail）：manifest 无限增长，agent 每步门禁都调这里，
// 全量读是线性放大的 I/O。
func LastTerminal(p *pipeline.Project) (event, detail, ts string) {
	lines, err := ReadJSONLTail(p.Artifact("manifest.jsonl"), 512)
	if err != nil {
		return "", "", ""
	}
	for i := len(lines) - 1; i >= 0; i-- {
		var e struct {
			Event  string `json:"event"`
			Detail string `json:"detail"`
			TS     string `json:"ts"`
		}
		if json.Unmarshal(lines[i], &e) != nil {
			continue
		}
		if terminalEvent(e.Event) {
			return e.Event, e.Detail, e.TS
		}
	}
	return "", "", ""
}

var (
	mu       sync.Mutex
	running  = map[string]context.CancelFunc{}
	runGen   = map[string]uint64{}        // 运行代际票：强制收尾凭票动手，防延时兜底误杀接手的新运行
	genSeq   uint64                       // 代际发号器（mu 内自增）
	finished = map[string]bool{}          // 终态落账认领（防泄漏 goroutine 与强制终局双写）
	orphaned = map[string]chan struct{}{} // 强制终局后仍在收尾的泄漏任务（Start 拒新，防双管线互踩）
)

// claimRun 登记运行并签发代际票（mu 临界区内调用）。
func claimRun(projectID string, cancel context.CancelFunc) uint64 {
	genSeq++
	running[projectID] = cancel
	runGen[projectID] = genSeq
	return genSeq
}

// releaseRun 摘除运行登记（mu 临界区内调用；gen>0 时校验代际，换主则不动）。
func releaseRun(projectID string, gen uint64) {
	if gen != 0 && runGen[projectID] != gen {
		return
	}
	delete(running, projectID)
	delete(runGen, projectID)
}

// claimFinish 终态落账认领：同一任务的终态只允许写一次。强制终局（Cancel 兜底）
// 与原 goroutine 的自然收尾赛跑时，后到者放弃。
func claimFinish(projectID string) bool {
	mu.Lock()
	defer mu.Unlock()
	if finished[projectID] {
		return false
	}
	finished[projectID] = true
	return true
}

// awaitNotOrphaned 强制终局后的收尾窗口：等老 goroutine 退出（正常几秒；若被
// 死锁卡死则等 3 分钟放行——死锁者不再写任何产物，双开实际无害，别把用户锁在
// 外面）。Start 与 Rework 共用（2026-10-19 中-5：Rework 原先漏等，取消楔死的
// 60 秒窗口内点重做会开出第二条管线与老 goroutine 并发写同一项目）。
func awaitNotOrphaned(projectID string) {
	mu.Lock()
	done := orphaned[projectID]
	mu.Unlock()
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(3 * time.Minute):
	}
}

// Start 启动制作（同项目幂等：已在跑则返回 false）。
func (r *Runner) Start(projectID string) (bool, error) {
	awaitNotOrphaned(projectID)
	mu.Lock()
	if r.stopping.Load() {
		mu.Unlock()
		return false, errors.New("服务正在停机，不再接受新任务")
	}
	if _, live := running[projectID]; live {
		mu.Unlock()
		return false, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	gen := claimRun(projectID, cancel)
	delete(finished, projectID) // 新一轮重置终态认领（已在 mu 临界区内）
	r.wg.Add(1)
	mu.Unlock()
	// 慢操作（磁盘 stat / manifest 追加写 / DB 写）全在锁外（2026-10-19 中-2）：
	// mu 是整个 produce 包的全局互斥锁，锁内做 IO 会把其他项目的 IsRunning/
	// Cancel/Start 全排在这把锁上；失败回滚认领即可（Rework 早退路径同款模式）。
	p := pipeline.NewProject(r.DataDir, projectID)
	if _, err := os.Stat(p.Dir); err != nil {
		mu.Lock()
		releaseRun(projectID, gen)
		mu.Unlock()
		r.wg.Done()
		return false, fmt.Errorf("项目不存在: %w", err)
	}
	p.Manifest(EventProduceStart, "") // 启动对账依据：有 start 无终态 = 进程死在中途（Reconcile 补写）
	r.jobStart(projectID, "produce")
	go func() {
		defer func() {
			mu.Lock()
			releaseRun(projectID, gen)
			done := orphaned[projectID] // 强制终局留下的收尾标记：本 goroutine 退出即解除
			delete(orphaned, projectID)
			mu.Unlock()
			if done != nil {
				close(done)
			}
			r.wg.Done()
		}()
		// 清理过的老项目先回源工作区（契约/段片/配音），新项目是廉价 no-op
		pipeline.EnsureWorkspace(p)
		// panic 兜底：audio_meta 等产物结构异常不该崩掉整个服务（goroutine panic 直接退进程）；
		// 真失败自动重跑（≤2 次），重跑烧完才终态落盘+聊天汇报
		err := r.runWithAutoRetry(ctx, p, projectID, EventProduceRetry, func() error { return r.run(ctx, p) })
		r.finishJob(p, projectID, err, jobTail{
			evCancelled:  EventProduceCancelled,
			evError:      EventProduceError,
			cancelDetail: "用户取消制作",
			stopDetail:   "服务停机，制作中断（产物已保留，重启后重新开工即按段复用）",
		})
	}()
	return true, nil
}

// safeRun panic → error（制作 goroutine 内不带 recover 时，一处 panic 崩整个进程）。
func safeRun(f func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("内部错误（panic）: %v", r)
		}
	}()
	return f()
}

// notifyOnDone OnDone 的防 panic 包装：finishJob 给取消/失败路径包了 swallow，
// 成功路径（run/runRework 返回 nil 后）也曾裸调——同样的 panic 面（NotifyResult
// 碰到坏产物结构就够），一并包住。
func (r *Runner) notifyOnDone(projectID, status, detail string) {
	if r.OnDone == nil {
		return
	}
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("[produce] OnDone(%s) panic（已吞，不影响已落盘终态）: %v\n", status, rec)
		}
	}()
	r.OnDone(projectID, status, detail)
}

// 自动重跑：制作/重做的真失败（LLM 超时、质检随机性、渲染偶发抖动等）不需要用户
// 做任何决策——产物按段落盘复用，重跑的代价与用户手打「继续」完全一样，那就该
// 自己做掉。只有重跑烧完还失败才惊动用户。取消/停机是用户意图，绝不重跑。
const maxAutoRetries = 2

var autoRetryDelays = [maxAutoRetries]time.Duration{10 * time.Second, 30 * time.Second} // var：测试可缩成 0

// runWithAutoRetry 首跑 + 至多 maxAutoRetries 次自动重跑；ev 是重跑的 manifest 事件名
// （produce.retry / rework.retry，事件流面板可见）。重跑轮次耗尽仍失败时 err 带
// 「已自动重跑」标记（%w 保错误链，取消分支的 errors.Is 不受影响），聊天话术据此
// 说明「自动重跑也没救回来」。ctx 已死（用户取消/停机）原样返回，走取消分支。
func (r *Runner) runWithAutoRetry(ctx context.Context, p *pipeline.Project, projectID, ev string, run func() error) error {
	err := safeRun(run)
	retried := 0
	for n := 1; err != nil && n <= maxAutoRetries && ctx.Err() == nil && !r.stopping.Load(); n++ {
		delay := autoRetryDelays[n-1]
		r.Manifest(p, ev, fmt.Sprintf("自动重跑 %d/%d（%s 后开始）；上次失败：%s",
			n, maxAutoRetries, delay, pipeline.FirstLine(err.Error(), 160)))
		select {
		case <-time.After(delay):
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			// 等待期被取消/停机：用户意图不是失败——返回 ctx 错误走取消分支，
			// 别把上一轮的失败错误落成「制作失败（已自动重跑 N 次仍未过）」
			return ctx.Err()
		}
		retried = n
		err = safeRun(run)
	}
	if err != nil && retried > 0 {
		err = fmt.Errorf("%w（已自动重跑 %d 次仍未过）", err, retried)
	}
	return err
}

// cancelGrace 强制终局的优雅宽限：ctx 取消后给在途调用（LLM 传输超时 ≤10min、
// python 子进程等）收尾的机会；超时即强制落终态，泄漏 goroutine 不绑架状态机。
const cancelGrace = 60 * time.Second

// Cancel 请求取消进行中的制作/重做（协作式：杀子进程 + 阶段边界退出）。
// 2026-10-17 实测教训：楔死的管线对 ctx 取消可能无动于衷（收尾 Wait 被泄漏
// goroutine 卡死），用户点「停止」后 UI 永远"运行中"——取消信号发出后挂一个
// 兜底计时器，宽限期满仍活着就强制落 cancelled 终态并摘除在册标记。
// 返回 false = 该项目没有在跑的任务。
func (r *Runner) Cancel(projectID string) bool {
	mu.Lock()
	cancel, live := running[projectID]
	gen := runGen[projectID]
	mu.Unlock()
	if !live {
		return false
	}
	cancel()
	go func() {
		r.awaitGraceThen(projectID, gen, cancelGrace, func(p *pipeline.Project) {
			detail := "制作卡住未响应取消，已强制终止（产物已保留，重跑会按段复用）"
			r.forceTerminal(p, EventProduceCancelled, detail, "cancelled", "")
		})
	}()
	return true
}

// forceFinishStall 看门狗升级兜底：刹车已 trip 但管线仍收不回来（如持锁死锁）
// 时，强制落失败终态把 UI 解放掉。原 goroutine 若事后醒来，其 finishJob 被
// claimFinish 挡住不会双写。grace 传 0 = 立即落账（刹车已给过收尾窗口）。
// selfGen = 触发看门狗的那个运行的代际票：延时开火期间若运行表已换主（自动
// 重跑/用户重开接手），票对不上就放手，绝不误杀新运行（2026-10-18 实测：
// 旧运行的 2 分钟兜底把 10 秒后接手的新重跑整个终结）。
func (r *Runner) forceFinishStall(projectID, reason string, selfGen uint64) {
	r.awaitGraceThen(projectID, selfGen, 0, func(p *pipeline.Project) {
		detail := "制作长时间无进展且无法收尾，已强制终止（" + pipeline.FirstLine(reason, 120) + "）——产物已按段保留，回「继续」可按段复用重跑"
		r.forceTerminal(p, EventProduceError, detail, "failed", detail)
	})
}

// runningGen 运行表里该项目当前的代际票。不在跑返回 0。
func (r *Runner) runningGen(projectID string) uint64 {
	mu.Lock()
	defer mu.Unlock()
	return runGen[projectID]
}

// awaitGraceThen 等待宽限期满（期间任务自然收尾则直接返回），期满把任务从
// running 摘除并登记 orphaned（Start 拒新防双管线互踩，老 goroutine 退出或
// TTL 到期解除），再调 write 执行落账。selfGen 非 0 时校验运行代际：运行表
// 已换主（票对不上）则视为新运行接手，直接放手不摘除不落账。
func (r *Runner) awaitGraceThen(projectID string, selfGen uint64, grace time.Duration, write func(*pipeline.Project)) {
	deadline := time.Now().Add(grace)
	for {
		time.Sleep(time.Second)
		mu.Lock()
		_, live := running[projectID]
		gen := runGen[projectID]
		mu.Unlock()
		if !live {
			return // 已自然收尾
		}
		if selfGen != 0 && gen != selfGen {
			return // 代际易主：运行表里已是别的运行（如自动重跑接手），别碰它
		}
		if time.Now().After(deadline) {
			break
		}
	}
	mu.Lock()
	gen, still := runGen[projectID], false
	if _, ok := running[projectID]; ok {
		still = true
	}
	if selfGen != 0 && gen != selfGen {
		mu.Unlock()
		return // 双检：等锁期间换了代，落账会让新运行被误判死亡
	}
	releaseRun(projectID, selfGen) // IsRunning 立即转假：UI 先行解放
	if still {
		if done := orphaned[projectID]; done == nil {
			orphaned[projectID] = make(chan struct{})
		}
	}
	mu.Unlock()
	if !still || !claimFinish(projectID) {
		return
	}
	write(pipeline.NewProject(r.DataDir, projectID))
}

// forceTerminal 强制终态落账（manifest + render_jobs + 事件 + 聊天汇报）。
func (r *Runner) forceTerminal(p *pipeline.Project, ev, detail, status, errMsg string) {
	log.Printf("[produce] %s %s", p.ID, detail)
	r.Manifest(p, ev, detail)
	r.jobTerminal(p.ID, status, "", errMsg)
	r.emit(p.ID, "stage", "pipeline", "error: "+detail)
	r.emit(p.ID, "error", "", detail)
	r.notifyOnDone(p.ID, status, detail)
}

// Rework 段级重做（plan.md §3.5：修改单元可到元素，重做单元=段）。
// 只重生成目标段画面（音频/时长不变）→ 组装 → 检查 → 整片重渲拼接。
// 支持一次多段（用户一句话圈几段一起改，共享同一条指令跑一遍管线）。
func (r *Runner) Rework(projectID string, segIDs []string, instruction string) (bool, error) {
	if len(segIDs) == 0 {
		return false, fmt.Errorf("未指定重做段")
	}
	awaitNotOrphaned(projectID) // 与 Start 同款收尾等待（2026-10-19 中-5）
	ctx, cancel := context.WithCancel(context.Background())
	mu.Lock()
	if r.stopping.Load() {
		mu.Unlock()
		cancel()
		return false, errors.New("服务正在停机，不再接受新任务")
	}
	if _, live := running[projectID]; live {
		mu.Unlock()
		cancel()
		return false, nil
	}
	gen := claimRun(projectID, cancel)
	// 新一轮任务重置终态认领（与 Start 同款）：上一轮失败的 finishJob 已认领
	// finished，不重置的话本轮重做失败时 claimFinish 拒收——终态不落账、
	// render_jobs 卡 running、无通知（2026-10-19 高-9）
	delete(finished, projectID)
	mu.Unlock()
	p := pipeline.NewProject(r.DataDir, projectID)
	sb, err := p.LoadStoryboard()
	if err != nil {
		cancel()
		mu.Lock()
		releaseRun(projectID, gen)
		mu.Unlock()
		return false, fmt.Errorf("分镜未生成: %w", err)
	}
	var segs []contract.Segment
	for _, id := range segIDs {
		seg, found := findSegment(sb, id)
		if !found {
			cancel()
			mu.Lock()
			releaseRun(projectID, gen)
			mu.Unlock()
			return false, fmt.Errorf("分镜里没有段 %s", id)
		}
		segs = append(segs, seg)
	}
	r.appendReworkHistory(p, segIDs, instruction)
	r.wg.Add(1)
	go func() {
		defer func() {
			mu.Lock()
			releaseRun(projectID, gen)
			mu.Unlock()
			r.wg.Done()
		}()
		// 清理过的老项目先回源工作区（重做的 sceneGen 要吃素材与设计产物）
		pipeline.EnsureWorkspace(p)
		err := r.runWithAutoRetry(ctx, p, projectID, EventReworkRetry, func() error { return r.runRework(ctx, p, segs, instruction) })
		r.finishJob(p, projectID, err, jobTail{
			evCancelled:    EventReworkCancelled,
			evError:        EventReworkError,
			manifestPrefix: joinSegIDs(segs) + ": ",
			cancelDetail:   "用户取消重做",
			stopDetail:     "服务停机，重做中断（该段旧画面仍在成片里，重启后可再重做）",
		})
	}()
	return true, nil
}

// jobTail 制作/重做两个 goroutine 终态收尾里仅有的四处差异。
type jobTail struct {
	evCancelled    string // manifest 取消事件名（EventProduceCancelled / EventReworkCancelled）
	evError        string // manifest 失败事件名
	manifestPrefix string // manifest detail 前缀（重做侧为段号前缀，制作侧为空）
	cancelDetail   string // 用户取消时给用户看的话
	stopDetail     string // 服务停机中断时给用户看的话
}

// finishJob 制作/重做 goroutine 的终态收尾（共用）：manifest 记账、render_jobs 终态
// 回写、emit 事件、聊天汇报（OnDone）。err == nil（成功）时不动——成功路径两边形态
// 差异大，由调用方各自处理。
// 顺序铁律：终态落盘先行，广播/聊天在后——账本是 Reconcile 与 agent 状态摘要的
// 唯一依据，下游展示出乱子（panic/断连）不许拖累它；OnDone 曾无 recover，panic
// 会带崩整个进程并把已写一半的终态变成悬挂 start。
func (r *Runner) finishJob(p *pipeline.Project, projectID string, err error, t jobTail) {
	// 终态全量补传（成功/失败/取消都走）：把本轮新增的 Keeper 产物与 manifest 送上
	// 正本后端——之后清理器才敢删本地。best-effort，缺口由 -storage-sync 兜底。
	r.syncUp(p, "")
	if err == nil {
		return
	}
	// 终态认领：强制终局（Cancel 兜底）可能已抢先落账——泄漏 goroutine 迟到的
	// 收尾不得覆盖用户已经看到的终态。
	if !claimFinish(projectID) {
		log.Printf("[produce] %s 迟到收尾跳过（终态已由强制终局落账）", projectID)
		return
	}
	swallow := func(what string) func() {
		return func() {
			if rec := recover(); rec != nil {
				log.Printf("[produce] %s panic（已吞，不影响已落盘终态）: %v\n", what, rec)
			}
		}
	}
	if errors.Is(err, context.Canceled) {
		detail := t.cancelDetail
		if r.stopping.Load() {
			detail = t.stopDetail
		}
		r.Manifest(p, t.evCancelled, t.manifestPrefix+detail)
		r.jobTerminal(projectID, "cancelled", "", detail)
		defer swallow("取消收尾")()
		r.emit(projectID, "stage", "pipeline", "cancelled")
		r.emit(projectID, "cancelled", "", detail)
		if r.OnDone != nil {
			r.notifyOnDone(projectID, "cancelled", detail)
		}
		return
	}
	r.Manifest(p, t.evError, t.manifestPrefix+err.Error())
	r.jobTerminal(projectID, "failed", "", err.Error())
	defer swallow("失败收尾")()
	r.emit(projectID, "stage", "pipeline", "error: "+err.Error())
	r.emit(projectID, "error", "", err.Error())
	if r.OnDone != nil {
		r.notifyOnDone(projectID, "failed", err.Error())
	}
}

func joinSegIDs(segs []contract.Segment) string {
	ids := make([]string, len(segs))
	for i, s := range segs {
		ids[i] = s.ID
	}
	return strings.Join(ids, ",")
}

// ReworkHistoryEntry 一键重做「用上次指令」的数据源（llm/rework-history.json，追加式）。
type ReworkHistoryEntry struct {
	Segs        []string  `json:"segs"`
	Instruction string    `json:"instruction"`
	At          time.Time `json:"at"`
}

func reworkHistoryPath(p *pipeline.Project) string { return p.Artifact("llm/rework-history.json") }

// LoadReworkHistory 全部历史（新在前）；无文件返回空。
func LoadReworkHistory(p *pipeline.Project) []ReworkHistoryEntry {
	b, err := os.ReadFile(reworkHistoryPath(p))
	if err != nil {
		return nil
	}
	var es []ReworkHistoryEntry
	if json.Unmarshal(b, &es) != nil {
		return nil
	}
	return es
}

// LastReworkInstruction 覆盖某段的最近一次重做指令（一键重做按钮预填）。
func LastReworkInstruction(p *pipeline.Project, segID string) string {
	for _, e := range LoadReworkHistory(p) {
		for _, s := range e.Segs {
			if s == segID {
				return e.Instruction
			}
		}
	}
	return ""
}

func (r *Runner) appendReworkHistory(p *pipeline.Project, segIDs []string, instruction string) {
	es := LoadReworkHistory(p)
	es = append([]ReworkHistoryEntry{{Segs: segIDs, Instruction: instruction, At: time.Now()}}, es...)
	if len(es) > 50 {
		es = es[:50]
	}
	// 双吞改留痕（中-L11）：重做历史是「用上次指令」按钮的数据源，静默失败
	// 会让该按钮无声失灵且无从排查
	if err := os.MkdirAll(filepath.Dir(reworkHistoryPath(p)), 0o755); err != nil {
		log.Printf("[produce] 重做历史目录创建失败: %v", err)
		return
	}
	b, err := json.MarshalIndent(es, "", "  ")
	if err != nil {
		log.Printf("[produce] 重做历史序列化失败: %v", err)
		return
	}
	if err := pipeline.WriteFileAtomic(reworkHistoryPath(p), b, 0o644); err != nil {
		log.Printf("[produce] 重做历史落盘失败: %v", err)
	}
}

func findSegment(sb *contract.Storyboard, segID string) (contract.Segment, bool) {
	for _, s := range sb.Segments {
		if s.ID == segID {
			return s, true
		}
	}
	return contract.Segment{}, false
}

// runRework 段级重做管线（多段共享一条指令跑一遍）。旁白/音频不动；
// instruction 作为画面改写要求回喂到每一处修复轮。
func (r *Runner) runRework(ctx context.Context, p *pipeline.Project, segs []contract.Segment, instruction string) error {
	id := p.ID
	only := joinSegIDs(segs)
	labels := make([]string, len(segs))
	for i, s := range segs {
		labels[i] = fmt.Sprintf("段%d「%s」", s.Idx, s.Key)
	}
	r.Manifest(p, EventReworkStart, only+": "+pipeline.FirstLine(instruction, 80))
	r.jobStart(id, "rework")
	r.emit(id, "progress", "rework", "重做 "+strings.Join(labels, "、"))

	// 1. 目标段画面重生成（删旧场景文件强制重出；其余段产物原样复用）
	r.emit(id, "stage", "compositions", "running")
	r.Manifest(p, "stage.start", "compositions")
	// 用户改画要求走 instruction 位（最高优先级），修复轮（check/画面审查）也必须带上
	instr := "用户对这一段画面的修改要求（必须落实）：\n" + instruction
	if err := r.sceneGen(ctx, p, instr, "", only); err != nil {
		return err
	}
	r.clearFrames(p, only)
	if err := pipeline.Run(ctx, p, "compositions"); err != nil {
		return fmt.Errorf("合成物落盘失败: %w", err)
	}
	r.emit(id, "stage", "compositions", "done")
	r.Manifest(p, "stage.finish", "compositions")

	// 2. 重组装（时间轴不变）+ 检查（失败只修目标段）
	r.emit(id, "stage", "assemble", "running")
	r.Manifest(p, "stage.start", "assemble")
	if err := r.assemble(ctx, p); err != nil {
		return fmt.Errorf("组装失败: %w", err)
	}
	r.emit(id, "stage", "assemble", "done")
	r.Manifest(p, "stage.finish", "assemble")
	if err := r.checkWithRepair(ctx, p, instr, only); err != nil {
		return err
	}

	// 3. 段级局部重渲：只删/重渲目标段段片，其余段复用，再 concat 出成片
	r.emit(id, "stage", "render", "running")
	r.Manifest(p, "stage.start", "render")
	for _, seg := range segs {
		if err := os.Remove(p.Artifact("renders/segs/" + seg.ID + ".mp4")); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := pipeline.Run(ctx, p, "render"); err != nil {
		return fmt.Errorf("渲染失败: %w", err)
	}
	if err := pipeline.Run(ctx, p, "stitch"); err != nil {
		return err
	}
	r.emit(id, "stage", "render", "done")
	r.Manifest(p, "stage.finish", "render")

	// 4. 画面审查：只审重做段，不过带要求重出（用户要求经 instr 一路带到修复轮）
	if err := r.frameQA(ctx, p, instr, only); err != nil {
		return err
	}
	r.emit(id, "done", "", "renders/main.mp4")
	r.Manifest(p, EventReworkDone, only)
	r.jobTerminal(id, "done", "renders/main.mp4", "")
	if r.OnDone != nil {
		r.notifyOnDone(id, "video", strings.Join(labels, "、")+" 画面重做完成")
	}
	return nil
}

func (r *Runner) IsRunning(projectID string) bool {
	mu.Lock()
	defer mu.Unlock()
	_, live := running[projectID]
	return live
}

// Reconcile 启动对账：running 表在内存里，进程一死就空——manifest 里「有 start
// 无终态」的项目会永远停在制作中，且 agent 读到的最近终态还是上一轮的（拿旧成片
// 幻报完成）。启动时扫一遍补写终态 + 聊天通知，用户回来就知道该重新开工。
// 必须在任何新任务 Start 之前调用（main 里启动 HTTP 前）。
func (r *Runner) Reconcile() {
	entries, err := os.ReadDir(filepath.Join(r.DataDir, "projects"))
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id := e.Name()
		p := pipeline.NewProject(r.DataDir, id)
		b, err := os.ReadFile(p.Artifact("manifest.jsonl"))
		if err != nil {
			continue
		}
		started := "" // 最近一次 start 事件（produce.start / rework.start），遇到终态清空
		for _, line := range strings.Split(string(b), "\n") {
			var ev struct {
				Event string `json:"event"`
			}
			if json.Unmarshal([]byte(line), &ev) != nil || ev.Event == "" {
				continue
			}
			switch {
			case ev.Event == EventProduceStart || ev.Event == EventReworkStart:
				started = ev.Event
			case terminalEvent(ev.Event):
				started = ""
			}
		}
		if started == "" {
			continue
		}
		isRework := started == EventReworkStart
		what := "制作"
		if isRework {
			what = "重做"
		}
		detail := fmt.Sprintf("上次服务重启时%s中断（产物已保留，重新开工即按段复用）", what)
		// 中断补写专用终态（不与真失败混用 *.error）：agent 摘要/聊天话术说「中断」，
		// 用户不必以为画面出了问题；本轮 detail 文案保留供审计。幂等：上方扫描已把
		// interrupted 记为终态，重复启动不会二次补写。
		ev := EventProduceInterrupted
		if isRework {
			ev = EventReworkInterrupted
		}
		r.Manifest(p, ev, detail)
		r.jobTerminal(id, "failed", "", detail)
		log.Printf("[produce] 对账：%s %s中断，已补写终态\n", id, what)
		// 整片制作中断 → 自动续跑，用户不再需要打「继续」：续跑走的就是
		// start_production 同一条路（manifest 按段复用产物）。上面已补写 interrupted
		// 终态（幂等标记：续跑途中再崩，下次启动至多多续一轮，不会无限循环）。
		// 重做不续：段级微调而已，该段旧画面仍在成片里，中断无害；且 rework.start
		// 的 detail 只留了前 80 字指令，照它重跑会拿残缺指令。
		if !isRework && r.AutoResume {
			if ok, err := r.Start(id); ok {
				log.Printf("[produce] 对账：%s 已自动续跑\n", id)
				if r.OnNotice != nil {
					r.OnNotice(id, "↻ 上次服务重启把制作打断了，已自动续跑——做完的段直接复用，不用重头再来。")
				}
				continue
			} else if err != nil {
				log.Printf("[produce] 对账：%s 自动续跑没起来（%v），退回通知用户\n", id, err)
			}
		}
		if r.OnDone != nil {
			r.notifyOnDone(id, "failed", detail)
		}
	}
}

// Shutdown 优雅停机：取消全部运行中的制作/重做，等它们把终态落盘（超时强退，
// 残留 start 由下次启动 Reconcile 兜底）。
func (r *Runner) Shutdown(ctx context.Context) {
	r.stopping.Store(true)
	mu.Lock()
	for _, cancel := range running {
		cancel()
	}
	mu.Unlock()
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		log.Println("[produce] 停机等待超时，强制退出（中断任务下次启动对账兜底）")
	}
}

func (r *Runner) emit(id, typ, stage, detail string) {
	r.Hub.Emit(id, typ, stage, detail)
}

func (r *Runner) Manifest(p *pipeline.Project, event, detail string) { p.Manifest(event, detail) }

// syncUp 检查点补传（best-effort）：把项目内 rel 前缀下本地有而远端缺的 Keeper
// 文件送上正本后端。失败只记日志——本地仍是工作区，清理器删前会验证远端正本。
func (r *Runner) syncUp(p *pipeline.Project, rel string) {
	if r.Artifacts == nil || !r.Artifacts.Available() {
		return
	}
	if _, err := artifacts.SyncUp(r.Artifacts, r.DataDir, artifacts.ProjectKey(p.ID, rel)); err != nil {
		log.Printf("[artifacts] %s 补传 %s 中断: %v", p.ID, rel, err)
	}
}

// jobStart/jobTerminal render_jobs 记账，与同名 manifest 事件成对出现。best-effort：
// 库不可写只打日志绝不挡制作——manifest 才是对账事实源，这里只是可查询镜像。
func (r *Runner) jobStart(projectID, kind string) {
	if r.Store == nil {
		return
	}
	if err := r.Store.StartRenderJob(projectID, kind); err != nil {
		log.Printf("[produce] render_jobs 起跑记账失败 %s: %v", projectID, err)
	}
}

func (r *Runner) jobTerminal(projectID, status, outputPath, errMsg string) {
	if r.Store == nil {
		return
	}
	if err := r.Store.FinishRenderJob(projectID, status, outputPath, errMsg); err != nil {
		log.Printf("[produce] render_jobs 终态记账失败 %s: %v", projectID, err)
	}
}

func (r *Runner) run(ctx context.Context, p *pipeline.Project) error {
	id := p.ID

	// ── 0. 路由（production-stage-design ⑭）：prep 新项目走段级流水线
	//（写→检→渲→段审→边界审→拼接→终审，见 segpipe.go）；老项目（legacy 分镜、
	// 无文字稿）走原栅栏管线，行为不变。
	if IsPrepProject(p) {
		return r.runSegmentPipeline(ctx, p)
	}

	// ── 1. TTS + 词级对齐（幂等：缺什么补什么）─────────────────
	r.emit(id, "stage", "tts", "running")
	r.Manifest(p, "stage.start", "tts")
	if err := r.python(ctx, "ai/tts_align.py", p.Dir, 30*time.Minute); err != nil {
		return fmt.Errorf("TTS/对齐失败: %w", err)
	}
	r.emit(id, "stage", "tts", "done")
	r.Manifest(p, "stage.finish", "tts")
	r.Manifest(p, "produce.tts", "done")

	// ── 1.5 双语字幕（可选，M-C）：短语级英文翻译 → audio_meta（幂等；失败不挡片）
	if err := r.translateSubtitles(ctx, p); err != nil {
		r.emit(id, "progress", "tts", "双语字幕翻译跳过: "+err.Error())
	}
	// 配音是最耗时的外部服务产物：TTS 一完成就上正本（几十 MB 级 wav × N 段）
	r.syncUp(p, "audio")
	if r.Artifacts != nil && r.Artifacts.Available() {
		if err := r.Artifacts.Put(artifacts.ProjectKey(id, "audio_meta.json"), p.Artifact("audio_meta.json")); err != nil {
			log.Printf("[artifacts] 上传 audio_meta.json 失败: %v", err)
		}
	}

	// ── 2. 语义 spec 生成（LLM）+ 确定性渲染 + 合成物落盘 ────────
	// 真图素材不在此采购：场景生成两段式按需申请（materials.go），模型要才检索。
	if err := r.compositions(ctx, p); err != nil {
		return err
	}

	// ── 3. 组装 ─────────────────────────────────────────────
	r.emit(id, "stage", "assemble", "running")
	r.Manifest(p, "stage.start", "assemble")
	if err := r.assemble(ctx, p); err != nil {
		return fmt.Errorf("组装失败: %w", err)
	}
	r.emit(id, "stage", "assemble", "done")
	r.Manifest(p, "stage.finish", "assemble")

	// ── 3.5 导演审片（全片连环画 × 预期考卷，见 director.go）：有导演统筹的
	//    长片才跑；意见 high 段重出画面，随后的 check 门禁统一兜机械问题。
	//    审片类故障降级放行（非阻塞哲学同 prereview/frameQA）──────
	if err := r.directorReview(ctx, p); err != nil {
		return err
	}

	// ── 4. check 门禁（自修复：报错回喂重生成 spec）────────────
	if err := r.checkWithRepair(ctx, p, "", ""); err != nil {
		return err
	}

	// ── 4.5 渲染前预审（snapshot 截帧+视觉模型，自修复见 prereview.go）：
	//    画面问题拦在最贵的渲染之前，渲后 frameQA 据此降级为薄保险 ───
	pr, err := r.preReview(ctx, p)
	if err != nil {
		return err
	}

	// ── 5. 渲染成片（按段增量渲 + concat，段片复用见 stageRender）──
	r.emit(id, "stage", "render", "running")
	r.Manifest(p, "stage.start", "render")
	if err := pipeline.Run(ctx, p, "render"); err != nil {
		return fmt.Errorf("渲染失败: %w", err)
	}
	if err := pipeline.Run(ctx, p, "stitch"); err != nil {
		return err
	}
	r.emit(id, "stage", "render", "done")
	r.Manifest(p, "stage.finish", "render")

	// ── 6. 画面审查（抽帧+视觉模型，自修复见 frameQA）：预审干净且无动态特色
	//    判据 → 跳过（预审已覆盖；stitch 已在步骤 5 完成，无缺口）；预审动过的段
	//    → 只复查这些；动态判据或预审未跑（pre_review=false/前置缺失）→ 全量现状
	switch {
	case pr.Ran && !pr.ReviewAll && len(pr.Repaired) == 0:
		r.Manifest(p, "frameqa.skipped_by_prereview", "预审全过，渲后免审")
	case pr.Ran && !pr.ReviewAll:
		if err := r.frameQA(ctx, p, "", strings.Join(pr.Repaired, ",")); err != nil {
			return err
		}
	default:
		if err := r.frameQA(ctx, p, "", ""); err != nil {
			return err
		}
	}
	r.emit(id, "done", "", "renders/main.mp4")
	r.Manifest(p, EventProduceDone, "renders/main.mp4")
	r.jobTerminal(id, "done", "renders/main.mp4", "")
	if r.OnDone != nil {
		r.notifyOnDone(id, "video", "全片制作完成")
	}
	return nil
}

// compositions 生成/复用场景文件 → 清旧帧 → 管线落盘。
func (r *Runner) compositions(ctx context.Context, p *pipeline.Project) error {
	r.emit(p.ID, "stage", "compositions", "running")
	r.Manifest(p, "stage.start", "compositions")
	// 目录字体先行：下载缓存 + 按本文稿子集化（申请书由 LockDesign 落；vend 同款
	// 铁律：模型只有申请权，执行在平台侧）。失败由 font_tool 自己剥族降级，不挡制作。
	if err := r.python(ctx, "ai/font_tool.py", p.Dir, 4*time.Minute); err != nil {
		r.Manifest(p, "fonts.prepare_error", err.Error())
	} else {
		r.Manifest(p, "fonts.prepared", "")
	}
	if err := r.sceneGen(ctx, p, "", "", ""); err != nil {
		return err
	}
	r.clearFrames(p, "")
	if err := pipeline.Run(ctx, p, "compositions"); err != nil {
		return fmt.Errorf("合成物落盘失败: %w", err)
	}
	r.emit(p.ID, "stage", "compositions", "done")
	r.Manifest(p, "stage.finish", "compositions")
	return nil
}

// checkWithRepair check 失败 → 报错回喂 → 重生成 spec → 重渲染 → 重查（最多 3 轮，plan §4.5）。
// instruction 原样传给 sceneGen 的用户要求位（rework 时不能丢）。
// only 非空 = 修复范围限定该段（rework），空 = 全片（首次制作）。
// checkRepairRules 回喂修复轮的规则清单（与修复目标段一起拼进 feedback）。
const checkRepairRules = `
- 重叠/越界：整体重排拉开间距，避开底部字幕带（禁放区见布局规则），必要时减少元素。
- 文字叠压 ✗（content_overlap）：默认＝重排拉开或给上层块加实底衬，与上一条同法。唯一例外＝` +
	"本段版式在 design.md「本片版式」一节标了「声明叠层」（有意叠字手法）——此时给上层元素加属性 " +
	"data-layout-allow-overlap 即合规放行。版式没声明叠层就贴这个属性＝作弊：渲染端会把声明剥掉重检，白修。\n" +
	`  打字机/逐字点亮/字锚高亮类效果的重叠高发区——别用两块文字同位叠放实现：光标做成独立细竖线元素
  在锚字旁侧位移；逐字点亮用同一元素换色/变辉光，不要在锚字上再叠一个同字副本；强调大字与锚字行错位排布。
  两块文字元素一旦 DOM 位置相互覆盖就会被判重叠，调 z-index 没用。` + "\n" +
	"- 对比度 ✗：文字色与它实际压着的背景太接近。从本片设计系统的色板里重新配一对前景/背景：" +
	"办法你定（加深/提亮、换色、给文字加底衬），方向是一步把差距拉开、别在达标线附近试探——" +
	"灰色地带横跳正是历轮修不收敛的根因。检查器的建议色不可信（实测往往不达标），不要照抄；" +
	"也不要为过关全片统一改成一个安全色，各段仍按各自设计走。\n" +
	"- 字体 ✗（font_family_without_font_face）：报的字体渲染端不存在、也没有合法下载渠道（专有字体永远下不了）。" +
	"修复＝把该字体从 font-family 栈里删掉、退到栈内或设计系统字体角色的后备字体——" +
	"这算合规修复，不算违反设计系统（渲染端不存在的字体本来就守护不了设计）；" +
	"除此之外画面一切照旧，不要顺手改构图/配色/动效。\n"

func (r *Runner) checkWithRepair(ctx context.Context, p *pipeline.Project, instruction, only string) error {
	for attempt := 1; attempt <= 3; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.emit(p.ID, "stage", "check", fmt.Sprintf("running（第 %d 次）", attempt))
		r.Manifest(p, "stage.start", "check")
		err := pipeline.Run(ctx, p, "check")
		if err == nil {
			// scene 确定性回放硬闸（P1-1）：hyperframes check 之后、放行之前，
			// 含 scene 的段同 t 两轮截帧像素必须一致；失败按 check 失败进同一修复轮。
			if rerr := r.sceneReplayCheck(ctx, p); rerr != nil {
				err = rerr
			} else {
				// check + 回放都过了：补一次戳（stageCheck 已写），下游 Run(render)/
				// Run(stitch) 里的 stageCheck 据此跳过——门禁对同一输入不该跑三遍
				pipeline.WriteCheckStamp(p)
				r.emit(p.ID, "stage", "check", "done")
				r.Manifest(p, "stage.finish", "check")
				return nil
			}
		}
		if ctx.Err() != nil { // 用户取消 ≠ check 失败，别进修复轮
			return ctx.Err()
		}
		// 廉价修复先行：checker 对对比度项给了建议色就直接回填 HTML 再检，
		// 不烧 LLM 修复轮——LLM 回喂修对比度从未收敛过（同色反复重生成）。
		fixN := applyContrastFixes(p, err.Error())
		fixN += applyGsapOpacityFixes(p, err.Error())
		if fixN > 0 {
			r.emit(p.ID, "stage", "check", fmt.Sprintf("cheap-fix：机械回填 %d 处（对比度/opacity 冲突）", fixN))
			r.Manifest(p, "check.contrast_fix", fmt.Sprintf("%d 处", fixN))
			if perr := r.assemble(ctx, p); perr == nil {
				if cerr := pipeline.Run(ctx, p, "check"); cerr == nil {
					// 捷径放行同样要过 scene 回放门禁：机械回填理论上不影响力
					// 确定性，但门禁口径必须与主路径一致（不兜「理论」）
					if rerr := r.sceneReplayCheck(ctx, p); rerr != nil {
						err = rerr // 回填过了但回放没过：带着回放错误进下面的修复轮
					} else {
						pipeline.WriteCheckStamp(p)
						r.emit(p.ID, "stage", "check", "done")
						r.Manifest(p, "stage.finish", "check")
						return nil
					}
				} else {
					err = cerr
				}
			}
			// 回填后仍不过 → 落到下面的 LLM 修复轮
		}
		if attempt == 3 {
			r.emit(p.ID, "stage", "check", "error: "+err.Error())
			r.Manifest(p, "stage.finish", "check")
			return fmt.Errorf("check 三轮未过: %w", err)
		}
		// 修复目标 = 重做段（可逗号多段）∪ 本轮报错涉及的段：check 是全片门禁，
		// 重做段之外的段报错时若只重生成重做段，那些错误永远修不掉（结构性死锁）。
		targets := failingSegs(blockingCheckErrors(err.Error()))
		for _, id := range strings.Split(only, ",") {
			if id = strings.TrimSpace(id); id != "" && !slices.Contains(targets, id) {
				targets = append([]string{id}, targets...)
			}
		}
		repairOnly := strings.Join(targets, ",")
		feedback := "上一版画面被自动检查拒绝，报错摘要（✗ 行逐项都要修）：\n" + pipeline.TailLines(blockingCheckErrors(err.Error()), 25) +
			"\n修复规则：\n" + checkRepairRules + "逐项修完再输出。"
		// 本轮 check 失败收口（计时），修复轮重生成的时间不计入 check
		r.Manifest(p, "stage.finish", "check")
		r.emit(p.ID, "stage", "check", fmt.Sprintf("repair：重生成画面（段：%s）", repairOnly))
		r.Manifest(p, "check.repair", pipeline.TailLines(err.Error(), 10))
		if err := r.sceneGen(ctx, p, instruction, feedback, repairOnly); err != nil {
			return err
		}
		r.clearFrames(p, repairOnly)
		if err := pipeline.Run(ctx, p, "compositions"); err != nil {
			return fmt.Errorf("合成物落盘失败: %w", err)
		}
		if err := r.assemble(ctx, p); err != nil {
			return fmt.Errorf("组装失败: %w", err)
		}
	}
	// 编译器站岗行：循环体内（成功放行 / ctx 取消 / 第 3 轮烧尽）必 return，正常到不了
	// 这里；真走到了说明轮次边界被人改坏，文案按用户视角给，不泄漏内部字样
	return errors.New("check 修复轮耗尽仍未通过")
}

// segIDRe 段 id（seg01、seg02…）。
var segIDRe = regexp.MustCompile(`seg\d{2,}`)

// blockingCheckErrors 取 check 诊断里真正阻塞门禁的错误行：Layout/Motion/Lint 段的
// ✗ 行。Contrast 段的 ✗ 是 warning 级不判门禁死，且已由 applyContrastFixes 廉价回填
// （LLM 回喂修对比度从未收敛）；把它的行喂进修复反馈只会让健康段被反复重出、
// 把已修好的段改坏——2026-10-10 宗教裁判所片实测：一处 Layout error + 八条 Contrast
// warning 的诊断整包回喂，修复轮连烧三轮重出十余段仍不过，正是本函数要斩的循环。
func blockingCheckErrors(out string) string {
	var keep []string
	inContrast := false
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "Contrast"):
			inContrast = true
			continue
		case strings.HasPrefix(t, "Lint"), strings.HasPrefix(t, "Layout"),
			strings.HasPrefix(t, "Motion"), strings.HasPrefix(t, "Runtime"), strings.HasPrefix(t, "Snapshots"):
			inContrast = false
		}
		if !inContrast && strings.Contains(t, "✗") {
			keep = append(keep, t)
		}
	}
	return strings.Join(keep, "\n")
}

// failingSegs 从 check 诊断提取报错（✗ 行）涉及的段 id：选择器前缀 #segNN-… 与
// 对比度项的 source 路径都算。info 行不取——健康段没必要重生成。
func failingSegs(checkOut string) []string {
	set := map[string]bool{}
	var order []string
	add := func(id string) {
		if !set[id] {
			set[id] = true
			order = append(order, id)
		}
	}
	lines := strings.Split(checkOut, "\n")
	for i, ln := range lines {
		if !strings.HasPrefix(strings.TrimSpace(ln), "✗") {
			continue
		}
		for _, id := range segIDRe.FindAllString(ln, -1) {
			add(id)
		}
		if i+1 < len(lines) { // 调用方喂的是 blockingCheckErrors 过滤后的纯 ✗ 行：
			// 段 id 可能只出现在相邻 ✗ 行（如对比度项只报选择器），顺带扫下一行
			for _, id := range segIDRe.FindAllString(lines[i+1], -1) {
				add(id)
			}
		}
	}
	return order
}

type voiceMeta struct {
	ID        string       `json:"id"`
	DurationS float64      `json:"duration_s"`
	Words     []wordTiming `json:"words"`
}

type wordTiming struct {
	Text  string  `json:"text"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

func loadAudioMeta(p *pipeline.Project) (map[string]voiceMeta, error) {
	b, err := os.ReadFile(p.Artifact("audio_meta.json"))
	if err != nil {
		return nil, fmt.Errorf("缺 audio_meta.json: %w", err)
	}
	var m struct {
		Voices []voiceMeta `json:"voices"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	out := map[string]voiceMeta{}
	for _, v := range m.Voices {
		out[v.ID] = v
	}
	return out, nil
}

// ── scene 确定性回放硬闸（P1-1）───────────────────────────────────

// sceneReplayCheck 含 scene 的段：全片主组合上对每个 scene 段取 3 个全局时刻，
// snapshot 两轮截帧（hyperframes snapshot 不支持 -c 单段，走主组合一次覆盖所有段），
// 像素必须一致——LLM 代码若藏了绕过黑名单的非确定性，这里拦下进修复轮，不带病出片。
// 两轮 snapshot 各一次浏览器冷启（~10-20s），与 scene 段数无关。
// 失败信息按「✗ 段ID」行格式输出，failingSegs 可解析进修复目标。
func (r *Runner) sceneReplayCheck(ctx context.Context, p *pipeline.Project) error {
	sb, err := p.LoadStoryboard()
	if err != nil {
		return nil // 无分镜：非本闸职责，别挡 check
	}
	voices, err := loadAudioMeta(p)
	if err != nil {
		return nil
	}
	// 全局时刻 → 段 的映射（scene 段各取 头0.5s/中点/尾-0.5s，避开段首尾 0.25s
	// 黑场转场的渐变区）。去重+下限钳位与 preReviewTimes 同口径：极短段三点合成
	// 一点时不喂重复 --at，错误也不会因负时刻撞到相邻段头上。
	t2seg := map[float64]string{}
	seen := map[float64]bool{}
	var ats []float64
	acc := 0.0
	for _, seg := range sb.Segments {
		dur := voices[seg.ID].DurationS + contract.SegTail
		if specHasScene(p, seg.ID) {
			for _, t := range []float64{0.5, dur / 2, dur - 0.5} {
				if t < 0.25 {
					continue
				}
				at := acc + t
				if at > acc+dur-0.05 {
					at = acc + dur - 0.05
				}
				key := round2(at)
				if seen[key] {
					continue
				}
				seen[key] = true
				ats = append(ats, key)
				t2seg[key] = seg.ID
			}
		}
		acc += dur
	}
	if len(ats) == 0 {
		return nil
	}
	r.emit(p.ID, "stage", "check", fmt.Sprintf("scene 回放门禁：%d 段 × 3 时刻", len(t2seg)/3))
	atArg := make([]string, len(ats))
	for i, t := range ats {
		atArg[i] = fmt.Sprintf("%.2f", t)
	}
	frames := make([][]string, 2)
	snap := func(atArg, dir string) ([]string, error) {
		// 共用体在 prereview.snapMainFrames（2026-10-19 中-6）：--no-browser-gpu
		// 强制 SwiftShader，渲染器 auto-detect 掷硬币是 seg07 事故根因——两处
		// 必须同一份参数，升级只改 pipeline.HyperframesPkg。
		return snapMainFrames(p, atArg, dir)
	}
	// 逐帧比对（带 LSB 容差 + 感知二级判据）：返回未过容差的帧名与按段去重的报错行。
	textureFrames := 0
	diffRounds := func(dir1, dir2 string, names []string) (badFrames, msgs []string, err error) {
		seen := map[string]bool{}
		unmapped := 0
		for _, name := range names {
			p1, p2 := filepath.Join(p.Artifact(dir1), name), filepath.Join(p.Artifact(dir2), name)
			ia, ib, derr := decodePngPair(p1, p2)
			if derr != nil {
				return nil, nil, derr
			}
			maxD, nD, tot := diffStatsImgs(ia, ib)
			if maxD <= replayMaxChannelDiff && (tot == 0 || float64(nD) <= replayDiffRatioMax*float64(tot)) {
				continue
			}
			// 逐像素超差 → 感知二级判据：纹理相位抖动（噪点/混合层/缩放画布的
			// LSB 级绘制路径差）整块放行，位置/配色/元素有无级真差异仍拦。
			// 复用上面已解码的帧，不再重开重解码
			bm, bp := diffPerceptualImgs(ia, ib)
			if bm <= perceptualMeanMax && bp <= perceptualPixMax {
				textureFrames++
				continue
			}
			segID := ""
			if m := reSnapAt.FindStringSubmatch(name); m != nil {
				if t, perr := strconv.ParseFloat(m[1], 64); perr == nil {
					segID = t2seg[round2(t)]
				}
			}
			if segID == "" {
				// 映射不到段的差异帧不能静默丢：非确定性失败可能恰好藏在解析不出
				// 时刻的帧里（文件名异常/时刻不在考卷表），聚合计数按失败处理
				unmapped++
				continue
			}
			if seen[segID] {
				continue
			}
			seen[segID] = true
			badFrames = append(badFrames, name)
			msgs = append(msgs, fmt.Sprintf("✗ %s: scene 确定性回放未过（%s 两轮差异超感知容差：块均差 %.1f、块内峰值 %.1f——code 藏了非确定性：只用注入的 rng，运动必须写成 t 的函数，禁任何自管时间/真随机路径）", segID, name, bm, bp))
		}
		if unmapped > 0 {
			msgs = append(msgs, fmt.Sprintf("✗ %d 帧两轮差异超容差但无法映射到段（文件名时刻解析失败），按门禁失败处理", unmapped))
		}
		if len(badFrames) == 0 && textureFrames > 0 {
			r.Manifest(p, "check.replay_texture", fmt.Sprintf("%d 帧逐像素超差但感知比对通过（纹理相位抖动，非代码非确定）", textureFrames))
		}
		return badFrames, msgs, nil
	}
	frames[0], err = snap(strings.Join(atArg, ","), "reports/replay/round1")
	if err != nil {
		return err
	}
	frames[1], err = snap(strings.Join(atArg, ","), "reports/replay/round2")
	if err != nil {
		return err
	}
	if len(frames[0]) == 0 {
		return fmt.Errorf("scene 回放 snapshot 未产出帧（--at %s）", strings.Join(atArg, ","))
	}
	if len(frames[0]) != len(frames[1]) {
		return fmt.Errorf("scene 回放两轮帧数不一致（%d vs %d）", len(frames[0]), len(frames[1]))
	}
	badFrames, errs, err := diffRounds("reports/replay/round1", "reports/replay/round2", frames[0])
	if err != nil {
		return err
	}
	if len(badFrames) > 0 {
		// 确认重测：只重截失败时刻。跨浏览器会话的大字号文字光栅化有 LSB 级
		// 抖动（同内容重测即 diff=0），重测通过放行；真非确定性重测依然不一致，
		// 报错进修复轮。
		var confirmAt []string
		for _, name := range badFrames {
			if m := reSnapAt.FindStringSubmatch(name); m != nil {
				confirmAt = append(confirmAt, m[1])
			}
		}
		c1, cerr := snap(strings.Join(confirmAt, ","), "reports/replay/confirm1")
		if cerr != nil {
			return cerr
		}
		c2, cerr := snap(strings.Join(confirmAt, ","), "reports/replay/confirm2")
		if cerr != nil {
			return cerr
		}
		if len(c1) > 0 && len(c1) == len(c2) {
			_, cMsgs, cerr := diffRounds("reports/replay/confirm1", "reports/replay/confirm2", c1)
			if cerr != nil {
				return cerr
			}
			if len(cMsgs) == 0 {
				r.Manifest(p, "check.replay_flake", fmt.Sprintf("%d 帧确认重测通过（文字光栅化抖动）", len(badFrames)))
				errs = nil
			} else {
				errs = cMsgs
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("scene 回放门禁:\n%s", strings.Join(errs, "\n"))
	}
	r.Manifest(p, "check.scene_replay", fmt.Sprintf("%d 段 diff=0", len(t2seg)/3))
	return nil
}

var reSnapAt = regexp.MustCompile(`at-([\d.]+)s\.png$`)

func round2(f float64) float64 { return math.Round(f*100) / 100 }

// 回放比对容差：加色混合下光栅化存在 LSB 级抖动（实测 900 粒子 scene：
// 两轮 16/2073600 像素差 1/255；拉普拉斯妖 seg38：1/2073600 像素差 8/255——
// alpha 临界值上抗锯齿覆盖判断翻转，非逻辑非确定）。大字号文字跨浏览器会话
// 抖动更大（实测 96px 标题带：1394/2073600=0.067% 像素差 1/255，同内容重测
// diff=0，见确认重测）。真逻辑非确定性（真随机/自管时间）是大面积大差异且
// 重现稳定：占比闸仍主拦，通道差阈值只防单像素误杀，确认重测拦"抖动撞线"。
const (
	replayMaxChannelDiff = 8    // 任一像素通道差超此值 = 硬失败（实测 GPU 噪声上限内）
	replayDiffRatioMax   = 2e-3 // 差异像素占比超此值 = 疑似失败（≈0.2%，实测文字抖动 0.067% 的 3 倍余量），进确认重测
)

// 感知二级判据：纹理类合规场景（噪点颗粒/混合模式扫描线/锥形渐变/被缩放的画布）
// 即使固定渲染器也存在绘制路径级的 LSB 相位抖动（2026-10-09 seg07 实测：强制
// SwiftShader 双会话 69 帧中 3 帧块均差 ≤4.3、块内峰值 ≤16；auto-detect 下渲染器
// 掷硬币可把差异放大到 45% 像素/通道差 16——已用 --no-browser-gpu 根除）。逐像素
// 等值判据对这类抖动必然误杀。8×8 盒滤波降采样把独立噪声平均掉：位置/配色/
// 元素有无级的结构差异（LLM 作弊的实际形态）在块统计里保留，纹理相位差抹平。
const (
	perceptualBlock   = 8    // 盒滤波边长（px）
	perceptualStep    = 2    // 块内抽样步长（均值口径足够）
	perceptualMeanMax = 8.0  // 块均差上限（0-255；实测合规最坏 4.3，1.9 倍裕量）
	perceptualPixMax  = 32.0 // 块内单像素差上限（实测合规最坏 16，2 倍裕量）
)

// decodePngPair 读两 PNG 并解码。调用方拿到原始 image.Image：逐像素比对走
// NRGBA 快路径，感知二级判据复用同一对已解码帧（超差帧重开+重解码曾是回放
// 门禁里最贵的一步）。
func decodePngPair(pathA, pathB string) (ia, ib image.Image, err error) {
	fa, err := os.Open(pathA)
	if err != nil {
		return nil, nil, err
	}
	defer fa.Close()
	ia, err = png.Decode(fa)
	if err != nil {
		return nil, nil, err
	}
	fb, err := os.Open(pathB)
	if err != nil {
		return nil, nil, err
	}
	defer fb.Close()
	ib, err = png.Decode(fb)
	if err != nil {
		return nil, nil, err
	}
	return ia, ib, nil
}

// nrgbaAt8 与 image.NRGBA.RGBA()>>8 同口径取预乘 8bit 通道。快路径免掉每像素
// 两次 color.Color 接口调用与转换（1080×1920 单帧 200 万像素，回放门禁几十帧
// 全走这里）；a=255（截图常态）直读原始字节。
func nrgbaAt8(px *image.NRGBA, x, y int) (r, g, b uint8) {
	i := px.PixOffset(x, y)
	a := px.Pix[i+3]
	if a == 0xff {
		return px.Pix[i], px.Pix[i+1], px.Pix[i+2]
	}
	if a == 0 {
		return 0, 0, 0
	}
	prem := func(c uint8) uint8 {
		return uint8((uint32(c) * 0x101 * uint32(a) / 0xff) >> 8)
	}
	return prem(px.Pix[i]), prem(px.Pix[i+1]), prem(px.Pix[i+2])
}

// diffPerceptualImgs 感知比对（吃已解码对象）：两帧各做 8×8 盒滤波降采样后
// 逐块比较，返回（最坏块均差, 最坏块内单像素差），0-255 口径（与 diffStatsImgs
// 同源像素口径：At().RGBA()>>8 的 0-255 预乘值）。尺寸不一致按最严重处理
// （报不崩也放行不了）。（文件路径版 pngDiffPerceptual 只剩测试在用，已移
// replay_perceptual_test.go——2026-10-19 死代码清仓）
func diffPerceptualImgs(ia, ib image.Image) (blockMean, blockMax float64) {
	if ia.Bounds() != ib.Bounds() {
		return 1e9, 1e9
	}
	atOf := func(m image.Image) func(x, y int) (uint8, uint8, uint8) {
		if n, ok := m.(*image.NRGBA); ok {
			return func(x, y int) (uint8, uint8, uint8) { return nrgbaAt8(n, x, y) }
		}
		return func(x, y int) (uint8, uint8, uint8) {
			r, g, b, _ := m.At(x, y).RGBA()
			return uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)
		}
	}
	atA, atB := atOf(ia), atOf(ib)
	abs := func(a, c uint8) float64 {
		if a > c {
			return float64(a - c)
		}
		return float64(c - a)
	}
	b := ia.Bounds()
	for by := b.Min.Y; by < b.Max.Y; by += perceptualBlock {
		for bx := b.Min.X; bx < b.Max.X; bx += perceptualBlock {
			var sum float64
			n := 0
			blockPeak := 0.0
			for y := by; y < by+perceptualBlock && y < b.Max.Y; y += perceptualStep {
				for x := bx; x < bx+perceptualBlock && x < b.Max.X; x += perceptualStep {
					ar, ag, ab := atA(x, y)
					br, bg, bb := atB(x, y)
					d := (abs(ar, br) + abs(ag, bg) + abs(ab, bb)) / 3
					sum += d
					if d > blockPeak {
						blockPeak = d
					}
					n++
				}
			}
			if n == 0 {
				continue
			}
			if m := sum / float64(n); m > blockMean {
				blockMean = m
			}
			if blockPeak > blockMax {
				blockMax = blockPeak
			}
		}
	}
	return blockMean, blockMax
}

// diffStatsImgs 逐像素比较（吃已解码对象）→ (最大通道差, 差异像素数, 总像素)。
// NRGBA（hyperframes snapshot 的实际格式）直接扫 Pix 数组；其余格式退回 At()
// 接口路径，口径不变。（文件路径版 pngDiffStats 只剩测试在用，已移
// replay_perceptual_test.go）
func diffStatsImgs(ia, ib image.Image) (maxDiff, nDiff, total int) {
	if ia.Bounds() != ib.Bounds() {
		return 1 << 30, 1 << 30, 0 // 尺寸不一致按最严重处理
	}
	na, aok := ia.(*image.NRGBA)
	nb, bok := ib.(*image.NRGBA)
	b := ia.Bounds()
	if aok && bok {
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				ra, ga, ba := nrgbaAt8(na, x, y)
				rb, gb, bb := nrgbaAt8(nb, x, y)
				total++
				d := absInt(int(ra) - int(rb))
				if d2 := absInt(int(ga) - int(gb)); d2 > d {
					d = d2
				}
				if d3 := absInt(int(ba) - int(bb)); d3 > d {
					d = d3
				}
				if d > 0 {
					nDiff++
				}
				if d > maxDiff {
					maxDiff = d
				}
			}
		}
		return maxDiff, nDiff, total
	}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			ra, ga, ba, _ := ia.At(x, y).RGBA()
			rb, gb, bb, _ := ib.At(x, y).RGBA()
			total++
			d := absInt(int(ra>>8) - int(rb>>8))
			if d2 := absInt(int(ga>>8) - int(gb>>8)); d2 > d {
				d = d2
			}
			if d3 := absInt(int(ba>>8) - int(bb>>8)); d3 > d {
				d = d3
			}
			if d > 0 {
				nDiff++
			}
			if d > maxDiff {
				maxDiff = d
			}
		}
	}
	return maxDiff, nDiff, total
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// translateSubtitles 双语字幕（M-C）：project.json bi_subtitle=true 时，把每段字幕
// 短语（词区间文本）一次性 LLM 翻译成英文短句，写回 audio_meta.json 的
// voices[].phrases_en（与 phrases 同序）；渲染层 subtitle.phrases_html 据此出英文次行。
// 幂等：已全部有 phrases_en 跳过；LLM 失败返回 nil（英文缺省不显示，不挡出片）。
func (r *Runner) translateSubtitles(ctx context.Context, p *pipeline.Project) error {
	var meta struct {
		BiSubtitle bool `json:"bi_subtitle"`
	}
	if b, err := os.ReadFile(p.Artifact("project.json")); err != nil || json.Unmarshal(b, &meta) != nil || !meta.BiSubtitle {
		return nil // 未开启 = 正常路径，不是错误
	}
	raw, err := os.ReadFile(p.Artifact("audio_meta.json"))
	if err != nil {
		return err
	}
	var am map[string]any
	if json.Unmarshal(raw, &am) != nil {
		return errors.New("audio_meta.json 解析失败")
	}
	voices, _ := am["voices"].([]any)
	type segJob struct {
		ID      string   `json:"id"`
		Phrases []string `json:"phrases"`
	}
	jobs := []segJob{}
	byIdx := map[string]int{}
	for i, v := range voices {
		vm, _ := v.(map[string]any)
		id, _ := vm["id"].(string)
		byIdx[id] = i
		ph, _ := vm["phrases"].([]any)
		words, _ := vm["words"].([]any)
		if len(ph) == 0 || len(words) == 0 {
			continue
		}
		if en, ok := vm["phrases_en"].([]any); ok && len(en) == len(ph) {
			continue // 已翻译（幂等）
		}
		j := segJob{ID: id}
		for _, pr := range ph {
			pm, _ := pr.([]any)
			i0, i1 := 0, len(words)-1
			if len(pm) == 2 {
				if f, ok := pm[0].(float64); ok {
					i0 = int(f)
				}
				if f, ok := pm[1].(float64); ok {
					i1 = int(f)
				}
			}
			// 区间钳位与 voices 的防御同思路：audio_meta.json 被手改/损坏时
			// i0 可能为负、i1 可能越界，words[i0] 裸索引会 panic 崩掉整个服务
			if i0 < 0 {
				i0 = 0
			}
			if i1 >= len(words) {
				i1 = len(words) - 1
			}
			var b strings.Builder
			for k := i0; k <= i1 && k < len(words); k++ {
				wm, _ := words[k].(map[string]any)
				if t, ok := wm["text"].(string); ok {
					b.WriteString(t)
				}
			}
			j.Phrases = append(j.Phrases, strings.TrimSpace(b.String()))
		}
		if len(j.Phrases) > 0 {
			jobs = append(jobs, j)
		}
	}
	if len(jobs) == 0 {
		return nil
	}
	prov, perr := llm.FromEnv(llm.RoleDialogue)
	if perr != nil {
		return perr
	}
	system := "你是视频字幕翻译器，只输出 JSON。把中文字幕短语翻成英文短句：字幕风格（短、口语、不逐字）、专有名词准确、每条独立成句不加引号。"
	user := "把下面每段的中文字幕短语翻译成英文，输出同构 JSON：[{\"id\":\"segXX\",\"phrases\":[\"英文短语\", …]}]，段顺序与短语顺序必须与输入完全一致。\n\n" + pipeline.MustJSON(jobs)
	var out []segJob
	// 单发任务统一记账（「过程」页）：翻译一次调用，留痕 = 请求 + 响应各一条。
	at := agentlog.Begin(p, "translate", "双语字幕翻译")
	if rec := at.Rec(); rec != nil {
		rec.Message(openai.ChatCompletionMessage{Role: openai.ChatMessageRoleSystem, Content: system})
		rec.Message(openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: user})
	}
	// low：机械翻译不吃深思考，跟随端点默认（max）是白烧 reasoning token
	u, err := prov.GenerateJSONEffort(ctx, system, user, &out, llm.EffortLow)
	at.Add(u)
	if err != nil {
		at.Done(err)
		return err
	}
	if rec := at.Rec(); rec != nil {
		rec.Message(openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: pipeline.MustJSON(out)})
	}
	at.Done(nil)
	got := map[string][]string{}
	for _, o := range out {
		if o.ID != "" && len(o.Phrases) > 0 {
			got[o.ID] = o.Phrases
		}
	}
	n := 0
	for _, j := range jobs {
		en, ok := got[j.ID]
		if !ok || len(en) != len(j.Phrases) {
			continue // 数量不一致的段放弃（宁缺勿错位）
		}
		i := byIdx[j.ID]
		// JSON 结构合法但 voices[i] 不是对象（如被手改成数组）→ 裸断言会 panic 崩进程
		vm, ok := voices[i].(map[string]any)
		if !ok {
			continue
		}
		vm["phrases_en"] = toAnySlice(en)
		n += len(en)
	}
	if n == 0 {
		return errors.New("翻译返回为空/错位")
	}
	nb, err := json.MarshalIndent(am, "", "  ")
	if err != nil {
		return fmt.Errorf("audio_meta 序列化失败（翻译回写中止，未动盘）: %w", err)
	}
	if err := pipeline.WriteFileAtomic(p.Artifact("audio_meta.json"), nb, 0o644); err != nil {
		return err
	}
	r.Manifest(p, "produce.bi_subtitle", fmt.Sprintf("%d 条英文短语", n))
	return nil
}

func toAnySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// 方法库（data/library/methods/，清单 methods.json）：spec 生成按段内容注入的领域参考。
// scene 域 = 确定性画布参考实现（成活率靠仿写不靠自由发挥）；comp 域 = 高级感构图公式
// （大留白单主体/暗底浅字/段间衔接，三篇参考片分析的沉淀）。清单驱动：加方法 =
// 加文件加 methods.json 条目，不改代码（methods_test.go 守卫清单与文件一致）。
type methodEntry struct {
	Domain   string   `json:"domain"` // scene | comp
	File     string   `json:"file"`   // methods/ 下相对路径
	Title    string   `json:"title"`
	Keywords []string `json:"keywords"`
}

// methodsCache 方法库文件缓存（key = 路径 + mtime + size）：methodsFor 在 sceneGen
// 并发区每段每轮都调，清单与方法正文此前每次重读盘是白付的 I/O（库是仓库只读内容，
// 运行期基本不变）。mtime 入键：库文件真被改了自动失效；锁保并发安全；缓存只增
// 不清会随文件改动积累陈旧键，超限整体重置。
var (
	methodsCacheMu sync.Mutex
	methodsCache   = map[string][]byte{}
)

func readCachedFile(path string) ([]byte, bool) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, false
	}
	key := fmt.Sprintf("%s|%d|%d", path, st.ModTime().UnixNano(), st.Size())
	methodsCacheMu.Lock()
	defer methodsCacheMu.Unlock()
	if b, ok := methodsCache[key]; ok {
		return b, true
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	if len(methodsCache) > 128 {
		methodsCache = map[string][]byte{}
	}
	methodsCache[key] = b
	return b, true
}

func loadMethods(rootDir string) []methodEntry {
	b, ok := readCachedFile(filepath.Join(rootDir, "data", "library", "methods", "methods.json"))
	if !ok {
		return nil
	}
	var ms []methodEntry
	if json.Unmarshal(b, &ms) != nil {
		return nil
	}
	return ms
}

// methodsFor 段内容命中方法关键词 → 返回 (scene 注入块, comp 注入块)。
// 按相关性挑选（非清单序）：命中词数×词长权重打分，scene 取最高分一份参考实现，
// comp 取 top2 构图纪律。匹配面 = 标题+画面简述+旁白（旁白信息量最大）。
// 两者独立命中：一段可以既拿弦图参考又拿暗底浅字纪律。
func methodsFor(rootDir string, seg contract.Segment) (sceneBlock, compBlock string) {
	hay := seg.Key + " " + seg.VisualBrief + " " + seg.Narration
	type scored struct {
		m    methodEntry
		body string
		s    int
	}
	var scenes, comps []scored
	for _, m := range loadMethods(rootDir) {
		s := methodScore(m, hay)
		if s <= 0 {
			continue
		}
		b, ok := readCachedFile(filepath.Join(rootDir, "data", "library", "methods", filepath.FromSlash(m.File)))
		if !ok {
			continue
		}
		sc := scored{m: m, body: string(b), s: s}
		switch m.Domain {
		case "scene":
			scenes = append(scenes, sc)
		case "comp":
			comps = append(comps, sc)
		}
	}
	rank := func(ss []scored) { // 分数降序，同分保持清单序（stable）
		sort.SliceStable(ss, func(i, j int) bool { return ss[i].s > ss[j].s })
	}
	rank(scenes)
	rank(comps)
	if len(scenes) > 0 {
		m := scenes[0]
		sceneBlock = fmt.Sprintf("\n## scene 参考实现（可运行的起点：参数、造型、机制都鼓励改写成本段的画面）\n```\n%s\n```\n", m.body)
	}
	if len(comps) > 2 {
		comps = comps[:2]
	}
	if len(comps) > 0 {
		parts := make([]string, len(comps))
		for i, c := range comps {
			parts[i] = "### " + c.m.Title + "\n" + strings.TrimSpace(c.body)
		}
		compBlock = "\n## 构图参考（别的片用过有效的手法，仅作启发——不是纪律；与本片设计系统或画面提示冲突时，一律以设计系统和画面提示为准，按本段气质自由取舍）\n" + strings.Join(parts, "\n\n") + "\n"
	}
	return sceneBlock, compBlock
}

// methodScore 相关性分：命中一个关键词 +1，长词（更专一）按每 4 字再加权。
func methodScore(m methodEntry, hay string) int {
	s := 0
	for _, kw := range m.Keywords {
		if strings.Contains(hay, kw) {
			s += 1 + len([]rune(kw))/4
		}
	}
	return s
}

// ProjectCanvas 项目画幅（project.json aspect；旧项目无字段按 16:9，与已渲成片一致）。
func ProjectCanvas(p *pipeline.Project) contract.Canvas {
	b, err := os.ReadFile(p.Artifact("project.json"))
	if err != nil {
		return contract.CanvasFor("16:9")
	}
	var meta struct {
		Aspect string `json:"aspect"`
	}
	_ = json.Unmarshal(b, &meta)
	if meta.Aspect == "" {
		meta.Aspect = "16:9"
	}
	return contract.CanvasFor(meta.Aspect)
}

// projectDirection 项目已确认/已起草的风格方向（style_samples.json）。
func projectDirection(p *pipeline.Project) string {
	var ss struct {
		Direction string `json:"direction"`
	}
	if b, err := os.ReadFile(p.Artifact("style/style_samples.json")); err == nil &&
		json.Unmarshal(b, &ss) == nil {
		return ss.Direction
	}
	return ""
}

// skinNote 皮肤槽位摘要：spec 措辞向槽位收敛（色名/字体名直接写进画面语言）。
func skinNote(p *pipeline.Project) string {
	var sk contract.Skin
	b, err := os.ReadFile(p.Artifact("style/skin.json"))
	if err != nil || json.Unmarshal(b, &sk) != nil || sk.Slots.BG == "" {
		return ""
	}
	s := sk.Slots
	note := fmt.Sprintf("\n- 皮肤槽位（标准件配色以此为准）：底色 %s / 文字 %s / 强调 %s / 字体 %s", s.BG, s.TXT, s.Accent, s.Font)
	if s.Mute != "" {
		note += " / 次级 " + s.Mute
	}
	if s.Surface != "" {
		note += " / 面板底 " + s.Surface
	}
	note += "\n- 画面措辞往皮肤气质靠（底色/强调色的邻近词）；custom 特效底也用这套色"
	return note
}

// visImg 一张注入多模态模型的图：URL 给模型（data URL），Path/Label 给「过程」页
// 对话留痕——前端按项目内路径渲染缩略图，点击放大看原文件。
type visImg struct {
	URL   string
	Path  string // 项目内相对路径（正斜杠）
	Label string
}

// visURLs 批量取 data URL（喂模型用）。
func visURLs(vis []visImg) []string {
	urls := make([]string, 0, len(vis))
	for _, v := range vis {
		urls = append(urls, v.URL)
	}
	return urls
}

// visRefs 批量转留痕图片引用。
func visRefs(vis []visImg) []llm.ImgRef {
	refs := make([]llm.ImgRef, 0, len(vis))
	for _, v := range vis {
		refs = append(refs, llm.ImgRef{URL: v.URL, Path: v.Path, Label: v.Label})
	}
	return refs
}

// projRel 项目内绝对路径 → 正斜杠相对路径（留痕图片引用用；前缀不符原样返回）。
func projRel(p *pipeline.Project, abs string) string {
	return strings.TrimPrefix(filepath.ToSlash(abs), filepath.ToSlash(p.Dir)+"/")
}

// styleSampleImages 已截图的风格样张（style/sample-N.png，ai/snap_samples.py 产物）→ 多模态注入图。
// spec 生成时附给多模态模型：看真图选 kind/配色/调性，替代纯文字风格注入。最多 3 张，缺文件返回 nil。
func styleSampleImages(p *pipeline.Project) []visImg {
	var vis []visImg
	for i := 0; i < 3; i++ {
		rel := fmt.Sprintf("style/sample-%d.png", i)
		path := p.Artifact(rel)
		if _, err := os.Stat(path); err != nil {
			break
		}
		// 缩到 640px JPEG 再注入：原始 PNG 直接 base64 曾把每次多模态调用的
		// 载荷白抬 ~200KB；样张是风格基准，640 宽足够看构图配色质感
		url := ""
		if u, err := shrinkSampleImage(path); err == nil {
			url = u
		} else if b, err := os.ReadFile(path); err == nil {
			url = "data:image/png;base64," + base64.StdEncoding.EncodeToString(b)
		} else {
			break
		}
		vis = append(vis, visImg{URL: url, Path: rel, Label: fmt.Sprintf("风格样张 %d", i+1)})
	}
	return vis
}

// shrinkSampleImage 样张 PNG → 640 宽 JPEG dataURL（读盘+缩图+编码一体）。
func shrinkSampleImage(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	src, _, err := image.Decode(f)
	if err != nil {
		return "", err
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	tw, th := 640, 640*h/w
	if tw > w {
		tw, th = w, h
	}
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	for y := 0; y < th; y++ {
		sy := b.Min.Y + y*h/th
		for x := 0; x < tw; x++ {
			dst.Set(x, y, src.At(b.Min.X+x*w/tw, sy))
		}
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 75}); err != nil {
		return "", err
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(out.Bytes()), nil
}

// feedbackBlock 回喂块：意见清单 + 上一版场景文件。prevHTML 非空时模型才看得见
// 旧稿——"定点修改"的前提是给它原稿（2026-10-18 实测：修复轮看不到旧版=每轮
// 推倒重来，修好旧毛病随机带出新毛病，判卷逐轮漂移永不收敛）。
func feedbackBlock(f, prevHTML string) string {
	if f == "" {
		return ""
	}
	b := "\n## 必须修正\n" + f + "\n"
	if prevHTML != "" {
		b += "\n## 上一版场景文件（修复底稿：只动「必须修正」点名的元素，其余逐字保留）\n```html\n" + prevHTML + "\n```\n"
	}
	return b
}

// ── 子进程与工具 ─────────────────────────────────────────────

// assembleMu assemble.py 的进程内互斥（2026-10-19 中-15）：写池 ≤4 worker 各自
// 全量重跑 assemble，index/.hf-seg/*.html 与 assets/hf.js 的文件写会被并发踩——
// 另一 worker 的 ensureSegCheckDir 恰在拷 .hf-seg/<seg>.html 时读到半截 →
// check 幻影失败白烧 LLM 修复轮。全局一把锁：组装是秒级轻活，串行化无感
// （瓶颈在渲染不在组装；Python 侧另有 tmp+rename 原子写双保险）。
var assembleMu sync.Mutex

// assemble 组装工位统一入口：互斥后跑 ai/assemble.py（见 assembleMu 注释）。
func (r *Runner) assemble(ctx context.Context, p *pipeline.Project) error {
	assembleMu.Lock()
	defer assembleMu.Unlock()
	return r.python(ctx, "ai/assemble.py", p.Dir, 2*time.Minute)
}

// python 跑仓库 ai/ 脚本（cwd = 仓库根）。任务取消时连带杀掉子进程；走
// pipeline.CommandContext——整棵进程树击杀，python 拉起的孙进程不残留。
// 成功时子进程 stdout 只回显尾部（tts_align 每段一行动辄上百行，全量回显淹没日志）。
func (r *Runner) python(ctx context.Context, script string, projDir string, timeout time.Duration) error {
	py := pipeline.PythonExe()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := pipeline.CommandContext(ctx, py, filepath.Join(r.RootDir, script), projDir)
	cmd.Dir = r.RootDir
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil { // 被取消/超时杀掉：报可识别的 ctx 错误而非 killed
			return ctx.Err()
		}
		return fmt.Errorf("%s 退出: %w\n%s", script, err, pipeline.TailLines(buf.String(), 15))
	}
	const tailN = 80
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) > tailN {
		lines = append([]string{"…（前略，仅保留尾部 " + fmt.Sprintf("%d", tailN) + " 行）"}, lines[len(lines)-tailN:]...)
	}
	fmt.Println(strings.Join(lines, "\n"))
	return nil
}

// clearFrames 清旧帧（落盘阶段只补缺失文件，不清不会重写）。
// only 非空只清该段；空清全部。注意不动 llm/comp-*.json——那是
// sceneGen 的产物、compositions 落盘阶段的输入，删了会退化成等待人工。
func (r *Runner) clearFrames(p *pipeline.Project, only string) {
	if only != "" {
		for _, id := range strings.Split(only, ",") {
			if id = strings.TrimSpace(id); id == "" {
				continue
			}
			_ = os.Remove(p.Artifact("compositions/frames/" + id + ".html"))
			_ = os.Remove(p.Artifact("compositions/registry/" + id + ".json"))
		}
		return
	}
	files, _ := filepath.Glob(filepath.Join(p.Dir, "compositions", "frames", "*.html"))
	for _, f := range files {
		_ = os.Remove(f)
	}
}

// contrastSuggRe 抓 checker 对比度诊断对：✗ 选择器 比值:1 (need …) \n Try rgb(..); source frames/segNN.html
var contrastSuggRe = regexp.MustCompile(
	`✗[ ]*([^\r\n]+?)\s+[0-9]+(?:\.[0-9]+)?:1\s*\(need[^)]*\)\s*\n\s*Try\s+(rgb\([^)]*\))\s*;\s*source\s+(\S+)`)

// gsapOpacityNoopRe 抓 gsap_from_opacity_noop 诊断对：✗ "#元素" 有 CSS opacity:0
// 与 gsap.from(opacity:0) 冲突 \n …frames\segNN.html #元素 t=…
var gsapOpacityNoopRe = regexp.MustCompile(
	`gsap_from_opacity_noop:\s*"#([\w-]+)"[^\r\n]*\n\s*\S*frames[\\/]([\w-]+\.html)`)

// gsapInlineOpacityRe / gsapRuleOpacityRe 抓任意元素的 opacity:0 声明（内联 style /
// CSS 规则两种形态），元素 id 捕获后在替换回调里比对：修复目标逐个变，包级预编译
// 免掉循环内 regexp.MustCompile 反复重编（对齐 contrastSuggRe 的既有风格）。
var (
	gsapInlineOpacityRe = regexp.MustCompile(`(id="([\w-]+)"[^>]*?style="[^"]*?)opacity:\s*0\s*(;|")`)
	gsapRuleOpacityRe   = regexp.MustCompile(`(#([\w-]+)\s*\{[^}]*?)opacity:\s*0\s*(;|\})`)
)

// applyGsapOpacityFixes gsap_from_opacity_noop 的机械修复：CSS 里藏着的 opacity:0
// 与 gsap.from({opacity:0}) 打架（from 的 immediateRender 本就负责初始隐藏，CSS
// 那份是画蛇添足），直接从该元素的内联 style / CSS 规则里剥掉声明再重检。
// LLM 修这个是掷骰子（同一元素反复重画还可能换姿势再犯），机械删除零成本收敛。
// 剥除必须带上分隔符（;/}/"）——裸 `opacity:0` 会误伤 opacity:0.5。
func applyGsapOpacityFixes(p *pipeline.Project, checkOut string) int {
	type fix struct{ file, elem string }
	seen := map[fix]bool{}
	var fixes []fix
	for _, m := range gsapOpacityNoopRe.FindAllStringSubmatch(checkOut, -1) {
		f := fix{m[2], m[1]}
		if !seen[f] {
			seen[f] = true
			fixes = append(fixes, f)
		}
	}
	if len(fixes) == 0 {
		return 0
	}
	perFile := map[string][]string{}
	for _, f := range fixes {
		perFile[f.file] = append(perFile[f.file], f.elem)
	}
	n := 0
	for file, elems := range perFile {
		path := p.Artifact("compositions/frames/" + file)
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		html := string(b)
		orig := html
		for _, elem := range elems {
			// 剥除必须带上分隔符（;/}/"）——裸 `opacity:0` 会误伤 opacity:0.5；
			// 捕获的 id 不是本轮修复目标时原样保留（等价于旧的按元素拼正则）
			html = gsapInlineOpacityRe.ReplaceAllStringFunc(html, func(m string) string {
				g := gsapInlineOpacityRe.FindStringSubmatch(m)
				if g[2] != elem {
					return m
				}
				return g[1] + g[3]
			})
			html = gsapRuleOpacityRe.ReplaceAllStringFunc(html, func(m string) string {
				g := gsapRuleOpacityRe.FindStringSubmatch(m)
				if g[2] != elem {
					return m
				}
				return g[1] + g[3]
			})
		}
		if html == orig {
			continue
		}
		if err := os.WriteFile(path, []byte(html), 0o644); err == nil {
			n += len(elems)
		}
	}
	return n
}

// applyContrastFixes 把 checker 给的对比度建议色直接回填到段帧 HTML（追加
// data-contrast-fix 样式块，!important 压过内联样式），返回本次新回填的处数。
// 已在 HTML 里的同条规则跳过（内容去重）：LLM 修复轮重生成 HTML 会冲掉旧补丁，
// 此时规则缺席会重新回填；规则在却还报同项，说明该建议色无效，不重复打转。
func applyContrastFixes(p *pipeline.Project, checkOut string) int {
	type fix struct {
		file, css string
	}
	var fixes []fix
	for _, m := range contrastSuggRe.FindAllStringSubmatch(checkOut, -1) {
		sel, color, src := strings.TrimSpace(m[1]), m[2], m[3]
		file, ok := segFrameFile(src)
		if !ok {
			continue // 只动段帧（caps/index 的建议色不碰——字幕配色是平台守卫的事）
		}
		fixes = append(fixes, fix{file, sel + "{color:" + color + "!important}"})
	}
	if len(fixes) == 0 {
		return 0
	}
	perFile := map[string][]string{}
	for _, f := range fixes {
		perFile[f.file] = append(perFile[f.file], f.css)
	}
	n := 0
	for rel, rules := range perFile {
		path := p.Artifact(rel)
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		html := string(b)
		var fresh []string
		for _, css := range rules {
			if !strings.Contains(html, css) {
				fresh = append(fresh, css)
			}
		}
		if len(fresh) == 0 {
			continue
		}
		block := "<style data-contrast-fix=\"1\">" + strings.Join(fresh, "") + "</style>"
		if i := strings.Index(html, `<style data-contrast-fix`); i >= 0 {
			if j := strings.Index(html[i:], "</style>"); j >= 0 {
				html = html[:i] + block + html[i+j+len("</style>"):]
			} else {
				continue
			}
		} else if i := strings.Index(html, "</body>"); i >= 0 {
			html = html[:i] + block + "\n" + html[i:]
		} else {
			html += block
		}
		if err := os.WriteFile(path, []byte(html), 0o644); err == nil {
			n += len(fresh)
		}
	}
	return n
}
