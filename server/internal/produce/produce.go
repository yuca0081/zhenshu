// Package produce：制作任务运行器（确定性编排，LLM 只在需要的点上介入）。
//
// 链路（v2）：tts（python）→ 每段场景文件（LLM 直写 HTML + 契约 lint 门禁，
//
//	见 scenegen.go / docs/contract.md）→ 管线落盘合成物 → 组装（python assemble）
//	→ check 门禁（失败带着报错回喂重生成，最多 2 轮）→ 渲染 → 成片。
//
// 每步 SSE 广播 stage 事件；产物落盘可从任意步重放（manifest 记账）。
package produce

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/events"
	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/pipeline"
	"webpage-video-agent/server/internal/store"
)

type Runner struct {
	DataDir string // 仓库根 data/（项目在 data/projects/<id>）
	RootDir string // 仓库根（ai/ 脚本所在）
	Hub     *events.Hub
	Store   store.Store // render_jobs 记账（nil = 不记账，测试场景）；manifest 仍是事实源
	OnDone  func(projectID, status, detail string) // 终态回写（status: video|failed|cancelled；detail 供聊天汇报失败原因等）

	wg       sync.WaitGroup  // 在跑的制作/重做 goroutine（优雅停机等待终态落盘）
	stopping atomic.Bool     // 停机中：取消语义从「用户取消」改「服务停机中断」，并拒绝新任务
}

// manifest 制作事件协议（跨包契约：本包写、Reconcile 启动对账扫、agent.lastProduceResult
// 读——此前三处各写魔法字符串，改名一处即静默破坏对账与终态汇报，收敛为唯一来源）。
const (
	EventProduceStart     = "produce.start"
	EventProduceDone      = "produce.done"
	EventProduceError     = "produce.error"
	EventProduceCancelled = "produce.cancelled"
	EventReworkStart      = "rework.start"
	EventReworkDone       = "rework.done"
	EventReworkError      = "rework.error"
	EventReworkCancelled  = "rework.cancelled"
)

var (
	mu      sync.Mutex
	running = map[string]context.CancelFunc{}
)

