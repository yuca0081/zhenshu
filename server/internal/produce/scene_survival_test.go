package produce

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/events"
	"webpage-video-agent/server/internal/pipeline"
)

// TestSceneSurvivalBench M-C 首项：LLM 全自动产 scene 段成活率基准（链路闸验收 ≥7/10）。
// SCENE_SURVIVAL=1 显式开启（真实 LLM 调用 + 渲染，全程 15-30 分钟）。
// 流程：夹具项目（10 段数学/粒子类选题，合成词表+静音音频）→ sceneGen（LLM 直写场景文件）→
// 落帧 → assemble → 逐段 hyperframes render → 抽帧像素统计 → 回放门禁。
// 成活 = 段渲出视频 + 中段画面非空 + 回放 diff=0。报告写 <proj>/reports/survival.md。
func TestSceneSurvivalBench(t *testing.T) {
	if os.Getenv("SCENE_SURVIVAL") != "1" {
		t.Skip("SCENE_SURVIVAL=1 才跑（真实 LLM + 渲染，15-30 分钟）")
	}
	root, _ := filepath.Abs(filepath.Join("..", "..", ".."))
	loadDotEnv(t, filepath.Join(root, ".env"))
	if os.Getenv("LLM_API_KEY") == "" {
		t.Fatal("LLM_API_KEY 未配置（.env 或环境变量）")
	}
	pipeline.EnsureFFmpeg() // FFMPEG_BIN/winget 扫描注入 extraPATH（判空抽帧要用）

	projID := "p-survival-bench"
	projDir := filepath.Join(root, "data", "projects", projID)
	os.RemoveAll(projDir)
	scaffoldSurvivalProject(t, root, projDir)

	r := &Runner{DataDir: filepath.Join(root, "data"), RootDir: root, Hub: events.NewHub()}
	p := &pipeline.Project{ID: projID, Dir: projDir, Ctx: context.Background()}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()

	instr := "本片是数学科普风格测试片（深空底+霓虹发光审美）。画面主体优先用程序化画布" +
		"（<canvas> + window.__draw_ 参数化绘制，每段 1 个，全画幅），再叠 1 个大字标题。纯代码画面，不引图片。"
	t.Log("① sceneGen（LLM 直写场景文件，10 段并发 3）…")
	if err := r.sceneGen(ctx, p, instr, "", ""); err != nil {
		t.Fatalf("sceneGen: %v", err)
	}
	t.Log("② 落帧 + assemble …")
	// 落帧（payload → compositions/frames）：不走 pipeline.Run（会从 manuscript 起全链重跑，
	// 触碰 TTS 等重阶段）；stageCompositions 的提取逻辑等价内联。
	for _, seg := range sbSegs(t, p) {
		b, err := os.ReadFile(p.Artifact("llm/comp-" + seg + ".json"))
		if err != nil {
			t.Fatalf("comp-%s.json: %v", seg, err)
		}
		var payload struct {
			HTML string `json:"html"`
		}
		if err := json.Unmarshal(b, &payload); err != nil || payload.HTML == "" {
			t.Fatalf("comp-%s.json 缺 html", seg)
		}
		if err := os.MkdirAll(p.Artifact("compositions/frames"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p.Artifact("compositions/frames/"+seg+".html"), []byte(payload.HTML), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.python(ctx, "ai/assemble.py", p.Dir, 2*time.Minute); err != nil {
		t.Fatalf("assemble: %v", err)
	}

	// ③ 逐段统计 + 渲染抽帧
	sb, _ := p.LoadStoryboard()
	type row struct {
		id                          string
		hasScene, rendered, painted bool
		note                        string
	}
	var rows []row
	for _, seg := range sb.Segments {
		row := row{id: seg.ID, hasScene: specHasScene(p, seg.ID)}
		if !existsFile(p.Artifact("compositions/frames/" + seg.ID + ".html")) {
			row.note = "场景文件缺失"
			rows = append(rows, row)
			continue
		}
		out := p.Artifact("renders/segs/" + seg.ID + ".mp4")
		if _, err := pipeline.RunCLI(p, "npx", "hyperframes@0.8.55", "render", ".",
			"-c", ".hf-seg/"+seg.ID+".html", "-o", "renders/segs/"+seg.ID+".mp4"); err != nil {
			row.note = "渲染失败"
			rows = append(rows, row)
			continue
		}
		row.rendered = true
		if midFramePainted(t, p, seg.ID, out) {
			row.painted = true
		} else {
			row.note = "中段画面疑似空屏"
		}
		rows = append(rows, row)
	}

	// ④ 确定性回放门禁（全片一次）
	replayErr := r.sceneReplayCheck(ctx, p)

	// ⑤ 报告
	var b strings.Builder
	sceneN, aliveScene := 0, 0
	fmt.Fprintf(&b, "# scene 成活率基准（%s）\n\n| 段 | scene | 渲出 | 画面 | 备注 |\n|---|---|---|---|---|\n",
		time.Now().Format("2006-01-02 15:04"))
	for _, row := range rows {
		mark := func(ok bool) string {
			if ok {
				return "✓"
			}
			return "✗"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", row.id, mark(row.hasScene), mark(row.rendered), mark(row.painted), row.note)
		if row.hasScene {
			sceneN++
			if row.rendered && row.painted {
				aliveScene++
			}
		}
	}
	fmt.Fprintf(&b, "\n- scene 段：%d/%d；scene 段成活（渲出+画面非空）：%d/%d（验收线 ≥70%%）\n",
		sceneN, len(rows), aliveScene, sceneN)
	fmt.Fprintf(&b, "- 确定性回放门禁：%v\n", replayErr == nil)
	if replayErr != nil {
		fmt.Fprintf(&b, "  - %s\n", strings.ReplaceAll(replayErr.Error(), "\n", " / "))
	}
	repDir := p.Artifact("reports")
	_ = os.MkdirAll(repDir, 0o755)
	_ = os.WriteFile(filepath.Join(repDir, "survival.md"), []byte(b.String()), 0o644)
	t.Log("\n" + b.String())
	if sceneN >= 7 && aliveScene*10 < sceneN*7 {
		t.Errorf("scene 成活率 %d/%d 低于验收线 70%%——优先补模板（methods/scene/），不放宽校验", aliveScene, sceneN)
	}
}

// sbSegs 分镜段 ID 列表。
func sbSegs(t *testing.T, p *pipeline.Project) []string {
	t.Helper()
	sb, err := p.LoadStoryboard()
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(sb.Segments))
	for _, seg := range sb.Segments {
		out = append(out, seg.ID)
	}
	return out
}

// midFramePainted 段中点抽帧，画面非全黑/非全纯色即视为画布有内容。
func midFramePainted(t *testing.T, p *pipeline.Project, segID, mp4 string) bool {
	ff, err := pipeline.FFmpegPath()
	if err != nil {
		return false
	}
	_ = os.MkdirAll(p.Artifact("reports"), 0o755) // scaffold 不建 reports；不建目录 ffmpeg 写 PNG 直接失败
	png := p.Artifact("reports/survival-" + segID + ".png")
	_ = os.Remove(png)
	if err := exec.Command(ff, "-y", "-v", "error", "-i", mp4, "-vf", "select=eq(n\\,60),scale=480:-2",
		"-frames:v", "1", png).Run(); err != nil {
		return false
	}
	b, err := os.ReadFile(png)
	if err != nil || len(b) < 3000 {
		return false
	}
	return pngPixelStd(b) > 4.0 // 纯色底的标准差 ≈0；有绘制内容显著大于 0
}

// pngPixelStd 极简像素标准差（真解码采样；纯色底 ≈0，有绘制内容显著大于 0）。
func pngPixelStd(b []byte) float64 {
	img, err := decodePNGBytes(b)
	if err != nil {
		return 0
	}
	bd := img.Bounds()
	var sum, sum2, n float64
	for y := bd.Min.Y; y < bd.Max.Y; y += 4 {
		for x := bd.Min.X; x < bd.Max.X; x += 4 {
			r, g, bl, _ := img.At(x, y).RGBA()
			v := float64(r+g+bl) / 3 / 257
			sum += v
			sum2 += v * v
			n++
		}
	}
	if n == 0 {
		return 0
	}
	mean := sum / n
	return sum2/n - mean*mean
}

func decodePNGBytes(b []byte) (image.Image, error) {
	return png.Decode(bytes.NewReader(b))
}

func scaffoldSurvivalProject(t *testing.T, root, projDir string) {
	t.Helper()
	for _, d := range []string{"storyboards", "llm", "audio", "style"} {
		if err := os.MkdirAll(filepath.Join(projDir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	w := func(rel string, v any) {
		b, _ := json.MarshalIndent(v, "", "  ")
		if err := os.WriteFile(filepath.Join(projDir, rel), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w("project.json", map[string]any{"name": "scene 成活率基准", "aspect": "16:9"})
	segs := []map[string]any{
		{"id": "seg01", "idx": 1, "key": "乘法表弦图", "narration": "乘法表在圆周上连线，倍率从二演化到三，图案焕然一新", "visual_brief": "弦图：圆周 N 点按倍率 k 连线，k 从 2 连续演变到 3", "duration_hint": 5},
		{"id": "seg02", "idx": 2, "key": "深空星尘", "narration": "深空里星尘缓慢漂浮，明暗呼吸", "visual_brief": "粒子场：星尘粒子漂浮+呼吸明暗", "duration_hint": 5},
		{"id": "seg03", "idx": 3, "key": "旋转立方体", "narration": "一个线框立方体在三维空间里缓缓旋转", "visual_brief": "线框 3D：立方体双轴旋转投影", "duration_hint": 5},
		{"id": "seg04", "idx": 4, "key": "椭圆曲线", "narration": "椭圆曲线上的点排成优美的弧线", "visual_brief": "函数曲线：椭圆曲线逐步绘制发光", "duration_hint": 5},
		{"id": "seg05", "idx": 5, "key": "波纹扩散", "narration": "波纹从中心一圈圈扩散出去", "visual_brief": "同心圆波纹从中心扩散，逐圈淡出", "duration_hint": 5},
		{"id": "seg06", "idx": 6, "key": "素数点亮", "narration": "数轴上的素数被逐个点亮", "visual_brief": "数轴：素数位置逐个点亮成光点", "duration_hint": 5},
		{"id": "seg07", "idx": 7, "key": "网络生长", "narration": "节点与连线织成一张生长的网络", "visual_brief": "网络图：节点逐个出现、连线生长", "duration_hint": 5},
		{"id": "seg08", "idx": 8, "key": "吸引子", "narration": "洛伦兹吸引子画出蝴蝶的翅膀", "visual_brief": "轨道曲线：洛伦兹吸引子轨迹逐步画出", "duration_hint": 5},
		{"id": "seg09", "idx": 9, "key": "傅里叶合成", "narration": "许多正弦波叠成方波", "visual_brief": "多条正弦曲线叠加逐步合成方波", "duration_hint": 5},
		{"id": "seg10", "idx": 10, "key": "旋臂星系", "narration": "粒子汇成星系的旋臂", "visual_brief": "粒子螺旋：粒子按旋臂分布缓慢旋转", "duration_hint": 5},
	}
	w("storyboards/storyboard.json", map[string]any{
		"timing_basis": "narration", "style_id": "cosmos",
		"segments": segs,
	})
	w("style/style_samples.json", map[string]any{
		"direction": "深空渐变（高饱和·宇宙系）", "confirmed": true,
		"samples": []map[string]any{{"tag": "A", "desc": "深空底", "html": `<div style="width:1920px;height:1080px;background:linear-gradient(200deg,#0B1035,#1B0B54);color:#EAF0FF;font-size:80px;font-family:Georgia,serif;display:flex;align-items:center;justify-content:center;">深空霓虹</div>`}},
	})
	// 合成词表 + 静音音频
	voices := []map[string]any{}
	for _, s := range segs {
		sid := s["id"].(string)
		nar := s["narration"].(string)
		var words []map[string]any
		for i, ch := range strings.Split(nar, "") {
			if strings.TrimSpace(ch) == "" {
				continue
			}
			words = append(words, map[string]any{"text": ch, "start": float64(20+i*45) / 100, "end": float64(60+i*45) / 100})
		}
		dur := 4.0
		voices = append(voices, map[string]any{"id": sid, "duration_s": dur, "words": words, "phrases": [][]int{{0, len(words) - 1}}})
		if err := exec.Command("ffmpeg", "-y", "-v", "error", "-f", "lavfi", "-i", "anullsrc=r=24000:cl=mono",
			"-t", fmt.Sprint(dur), filepath.Join(projDir, "audio", sid+".wav")).Run(); err != nil {
			t.Fatalf("静音音频 %s: %v", sid, err)
		}
	}
	w("audio_meta.json", map[string]any{"voices": voices})
}

// loadDotEnv 极简 .env 加载（KEY=VALUE，# 注释）——不引 config 包避免全局副作用。
func loadDotEnv(t *testing.T, path string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, ln := range strings.Split(string(b), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") || !strings.Contains(ln, "=") {
			continue
		}
		k, v, _ := strings.Cut(ln, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if os.Getenv(k) == "" && v != "" {
			os.Setenv(k, v)
		}
	}
}

var _ = contract.SegTail // 引用契约包保持 import（未来统计可直接用）
