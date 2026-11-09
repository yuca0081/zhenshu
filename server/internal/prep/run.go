package prep

// run.go —— 准备阶段编排器（§5.1）：三个独立会话链式传工件，Go 拥有循环。
//
//	Run        管线入口：缺哪站跑哪站（重放语义）；LLM 未配置 = 降级返回 nil，
//	           legacy 单发路径照常接管（无 key 老路径零行为变化）。
//	RunStrict  反馈重跑入口（agent rerun_prep 工具 / api）：失败上抛。
//
// 预算耗尽/模型失败 → 至多一次冷重试（全新会话，工具状态归零）；重试仍败才报失败。
// 反馈路由（终审后）：改文字→重①（②③级联）/ 改风格→重②③ / 改结构场景→重③，
// 由 InvalidateFrom + Run 组合实现——工位内部没有「重跑」概念，失效即重放。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"webpage-video-agent/server/internal/agentlog"
	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/pipeline"
)

// 工位时间预算（2026-10-23 中-2）：总闸整链一刀切会被②的慢 LLM 大调用
// +样张机检/判卷/修复轮叠加烧穿（三次真机两轮 timeout、92 万 token 零产出）。
// 按工位分账：单站超线即收口、报清进度，
// ②临近预算线进入抢收窗口（styleGrabWindow）拒渲新样张、逼 submit 带伤交付。
var stationBudgets = map[string]time.Duration{
	StationScript: 6 * time.Minute,
	StationStyle:  40 * time.Minute,
	// 2026-10-24 12→25：回合预算放宽到 60 后（qwen 逐段估时长节奏慢），
	// 时间闸也得跟上——30 回合就吃了 12 分钟，60 回合时间闸先到照样掐。
	StationStoryboard: 25 * time.Minute,
}

// prepChainBudget 三工位链级总闸（runner.go 兜底闸，budget_test 同源断言）：
// 2026-10-27 60→80——③站闸放宽到 25 后工位预算和 71 分钟已越过旧总闸 60，
// ③实际只剩 14 分钟、25 分钟站闸成假账，长文稿复现「烧穿被掐」。
const prepChainBudget = 80 * time.Minute

func stationBudget(name string) time.Duration {
	if d, ok := stationBudgets[name]; ok {
		return d
	}
	return 15 * time.Minute
}

// stationLabels 工位人话名（预算收口/勾选集摘要共用）。
var stationLabels = map[string]string{
	StationMusic:      "音乐三件套",
	StationScript:     "①文字稿",
	StationStyle:      "②风格包",
	StationStoryboard: "③分镜稿",
}

// StationListText 计划工位的中文摘要（按执行序；api 的启动提示与 done 账共用）。
func StationListText(plan []string) string {
	out := make([]string, 0, len(plan))
	for _, s := range plan {
		if l := stationLabels[s]; l != "" {
			out = append(out, l)
		}
	}
	return strings.Join(out, " → ")
}

// doneStations 已落盘工件的人话清单（预算收口时报进度：烧在谁身上、还缺谁）。
func doneStations(p *pipeline.Project) string {
	var done []string
	for _, st := range []string{StationScript, StationStyle, StationStoryboard} {
		if HasArtifact(p, st) {
			done = append(done, stationLabels[st])
		}
	}
	if len(done) == 0 {
		return "无"
	}
	return strings.Join(done, "、")
}

// Available LLM 是否可用（不可用时管线入口降级 legacy，反馈重跑入口报错）。
// 查 prep 链实际用的工位而非规划档：工位各自绑定供应商后，规划档没 key 不再误杀。
func Available() bool {
	for _, st := range []llm.Role{llm.StationPrepScript, llm.StationPrepStyleText, llm.StationPrepStyleVision, llm.StationPrepStoryboard} {
		if _, err := llm.FromEnv(st); err == nil {
			return true
		}
	}
	return false
}

// LegacyProject 老项目判定：没有文字稿工件、但 legacy 分镜已在——prep 不碰它
// （旧项目升级靠显式反馈重跑，不靠管线偷偷重写）。
func LegacyProject(p *pipeline.Project) bool {
	if HasArtifact(p, StationScript) {
		return false
	}
	_, err := p.LoadStoryboard()
	return err == nil
}

// Run 管线入口（stagePrep 调用）：缓存语义——缺哪站跑哪站，已有工件跳过。
func Run(ctx context.Context, p *pipeline.Project, d Deps) error {
	return run(ctx, p, d, false, nil)
}

