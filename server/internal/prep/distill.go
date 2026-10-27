package prep

// distill.go —— 蒸馏监工（docs/style-kit-distillation-plan-2026-10-26.md 第 2 步）。
//
// 与音乐工位同套路（ReAct Loop + submit 唯一出口 + 带伤交门），但服务对象是
// 风格库条目而非项目：起一个一次性 Docker 毛坯房 → 递考卷袋（真迹帧+简报）→
// 画师 agent 在房里自由选型、用代码还原画风 → submit_kit 触发毕业检查
//（契约 lint / 重渲 MD5 / 依赖对账）+ 判卷官（真迹对照四维 + 换题考试）→
// 过线或带伤 → 画材包组装入库（Unverified，制作管线只见冻品是第 3 步的事）。
//
// 懒触发（方案 §3.1）：解析入库打「待蒸馏」标记，本监工不自动开单——
// admin API 单发触发，并发 = 1（方案 §5.1 多单排队）。
//
// 判卷与画师自查分离（kit 规范 §5）：判卷官是另一次独立的多模态调用，
// 保守口径 + 像素实测（measure.mjs）兜底，防 confidently 误判。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/pipeline"
	"webpage-video-agent/server/internal/stylelib"
)

// ── 单飞管理器（并发=1）────────────────────────────────────

var (
	distillMu   sync.Mutex
	distillCur  *distillRunInfo
	distillLast = map[string]map[string]any{} // entryID → 最近一单摘要
)

type distillRunInfo struct {
	EntryID   string    `json:"entry_id"`
	EntryName string    `json:"entry_name"`
	Phase     string    `json:"phase"` // bag/room/painting/judging/collecting
	StartedAt time.Time `json:"started_at"`
	Calls     int       `json:"calls"`
}

// DistillSnapshot 状态端点数据：当前在跑的单 + 各条目最近一单。
func DistillSnapshot() map[string]any {
	distillMu.Lock()
	defer distillMu.Unlock()
	out := map[string]any{"running": nil, "last": distillLast}
	if distillCur != nil {
		out["running"] = distillCur
	}
	return out
}

// StartDistill 开一单（异步跑；蒸馏房全局并发 1）。
func StartDistill(d Deps, entryID string) error {
	if d.Lib == nil {
		return errors.New("风格库未接线")
	}
	st, ok := d.Lib.Get(entryID)
	if !ok {
		return fmt.Errorf("%w: %s", stylelib.ErrNoEntry, entryID)
	}
	if st.Kit != nil {
		return fmt.Errorf("条目已有画材包（毕业资产防覆盖；确要重蒸先人工清理 kit 媒体与账字段）")
	}
	distillMu.Lock()
	defer distillMu.Unlock()
	if distillCur != nil {
		return fmt.Errorf("蒸馏房占用中（并发=1）：%s %s——多单排队", distillCur.EntryID, distillCur.Phase)
	}
	distillCur = &distillRunInfo{EntryID: entryID, EntryName: st.Name, Phase: "bag", StartedAt: time.Now()}
	go func() {
		err := runDistill(context.Background(), d, entryID)
		distillMu.Lock()
		summary := map[string]any{
			"entry_id": entryID, "entry_name": st.Name, "finished_at": time.Now(),
		}
		if err != nil {
			summary["ok"] = false
			summary["error"] = pipeline.FirstLine(err.Error(), 300)
		} else {
			summary["ok"] = true
		}
		distillLast[entryID] = summary
		distillCur = nil
		distillMu.Unlock()
		if err != nil {
			log.Printf("[distill] %s 失败: %v", entryID, err)
		}
	}()
	return nil
}

// ── 蒸馏单主体 ────────────────────────────────────────────

// distillManifest 画师交的清单（out/kit.json，与 stylelib.StyleKit 同构——
// 分开定义：房内产物的校验口径不该绑死在库 schema 上）。
type distillManifest struct {
	Framework  string                 `json:"framework"`
	Deps       []stylelib.StyleKitDep `json:"deps"`
	Files      []string               `json:"files"`
	CheatSheet string                 `json:"cheat_sheet"`
	Notes      string                 `json:"notes"`
}

type distillFrameScore struct {
	Frame       string  `json:"frame"`
	Pixel       float64 `json:"pixel"`
	Palette     float64 `json:"palette"`
	Texture     float64 `json:"texture"`
	Composition float64 `json:"composition"`
	Total       float64 `json:"total"`
	Fatal       string  `json:"fatal,omitempty"`
	Notes       string  `json:"notes"`
}

