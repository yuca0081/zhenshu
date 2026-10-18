package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"webpage-video-agent/server/internal/artifacts"
	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/llm"
)

// ── 错误语义 ──────────────────────────────────────────────────────

// ErrAwaitLLM 会话模式（LLM_MODE=manual）：请求文件已写好，
// 等待产物文件出现后重跑继续（每步落盘、从任意步重放）。
type ErrAwaitLLM struct {
	RequestPath string
	OutputPath  string
	Note        string
}

func (e *ErrAwaitLLM) Error() string {
	note := e.Note
	if note == "" {
		note = "填写产物后重跑 m0 run 继续"
	}
	return fmt.Sprintf("等待 LLM 产物 %s（请求见 %s；%s）", e.OutputPath, e.RequestPath, note)
}

// ErrTodo 阶段属后续工作（TTS 引擎接入、hyperframes 渲染环境等），骨架先占位。
var ErrTodo = errors.New("阶段待实现（后续里程碑）")

// ── 项目 ──────────────────────────────────────────────────────────

type Project struct {
	ID  string
	Dir string
	// Ctx 任务级取消信号（pipeline.Run 注入）；runCLI 等长作业用其杀子进程。
	// 为 nil 时视为 context.Background()（CLI 直跑 m0 run 场景）。
	Ctx context.Context

	manifestMu sync.Mutex // 并发作业（spec 并行生成）下 manifest.jsonl 追加互斥
}

func NewProject(dataDir, id string) *Project {
	return &Project{ID: id, Dir: filepath.Join(dataDir, "projects", id)}
}

func (p *Project) Artifact(rel string) string { return filepath.Join(p.Dir, rel) }
func (p *Project) LLMRequest(name string) string {
	return filepath.Join(p.Dir, "llm", name+".request.md")
}
func (p *Project) LLMOutput(name string) string { return filepath.Join(p.Dir, "llm", name+".json") }
func (p *Project) LoadStoryboard() (*contract.Storyboard, error) {
	b, err := os.ReadFile(p.Artifact("storyboards/storyboard.json"))
	if err != nil {
		return nil, err
	}
	var sb contract.Storyboard
	if err := json.Unmarshal(b, &sb); err != nil {
		return nil, fmt.Errorf("storyboard.json 解析失败: %w", err)
	}
	return &sb, nil
}

// Manifest 每步事件落盘（审计与重放依据）。manifest 是 Reconcile 启动对账与 agent
// 终态汇报的唯一依据，终态一行丢失=对账全盲，写失败重试一次再放弃（仍只打 stderr
// 不上抛——记账失败不该挡制作主链路）。
func (p *Project) Manifest(event, detail string) {
	p.ManifestFields(event, detail, nil)
}

// ManifestFields 带额外结构化字段的账行（agent.start/done 的任务号/token 载荷）。
// extra 并入顶层 JSON；老账行与读取方不受影响（多余字段向后兼容）。
func (p *Project) ManifestFields(event, detail string, extra map[string]any) {
	f := p.Artifact("manifest.jsonl")
	now := time.Now()
	// ms（unix 毫秒）与 ts 同刻双写：ts 是秒级给人看的，活动流算每步用时吃毫秒差
	m := map[string]any{
		"ts": now.Format(time.RFC3339), "ms": now.UnixMilli(), "event": event, "detail": detail,
	}
	for k, v := range extra {
		m[k] = v
	}
	line, _ := json.Marshal(m)
	line = append(line, '\n')
	p.manifestMu.Lock()
	defer p.manifestMu.Unlock()
	for attempt := 0; attempt < 2; attempt++ {
		fh, err := os.OpenFile(f, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[manifest] 打开失败 %s: %v\n", f, err)
			continue
		}
		_, werr := fh.Write(line)
		_ = fh.Close()
		if werr == nil {
			return
		}
		fmt.Fprintf(os.Stderr, "[manifest] 写入失败 %s: %v\n", f, werr)
	}
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

// WriteFileAtomic 原子写：tmp+rename。project.json 这类高频读写的配置文件裸写
// 会被并发读者撞上撕裂窗口（stitch goroutine 读到半截 JSON → 静默丢 BGM/后期）。
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".atomic-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // rename 成功后是 no-op
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// tmpSuffixName 唯一临时产物名（ffmpeg 按扩展名推断封装格式，后缀必须保住）。
// 固定名（.post.tmp.mp4 等）在两个终装重叠时（StitchNow 不进 produce 互斥表）
// 会被交错写入后先完成者 rename 转正——静默产出损坏成片。
func tmpSuffixName(out, prefix, ext string) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(out), prefix+"-*"+ext)
	if err != nil {
		return "", err
	}
	name := f.Name()
	f.Close()
	os.Remove(name) // ffmpeg -y 自建；先占号防同目录并发拿到同名
	return name, nil
}

// CommandContext 是 ctxCommand 的跨包出口：produce/styleref 等包的所有外部进程
// 都必须走这里，否则取消时孙进程残留（详见 ctxCommand 注释）。
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	return ctxCommand(ctx, name, args...)
}

// ctxCommand 可取消的外部进程：ctx 结束时整棵进程树击杀。exec.CommandContext
// 默认只杀直接子进程——npx→node→hyperframes→chromium/ffmpeg 的孙进程会存活，
// 继续烧 CPU、写 renders/（win32 是主力开发平台，TerminateProcess 杀不动 cmd 包装的树）。
func ctxCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	setSysProcAttr(cmd)
	cmd.Cancel = func() error {
		// killProcessTree 连 pid 本身一起杀（unix 对进程组 Kill，win 是 taskkill /T），
		// 无需再 Process.Kill——进程已死时它只会往 Cancel 错误链里混 os.ErrProcessDone。
		if cmd.Process != nil {
			killProcessTree(cmd.Process.Pid)
		}
		return nil
	}
	cmd.WaitDelay = 10 * time.Second // 树杀后给孙进程管道收尾的宽限，超时强收
	return cmd
}

// ProjectMu 返回该项目 project.json 的读-改-写互斥锁。api 直改（如 applyStyle）
// 与 agent 轮内的 SetProjectField 并发时，双方各基于旧快照整文件回写，后写覆盖
// 先写（style_id 被 bgm 写入抹掉那类丢更新）——单笔原子写防撕裂不防丢更新，
// 必须串行化。凡手工读-改-写 project.json 的代码都必须持有它。
func ProjectMu(p *Project) *sync.Mutex {
	projMuLock.Lock()
	defer projMuLock.Unlock()
	mu := projMus[p.Dir]
	if mu == nil {
		mu = &sync.Mutex{}
		projMus[p.Dir] = mu
	}
	return mu
}

var (
	projMuLock sync.Mutex
	projMus    = map[string]*sync.Mutex{} // 按项目目录取锁
)

func pctx(p *Project) context.Context {
	if p.Ctx != nil {
		return p.Ctx
	}
	return context.Background()
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s 解析失败: %w", filepath.Base(path), err)
	}
	return nil
}

// ── 阶段定义 ──────────────────────────────────────────────────────

type Stage struct {
	Name string
	Run  func(p *Project) error
}

// Stages M0 管线：固定顺序 = 依赖方向（音频先于画面、基准先于适配）。
// prep 是准备阶段三工位（①文稿②风格③分镜，docs/prep-agents-design.md）——
// 经 PrepFn 钩子接线（main 注入 prep.Run，防 import 环）；产物齐全即重放跳过，
// LLM 未配置时内部降级，legacy 单发路径照常接管。
func Stages() []Stage {
	return []Stage{
		{Name: "prep", Run: stagePrep},
		{Name: "manuscript", Run: stageManuscript},
		{Name: "storyboard", Run: stageStoryboard},
		{Name: "style_samples", Run: stageStyleSamples},
		{Name: "tts", Run: stageTTS},
		{Name: "compositions", Run: stageCompositions},
		{Name: "check", Run: stageCheck},
		{Name: "render", Run: stageRender},
		{Name: "stitch", Run: stageStitch},
	}
}

// PrepFn 准备阶段工位钩子（main 接线 = prep.Run；nil = 未接线，prep 跳过）。
var PrepFn func(ctx context.Context, p *Project) error

// stagePrep 准备阶段：三工件（scripts/script.json、style/stylepack.json、
// storyboards/draft.json）任一缺失才跑工位；老项目（无文字稿工件但 legacy 分镜
// 已在）不碰——升级走显式反馈重跑，不靠管线偷偷重写。
func stagePrep(p *Project) error {
	if PrepFn == nil {
		p.Manifest("prep.skipped", "准备工位未接线")
		return nil
	}
	complete := true
	for _, rel := range []string{"scripts/script.json", "style/stylepack.json", "storyboards/draft.json"} {
		if !exists(p.Artifact(rel)) {
			complete = false
			break
		}
	}
	if complete {
		return nil // 重放语义：三工件齐全，翻译产物随工件在，直接进制作
	}
	if !exists(p.Artifact("scripts/script.json")) {
		if _, err := os.Stat(p.Artifact("storyboards/storyboard.json")); err == nil {
			p.Manifest("prep.skipped", "老项目（legacy 分镜已在）——不重写，升级走反馈重跑")
			return nil
		}
	}
	return PrepFn(pctx(p), p)
}

// Run 顺序执行阶段；遇 等待LLM/待实现/错误 即停（产物已落盘，重跑续上）。
// until 非空时执行到该阶段为止（含）。ctx 取消时在阶段边界退出（渲染中的
// 子进程由 runCLI 经 p.Ctx 连带杀掉）。
func Run(ctx context.Context, p *Project, until string) error {
	return runStages(ctx, p, Stages(), until)
}

// RunFrom 从 from 阶段起顺序执行到管线末尾（含）。段级流水线在自管逐段
// check 放行后只需要 render+stitch 的收尾能力——全阶段 Run 会把全局 check
// 栅栏又装回来。from 未知时按全阶段跑（保守：不静默丢前置）。
func RunFrom(ctx context.Context, p *Project, from string) error {
	stages := Stages()
	if from != "" {
		for i, st := range stages {
			if st.Name == from {
				stages = stages[i:]
				return runStages(ctx, p, stages, "")
			}
		}
	}
	return runStages(ctx, p, stages, "")
}

func runStages(ctx context.Context, p *Project, stages []Stage, until string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	p.Ctx = ctx
	for _, st := range stages {
		if err := ctx.Err(); err != nil {
			return err
		}
		fmt.Printf("▶ %-14s ", st.Name)
		err := st.Run(p)
		var await *ErrAwaitLLM
		switch {
		case err == nil:
			fmt.Println("✓")
			p.Manifest("stage.done", st.Name)
		case errors.As(err, &await):
			fmt.Println("⏸")
			fmt.Println("   ", err.Error())
			p.Manifest("stage.await", st.Name+": "+err.Error())
			return err
		case errors.Is(err, ErrTodo):
			fmt.Println("∘ 待实现")
			p.Manifest("stage.todo", st.Name)
			return err
		default:
			fmt.Println("✗ " + err.Error())
			p.Manifest("stage.error", st.Name+": "+err.Error())
			return err
		}
		if until != "" && st.Name == until {
			return nil
		}
	}
	fmt.Println("✅ 全部阶段完成")
	return nil
}

// ── 阶段实现 ──────────────────────────────────────────────────────

// stageManuscript 文稿：粘贴的文章正文即文稿（M0 输入），落成 manuscripts/manuscript.json。
func stageManuscript(p *Project) error {
	out := p.Artifact("manuscripts/manuscript.json")
	if exists(out) {
		return nil // 已有产物，跳过（重放语义）
	}
	raw, err := os.ReadFile(p.Artifact("input/article.txt"))
	if err != nil {
		return fmt.Errorf("缺少 input/article.txt: %w", err)
	}
	content := strings.TrimSpace(string(raw))
	if content == "" {
		return fmt.Errorf("文稿为空")
	}
	m := contract.NewManuscript(content, "paste")
	b, _ := json.MarshalIndent(m, "", "  ")
	if err := writeFile(out, string(b)); err != nil {
		return err
	}
	p.Manifest("manuscript.saved", fmt.Sprintf("%d 字", m.WordCount))
	return nil
}