// RunStrict 严格入口（反馈重跑）：失败上抛；同为缓存语义（调用方先 InvalidateFrom）。
func RunStrict(ctx context.Context, p *pipeline.Project, d Deps) error {
	return run(ctx, p, d, true, nil)
}

// run 准备链编排。plan 为空 = 缓存语义（Run/RunStrict：存在即跳过）；
// plan 非空 = 主题页勾选集（Runner.Start）：只跑计划内工位，且计划内工位
// 一律重跑（勾选即重做）。计划内工位成功后级联作废其下游——这样「只勾文稿」
// 不会把新稿配旧风格，而是让风格/分镜空出来等下一次勾选。
func run(ctx context.Context, p *pipeline.Project, d Deps, strict bool, plan []string) error {
	if !Available() {
		if strict {
			return errors.New("LLM 未配置（LLM_API_KEY），准备工位无法重跑")
		}
		p.Manifest("prep.skipped", "LLM 未配置，准备阶段降级 legacy 单发路径")
		return nil
	}
	var planned map[string]bool
	if plan != nil {
		planned = make(map[string]bool, len(plan))
		for _, s := range plan {
			planned[s] = true
		}
	}
	// 项目 token 预算账（P0-1）：历史用量从 manifest 播种（重启不归零），塞进
	// ctx 后三工位的全部 LLM 调用自动过闸记账——风格包实测 17–26 万 token/片，
	// 闸挂在编排器一处比每个工位各自设防可靠。
	sp, sc, sl := agentlog.ProjectSpend(p)
	ctx = llm.WithAccount(ctx, llm.NewAccount(int64(sp), int64(sc), int64(sl), llm.ProjectTokenBudget()))
	// 统一准备链分支（rev2，docs/prep-unified-agent-design.md §3）：
	// unified_prep=1 的项目走单 agent 一条循环，不再按站分账。音乐模式优先。
	if ProjectMode(p) != "music" && UnifiedPrep(p) {
		return runUnified(ctx, p, d, plan)
	}
	type station struct {
		name  string
		run   func(context.Context) error
		after func()
	}
	// 驱动模式分流：音乐驱动 PV 走单站音乐工位（做曲→地图，一站三交付），
	// 文稿三工位整条不进——PV 的分镜工位（M2）与打包门上线前，制作入口另有
	// 拦截，半条链跑出来只会污染工件区。
	doneMsg := "三工件就绪，等待打包终审"
	if planned != nil {
		doneMsg = "本次已生成：" + StationListText(plan)
		if len(plan) == 3 {
			doneMsg = "三工件就绪，等待打包终审"
		} else if len(plan) > 0 && plan[len(plan)-1] != StationStoryboard {
			// 计划没跑到③：依赖本次上游的下游工件已被级联作废、留空
			doneMsg += "（下游未勾选，已按依赖作废，等下次补跑）"
		} else {
			doneMsg += "（其余工件保持原样）"
		}
	}
	// 站位落盘即广播 prep_updated：前端（App.vue）监听该事件重拉 view/manuscript/
	// style/prep——否则异步链只发 stage 事件，各页冻结在链启动前的旧态
	// （2026-10-30 GUI 实测：文稿页 0 字、风格页初始态、三工件齐了想法页还写进行中）。
	upd := func(station string) func() { return func() { d.emit(p.ID, "prep_updated", "", station) } }
	stations := []station{
		{StationScript, func(c context.Context) error { return runScriptStation(c, p, d) }, upd(StationScript)},
		{StationStyle, func(c context.Context) error { return runStyleStation(c, p, d) }, upd(StationStyle)},
		{StationStoryboard, func(c context.Context) error { return runStoryboardStation(c, p, d) }, upd(StationStoryboard)},
	}
	if ProjectMode(p) == "music" {
		stations = []station{
			{StationMusic, func(c context.Context) error { return runMusicStation(c, p, d) }, upd(StationMusic)},
		}
		doneMsg = "音乐三件套就绪（brief/曲/地图）——分镜工位上线后接续"
	}
	d.emit(p.ID, "stage", "prep", "running")
	for _, st := range stations {
		if planned != nil && !planned[st.name] {
			continue // 勾选集之外的工位：不跑、不动
		}
		if planned == nil && HasArtifact(p, st.name) {
			p.Manifest("prep.skip_cached", st.name)
			continue
		}
		// 工位时间预算（2026-10-23 中-2）：总闸 60 分钟一刀切会被②的慢 LLM 大调用
		// +样张修复轮烧穿（三次真机两轮 timeout、92 万 token 零产出）。按工位分账：
		// 单站超线即收口，报清「谁烧穿、已完成什么」，冷重试不再救预算耗尽。
		sctx, scancel := context.WithTimeout(ctx, stationBudget(st.name))
		err := runWithColdRetry(sctx, p, st.name, st.run)
		scancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				err = fmt.Errorf("%s超出 %s 时间预算（抢收窗口内未提交）——已完成：%s。重跑将从缺的工位续起，已完成工位走缓存不重烧",
					st.name, stationBudget(st.name), doneStations(p))
				p.Manifest("prep.station_budget", st.name)
			}
			p.Manifest("prep.error", st.name+": "+pipeline.FirstLine(err.Error(), 160))
			d.emit(p.ID, "stage", "prep", "error: "+pipeline.FirstLine(err.Error(), 120))
			d.emit(p.ID, "prep_updated", "", "error") // 链终止也要刷前端（running 态落定）
			return err
		}
		// 勾选即重做：本工位刚换了内容，下游工件立刻失效（不自动重做，除非也在
		// 计划里）。删除放在成功后——本工位失败时旧下游至少还能和旧上游配套。
		if planned != nil {
			switch st.name {
			case StationScript:
				InvalidateFrom(p, StationStyle)
			case StationStyle:
				InvalidateFrom(p, StationStoryboard)
			}
		}
		if st.after != nil {
			st.after()
		}
	}
	p.Manifest("prep.done", doneMsg)
	d.emit(p.ID, "stage", "prep", "done")
	if plan == nil {
		d.emit(p.ID, "prep_updated", "", "done")
	}
	// 勾选集的终态 prep_updated 由 Runner 在清空 active 之后补发：先清锁区再通知，
	// 前端刷新时读到的就是「已结束、已解锁」，不会停在锁着的完成态。
	return nil
}