type distillVerdict struct {
	Frames  []distillFrameScore `json:"frames"`
	Neutral struct {
		Score float64 `json:"score"`
		Notes string  `json:"notes"`
	} `json:"neutral"`
	Summary string `json:"summary"`
}

// 过线门槛（kit 规范 §8 D2 暂行：每考题 ≥30/40 且无致命穿帮，换题 ≥70/100）。
func (v *distillVerdict) passed() bool {
	if v == nil || len(v.Frames) == 0 {
		return false
	}
	for _, f := range v.Frames {
		if f.Total < 30 || strings.TrimSpace(f.Fatal) != "" {
			return false
		}
	}
	return v.Neutral.Score >= 70
}

// reproduceLine 还原分一行（Score.Reproduce 的口径）。
func (v *distillVerdict) reproduceLine() string {
	if v == nil {
		return ""
	}
	parts := make([]string, 0, len(v.Frames))
	for i, f := range v.Frames {
		label := f.Frame
		if label == "" {
			label = string(rune('A' + i))
		}
		parts = append(parts, fmt.Sprintf("%s %.1f/40", label, f.Total))
	}
	return strings.Join(parts, " · ")
}

// distillJob 一单的全部状态。
type distillJob struct {
	d       Deps
	entry   *stylelib.Style
	taskDir string
	outDir  string
	truthN  int
	room    *distillRoom
	loop    *Loop

	judgeCalls int             // 判卷调用计数（封顶 distillJudgeCap）
	verdict    *distillVerdict // 最近一次判卷（带伤入库也要标分）
}

const distillJudgeCap = 3

func runDistill(ctx context.Context, d Deps, entryID string) error {
	entry, ok := d.Lib.Get(entryID)
	if !ok || entry.Kit != nil {
		return fmt.Errorf("条目状态已变（被删或已有 kit），本单作废: %s", entryID)
	}
	frames, _ := d.Lib.MediaNames(entryID)
	if len(frames) == 0 {
		return fmt.Errorf("条目 %s 没有真迹帧，开不了蒸馏单", entryID)
	}
	sort.Strings(frames)
	taskDir := filepath.Join(d.Lib.DistillDir(), entryID+"-"+time.Now().Format("20060102-150405"))
	job := &distillJob{
		d: d, entry: entry, taskDir: taskDir,
		outDir: filepath.Join(taskDir, "out"),
		truthN: len(frames),
	}
	if job.truthN > 3 {
		job.truthN = 3 // 考题取首/中/尾三帧（首单口径 A/B/C）
	}
	defer func() { job.phase("done") }()
	if err := job.buildBag(frames); err != nil {
		return err
	}
	job.room = &distillRoom{taskDir: taskDir, image: distillImage(), exec: realDockerExec}
	job.phase("room")
	if err := job.room.start(ctx); err != nil {
		return err
	}
	defer job.room.stop()

	wctx, cancel := context.WithTimeout(ctx, distillWall())
	defer cancel()

	rec := llm.OpenTranscript(taskDir, "distill")
	job.loop = &Loop{
		Prov:      distillPainterProv(),
		System:    distillSystem,
		Kickoff:   job.kickoff(),
		Tools:     append(job.room.tools(), job.submitTool()),
		Submit:    "submit_kit",
		MaxRounds: distillRounds(),
		OnStep: func(round int, kind, detail string) {
			job.phase("painting")
			if rec != nil {
				rec.Note("r%d %s %s", round, kind, pipeline.FirstLine(detail, 200))
			}
		},
	}
	if rec != nil {
		job.loop.Rec = rec
	}
	job.phase("painting")
	err := job.loop.Run(wctx)
	switch {
	case err == nil:
		return job.collect(false)
	case errors.Is(err, ErrBudgetExhausted), errors.Is(err, context.DeadlineExceeded):
		// 带伤交门（方案 §4.1 规矩一）：预算/墙钟到限，交当前最好版本并标分。
		log.Printf("[distill] %s 预算到限，走带伤收卷", entryID)
		if !job.artifactsPresent() {
			return fmt.Errorf("预算耗尽且 out/ 无可交付物（kit.json/kit.js 缺席）: %w", err)
		}
		v, _, jerr := job.evaluate(wctx, true)
		if jerr != nil {
			log.Printf("[distill] 带伤判卷失败（按未判卷入库）: %v", jerr)
		}
		_ = v
		return job.collect(true)
	default:
		return fmt.Errorf("蒸馏画师会话失败: %w", err)
	}
}

