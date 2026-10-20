package produce

// segpipe.go —— 段级流水线（production-stage-design 工作项⑬/⑭/⑮/⑰）：
//
//	TTS（一次，全局）→ ④写画面 ×N（并发 ≤4，谁写完谁往下流）
//	   段链：本段 check（junction 子工程，含 canvas 段回放门禁）
//	         → 段渲染（不被审核闸）→ ⑤段审（判卷型 ReAct，review 包）
//	→ 邻居两侧段审都过 → ⑤边界审 → 最后一对边界通过 → 拼接（永远零改动）
//	→ 全片终审（12–16 帧四查）→ 出片
//
// 与 legacy 栅栏管线的分工：prep 新项目（scripts/script.json 在）走本文件；
// 老项目走 run() 原路径零改动（⑭：legacy 设计流程原样保留）。
// 调度规则（§4.3）：三池独立限流（写≤4/审≤4/渲≤min(4,CPU-2)，皆环境变量）；
// 写优先于审（写在飞时审核每周期最多放行一个）；段状态机与签名刹车见 §4.2/§4.5。
// 可追溯与重放（§4.4）：reviews/journal.jsonl 追加只写，每次状态迁移与审核意见
// 单落盘；重跑从 journal 恢复，不重做已完成段。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"webpage-video-agent/server/internal/artifacts"
	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/pipeline"
	"webpage-video-agent/server/internal/prep"
	"webpage-video-agent/server/internal/review"
)

// 段状态机（§4.2）。写账与恢复都认这套词。
const (
	stWritten    = "written"
	stChecked    = "checked"
	stRendered   = "rendered"
	stAudited    = "audited"
	stRewriting  = "rewriting"
	maxSegRework = 2 // 同段打回 ≤2 轮，超限刹车
	maxFinalRd   = 2 // 全片终审 ≤2 轮
)

// ── ⑰ 并发配置（改配置不改代码；默认值 = §4.3 第 1 条）──────────────

func envLimit(key string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(key)); err == nil && n >= 1 && n <= 16 {
		return n
	}
	return def
}

// writeConcurrency 写画面并发（PRODUCE_WRITE_CONCURRENCY，默认 4）。
func writeConcurrency() int { return envLimit("PRODUCE_WRITE_CONCURRENCY", 4) }

// auditConcurrency 审核并发（PRODUCE_AUDIT_CONCURRENCY，默认 4）。
func auditConcurrency() int { return envLimit("PRODUCE_AUDIT_CONCURRENCY", 4) }

// renderConcurrency 渲染并发（PRODUCE_RENDER_CONCURRENCY，默认 min(4, CPU-2)，
// 下限 1——渲吃 CPU，给写/审留余量）。
func renderConcurrency() int {
	if n, err := strconv.Atoi(os.Getenv("PRODUCE_RENDER_CONCURRENCY")); err == nil && n >= 1 && n <= 16 {
		return n
	}
	def := runtime.NumCPU() - 2
	if def > 4 {
		def = 4
	}
	if def < 1 {
		def = 1
	}
	return def
}

// IsPrepProject prep 新项目判定（段级流水线的路由条件）：文字稿工件在。
// 老项目（legacy 分镜、无文字稿）照走原栅栏管线，行为不变。
func IsPrepProject(p *pipeline.Project) bool {
	_, err := os.Stat(p.Artifact("scripts/script.json"))
	return err == nil
}

// narrationFingerprint 全片旁白指纹（段 id+旁白逐段哈希）：TTS 幂等复用的依据。
// 文稿/分镜改动（旁白或段集变化）→ 指纹变 → TTS 重跑；一致 → 复用现有配音。
func narrationFingerprint(sb *contract.Storyboard) string {
	h := sha256.New()
	for _, seg := range sb.Segments {
		h.Write([]byte(seg.ID + "\x00" + seg.Narration + "\x00"))
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// ── 日志与恢复（§4.4：追加只写，恢复不重做已完成段）─────────────────

// journalLine 日志一行（reviews/journal.jsonl）。gen 是段的当前代际（重写 +1，
// 边界审记录两侧代际——任一侧重写即失效重审）。
type journalLine struct {
	TS     string `json:"ts"`
	Kind   string `json:"kind"` // seg | bnd | final
	ID     string `json:"id"`
	State  string `json:"state"`
	Round  int    `json:"round,omitempty"`
	Gen    int    `json:"gen,omitempty"`
	GenP   int    `json:"gen_p,omitempty"` // 边界审专用
	GenN   int    `json:"gen_n,omitempty"`
	Detail string `json:"detail,omitempty"`
}

func journalPath(p *pipeline.Project) string { return p.Artifact("reviews/journal.jsonl") }

func loadJournal(p *pipeline.Project) map[string]journalLine {
	out := map[string]journalLine{}
	b, err := os.ReadFile(journalPath(p))
	if err != nil {
		return out // 首跑无账本：必须给非 nil map——nil map 首次赋值 panic 曾把整条流水线冻死
	}
	for _, line := range strings.Split(string(b), "\n") {
		var e journalLine
		if json.Unmarshal([]byte(line), &e) != nil || e.Kind == "" || e.ID == "" {
			continue
		}
		out[e.Kind+"/"+e.ID] = e
	}
	return out
}

func verdictPath(p *pipeline.Project, kind, id string, round int) string {
	return p.Artifact(fmt.Sprintf("reviews/%s-%s-r%d.json", kind, id, round))
}

func saveVerdict(p *pipeline.Project, kind, id string, round int, v *review.Verdict) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(verdictPath(p, kind, id, round)), 0o755); err != nil {
		return err
	}
	return pipeline.WriteFileAtomic(verdictPath(p, kind, id, round), b, 0o644)
}

// ── 签名刹车（§4.5）──────────────────────────────────────────────

// brakeTracker 签名 = 类别@锚点。同签名连续两轮 high → 刹车；≥N 个不同段出现
// 同类别 high → 刹车（系统性早停），N = max(3, 段数/4)。2026-10-17 实测教训：
// 旧版阈值 2 且跨轮累计、过审不清除——审查意见本就带随机性（同一渲染物逐轮
// 意见漂移），两段在不同轮各出一次同类别 high 就处死整片，一晚 9 轮执行全灭。
// 过审轨道调 clear 撤销记账：修好了就不该继续记仇。check 返回非空刹车理由。
type brakeTracker struct {
	mu       sync.Mutex
	segCount int
	prevSigs map[string]map[string]bool // 轨道名（段 id / bnd:b01 / final）→ 上一轮 high 签名集
	catSegs  map[string]map[string]bool // category → 出现过 high 的轨道
	tripped  string
}

func newBrakeTracker(segCount int) *brakeTracker {
	return &brakeTracker{
		segCount: segCount,
		prevSigs: map[string]map[string]bool{},
		catSegs:  map[string]map[string]bool{},
	}
}

// clear 轨道过审：撤销它在各类别里的刹车记账。
func (b *brakeTracker) clear(track string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.prevSigs, track)
	for cat, tracks := range b.catSegs {
		delete(tracks, track)
		if len(tracks) == 0 {
			delete(b.catSegs, cat)
		}
	}
}

func (b *brakeTracker) check(track string, sigs []string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tripped != "" {
		return b.tripped
	}
	cats := map[string]bool{}
	for _, sig := range sigs {
		cat, _, _ := strings.Cut(sig, "@")
		cats[cat] = true
		if b.prevSigs[track][sig] {
			b.tripped = fmt.Sprintf("签名刹车：%s 连续两轮 high（%s）", sig, track)
			return b.tripped
		}
	}
	limit := b.segCount / 4
	if limit < 3 {
		limit = 3
	}
	for cat := range cats {
		if b.catSegs[cat] == nil {
			b.catSegs[cat] = map[string]bool{}
		}
		b.catSegs[cat][track] = true
		if len(b.catSegs[cat]) >= limit {
			b.tripped = fmt.Sprintf("系统性刹车：%d 个不同轨道出现同类别 %q high", len(b.catSegs[cat]), cat)
			return b.tripped
		}
	}
	b.prevSigs[track] = map[string]bool{}
	for _, sig := range sigs {
		b.prevSigs[track][sig] = true
	}
	return ""
}

func (b *brakeTracker) trip(reason string) {
	b.mu.Lock()
	if b.tripped == "" {
		b.tripped = reason
	}
	b.mu.Unlock()
}

