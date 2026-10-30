package produce

// cover.go —— 出片封面（2026-10-30）：终审过后、produce.done 之前自动出一张
// 兜底封面 renders/cover.png（成片抽帧 + 设计系统标题版式，零 LLM）。纪律同
// 配乐工位：失败跳过记账、永不挡片。项目页可按需重做（api regenCover）：换帧
// （传 t=播放器当前时刻）/改标题文案；段级重做收尾也刷新一次（同帧位重抓，
// 用户手挑过的帧位与标题不丢）。
// 版式走 HTML→无头截图（ai/snap_samples.py --one：画幅同尺寸窗口 + 目录字体
// woff2 真声明）而非 ffmpeg drawtext——字体资产是 woff2 data URL，drawtext
// 吃不了；设计系统的字体栈/字重/字距/强调色也只有 CSS 表达得全。

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"webpage-video-agent/server/internal/pipeline"
	"webpage-video-agent/server/internal/prep"
)

// CoverSpec 封面账面（renders/cover.json）：帧时刻与标题文案。重做入口与前端
// 展示都读它；缺失 = 项目还没有封面（老项目/自动生成失败）。
type CoverSpec struct {
	T     float64 `json:"t"`
	Title string  `json:"title"`
}

func coverSpecPath(p *pipeline.Project) string  { return p.Artifact("renders/cover.json") }
func coverPNGPath(p *pipeline.Project) string   { return p.Artifact("renders/cover.png") }
func coverFramePath(p *pipeline.Project) string { return p.Artifact("renders/cover-frame.jpg") }

// LoadCover 读封面账面；缺失/损坏返回 nil（损坏不硬撑——下次重做会覆盖）。
func LoadCover(p *pipeline.Project) *CoverSpec {
	b, err := os.ReadFile(coverSpecPath(p))
	if err != nil {
		return nil
	}
	var cs CoverSpec
	if json.Unmarshal(b, &cs) != nil || cs.T < 0 {
		return nil
	}
	return &cs
}

// PickCoverT 兜底帧位：总时长 30% 处所在段的中点——避开开场标题卡与结尾
// 收尾卡，落段中点又避开段界转场。timeline 缺失（拼接未落账）返回 false。
func PickCoverT(p *pipeline.Project) (float64, bool) {
	tl := pipeline.LoadTimeline(p)
	if len(tl) == 0 {
		return 0, false
	}
	total := tl[len(tl)-1].End
	if total <= 0 {
		return 0, false
	}
	target := total * 0.3
	row := tl[0]
	for _, r := range tl {
		if r.Start <= target {
			row = r
			continue
		}
		break
	}
	t := (row.Start + row.End) / 2
	if t < 0 {
		t = 0
	}
	return t, true
}

// DefaultCoverTitle 封面标题兜底 = 项目名（project.json "name"，创建时落盘）。
func DefaultCoverTitle(p *pipeline.Project) string {
	return strings.TrimSpace(pipeline.ReadProjectField(p, "name"))
}

// MakeCover 出一张封面：成片 t 时刻抽原画帧 → 设计系统标题版式 HTML →
// 无头截图 renders/cover.png，账面落 cover.json。每步失败都原样返回错误，
// 挡不挡片由调用方决定（自动路径吞掉，手动路径报给用户）。
func (r *Runner) MakeCover(ctx context.Context, p *pipeline.Project, t float64, title string) error {
	src := p.Artifact("renders/main.mp4")
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("成片不在本机，抽不了封面帧: %w", err)
	}
	if t < 0 {
		t = 0
	}
	ff, err := pipeline.FFmpegPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p.Artifact("renders"), 0o755); err != nil {
		return err
	}
	// 原画帧（JPEG q2 视觉无损级，base64 内嵌进版式 HTML——--one 会把 HTML 拷进
	// style/ 截图，相对路径会断，data URL 是既有惯例）
	var buf bytes.Buffer
	grab := exec.CommandContext(ctx, ff, "-y", "-v", "error", "-ss", strconv.FormatFloat(t, 'f', 3, 64),
		"-i", src, "-frames:v", "1", "-q:v", "2", coverFramePath(p))
	grab.Stdout, grab.Stderr = &buf, &buf
	if err := grab.Run(); err != nil {
		return fmt.Errorf("抽帧失败（t=%.2f）: %s", t, pipeline.TailLines(buf.String(), 3))
	}
	frame, err := os.ReadFile(coverFramePath(p))
	if err != nil {
		return fmt.Errorf("封面帧不可读: %w", err)
	}
	if err := pipeline.WriteFileAtomic(p.Artifact("renders/.cover.html"), []byte(buildCoverHTML(p, frame, title)), 0o644); err != nil {
		return err
	}
	// --one 模式走 pythonRaw：argv 必须是 ["--one", proj, html, out]——r.python
	// 的「脚本 <项目目录>」惯例会把 projDir 垫在 argv[1]，--one 分支永不命中、
	// 脚本落回批量模式重截样张（首跑实测），封面静默缺席。
	if err := r.pythonRaw(ctx, "ai/snap_samples.py",
		[]string{"--one", p.Dir, p.Artifact("renders/.cover.html"), coverPNGPath(p)}, 2*time.Minute); err != nil {
		return fmt.Errorf("封面截图失败: %w", err)
	}
	if _, err := os.Stat(coverPNGPath(p)); err != nil {
		return fmt.Errorf("截图无产物（浏览器静默失败）")
	}
	cs := CoverSpec{T: t, Title: title}
	b, err := json.Marshal(cs)
	if err != nil {
		return err
	}
	return pipeline.WriteFileAtomic(coverSpecPath(p), b, 0o644)
}