// stageStoryboard 分镜（LLM 作业 #1）：api 模式直接调 DeepSeek；manual 模式走文件契约。
// api 失败（无 key/网络）自动降级为 ⏸ 等待人工产物，不阻塞管线。
func stageStoryboard(p *Project) error {
	final := p.Artifact("storyboards/storyboard.json")
	if exists(final) {
		var sb contract.Storyboard
		if err := readJSON(final, &sb); err != nil {
			return err
		}
		return contract.ValidateStoryboard(&sb)
	}
	accept := func(sb *contract.Storyboard) error {
		if err := contract.ValidateStoryboard(sb); err != nil {
			return fmt.Errorf("分镜未过自检: %w", err)
		}
		if err := writeFile(final, mustJSON(sb)); err != nil {
			return err
		}
		p.Manifest("storyboard.saved", fmt.Sprintf("%d 段 / %.0fs", len(sb.Segments), sb.TotalHint()))
		return nil
	}

	out := p.LLMOutput("storyboard")
	if exists(out) { // 会话模式：人工/上轮已填的产物
		var sb contract.Storyboard
		if err := readJSON(out, &sb); err != nil {
			return err
		}
		directorPass(p, &sb) // 导演定调（有 API key 才跑；失败降级不挡）
		return accept(&sb)
	}

	// API 模式
	prov, perr := llm.FromEnv(llm.RolePlan)
	if perr == nil {
		article, _ := os.ReadFile(p.Artifact("input/article.txt"))
		var sb contract.Storyboard
		usage, gerr := prov.GenerateJSON(pctx(p),
			"你是视频的分镜规划器。只输出 JSON。",
			storyboardUserPrompt(p, string(article)), &sb)
		if gerr == nil {
			p.Manifest("llm.usage", fmt.Sprintf("storyboard prompt=%d completion=%d tokens",
				usage.PromptTokens, usage.CompletionTokens))
			directorPass(p, &sb) // 导演定调（失败降级：分镜照常入库）
			if aerr := accept(&sb); aerr == nil {
				p.Manifest("storyboard.api", prov.Name+"/"+prov.Model)
				return nil
			} else {
				p.Manifest("storyboard.api.rejected", aerr.Error())
				// 校验拒绝：落一份请求文件供人工修正，不继续重试（bake-off 要记录失败）
				_ = writeFile(p.LLMRequest("storyboard"),
					storyboardUserPrompt(p, string(article))+fmt.Sprintf("\n\n<!-- 上次模型输出被拒绝: %s\n    产物写入 %s 后重跑 -->\n", aerr, out))
				return fmt.Errorf("API 分镜被自检拒绝: %w", aerr)
			}
		}
		p.Manifest("storyboard.api.error", gerr.Error())
	}

	// 降级：会话模式（写请求文件，等待产物）
	req := p.LLMRequest("storyboard")
	article, _ := os.ReadFile(p.Artifact("input/article.txt"))
	if err := writeFile(req, storyboardRequest(p, string(article), out)); err != nil {
		return err
	}
	return &ErrAwaitLLM{RequestPath: req, OutputPath: out}
}

// storyboardBrief 分镜作业正文：API 直调与会话请求共用一份（防两处漂移，同 designBrief 做法）。
// notes 为用户的分镜修改要求，空则不占位。
func storyboardBrief(article, notes string) string {
	noteBlock := ""
	if notes != "" {
		noteBlock = "\n\n用户对分镜的修改要求（优先满足）：\n" + notes
	}
	return fmt.Sprintf(`把文稿改写成视频分镜表 JSON。

## 要求
- timing_basis 固定 "narration"；plan 写一句话分段思路
- 每段 = {id:"seg01"起连续编号, idx, key:段落短标题, narration:旁白全文, visual_brief:画面提示一句话, duration_hint:秒数}
- 忠实原文，不编造事实；旁白只能来自文稿（口语化改写可以）
- 每段 8–16 秒；总时长 ≤ 900 秒（超限需压缩取舍）
- 可选 overlay（跨段统一角标：左上章节/右上计数器/底部进度线；默认不加，仅纪年、
  进程、章节感强的题材且用户明确提出时才启用）：
  头部加 "overlay":{"enabled":true,"chapter_counter":{"prefix":"距今","unit":"亿年","from":46.0,"to":0.0},"timeline":["章节1","章节2"]}（三锚点均可缺省）；
  启用时每段加 "chapter":"中文章节名|English Small Caps"（竖线前后可只留其一；相邻同章各段写同值）

## 文稿（%d 字）
---
%s
---%s`, utf8.RuneCountInString(article), article, noteBlock)
}