func (b *brakeTracker) reason() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.tripped
}

// ── 段级流水线入口（⑭ 风格包硬门 + TTS + 调度）──────────────────────

// runSegmentPipeline prep 新项目的制作主链。
func (r *Runner) runSegmentPipeline(ctx context.Context, p *pipeline.Project) error {
	id := p.ID
	// ⑭ 设计发明退役：风格包是硬前置——缺失/未确认明确报错引导走 prep 反馈重跑，
	// 不静默发明、不等待样张确认。设计系统已随打包终审确认并落盘锁定。
	sp, err := prep.LoadStylePack(p)
	if err != nil || !sp.Confirmed {
		return errors.New("风格包缺失或未确认（打包终审硬门）——制作阶段不再自行发明设计；请在对话里说明要改什么走准备阶段反馈重跑，打包确认后再开工")
	}
	script, serr := prep.LoadScript(p)
	if serr != nil {
		return fmt.Errorf("文字稿缺失: %w", serr)
	}
	sb, err := p.LoadStoryboard()
	if err != nil {
		return fmt.Errorf("分镜缺失: %w", err)
	}

	// TTS + 词级对齐（一次，全局，幂等——设计文档 §2）。旁白指纹（段集+逐段旁白）
	// 未变且配音在 → 复用不重跑不记账：重跑轮逐字复跑 TTS 只会产生「配音(第N轮)
	// <1秒」的吓人账行（2026-10-18 实测）；指纹变（文稿/分镜改旁白）自动过期重跑。
	fp := narrationFingerprint(sb)
	reuseTTS := false
	if b, err := os.ReadFile(p.Artifact("audio_meta.fp")); err == nil && string(b) == fp {
		if _, err := loadAudioMeta(p); err == nil {
			reuseTTS = true
		}
	}
	if reuseTTS {
		r.Manifest(p, "produce.tts", "reused")
	} else {
		r.emit(id, "stage", "tts", "running")
		r.Manifest(p, "stage.start", "tts")
		if err := r.python(ctx, "ai/tts_align.py", p.Dir, 30*time.Minute); err != nil {
			return fmt.Errorf("TTS/对齐失败: %w", err)
		}
		r.emit(id, "stage", "tts", "done")
		r.Manifest(p, "stage.finish", "tts")
		if err := pipeline.WriteFileAtomic(p.Artifact("audio_meta.fp"), []byte(fp), 0o644); err != nil {
			log.Printf("[produce] 写配音指纹失败: %v", err)
		}
	}
	r.Manifest(p, "produce.tts", "done")
	if err := r.translateSubtitles(ctx, p); err != nil {
		r.emit(id, "progress", "tts", "双语字幕翻译跳过: "+err.Error())
	}
	r.syncUp(p, "audio")
	if r.Artifacts != nil && r.Artifacts.Available() {
		if err := r.Artifacts.Put(artifacts.ProjectKey(id, "audio_meta.json"), p.Artifact("audio_meta.json")); err != nil {
			log.Printf("[produce] 上传 audio_meta.json 失败: %v", err)
		}
	}
	voices, err := loadAudioMeta(p)
	if err != nil {
		return err
	}

	// 目录字体先行（一次，全局；失败降级不挡制作）+ 样张截图
	r.emit(id, "stage", "compositions", "running")
	r.Manifest(p, "stage.start", "compositions")
	if err := r.python(ctx, "ai/font_tool.py", p.Dir, 4*time.Minute); err != nil {
		r.Manifest(p, "fonts.prepare_error", err.Error())
	} else {
		r.Manifest(p, "fonts.prepared", "")
	}
	r.snapSamples(ctx, p)

	svc := &review.Service{P: p, Emit: r.Hub.Emit}
	c := newCoordinator(r, p, svc, sb, script, sp, voices)
	if err := c.run(ctx); err != nil {
		return err
	}
	r.emit(id, "stage", "compositions", "done")
	r.Manifest(p, "stage.finish", "compositions")
	r.emit(id, "done", "", "renders/main.mp4")
	r.Manifest(p, EventProduceDone, "renders/main.mp4")
	r.jobTerminal(id, "done", "renders/main.mp4", "")
	// 完成通知如实区分「全过」与「带伤」（2026-10-19 中-13：带伤放行/审查器
	// 故障降级/终审降级的运行原先也说「全部通过」，用户无从知晓成片有账）
	if wounds := c.woundedNotes(); len(wounds) > 0 {
		r.notifyOnDone(id, "video", fmt.Sprintf("全片制作完成（带伤 %d 处：%s）——明细见过程页与 reviews/ 意见单",
			len(wounds), strings.Join(wounds, "、")))
	} else {
		r.notifyOnDone(id, "video", "全片制作完成（段级流水线：逐段审核、边界审、全片终审全部通过）")
	}
	return nil
}

// woundedNotes 汇总本run的「带伤」账：段审打回超限带伤放行、审查器故障降级、
// 边界带伤放行/降级、终审降级——完成通知与「全部通过」区分开（中-13）。
func (c *coordinator) woundedNotes() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, s := range c.segs {
		if e := c.journal["seg/"+s.seg.ID]; e.State == stAudited &&
			(strings.Contains(e.Detail, "带伤") || strings.Contains(e.Detail, "降级")) {
			out = append(out, s.seg.ID+" "+e.Detail)
		}
	}
	for _, b := range c.bounds {
		if e := c.journal["bnd/"+b.id]; e.State == stAudited &&
			(strings.Contains(e.Detail, "带伤") || strings.Contains(e.Detail, "降级")) {
			out = append(out, b.id+" "+e.Detail)
		}
	}
	if e := c.journal["final/all"]; e.State == stAudited &&
		(strings.Contains(e.Detail, "带伤") || strings.Contains(e.Detail, "降级")) {
		out = append(out, "全片终审 "+e.Detail)
	}
	return out
}

// ── 协调器 ────────────────────────────────────────────────────────

type segCtx struct {
	seg    contract.Segment
	dur    float64 // 段片实测时长（渲后）
	gen    int     // 代际：每次审核派发 +1（边界对失效重审的依据；已过段的边界记录对齐此值）
	round  int     // 已派发的审核轮次
	feedCh chan string
	// workerStarted 该段是否有 chain worker（过审后 worker 停在 feedCh 等打回——
	// 终审打回直接唤醒它；恢复进场的已过段没有 worker，打回时现起一个）。
	workerStarted bool
	// pendingFeedback 现起 worker 时携带的首笔打回反馈（spawnWorker 写、
	// chainWorker 取；没有它，恢复进场的已过段被边界审打回后反馈无人消费，
	// 段永不重写、waitAllPassed 永等——2026-10-17 楔死事故根因）。
	pendingFeedback string
}

type auditReq struct {
	kind  string // seg | bnd
	segID string
	bi    int // 边界序号
}

type boundaryCtx struct {
	id         string
	prev, next string
	genP, genN int // 已过审时的两侧代际（-1 = 未审/已失效）
	round      int // 已打回次数（2026-10-19 中-7：轮次上限依据；过审/降级即清零）
}

type coordinator struct {
	r      *Runner
	p      *pipeline.Project
	svc    *review.Service
	sb     *contract.Storyboard
	script *contract.Script
	sp     *contract.StylePack
	voices map[string]voiceMeta

	segs   []*segCtx
	bounds []*boundaryCtx
	brakes *brakeTracker

	mu      sync.Mutex
	journal map[string]journalLine // 内存镜像（log 时同步更新；恢复判定与边界代际比对共用）

	writeInFlight int // 写在飞计数（写优先于审的闸）
	auditWhileW   int // 写在飞窗口内已放行的审核数

	writeSem  chan struct{}
	auditSem  chan struct{}
	renderSem chan struct{}
	auditCh   chan auditReq

	bndBusy map[int]bool // 边界审在飞（auditBoundary 自己释放）；跟踪器据此防重派

	passNotify chan string // 段过审通知（边界跟踪的唤醒信号）

	samplesOnce sync.Once
	sampleVis   []visImg

	// cctx/chainWg 供审核路径现起 worker 用（spawnWorker 提为方法后需要；
	// cctx 由 run 写定后才起任何 goroutine，无数据竞争）。
	cctx    context.Context
	chainWg sync.WaitGroup
	// progressAt 最近一次进展时刻（emit/log 都算）；看门狗据此升级全管线静默。
	progressAt atomic.Int64
}

