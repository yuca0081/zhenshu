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
	f := p.Artifact("manifest.jsonl")
	line, _ := json.Marshal(map[string]any{
		"ts": time.Now().Format(time.RFC3339), "event": event, "detail": detail,
	})
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

// ctxCommand 可取消的外部进程：ctx 结束时整棵进程树击杀。exec.CommandContext
// 默认只杀直接子进程——npx→node→hyperframes→chromium/ffmpeg 的孙进程会存活，
// 继续烧 CPU、写 renders/（win32 是主力开发平台，TerminateProcess 杀不动 cmd 包装的树）。
func ctxCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	setSysProcAttr(cmd)
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			killProcessTree(cmd.Process.Pid)
		}
		if cmd.Process != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = 10 * time.Second // 树杀后给孙进程管道收尾的宽限，超时强收
	return cmd
}

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
func Stages() []Stage {
	return []Stage{
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

// Run 顺序执行阶段；遇 等待LLM/待实现/错误 即停（产物已落盘，重跑续上）。
// until 非空时执行到该阶段为止（含）。ctx 取消时在阶段边界退出（渲染中的
// 子进程由 runCLI 经 p.Ctx 连带杀掉）。
func Run(ctx context.Context, p *Project, until string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	p.Ctx = ctx
	for _, st := range Stages() {
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
			_ = writeFile(final, mustJSON(ss))
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
		if err := writeFile(final, mustJSON(ss)); err != nil {
			return err
		}
		p.Manifest("style.confirmed", ss.Direction)
		return LockDesign(p, &ss)
	}

	// API 模式：直出设计系统（confirmed 置 false，等用户在舞台确认——硬门由 final 落定校验把守）。
	// NoThink：设计+样张是结构化 CSS 作业，开思考烧上万 reasoning token、拖到分钟级。
	// 产出比 v1 大（皮肤×2+三角色+动效+装置+样张），草稿校验带修复循环（缺项回喂重出，≤2 轮）；
	// 风格简报存在时追加特色落点覆盖检查（v3 §4.5：机械数数，缺/越枚举=打回）。
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
			usage, gerr := prov.GenerateJSONNoThink(pctx(p), designSystemPrompt, prompt, &ss)
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
				if err := writeFile(final, mustJSON(ss)); err != nil {
					return err
				}
				p.Manifest("llm.usage", fmt.Sprintf("style_samples prompt=%d completion=%d tokens",
					usage.PromptTokens, usage.CompletionTokens))
				p.Manifest("style.drafted", ss.Direction)
				p.Manifest("style_samples.api", prov.Name+"/"+prov.Model)
				// 迷你真段：草稿落定后立刻出动态小样（任何失败只降级静态，见 styleclips.go），
				// 完成后把 Video/ClipNote 回写进样张文件
				if EnsureSampleClips(p, &ss, sb) {
					_ = writeFile(final, mustJSON(ss))
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
// 生成命令（引擎 Kokoro 离线，验证通过的调用形态）：
//
//	npx hyperframes@0.8.55 tts --text-file audio/segNN.txt -v zf_xiaobei -l zh -o audio/segNN.wav --json
//	faster-whisper medium + initial_prompt=旁白原文 → 字级时间戳 → audio_meta.json
//
// ⚠️ --text-file 文件必须存在，否则路径字符串会被当文本朗读（已踩坑）。
// 本阶段校验产物齐全；批量生成器在 ai/ 侧（ai/tts_align.py，下一里程碑并入）。
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
	return nil
}

// stageCheck 质量门禁：调 hyperframes check（lint+runtime+layout+motion+contrast）。
func stageCheck(p *Project) error {
	out, err := runCLI(p, "npx", "--yes", "hyperframes@0.8.55", "check", "--no-browser-gpu")
	if err != nil {
		// 完整输出：上游修复轮要从 ✗ 行提取报错段（截断会丢 Layout 段的重叠错误）；
		// 对话/清单侧各自再做 tail/挑行
		return fmt.Errorf("check 未过（诊断如下，修复后重跑）:\n%s", out)
	}
	_ = writeFile(p.Artifact(".hyperframes-ok"), "check passed "+time.Now().Format(time.RFC3339)+"\n")
	p.Manifest("check.passed", "")
	return nil
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
		fmt.Printf("  · %s 渲染中\n", seg.ID)
		outLog, err := runCLI(p, "npx", "hyperframes@0.8.55", "render", ".",
			"-c", ".hf-seg/"+seg.ID+".html", "-o", "renders/segs/"+seg.ID+".mp4")
		if err != nil {
			return fmt.Errorf("%s 渲染失败:\n%s", seg.ID, tailLines(outLog, 30))
		}
		if sum, err := segSrcSum(p, seg.ID); err == nil {
			_ = os.WriteFile(out+".sum", []byte(sum), 0o644)
		}
		p.Manifest("render.seg", seg.ID)
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
	return nil
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
func segSrcSum(p *Project, segID string) (string, error) {
	h := sha256.New()
	for _, src := range []string{
		p.Artifact("compositions/frames/" + segID + ".html"),
		p.Artifact("audio/" + segID + ".wav"),
		p.Artifact(".hf-seg/" + segID + ".html"),
	} {
		b, err := os.ReadFile(src)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", src, len(b))
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
	tl := make([]timelineRow, len(sb.Segments))
	at := 0.0
	for i, seg := range sb.Segments {
		d := probeDuration(filepath.Join(segsDir, seg.ID+".mp4"))
		if d <= 0 {
			return fmt.Errorf("timeline: 段片 %s 时长探测失败", seg.ID)
		}
		tl[i] = timelineRow{ID: seg.ID, Start: at, End: at + d}
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
	return "fade"
}

// ProjectStyleID 项目的风格库锚点：style_samples.style_id 优先（选库/冷启动落盘），
// storyboard.style_id 兜底（老项目分镜 LLM 填写）。
func ProjectStyleID(p *Project) string {
	if b, err := os.ReadFile(p.Artifact("style/style_samples.json")); err == nil {
		var ss struct {
			StyleID string `json:"style_id"`
		}
		if json.Unmarshal(b, &ss) == nil && strings.TrimSpace(ss.StyleID) != "" {
			return strings.TrimSpace(ss.StyleID)
		}
	}
	if sb, err := p.LoadStoryboard(); err == nil {
		return strings.TrimSpace(sb.StyleID)
	}
	return ""
}

// timelineRow xfade 成片的实际段边界（sfx 布点/前端时间轴对齐的权威口径）。
type timelineRow struct {
	ID    string  `json:"id"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// LoadTimeline 读 renders/timeline.json（xfade/concat 拼接都写，段片实测边界）。
func LoadTimeline(p *Project) []timelineRow {
	b, err := os.ReadFile(p.Artifact("renders/timeline.json"))
	if err != nil {
		return nil
	}
	var tl []timelineRow
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
	tl := make([]timelineRow, len(ids))
	for i, id := range ids {
		end := starts[i] + durs[i]
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		tl[i] = timelineRow{ID: id, Start: starts[i], End: end}
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
	return nil
}

// StitchNow 只跑拼接/混音/后期（成片就绪后换 BGM 或调后期用，画面不动）。
func StitchNow(p *Project) error { return stageStitch(p) }

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

// runFFmpegRename 跑 ffmpeg，成功后临时文件原子转正（混音/终装共用）。
func runFFmpegRename(p *Project, args []string, tmp, out, what string) error {
	if _, err := runFFmpeg(p, p.Dir, what, args...); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	_ = os.Remove(out)
	return os.Rename(tmp, out)
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
	_ = os.Remove(out)
	if err := os.Rename(tmp, out); err != nil {
		return ""
	}
	return out
}

// ── 统一后期（P0-1：暗角/颗粒/色差）────────────────────────────────

// postFX 全片统一后期参数（stitch 阶段滤镜，0–1；全 0 = 关闭）。
// 来源优先级：project.json "post"（false=关 / 对象=整组替换）> 风格 tokens.post > 默认轻后期。
type postFX struct {
	Vignette float64 `json:"vignette"` // 暗角
	Grain    float64 `json:"grain"`    // 胶片颗粒
	Chroma   float64 `json:"chroma"`   // 轻微色差
}

// 默认轻后期：风格未配 post 节时的兜底（轻暗角+轻颗粒；色差默认关，按风格再开）。
var defaultPostFX = postFX{Vignette: 0.35, Grain: 0.4}

func (fx *postFX) off() bool {
	return fx == nil || (fx.Vignette <= 0 && fx.Grain <= 0 && fx.Chroma <= 0)
}

// postFor 后期参数解析（stitch 每次执行时读盘——换风格/改配置无需重启）。
// v3 库条目不携带 tokens，读库 post 的旧路径已随 schema 退役。
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
	d := defaultPostFX
	return &d
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

// probeDuration ffprobe 取媒体时长（秒；失败返回 0，调用方跳过淡出）。
func probeDuration(path string) float64 {
	if p, err := exec.LookPath("ffprobe"); err == nil {
		out, err := exec.Command(p, "-v", "error", "-show_entries", "format=duration",
			"-of", "csv=p=0", path).Output()
		if err == nil {
			if f, perr := strconv.ParseFloat(strings.TrimSpace(string(out)), 64); perr == nil {
				return f
			}
		}
	}
	if rootDir != "" && extraPATH != "" { // 回退 winget bin 目录
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
	_ = os.Remove(dst)
	return os.Rename(tmp, dst)
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
func SetProjectField(p *Project, key string, val any) error {
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
	return WriteFileAtomic(p.Artifact("project.json"), mustJSONBytes(meta), 0o644)
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
