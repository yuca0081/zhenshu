package prep

// runner.go —— 准备阶段的异步运行器（「想法」页签的自动完成按钮底座）：
// 三工位链在后台 goroutine 跑（分钟级），SSE 播报进度、manifest 记账；
// 同项目互斥（与 produce 互斥表同款模式）、支持取消。
// Start 是全量入口：主题项目从①起步、粘贴稿件项目同样走这里——对话工具与
// 想法页签两条路共用同一条链。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

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
	// ErrPrepNoWork 勾选集为空（前端未选任何工位）；未知工位名单独报。
	ErrPrepNoWork = errors.New("没有要生成的工件——至少勾选一个工位")
	// ErrPrepStation 勾选集里出现未知工位名（api 层转 400，不落到 409 的「已在跑」）。
	ErrPrepStation = errors.New("工位名不合法")
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
	// active 本次链实际要跑的工位（含为满足下游而自动补跑的前置）。
	// 主题页签的「选区锁定」与 /prep 的 active 字段都读它；跑完即清。
	active map[string][]string
}

// NewRunner 构造运行器（Deps 与管线钩子共用同一份，emit/账本单源）。
func NewRunner(dataDir string, deps Deps) *Runner {
	return &Runner{
		DataDir: dataDir, Deps: deps,
		running: map[string]context.CancelFunc{},
		active:  map[string][]string{},
	}
}

// ActiveStations 本次链实际在跑的工位（未在跑返回空）。前端据此锁工作区：
// 不是「勾了什么锁什么」，而是「实际会重写什么锁什么」——下游要跑时上游
// 缺件会补跑，那个上游区域也必须锁，否则用户会在链跑到它之前改坏输入。
func (r *Runner) ActiveStations(id string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.active[id]...)
}

// IsRunning 该项目的准备链是否在跑。
func (r *Runner) IsRunning(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, live := r.running[id]
	return live
}