func newCoordinator(r *Runner, p *pipeline.Project, svc *review.Service, sb *contract.Storyboard,
	script *contract.Script, sp *contract.StylePack, voices map[string]voiceMeta) *coordinator {
	c := &coordinator{
		r: r, p: p, svc: svc, sb: sb, script: script, sp: sp, voices: voices,
		brakes:     newBrakeTracker(len(sb.Segments)),
		journal:    loadJournal(p),
		writeSem:   make(chan struct{}, writeConcurrency()),
		auditSem:   make(chan struct{}, auditConcurrency()),
		renderSem:  make(chan struct{}, renderConcurrency()),
		auditCh:    make(chan auditReq, len(sb.Segments)*3+len(sb.Segments)),
		passNotify: make(chan string, len(sb.Segments)*4+8),
		bndBusy:    make(map[int]bool),
	}
	for _, seg := range sb.Segments {
		c.segs = append(c.segs, &segCtx{seg: seg, feedCh: make(chan string, 4)})
	}
	for i := 0; i+1 < len(sb.Segments); i++ {
		c.bounds = append(c.bounds, &boundaryCtx{
			id: fmt.Sprintf("b%02d", i+1), prev: sb.Segments[i].ID, next: sb.Segments[i+1].ID,
			genP: -1, genN: -1,
		})
	}
	return c
}

// touch 记录一次进展（emit/log 都算进展）。看门狗据此判断全管线静默。
func (c *coordinator) touch() { c.progressAt.Store(time.Now().UnixNano()) }

func (c *coordinator) emit(typ, stage, detail string) {
	c.touch()
	c.r.emit(c.p.ID, typ, stage, detail)
}

func (c *coordinator) seg(id string) *segCtx {
	for _, s := range c.segs {
		if s.seg.ID == id {
			return s
		}
	}
	return nil
}

// log 状态迁移落账：journal 追加 + 内存镜像 + manifest 一行（§4.4）。
// audited 迁移顺带唤醒边界跟踪（提示性；轮询兜底）。journal 文件写与 manifest
// 落盘都在锁外（2026-10-19 中-L6：临界区只碰内存镜像——磁盘 IO 进临界区会把
// 并发段的记账串行排队，与 journal 文件写「刻意放锁外」同一顾虑）。
func (c *coordinator) log(e journalLine) {
	e.TS = time.Now().Format(time.RFC3339)
	b, _ := json.Marshal(e)
	_ = os.MkdirAll(filepath.Dir(journalPath(c.p)), 0o755)
	if f, err := os.OpenFile(journalPath(c.p), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		f.Write(append(b, '\n'))
		f.Close()
	}
	c.touch()
	c.mu.Lock()
	if c.journal == nil {
		c.journal = map[string]journalLine{}
	}
	c.journal[e.Kind+"/"+e.ID] = e
	notify := e.Kind == "seg" && e.State == stAudited
	c.mu.Unlock() // panic 也不许把锁带走：锁被带走的瞬间全协调器冻结成化石
	c.r.Manifest(c.p, "seg."+e.Kind+"."+e.State, e.ID+": "+pipeline.FirstLine(e.Detail, 120))
	if notify {
		select {
		case c.passNotify <- e.ID:
		default:
		}
	}
}

// segState 段状态迁移记账的薄封装（round/gen 进 journal，恢复判定用）。
func (c *coordinator) segState(id, state string, round, gen int, detail string) {
	c.log(journalLine{Kind: "seg", ID: id, State: state, Round: round, Gen: gen, Detail: detail})
}

func (c *coordinator) segEntry(id string) journalLine {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.journal["seg/"+id]
}

// ── 主流程 ────────────────────────────────────────────────────────

func (c *coordinator) run(ctx context.Context) error {
	cctx, ccancel := context.WithCancel(ctx)
	defer ccancel() // 管线终了（含刹车/失败路径）排干全部 worker
	c.cctx = cctx
	c.touch()
	go c.watchdog(cctx) // 全管线静默超限 → 刹车（见 watchdog 注释）

	c.emit("stage", "segpipe", fmt.Sprintf("running（%d 段；写≤%d 审≤%d 渲≤%d）",
		len(c.segs), writeConcurrency(), auditConcurrency(), renderConcurrency()))

	// 恢复（§4.4）：journal 已 audited 的段不重做——代际取自账面，边界对齐后
	// 只补它参与的边界审；其余段从断点状态重新走链（write/check/render 各自幂等）。
	for _, s := range c.segs {
		if e := c.segEntry(s.seg.ID); e.State == stAudited {
			s.round = e.Round
			s.gen = e.Gen
			if s.gen < 1 {
				s.gen = 1
			}
		}
	}
	for _, b := range c.bounds {
		if e := c.bndEntry(b.id); e.State == stAudited {
			b.genP, b.genN = e.GenP, e.GenN
		}
	}

	for _, s := range c.segs {
		if c.segEntry(s.seg.ID).State == stAudited {
			continue // 已过段：不重做（worker 也不起——打回时由 dispatchRewrite 现起并携带反馈）
		}
		c.spawnWorker(s, "")
	}
	// 审核 dispatcher（段审/边界审同队列，写优先闸 + 池限流）
	var auditWg sync.WaitGroup // 在飞审核（排干后再收尾）
	var dispatchWg sync.WaitGroup
	dispatchWg.Add(1)
	go func() { defer dispatchWg.Done(); c.auditDispatcher(cctx, &auditWg) }()
	// 边界跟踪（两侧段审都过 → 派边界审；重写致代际失配自动重审）
	var trackerWg sync.WaitGroup
	trackerWg.Add(1)
	go func() { defer trackerWg.Done(); c.boundaryTracker(cctx) }()

	var finalErr error
	for round := 1; ; round++ {
		// 等全部边界通过（或刹车/取消）。可多次进入：终审打回会让边界失效重审。
		if err := c.waitAllPassed(cctx); err != nil {
			finalErr = err
			break
		}
		// 拼接（永远零改动）：RunFrom("render") 补渲缺失段片（正常为零）+ concat + stitch
		if err := c.stitch(cctx); err != nil {
			finalErr = err
			break
		}
		// 全片终审（≤2 轮或刹车）
		rewrite, err := c.finalAudit(cctx, round)
		if err != nil {
			finalErr = err
			break
		}
		if len(rewrite) == 0 {
			break // 终审全过 → 出片
		}
		// 打回段走原 worker 链（重写→检→渲→段审），边界随代际失效自动重审；
		// 全部重新通过后回 loop 重拼+重审。dispatchRewrite 自带接收者保证：
		// 过审段唤醒停在 feedCh 的 worker，恢复进场的已过段现起并携带反馈。
		for _, segID := range rewrite {
			c.segState(segID, stRewriting, 0, 0, "终审打回重写")
			if s := c.seg(segID); s != nil {
				c.dispatchRewrite(cctx, s, finalFeedback(c, segID, round))
			}
		}
	}
	ccancel() // 终局：停派发 + 排干在飞（刹车动作语义，§4.5）
	// 排干顺序：dispatcher → 在飞审核（其间可能现起 worker，chainWg.Add）→ 工人 → 跟踪。
	// 审核必须在工人前收：审核 goroutine 的 dispatchRewrite 会现起 worker，
	// 先收工人会漏算后 Add 的计数（WaitGroup Add-after-Wait 竞态）。
	dispatchWg.Wait()
	auditWg.Wait()
	c.chainWg.Wait()
	trackerWg.Wait()
	c.cleanup()
	if reason := c.brakes.reason(); reason != "" && finalErr == nil {
		finalErr = errors.New(reason)
	}
	return finalErr
}