// storyboardNotes 用户的分镜修改要求（update_storyboard 工具写 input/storyboard_notes.txt）。
// 语义与 style_notes.txt 一致：文件在就一直生效，重新写入即替换。
func storyboardNotes(p *Project) string {
	b, err := os.ReadFile(p.Artifact("input/storyboard_notes.txt"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// storyboardUserPrompt API 模式的 user prompt：共用正文 + API 输出格式行。
func storyboardUserPrompt(p *Project, article string) string {
	return storyboardBrief(article, storyboardNotes(p)) +
		"\n\n只输出 JSON：第一个字符必须是 {、最后一个字符必须是 }，无 markdown 围栏、无注释。"
}

// ── 导演定调（storyboard 阶段第二段作业，director layer M1）──────────

// directorPass 导演定调：分镜本体落库前，读全片分镜补注 arc/motifs/逐段节拍
// （全片叙事视野的唯一生产者——分镜规划器逐段盲写，设计阶段只管视觉）。
// 校验拒绝回喂重出（≤3 轮，同设计阶段循环）；烧尽或无 provider → 降级：
// 清空导演字段按原分镜入库，绝不阻塞分镜落地（无 key 老路径零行为变化）。
func directorPass(p *Project, sb *contract.Storyboard) {
	prov, perr := llm.FromEnv(llm.RolePlan)
	if perr != nil {
		p.Manifest("director.plan_skipped", "无 LLM 配置，分镜不带导演统筹（全链路可正常制作）")
		return
	}
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		var dp contract.DirectorPlan
		usage, gerr := prov.GenerateJSON(pctx(p),
			"你是视频的导演统筹。只输出 JSON。", directorUserPrompt(sb, lastErr), &dp)
		if gerr == nil {
			if merr := applyDirectorPlan(sb, &dp); merr != nil {
				lastErr = merr
			} else if verr := contract.ValidateStoryboard(sb); verr != nil {
				lastErr = verr
			} else {
				chapters, motifs, beats := 0, len(sb.Motifs), 0
				if sb.Arc != nil {
					chapters = len(sb.Arc.Chapters)
				}
				for _, seg := range sb.Segments {
					beats += len(seg.Beats)
				}
				p.Manifest("llm.usage", fmt.Sprintf("director prompt=%d completion=%d tokens",
					usage.PromptTokens, usage.CompletionTokens))
				p.Manifest("director.plan", fmt.Sprintf("%d 章 / %d 母题 / %d 节拍", chapters, motifs, beats))
				return
			}
		} else {
			lastErr = gerr
		}
		p.Manifest("director.plan.rejected", fmt.Sprintf("第 %d 轮: %v", attempt, lastErr))
	}
	// 降级收尾：最后一轮可能已部分合入（合入成功但校验失败），清场再还分镜本体。
	sb.Arc, sb.Motifs = nil, nil
	for i := range sb.Segments {
		sb.Segments[i].Beats = nil
	}
	p.Manifest("director.plan_skipped", "导演统筹三轮未过，分镜按无导演字段入库: "+FirstLine(lastErr.Error(), 120))
}

// applyDirectorPlan 把导演定调合入分镜：本体字段（旁白/时长/标题）不经导演之手，
// 只动 arc/motifs/beats。先清场再合入——重试轮上一版的部分结果不能残留。
func applyDirectorPlan(sb *contract.Storyboard, dp *contract.DirectorPlan) error {
	byIdx := map[string]int{}
	for i, seg := range sb.Segments {
		byIdx[seg.ID] = i
	}
	for _, s := range dp.Segs {
		if _, ok := byIdx[s.ID]; !ok {
			return fmt.Errorf("导演节拍引用了不存在的段 %s", s.ID)
		}
	}
	sb.Arc = dp.Arc
	sb.Motifs = dp.Motifs
	for i := range sb.Segments {
		sb.Segments[i].Beats = nil
	}
	for _, s := range dp.Segs {
		sb.Segments[byIdx[s.ID]].Beats = s.Beats
	}
	return nil
}

// directorUserPrompt 导演作业正文 + 上轮校验拒绝的回喂（修复轮）。
func directorUserPrompt(sb *contract.Storyboard, lastErr error) string {
	feedback := ""
	if lastErr != nil {
		feedback = "\n\n上一版被自动校验拒绝，逐条修正后重新输出完整 JSON：\n- " + lastErr.Error()
	}
	return directorBrief(sb) + feedback
}

// directorBrief 导演定调作业正文。节拍锚句要求逐字抄旁白（校验硬拦），
// 所以这里给全文旁白而不是截断摘要。题材中立：对内容类型零预设。
func directorBrief(sb *contract.Storyboard) string {
	var segs strings.Builder
	for _, seg := range sb.Segments {
		fmt.Fprintf(&segs, "### %s（%s，约 %.0f 秒）\n旁白：%s\n画面提示：%s\n\n",
			seg.ID, seg.Key, seg.DurationHint, seg.Narration, seg.VisualBrief)
	}
	return fmt.Sprintf(`你是这部视频的导演。分镜表已经把内容切好了段；你的工作不是改它，而是补上「全片视角」——观众需要的起伏弧线、能被记住的复现意象、每一段「此刻该看到什么」的节拍。这些会被注入画面生成与审片验收，写得像给剧组下指令一样具体。

## 要求
- arc.spine：全片一句话主线（观众看完能复述的那一句）
- arc.chapters：2–5 章，segs 按段序连续铺满全部分段（不重不漏、不改顺序）；每章 tension 取 1–5；弧线必须有起伏，相邻章张力全部相同＝没有设计，会被打回
- motifs：0–3 个贯穿母题——同一视觉意象在 ≥2 段复现（role：establish 首次立起 / callback 中途复现 / echo 结尾呼应）；不是每片都需要母题，内容节奏快、意象扁平就写 []，硬编的母题会被审片打回
- segments[].beats：每段 2–5 条节拍，覆盖本段旁白的开头与收尾信息点。
  quote 必须逐字抄本段旁白的原句（连续原文，一个字都不能改——锚句要用来对时间轴）；
  expect 写「观众此刻该看到什么」（审片照它逐帧找画面），不写实现手法——怎么画是画面工程师的事
- 全片视角：钩子怎么立住、中段怎么递进、结尾怎么收束；只盯着单段写的节拍会被打回

## 分镜（%d 段 / 约 %.0f 秒）
%s## 输出 JSON 形态
{
  "arc": { "spine": "一句话主线", "chapters": [ { "title": "章名", "mood": "章情绪", "tension": 3, "segs": ["seg01","seg02"] } ] },
  "motifs": [ { "name": "母题名", "desc": "视觉形态一句话", "appearances": [ { "seg_id": "seg01", "role": "establish" } ] } ],
  "segments": [ { "id": "seg01", "beats": [ { "quote": "旁白原句", "expect": "观众此刻该看到什么" } ] } ]
}

只输出 JSON：第一个字符必须是 {、最后一个字符必须是 }，无 markdown 围栏、无注释。`,
		len(sb.Segments), sb.TotalHint(), segs.String())
}

// stageStyleSamples 风格样张 + 本片设计系统（LLM 作业 #2，v2 笼子二，见 design.go）：
// 必须 confirmed=true 才放行（硬门）。
// input/style_notes.txt 存在时按用户修改要求重生成（draft_style_samples 带 instruction 写入）。
func stageStyleSamples(p *Project) error {
	final := p.Artifact("style/style_samples.json")
	if exists(final) {
		var ss contract.StyleSamples
		if err := readJSON(final, &ss); err != nil {
			return err
		}
		if err := contract.ValidateStyleSamples(&ss); err != nil {
			return err
		}
		// 迷你真段补渲（幂等，只补缺）：确认门还没拍板才有意义——已确认的老项目
		// 不为预览烧渲染。中途断电重跑在这里续上。
		if !ss.Confirmed && EnsureSampleClips(p, &ss, nil) {
			// api/agent 会在管线跑动期间并发读样张文件，裸写有撕裂窗口，一律原子写
			_ = WriteFileAtomic(final, mustJSONBytes(ss), 0o644)
		}
		return LockDesign(p, &ss)
	}
	out := p.LLMOutput("style_samples")
	if exists(out) {
		var ss contract.StyleSamples
		if err := readJSON(out, &ss); err != nil {
			return err
		}
		if err := contract.ValidateStyleSamples(&ss); err != nil {
			return err // 包括 confirmed=false：等待用户确认
		}
		// 会话模式（人工/上轮产物）也过落点校验：简报存在时特色必须登记齐且真实现
		if err := LoadStyleBrief(p).CheckAbsorption(ss.SignatureAbsorption); err != nil {
			return fmt.Errorf("人工样张产物不合格: %w", err)
		}
		if err := LoadStyleBrief(p).CheckDynamicLanding(&ss); err != nil {
			return fmt.Errorf("人工样张产物不合格: %w", err)
		}
		if err := WriteFileAtomic(final, mustJSONBytes(ss), 0o644); err != nil {
			return err
		}
		p.Manifest("style.confirmed", ss.Direction)
		return LockDesign(p, &ss)
	}

	// API 模式：直出设计系统（confirmed 置 false，等用户在舞台确认——硬门由 final 落定校验把守）。
	// 开思考：设计系统是全片最有创造力的作业（现场发明视觉语言），值得慢与贵；
	// NoThink 实测产出发明性弱、往安全牌靠。每片一次成本可控，草稿校验带修复循环
	// （缺项回喂重出，≤2 轮）；风格简报存在时追加特色落点覆盖检查（v3 §4.5：
	// 机械数数，缺/越枚举=打回）。
	sb, err := p.LoadStoryboard()
	if err != nil {
		return fmt.Errorf("先完成 storyboard: %w", err)
	}
	prov, perr := llm.FromEnv(llm.RolePlan)
	if perr == nil {
		var ss contract.StyleSamples
		prompt := designUserPrompt(p, sb)
		var lastErr error
		for round := 0; round < 3; round++ {
			usage, gerr := prov.GenerateJSON(pctx(p), designSystemPrompt, prompt, &ss)
			if gerr != nil {
				lastErr = gerr
				break
			}
			verr := contract.ValidateStyleSamplesDraft(&ss)
			if verr == nil {
				verr = LoadStyleBrief(p).CheckAbsorption(ss.SignatureAbsorption)
			}
			if verr == nil {
				verr = LoadStyleBrief(p).CheckDynamicLanding(&ss)
			}
			if verr == nil {
				ss.Confirmed = false
				// api/agent 会在管线跑动期间并发读样张文件，裸写有撕裂窗口，一律原子写
				if err := WriteFileAtomic(final, mustJSONBytes(ss), 0o644); err != nil {
					return err
				}
				p.Manifest("llm.usage", fmt.Sprintf("style_samples prompt=%d completion=%d tokens",
					usage.PromptTokens, usage.CompletionTokens))
				p.Manifest("style.drafted", ss.Direction)
				p.Manifest("style_samples.api", prov.Name+"/"+prov.Model)
				// 迷你真段：草稿落定后立刻出动态小样（任何失败只降级静态，见 styleclips.go），
				// 完成后把 Video/ClipNote 回写进样张文件
				if EnsureSampleClips(p, &ss, sb) {
					_ = WriteFileAtomic(final, mustJSONBytes(ss), 0o644)
				}
				return nil
			}
			lastErr = verr
			prompt = designUserPrompt(p, sb) + "\n\n## 上一版被设计校验拒绝（缺项/越界必须补齐，其余保持不变）\n" + verr.Error()
		}
		p.Manifest("style_samples.api.error", lastErr.Error())
	}

	req := p.LLMRequest("style_samples")
	if err := writeFile(req, designRequest(p, sb, out)); err != nil {
		return err
	}
	return &ErrAwaitLLM{RequestPath: req, OutputPath: out, Note: "样张 JSON 中 confirmed 置 true 代表用户已确认（硬门）"}
}

// stageTTS 配音：每段音频 + 逐词时间戳。
// 批量生成器在 ai/ 侧（ai/tts_align.py）：引擎百炼（DASHSCOPE_API_KEY，qwen-audio-3.0-tts-plus）
// → Edge 兜底（微软免费）；FunASR paraformer 字级时间戳 → audio_meta.json。
//
// ⚠️ --text-file 文件必须存在，否则路径字符串会被当文本朗读（已踩坑）。
// 本阶段校验产物齐全。
func stageTTS(p *Project) error {
	sb, err := p.LoadStoryboard()
	if err != nil {
		return fmt.Errorf("先完成 storyboard: %w", err)
	}
	missing := 0
	for _, seg := range sb.Segments {
		if !exists(p.Artifact("audio/" + seg.ID + ".wav")) {
			missing++
		}
	}
	if missing > 0 {
		return fmt.Errorf("缺 %d/%d 段音频：运行 ai/tts_align.py 或 hyperframes tts（见上注释）: %w",
			missing, len(sb.Segments), ErrTodo)
	}
	if !exists(p.Artifact("audio_meta.json")) {
		return fmt.Errorf("缺 audio_meta.json（字级时间戳）: %w", ErrTodo)
	}
	p.Manifest("tts.ready", fmt.Sprintf("%d 段", len(sb.Segments)))
	return nil
}

// stageCompositions 合成物（LLM 作业 #3）：每段一个 HTML（含元素命名规范），可并行填写。
func stageCompositions(p *Project) error {
	sb, err := p.LoadStoryboard()
	if err != nil {
		return fmt.Errorf("先完成 storyboard: %w", err)
	}
	// 样式方向注入 packet
	direction := ""
	var ss contract.StyleSamples
	if readJSON(p.Artifact("style/style_samples.json"), &ss) == nil {
		direction = ss.Direction
	}
	awaiting := ""
	for _, seg := range sb.Segments {
		comp := p.Artifact("compositions/frames/" + seg.ID + ".html")
		if exists(comp) {
			continue
		}
		name := "comp-" + seg.ID
		out := p.LLMOutput(name)
		if exists(out) {
			b, rerr := os.ReadFile(out)
			if rerr != nil {
				return rerr
			}
			// 产物格式：{"html": "...", "elements": [...]}
			var payload struct {
				HTML     string                  `json:"html"`
				Elements []contract.NamedElement `json:"elements"`
			}
			if err := json.Unmarshal(b, &payload); err != nil {
				return fmt.Errorf("%s 解析失败: %w", out, err)
			}
			if payload.HTML == "" || len(payload.Elements) == 0 {
				return fmt.Errorf("%s 缺少 html 或 elements（元素命名规范是硬约束）", out)
			}
			if err := writeFile(comp, payload.HTML); err != nil {
				return err
			}
			reg, _ := json.MarshalIndent(contract.ElementRegistry{Elements: payload.Elements}, "", "  ")
			_ = writeFile(p.Artifact("compositions/registry/"+seg.ID+".json"), string(reg))
			p.Manifest("composition.saved", seg.ID)
			continue
		}
		req := p.LLMRequest(name)
		if err := writeFile(req, compRequest(p, seg, direction, out)); err != nil {
			return err
		}
		awaiting = seg.ID
	}
	if awaiting != "" {
		return &ErrAwaitLLM{
			RequestPath: p.LLMRequest("comp-" + awaiting),
			OutputPath:  p.LLMOutput("comp-" + awaiting),
			Note:        "每段一个产物文件，可任意顺序/并行填写",
		}
	}
	// 场景 HTML 忘写系统字体 @font-face 是 lint 硬错误且 LLM 修复轮不收敛——落盘侧机械补齐
	fixupFrameFonts(p)
	// 叠层声明机械钳：设计没声明叠层版式时剥掉 data-layout-allow-overlap（防修复轮
	// 被报了就贴属性骗检）；声明过的片卡单帧上限。首发与修复轮都过这里。
	clampOverlapDecls(p)
	return nil
}

// stageCheck 质量门禁：调 hyperframes check（lint+runtime+layout+motion+contrast）。
// 输入集自上次通过后没有任何变化时直接复用结果：Run 的语义是顺序执行 until 之前的
// 所有阶段，produce 的 happy path 里 checkWithRepair → Run("render") → Run("stitch")
// 会对同一输入连跑 3 次门禁，rework/frameQA 修复轮同样三跑——check 是分钟级的
// 浏览器作业，段片渲染有 segStale 缓存而它没有，重复跑纯属白付。
func stageCheck(p *Project) error {
	if !checkStale(p) {
		fmt.Printf("  · check 输入未变，复用上次结果\n")
		p.Manifest("check.skipped_fresh", "")
		return nil
	}
	out, err := runCLI(p, "npx", "--yes", "hyperframes@0.8.55", "check", "--no-browser-gpu")
	if err != nil {
		// 完整输出：上游修复轮要从 ✗ 行提取报错段（截断会丢 Layout 段的重叠错误）；
		// 对话/清单侧各自再做 tail/挑行
		return fmt.Errorf("check 未过（诊断如下，修复后重跑）:\n%s", out)
	}
	_ = writeFile(p.Artifact(".hyperframes-ok"), "check passed "+time.Now().Format(time.RFC3339)+"\n")
	WriteCheckStamp(p)
	p.Manifest("check.passed", "")
	return nil
}

// checkStampPath check 复用戳：内容随意，起的是「上次通过时刻」mtime 基准的作用。
func checkStampPath(p *Project) string { return p.Artifact(".check.stamp") }

// WriteCheckStamp check 通过后落戳。调用时机 = 门禁真跑且通过；checkWithRepair 的
// 放行点（回放门禁也过掉）会再补一次，把「check+回放整轮通过」的语义钉在戳上。
func WriteCheckStamp(p *Project) {
	_ = writeFile(checkStampPath(p), "check passed "+time.Now().Format(time.RFC3339)+"\n")
}

// checkStale check 门禁是否需要真跑：无戳（首次/上次失败/中途被杀），或输入集任一
// 文件 mtime 新于戳。修复轮重出场景文件、机械回填动帧都会刷新输入 mtime，戳天然
// 失效——这正是「修复轮后必须真检」要的语义。输入集 = stageCheck 实际消费的合成物
// （主组合 + 段帧 + 字幕子组合），外加分镜与旁白时间戳兜底（agent 侧改稿后即使还没
// 重组装也触发重检）。刻意不含 style/design.md、skin.json：LockDesign 每次 Run 都
// 无条件重写它们，计入会把缓存打穿成每阶段重跑；设计变更的真实传导路径是段帧重生成，
// 段帧 mtime 已覆盖。
func checkStale(p *Project) bool {
	stamp, err := os.Stat(checkStampPath(p))
	if err != nil {
		return true
	}
	cutoff := stamp.ModTime()
	for _, rel := range []string{"index.html", "storyboards/storyboard.json", "audio_meta.json"} {
		if fi, err := os.Stat(p.Artifact(rel)); err == nil && fi.ModTime().After(cutoff) {
			return true
		}
	}
	for _, dir := range []string{"compositions/frames", "compositions/caps"} {
		entries, err := os.ReadDir(p.Artifact(dir))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".html") {
				continue
			}
			if fi, err := e.Info(); err == nil && fi.ModTime().After(cutoff) {
				return true
			}
		}
	}
	return false
}