// Start 启动制作（同项目幂等：已在跑则返回 false）。
func (r *Runner) Start(projectID string) (bool, error) {
	mu.Lock()
	defer mu.Unlock()
	if r.stopping.Load() {
		return false, errors.New("服务正在停机，不再接受新任务")
	}
	if _, live := running[projectID]; live {
		return false, nil
	}
	p := pipeline.NewProject(r.DataDir, projectID)
	if _, err := os.Stat(p.Dir); err != nil {
		return false, fmt.Errorf("项目不存在: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	running[projectID] = cancel
	p.Manifest(EventProduceStart, "") // 启动对账依据：有 start 无终态 = 进程死在中途（Reconcile 补写）
	r.jobStart(projectID, "produce")
	r.wg.Add(1)
	go func() {
		defer func() {
			mu.Lock()
			delete(running, projectID)
			mu.Unlock()
			r.wg.Done()
		}()
		// panic 兜底：audio_meta 等产物结构异常不该崩掉整个服务（goroutine panic 直接退进程）
		err := safeRun(func() error { return r.run(ctx, p) })
		if errors.Is(err, context.Canceled) {
			detail := "用户取消制作"
			if r.stopping.Load() {
				detail = "服务停机，制作中断（产物已保留，重启后重新开工即按段复用）"
			}
			r.emit(projectID, "stage", "pipeline", "cancelled")
			r.emit(projectID, "cancelled", "", detail)
			r.Manifest(p, EventProduceCancelled, detail)
			r.jobTerminal(projectID, "cancelled", "", detail)
			if r.OnDone != nil {
				r.OnDone(projectID, "cancelled", detail)
			}
			return
		}
		if err != nil {
			r.emit(projectID, "stage", "pipeline", "error: "+err.Error())
			r.emit(projectID, "error", "", err.Error())
			r.Manifest(p, EventProduceError, err.Error())
			r.jobTerminal(projectID, "failed", "", err.Error())
			if r.OnDone != nil {
				r.OnDone(projectID, "failed", err.Error())
			}
		}
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

// Cancel 请求取消进行中的制作/重做（协作式：杀子进程 + 阶段边界退出）。
// 返回 false = 该项目没有在跑的任务。
func (r *Runner) Cancel(projectID string) bool {
	mu.Lock()
	cancel, live := running[projectID]
	mu.Unlock()
	if !live {
		return false
	}
	cancel()
	return true
}

// Rework 段级重做（plan.md §3.5：修改单元可到元素，重做单元=段）。
// 只重生成目标段画面（音频/时长不变）→ 组装 → 检查 → 整片重渲拼接。
// 支持一次多段（用户一句话圈几段一起改，共享同一条指令跑一遍管线）。
func (r *Runner) Rework(projectID string, segIDs []string, instruction string) (bool, error) {
	if len(segIDs) == 0 {
		return false, fmt.Errorf("未指定重做段")
	}
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
	running[projectID] = cancel
	mu.Unlock()
	p := pipeline.NewProject(r.DataDir, projectID)
	sb, err := p.LoadStoryboard()
	if err != nil {
		cancel()
		mu.Lock()
		delete(running, projectID)
		mu.Unlock()
		return false, fmt.Errorf("分镜未生成: %w", err)
	}
	var segs []contract.Segment
	for _, id := range segIDs {
		seg, found := findSegment(sb, id)
		if !found {
			cancel()
			mu.Lock()
			delete(running, projectID)
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
			delete(running, projectID)
			mu.Unlock()
			r.wg.Done()
		}()
		err := safeRun(func() error { return r.runRework(ctx, p, segs, instruction) })
		if errors.Is(err, context.Canceled) {
			detail := "用户取消重做"
			if r.stopping.Load() {
				detail = "服务停机，重做中断（该段旧画面仍在成片里，重启后可再重做）"
			}
			r.emit(projectID, "stage", "pipeline", "cancelled")
			r.emit(projectID, "cancelled", "", detail)
			r.Manifest(p, EventReworkCancelled, joinSegIDs(segs)+": "+detail)
			r.jobTerminal(projectID, "cancelled", "", detail)
			if r.OnDone != nil {
				r.OnDone(projectID, "cancelled", detail)
			}
			return
		}
		if err != nil {
			r.emit(projectID, "stage", "pipeline", "error: "+err.Error())
			r.emit(projectID, "error", "", err.Error())
			r.Manifest(p, EventReworkError, joinSegIDs(segs)+": "+err.Error())
			r.jobTerminal(projectID, "failed", "", err.Error())
			if r.OnDone != nil {
				r.OnDone(projectID, "failed", err.Error())
			}
		}
	}()
	return true, nil
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
	_ = os.MkdirAll(filepath.Dir(reworkHistoryPath(p)), 0o755)
	if b, err := json.MarshalIndent(es, "", "  "); err == nil {
		_ = pipeline.WriteFileAtomic(reworkHistoryPath(p), b, 0o644)
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
	r.Manifest(p, EventReworkStart, only+": "+firstLine(instruction, 80))
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
	if err := r.python(ctx, "ai/assemble.py", p.Dir, 2*time.Minute); err != nil {
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
		r.OnDone(id, "video", strings.Join(labels, "、")+" 画面重做完成")
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
			switch ev.Event {
			case EventProduceStart, EventReworkStart:
				started = ev.Event
			case EventProduceDone, EventProduceError, EventProduceCancelled,
				EventReworkDone, EventReworkError, EventReworkCancelled:
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
		// 中断补写的终态与被中断的任务同族（rework 中断写 rework.error），
		// agent 侧两种终态都认，但审计口径必须自洽
		ev := EventProduceError
		if isRework {
			ev = EventReworkError
		}
		r.Manifest(p, ev, detail)
		r.jobTerminal(id, "failed", "", detail)
		if r.OnDone != nil {
			r.OnDone(id, "failed", detail)
		}
		log.Printf("[produce] 对账：%s %s中断，已补写终态\n", id, what)
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

	// ── 2. 语义 spec 生成（LLM）+ 确定性渲染 + 合成物落盘 ────────
	if err := r.compositions(ctx, p); err != nil {
		return err
	}

	// ── 3. 组装 ─────────────────────────────────────────────
	r.emit(id, "stage", "assemble", "running")
	r.Manifest(p, "stage.start", "assemble")
	if err := r.python(ctx, "ai/assemble.py", p.Dir, 2*time.Minute); err != nil {
		return fmt.Errorf("组装失败: %w", err)
	}
	r.emit(id, "stage", "assemble", "done")
	r.Manifest(p, "stage.finish", "assemble")

	// ── 4. check 门禁（自修复：报错回喂重生成 spec）────────────
	if err := r.checkWithRepair(ctx, p, "", ""); err != nil {
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

	// ── 6. 画面审查（抽帧+视觉模型，自修复见 frameQA）────────────
	if err := r.frameQA(ctx, p, "", ""); err != nil {
		return err
	}
	r.emit(id, "done", "", "renders/main.mp4")
	r.Manifest(p, EventProduceDone, "renders/main.mp4")
	r.jobTerminal(id, "done", "renders/main.mp4", "")
	if r.OnDone != nil {
		r.OnDone(id, "video", "全片制作完成")
	}
	return nil
}

// compositions 生成/复用场景文件 → 清旧帧 → 管线落盘。
func (r *Runner) compositions(ctx context.Context, p *pipeline.Project) error {
	r.emit(p.ID, "stage", "compositions", "running")
	r.Manifest(p, "stage.start", "compositions")
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
func (r *Runner) checkWithRepair(ctx context.Context, p *pipeline.Project, instruction, only string) error {
	for attempt := 1; attempt <= 3; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.emit(p.ID, "stage", "check", fmt.Sprintf("running（第 %d 次）", attempt))
		r.Manifest(p, "stage.start", "check")
		_ = os.Remove(p.Artifact(".hyperframes-ok"))
		err := pipeline.Run(ctx, p, "check")
		if err == nil {
			// scene 确定性回放硬闸（P1-1）：hyperframes check 之后、放行之前，
			// 含 scene 的段同 t 两轮截帧像素必须一致；失败按 check 失败进同一修复轮。
			if rerr := r.sceneReplayCheck(ctx, p); rerr != nil {
				err = rerr
				} else {
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
		if n := applyContrastFixes(p, err.Error()); n > 0 {
			r.emit(p.ID, "stage", "check", fmt.Sprintf("contrast-fix：按建议色回填 %d 处", n))
			r.Manifest(p, "check.contrast_fix", fmt.Sprintf("%d 处", n))
			if err := r.python(ctx, "ai/assemble.py", p.Dir, 2*time.Minute); err == nil {
				if pipeline.Run(ctx, p, "check") == nil {
					r.emit(p.ID, "stage", "check", "done")
					r.Manifest(p, "stage.finish", "check")
					return nil
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
		targets := failingSegs(err.Error())
		for _, id := range strings.Split(only, ",") {
			if id = strings.TrimSpace(id); id != "" && !slices.Contains(targets, id) {
				targets = append([]string{id}, targets...)
			}
		}
		repairOnly := strings.Join(targets, ",")
		feedback := "上一版画面被自动检查拒绝，报错摘要（✗ 行逐项都要修）：\n" + tail(err.Error(), 25) +
			"\n修复规则：\n" +
			"- 重叠/越界：整体重排拉开间距，避开底部字幕带（禁放区见布局规则），必要时减少元素。\n" +
			"- 对比度 ✗：文字色与它实际压着的背景太接近。检查器的建议色不可信（实测往往不达标）——" +
			"文字压在照片/深色底上一律改极浅色（#EFE9DA、#E3E3E3 级），浅色底上用深色（#2D2D2D 级）；" +
			"中灰（#616161、#9E9E9E）做正文/标注色直接不达标，禁用。\n" +
			"逐项修完再输出。"
		// 本轮 check 失败收口（计时），修复轮重生成的时间不计入 check
		r.Manifest(p, "stage.finish", "check")
		r.emit(p.ID, "stage", "check", fmt.Sprintf("repair：重生成画面（段：%s）", repairOnly))
		r.Manifest(p, "check.repair", tail(err.Error(), 10))
		if err := r.sceneGen(ctx, p, instruction, feedback, repairOnly); err != nil {
			return err
		}
		r.clearFrames(p, repairOnly)
		if err := pipeline.Run(ctx, p, "compositions"); err != nil {
			return fmt.Errorf("合成物落盘失败: %w", err)
		}
		if err := r.python(ctx, "ai/assemble.py", p.Dir, 2*time.Minute); err != nil {
			return fmt.Errorf("组装失败: %w", err)
		}
	}
	return errors.New("unreachable")
}

// segIDRe 段 id（seg01、seg02…）。
var segIDRe = regexp.MustCompile(`seg\d{2,}`)

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
		if i+1 < len(lines) { // 对比度项的选择器在 ✗ 行，段文件在下一行 Try/source
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
	// 全局时刻 → 段 的映射（scene 段各取 头0.5s/中点/尾-0.5s，
	// 避开段首尾 0.25s 黑场转场的渐变区）
	t2seg := map[float64]string{}
	var ats []float64
	acc := 0.0
	for _, seg := range sb.Segments {
		dur := voices[seg.ID].DurationS + contract.SegTail
		if specHasScene(p, seg.ID) {
			for _, t := range []float64{0.5, dur / 2, dur - 0.5} {
				at := acc + t
				if at > acc+dur-0.05 {
					at = acc + dur - 0.05
				}
				ats = append(ats, round2(at))
				t2seg[round2(at)] = seg.ID
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
	for round := 0; round < 2; round++ {
		dir := fmt.Sprintf("reports/replay/round%d", round+1)
		_ = os.RemoveAll(p.Artifact(dir))
		out, err := pipeline.RunCLI(p, "npx", "hyperframes@0.8.55", "snapshot", ".",
			"--at", strings.Join(atArg, ","), "--no-end", "-o", dir)
		if err != nil {
			return fmt.Errorf("scene 回放 snapshot 失败: %s", tail(out, 8))
		}
		entries, err := os.ReadDir(p.Artifact(dir))
		if err != nil {
			return err
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "frame-") && strings.HasSuffix(e.Name(), ".png") {
				frames[round] = append(frames[round], e.Name())
			}
		}
	}
	if len(frames[0]) == 0 {
		return fmt.Errorf("scene 回放 snapshot 未产出帧（--at %s）", strings.Join(atArg, ","))
	}
	if len(frames[0]) != len(frames[1]) {
		return fmt.Errorf("scene 回放两轮帧数不一致（%d vs %d）", len(frames[0]), len(frames[1]))
	}
	// 逐帧比对（带 LSB 容差：加色混合下 GPU 光栅化有 1/255 级抖动，非逻辑非确定）
	var errs []string
	reported := map[string]bool{}
	for _, name := range frames[0] {
		maxD, nD, tot, err := pngDiffStats(
			filepath.Join(p.Artifact(fmt.Sprintf("reports/replay/round1")), name),
			filepath.Join(p.Artifact(fmt.Sprintf("reports/replay/round2")), name))
		if err != nil {
			return err
		}
		if maxD <= replayMaxChannelDiff && (tot == 0 || float64(nD) <= replayDiffRatioMax*float64(tot)) {
			continue
		}
		m := reSnapAt.FindStringSubmatch(name)
		segID := ""
		if m != nil {
			if t, perr := strconv.ParseFloat(m[1], 64); perr == nil {
				segID = t2seg[round2(t)]
			}
		}
		if segID == "" || reported[segID] {
			continue
		}
		reported[segID] = true
		errs = append(errs, fmt.Sprintf("✗ %s: scene 确定性回放未过（%s 两次截图像素不一致：max差=%d、差异像素 %d/%d——code 藏了非确定性：只用注入的 rng，运动必须写成 t 的函数，禁任何自管时间/真随机路径）", segID, name, maxD, nD, tot))
	}
	if len(errs) > 0 {
		return fmt.Errorf("scene 回放门禁:\n%s", strings.Join(errs, "\n"))
	}
	r.Manifest(p, "check.scene_replay", fmt.Sprintf("%d 段 diff=0", len(t2seg)/3))
	return nil
}

var reSnapAt = regexp.MustCompile(`at-([\d.]+)s\.png$`)

func round2(f float64) float64 { return math.Round(f*100) / 100 }

// 回放比对容差：加色混合下 GPU 光栅化存在 LSB 级抖动（实测 900 粒子 scene：
// 两轮 16/2073600 像素差 1/255；拉普拉斯妖 seg38：1/2073600 像素差 8/255——
// alpha 临界值上抗锯齿覆盖判断翻转，非逻辑非确定）。真逻辑非确定性
// （真随机/自管时间）是大面积大差异：占比 0.05% 闸仍主拦，通道差阈值只防单像素误杀。
const (
	replayMaxChannelDiff = 8    // 任一像素通道差超此值 = 硬失败（实测 GPU 噪声上限内）
	replayDiffRatioMax   = 5e-4 // 差异像素占比超此值 = 硬失败（≈0.05%，2M 像素 ≈1000 个）
)

// pngDiffStats 解码两 PNG 逐像素比较 → (最大通道差, 差异像素数, 总像素)。
func pngDiffStats(pathA, pathB string) (maxDiff, nDiff, total int, err error) {
	fa, err := os.Open(pathA)
	if err != nil {
		return 0, 0, 0, err
	}
	defer fa.Close()
	ia, err := png.Decode(fa)
	if err != nil {
		return 0, 0, 0, err
	}
	fb, err := os.Open(pathB)
	if err != nil {
		return 0, 0, 0, err
	}
	defer fb.Close()
	ib, err := png.Decode(fb)
	if err != nil {
		return 0, 0, 0, err
	}
	if ia.Bounds() != ib.Bounds() {
		return 1 << 30, 1 << 30, 0, nil // 尺寸不一致按最严重处理
	}
	b := ia.Bounds()
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
	return maxDiff, nDiff, total, nil
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
		ID       string   `json:"id"`
		Phrases  []string `json:"phrases"`
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
	if _, err := prov.GenerateJSON(ctx, system, user, &out); err != nil {
		return err
	}
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
	nb, _ := json.MarshalIndent(am, "", "  ")
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
//（大留白单主体/暗底浅字/段间衔接，三篇参考片分析的沉淀）。清单驱动：加方法 =
// 加文件加 methods.json 条目，不改代码（methods_test.go 守卫清单与文件一致）。
type methodEntry struct {
	Domain   string   `json:"domain"` // scene | comp
	File     string   `json:"file"`   // methods/ 下相对路径
	Title    string   `json:"title"`
	Keywords []string `json:"keywords"`
}

func loadMethods(rootDir string) []methodEntry {
	b, err := os.ReadFile(filepath.Join(rootDir, "data", "library", "methods", "methods.json"))
	if err != nil {
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
		b, err := os.ReadFile(filepath.Join(rootDir, "data", "library", "methods", filepath.FromSlash(m.File)))
		if err != nil {
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
		sceneBlock = fmt.Sprintf("\n## scene 参考实现（仿写起点：改参数与造型，机制逐字保留）\n```\n%s\n```\n", m.body)
	}
	if len(comps) > 2 {
		comps = comps[:2]
	}
	if len(comps) > 0 {
		parts := make([]string, len(comps))
		for i, c := range comps {
			parts[i] = "### " + c.m.Title + "\n" + strings.TrimSpace(c.body)
		}
		compBlock = "\n## 构图方法（高级感公式——按此纪律编排本段画面）\n" + strings.Join(parts, "\n\n") + "\n"
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

// styleLayout 风格版式（构图模式）：styles.json layouts 字段，描述本风格的标准构图，
// spec 生成每段先选一个版式再填元素——治"整齐网格 PPT 感"。
type styleLayout struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Desc string `json:"desc"`
}

// styleRegEntry styles.json 条目（Go 侧只消费方向/题材/配图调性/版式）。
type styleRegEntry struct {
	Direction string        `json:"direction"`
	Genre     string        `json:"genre"`
	Keywords  []string      `json:"keywords"`
	Photo     string        `json:"photo"`
	Layouts   []styleLayout `json:"layouts"`
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

// matchStyleReg 方向名 → 注册表条目。两轮匹配：完整方向名子串优先，再关键词最长命中
// （与 ai/engines/stylepack_generic.py match 同语义）。未命中返回 nil。
func matchStyleReg(direction, dataDir string) *styleRegEntry {
	if direction == "" {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(dataDir, "library", "registry", "styles.json"))
	if err != nil {
		return nil
	}
	var styles []styleRegEntry
	if json.Unmarshal(b, &styles) != nil {
		return nil
	}
	for i := range styles {
		if styles[i].Direction != "" && strings.Contains(direction, styles[i].Direction) {
			return &styles[i]
		}
	}
	best, bestLen := -1, 0
	for i := range styles {
		for _, kw := range styles[i].Keywords {
			n := len([]rune(kw))
			if kw != "" && strings.Contains(direction, kw) && n > bestLen {
				best, bestLen = i, n
			}
		}
	}
	if best >= 0 {
		return &styles[best]
	}
	return nil
}

// styleNoteFor 项目风格方向 → spec 提示里的风格说明。
// 皮肤文件（§12.2）优先注入槽位摘要（spec 的配色/字体措辞向皮肤收敛），
// 注册表命中再补题材 + 配图调性（两者不互斥）。
func styleNoteFor(p *pipeline.Project, dataDir string) string {
	direction := projectDirection(p)
	if direction == "" {
		return ""
	}
	note := "\n## 本片风格\n- 方向：" + direction
	note += skinNote(p)
	st := matchStyleReg(direction, dataDir)
	if st == nil {
		return note
	}
	if st.Genre != "" {
		note += "\n- 适用题材：" + st.Genre
	}
	if st.Photo != "" {
		note += "\n- 配图 query 调性：" + st.Photo + "（image 的 query 往这个调性上靠）"
	}
	return note
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
	note += "\n- 配图 query 描述配色时往皮肤气质靠（底色/强调色的邻近词）；custom 特效底也用这套色"
	return note
}

// styleLayoutsFor 项目风格 → 注册表命中的版式列表（未命中/老注册表返回 nil）。
func styleLayoutsFor(p *pipeline.Project, dataDir string) []styleLayout {
	st := matchStyleReg(projectDirection(p), dataDir)
	if st == nil {
		return nil
	}
	return st.Layouts
}

// layoutNote 本段版式注入块：指定版式（按段号轮换，相邻段天然不同）+ 全表可改选。
// layouts 为空返回 ""，行为与旧版一致。校验硬底线（≥3 元素、不重叠）不放松。
func layoutNote(layouts []styleLayout, segIdx int) string {
	if len(layouts) == 0 {
		return ""
	}
	if segIdx < 1 {
		segIdx = 1
	}
	asg := layouts[(segIdx-1)%len(layouts)]
	var b strings.Builder
	b.WriteString("\n## 本段版式（先定构图，再往里填元素；禁止退回「标题+小卡平铺」的默认网格）\n")
	fmt.Fprintf(&b, "- 本段指定版式：%s（%s）——%s\n", asg.ID, asg.Name, asg.Desc)
	b.WriteString("- 本风格可用版式（画面提示与指定版式明显冲突时可改选，note 首句写「版式：<id>」）：\n")
	for _, l := range layouts {
		fmt.Fprintf(&b, "  - %s（%s）：%s\n", l.ID, l.Name, l.Desc)
	}
	b.WriteString("- 元素数量与铺排以版式描述为准（下方 4–7 个的一般规则让位于版式；仍须 ≥3 个且不重叠）")
	return b.String()
}

// styleSampleImages 已截图的风格样张（style/sample-N.png，ai/snap_samples.py 产物）→ data URL。
// spec 生成时附给多模态模型：看真图选 kind/配色/调性，替代纯文字风格注入。最多 3 张，缺文件返回 nil。
func styleSampleImages(p *pipeline.Project) []string {
	var urls []string
	for i := 0; i < 3; i++ {
		b, err := os.ReadFile(p.Artifact(fmt.Sprintf("style/sample-%d.png", i)))
		if err != nil {
			break
		}
		urls = append(urls, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(b))
	}
	return urls
}

func feedbackBlock(f string) string {
	if f == "" {
		return ""
	}
	return "\n## 必须修正\n" + f + "\n"
}

// ── 子进程与工具 ─────────────────────────────────────────────

// pythonExe 解析可用的 Python 解释器。Windows 的 `python` 常是 Microsoft Store
// 的 WindowsApps 占位 stub（LookPath 找得到，一跑就退 9009），跳过它退到 py 启动器。
func pythonExe() string {
	if p, err := exec.LookPath("python"); err == nil && !isStoreStub(p) {
		return p
	}
	if p, err := exec.LookPath("py"); err == nil {
		return p
	}
	return "python"
}

// isStoreStub 识别 WindowsApps 商店占位假 python。真从 Store 装了 Python 的路径
// 同样含 WindowsApps——会被误跳过，但那是 py 启动器不存在，兜底仍回 `python`，不受影响。
func isStoreStub(p string) bool {
	return runtime.GOOS == "windows" &&
		strings.Contains(strings.ToLower(filepath.ToSlash(p)), "windowsapps")
}

// python 跑仓库 ai/ 脚本（cwd = 仓库根）。任务取消时连带杀掉子进程。
func (r *Runner) python(ctx context.Context, script string, projDir string, timeout time.Duration) error {
	py := pythonExe()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, py, filepath.Join(r.RootDir, script), projDir)
	cmd.Dir = r.RootDir
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil { // 被取消/超时杀掉：报可识别的 ctx 错误而非 killed
			return ctx.Err()
		}
		return fmt.Errorf("%s 退出: %w\n%s", script, err, tail(buf.String(), 15))
	}
	fmt.Println(buf.String())
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

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func firstLine(s string, n int) string {
	s = strings.SplitN(s, "\n", 2)[0]
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// contrastSuggRe 抓 checker 对比度诊断对：✗ 选择器 比值:1 (need …) \n Try rgb(..); source frames/segNN.html
var contrastSuggRe = regexp.MustCompile(
	`✗[ ]*([^\r\n]+?)\s+[0-9]+(?:\.[0-9]+)?:1\s*\(need[^)]*\)\s*\n\s*Try\s+(rgb\([^)]*\))\s*;\s*source\s+(\S+)`)

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
		if !strings.HasPrefix(src, "compositions/frames/") || !strings.HasSuffix(src, ".html") {
			continue // 只动段帧，别处一概不碰
		}
		fixes = append(fixes, fix{src, sel + "{color:" + color + "!important}"})
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