// watchdog 全管线静默升级（P0-2，2026-10-17 实测教训）：LLM 挂死最长可静默
// 10min×3 重试，审查反馈投给无人消费的轨道能让 waitAllPassed 永等——旧版对
// 这两类静默都不设防，UI 永远"运行中"、无失败落账。超过阈值的全体沉默一律
// 刹车转成可重跑失败；单工位慢而其余在动的场景不算静默（emit/log 都刷新计时）。
// 只 trip 刹车不 cancel ctx：卡在 HTTP 里的调用由传输超时自己退（≤10min），
// 保持"看门狗失败可自动重跑、不与用户取消混淆"的语义。
func (c *coordinator) watchdog(ctx context.Context) {
	interval := produceStallTimeout()
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			last := c.progressAt.Load()
			if last > 0 && time.Since(time.Unix(0, last)) > interval {
				c.brakes.trip(fmt.Sprintf("管线静默超过 %s 无进展，看门狗终止（可重跑，产物已按段保留）", interval))
				c.emit("stage", "segpipe", "error: "+c.brakes.reason())
				c.r.Manifest(c.p, "segpipe.watchdog", c.brakes.reason())
				// 二级兜底：刹车后若管线仍卡着收不回来（比如某处还有持锁死锁），
				// 2 分钟后强制落终态把 UI 解放掉——泄漏 goroutine 不绑架状态机。
				// selfGen = 触发时刻的本运行代际票：2 分钟里若自动重跑/用户重开
				// 接手了运行表，票对不上就放手，绝不开火误杀新运行（2026-10-18）。
				selfGen := c.r.runningGen(c.p.ID)
				go func(id, reason string, selfGen uint64) {
					time.Sleep(2 * time.Minute)
					c.r.forceFinishStall(id, "看门狗升级："+reason, selfGen)
				}(c.p.ID, c.brakes.reason(), selfGen)
				return
			}
		}
	}
}

// produceStallTimeout 全管线静默升级阈值（PRODUCE_STALL_TIMEOUT，默认 32m）。
// 必须大于审查工位两 journal 落账间的合法静默：多轮判卷每轮一次 LLM 调用
// （单次传输超时 10min），2-3 轮不落账=20-30 分钟静默是常态——15m 阈值实测
// 反复误杀健康的边界审（2026-10-18，每误杀一次重跑损失 45min+）。
// 超过此值也会被升级——如实失败可重跑好过无声悬挂。
func produceStallTimeout() time.Duration {
	if v := os.Getenv("PRODUCE_STALL_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > time.Minute {
			return d
		}
	}
	return 32 * time.Minute
}

func (c *coordinator) bndEntry(id string) journalLine {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.journal["bnd/"+id]
}

// stitch 拼接：RunFrom("render")。stitch_fix 清单的机械执行位 = 重跑 assemble
// （字幕时序由 TTS 时间戳确定性再生成）；不新增任何拼接层机制（§9.1）。
func (c *coordinator) stitch(ctx context.Context) error {
	c.emit("stage", "render", "running")
	c.r.Manifest(c.p, "stage.start", "render")
	if fixes := c.takeStitchFixes(); len(fixes) > 0 {
		if err := c.r.assemble(ctx, c.p); err != nil {
			return fmt.Errorf("拼接修正（assemble 重跑）失败: %w", err)
		}
		c.r.Manifest(c.p, "review.stitch_fix_applied", fmt.Sprintf("%d 条", len(fixes)))
	}
	if err := pipeline.RunFrom(ctx, c.p, "render"); err != nil {
		return fmt.Errorf("渲染/拼接失败: %w", err)
	}
	c.emit("stage", "render", "done")
	c.r.Manifest(c.p, "stage.finish", "render")
	return nil
}

// waitAllPassed 阻塞到全部段过审且全部边界过审（或刹车/取消）。
func (c *coordinator) waitAllPassed(ctx context.Context) error {
	for {
		if reason := c.brakes.reason(); reason != "" {
			return errors.New(reason)
		}
		if c.allPassed() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// allPassed 全部段 audited（当前代际在账）且全部边界在当前代际过审。
func (c *coordinator) allPassed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.segs {
		e := c.journal["seg/"+s.seg.ID]
		if e.State != stAudited || e.Gen != s.gen {
			return false
		}
	}
	for _, b := range c.bounds {
		if b.genP < 0 || b.genN < 0 {
			return false
		}
		s, n := c.seg(b.prev), c.seg(b.next)
		if s == nil || n == nil || s.gen != b.genP || n.gen != b.genN {
			return false
		}
	}
	return true
}

// cleanup 段级 check 子工程收尾（junction 先摘后删，绝不 RemoveAll 整目录）。
func (c *coordinator) cleanup() {
	for _, s := range c.segs {
		removeSegCheckDir(c.p, s.seg.ID)
	}
}

// ── 段链 worker：写 → 检 → 渲（审不在链上，渲完进审核队列）──────────

func (c *coordinator) chainWorker(ctx context.Context, s *segCtx) {
	defer c.guardWorker("段链 " + s.seg.ID)
	feedback := ""
	// 现起 worker 携带的首笔打回反馈（恢复进场的已过段场景）——起手就消费，
	// 不然段按无反馈重写一遍后再吃反馈又重写一遍，白烧一轮。
	c.mu.Lock()
	if s.pendingFeedback != "" {
		feedback = s.pendingFeedback
		s.pendingFeedback = ""
	}
	c.mu.Unlock()
	for {
		if reason := c.brakes.reason(); reason != "" {
			return // 刹车：停止派发新作业
		}
		if err := c.writeSegment(ctx, s, feedback); err != nil {
			if ctx.Err() != nil {
				return
			}
			c.fail(err)
			return
		}
		feedback = ""
		if err := c.checkSegment(ctx, s); err != nil {
			if ctx.Err() != nil {
				return
			}
			c.fail(err)
			return
		}
		if err := c.renderSegment(ctx, s); err != nil {
			if ctx.Err() != nil {
				return
			}
			c.fail(err)
			return
		}
		// 进审核队列（dispatcher 拥有轮次与打回路由）
		select {
		case <-ctx.Done():
			return
		case c.auditCh <- auditReq{kind: "seg", segID: s.seg.ID}:
		}
		// 等打回（或管线终结）
		select {
		case <-ctx.Done():
			return
		case fb := <-s.feedCh:
			feedback = fb
		}
	}
}

// writeSegment ④写画面（并发 ≤PRODUCE_WRITE_CONCURRENCY）：sceneGen 单段 → 合成物落盘 → 机械钳 → assemble。
// 写信号量在这里真正落闸（2026-10-17 429 风暴教训：writeSem 建了没接，15 段齐发打爆限流）。
func (c *coordinator) writeSegment(ctx context.Context, s *segCtx, feedback string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case c.writeSem <- struct{}{}:
	}
	defer func() { <-c.writeSem }()
	// 预算闸前置（P0-1）：耗尽就别开段了——等第一笔调用被拒再层层上抛，账面难看
	if err := llm.CheckBudget(ctx); err != nil {
		return err
	}
	c.writeGate(true)
	defer c.writeGate(false)
	c.emit("progress", "compositions", fmt.Sprintf("%s 写画面中", s.seg.ID))
	if err := c.r.sceneGen(ctx, c.p, "", feedback, s.seg.ID); err != nil {
		return fmt.Errorf("%s 场景生成失败: %w", s.seg.ID, err)
	}
	c.r.clearFrames(c.p, s.seg.ID)
	if err := segCompositions(c.p, s.seg.ID); err != nil {
		return err
	}
	pipeline.ClampSegmentFonts(c.p, s.seg.ID) // 机械钳：字体栈归一（别名→规范 Family，剥未知字体）
	pipeline.FixupSegmentFrame(c.p, s.seg.ID) // 机械钳：字体声明补齐（目录字体接 url()）
	pipeline.ClampSegmentFrame(c.p, s.seg.ID) // 机械钳：叠层声明
	if err := c.r.assemble(ctx, c.p); err != nil {
		return fmt.Errorf("组装失败: %w", err)
	}
	c.segState(s.seg.ID, stWritten, 0, 0, "")
	c.emit("progress", "compositions", fmt.Sprintf("%s 场景文件 ✓", s.seg.ID))
	return nil
}

// writeGate 写在飞计数（归零即重置写优先窗口）。
func (c *coordinator) writeGate(on bool) {
	c.mu.Lock()
	if on {
		c.writeInFlight++
		c.auditWhileW = 0
	} else {
		c.writeInFlight--
		if c.writeInFlight <= 0 {
			c.auditWhileW = 0
		}
	}
	c.mu.Unlock()
}