// Start 启动准备链。idea 非空落 input/prep_idea.txt（①②kickoff 消费）。
//
// stations = 本次要（重新）生成的工位（script/style/storyboard）：
//   - 空 = 三工位全选（老客户端/音乐 PV 兼容）；
//   - 勾了的下游缺前置时自动补跑（缺文稿补①、缺风格补②），planStations 求闭包；
//   - 勾选即重做：被勾的工位即使工件已在也重跑，成功后级联作废下游
//     （脚本成功→清风格/分镜；风格成功→清分镜）。未勾的下游不会被自动重做，
//     所以「只重做文稿」会留下空着的风格/分镜——这是链式依赖的事实，UI 要说明。
//
// force 仅保留给音乐 PV（三件套一锅端重做）；文稿链按勾选集驱动，不再需要它。
// 返回 (false, nil, nil) = 已在跑（幂等）；第二个返回值 = 实际执行计划（含补跑前置）。
func (r *Runner) Start(id, idea string, force bool, stations []string) (bool, []string, error) {
	r.mu.Lock()
	if _, live := r.running[id]; live {
		r.mu.Unlock()
		return false, nil, nil
	}
	p := pipeline.NewProject(r.DataDir, id)
	if _, err := os.Stat(p.Dir); err != nil {
		r.mu.Unlock()
		return false, nil, fmt.Errorf("项目不存在: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.running[id] = cancel
	r.mu.Unlock()
	release := func() {
		r.mu.Lock()
		delete(r.running, id)
		delete(r.active, id)
		r.mu.Unlock()
	}

	if LegacyProject(p) {
		release()
		return false, nil, ErrPrepLegacy
	}
	if !Available() {
		release()
		return false, nil, ErrPrepNoLLM
	}
	music := ProjectMode(p) == "music"
	var plan []string
	if music {
		// 音乐 PV 单站：完成标记是音乐地图；force 语义照旧（三件套一锅端）。
		if HasArtifact(p, StationMusic) && !force {
			release()
			return false, nil, errors.New("准备工件已生成——去「终审」页签确认，或勾选「按新想法重跑」")
		}
		if force {
			InvalidateFrom(p, StationMusic)
		}
		plan = []string{StationMusic}
	} else {
		// 老客户端（不传 stations）保留旧保护：三件齐时不再无确认地全量重跑。
		// 新主题页总会传勾选集（空勾选在前端就被禁用）。
		if len(stations) == 0 && !force && HasArtifact(p, StationStoryboard) {
			release()
			return false, nil, errors.New("准备工件已生成——勾选要重做的工件，或 force=true 全量重跑")
		}
		want, err := parseStations(stations)
		if err != nil {
			release()
			return false, nil, err
		}
		// 老客户端不传 stations：沿用「缺哪站跑哪站」的缓存语义（与管线 Run 一致），
		// 不要把它理解成「全勾重做」——那会在半成品项目上无谓重烧已完成工位。
		if len(stations) == 0 && !force {
			want = map[string]bool{
				StationScript:     !HasArtifact(p, StationScript),
				StationStyle:      !HasArtifact(p, StationStyle),
				StationStoryboard: !HasArtifact(p, StationStoryboard),
			}
		}
		plan = planStations(p, want)
		if len(plan) == 0 {
			release()
			return false, nil, ErrPrepNoWork
		}
		// 统一链（rev2）：三件互相影响，部分重做没有意义——勾选集收敛为三件
		// 全选（锁区按全三件；执行走 runUnified 单 agent 循环）。
		if UnifiedPrep(p) {
			plan = []string{StationScript, StationStyle, StationStoryboard}
			p.Manifest("prep.unified_plan", "统一链：三件一锅端（rev2 实验档）")
		}
	}
	if idea = strings.TrimSpace(idea); idea != "" {
		if err := os.MkdirAll(filepath.Dir(p.Artifact(IdeaFile)), 0o755); err != nil {
			release()
			return false, nil, err
		}
		if err := os.WriteFile(p.Artifact(IdeaFile), []byte(idea), 0o644); err != nil {
			release()
			return false, nil, err
		}
		applyIdeaDuration(p, idea)
	}
	// 先登记 active 再起 goroutine：前端可能在 stage running 事件后立刻拉 /prep，
	// 锁区集合不能晚于链启动出现（否则会闪一下可编辑态）。
	r.mu.Lock()
	r.active[id] = append([]string(nil), plan...)
	r.mu.Unlock()

	startMsg := "启动准备链（勾选 " + strings.Join(plan, "、") + "）"
	if music {
		startMsg = "启动音乐工位（做曲→音乐地图）"
	}
	p.Manifest("prep.start", startMsg)
	r.Deps.emit(id, "stage", "prep", "running")
	// 立即广播一次：前端要尽快拿到 active 去锁工作区。只靠每个工位跑完后的
	// prep_updated 太晚——第一站可能跑几分钟，这期间对应页签会一直可编辑。
	r.Deps.emit(id, "prep_updated", "", "start")
	// 总闸：回合预算放宽后（①30/②12/③10），单站烧穿预算的最坏墙钟很可观，
	// 再叠一次冷重试必须有个链级上限兜底——prepChainBudget（80 分钟，2026-10-27
	// 随③站闸 25 放宽）覆盖正常长链，卡死（端点挂住/无限重试）到点收口，不无限拖。
	runCtx, cancelRun := context.WithTimeout(ctx, prepChainBudget)
	go func() {
		defer release()
		defer cancelRun()
		// 清理过的老项目先回源工作区；链终了全量补传（工件/样张/manifest 上正本）
		if r.Deps.Artifacts != nil && r.Deps.Artifacts.Available() {
			if n, err := artifacts.Restore(r.Deps.Artifacts, r.DataDir, artifacts.LocalPrefix(id)); err != nil {
				fmt.Printf("[artifacts] %s 准备前回源中断: %v\n", id, err)
			} else if n > 0 {
				fmt.Printf("[artifacts] %s 回源 %d 个文件\n", id, n)
			}
		}
		err := run(runCtx, p, r.Deps, true, plan)
		// 内容链已收口：先放开锁区（active 清空）再补发终态刷新。prep_updated done
		// 若在清空之前发出，停在别的页签（IdeaPane 轮询已暂停）的前端会刷新到
		// 「running=true + active 还在」的旧态，锁就再也等不到解锁事件。
		r.mu.Lock()
		delete(r.active, id)
		r.mu.Unlock()
		r.Deps.emit(id, "prep_updated", "", "unlock")
		if r.Deps.Artifacts != nil && r.Deps.Artifacts.Available() {
			if n, err := artifacts.SyncUp(r.Deps.Artifacts, r.DataDir, artifacts.LocalPrefix(id)); err != nil {
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
	return true, plan, nil
}

// parseStations 解析勾选集：未知工位名报错；空集 = 全选（老客户端兼容）。
func parseStations(in []string) (map[string]bool, error) {
	want := map[string]bool{}
	if len(in) == 0 {
		for _, s := range []string{StationScript, StationStyle, StationStoryboard} {
			want[s] = true
		}
		return want, nil
	}
	for _, s := range in {
		switch s {
		case StationScript, StationStyle, StationStoryboard:
			want[s] = true
		default:
			return nil, fmt.Errorf("%w：%q（只能是 script/style/storyboard）", ErrPrepStation, s)
		}
	}
	return want, nil
}

// planStations 把勾选集求成实际执行闭包（按链序 ①→②→③）：
//   - 分镜要跑 → 风格必须可用；文稿要重跑或缺席时风格也得跟跑（它的输入变了）；
//   - 风格/分镜要跑 → 文稿必须可用，缺则补跑；
//   - 文稿单独重跑 → 只跑①，下游由链成功后的级联作废清空（不自动重做）。
func planStations(p *pipeline.Project, want map[string]bool) []string {
	if want[StationStoryboard] {
		if want[StationScript] || !HasArtifact(p, StationScript) || !HasArtifact(p, StationStyle) {
			want[StationStyle] = true
		}
	}
	if (want[StationStyle] || want[StationStoryboard]) && !HasArtifact(p, StationScript) {
		want[StationScript] = true
	}
	var plan []string
	for _, s := range []string{StationScript, StationStyle, StationStoryboard} {
		if want[s] {
			plan = append(plan, s)
		}
	}
	return plan
}

// RunInline 对话侧同步执行（agent 的 rerun_prep / draft_* 工具用）：与异步链
// 共用 running 互斥——已有链在跑返回 ErrPrepRunning；执行期间登记 running，
// 想法页 runPrep / cancelPrep / deleteProject 因此都能看到这条「链」在跑
// （2026-10-19 高-6 收口：对话侧原先直接 RunStrict 不登记，与想法页可双开
// 两条链互踩工件）。run 闭包里放作废+执行，ErrPrepRunning 时不会白作废。
func (r *Runner) RunInline(ctx context.Context, id string, run func(ctx context.Context) error) error {
	// 对话侧重跑不传勾选集：按全链锁（保守），避免某个工位正在被写而前端可编辑。
	plan := []string{StationScript, StationStyle, StationStoryboard}
	if ProjectMode(pipeline.NewProject(r.DataDir, id)) == "music" {
		plan = []string{StationMusic}
	}
	r.mu.Lock()
	if _, live := r.running[id]; live {
		r.mu.Unlock()
		return ErrPrepRunning
	}
	ictx, cancel := context.WithCancel(ctx)
	r.running[id] = cancel
	r.active[id] = plan
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.running, id)
		delete(r.active, id)
		r.mu.Unlock()
		// 对话侧同步重跑同样有锁区：先清 active 再补发刷新，避免前端停在锁住的终态。
		r.Deps.emit(id, "prep_updated", "", "unlock")
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

// Reconcile 开机对账（2026-10-23 中-4，与 produce.Reconcile 同职责）：上次进程
// 死在准备链中途的项目补写 interrupted 终态 + 聊天通告。进程死亡时 goroutine
// 随葬，manifest 只有 prep.start 没有 prep.done/error/cancelled——不补账的话
// 状态停半途、用户无从知晓，当轮 token 消耗也不可见。会话侧 RunInline 不写
// prep.start，不会误报；启动时 running 必为空，无需查在跑。
func (r *Runner) Reconcile(notify func(projectID, msg string)) {
	entries, err := os.ReadDir(filepath.Join(r.DataDir, "projects"))
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), "_") {
			continue
		}
		p := pipeline.NewProject(r.DataDir, e.Name())
		if !prepDiedMidflight(p) {
			continue
		}
		msg := "⏹ 上次准备阶段因服务重启中断（终态没来得及落账）。「想法」页签或对话里说一声即可重跑——已完成的工位走缓存，不会重烧。"
		p.Manifest("prep.interrupted", "服务重启，准备链中途死亡（Reconcile 补账）")
		if notify != nil {
			notify(e.Name(), msg)
		}
	}
}

// prepDiedMidflight 尾窗扫描：manifest 里最后一个 prep 生命周期事件是
// prep.start（无终态）= 死在中途。只读尾部 64KB，账是旁路，读坏了当无事。
func prepDiedMidflight(p *pipeline.Project) bool {
	f, err := os.Open(p.Artifact("manifest.jsonl"))
	if err != nil {
		return false
	}
	defer f.Close()
	const tail = 64 << 10
	info, err := f.Stat()
	if err != nil {
		return false
	}
	off := info.Size() - tail
	if off < 0 {
		off = 0
	}
	b := make([]byte, info.Size()-off)
	if _, err := f.ReadAt(b, off); err != nil {
		return false
	}
	lines := strings.Split(string(b), "\n")
	if off > 0 && len(lines) > 0 {
		lines = lines[1:] // 首行多半是半截，弃
	}
	last := ""
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		var e struct {
			Event string `json:"event"`
		}
		if json.Unmarshal([]byte(l), &e) != nil {
			continue
		}
		switch e.Event {
		case "prep.start":
			last = e.Event
		case "prep.done", "prep.error", "prep.cancelled", "prep.interrupted":
			last = e.Event
		}
	}
	return last == "prep.start"
}