func (job *distillJob) phase(p string) {
	distillMu.Lock()
	if distillCur != nil && distillCur.EntryID == job.entry.ID {
		distillCur.Phase = p
		if job.loop != nil {
			distillCur.Calls = job.loop.Calls
		}
	}
	distillMu.Unlock()
}

// buildBag 组考卷袋：in/brief.md（风格档案——题材 Subject 永不进袋，题材中毒
// 的口子）+ in/task.md（考题说明）+ in/truth-N.png（真迹帧，首/中/尾三张）。
func (job *distillJob) buildBag(frames []string) error {
	inDir := filepath.Join(job.taskDir, "in")
	for _, dir := range []string{inDir, job.outDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if err := os.Chmod(job.outDir, 0o777); err != nil { // 容器内 uid 1000 要写
		return err
	}
	picked := pickSpread(frames, job.truthN)
	var b strings.Builder
	st := job.entry
	fmt.Fprintf(&b, "# 考题：%s\n\n主标签：%s", st.Name, st.Tags.Main)
	if len(st.Tags.Aux) > 0 {
		fmt.Fprintf(&b, "（%s）", strings.Join(st.Tags.Aux, "、"))
	}
	if st.Facets != (stylelib.StyleFacets{}) {
		fmt.Fprintf(&b, "\n分面：%s / %s / %s", st.Facets.Mood, st.Facets.Tone, st.Facets.Density)
	}
	if len(st.Signatures) > 0 {
		b.WriteString("\n\n特色清单（参考片真正独有的手法，机制描述）：")
		for _, sg := range st.Signatures {
			fmt.Fprintf(&b, "\n- %s：%s", sg.Name, sg.Mechanism)
		}
	}
	fmt.Fprintf(&b, "\n\n真迹帧 %d 张（in/truth-*.png）：还原的对象——还原效果不还原内容，判卷盯风格特征。", job.truthN)
	task := fmt.Sprintf(`# 考题说明

任务：把这套画风的「怎么画」蒸馏成可执行画材包（kit），交付到 out/。
第一件事：read_file /opt/distill-tools/SPEC.md（规则书：四类语义 API/库坑/去题材化/交付契约）。

考题：%d 张真迹帧，逐张出 scene-exam-1..%d.html 还原其画风与构图语言。
换题考试：scene-neutral.html（圆+方块+波浪）必须保持同款画风——只会抄考题在这里露馅。
迭代纪律：每轮改动后渲染出来亲眼看（read_file out/repro-*.png 附图回看），交付笔记要引用亲眼看到的像素证据。
交付：out/ 里备齐 kit.js、kit.json、scene-exam-*.html、scene-neutral.html（有依赖再加 package.json + package-lock.json），然后 submit_kit。
预算：回合与墙钟都有限，到限必须交当前最好版本（带伤也要交）。

房间布局：/task/in 考卷（只读）/ /task/out 交付 / /task/work 草稿 / /opt/distill-tools 工具（render.sh 渲染、sheet.mjs 拼对照、measure.mjs 像素实测、SPEC.md 规则书）。`,
		job.truthN, job.truthN)
	if err := os.WriteFile(filepath.Join(inDir, "brief.md"), []byte(b.String()), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(inDir, "task.md"), []byte(task), 0o644); err != nil {
		return err
	}
	for i, name := range picked {
		rc, _, _, ok := job.d.Lib.OpenMedia(job.entry.ID, name)
		if !ok {
			return fmt.Errorf("真迹帧 %s 读不到（媒体后端异常）", name)
		}
		dst, err := os.Create(filepath.Join(inDir, fmt.Sprintf("truth-%d.png", i+1)))
		if err != nil {
			rc.Close()
			return err
		}
		_, cErr := io.Copy(dst, rc)
		dErr := dst.Close()
		rc.Close()
		if cErr != nil || dErr != nil {
			return fmt.Errorf("真迹帧 %s 拷贝失败", name)
		}
	}
	return nil
}

// pickSpread 首帧/中帧/尾帧取 n 张（n ≤ len；单帧退化直取）——考题覆盖整片的
// 画风分布，避免只考开头。
func pickSpread(frames []string, n int) []string {
	if n >= len(frames) {
		out := make([]string, len(frames))
		copy(out, frames)
		return out
	}
	if n == 1 {
		return []string{frames[0]}
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, frames[i*(len(frames)-1)/(n-1)])
	}
	return out
}