// auditGate 写优先于审（§4.3 第 2 条）：写队列非空的窗口里，审核每周期最多放行一个。
func (c *coordinator) auditGate(ctx context.Context) {
	for {
		c.mu.Lock()
		if c.writeInFlight == 0 || c.auditWhileW == 0 {
			if c.writeInFlight > 0 {
				c.auditWhileW++
			}
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// checkSegment 本段 check（junction 子工程，含 canvas 段回放门禁）。
func (c *coordinator) checkSegment(ctx context.Context, s *segCtx) error {
	if err := c.r.segCheck(ctx, c.p, s.seg.ID, s.dur); err != nil {
		return err
	}
	c.segState(s.seg.ID, stChecked, 0, 0, "")
	return nil
}

// renderSegment 段渲染（不被审核闸，§4.2）：渲池限流 → 内容指纹判旧（恢复进场
// 的已渲段不重渲，2026-10-19 中-14——stitch 侧 stageRender 本就按同一指纹跳过，
// 旧版这里无条件重渲，断点续跑对未变段片整段分钟级白渲）→ 帧数核对（rendered 位）。
func (c *coordinator) renderSegment(ctx context.Context, s *segCtx) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case c.renderSem <- struct{}{}:
	}
	defer func() { <-c.renderSem }()
	out := c.p.Artifact("renders/segs/" + s.seg.ID + ".mp4")
	if !pipeline.SegStale(c.p, s.seg.ID, out) {
		c.r.Manifest(c.p, "render.seg.cached", s.seg.ID)
	} else if err := pipeline.RenderSeg(c.p, s.seg.ID); err != nil {
		return err
	}
	dur := pipeline.SegVideoDuration(c.p, s.seg.ID)
	if dur <= 0 {
		return fmt.Errorf("%s 段片时长探测失败", s.seg.ID)
	}
	if err := verifySegFrames(c.p, s.seg.ID, dur); err != nil {
		// 帧数不符重渲一次，仍不符如实报错（段片完整性是 rendered 位的硬条件）
		c.r.Manifest(c.p, "render.reframes", s.seg.ID+": "+err.Error())
		if err2 := pipeline.RenderSeg(c.p, s.seg.ID); err2 != nil {
			return err2
		}
		if err2 := verifySegFrames(c.p, s.seg.ID, dur); err2 != nil {
			return fmt.Errorf("%s: %w", s.seg.ID, err2)
		}
	}
	s.dur = dur
	c.segState(s.seg.ID, stRendered, 0, 0, fmt.Sprintf("%.2fs", dur))
	c.emit("progress", "render", fmt.Sprintf("%s 段片 ✓（%.1fs）", s.seg.ID, dur))
	return nil
}

// verifySegFrames 帧数核对：实测帧数 ≈ 时长×帧率（±1% ±2 帧容差，首尾帧量化）。
func verifySegFrames(p *pipeline.Project, segID string, dur float64) error {
	frames, fps, err := pipeline.SegVideoFrames(p, segID)
	if err != nil {
		return err
	}
	want := dur * fps
	tol := 2 + want*0.01
	if float64(frames) < want-tol || float64(frames) > want+tol {
		return fmt.Errorf("帧数核对未过：实测 %d 帧，期望 %.0f±%.0f（%.2fs × %.2ffps）", frames, want, tol, dur, fps)
	}
	return nil
}

// guardWorker 协调器 goroutine 的 panic 兜底：把 panic 转成刹车错误，管线如实
// 失败可重跑——而不是 goroutine 僵死、整条流水线无声冻结（实例：nil map 写）。
func (c *coordinator) guardWorker(what string) {
	if rec := recover(); rec != nil {
		c.fail(fmt.Errorf("%s panic: %v", what, rec))
	}
}

func (c *coordinator) fail(err error) {
	c.brakes.trip("段链失败：" + err.Error())
	c.emit("error", "segpipe", pipeline.FirstLine(err.Error(), 160))
	c.r.Manifest(c.p, "segpipe.error", err.Error())
}

// ── 审核 dispatcher：写优先闸 + 池限流；段审/边界审同队列 ────────────

func (c *coordinator) auditDispatcher(ctx context.Context, auditWg *sync.WaitGroup) {
	defer c.guardWorker("审核调度")
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-c.auditCh:
			if c.brakes.reason() != "" {
				continue // 刹车：不再派发新审核
			}
			c.auditGate(ctx)
			select {
			case <-ctx.Done():
				return
			case c.auditSem <- struct{}{}:
			}
			auditWg.Add(1)
			go func(req auditReq) {
				defer auditWg.Done()
				defer func() { <-c.auditSem }()
				if req.kind == "seg" {
					c.auditSegment(ctx, req)
				} else {
					c.auditBoundary(ctx, req)
				}
			}(req)
		}
	}
}

// auditSegment 段审 → 意见单落盘 → 无 high = audited；有 high = 刹车判定 +
// 打回重写（≤2 轮）。审查器自身故障降级放行（宁放过不误杀，哲学同 frameQA）。
func (c *coordinator) auditSegment(ctx context.Context, req auditReq) {
	defer c.guardWorker("段审 " + req.segID)
	if err := llm.CheckBudget(ctx); err != nil { // 预算耗尽=刹车，不是审查器故障（勿降级放行）
		c.fail(err)
		return
	}
	s := c.seg(req.segID)
	if s == nil {
		return
	}
	c.mu.Lock()
	s.round++
	s.gen++
	round, gen := s.round, s.gen
	c.mu.Unlock()
	ends := c.sentenceEnds(s)
	in := review.SegInput{
		Seg:         s.seg,
		Sentences:   c.sentencePairs(s),
		Facts:       segFacts(c.script, s.seg),
		Rules:       designRulesDigest(c.sp),
		Samples:     c.sampleRefs(),
		Dur:         s.dur,
		Round:       round,
		AnchorTimes: c.anchorFramesFor(s, ends),
		LogPath:     c.p.Artifact(fmt.Sprintf("reviews/seg-%s.tools.jsonl", s.seg.ID)),
	}
	if round > 1 {
		in.Prior = c.loadVerdictFile("seg", s.seg.ID, round-1)
	}
	v, err := c.svc.AuditSegment(ctx, in)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, llm.ErrBudgetExceeded) { // 预算耗尽是刹车不是故障：降级放行只会烧穿预算继续跑
			c.fail(err)
			return
		}
		// 审查器故障：记账降级放行（不挡成片，用户知情——非阻塞哲学同 frameQA）
		c.r.Manifest(c.p, "review.seg_error", fmt.Sprintf("%s: %s", s.seg.ID, pipeline.FirstLine(err.Error(), 140)))
		c.emit("progress", "review", fmt.Sprintf("段审 %s 审查器故障，降级放行", s.seg.ID))
		c.segState(s.seg.ID, stAudited, round, gen, "审查器故障降级")
		return
	}
	_ = saveVerdict(c.p, "seg", s.seg.ID, round, &v)
	high := v.HighFindings()
	detail := fmt.Sprintf("%d 条意见（%d high）", len(v.Findings), len(high))
	if len(high) == 0 {
		c.brakes.clear(s.seg.ID) // 过审撤销刹车记账：修好了就不记仇
		c.segState(s.seg.ID, stAudited, round, gen, detail)
		c.emit("progress", "review", fmt.Sprintf("段审 %s 通过（第 %d 轮）", s.seg.ID, round))
		return
	}
	if reason := c.brakes.check(s.seg.ID, v.Signatures()); reason != "" {
		c.brakes.trip(reason + "（" + s.seg.ID + "）")
		c.emit("stage", "review", "error: "+c.brakes.reason())
		c.r.Manifest(c.p, "review.brake", c.brakes.reason())
		return
	}
	if round > maxSegRework {
		// 打回超限不再杀整片（2026-10-18 实测：seg06/seg02 两个顽固段各把整条
		// 制作拖死一波，三次尝试全折在"打回超限"）。改降级放行：意见单已落盘
		// reviews/ 可查，带伤记账明示——宁出带伤成片不出零成片；哲学同审查器
		// 故障放行。真系统性问题仍有签名刹车兜底（同签名连续两轮/跨段同类别），
		// 那才是该停机的信号；单个段反复修不动多半是 LLM 写不出满足判卷的实现，
		// 继续死磕只是烧钱。
		c.brakes.clear(s.seg.ID)
		c.segState(s.seg.ID, stAudited, round, gen, detail+"（打回超限，带伤放行）")
		c.emit("progress", "review", fmt.Sprintf("段审 %s %d 轮仍未过，带伤放行（意见单在 reviews/ 可查）", s.seg.ID, round))
		c.r.Manifest(c.p, "review.seg_pass_degraded", fmt.Sprintf("%s r%d: %s", s.seg.ID, round, detail))
		return
	}
	c.r.Manifest(c.p, "review.rework", fmt.Sprintf("%s r%d: %s", s.seg.ID, round, detail))
	c.emit("progress", "review", fmt.Sprintf("段审 %s %d 条 high，打回重写（第 %d 轮）", s.seg.ID, len(high), round))
	c.dispatchRewrite(ctx, s, redrawFeedback(high))
}