// stageRender 按段渲染（增量）+ 无损拼接。
// 每段一个单段工程 .hf-seg/segNN.html（assemble 生成，含该段画面与配音），
// 渲出 renders/segs/segNN.mp4；段片缺失或段输入内容（帧html/音频/段工程，哈希
// 指纹见 segStale）与上次渲染不一致则重渲，其余段跳过——rework 只重渲改动段，
// 耗时与段长成正比而非片长。
// main.mp4 = 人声母版 concat -c copy 后按项目配乐混音（见 stageStitch）。
func stageRender(p *Project) error {
	sb, err := p.LoadStoryboard()
	if err != nil {
		return err
	}
	segsDir := p.Artifact("renders/segs")
	if err := os.MkdirAll(segsDir, 0o755); err != nil {
		return err
	}
	live := map[string]bool{}
	for _, seg := range sb.Segments {
		live[seg.ID] = true
		out := filepath.Join(segsDir, seg.ID+".mp4")
		if !segStale(p, seg.ID, out) {
			p.Manifest("render.seg.cached", seg.ID)
			continue
		}
		if err := RenderSeg(p, seg.ID); err != nil {
			return err
		}
	}
	entries, _ := os.ReadDir(segsDir)
	for _, e := range entries { // 清理清单外残留段片（段增删后旧文件不进 concat，也不留垃圾）
		base := strings.TrimSuffix(e.Name(), ".mp4")
		if strings.HasSuffix(e.Name(), ".mp4") && !live[base] {
			_ = os.Remove(filepath.Join(segsDir, e.Name()))
			_ = os.Remove(filepath.Join(segsDir, e.Name()+".sum"))
		}
		if strings.HasSuffix(e.Name(), ".mp4.sum") && !live[strings.TrimSuffix(e.Name(), ".mp4.sum")] {
			_ = os.Remove(filepath.Join(segsDir, e.Name()))
		}
	}
	if err := concatSegs(p, sb, segsDir); err != nil {
		return err
	}
	p.Manifest("render.done", "renders/main_voice.mp4")
	syncArtifacts(artifacts.ProjectKey(p.ID, "renders")) // 母版/timeline/list.txt/段片兜底补传
	return nil
}

// RenderSeg 渲单个段片（.hf-seg 单段工程 → renders/segs/segNN.mp4），落指纹旁车
// 并上正本。段级流水线与 stageRender 共用：谁渲的都走同一指纹口径，后续
// stageRender/concat 才能正确判"新鲜"跳过。要求该段的段工程已在（assemble 产物）。
func RenderSeg(p *Project, segID string) error {
	fmt.Printf("  · %s 渲染中\n", segID)
	outLog, err := runCLI(p, "npx", "hyperframes@0.8.55", "render", ".",
		"-c", ".hf-seg/"+segID+".html", "-o", "renders/segs/"+segID+".mp4")
	if err != nil {
		return fmt.Errorf("%s 渲染失败:\n%s", segID, tailLines(outLog, 30))
	}
	out := p.Artifact("renders/segs/" + segID + ".mp4")
	if sum, err := segSrcSum(p, segID); err == nil {
		_ = os.WriteFile(out+".sum", []byte(sum), 0o644)
	}
	p.Manifest("render.seg", segID)
	// 段片是最贵的产物（分钟级渲染）：渲完立即上正本，rework/清理后免重渲
	putArtifact(artifacts.ProjectKey(p.ID, "renders/segs/"+segID+".mp4"), out)
	putArtifact(artifacts.ProjectKey(p.ID, "renders/segs/"+segID+".mp4.sum"), out+".sum")
	return nil
}

// SegVideoDuration 段片实测时长（ffprobe；缺失/探不出返回 0）。帧数核对与终审
// 抽帧的时刻轴都以此为准——与 concat 同源（writeActualTimeline 同一探测函数）。
func SegVideoDuration(p *Project, segID string) float64 {
	return probeDuration(p.Artifact("renders/segs/" + segID + ".mp4"))
}

// SegVideoFrames 段片实际视频帧数（快速：-count_packets 只拆包不解码）。
// 段级流水线的 rendered 位用它与「时长×帧率」核对段片完整性。
func SegVideoFrames(p *Project, segID string) (int, float64, error) {
	out, err := ffprobeStream(p.Artifact("renders/segs/"+segID+".mp4"),
		"-select_streams", "v:0", "-count_packets",
		"-show_entries", "stream=nb_read_packets,r_frame_rate")
	if err != nil {
		return 0, 0, err
	}
	var frames int
	var fps float64
	for _, kv := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(kv), "=")
		if !ok {
			continue
		}
		switch k {
		case "nb_read_packets":
			frames, _ = strconv.Atoi(v)
		case "r_frame_rate":
			if num, den, ok2 := strings.Cut(v, "/"); ok2 {
				var n, d int
				n, _ = strconv.Atoi(num)
				d, _ = strconv.Atoi(den)
				if d > 0 {
					fps = float64(n) / float64(d)
				}
			}
		}
	}
	if frames <= 0 || fps <= 0 {
		return 0, 0, fmt.Errorf("段片 %s 帧数/帧率探测失败（frames=%d fps=%.2f）", segID, frames, fps)
	}
	return frames, fps, nil
}

// segStale 段片是否需要重渲：缺失，或该段的 帧html/音频/单段工程 内容与上次渲染时
// 不一致。按内容哈希（sidecar .sum）判旧而不是 mtime：修复轮回滚会把全部段源原样
// 重写一遍，mtime 全部刷新，按时间判会把段级缓存打穿成 20 分钟的全片重渲。
func segStale(p *Project, segID, out string) bool {
	if _, err := os.Stat(out); err != nil {
		return true
	}
	side := out + ".sum"
	want, err := os.ReadFile(side)
	if err != nil {
		return true // 没有指纹（老项目首次升级/上次渲染中断）→ 重渲一次补上
	}
	got, err := segSrcSum(p, segID)
	if err != nil {
		return true
	}
	return string(want) != got
}