func (job *distillJob) kickoff() string {
	return fmt.Sprintf("开工。蒸馏对象：《%s》（主标签 %s，特色 %d 条）。考卷在 in/（brief.md + %d 张真迹帧）。先读规则书与考卷、亲眼看真迹，再动手。交付的唯一出口是 submit_kit。",
		job.entry.Name, job.entry.Tags.Main, len(job.entry.Signatures), job.truthN)
}

// artifactsPresent 带伤收卷的门槛：清单+本体+至少一张考题场景在。
func (job *distillJob) artifactsPresent() bool {
	for _, f := range []string{"kit.json", "kit.js"} {
		if st, err := os.Stat(filepath.Join(job.outDir, f)); err != nil || st.IsDir() {
			return false
		}
	}
	found, _ := filepath.Glob(filepath.Join(job.outDir, "scene-exam-*.html"))
	return len(found) > 0
}

// evaluate 毕业检查全链：契约 lint → 重渲×2 对 MD5（⑤ 种子确定性）→ 拼对照
// → 像素实测 → 判卷官。返回判卷结果/问题清单/判卷调用错误。force=true 时
// 判卷照跑一次（带伤也要标分）但不拦截问题。
func (job *distillJob) evaluate(ctx context.Context, force bool) (*distillVerdict, []string, error) {
	job.phase("judging")
	var problems []string
	m, errs := loadDistillManifest(job.outDir)
	problems = append(problems, errs...)
	if m != nil {
		// 重渲考题+换题各两遍（同 seed）对 MD5；顺带产出判卷用的 repro/neutral/sheet。
		for i := 1; i <= job.truthN; i++ {
			if err := job.renderTwice(ctx, fmt.Sprintf("scene-exam-%d.html", i), fmt.Sprintf("repro-%d.png", i)); err != nil {
				problems = append(problems, err.Error())
			}
		}
		if err := job.renderTwice(ctx, "scene-neutral.html", "neutral.png"); err != nil {
			problems = append(problems, err.Error())
		}
		if len(problems) == 0 {
			if err := job.buildSheet(ctx); err != nil {
				problems = append(problems, err.Error())
			}
		}
	}
	if len(problems) > 0 && !force {
		return nil, problems, nil
	}
	// 判卷（换题与考题必须同配方版本——SPEC §7.1；这里渲的就是当前 out/ 的版本）
	if st, err := os.Stat(filepath.Join(job.outDir, "sheet.png")); err != nil || st.IsDir() {
		return nil, append(problems, "对照拼图 sheet.png 缺席（渲染失败？）"), nil
	}
	measure := ""
	if out, err := job.room.run(ctx, fmt.Sprintf("node /opt/distill-tools/measure.mjs %s", job.measureArgs()), 120*time.Second); err == nil {
		measure = out
	}
	v, err := job.judge(ctx, measure)
	if err != nil {
		return nil, problems, err
	}
	job.verdict = v
	if !v.passed() && !force {
		problems = append(problems, "判卷未过线："+v.headline())
	}
	return v, problems, nil
}

// renderTwice 同一场景渲两遍（同 seed，确定性契约），MD5 不一致=毕业检查⑤不过。
func (job *distillJob) renderTwice(ctx context.Context, html, png string) error {
	base := strings.TrimSuffix(png, ".png")
	render := func(out string) error {
		out2, err := job.room.run(ctx, fmt.Sprintf("/opt/distill-tools/render.sh out/%s out/%s 1280x720", html, out), 5*time.Minute)
		if err != nil {
			return fmt.Errorf("渲 %s 失败: %s %v", html, pipeline.TailLines(out2, 4), err)
		}
		if st, err := os.Stat(filepath.Join(job.outDir, out)); err != nil || st.Size() == 0 {
			return fmt.Errorf("渲 %s 没产出 %s", html, out)
		}
		return nil
	}
	if err := render(png); err != nil {
		return err
	}
	if err := render("." + base + "-check.png"); err != nil {
		return err
	}
	out, err := job.room.run(ctx, fmt.Sprintf("cmp -s out/%s out/.%s-check.png && echo SAME || echo DIFF", png, base), 30*time.Second)
	if err != nil || strings.TrimSpace(out) != "SAME" {
		return fmt.Errorf("%s 两次渲染不一致（毕业检查⑤种子确定性不过：查 Math.random/时间戳/未播种随机）", png)
	}
	_, _ = job.room.run(ctx, fmt.Sprintf("rm -f out/.%s-check.png", base), 15*time.Second)
	return nil
}

func (job *distillJob) measureArgs() string {
	args := make([]string, 0, job.truthN+1)
	for i := 1; i <= job.truthN; i++ {
		args = append(args, fmt.Sprintf("out/repro-%d.png", i))
	}
	return strings.Join(append(args, "out/neutral.png"), " ")
}