// spawnWorker 起该段的链 worker（幂等）。feedback 非空 = 恢复进场的已过段
// 被打回时现起，反馈作为首笔作业（chainWorker 起手取 pendingFeedback）。
func (c *coordinator) spawnWorker(s *segCtx, feedback string) {
	c.mu.Lock()
	if s.workerStarted {
		c.mu.Unlock()
		return
	}
	s.workerStarted = true
	s.pendingFeedback = feedback
	c.mu.Unlock()
	c.chainWg.Add(1)
	go func() {
		defer c.chainWg.Done()
		c.chainWorker(c.cctx, s)
	}()
}

// dispatchRewrite 打回重写投递。两条硬保证（P0-1，2026-10-17 楔死教训）：
//  1. 投递前必有接收者——恢复进场的已过段没有 worker，直接 spawn 并把反馈
//     作为首笔作业；旧版把反馈投进无主缓冲，段永不重写、waitAllPassed 永等。
//  2. 已有 worker 时投递限时 30s——worker 渲完必停在 feedCh 等，超时未取即
//     worker 异常，刹车转成可见失败而不是无限悬置。
func (c *coordinator) dispatchRewrite(ctx context.Context, s *segCtx, feedback string) {
	if s == nil {
		return
	}
	c.mu.Lock()
	started := s.workerStarted
	c.mu.Unlock()
	if !started {
		c.spawnWorker(s, feedback)
		return
	}
	select {
	case <-ctx.Done():
	case <-time.After(30 * time.Second):
		reason := fmt.Sprintf("%s 打回投递 30s 无人接收（worker 异常）", s.seg.ID)
		c.brakes.trip(reason)
		c.emit("stage", "review", "error: "+reason)
		c.r.Manifest(c.p, "review.brake", reason)
	case s.feedCh <- feedback:
	}
}

// ── 边界跟踪（§4.3 第 4 条）───────────────────────────────────────

// boundaryTracker 常驻：邻居两侧段审都过（当前代际）→ 派边界审；任一侧重写 →
// 代际失配自动失效重审。
func (c *coordinator) boundaryTracker(ctx context.Context) {
	defer c.guardWorker("边界跟踪")
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.passNotify:
		case <-time.After(800 * time.Millisecond):
		}
		for i, b := range c.bounds {
			s, n := c.seg(b.prev), c.seg(b.next)
			if s == nil || n == nil {
				continue
			}
			c.mu.Lock()
			gp, gn := b.genP, b.genN
			busy := c.bndBusy[i]
			c.mu.Unlock()
			sg, ng := c.segGen(s), c.segGen(n)
			if gp == sg && gn == ng {
				continue // 当前代际已审过
			}
			if !c.segAudited(s) || !c.segAudited(n) {
				continue // 两侧没都过审，等
			}
			if busy {
				continue // 在飞（auditBoundary 完成时自己释放）
			}
			c.mu.Lock()
			// 双检：等锁期间可能已有并发派发（passNotify 唤醒 vs 兜底轮询）
			if c.bndBusy[i] {
				c.mu.Unlock()
				continue
			}
			c.bndBusy[i] = true
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				c.mu.Lock()
				c.bndBusy[i] = false
				c.mu.Unlock()
				return
			case c.auditCh <- auditReq{kind: "bnd", bi: i}:
			default:
				c.mu.Lock()
				c.bndBusy[i] = false // 队列满：下轮再派
				c.mu.Unlock()
			}
		}
	}
}

func (c *coordinator) segGen(s *segCtx) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return s.gen
}

func (c *coordinator) segAudited(s *segCtx) bool {
	e := c.segEntry(s.seg.ID)
	return e.State == stAudited && e.Gen == c.segGen(s)
}

// auditBoundary 边界审 → 打回路由（anchor 时刻正负定前/后段）+ stitch_fix 清单。
func (c *coordinator) auditBoundary(ctx context.Context, req auditReq) {
	defer c.guardWorker("边界审")
	if err := llm.CheckBudget(ctx); err != nil { // 预算耗尽=刹车，不是审查器故障（勿降级放行）
		c.fail(err)
		return
	}
	// 在飞标志由本 goroutine 释放（含打回重写路径）——旧版靠跟踪器在「过审记账」
	// 分支复位，而打回路径不记账（genP/genN 保持 -1），标志永不复位：被打回段
	// 重写过审后该边界再无人重派，全管线静等看门狗（2026-10-19 萤火虫实测楔死）。
	defer func() {
		c.mu.Lock()
		c.bndBusy[req.bi] = false
		c.mu.Unlock()
	}()
	if req.bi < 0 || req.bi >= len(c.bounds) {
		return
	}
	b := c.bounds[req.bi]
	s, n := c.seg(b.prev), c.seg(b.next)
	if s == nil || n == nil {
		return
	}
	c.mu.Lock()
	rnd := b.round // 已打回次数（0 = 首审；递增在打回派发处，过审/降级清零）
	c.mu.Unlock()
	in := review.BndInput{
		ID: b.id, Prev: s.seg, Next: n.seg,
		PrevDur: pipeline.SegVideoDuration(c.p, s.seg.ID),
		NextDur: pipeline.SegVideoDuration(c.p, n.seg.ID),
		Round:   rnd + 1,
		LogPath: c.p.Artifact(fmt.Sprintf("reviews/bnd-%s.tools.jsonl", b.id)),
	}
	if c.sp.DesignSystem != nil && c.sp.DesignSystem.EntryExit != nil {
		in.EntryDur = c.sp.DesignSystem.EntryExit.EntryDur
	}
	v, err := c.svc.AuditBoundary(ctx, in)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, llm.ErrBudgetExceeded) { // 预算耗尽是刹车不是故障
			c.fail(err)
			return
		}
		c.r.Manifest(c.p, "review.bnd_error", b.id+": "+pipeline.FirstLine(err.Error(), 140))
		// 审查器故障：按当前代际通过记账（防打转），明细交用户。
		// 直接读 s.gen/n.gen 而非调 segGen——segGen 内部拿同一把 c.mu，二次
		// 加锁自锁（2026-10-17 转储实锤：死锁 goroutine 变成永久持锁者）；
		// s/n 是 c.seg() 来的同指针，gen 字段统一在 mu 内读写。
		c.mu.Lock()
		b.genP, b.genN = s.gen, n.gen
		b.round = 0
		gp, gn := b.genP, b.genN
		c.mu.Unlock()
		c.log(journalLine{Kind: "bnd", ID: b.id, State: stAudited, GenP: gp, GenN: gn, Detail: "审查器故障降级"})
		return
	}
	// 真实轮次落盘（2026-10-19 中-7：旧版恒写 round 1，多次重审互相覆盖不可考）
	_ = saveVerdict(c.p, "bnd", b.id, rnd+1, &v)
	// stitch 分流（2026-10-19 中-8）：stitch_fix 是拼接侧可机械修正的意见，不该
	// 触发段重画——旧版全部 high 一律打回，含 stitch_fix 的边界永远活不到 stitch
	// 阶段（takeStitchFixes 恒空，「能拼接修的别判 redraw」的提示词承诺落空）。
	high := v.HighFindings()
	var redraw []review.Finding
	for _, f := range high {
		if f.Fix != "stitch_fix" {
			redraw = append(redraw, f)
		}
	}
	if len(redraw) == 0 {
		// 全过，或只剩 stitch_fix（交 stitch 位执行）——都按当前代际通过记账
		c.mu.Lock()
		b.genP, b.genN = s.gen, n.gen
		b.round = 0 // 过审清轮次：新代际失效重审从首审起算
		gp, gn := b.genP, b.genN
		c.mu.Unlock()
		c.brakes.clear("bnd:" + b.id) // 过审撤销刹车记账
		detail := fmt.Sprintf("%d 条意见", len(v.Findings))
		if len(high) > 0 {
			detail = fmt.Sprintf("%d 条意见（%d 条 stitch_fix 交拼接修正）", len(v.Findings), len(high))
		}
		c.log(journalLine{Kind: "bnd", ID: b.id, State: stAudited, GenP: gp, GenN: gn, Detail: detail})
		c.emit("progress", "review", fmt.Sprintf("边界 %s 通过", b.id))
		return
	}
	// 打回路由：high 意见 anchor 的时刻符号投票定前/后段（负=前段内）
	prevVotes, nextVotes := 0, 0
	for _, f := range redraw {
		if t, ok := bndAnchorT(f.Anchor); ok {
			if t < 0 {
				prevVotes++
			} else {
				nextVotes++
			}
		}
	}
	target, dirWhy := n, "后段"
	if prevVotes > nextVotes {
		target, dirWhy = s, "前段"
	}
	if reason := c.brakes.check("bnd:"+b.id, v.Signatures()); reason != "" {
		c.brakes.trip(reason)
		c.emit("stage", "review", "error: "+c.brakes.reason())
		c.r.Manifest(c.p, "review.brake", c.brakes.reason())
		return
	}
	// 轮次上限（2026-10-19 中-7，与段审 maxSegRework 同口径）：bndBusy 死转修复
	// 后，打回-重写循环若无上界，签名刹车在「单边界+单段」场景凑不满阈值
	// （同签名连续两轮兜得住同意见循环，意见签名每轮漂移的循环就裸奔）——
	// 超限带伤放行：宁出带伤成片不出零成片（哲学同段审打回超限）。
	if rnd+1 > maxSegRework {
		c.brakes.clear("bnd:" + b.id)
		c.mu.Lock()
		b.genP, b.genN = s.gen, n.gen
		b.round = 0
		gp, gn := b.genP, b.genN
		c.mu.Unlock()
		c.log(journalLine{Kind: "bnd", ID: b.id, State: stAudited, GenP: gp, GenN: gn,
			Detail: fmt.Sprintf("第 %d 轮仍有 %d 条 high，带伤放行", rnd+1, len(redraw))})
		c.emit("progress", "review", fmt.Sprintf("边界 %s %d 轮仍未过，带伤放行（意见单在 reviews/ 可查）", b.id, rnd+1))
		c.r.Manifest(c.p, "review.bnd_pass_degraded", fmt.Sprintf("%s r%d: %d 条 high", b.id, rnd+1, len(redraw)))
		return
	}
	c.mu.Lock()
	b.round = rnd + 1
	c.mu.Unlock()
	c.r.Manifest(c.p, "review.rework", fmt.Sprintf("%s → %s（%s）", b.id, target.seg.ID, dirWhy))
	c.emit("progress", "review", fmt.Sprintf("边界 %s %d 条 high，打回 %s 重画", b.id, len(redraw), target.seg.ID))
	// 打回瞬间先摘账再派（同终审打回路径的模式）：bndBusy 在本函数返回时自释放
	// （打回路径不记账），被打回段的 journal 若还停在 audited@当前代际，
	// 跟踪器 ≤800ms 内就会用同一批像素重派边界审——假审计空烧 1-2 次多模态、
	// 相同意见再打回把轮次预算烧穿后提前记「带伤放行」，重写落地后还要白滚一轮
	// （2026-10-19 四路审查实锤）。stRewriting 让 segAudited 即刻为假，跟踪器
	// 自然等到重写落地后的新代际。
	c.segState(target.seg.ID, stRewriting, 0, 0, "边界审打回重写")
	c.dispatchRewrite(ctx, target, redrawFeedback(redraw))
}