// runTracked 工位循环的统一收口（「过程」页数据源）：对话留痕 + token 用量 +
// manifest agent.start/done。冷重试复用同一任务号——重试的对话接在同一留痕文件
// 后面，账上第二次 agent.start 开新组。
func runTracked(ctx context.Context, p *pipeline.Project, task, label string, loop *Loop) error {
	at := agentlog.Begin(p, task, label)
	loop.Rec = at.Rec()
	// 增量落账（2026-10-23 中-4）：逐调用喂 Task，未落账增量过 2 万 token 自动
	// Flush——进程死亡时已烧的钱不再整段消失（实测②一轮丢 40 万级）。
	loop.OnCall = at.Add
	err := loop.Run(ctx)
	at.Total(loop.Usage, loop.Calls)
	at.Done(err)
	return err
}

// runWithColdRetry 预算耗尽/模型失败 → 记账 + 至多一次冷重试（§5.2）。
// 冷重试是全新会话：Loop 与工具闭包都在 station.run 内新建，重调即归零。
// 预算耗尽（P0-1）不冷重试：重试必立刻再拒，白烧一轮记账。
func runWithColdRetry(ctx context.Context, p *pipeline.Project, name string, f func(context.Context) error) error {
	err := f(ctx)
	if err == nil || ctx.Err() != nil || errors.Is(err, llm.ErrBudgetExceeded) {
		return err
	}
	p.Manifest("prep.cold_retry", name+": "+pipeline.FirstLine(err.Error(), 160))
	return f(ctx)
}

// Summary 工件就绪态一行账（agent/终审 UI 用）。
func Summary(p *pipeline.Project) string {
	if ProjectMode(p) == "music" {
		if mp, err := loadMusicMap(p); err == nil {
			return fmt.Sprintf("整曲 %dBPM %.0f 秒 %d 段（brief/曲/地图三件套就绪）", mp.Bpm, mp.Duration, len(mp.Sections))
		}
		return "音乐三件套缺"
	}
	s := ""
	if HasArtifact(p, StationScript) {
		if sc, err := LoadScript(p); err == nil {
			s += fmt.Sprintf("文字稿 %d 句/%d 字；", len(sc.Sentences), sc.WordCount)
		}
	} else {
		s += "文字稿缺；"
	}
	if HasArtifact(p, StationStyle) {
		if sp, err := LoadStylePack(p); err == nil && sp.DesignSystem != nil {
			s += fmt.Sprintf("风格包「%s」；", sp.DesignSystem.Direction)
		}
	} else {
		s += "风格包缺；"
	}
	if HasArtifact(p, StationStoryboard) {
		if dr, err := LoadDraft(p); err == nil {
			s += fmt.Sprintf("分镜稿 %d 段；", len(dr.Segments))
		}
	} else {
		s += "分镜稿缺；"
	}
	return strings.TrimSuffix(s, "；")
}