// segSrcSum 段渲染输入的内容指纹：帧html + 音频 + 单段工程逐个 sha256 拼接。
// 指纹只吃内容与文件基名（+槽位序号）——绝对路径掺进去的话项目目录一挪/
// 换机器，全部段片判 stale 触发全片重渲。
func segSrcSum(p *Project, segID string) (string, error) {
	h := sha256.New()
	for i, src := range []string{
		p.Artifact("compositions/frames/" + segID + ".html"),
		p.Artifact("audio/" + segID + ".wav"),
		p.Artifact(".hf-seg/" + segID + ".html"),
	} {
		b, err := os.ReadFile(src)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%d\x00%s\x00%d\x00", i, filepath.Base(src), len(b))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// concatSegs 全部段片 concat -c copy 拼成人声母版 main_voice.mp4（同源输出参数
// 一致，免重编码）。BGM 在 stitch 阶段从母版混入 main.mp4——换配乐不用重渲画面。
// 转场 xfade（v1b）时改走交叉叠化链（见 xfadeConcat）。
func concatSegs(p *Project, sb *contract.Storyboard, segsDir string) error {
	if transitionFor(p) == "xfade" {
		if err := xfadeConcat(p, sb, segsDir); err == nil {
			return nil
		} else if !errors.Is(err, errXfadeShort) {
			return err // 命令级失败是错误（时间轴语义变了，不能静默换路）
		}
		// 段过短放不下叠化窗口：回退硬拼 + 清掉可能存在的旧 timeline（口径必须一致）
		_ = os.Remove(p.Artifact("renders/timeline.json"))
		p.Manifest("render.xfade_fallback", "段过短，回退 concat")
	}
	list := filepath.Join(segsDir, "list.txt")
	var b strings.Builder
	for _, seg := range sb.Segments {
		b.WriteString("file '" + seg.ID + ".mp4'\n")
	}
	if err := os.WriteFile(list, []byte(b.String()), 0o644); err != nil {
		return err
	}
	if _, err := runFFmpeg(p, segsDir, "段片拼接", "-y", "-v", "error",
		"-f", "concat", "-safe", "0", "-i", "list.txt", "-c", "copy", "../main_voice.mp4"); err != nil {
		return err
	}
	// concat 成片也写 timeline.json：段片时长按帧量化（30fps 每段 ±1 帧），
	// 裸「配音+0.35」累计与成片实际边界逐段漂移（长片尾段累计可达 0.2s+），
	// 前端时间轴按 timeline 映射段内时刻——必须与成片同源（实测段片时长）。
	if err := writeActualTimeline(p, sb, segsDir); err != nil {
		return err
	}
	return nil
}

// writeActualTimeline 按段片实测时长（ffprobe）累计写 renders/timeline.json。
// 任一段片探不出时长 → 报错（边界错位会直接污染前端时间轴映射，不能静默跳过）。
func writeActualTimeline(p *Project, sb *contract.Storyboard, segsDir string) error {
	tl := make([]TimelineRow, len(sb.Segments))
	at := 0.0
	for i, seg := range sb.Segments {
		d := probeDuration(filepath.Join(segsDir, seg.ID+".mp4"))
		if d <= 0 {
			return fmt.Errorf("timeline: 段片 %s 时长探测失败", seg.ID)
		}
		tl[i] = TimelineRow{ID: seg.ID, Start: at, End: at + d}
		at += d
	}
	b, err := json.MarshalIndent(tl, "", "  ")
	if err != nil {
		return err
	}
	if werr := WriteFileAtomic(p.Artifact("renders/timeline.json"), b, 0o644); werr != nil {
		return werr
	}
	p.Manifest("render.timeline", fmt.Sprintf("%d 段实测边界，总长 %.2fs", len(tl), at))
	return nil
}

// ── 转场 v1b：段间交叉叠化 ────────────────────────────────────────

// xfadeDur 叠化窗口（秒）。0.4 与 v1 黑场微叠化(0.25×2)观感同量级，但内容连续。
const xfadeDur = 0.4

var errXfadeShort = errors.New("xfade: 段过短")

// transitionFor 转场模式：project.json "transition" > "fade"。
// cut=页内硬切；fade=v1 页内黑场微叠化；xfade=v1b concat 侧真交叉叠化。
// v3 库条目不携带 tokens，读库配置的旧路径已随 schema 退役。
func transitionFor(p *Project) string {
	if b, err := os.ReadFile(p.Artifact("project.json")); err == nil {
		var m map[string]any
		if json.Unmarshal(b, &m) == nil {
			if v, _ := m["transition"].(string); v != "" {
				return strings.ToLower(strings.TrimSpace(v))
			}
		}
	}
	// 缺省 cut：段间转场是设计决策（LockDesign 从 motion.transition 映射写入），
	// 没写 = 直切——段间节奏由场景内的退场/入场编排接管，平台不默认黑场翻页。
	return "cut"
}

// TimelineRow xfade 成片的实际段边界（sfx 布点/前端时间轴对齐的权威口径）。
type TimelineRow struct {
	ID    string  `json:"id"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// LoadTimeline 读 renders/timeline.json（xfade/concat 拼接都写，段片实测边界）。
func LoadTimeline(p *Project) []TimelineRow {
	b, err := os.ReadFile(p.Artifact("renders/timeline.json"))
	if err != nil {
		return nil
	}
	var tl []TimelineRow
	if json.Unmarshal(b, &tl) != nil {
		return nil
	}
	return tl
}

// buildXfadeGraph 纯函数：各段时长 → xfade/acrossfade 链 + 各段实际起点。
// 第 i 个边界的 offset = Σ_{k<i}d_k − D·i（前 i 次收缩都已发生）。
func buildXfadeGraph(durs []float64) (vc, ac string, starts []float64, err error) {
	n := len(durs)
	if n < 2 {
		return "", "", nil, errXfadeShort
	}
	for _, d := range durs {
		if d < xfadeDur+0.05 {
			return "", "", nil, errXfadeShort
		}
	}
	var vparts, aparts []string
	prevV, prevA := "[0:v]", "[0:a]"
	cum := durs[0]
	starts = append(starts, 0)
	for i := 1; i < n; i++ {
		outV, outA := fmt.Sprintf("[xv%d]", i), fmt.Sprintf("[xa%d]", i)
		if i == n-1 {
			outV, outA = "[vout]", "[aout]"
		}
		vparts = append(vparts, fmt.Sprintf("%s[%d:v]xfade=transition=fade:duration=%.2f:offset=%.3f%s",
			prevV, i, xfadeDur, cum-xfadeDur*float64(i), outV))
		aparts = append(aparts, fmt.Sprintf("%s[%d:a]acrossfade=d=%.2f%s", prevA, i, xfadeDur, outA))
		prevV, prevA = outV, outA
		starts = append(starts, cum-xfadeDur*float64(i))
		cum += durs[i]
	}
	return strings.Join(vparts, ";"), strings.Join(aparts, ";"), starts, nil
}

// xfadeConcat 段片链式交叉叠化出 main_voice.mp4（真叠化必须整片重编码一次，
// 与后期链路同价）。总时长每边界缩 xfadeDur，实际段起点写 renders/timeline.json。
func xfadeConcat(p *Project, sb *contract.Storyboard, segsDir string) error {
	if !hasFFmpegFilter("xfade") || !hasFFmpegFilter("acrossfade") {
		return errXfadeShort // 滤镜缺失：走 concat 老路（与段过短同一回退口径）
	}
	ids := make([]string, len(sb.Segments))
	durs := make([]float64, len(sb.Segments))
	for i, seg := range sb.Segments {
		ids[i] = seg.ID
		durs[i] = probeDuration(filepath.Join(segsDir, seg.ID+".mp4"))
	}
	vc, ac, starts, err := buildXfadeGraph(durs)
	if err != nil {
		return err
	}
	args := []string{"-y", "-v", "error"}
	for _, id := range ids {
		args = append(args, "-i", id+".mp4")
	}
	args = append(args,
		"-filter_complex", vc+";"+ac,
		"-map", "[vout]", "-map", "[aout]",
		"-c:v", "libx264", "-crf", "18", "-preset", "medium", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "192k", "../main_voice.mp4")
	if _, err := runFFmpeg(p, segsDir, "交叉叠化拼接", args...); err != nil {
		return err
	}
	tl := make([]TimelineRow, len(ids))
	for i, id := range ids {
		end := starts[i] + durs[i]
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		tl[i] = TimelineRow{ID: id, Start: starts[i], End: end}
	}
	if b, err := json.MarshalIndent(tl, "", "  "); err == nil {
		// timeline 是 sfx 落点与前端字幕轨的权威边界，写失败必须留痕（否则按裸累计错位）
		if werr := WriteFileAtomic(p.Artifact("renders/timeline.json"), b, 0o644); werr != nil {
			p.Manifest("render.xfade.timeline_error", werr.Error())
		}
	}
	p.Manifest("render.xfade", fmt.Sprintf("%d 段交叉叠化（每边界 %.2fs）", len(ids), xfadeDur))
	return nil
}

// ffmpegPath PATH 优先，回退 extraPATH（与 runCLI 的 Env 注入同一来源）。
func ffmpegPath() (string, error) {
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p, nil
	}
	if extraPATH != "" {
		for _, cand := range []string{"ffmpeg.exe", "ffmpeg"} {
			p := filepath.Join(extraPATH, cand)
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				return p, nil
			}
		}
	}
	return "", errors.New("ffmpeg 不在 PATH（ensureFFmpeg 未生效？）")
}

// FFmpegPath 供外部包（produce 抽帧审查）定位 ffmpeg。
func FFmpegPath() (string, error) { return ffmpegPath() }

// cmdEnv runCLI 与 ffmpeg 共用的进程环境（extraPATH 注入）。
func cmdEnv() []string {
	env := os.Environ()
	if rootDir != "" {
		// 用户 ~/.npmrc 的 cache=./npm-cache 是相对路径，会随子进程 CWD（项目目录）
		// 漂移成每项目一份空缓存，--offline 必 ENOTCACHED。钉死到仓库级共享缓存：
		// 绝对路径、env 优先级高于 userconfig，且与手工预热的位置一致。
		env = append(env, "npm_config_cache="+filepath.Join(rootDir, "npm-cache"))
	}
	if extraPATH == "" {
		return env
	}
	return append(env, "PATH="+extraPATH+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// stageStitch 成片终装：人声母版 main_voice.mp4 → 统一后期（P0-1：暗角/颗粒/色差，
// 参数见 postFor）+ 按项目配乐（project.json bgm 字段）混入 BGM（sidechain 闪避）
// + 转场 sfx 垫（project.json transition_sfx / 风格 token，段边界气流/章节上扬）
// 写 renders/main.mp4；三者皆无则直接转正（-c copy 老路径）。
// 母版始终保留，换 BGM / 调后期只需重跑本阶段（StitchNow），不动画面渲染。
func stageStitch(p *Project) error {
	voice := p.Artifact("renders/main_voice.mp4")
	if !exists(voice) {
		return fmt.Errorf("无 renders/main_voice.mp4（render 阶段未完成？）")
	}
	out := p.Artifact("renders/main.mp4")
	post := postFor(p)
	name, track := bgmFor(p)
	stem := sfxStem(p)
	if err := stitchFinal(p, voice, track, stem, post, out); err != nil {
		return err
	}
	detail := "renders/main.mp4"
	if !post.off() {
		detail += fmt.Sprintf("+post(v=%.2f,g=%.2f,c=%.2f)", post.Vignette, post.Grain, post.Chroma)
	}
	if track != "" {
		detail += "+bgm=" + name
	}
	if stem != "" {
		detail += "+sfx"
	}
	p.Manifest("film.ready", detail)
	putArtifact(artifacts.ProjectKey(p.ID, "renders/main.mp4"), out)
	return nil
}

// StitchNow 只跑拼接/混音/后期（成片就绪后换 BGM 或调后期用，画面不动）。
// 清理过的老项目先回源母版/段片再拼。
func StitchNow(p *Project) error {
	EnsureWorkspace(p)
	return stageStitch(p)
}

// ── BGM 混音 ──────────────────────────────────────────────────────

// bgmTrack 曲库清单（data/library/media/audio/tracks.json）。
type bgmTrack struct {
	Name     string  `json:"name"`
	File     string  `json:"file"`
	Duration float64 `json:"duration"`
	Mood     string  `json:"mood"`
}

func musicDir() string { return filepath.Join(dataDir, "library", "media", "audio") }

func loadTracks() []bgmTrack {
	if dataDir == "" {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(musicDir(), "tracks.json"))
	if err != nil {
		return nil
	}
	var ts []bgmTrack
	if json.Unmarshal(b, &ts) != nil {
		return nil
	}
	return ts
}

// bgmFor 项目配乐解析：project.json "bgm" 字段 → 曲库文件路径（空 = 无配乐）。
func bgmFor(p *Project) (name, file string) {
	b, err := os.ReadFile(p.Artifact("project.json"))
	if err != nil {
		return "", ""
	}
	var meta struct {
		BGM string `json:"bgm"`
	}
	if json.Unmarshal(b, &meta) != nil {
		return "", ""
	}
	want := strings.TrimSpace(meta.BGM)
	if want == "" || strings.EqualFold(want, "off") {
		return "", ""
	}
	for _, tr := range loadTracks() {
		if strings.EqualFold(tr.Name, want) {
			f := filepath.Join(musicDir(), tr.File)
			if exists(f) {
				return tr.Name, f
			}
			return "", ""
		}
	}
	return "", ""
}

// mixBGM 人声母版 + 循环 BGM（-stream_loop 无限铺底）→ sidechain 闪避
// （音乐听人声的话自动压低）→ amix（不归一化，人声原样；sfx 垫同入终混）。
// 视频流直拷。母版永不改动，产物写临时文件后原子转正——重复执行无二次混音风险。
func mixBGM(p *Project, voice, track, sfx, out string) error {
	fade := bgmFade(voice)
	mixIn, nIn := bgmMixIn(sfx)
	fc := bgmFcTail(fade, mixIn, nIn)
	args := []string{"-y", "-v", "error", "-i", voice,
		"-stream_loop", "-1", "-i", track}
	if sfx != "" {
		args = append(args, "-i", sfx)
	}
	args = append(args,
		"-filter_complex", fc,
		"-map", "0:v", "-map", "[aout]",
		"-c:v", "copy", "-c:a", "aac", "-b:a", "192k")
	tmp, err := tmpSuffixName(out, ".mix-bgm", ".tmp.mp4") // tmp 名必须 .mp4 结尾：ffmpeg 按扩展名推断封装
	if err != nil {
		return err
	}
	args = append(args, tmp)
	return runFFmpegRename(p, args, tmp, out, "混音")
}

// bgmFade BGM 结尾淡出：人声比 3.5s 长时末尾 1.5s 收（mixBGM 与终装同源）。
func bgmFade(voice string) string {
	if dur := probeDuration(voice); dur > 3.5 {
		return fmt.Sprintf(",afade=t=out:st=%.2f:d=1.5", dur-1.7)
	}
	return ""
}

// bgmMixIn sfx 垫在 amix 里的第三路引用。输入序约定：0=人声、1=BGM、2=sfx——
// 写成 [3:a] 会因流不存在被 ffmpeg 拒图，终装失败。
func bgmMixIn(sfx string) (string, int) {
	if sfx != "" {
		return "[2:a]", 3
	}
	return "", 2
}

// bgmFcTail BGM 闪避混音的音频滤镜段（终装 stitchComboFc = 视频链 + 本段）。
func bgmFcTail(fade, mixIn string, nIn int) string {
	return fmt.Sprintf("[1:a]volume=0.34%s[m];"+
		"[m][0:a]sidechaincompress=threshold=0.035:ratio=7:attack=120:release=1100[duck];"+
		"[0:a][duck]%samix=inputs=%d:duration=first:dropout_transition=0:normalize=0[aout]",
		fade, mixIn, nIn)
}

// mixSfx 无 BGM 无后期、仅 sfx 垫时的最小混音（视频直拷，音频重编一次）。
func mixSfx(p *Project, voice, sfx, out string) error {
	fc := "[1:a][0:a]amix=inputs=2:duration=first:dropout_transition=0:normalize=0[aout]"
	tmp, err := tmpSuffixName(out, ".mix-sfx", ".tmp.mp4")
	if err != nil {
		return err
	}
	args := []string{"-y", "-v", "error", "-i", voice, "-i", sfx,
		"-filter_complex", fc,
		"-map", "0:v", "-map", "[aout]",
		"-c:v", "copy", "-c:a", "aac", "-b:a", "192k", tmp}
	return runFFmpegRename(p, args, tmp, out, "sfx 混音")
}

// runFFmpeg 跑 ffmpeg 并捕获输出（接 p.Ctx：取消/停机时连树击杀）。非零退出时
// 返回带尾部日志的错误；what 用于错误消息前缀（拼接/交叉叠化/混音…）。
func runFFmpeg(p *Project, dir, what string, args ...string) (string, error) {
	ff, err := ffmpegPath()
	if err != nil {
		return "", err
	}
	cmd := ctxCommand(pctx(p), ff, args...)
	cmd.Dir = dir
	cmd.Env = cmdEnv()
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		if ctxErr := pctx(p).Err(); ctxErr != nil {
			return buf.String(), fmt.Errorf("ffmpeg %s被取消: %w", what, ctxErr)
		}
		return buf.String(), fmt.Errorf("ffmpeg %s退出: %w\n%s", what, err, tailLines(buf.String(), 20))
	}
	return buf.String(), nil
}

// renameOver 临时产物转正：Windows 的 os.Rename 底层 MoveFileEx 自带覆盖语义，
// 直接 rename 即可；先 Remove 再 rename 会制造「旧产物已删、新产物未就位」的缺失
// 窗口（并发读者撞空），中途被杀则产物彻底丢失。个别文件系统不支持覆盖时退回
// Remove+重试一次——此时先删后建已是该平台能做到的最好语义。
func renameOver(tmp, out string) error {
	if err := os.Rename(tmp, out); err == nil {
		return nil
	}
	_ = os.Remove(out)
	return os.Rename(tmp, out)
}

// runFFmpegRename 跑 ffmpeg，成功后临时文件原子转正（混音/终装共用）。
func runFFmpegRename(p *Project, args []string, tmp, out, what string) error {
	if _, err := runFFmpeg(p, p.Dir, what, args...); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return renameOver(tmp, out)
}

// ── 转场 sfx（M-C：段边界气流声 / 章节上扬）──────────────────────

// sfxClip sfx 清单条目（data/library/media/audio/sfx/sfx.json；ai/make_sfx.py 产物）。
type sfxClip struct {
	Name     string  `json:"name"`
	File     string  `json:"file"`
	Duration float64 `json:"duration"`
}

func sfxDir() string { return filepath.Join(dataDir, "library", "media", "audio", "sfx") }

// loadSfx 清单里找一枚素材（清单/文件任一缺失 = 不可用，调用方静默降级）。
func loadSfx(name string) (sfxClip, string, bool) {
	if dataDir == "" {
		return sfxClip{}, "", false
	}
	b, err := os.ReadFile(filepath.Join(sfxDir(), "sfx.json"))
	if err != nil {
		return sfxClip{}, "", false
	}
	var cs []sfxClip
	if json.Unmarshal(b, &cs) != nil {
		return sfxClip{}, "", false
	}
	for _, c := range cs {
		if c.Name == name && c.Duration > 0 {
			f := filepath.Join(sfxDir(), c.File)
			if exists(f) {
				return c, f, true
			}
		}
	}
	return sfxClip{}, "", false
}

// sfx 混入音量：垫在成片里只做段间"空气感"缝合，常驻人声之下。
const (
	sfxWhooshVol = 0.32
	sfxRiserVol  = 0.38
)

// sfxFor 转场音效开关：project.json "transition_sfx"（true/false）> 默认关。
// v3 库条目不携带 tokens，读库配置的旧路径已随 schema 退役。
func sfxFor(p *Project) bool {
	if b, err := os.ReadFile(p.Artifact("project.json")); err == nil {
		var meta struct {
			TransitionSFX *bool `json:"transition_sfx"`
		}
		if json.Unmarshal(b, &meta) == nil && meta.TransitionSFX != nil {
			return *meta.TransitionSFX
		}
	}
	return false
}

// SetTransitionSFX 写 project.json 的 transition_sfx 字段（agent 工具 set_sfx 落点）。
// on/off 都显式落值：风格默认开的片子也能被项目级 false 压掉（与 post:false 同语义）。
// TransitionSFXOn 供 agent 查询当前状态（含风格默认）。
func SetTransitionSFX(p *Project, on bool) error {
	return SetProjectField(p, "transition_sfx", on)
}

// TransitionSFXOn agent 查询转场音效当前状态（含风格默认）。
func TransitionSFXOn(p *Project) bool { return sfxFor(p) }

// segBoundary 整片里的一条段边界：位置（秒）+ 是否章节切换。
type segBoundary struct {
	at          float64
	chapterTurn bool
}

// segBoundaries 按段片实际时长（ffprobe，与 concat 同源）累计出整片边界。
// 章节切换（相邻段 Chapter 不同）标 riser 位；无章节信息全 whoosh。
// 任一段片缺失/探不出 → nil（sfx 静默跳过，不挡终装）。
func segBoundaries(p *Project) []segBoundary {
	sb, err := p.LoadStoryboard()
	if err != nil || len(sb.Segments) < 2 {
		return nil
	}
	// xfade 成片：实际边界以 renders/timeline.json 为准（每边界比裸累计早 xfadeDur·n）
	if tl := LoadTimeline(p); len(tl) == len(sb.Segments) {
		var out []segBoundary
		for i := 0; i+1 < len(sb.Segments); i++ {
			out = append(out, segBoundary{at: tl[i].End, chapterTurn: sb.Segments[i].Chapter != sb.Segments[i+1].Chapter})
		}
		return out
	}
	var out []segBoundary
	at := 0.0
	for i := 0; i+1 < len(sb.Segments); i++ {
		d := probeDuration(p.Artifact("renders/segs/" + sb.Segments[i].ID + ".mp4"))
		if d <= 0 {
			return nil
		}
		at += d
		out = append(out, segBoundary{at: at, chapterTurn: sb.Segments[i].Chapter != sb.Segments[i+1].Chapter})
	}
	return out
}

// sfxPlacements 边界 → 两枚素材的落点（秒）：whoosh 居中于边界（提前半长），
// riser 收在边界上（提前全长）；<0（片头贴边）丢弃。
func sfxPlacements(bs []segBoundary, whooshDur, riserDur float64) (wAt, rAt []float64) {
	for _, b := range bs {
		if b.chapterTurn {
			if t := b.at - riserDur; t >= 0 {
				rAt = append(rAt, t)
			}
		} else if t := b.at - whooshDur/2; t >= 0 {
			wAt = append(wAt, t)
		}
	}
	return wAt, rAt
}

// sfxStemFilter 音效垫的滤镜图：asplit 复制素材到各落点（adelay 毫秒，双通道）。
// 输入约定 [0]=anullsrc 静音垫底、[1]=whoosh（有落点才进图）、[2]=riser。
func sfxStemFilter(wAt, rAt []float64) string {
	var chains []string
	mix := []string{"[0:a]"}
	delay := func(at float64, vol float64) string {
		ms := int(math.Round(at * 1000))
		if ms <= 0 {
			return fmt.Sprintf("volume=%.2f", vol)
		}
		return fmt.Sprintf("volume=%.2f,adelay=%d|%d", vol, ms, ms)
	}
	if len(wAt) > 0 {
		outs := make([]string, len(wAt))
		for i := range wAt {
			outs[i] = fmt.Sprintf("[w%d]", i)
		}
		chains = append(chains, fmt.Sprintf("[1:a]asplit=%d%s", len(wAt), strings.Join(outs, "")))
		for i, at := range wAt {
			chains = append(chains, fmt.Sprintf("[w%d]%s[w%dd]", i, delay(at, sfxWhooshVol), i))
			mix = append(mix, fmt.Sprintf("[w%dd]", i))
		}
	}
	if len(rAt) > 0 {
		in := 1
		if len(wAt) > 0 {
			in = 2
		}
		outs := make([]string, len(rAt))
		for i := range rAt {
			outs[i] = fmt.Sprintf("[r%d]", i)
		}
		chains = append(chains, fmt.Sprintf("[%d:a]asplit=%d%s", in, len(rAt), strings.Join(outs, "")))
		for i, at := range rAt {
			chains = append(chains, fmt.Sprintf("[r%d]%s[r%dd]", i, delay(at, sfxRiserVol), i))
			mix = append(mix, fmt.Sprintf("[r%dd]", i))
		}
	}
	return strings.Join(chains, ";") + ";" + strings.Join(mix, "") +
		fmt.Sprintf("amix=inputs=%d:duration=first:dropout_transition=0:normalize=0[s]", len(mix))
}

// sfxStem 开关开 + 素材在 + 边界可探 → 铺出与成片等长的音效垫 renders/sfx_stem.wav
// 并返回路径；任何一环不满足返回 ""（stitch 走无 sfx 老路——sfx 是妆不是闸）。
func sfxStem(p *Project) string {
	if !sfxFor(p) {
		return ""
	}
	bs := segBoundaries(p)
	if len(bs) == 0 {
		return ""
	}
	whoosh, wf, okW := loadSfx("whoosh")
	riser, rf, okR := loadSfx("riser")
	wAt, rAt := sfxPlacements(bs, whoosh.Duration, riser.Duration)
	if !okW {
		wAt = nil
	}
	if !okR {
		rAt = nil
	}
	if len(wAt) == 0 && len(rAt) == 0 {
		return ""
	}
	dur := probeDuration(p.Artifact("renders/main_voice.mp4"))
	if dur <= 0 {
		return ""
	}
	out := p.Artifact("renders/sfx_stem.wav")
	args := []string{"-y", "-v", "error",
		"-f", "lavfi", "-t", fmt.Sprintf("%.2f", dur+0.5), "-i", "anullsrc=r=44100:cl=stereo"}
	if len(wAt) > 0 {
		args = append(args, "-i", wf)
	}
	if len(rAt) > 0 {
		args = append(args, "-i", rf)
	}
	tmp, terr := tmpSuffixName(out, ".sfx-stem", ".tmp.wav")
	if terr != nil {
		return ""
	}
	args = append(args, "-filter_complex", sfxStemFilter(wAt, rAt),
		"-map", "[s]", "-c:a", "pcm_s16le", tmp)
	if _, err := runFFmpeg(p, p.Dir, "sfx 垫", args...); err != nil {
		fmt.Printf("  · sfx 垫生成失败（跳过音效）: %v\n", err)
		_ = os.Remove(tmp)
		return ""
	}
	if err := renameOver(tmp, out); err != nil {
		_ = os.Remove(tmp) // 转正失败别把 tmp 留在 renders/ 里当垃圾
		return ""
	}
	return out
}

// ── 统一后期（P0-1：暗角/颗粒/色差）────────────────────────────────

// postFX 全片统一后期参数（stitch 阶段滤镜，0–1；全 0 = 关闭）。
// 来源唯一：project.json "post"（LockDesign 从设计系统 post 槽位写入；
// 设计没写 post = false = 不加后期）。后期质感是设计决策，平台不再默认施加。
type postFX struct {
	Vignette float64 `json:"vignette"` // 暗角
	Grain    float64 `json:"grain"`    // 胶片颗粒
	Chroma   float64 `json:"chroma"`   // 轻微色差
}

func (fx *postFX) off() bool {
	return fx == nil || (fx.Vignette <= 0 && fx.Grain <= 0 && fx.Chroma <= 0)
}

// postFor 后期参数解析（stitch 每次执行时读盘——换风格/改配置无需重启）。
// project.json "post"：false/null/缺省 = 不加后期；对象 = 按 0–1 施加。
// 写入方是 LockDesign（设计系统 post 槽位）；没有设计 post 的项目干净出片。
func postFor(p *Project) *postFX {
	if b, err := os.ReadFile(p.Artifact("project.json")); err == nil {
		var meta struct {
			Post json.RawMessage `json:"post"`
		}
		if json.Unmarshal(b, &meta) == nil && len(meta.Post) > 0 {
			s := strings.TrimSpace(string(meta.Post))
			if s == "false" || s == "null" {
				return nil
			}
			var fx postFX
			if json.Unmarshal(meta.Post, &fx) == nil {
				return &fx // 对象=整组替换（未写字段视为关该项）
			}
		}
	}
	return nil
}

// videoChain 后期视频滤镜链（空 = 无滤镜）。顺序：色差 → 暗角 → 颗粒
// （镜头色散在最前、感光颗粒在最后，贴近物理成像次序）。
func (fx *postFX) videoChain() string {
	if fx.off() {
		return ""
	}
	var parts []string
	if fx.Chroma > 0 && hasFFmpegFilter("rgbashift") {
		r := 1 + int(math.Round(math.Min(fx.Chroma, 1))) // 1–2px，再大就是故障风了
		parts = append(parts, fmt.Sprintf("rgbashift=rh=-%d:bh=%d", r, r))
	}
	if fx.Vignette > 0 && hasFFmpegFilter("vignette") {
		s := math.Min(math.Max(fx.Vignette, 0), 0.92) // 钳制：angle ≤ PI/3.2，防黑边
		parts = append(parts, fmt.Sprintf("vignette=angle=%.4f", math.Pi/(5.5-2.5*s)))
	}
	if fx.Grain > 0 && hasFFmpegFilter("noise") {
		n := 2 + int(math.Round(10*math.Min(fx.Grain, 1))) // 3–12，轻颗粒区间
		parts = append(parts, fmt.Sprintf("noise=alls=%d:allf=t", n))
	}
	return strings.Join(parts, ",")
}

// ffmpeg 滤镜能力探测（成功才缓存）：某个滤镜本机构建没带时跳过该项，
// 后期缺一块只是少一层妆，不能让整片终装失败。
var (
	filtersMu     sync.Mutex
	filtersAvail  map[string]bool
	filtersProbed bool
)

func hasFFmpegFilter(name string) bool {
	filtersMu.Lock()
	defer filtersMu.Unlock()
	// 探测失败不缓存（sync.Once 版会把首次失败的空结果永久化，此后所有滤镜判断失真）；
	// 下次调用重探，直到真的跑通一次 -filters。
	if !filtersProbed {
		avail := map[string]bool{}
		ff, err := ffmpegPath()
		if err == nil {
			if out, err := exec.Command(ff, "-hide_banner", "-filters").Output(); err == nil {
				for _, line := range strings.Split(string(out), "\n") {
					if f := strings.Fields(line); len(f) >= 2 {
						avail[f[1]] = true
					}
				}
				filtersAvail = avail
				filtersProbed = true
			}
		}
	}
	if !filtersProbed { // ffmpeg 还探不了（stitch 真跑时也会失败）→ 放行
		return true
	}
	return filtersAvail[name]
}

// stitchFinal 终装：后期 + BGM 混音 + 转场 sfx 一条命令图完成。有后期时视频重编码
// 一次（libx264 crf 18，视觉近无损；母版不动，重跑零成本）；无后期无 BGM 无 sfx 走
// 老路径（-c:v copy）保证现有项目行为不变。产物写临时文件后原子转正。
func stitchFinal(p *Project, voice, track, sfx string, post *postFX, out string) error {
	vc := ""
	if !post.off() {
		vc = post.videoChain()
	}
	if vc == "" { // 无后期，或参数给了但滤镜全不可用 → 老路径（-c:v copy），行为不变
		if track != "" {
			return mixBGM(p, voice, track, sfx, out)
		}
		if sfx != "" {
			return mixSfx(p, voice, sfx, out)
		}
		return copyFile(voice, out)
	}
	// crf 18 视觉近无损，但胶片颗粒（noise 每帧新随机数，不可压缩）会把码率顶到
	// 70+ Mbps；VBV 封顶让交付体积可控（1080x1920 交付 10M 足够透明）。
	vcodec := []string{"-c:v", "libx264", "-crf", "18", "-preset", "medium",
		"-maxrate", "10M", "-bufsize", "20M", "-pix_fmt", "yuv420p"}
	tmp, err := tmpSuffixName(out, ".post", ".tmp.mp4") // tmp 名必须 .mp4 结尾：ffmpeg 按扩展名推断封装
	if err != nil {
		return err
	}
	args := []string{"-y", "-v", "error", "-i", voice}
	if track == "" {
		if sfx == "" {
			fc := fmt.Sprintf("[0:v]%s[vout]", vc)
			args = append(args,
				"-filter_complex", fc,
				"-map", "[vout]", "-map", "0:a?",
				"-c:a", "copy")
		} else {
			fc := fmt.Sprintf("[0:v]%s[vout];"+
				"[1:a][0:a]amix=inputs=2:duration=first:dropout_transition=0:normalize=0[aout]", vc)
			args = append(args,
				"-i", sfx,
				"-filter_complex", fc,
				"-map", "[vout]", "-map", "[aout]",
				"-c:a", "aac", "-b:a", "192k")
		}
	} else {
		mixIn, nIn := bgmMixIn(sfx)
		fc := stitchComboFc(vc, bgmFade(voice), mixIn, nIn)
		args = append(args, "-stream_loop", "-1", "-i", track)
		if sfx != "" {
			args = append(args, "-i", sfx)
		}
		args = append(args,
			"-filter_complex", fc,
			"-map", "[vout]", "-map", "[aout]",
			"-c:a", "aac", "-b:a", "192k")
	}
	args = append(args, vcodec...)
	args = append(args, tmp)
	return runFFmpegRename(p, args, tmp, out, "终装")
}

// stitchComboFc 后期+BGM(+sfx) 一图流的滤镜图（纯函数抽出，测试校验流引用）。
// 输入约定：0=人声母版、1=BGM、2=sfx 垫（sfx 非空时才有第 3 路输入）。
func stitchComboFc(vc string, fade string, mixIn string, nIn int) string {
	return fmt.Sprintf("[0:v]%s[vout];", vc) + bgmFcTail(fade, mixIn, nIn)
}

// probeDuration ffprobe 取媒体时长（秒；失败返回 0，调用方跳过淡出）。带缓存：
// 一次 stitch 里 bgmFade/sfxStem/writeActualTimeline 会对同一文件反复探测，ffprobe
// 每次都是一次进程拉起。key 带文件 mtime+size——文件被重渲/重混后自动失效。
var (
	probeMu    sync.Mutex
	probeCache = map[string]float64{}
)

func probeDuration(path string) float64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	key := fmt.Sprintf("%s|%d|%d", path, st.ModTime().UnixNano(), st.Size())
	probeMu.Lock()
	d, ok := probeCache[key]
	probeMu.Unlock()
	if ok {
		return d
	}
	d = probeDurationUncached(path)
	probeMu.Lock()
	if len(probeCache) > 256 { // 防多项目长跑积累陈旧键：超限整体重置，下次重探
		probeCache = map[string]float64{}
	}
	probeCache[key] = d
	probeMu.Unlock()
	return d
}

func probeDurationUncached(path string) float64 {
	if p, err := exec.LookPath("ffprobe"); err == nil {
		out, err := exec.Command(p, "-v", "error", "-show_entries", "format=duration",
			"-of", "csv=p=0", path).Output()
		if err == nil {
			if f, perr := strconv.ParseFloat(strings.TrimSpace(string(out)), 64); perr == nil {
				return f
			}
		}
	}
	if extraPATH != "" { // 回退 EnsureFFmpeg 注入的 bin 目录（winget 安装场景）；rootDir 与 ffprobe 定位无关
		for _, cand := range []string{"ffprobe.exe", "ffprobe"} {
			out, err := exec.Command(filepath.Join(extraPATH, cand), "-v", "error",
				"-show_entries", "format=duration", "-of", "csv=p=0", path).Output()
			if err == nil {
				if f, perr := strconv.ParseFloat(strings.TrimSpace(string(out)), 64); perr == nil {
					return f
				}
			}
		}
	}
	return 0
}

// ffprobeStream ffprobe 流级字段（key=value 输出原样返回）。PATH 优先，回退
// extraPATH（与 probeDurationUncached 同一定位口径）。
func ffprobeStream(path string, args ...string) (string, error) {
	full := append([]string{"-v", "error"}, append(append([]string{}, args...),
		"-of", "default=noprint_wrappers=1", path)...)
	if p, err := exec.LookPath("ffprobe"); err == nil {
		out, err := exec.Command(p, full...).Output()
		if err == nil {
			return string(out), nil
		}
	}
	if extraPATH != "" {
		for _, cand := range []string{"ffprobe.exe", "ffprobe"} {
			out, err := exec.Command(filepath.Join(extraPATH, cand), full...).Output()
			if err == nil {
				return string(out), nil
			}
		}
	}
	return "", errors.New("ffprobe 不在 PATH")
}

// copyFile 成片转正（母版保留副本，换 BGM 时可重新混音）。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := tmpSuffixName(dst, ".copy", ".tmp")
	if err != nil {
		return err
	}
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return renameOver(tmp, dst)
}

// ListBGM 曲库可读清单（agent 工具用）："calm——温暖平静…"。
func ListBGM() string {
	ts := loadTracks()
	if len(ts) == 0 {
		return ""
	}
	lines := make([]string, 0, len(ts))
	for _, tr := range ts {
		lines = append(lines, fmt.Sprintf("%s（%s）", tr.Name, tr.Mood))
	}
	return strings.Join(lines, "；")
}

// SetBGM 写 project.json 的 bgm 字段（track="off"/"" 清除；曲名做曲库校验）。
func SetBGM(p *Project, track string) error {
	want := strings.TrimSpace(track)
	if want != "" && !strings.EqualFold(want, "off") {
		ok := false
		for _, tr := range loadTracks() {
			if strings.EqualFold(tr.Name, want) {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("曲名 %q 不在曲库（可用：%s）", want, ListBGM())
		}
	}
	if want == "" || strings.EqualFold(want, "off") {
		return SetProjectField(p, "bgm", nil)
	}
	return SetProjectField(p, "bgm", strings.ToLower(want))
}

// SetBiSubtitle 双语字幕开关（M-C）：project.json bi_subtitle=true 开启。
// 翻译在制作流程 1.5 步（TTS 后）幂等补做，字幕渲染在段帧——开关后需整片重制。
func SetBiSubtitle(p *Project, on bool) error {
	return SetProjectField(p, "bi_subtitle", on)
}

// SetVoice 写 project.json 的 voice 字段（旁白音色，Edge TTS 音色 id；空串清除回落默认）。
// 音色变化时 tts_align.py 检测 audio_meta.json 的 voice 字段不符会全量重配音。
func SetVoice(p *Project, voice string) error {
	want := strings.TrimSpace(voice)
	if want == "" {
		return SetProjectField(p, "voice", nil)
	}
	return SetProjectField(p, "voice", want)
}

// extraPATH 额外 PATH（如 winget 安装的 FFmpeg bin），由宿主进程注入。
var extraPATH string

// rootDir 仓库根（SetRootDir 注入）：仅用于 ffmpeg PATH 回退。
var rootDir string

// dataDir 数据根（SetDataDir 注入）：素材库/BGM 曲库定位 data/library/media/audio/。
var dataDir string

// SetRootDir 注入仓库根（server/m0 启动时调用；不注入 = PATH 回退关闭）。
func SetRootDir(dir string) { rootDir = dir }

// SetDataDir 注入数据根（server/m0 启动时调用；不注入 = BGM 功能关闭）。
func SetDataDir(dir string) { dataDir = dir }

// artifactsStore 产物对象存储后端（SetArtifacts 注入）：项目产物/参考库的正本进
// MinIO，本地目录降级为工作区+缓存（docs/storage-cloud-migration.md）。
// nil = 未接线（m0/测试场景），全部上传与回源为无操作，行为与纯本地现状一致。
var artifactsStore artifacts.Store

// SetArtifacts 注入产物存储后端（server 启动时调用；m0 不注入 = 纯本地现状）。
func SetArtifacts(s artifacts.Store) { artifactsStore = s }

// putArtifact 检查点上传（best-effort）：失败只记日志不打断管线——本地仍是工作区，
// 缺口由后续检查点、终态全量补传或 -storage-sync 补齐；且清理器删本地前会先验证
// 远端正本，漏传最多暂时多占磁盘，绝不丢数据。
func putArtifact(key, srcPath string) {
	if artifactsStore == nil || !artifactsStore.Available() {
		return
	}
	if err := artifactsStore.Put(key, srcPath); err != nil {
		fmt.Printf("[artifacts] 上传 %s 失败: %v\n", key, err)
	}
}

// syncArtifacts 检查点补传某前缀（幂等：远端已 Stat 到的跳过）。
func syncArtifacts(prefix string) {
	if artifactsStore == nil || !artifactsStore.Available() {
		return
	}
	if _, err := artifacts.SyncUp(artifactsStore, dataDir, prefix); err != nil {
		fmt.Printf("[artifacts] 补传 %s 中断: %v\n", prefix, err)
	}
}

// EnsureWorkspace 老项目开跑前的透明回源：清理器删过本地大文件的项目，从对象存储
// 拉回 Keeper 文件（契约 JSON/段片/配音/设计/素材）。本地齐全的项目只有一次远端
// List 的代价（几十至几百 key，百毫秒级），新项目/未接线是 no-op。
// 可再生成的 Discard 类（渲染壳/QA 截帧）不回源——assemble/compositions 阶段会
// 从已回源的缓存契约（llm/comp-*.json）确定性重建。
func EnsureWorkspace(p *Project) {
	if artifactsStore == nil || !artifactsStore.Available() {
		return
	}
	prefix := artifacts.ProjectKey(p.ID, "")
	n, err := artifacts.Restore(artifactsStore, dataDir, prefix)
	if err != nil {
		fmt.Printf("[artifacts] %s 工作区回源中断（继续用本地现有文件）: %v\n", p.ID, err)
		return
	}
	if n > 0 {
		p.Manifest("workspace.restored", fmt.Sprintf("从对象存储回源 %d 个文件", n))
		fmt.Printf("[artifacts] %s 回源 %d 个文件\n", p.ID, n)
	}
}

// SetExtraPATH 注入额外可执行搜索路径（check/render 需要 ffmpeg）。
func SetExtraPATH(dir string) { extraPATH = dir }

// EnsureFFmpeg hyperframes check/render 与段片拼接需要 ffmpeg。
// PATH 里没有时：FFMPEG_BIN 指定的目录/可执行文件优先，否则扫 winget 安装目录
// （开发机场景；容器/服务器内 PATH 自带或设 FFMPEG_BIN）。
func EnsureFFmpeg() {
	if _, err := exec.LookPath("ffmpeg"); err == nil {
		return
	}
	if bin := os.Getenv("FFMPEG_BIN"); bin != "" {
		dir := bin
		if st, err := os.Stat(bin); err == nil && !st.IsDir() {
			dir = filepath.Dir(bin)
		}
		if ffmpegIn(dir) {
			os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			SetExtraPATH(dir)
			fmt.Printf("[ffmpeg] 注入 PATH: %s\n", dir)
			return
		}
		fmt.Printf("[ffmpeg] 警告：FFMPEG_BIN=%s 下没有 ffmpeg，回退 winget 扫描\n", bin)
	}
	// winget 用户级安装目录（按当前用户家目录拼，不再硬编码他人机器的绝对路径）
	if home, herr := os.UserHomeDir(); herr == nil {
		pkgs := filepath.Join(home, "AppData", "Local", "Microsoft", "WinGet", "Packages")
		matches, _ := filepath.Glob(filepath.Join(pkgs, "Gyan.FFmpeg*", "ffmpeg-*", "bin"))
		if len(matches) > 0 {
			os.Setenv("PATH", matches[0]+string(os.PathListSeparator)+os.Getenv("PATH"))
			SetExtraPATH(matches[0])
			fmt.Printf("[ffmpeg] 注入 PATH: %s\n", matches[0])
			return
		}
	}
	fmt.Println("[ffmpeg] 警告：PATH 中找不到 ffmpeg，check/render 将失败（装 FFmpeg 或设 FFMPEG_BIN 指向 bin 目录）")
}

func ffmpegIn(dir string) bool {
	for _, n := range []string{"ffmpeg", "ffmpeg.exe"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
			return true
		}
	}
	return false
}

// runCLI 在项目目录执行外部命令（hyperframes CLI 等），返回合并输出。
// npx 一律 --offline 优先：hyperframes 锁版本已在本地缓存，渲染管线不依赖 registry 网络。
// 缓存未命中（ENOTCACHED，如新机器）时去掉 --offline 联网补缓存重试一次——
// 拉过之后落进共享缓存，后续恢复纯离线。
func runCLI(p *Project, name string, args ...string) (string, error) {
	out, err := runCLIOne(p, name, append([]string{"--offline", "--yes"}, args...)...)
	if err != nil && strings.Contains(out, "ENOTCACHED") {
		fmt.Printf("[npm] 离线缓存未命中，联网补拉：%s %v\n", name, args)
		return runCLIOne(p, name, append([]string{"--yes"}, args...)...)
	}
	return out, err
}

func runCLIOne(p *Project, name string, full ...string) (string, error) {
	ctx := pctx(p)
	cmd := ctxCommand(ctx, name, full...)
	cmd.Dir = p.Dir
	cmd.Env = cmdEnv()
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	if err != nil && ctx.Err() != nil { // 被取消杀掉：报可识别的 ctx 错误而非 killed
		return buf.String(), ctx.Err()
	}
	return buf.String(), err
}

func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// TailLines 尾 n 行（错误摘要/命令输出回喂用）。produce/agent 与 pipeline 共用一份。
func TailLines(s string, n int) string { return tailLines(s, n) }

// FirstLine 首行截到 n 个 rune（超长加 …）。
func FirstLine(s string, n int) string {
	s = strings.SplitN(s, "\n", 2)[0]
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// TruncRunes TrimSpace 后截到 n 个 rune（超长加 …）。
func TruncRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

// RunCLI 供 produce 包复用（scene 确定性回放门禁调 hyperframes snapshot）。
func RunCLI(p *Project, name string, args ...string) (string, error) {
	return runCLI(p, name, args...)
}

func mustJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

// MustJSON 供 produce 包复用（双语字幕翻译的输入序列化）。
func MustJSON(v any) string { return mustJSON(v) }

func mustJSONBytes(v any) []byte {
	b, _ := json.MarshalIndent(v, "", "  ")
	return b
}

// MustJSONBytes 供 agent/api 复用（此前两包各持一份同义 mustJSON）。
func MustJSONBytes(v any) []byte { return mustJSONBytes(v) }

// SetProjectField 写 project.json 单字段（val=nil 删除该键），读-改-原子写。
// BGM/音色/字幕/sfx 开关与状态回写共用——此前同一样板在三个包手抄了七份。
// 持 per-project 锁串行化：api 侧手工读-改-写（applyStyle 等）也必须持同一把锁。
func SetProjectField(p *Project, key string, val any) error {
	mu := ProjectMu(p)
	mu.Lock()
	defer mu.Unlock()
	b, err := os.ReadFile(p.Artifact("project.json"))
	if err != nil {
		return err
	}
	var meta map[string]any
	if err := json.Unmarshal(b, &meta); err != nil {
		return fmt.Errorf("project.json 解析失败: %w", err)
	}
	if val == nil {
		delete(meta, key)
	} else {
		meta[key] = val
	}
	if err := WriteFileAtomic(p.Artifact("project.json"), mustJSONBytes(meta), 0o644); err != nil {
		return err
	}
	putArtifact(artifacts.ProjectKey(p.ID, "project.json"), p.Artifact("project.json"))
	return nil
}

// ── LLM 请求模板（prompt 库 v0 · 会话模式即产物契约）─────────────

func storyboardRequest(p *Project, article, out string) string {
	return fmt.Sprintf(`# 作业：分镜规划（帧述 M0 · LLM 作业 #1）

## 角色与任务
你是视频的分镜规划器。

%s

## 输出要求
把 JSON 写到: %s
严格按此结构（UTF-8，无注释，无多余字段）:

{
  "timing_basis": "narration",
  "style_id": "",
  "plan": "一句话说明分段思路与时长分配",
  "segments": [
    {
      "id": "seg01",
      "idx": 1,
      "key": "段落短标题",
      "narration": "该段旁白全文",
      "visual_brief": "画面提示（一句话）",
      "duration_hint": 10
    }
  ]
}
（overlay 可选：仅启用时在头部加 "overlay" 字段、相关段加 "chapter"，结构见上面要求）

## 硬约束（schema 校验会拒收）
- segments 非空；每段 narration、duration_hint > 0；id 从 seg01 连续编号
- 总时长 ≤ 900；旁白只能来自文稿内容
`, storyboardBrief(article, storyboardNotes(p)), out)
}

func compRequest(p *Project, seg contract.Segment, direction, out string) string {
	return fmt.Sprintf(`# 作业：段合成物（帧述 M0 · LLM 作业 #3 · %s）

## 段信息（来自分镜表）
- 段号: %d（id=%s）
- 标题: %s
- 旁白（时长基准，约 %.0f 秒）: %s
- 画面提示: %s
- 全片风格: %s

## 任务
写这一个段的 HyperFrames 子合成物 HTML（一段 = 一个 sub-composition），
同时给出元素注册表（每个可见元素的稳定 id + 人话名）。

## HyperFrames 硬规则（违反会被 lint 拒收）
1. 整个文件就是一个 <template>…</template> 片段，无 DOCTYPE/html/head/body
2. 根元素 data-composition-id="%s"；样式作用于根用 #root，不要给根加 class
3. 恰好一个时间轴，用守卫 API 创建（内部即 paused 并注册 window.__timelines）：var tl = hf.timeline("%s");
4. 动画一律走 hf 守卫 API，白名单外的属性会被 hf 当场拒绝并给出替代建议：
   - hf.set(tl, 选择器, 属性表, 时刻) — 定格初值；hf.tween(tl, 选择器, {属性 + duration + ease}, 时刻)
   - hf.fromTo(tl, 选择器, {起始}, {结束 + duration}, 时刻)
   - 可补间属性只有 opacity / x / y / scale / scaleX / scaleY / rotation / skew 系（transform 合成器属性）。
     letterSpacing、width、height、fontSize、top、left 等排版布局属性禁止补间——
     字距展开拆成单字元素动 x，宽度增长用 scaleX，字号变化用 scale。
   - canvas / 程序化动画：hf.eachFrame(tl, 时长秒, function(t){ draw(t); }) —
     draw 必须是 t 的纯函数；随机数用固定种子每帧重生成，seek 到任意时刻画面可复现。
   - 数字滚动（读数/统计跳变）：hf.count(tl, 元素, 起值, 止值, 时刻, 时长, 格式函数)
   - 画面脚本里不直接调用 gsap（hf 内部已封装；引擎对裸 gsap 的违规属性仍会拦截）
5. 每个可见元素给 data-start / data-duration / data-track-index 与 class="clip"
6. 确定性：无网络、无 Date.now、无 Math.random、无 CSS transition/repeat/yoyo
7. 字体只用本地资产或系统楷体；不引任何外链；字体栈里出现的每个字体都要有 @font-face（系统字体用 src:local 声明）
8. 揭示随旁白时间点分布（不前载）；字幕带占底部 ~17%%，主内容留在上 83%%
9. 元素命名规范（平台门禁）：每个有意义的可见元素带
   data-hf-name="人话名"（如 data-hf-name="箭头：蓝光弹开"）

## 输出要求
把 JSON 写到: %s

{
  "html": "<template>…整段合成物…</template>",
  "elements": [
    { "id": "%s-main-title", "name": "标题：xxx" },
    { "id": "%s-arrow-1",   "name": "箭头：xxx" }
  ]
}
`, seg.Key, seg.Idx, seg.ID, seg.Key, seg.DurationHint, seg.Narration, seg.VisualBrief, directionLabel(direction), seg.ID, seg.ID, out, seg.ID, seg.ID)
}

func directionLabel(d string) string {
	if d == "" {
		return "（风格样张未出，先按分镜气质自拟，风格确认后可重生成）"
	}
	return d
}