// bndAnchorT 解析边界 anchor 的相对时刻（b02|t=-0.2 → -0.2, true）。
func bndAnchorT(anchor string) (float64, bool) {
	i := strings.Index(anchor, "|t=")
	if i < 0 {
		return 0, false
	}
	f, err := strconv.ParseFloat(anchor[i+3:], 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// takeStitchFixes 边界审 stitch_fix 意见 → 拼接修正清单（stitch 位执行 = 重跑
// assemble）。按轮次文件聚合（2026-10-19 中-7/中-8：意见单已落真实轮次——旧版
// 只读 round 1，而含 stitch_fix 的边界在新分流下按当前代际通过时往往不在 r1）。
// 幂等聚合不删文件；已执行的标记是 stitch 位的 review.stitch_fix_applied 账行。
func (c *coordinator) takeStitchFixes() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, b := range c.bounds {
		for r := 1; r <= maxSegRework+2; r++ {
			v := c.loadVerdictFile("bnd", b.id, r)
			if v == nil {
				continue
			}
			for _, f := range v.Findings {
				if f.Fix == "stitch_fix" && f.Severity == "high" {
					out = append(out, fmt.Sprintf("%s %s: %s", b.id, f.Anchor, f.Evidence))
				}
			}
		}
	}
	return out
}

// ── 全片终审（§7：12–16 帧四查；意见按时刻落段映射重写清单；≤2 轮）──

// finalAudit 跑一轮终审。返回需重写的段 id 清单（空 = 全过）。
func (c *coordinator) finalAudit(ctx context.Context, round int) ([]string, error) {
	if err := llm.CheckBudget(ctx); err != nil { // 预算耗尽=刹车（勿降级放行）
		return nil, err
	}
	tl := pipeline.LoadTimeline(c.p)
	if len(tl) == 0 {
		return nil, errors.New("终审缺 timeline（拼接未落 timeline.json）")
	}
	frames, paragraphs := finalFrames(c.p, c.sb, tl)
	total := tl[len(tl)-1].End
	in := review.FinalInput{
		Frames: frames, Paragraphs: paragraphs, TotalDur: total, Round: round,
		LogPath: c.p.Artifact(fmt.Sprintf("reviews/final-r%d.tools.jsonl", round)),
	}
	v, err := c.svc.AuditFinal(ctx, in)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("终审失败: %w", err)
		}
		if errors.Is(err, llm.ErrBudgetExceeded) { // 预算耗尽是刹车不是故障
			return nil, err
		}
		// 终审故障降级放行（2026-10-19 中-9）：段审/边界审故障都记账降级，唯独
		// 终审一次 LLM 抖动/预算烧尽就整片失败、再叠两轮自动重跑——口径拉齐：
		// 记账出片带账（review.final_error），意见单缺失如实可见。
		c.r.Manifest(c.p, "review.final_error", pipeline.FirstLine(err.Error(), 140))
		c.log(journalLine{Kind: "final", ID: "all", State: stAudited, Round: round, Detail: "终审审查器故障降级"})
		c.emit("stage", "review", "done: 全片终审审查器故障，降级放行（账在 manifest）")
		return nil, nil
	}
	_ = saveVerdict(c.p, "final", "all", round, &v)
	high := v.HighFindings()
	if len(high) == 0 {
		c.log(journalLine{Kind: "final", ID: "all", State: stAudited, Round: round, Detail: "全片终审通过"})
		c.emit("stage", "review", "done: 全片终审通过")
		return nil, nil
	}
	c.log(journalLine{Kind: "final", ID: "all", State: stRewriting, Round: round,
		Detail: fmt.Sprintf("%d 条 high，重写清单生成", len(high))})
	// 时刻 → 段映射（drift 含相邻段——§7）
	rewrite := map[string]bool{}
	for _, f := range high {
		t, perr := strconv.ParseFloat(strings.TrimPrefix(f.Anchor, "t="), 64)
		if perr != nil {
			continue
		}
		idx := segIndexAt(tl, t)
		if idx < 0 {
			continue
		}
		rewrite[tl[idx].ID] = true
		if f.Category == "drift" {
			if idx > 0 {
				rewrite[tl[idx-1].ID] = true
			}
			if idx+1 < len(tl) {
				rewrite[tl[idx+1].ID] = true
			}
		}
	}
	if len(rewrite) == 0 {
		return nil, fmt.Errorf("终审 %d 条 high 但时刻落段映射为空（anchor 非法）", len(high))
	}
	if reason := c.brakes.check("final", v.Signatures()); reason != "" {
		return nil, errors.New(reason)
	}
	if round > maxFinalRd {
		return nil, fmt.Errorf("全片终审 %d 轮仍未过，刹车待人工", maxFinalRd)
	}
	ids := make([]string, 0, len(rewrite))
	for _, s := range c.segs {
		if rewrite[s.seg.ID] {
			ids = append(ids, s.seg.ID)
		}
	}
	c.emit("progress", "review", fmt.Sprintf("终审 %d 条 high → 重写 %s（第 %d 轮）", len(high), strings.Join(ids, ","), round))
	return ids, nil
}