// buildSheet 真迹×还原 拼一张对照网格照（判卷官与画师自查共用同一张）。
func (job *distillJob) buildSheet(ctx context.Context) error {
	pairs := make([]string, 0, job.truthN*2)
	for i := 1; i <= job.truthN; i++ {
		pairs = append(pairs, fmt.Sprintf(`"真迹%d:in/truth-%d.png"`, i, i))
	}
	for i := 1; i <= job.truthN; i++ {
		pairs = append(pairs, fmt.Sprintf(`"还原%d:out/repro-%d.png"`, i, i))
	}
	out, err := job.room.run(ctx, "node /opt/distill-tools/sheet.mjs out/sheet.png "+strings.Join(pairs, " "), 2*time.Minute)
	if err != nil {
		return fmt.Errorf("拼对照失败: %s", pipeline.TailLines(out, 4))
	}
	return nil
}

// submitTool submit_kit：唯一合法出口。契约不过=问题清单回喂；判卷不过=判卷
// 意见回喂；到判卷封顶只收 force 提交（带伤交门）。
func (job *distillJob) submitTool() Tool {
	return Tool{
		Def: toolSchema("submit_kit", "提交画材包（唯一出口）：要求 out/ 已备齐 kit.js/kit.json/scene-exam-*.html/scene-neutral.html（有依赖再加 package.json+package-lock.json）。提交会触发机械毕业检查（重渲对 MD5/依赖对账）与判卷官打分，问题会回喂给你修。", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"summary": map[string]any{"type": "string", "description": "一句话：选型路线与还原策略"},
				"force":   map[string]any{"type": "boolean", "description": "判卷次数用尽后的带伤提交（分数如实入库）"},
			},
			"required": []string{"summary"},
		}),
		Run: func(ctx context.Context, args string) Observation {
			var in struct {
				Summary string `json:"summary"`
				Force   bool   `json:"force"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return Observation{Text: "参数错误：" + err.Error()}
			}
			if strings.TrimSpace(in.Summary) == "" {
				return Observation{Text: "summary 不能空：一句话说清选型与还原策略。"}
			}
			if !job.artifactsPresent() {
				return Observation{Text: "out/ 还不齐（kit.js/kit.json/scene-exam-*.html 至少一张）——备齐再交。"}
			}
			if job.judgeCalls >= distillJudgeCap && !in.Force {
				return Observation{Text: fmt.Sprintf("判卷已 %d 次（封顶 %d）。修完用 force=true 带伤提交，分数如实入库。", job.judgeCalls, distillJudgeCap)}
			}
			v, problems, jerr := job.evaluate(ctx, in.Force)
			switch {
			case jerr != nil && !in.Force:
				return Observation{Text: "判卷通道故障（可稍后重交）：" + jerr.Error()}
			case len(problems) > 0 && !in.Force:
				fb := "还没毕业，问题清单：\n- " + strings.Join(problems, "\n- ")
				if v != nil {
					fb += "\n" + v.feedback()
				}
				return Observation{Text: fb}
			}
			log.Printf("[distill] %s kit 提交通过（带伤=%v）", job.entry.ID, in.Force || (v != nil && !v.passed()))
			return Observation{Text: "画材包通过毕业检查，正在组装入库。", Done: true}
		},
	}
}

// judge 判卷官（独立多模态调用；DISTILL_JUDGE_MODEL 可换人，防自夸偏差）。
func (job *distillJob) judge(ctx context.Context, measure string) (*distillVerdict, error) {
	if job.judgeCalls >= distillJudgeCap {
		return nil, fmt.Errorf("判卷次数封顶（%d）", distillJudgeCap)
	}
	sheetURL, err := pipeline.ShrinkImageDataURL(filepath.Join(job.outDir, "sheet.png"), 1400, 82)
	if err != nil {
		return nil, fmt.Errorf("读对照拼图失败: %w", err)
	}
	neutralURL, err := pipeline.ShrinkImageDataURL(filepath.Join(job.outDir, "neutral.png"), 1100, 82)
	if err != nil {
		return nil, fmt.Errorf("读换题样图失败: %w", err)
	}
	prov := distillJudgeProv()
	user := fmt.Sprintf(`风格：《%s》。第 1 张图是对照网格照（前 %d 格真迹、后 %d 格该画材包的还原渲染，同编号对照）；第 2 张是换题样图（中性测试图：圆+方块+波浪）。
像素实测（程序化测量，优先于你的肉眼印象）：
%s
打分要求：每格还原帧按四维各 0-10 打分并给总分（像素感/色阶/质感/构图语言）；盯风格特征不逐像素比对内容；有致命穿帮（远看不神似/并排穿帮）必须写 fatal。换题样图 0-100 打风格保持分。只断言亲眼所见。`, job.entry.Name, job.truthN, job.truthN, strings.TrimSpace(measure))
	var v distillVerdict
	job.judgeCalls++
	u, err := prov.GenerateJSONVisionEffort(ctx, judgeSystem, user, []string{sheetURL, neutralURL}, 0.2, &v, llm.EffortHigh)
	if err != nil {
		return nil, err
	}
	if len(v.Frames) != job.truthN {
		// 少回帧会在子集上误判过线（passed 只看回到的帧）——按格式错误打回
		return nil, fmt.Errorf("判卷帧数 %d ≠ 考题数 %d（对照格没逐格打分）", len(v.Frames), job.truthN)
	}
	if job.loop != nil {
		llm.AccumulateUsage(&job.loop.Usage, u)
	}
	job.verdict = &v
	return &v, nil
}

func (v *distillVerdict) feedback() string {
	if v == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "判卷意见（%s）：", v.reproduceLine())
	for i, f := range v.Frames {
		label := f.Frame
		if label == "" {
			label = fmt.Sprintf("考题%d", i+1)
		}
		fmt.Fprintf(&b, "\n- %s %.1f/40（像素感%.0f 色阶%.0f 质感%.0f 构图%.0f）", label, f.Total, f.Pixel, f.Palette, f.Texture, f.Composition)
		if f.Fatal != "" {
			fmt.Fprintf(&b, " 致命：%s", f.Fatal)
		}
		if f.Notes != "" {
			fmt.Fprintf(&b, "——%s", f.Notes)
		}
	}
	fmt.Fprintf(&b, "\n- 换题 %.0f/100——%s", v.Neutral.Score, v.Neutral.Notes)
	if v.Summary != "" {
		fmt.Fprintf(&b, "\n总评：%s", v.Summary)
	}
	return b.String()
}

func (v *distillVerdict) headline() string {
	if v == nil {
		return "未判卷"
	}
	return fmt.Sprintf("%s · 换题 %.0f/100", v.reproduceLine(), v.Neutral.Score)
}

// collect 组装入库：出料过滤（白名单+限额）→ 媒体落位 kit/ + repro/neutral 样图
// → AttachKit 回账（Unverified 语义见方案 §6：用过它的片出片过审才转正）。
func (job *distillJob) collect(forced bool) error {
	job.phase("collecting")
	m, errs := loadDistillManifest(job.outDir)
	if m == nil {
		return fmt.Errorf("out/kit.json 不合格：%s", strings.Join(errs, "；"))
	}
	files, err := collectKitFiles(job.outDir, m)
	if err != nil {
		return err
	}
	score := stylelib.StyleKitScore{Rounds: job.loop.Calls}
	if job.verdict != nil {
		mark := "蒸馏判卷·过线"
		if !job.verdict.passed() {
			mark = "蒸馏判卷·未过线带伤"
		}
		score.Reproduce = job.verdict.reproduceLine() + "（" + mark + "）"
		score.Neutral = job.verdict.Neutral.Score
	} else {
		score.Reproduce = "未判卷（带伤入库）"
	}
	kit := &stylelib.StyleKit{
		Framework:  m.Framework,
		Deps:       m.Deps,
		Files:      files,
		CheatSheet: m.CheatSheet,
		Score:      score,
		Notes:      m.Notes,
	}
	if forced {
		kit.Notes = strings.TrimSpace("（预算耗尽带伤交卷）\n" + kit.Notes)
	}
	// 媒体落位：kit 代码 + 样图（repro/neutral 是 QA 对照锚，方案 §6）
	for _, f := range files {
		if err := job.d.Lib.PutMedia(job.entry.ID, "kit/"+f, filepath.Join(job.outDir, f)); err != nil {
			return fmt.Errorf("kit 文件 %s 入库失败: %w", f, err)
		}
	}
	for i := 1; i <= job.truthN; i++ {
		png := filepath.Join(job.outDir, fmt.Sprintf("repro-%d.png", i))
		if _, err := os.Stat(png); err == nil {
			if err := job.d.Lib.PutMedia(job.entry.ID, fmt.Sprintf("repro-%d.png", i), png); err != nil {
				return fmt.Errorf("还原样图 %d 入库失败: %w", i, err)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(job.outDir, "neutral.png")); err == nil {
		if err := job.d.Lib.PutMedia(job.entry.ID, "neutral.png", filepath.Join(job.outDir, "neutral.png")); err != nil {
			return fmt.Errorf("换题样图入库失败: %w", err)
		}
	}
	if err := job.d.Lib.AttachKit(job.entry.ID, kit); err != nil {
		return err
	}
	log.Printf("[distill] %s《%s》画材包入库：graduated=%v %s（%d 回合，判卷 %d 次）",
		job.entry.ID, job.entry.Name, job.verdict != nil && job.verdict.passed(), job.verdict.headline(), job.loop.Calls, job.judgeCalls)
	return nil
}

// collectKitFiles 出料清单：kit.json + 清单 files + package*.json（有依赖时）。
// 名称过 stylelib.ValidMediaName 的 kit/ 子树口径（字符集+防穿越），单文件 ≤2MB。
func collectKitFiles(outDir string, m *distillManifest) ([]string, error) {
	files := []string{"kit.json"}
	seen := map[string]bool{"kit.json": true, "kit.js": false}
	for _, f := range m.Files {
		if f == "" || seen[f] {
			continue
		}
		if !stylelib.ValidMediaName("kit/" + f) {
			return nil, fmt.Errorf("清单文件名 %q 不合法（字母/数字/._-，平铺不带子目录）", f)
		}
		files = append(files, f)
		seen[f] = true
	}
	if !seen["kit.js"] {
		files = append(files, "kit.js")
	}
	if len(m.Deps) > 0 {
		for _, f := range []string{"package.json", "package-lock.json"} {
			if st, err := os.Stat(filepath.Join(outDir, f)); err == nil && !st.IsDir() && !seen[f] {
				files = append(files, f)
				seen[f] = true
			}
		}
	}
	for _, f := range files {
		st, err := os.Stat(filepath.Join(outDir, f))
		if err != nil || st.IsDir() {
			return nil, fmt.Errorf("清单文件 %s 不在 out/", f)
		}
		if st.Size() > 2*1024*1024 {
			return nil, fmt.Errorf("文件 %s 超 2MB（画材包是代码不是素材库）", f)
		}
	}
	return files, nil
}

// loadDistillManifest 读+lint 房内清单。返回 (nil, 问题清单) 时不可继续。
func loadDistillManifest(outDir string) (*distillManifest, []string) {
	var errs []string
	b, err := os.ReadFile(filepath.Join(outDir, "kit.json"))
	if err != nil {
		return nil, []string{"kit.json 读不到：" + err.Error()}
	}
	var m distillManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, []string{"kit.json 不是合法 JSON：" + err.Error()}
	}
	if strings.TrimSpace(m.Framework) == "" {
		errs = append(errs, "framework 为空（选型+为什么，选型本身是风格知识）")
	}
	if n := len([]rune(m.CheatSheet)); n < 200 {
		errs = append(errs, fmt.Sprintf("cheat_sheet 太短（%d 字）——四类语义组织，这是画面工位真正注入的东西", n))
	} else if n > 8000 {
		errs = append(errs, fmt.Sprintf("cheat_sheet 超 8k 字（%d）——压到 ~2k token", n))
	}
	if strings.TrimSpace(m.Notes) == "" {
		errs = append(errs, "notes 为空（配方笔记：路线+已知弱点，≤14 行）")
	}
	if strings.Count(m.Notes, "\n") > 16 {
		errs = append(errs, "notes 超 14 行——提炼，别记流水账")
	}
	seenDep := map[string]bool{}
	for _, dep := range m.Deps {
		if strings.TrimSpace(dep.Name) == "" {
			errs = append(errs, "deps 有空包名")
			continue
		}
		if seenDep[dep.Name] {
			errs = append(errs, "deps 重复："+dep.Name)
		}
		seenDep[dep.Name] = true
		if !distillExactVersionRe.MatchString(dep.Version) {
			errs = append(errs, fmt.Sprintf("依赖 %s 版本 %q 不精确（要 1.2.3 形式，毕业检查①：@latest/^/~ 违法）", dep.Name, dep.Version))
		}
		if strings.TrimSpace(dep.License) == "" {
			errs = append(errs, "依赖 "+dep.Name+" 缺 license（毕业检查③：GPL 类标商用警示）")
		}
	}
	if len(m.Deps) > 0 {
		for _, f := range []string{"package.json", "package-lock.json"} {
			if _, err := os.Stat(filepath.Join(outDir, f)); err != nil {
				errs = append(errs, "声明了依赖但 out/ 缺 "+f+"（毕业检查②：lockfile 即账单）")
			}
		}
		// package.json 的依赖面 ⊆ 清单声明（防装了不报）
		if pb, err := os.ReadFile(filepath.Join(outDir, "package.json")); err == nil {
			var pkg struct {
				Dependencies map[string]string `json:"dependencies"`
			}
			if json.Unmarshal(pb, &pkg) == nil {
				for name, ver := range pkg.Dependencies {
					if !seenDep[name] {
						errs = append(errs, "package.json 装了 "+name+" 但 kit.json 没声明")
					}
					if strings.Contains(ver, "^") || strings.Contains(ver, "~") || strings.Contains(ver, "latest") {
						errs = append(errs, "package.json 里 "+name+" 版本 "+ver+" 不是钉死版本")
					}
				}
			}
		}
	}
	return &m, errs
}

var distillExactVersionRe = regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.\-+]+)?$`)

// ── Provider 取用 ─────────────────────────────────────────

// distillPainterProv 画师本体必须多模态（read_file 回看渲染结果走附图通道）。
func distillPainterProv() *llm.Provider {
	prov, _ := llm.FromEnv(llm.RoleVisual)
	return prov
}

// distillJudgeProv 判卷官：DISTILL_JUDGE_MODEL 可指定不同模型（判卷≠画师，
// 防自夸偏差）；缺省与画师同模型——配了第二个视觉模型就分家。
func distillJudgeProv() *llm.Provider {
	prov, _ := llm.FromEnv(llm.RoleVisual)
	if v := os.Getenv("DISTILL_JUDGE_MODEL"); v != "" && prov != nil {
		cp := *prov
		cp.Model = v
		return &cp
	}
	return prov
}

// ── 提示词（方案 §4.1：一句话级规矩，不是架构）────────────

const distillSystem = `你是蒸馏画师：在一次性 Docker 毛坯房里，用可执行代码还原一套参考视频的画风，交付画材包（kit）。
开局三件事：读 /opt/distill-tools/SPEC.md（规则书）；读 /task/in/brief.md 与 task.md（考卷）；read_file 逐张看真迹帧 in/truth-*.png。
工作区：/task/in 考卷（只读）· /task/out 交付 · /task/work 草稿。工具：render.sh 渲染、sheet.mjs 拼对照、measure.mjs 像素实测、licenses.mjs 列依赖账。
铁律：
- 必须看图：每轮改动后渲染出来亲眼看（read_file 回看 out/ 下的 PNG），交付笔记要引用亲眼看到的像素证据；盲画=白干。
- 还原效果不还原内容：判卷盯风格特征（笔触/色阶/质感/构图语言），神似>形似；kit 只存画法不存题材（去题材化纪律见 SPEC §4）。
- 种子确定性：一切随机走种子，禁 Math.random/时间戳——交付时每个场景会被重渲两遍对 MD5。
- 依赖钉精确版本（1.2.3 合法，^/~/@latest 违法），npm install 加 --ignore-scripts；选型自由但理由写进 kit.json 的 framework。
- 换题场景与考题场景必须同配方版本（旧参数应付换题是首单最大教训）。
交付：out/ 备齐 kit.js、kit.json（framework/deps/files/cheat_sheet/notes）、scene-exam-1..N.html、scene-neutral.html（圆+方块+波浪）；然后 submit_kit（唯一出口，机械检查+判卷不过会回喂意见）。
预算有限：到限必须交当前最好版本（submit_kit 带 force=true 带伤交卷），分数如实标。`

const judgeSystem = `你是风格还原判卷官，给画材包的还原样图打分。口径：
- 每张还原帧四维各 0-10：像素感（块感/锐度/媒介质感是否成立）、色阶（值域/色板命中度）、质感（分层/特效形态）、构图语言（镜头感/版式/画面秩序感）；总分为四维和。
- 盯风格特征，不逐像素比对内容——它不是在抄画。远看不神似或并排明显穿帮=致命，fatal 必须写明。
- 换题样图（中性测试图）0-100 打风格保持分：真画法换什么题都有那味儿，只会抄考题的在这里露馅。
- 像素实测数据优先于肉眼印象；没有证据的形容词不要写。只断言亲眼所见。
- 输出 JSON：{"frames":[{"frame":"A","pixel":0,"palette":0,"texture":0,"composition":0,"total":0,"fatal":"","notes":""}],"neutral":{"score":0,"notes":""},"summary":"一句话"}。frames 顺序与对照网格照里的还原格一致。`