// makeCoverTail 出片/重做收尾的自动封面（best-effort：失败记账 cover.error 不挡
// 片）。keepFrame=true（段级重做）沿用既有帧位与标题——用户手挑过的东西不因
// 重做丢掉，只把同一位的画面刷新成新片；false（整片重做）重挑兜底帧位，
// 标题仍沿用既有（用户改过标题的话）。
func (r *Runner) makeCoverTail(ctx context.Context, p *pipeline.Project, keepFrame bool) {
	if ctx.Err() != nil {
		return
	}
	existing := LoadCover(p)
	t := 0.0
	switch {
	case keepFrame && existing != nil:
		t = existing.T
	default:
		picked, ok := PickCoverT(p)
		if !ok {
			r.Manifest(p, "cover.skipped", "无 timeline（拼接未落账），封面跳过")
			return
		}
		t = picked
	}
	title := DefaultCoverTitle(p)
	if existing != nil && strings.TrimSpace(existing.Title) != "" {
		title = existing.Title
	}
	if err := r.MakeCover(ctx, p, t, title); err != nil {
		if ctx.Err() != nil {
			return // 取消收场：这是取消不是封面故障
		}
		log.Printf("[produce] %s 封面生成失败（不挡片）: %v", p.ID, err)
		r.Manifest(p, "cover.error", err.Error())
		return
	}
	r.Manifest(p, "cover.done", fmt.Sprintf("t=%.2fs「%s」renders/cover.png", t, title))
}

// buildCoverHTML 封面版式：完整帧 + 毛玻璃标题带（下三分）+ 带内标题块（设计
// 系统 display 字体栈/字重/字距 + 皮肤强调色小杠紧贴标题）。设计件缺失逐项
// 优雅降级（系统字体栈/无小杠），截图窗口由 --one 按项目画幅定，这里只排版。
// 关键约束：正片画面自带文字可以在任何位置（烧录字幕/版面内容，首跑实测压
// 标题上），纯加深色罩永远压不干净、放大裁切又切坏构图——标题带用
// backdrop blur（无头截图已实测生效）：底下文字糊成色斑，字可读性归零，
// 带里仍透出帧的原有色彩，带外画面零裁切。
func buildCoverHTML(p *pipeline.Project, frame []byte, title string) string {
	stack, weight, tracking := "", "", ""
	accent := ""
	if sp, err := prep.LoadStylePack(p); err == nil && sp.DesignSystem != nil {
		if ds := sp.DesignSystem.Fonts; ds != nil {
			stack = strings.TrimSpace(ds.Display.Stack)
			weight = strings.TrimSpace(ds.Display.Weight)
			tracking = strings.TrimSpace(ds.Display.Tracking)
		}
		if skins := sp.DesignSystem.EffectiveSkins(); len(skins) > 0 {
			accent = strings.TrimSpace(skins[0].Slots.Accent)
		}
	}
	if stack == "" {
		stack = `"Microsoft YaHei", "Noto Sans SC", sans-serif`
	} else {
		stack += `, "Microsoft YaHei", "Noto Sans SC", sans-serif`
	}
	if weight == "" {
		weight = "700"
	}

	// 目录字体真声明：栈里点名、font_assets 备了 woff2 的族逐个补 url()——
	// 与正片同款字体，封面不是雅黑替身（2026-10-13 齐马蓝教训）
	fontFaces := ""
	assets, _ := pipeline.LoadFontAssetsDetail(p)
	for family, dataURL := range assets {
		if strings.Contains(strings.ToLower(stack), family) {
			fontFaces += fmt.Sprintf("@font-face{font-family:'%s';src:url(\"%s\") format('woff2');}", family, dataURL)
		}
	}

	fontSize := 84 // 16:9
	if pipeline.ReadProjectField(p, "aspect") == "9:16" {
		fontSize = 68
	}
	bar := ""
	if accent != "" {
		bar = fmt.Sprintf(`<div class="bar" style="background:%s"></div>`, html.EscapeString(accent))
	}
	return fmt.Sprintf(`<!doctype html><html><head><meta charset="utf-8"><style>
html,body{margin:0;padding:0;width:100%%;height:100%%;overflow:hidden;background:#000;}
%s
.bg{position:absolute;inset:0;background:url(data:image/jpeg;base64,%s) center/cover no-repeat;}
.band{position:absolute;left:0;right:0;bottom:0;box-sizing:border-box;padding:32px 64px 42px;
      backdrop-filter:blur(22px) saturate(1.15);background:rgba(8,10,14,.52);}
/* 带上缘羽化：帧内容常正好探到带边，硬边会拦腰切断一行字留残字上缘
   （真机验收实录）——40px 渐变+中blur过渡，残字上半截在过渡里化掉 */
.band::before{content:"";position:absolute;left:0;right:0;top:-40px;height:40px;
      backdrop-filter:blur(14px);background:linear-gradient(180deg,rgba(8,10,14,0),rgba(8,10,14,.52));}
.bar{width:64px;height:6px;margin-bottom:14px;}
.title{font-family:%s;font-weight:%s;letter-spacing:%s;color:#fff;font-size:%dpx;line-height:1.22;max-width:92%%;text-shadow:0 2px 14px rgba(0,0,0,.4);}
</style></head><body><div class="bg"></div>
<div class="band">%s<div class="title">%s</div></div></body></html>`,
		fontFaces, base64.StdEncoding.EncodeToString(frame),
		stack, weight, trackingOrNormal(tracking), fontSize, bar, html.EscapeString(title))
}

func trackingOrNormal(t string) string {
	if t == "" {
		return "normal"
	}
	return t
}