// segIndexAt 全片时刻 → 段序号。
func segIndexAt(tl []pipeline.TimelineRow, t float64) int {
	for i, row := range tl {
		if t >= row.Start && t < row.End {
			return i
		}
	}
	if len(tl) > 0 && t >= tl[len(tl)-1].End {
		return len(tl) - 1
	}
	return 0
}

// finalFrames 终审帧格（§7）：段落界帧（每段落首段起点）必取，剩余名额用段界帧
// 均匀补足，总帧 12–16；帧上标注「此刻=第几段/段落界」。
func finalFrames(p *pipeline.Project, sb *contract.Storyboard, tl []pipeline.TimelineRow) (frames []review.AnchorFrame, paragraphs []string) {
	paraStarts := map[string]bool{}
	if p != nil {
		if d, err := prep.LoadDraft(p); err == nil {
			for _, para := range d.Paragraphs {
				paragraphs = append(paragraphs, para.Title)
				if len(para.Segs) > 0 {
					paraStarts[para.Segs[0]] = true
				}
			}
		}
	}
	const minFrames, maxFrames = 12, 16
	type cand struct {
		t     float64
		label string
		pin   bool
	}
	var cands []cand
	for i, seg := range sb.Segments {
		if i >= len(tl) {
			break
		}
		if paraStarts[seg.ID] {
			cands = append(cands, cand{t: tl[i].Start, label: fmt.Sprintf("此刻=段落界·第%d段「%s」起点", i+1, seg.Key), pin: true})
		} else {
			cands = append(cands, cand{t: tl[i].Start, label: fmt.Sprintf("此刻=第%d段「%s」起点", i+1, seg.Key)})
		}
	}
	pinned := 0
	for _, cd := range cands {
		if cd.pin {
			pinned++
		}
	}
	// 超上限：非段落界帧均匀采样淘汰（段落界帧必取，§7）
	if len(cands) > maxFrames {
		keep := maxFrames - pinned
		var mids []cand
		for _, cd := range cands {
			if !cd.pin {
				mids = append(mids, cd)
			}
		}
		if keep > len(mids) {
			keep = len(mids)
		}
		var sampled []cand
		if keep > 0 {
			for i := 0; i < keep; i++ {
				sampled = append(sampled, mids[i*len(mids)/keep])
			}
		}
		var pins []cand
		for _, cd := range cands {
			if cd.pin {
				pins = append(pins, cd)
			}
		}
		cands = append(pins, sampled...)
	} else if len(cands) < minFrames {
		// 不足下限：段中点补帧，仍不足再补四分位点（短片才走得到）
		for i := 0; i < len(tl) && len(cands) < minFrames; i++ {
			if i >= len(sb.Segments) {
				break
			}
			cands = append(cands, cand{t: (tl[i].Start + tl[i].End) / 2,
				label: fmt.Sprintf("此刻=第%d段「%s」中点", i+1, sb.Segments[i].Key)})
		}
		for i := 0; i < len(tl) && len(cands) < minFrames; i++ {
			if i >= len(sb.Segments) {
				break
			}
			cands = append(cands, cand{t: tl[i].Start + (tl[i].End-tl[i].Start)*0.75,
				label: fmt.Sprintf("此刻=第%d段「%s」后段", i+1, sb.Segments[i].Key)})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].t < cands[j].t })
	for _, cd := range cands {
		frames = append(frames, review.AnchorFrame{T: round2(cd.t), Label: cd.label})
	}
	return frames, paragraphs
}

// ── 段审材料装配 ──────────────────────────────────────────────────

func (c *coordinator) sentencePairs(s *segCtx) [][2]string {
	out := make([][2]string, 0)
	for _, sen := range segSentences(c.script, s.seg) {
		out = append(out, [2]string{sen.ID, sen.Text})
	}
	return out
}

func (c *coordinator) sentenceEnds(s *segCtx) map[string]float64 {
	return sentenceEndTimes(segSentences(c.script, s.seg), c.voices[s.seg.ID].Words)
}

func (c *coordinator) anchorFramesFor(s *segCtx, ends map[string]float64) []review.AnchorFrame {
	return anchorFrames(segSentences(c.script, s.seg), ends, s.dur)
}

// sampleRefs 样张注入图（URL 喂模型 + 路径/标签给「过程」页缩略图；每次审核
// 同一套，算一次缓存）。
func (c *coordinator) sampleRefs() []llm.ImgRef {
	c.samplesOnce.Do(func() { c.sampleVis = styleSampleImages(c.p) })
	return visRefs(c.sampleVis)
}

func (c *coordinator) loadVerdictFile(kind, id string, round int) *review.Verdict {
	b, err := os.ReadFile(verdictPath(c.p, kind, id, round))
	if err != nil {
		return nil
	}
	var v review.Verdict
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	return &v
}

// designRulesDigest 设计规则要点（§6.3 可空段）：贯穿装置 + 皮肤切换规则 +
// 标准入/收尾模板——审核判「装置落点」维的依据。
func designRulesDigest(sp *contract.StylePack) string {
	if sp == nil || sp.DesignSystem == nil {
		return ""
	}
	ss := sp.DesignSystem
	var lines []string
	if ss.Device != nil && ss.Device.Name != "" {
		lines = append(lines, fmt.Sprintf("贯穿装置「%s」：%s；逐段运用：%s", ss.Device.Name, ss.Device.Desc, ss.Device.PerSeg))
	}
	if len(ss.EffectiveSkins()) == 2 && strings.TrimSpace(ss.SkinRule) != "" {
		lines = append(lines, "双皮肤切换规则："+ss.SkinRule)
	}
	if ss.EntryExit != nil && ss.EntryExit.Entry != "" {
		line := fmt.Sprintf("标准入场：%s；标准收尾：%s", ss.EntryExit.Entry, ss.EntryExit.Exit)
		if ss.EntryExit.EntryDur > 0 {
			line += fmt.Sprintf("（入场预算 %.2fs）", ss.EntryExit.EntryDur)
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

// redrawFeedback high 意见 → ④修复回喂（只带失败条目+证据+期望，§6.6）。
// 2026-10-18 实测教训：旧版"重写整个场景文件"等于每轮推倒重来——修好旧毛病的
// 同时随机带出新毛病，判卷逐轮漂移永不收敛（seg06 三轮换三种死法、seg02 四轮
// 不收敛）。改为外科手术式修改：只动点名的元素，其余逐字保留。
func redrawFeedback(high []review.Finding) string {
	var b strings.Builder
	b.WriteString("上一版画面被审核工位打回。在上一版场景文件的基础上做定点修改——只有下面点名的元素允许动，其余 HTML 结构、文案、CSS、动效全部逐字保留，禁止推倒重写、禁止顺手改构图/配色/动效：\n")
	for _, f := range high {
		fmt.Fprintf(&b, "- [%s][%s] %s\n", f.Category, f.Anchor, f.Evidence)
	}
	b.WriteString("\n修正要求：问题指向的画面元素按 evidence 里的期望改；anchor 是 N 几的以该句旁白承载的画面为准，t=几秒的以该时刻画面为准。上一版没被点名的问题说明已达标，改掉反而会引入新意见。\n")
	return b.String()
}

// finalFeedback 终审打回的段级回喂。
func finalFeedback(c *coordinator, segID string, round int) string {
	v := c.loadVerdictFile("final", "all", round)
	var b strings.Builder
	b.WriteString("上一版画面被全片终审打回，涉及本段的意见逐条修正：\n")
	hit := false
	if v != nil {
		tl := pipeline.LoadTimeline(c.p) // 逐条意见共用一份时间轴（中-L5：循环内反复读盘）
		for _, f := range v.HighFindings() {
			t, perr := strconv.ParseFloat(strings.TrimPrefix(f.Anchor, "t="), 64)
			if perr != nil {
				continue
			}
			if idx := segIndexAt(tl, t); idx >= 0 && tl[idx].ID == segID {
				fmt.Fprintf(&b, "- [%s][%s] %s\n", f.Category, f.Anchor, f.Evidence)
				hit = true
			}
		}
	}
	if !hit {
		b.WriteString("- [drift] 本段是风格渐变跑偏的相邻段：色系/字体/质感/装饰密度向全片基准（前后段与样张）看齐\n")
	}
	return b.String()
}
