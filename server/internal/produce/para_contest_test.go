package produce

// para_contest_test.go —— 段落制创作比武实验（docs/para-transition-plan-2026-10-29.md §6.1）。
//
// 挑一个 3~4 分镜的段落，手工按 §5.1 格式写段落作业书，发写画面工位（同一 provider、
// 同一系统提示词、同一契约检查/机检/渲染/段审机器），验收三指标：契约一次过率、
// 段审结论 vs 该段落分镜单写基线、过渡观感（人工）。不动管线代码——段落作业书的
// 组装逻辑全部在本测试内手写，渲染/检查/判卷复用工位的既有函数。
//
// 默认跳过；显式指定源项目跑：
//
//	PARA_CONTEST=p20261023-110234 go test ./internal/produce -run TestParaContest -v -timeout 45m
//
// 沙盒在 data/paracontest/<项目ID>/（不进 data/projects，服务器不会列到）；
// 产物：renders/segs/para01.mp4（段落片）、reviews/（判卷）、RESULT 摘要打日志。
import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joho/godotenv"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/events"
	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/pipeline"
	"webpage-video-agent/server/internal/prep"
	"webpage-video-agent/server/internal/review"
)

// paraContestPrompt §5.1 段落作业书（手工版——这是实验变量本体）：
// 与单段作业书（sceneUserPrompt）同构，差异只在「时间轴网格」与「段落编排要求」。
func paraContestPrompt(cv contract.Canvas, total float64, paraTitle string, cells []paraCell, merged voiceMeta,
	styleShared, golden, rules, sceneTpl, compMethods string) string {

	var wb strings.Builder
	for i, w := range merged.Words {
		if i > 0 && i%10 == 0 {
			wb.WriteString("\n")
		}
		fmt.Fprintf(&wb, "%d:%s@%.1fs ", i, w.Text, w.Start)
	}
	var cellLines strings.Builder
	for i, c := range cells {
		fmt.Fprintf(&cellLines, "- 分镜%d %s 格子 [%.3f, %.3f]（旁白 %.2fs + 收尾垫 0.35s）\n  标题：%s\n  旁白：%s\n  画面提示：%s\n",
			i+1, c.seg.ID, c.start, c.end, c.v.DurationS, c.seg.Key, c.seg.Narration, c.seg.VisualBrief)
	}
	return fmt.Sprintf(`为这一个段落的视频直接写场景文件 HTML（你是画面工程师，版式/动效/图形程序全部自己写）。
本作业与单段作业的唯一区别：你拿到的是一整个段落（%d 个分镜）的连续时间轴——
一张 HTML、一条 timeline，从段落第一帧编排到最后一帧。
分镜之间不是「换页」，是同一舞台的持续推进：过渡由你在时间轴里编排出来。

## 契约摘要（完整版 docs/contract.md；违反任意一条会被 lint 拒收）
1. 整个文件 = 一个 <template> 片段，无 DOCTYPE/html/head/body
2. 根元素 data-composition-id="para01"（逐字）data-width="%d" data-height="%d"；内部 #root 为舞台
3. 恰好一个 gsap.timeline({paused:true})，末尾注册 window.__timelines["para01"]
4. 全屏轨道层（背景等）带 class="clip" + data-start/data-duration/data-track-index
5. 每个有意义的可见元素带 data-hf-name="人话名"（≥3 个；元素级指认依赖它）
8. 字体：只准用渲染端实际安装的这些——微软雅黑 Microsoft YaHei、黑体 SimHei、宋体 SimSun、楷体 KaiTi、仿宋 FangSong、Consolas、Segoe UI、Georgia、Arial、Times New Roman、Courier New、Impact（外加 serif/sans-serif/monospace 通用族收尾）；
   例外：注入的设计系统展示字体（目录字体，或样张阶段自装的字体）可以直接用，平台保证声明与字体文件；
   引用即声明（@font-face{font-family:'X';src:local('X');}）；除此之外禁 PingFang SC、思源 Source Han/Noto 等未安装字体——lint 对未声明字体会一票否决整段；
   禁止在 CSS transform 里做居中（translateX(-50%%) 等）再让 GSAP 补间同一元素——GSAP 会整体覆盖 CSS transform，居中必须用 GSAP 的 xPercent/yPercent；
   元素的初始缩放/位移状态（如 scale(1.12)、scaleX(0)）一律写进 GSAP 的 fromTo from 值，CSS 里禁写 transform 声明——lint 对 CSS transform 与 GSAP 补间同元素一票否决
9. 不写字幕——底部字幕带由平台注入；内容留在上 83%%，禁止出现 data-ph=/data-w= 标注
10. 元素揭示随旁白节拍分布（对齐文末词序表——时刻是段落绝对时间，不前载不拖尾）
11. 需要新框架（如 three.js）：在 <template> 内写注释声明 <!-- vend: 包名@精确版本 -->，
    平台自动采购并注入（模型不写任何 <script> 标签，含本地路径也不行）
12. 首帧即景——段落 t=0 背景与主体场景已在画面，入场只做元素级位移/缩放/点亮，
    禁整页从黑场/纯色空白淡入开场；末帧定格——段落结尾定格在内容帧，该退的元素
    提前退完、留下的停稳，禁整页淡出到黑/白（以上两条针对整个段落的首尾，
    段落内部的分镜边界不受这两条约束——那里要的是连续编排不是定格）

## 时间轴网格（本段落的骨架——旁白贴格、画面穿格）
- 段落总时长 %.3f 秒 = %d 个分镜格子拼接；timeline 全长用它，格子边界如下
%s- 每个格子的旁白按词级时间戳贴合（见文末词序表，时刻已换算成段落绝对时间）
- 画面动画不受格子边界约束：同一舞台元素可以跨格子持续演进、运镜跨分镜延续——
  格子只是旁白的家，不是画面的墙；信息节拍跟旁白走（旁白每转一个信息点画面响应）
- 分镜之间的过渡由你自己编排（同舞台元素推进/运镜转折/构图重组/焦点点换），
  禁止把格子边界做成「换页」感：禁整页淡出再淡入、禁背景整层换掉重来——
  背景与舞台装置是段落级连续的，变的是前景内容与焦点

## 段落定位
- 第 1/6 段落「%s」·全段 %d 分镜（段落之间才是话题切换，本文件内部全是同一话题的延续）

%s
## 结构金样例（结构/时序/canvas 写法照这个模式；视觉内容必须按本段重写；源码从下一行起到「布局规则」前为止）
%s
## 布局规则
%s
- 每屏至少一个可辨认的视觉重心；禁止全屏无差别小元素平铺；段落适合铺氛围时优先写背景层（渐变/纹理/程序化画布），别停在纯色底
- 样张/参考图里的场记标注文字是打样批注，不是画面内容——场景里的每一条可见文字都必须是观众该读的内容
- 字卡/标题类大字不得逐字重复旁白句——旁白原文由底部字幕承载；字卡只提炼关键词/短语

## 动效手感（防呆板——节奏与编排的硬要求，手法本身自由）
- 入场手法从设计系统「入场手法菜单」取用并轮换：本段落至少三种手法、相邻分镜主手法不重复；全段落只用一种「淡入+浮入」=PPT，打回
- 旁白每转一个信息点画面必须响应（新元素入场/已有元素强调变色/运镜变向）；入场完成后静坐到段尾=PPT，打回
- 运镜按设计系统「运镜」条执行（安静段/定格也是合法设计，全段没有一处可感知运动才是失败）
- 禁所有元素同秒砸入：入场错峰对齐旁白短语节拍，间隔有快慢变化，不要均匀排队
- 扫屏条/横移/滑轨类位移一律用 x/y transform 补间（元素锚定后改 x），禁补间 left/top
- 禁 ease:"none" 的平移/淡入裸入场（canvas 驱动线本身的 ease:"none" 不受此限）：入场要有性格
- 次要元素跟随主元素滞后 0.1–0.3s 入场，成组出现要有主次
- 分镜边界附近是编排重点：下个分镜的首个信息点入场前，先用运镜/强调把视线引过去

## 各分镜信息（格子边界与画面要点；契约/设计/样张/金样例为全段共享）
%s
## 词序表（reveal 对齐用；时刻=段落绝对时间，跨分镜连续编号）
%s
## 输出（只输出一个 JSON 对象：第一个字符必须是 {、最后一个字符必须是 }，前后不许有任何字、标签、围栏或解释）
{"note":"画面思路一句话","html":"<template>…整个段落的场景文件…</template>","elements":[{"id":"元素DOM id","name":"人话名（与 data-hf-name 一致）"}]}
`, len(cells), int(cv.W), int(cv.H), total,
		len(cells), cellLines.String(), paraTitle, len(cells),
		styleShared, golden, rules, sceneTpl+"\n"+compMethods, wb.String())
}

type paraCell struct {
	seg contract.Segment
	v   voiceMeta
	// 格子边界（段落绝对时间）：start = Σ_{k<i}(dur_k+SegTail)，end = start+dur+SegTail
	start, end float64
}

// python 组装器（沙盒内落盘执行）：造 para01.wav（裁尾+垫缝对齐网格）与
// .hf-seg/para01.html（字幕带+场景挂载，复用 ai/assemble.py 的模块级函数；
// scenes_html/project_html 是 main 的内嵌函数，按原文复制）。
const paraContestBuilder = `
import json, pathlib, subprocess, sys, hashlib
sys.path.insert(0, sys.argv[2])  # <root>/ai
import assemble

proj = pathlib.Path(sys.argv[1])
meta = json.loads((proj / 'contest-meta.json').read_text(encoding='utf-8'))
cells = meta['cells']; voice = meta['voice']; W, H = meta['w'], meta['h']

# ── para01.wav：每段 atrim 到网格时长再 pad 0.35s 尾，concat 出段落音轨（绝对路径，cwd 无关）
inputs, fc, labels = [], [], []
for i, c in enumerate(cells):
    inputs += ['-i', str(proj / 'audio' / f"{c['sid']}.wav")]
    fc.append(f"[{i}:a]atrim=duration={c['narr_dur']},apad=pad_dur=0.35,aresample=48000[a{i}]")
    labels.append(f"[a{i}]")
fc.append(''.join(labels) + f"concat=n={len(cells)}:v=0:a=1[out]")
subprocess.run(['ffmpeg', '-y', '-v', 'error', *inputs, '-filter_complex', ';'.join(fc),
                '-map', '[out]', '-c:a', 'pcm_s16le', str(proj / 'audio' / 'para01.wav')], check=True)

# ── 单段工程 .hf-seg/para01.html（scenes_html/project_html 按 assemble.main 内嵌原文复制）
slots = assemble.load_skin_slots(proj)
ovl, chapters, style_id = assemble.load_overlay(proj)
tokens = assemble.load_style_tokens(style_id)
fontface = assemble.fontface_css(tokens.get('font') or '', "Georgia, 'Times New Roman', 'SimSun', serif", slots.get('font') or '')
fontface += '\n' + assemble.fontasset_css(proj, tokens.get('font') or '', slots.get('font') or '')

def scenes_html(scenes, track_from=0):
    out = []
    for i, (sid, start, dur, adur) in enumerate(scenes):
        out.append(
            f'      <div id="el-{sid}" class="scene" data-composition-id="{sid}"\n'
            f'        data-composition-src="compositions/frames/{sid}.html"\n'
            f'        data-start="{start}" data-duration="{dur}" data-track-index="{track_from + i}"></div>\n'
            f'      <audio id="el-{sid}-voice" src="audio/{sid}.wav"\n'
            f'        data-start="{start}" data-duration="{adur}" data-track-index="10" data-volume="1"></audio>')
    return chr(10).join(out)

def project_html(w, h, total_dur, body, ovl_body='', ovl_js='', cap_body='', cap_js='', xfade=True, vend_head=''):
    katex_head = ''
    if (assemble.AI_DIR / 'assets' / 'katex' / 'katex.min.js').exists():
        katex_head = ('    <link rel="stylesheet" href="assets/katex/katex.min.css" />\n'
                      '    <script src="assets/katex/katex.min.js"></script>\n')
    xf_body = ('      <div id="xfade" style="position:absolute;inset:0;background:#000;'
               'z-index:100;pointer-events:none;opacity:1;"></div>\n') if xfade else ''
    xf_js = ''
    if xfade:
        xf_js = (f"tl.fromTo('#xfade',{{opacity:1}},{{opacity:0,duration:.25,ease:'none'}},0);"
                 f"tl.to('#xfade',{{opacity:1,duration:.25,ease:'none'}},{max(0, total_dur - 0.25):.3f});")
    return f"""<!doctype html>
<html lang="zh-CN">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width={w}, height={h}" />
    <script src="assets/gsap.min.js"></script>
    <script src="assets/hf.js"></script>
{katex_head}{vend_head}    <style>
      * {{ margin: 0; padding: 0; box-sizing: border-box; }}
      html, body {{ width: {w}px; height: {h}px; overflow: hidden; background: #000; }}
      #root {{ position: relative; width: {w}px; height: {h}px; overflow: hidden; background: {slots.get('bg') or '#FDF6E3'}; }}
      .scene {{ position: absolute; inset: 0; width: 100%; height: 100%; }}
      {fontface}
    </style>
  </head>
  <body>
    <div id="root" data-composition-id="main" data-start="0" data-duration="{total_dur}" data-width="{w}" data-height="{h}">
{body}
{ovl_body}
{cap_body}
{xf_body}    </div>
    <script>
      window.__timelines = window.__timelines || {{}};
      window.__timelines["main"] = gsap.timeline({{ paused: true }});
      (function () {{ var tl = window.__timelines["main"];
        tl.to({{}}, {{ duration: {total_dur} }}, 0);
        {ovl_js}
        {cap_js}
        {xf_js}
      }})();
    </script>
  </body>
</html>
"""

total = meta['total']; adur = voice['duration_s']
scenes = [('para01', 0, total, adur)]
cap_body, cap_js = assemble.subtitle_blocks(scenes, [voice], slots, proj, W, H)
seg_html = project_html(W, H, total, scenes_html(scenes), '', '', cap_body, cap_js,
                        xfade=False, vend_head=assemble.vend_head_for(proj, ['para01']))
src_fp = hashlib.sha1()
for ref in (proj / 'compositions' / 'frames' / 'para01.html',
            proj / 'compositions' / 'caps' / 'cap-para01.html'):
    if ref.exists():
        src_fp.update(ref.read_bytes())
seg_html += f'\n<!-- src-fp:{src_fp.hexdigest()[:16]} -->\n'
(proj / '.hf-seg').mkdir(exist_ok=True)
assemble.write_atomic(proj / '.hf-seg' / 'para01.html', seg_html)
print('para01 project html ok, total', total)
`

func TestParaContest(t *testing.T) {
	srcID := os.Getenv("PARA_CONTEST")
	if srcID == "" {
		t.Skip("未指定 PARA_CONTEST=<项目ID>，跳过段落制比武")
	}
	root, _ := filepath.Abs(filepath.Join("..", "..", ".."))
	if err := godotenv.Load(filepath.Join(root, ".env")); err != nil {
		t.Logf("（无 .env，用进程环境变量）: %v", err)
	}
	pipeline.SetRootDir(root)
	pipeline.SetDataDir(filepath.Join(root, "data"))
	pipeline.EnsureFFmpeg()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()

	// ── 沙盒：data/paracontest/<id>/（copy 工件不 copy 渲染产物）
	src := pipeline.NewProject(filepath.Join(root, "data"), srcID)
	sandbox := filepath.Join(root, "data", "paracontest")
	p := pipeline.NewProject(sandbox, srcID)
	for _, rel := range []string{"project.json", "meta.json", "package.json", "audio_meta.json",
		"storyboards", "style", "scripts", "assets", "audio"} {
		if err := copyTree(filepath.Join(src.Dir, rel), filepath.Join(p.Dir, rel)); err != nil {
			t.Fatalf("拷 %s: %v", rel, err)
		}
	}
	for _, d := range []string{"compositions/frames", "compositions/caps", "compositions/registry",
		"llm", "renders/segs", "reports", "reviews"} {
		os.MkdirAll(filepath.Join(p.Dir, d), 0o755)
	}
	// 沙盒只用段落1 的三条 wav，其余清掉省地方
	keep := map[string]bool{"seg01.wav": true, "seg02.wav": true, "seg03.wav": true}
	entries, _ := os.ReadDir(filepath.Join(p.Dir, "audio"))
	for _, e := range entries {
		if !keep[e.Name()] {
			os.Remove(filepath.Join(p.Dir, "audio", e.Name()))
		}
	}

	sb, err := src.LoadStoryboard()
	if err != nil {
		t.Fatal(err)
	}
	voices, err := loadAudioMeta(src)
	if err != nil {
		t.Fatal(err)
	}
	// ── 段落1 = seg01–03，网格 = Σ(dur+SegTail)
	var cells []paraCell
	at := 0.0
	for _, seg := range sb.Segments {
		if seg.ID != "seg01" && seg.ID != "seg02" && seg.ID != "seg03" {
			continue
		}
		v := voices[seg.ID]
		cells = append(cells, paraCell{seg: seg, v: v, start: at, end: at + v.DurationS + contract.SegTail})
		at += v.DurationS + contract.SegTail
	}
	if len(cells) != 3 {
		t.Fatalf("段落1 应 3 分镜，得到 %d", len(cells))
	}
	// 合并词表/短语（段落绝对时间）+ 沙盒 meta 给 python
	merged := voiceMeta{ID: "para01", DurationS: at - contract.SegTail}
	var phrases [][2]int
	var phrasesEn []string
	wOff, idxOff := 0.0, 0
	for _, c := range cells {
		for _, w := range c.v.Words {
			merged.Words = append(merged.Words, wordTiming{Text: w.Text, Start: w.Start + wOff, End: w.End + wOff})
		}
		_ = idxOff
		idxOff += len(c.v.Words)
		wOff += c.v.DurationS + contract.SegTail
	}
	// phrases：从源 audio_meta 原文读（voiceMeta 不带 phrases，直读 json）
	rawMeta := map[string]map[string]any{}
	if b, err := os.ReadFile(filepath.Join(p.Dir, "audio_meta.json")); err == nil {
		var m struct {
			Voices []map[string]any `json:"voices"`
		}
		if json.Unmarshal(b, &m) == nil {
			for _, v := range m.Voices {
				rawMeta[v["id"].(string)] = v
			}
		}
	}
	off := 0
	for _, c := range cells {
		if rm := rawMeta[c.seg.ID]; rm != nil {
			if ps, ok := rm["phrases"].([]any); ok {
				for _, p := range ps {
					rng := p.([]any)
					i0, _ := rng[0].(float64)
					i1, _ := rng[1].(float64)
					phrases = append(phrases, [2]int{int(i0) + off, int(i1) + off})
				}
			}
			if en, ok := rm["phrases_en"].([]any); ok {
				for _, e := range en {
					if s, ok := e.(string); ok && s != "" {
						phrasesEn = append(phrasesEn, s)
					}
				}
			}
		}
		off += len(c.v.Words)
	}
	// python 元数据：格子键名按组装器的口径（sid/narr_dur）
	cellMaps := make([]map[string]any, len(cells))
	for i, c := range cells {
		cellMaps[i] = map[string]any{"sid": c.seg.ID, "start": c.start, "end": c.end, "narr_dur": c.v.DurationS}
	}
	metaJSON, _ := json.Marshal(map[string]any{
		"w": 1920, "h": 1080, "total": at, "voice": map[string]any{
			"id": "para01", "duration_s": at - contract.SegTail,
			"words": merged.Words, "phrases": phrases, "phrases_en": phrasesEn,
		},
		"cells": cellMaps,
	})
	os.WriteFile(filepath.Join(p.Dir, "contest-meta.json"), metaJSON, 0o644)

	// ── 作业书（§5.1 手工版）+ 工位机器
	prov, perr := llm.FromEnv(llm.StationSceneGen)
	if perr != nil {
		t.Fatalf("需要 API 模式: %v", perr)
	}
	cv := ProjectCanvas(p)
	golden := goldenExample(root)
	designShared, _ := designNotesFor(p, 0)
	styleShared := sampleStyleNote(p) + designShared
	sceneM, compM := methodsFor(root, cells[0].seg)
	r := &Runner{DataDir: filepath.Join(root, "data"), RootDir: root, Hub: events.NewHub()}
	rules := "- 内容安全区 x∈[0,1920]、y∈[0,896]；底部 896px 起是平台字幕带，禁放内容\n" +
		"- 顶部两角是平台角标保留区：内容避开左上(x<500,y<150)与右上(x>1420,y<150)两个角域\n" +
		"- 横屏 1920×1080：构图自己按本段画面与设计系统定——主体尺寸、位置、层次都由你编排"
	paraTitle := "破题：说明书之问，引出存在主义与萨特、加缪"

	type payloadT struct {
		HTML     string                  `json:"html"`
		Elements []contract.NamedElement `json:"elements"`
	}

	// 首写 + 契约修复轮（≤2，同工位口径）。
	// PARA_CONTEST_KEEP=1 复用沙盒里已有的 comp-para01.json（中断续跑不重付 LLM 钱；
	// 「一次过」指标以首跑日志为准，复用轮只续 check→渲染→段审链）。
	var payload payloadT
	reuse := os.Getenv("PARA_CONTEST_KEEP") == "1"
	lintRounds := -1
	totalPrompt, totalCompl := 0, 0
	user := paraContestPrompt(cv, at, paraTitle, cells, merged, styleShared, golden, rules, sceneM, compM)
	if reuse {
		if b, err := os.ReadFile(p.LLMOutput("comp-para01")); err == nil && json.Unmarshal(b, &payload) == nil && payload.HTML != "" {
			lintRounds = -2 // 复用：首写指标看上一次跑的日志
			t.Log("复用沙盒已生成的 comp-para01.json（不重付 LLM）")
		}
	}
	if lintRounds == -1 {
		callCtx, cc := context.WithTimeout(ctx, 12*time.Minute)
		u, gerr := prov.GenerateJSONNoThink(callCtx, sceneSystemFor(), user, &payload)
		cc()
		if gerr != nil {
			t.Fatalf("首写失败: %v", gerr)
		}
		totalPrompt, totalCompl = u.PromptTokens, u.CompletionTokens
		lintRounds = 0
	}
	errs := sceneValidate(r, ctx, p, payload.HTML, "para01")
	for repair := 0; repair < 2 && len(errs) > 0; repair++ {
		lintRounds++
		t.Logf("契约第 %d 轮未过（%d 条）：\n- %s", repair+1, len(errs), strings.Join(errs, "\n- "))
		fb := "上一版场景文件被契约检查拒绝，必须逐条修正：\n- " + strings.Join(errs, "\n- ")
		callCtx, cc := context.WithTimeout(ctx, 12*time.Minute)
		u2, err2 := prov.GenerateJSONNoThink(callCtx, sceneSystemFor(),
			user+"\n\n## 修复要求（最高优先级）\n"+fb, &payload)
		cc()
		totalPrompt += u2.PromptTokens
		totalCompl += u2.CompletionTokens
		if err2 != nil {
			t.Fatalf("契约修复轮失败: %v", err2)
		}
		errs = sceneValidate(r, ctx, p, payload.HTML, "para01")
	}
	if len(errs) > 0 {
		t.Fatalf("契约三轮未过：\n- %s", strings.Join(errs, "\n- "))
	}
	if len(payload.Elements) < 3 {
		t.Fatalf("元素注册表不足 3 条")
	}
	verdict := map[int]string{0: "一次过", -2: "复用（首跑一次过）"}[lintRounds]
	if verdict == "" {
		verdict = fmt.Sprintf("过（%d 修复轮）", lintRounds)
	}
	t.Logf("★ 契约%s，prompt=%d compl=%d tokens", verdict, totalPrompt, totalCompl)

	// ── 落产物 + 组装 + 渲染
	b, _ := json.MarshalIndent(payload, "", "  ")
	pipeline.WriteFileAtomic(p.LLMOutput("comp-para01"), b, 0o644)
	if err := segCompositions(p, "para01"); err != nil {
		t.Fatal(err)
	}
	pipeline.ClampSegmentFonts(p, "para01")
	pipeline.FixupSegmentFrame(p, "para01")
	pipeline.ClampSegmentFrame(p, "para01")
	builder := filepath.Join(sandbox, "build_para.py")
	os.WriteFile(builder, []byte(paraContestBuilder), 0o644)
	if out, err := exec.CommandContext(ctx, "python", builder, p.Dir, filepath.Join(root, "ai")).CombinedOutput(); err != nil {
		t.Fatalf("python 组装失败: %v\n%s", err, string(out))
	} else {
		t.Log(string(out))
	}
	if err := pipeline.RenderSeg(p, "para01"); err != nil {
		t.Fatalf("段落渲染失败: %v", err)
	}
	dur := pipeline.SegVideoDuration(p, "para01")
	t.Logf("★ 段落片渲染完成 %.2fs（网格 %.2fs）", dur, at)
	if d := dur - at; d > 0.5 || d < -0.5 {
		t.Errorf("段落片时长与网格偏差 %.2fs", d)
	}

	// ── 机检（check 单段子工程 + 回放/接缝闸）
	if err := ensureSegCheckDir(p, "para01"); err != nil {
		t.Fatal(err)
	}
	out, err := pipeline.RunCLI(p, "npx", pipeline.HyperframesPkg, "check", ".hf-check/para01", "--no-browser-gpu")
	if err != nil && blockingCheckErrors(out) == "" && checkRanSections(out) {
		t.Log("机检：诊断全是已摘闸美术类，放行")
	} else if err != nil {
		t.Logf("★ 机检有 blocking 项（喂报告，实验记录不修复）：\n%s", pipeline.TailLines(blockingCheckErrors(out), 15))
	} else {
		t.Log("★ 机检过")
		if rerr := r.segReplayCheck(ctx, p, "para01", dur); rerr != nil {
			t.Logf("★ 回放门禁: %v", rerr)
		}
		if serr := r.segSeamCheck(ctx, p, "para01", dur); serr != nil {
			t.Logf("★ 接缝闸: %v", serr)
		}
	}

	// ── 段审（同一判卷服务，锚点=3 分镜全部句末+段落首末帧）
	// 句编号全局重排：各分镜句号独立起算（每段 N1..），三段拼接会撞键——
	// 段落级判卷按段落连续编号（N1..Nk），锚点标注含句文本不受影响。
	script, serr := prep.LoadScript(p)
	if serr != nil {
		t.Fatal(serr)
	}
	var sentences []contract.Sentence
	ends := map[string]float64{}
	gN := 0
	for _, c := range cells {
		sents := segSentences(script, c.seg)
		se := sentenceEndTimes(sents, c.v.Words)
		for _, s := range sents {
			gN++
			ns := s
			ns.ID = fmt.Sprintf("N%d", gN)
			if t, ok := se[s.ID]; ok {
				ends[ns.ID] = t + c.start
			}
			sentences = append(sentences, ns)
		}
	}
	sp := loadStylePackFor(t, p)
	in := review.SegInput{
		Seg:         contract.Segment{ID: "para01", Key: paraTitle, Narration: joinNarration(cells), VisualBrief: joinBriefs(cells), Idx: 1},
		Sentences:   sentencePairsOf(sentences),
		Facts:       concatFacts(script, cells),
		Rules:       designRulesDigest(sp),
		Samples:     visRefs(styleSampleImages(p)),
		Dur:         dur,
		Round:       1,
		SourceText:  segSourceText(p, "para01"),
		AnchorTimes: anchorFrames(sentences, ends, dur),
		LogPath:     p.Artifact("reviews/para-audit.tools.jsonl"),
	}
	svc := &review.Service{P: p}
	v, aerr := svc.AuditSegment(ctx, in)
	if aerr != nil {
		t.Fatalf("段审失败: %v", aerr)
	}
	vb, _ := json.MarshalIndent(v, "", "  ")
	os.WriteFile(p.Artifact("reviews/seg-para01-r1.json"), vb, 0o644)
	t.Logf("★ 段审判卷：dims=%v", v.Dims)
	for _, f := range v.Findings {
		t.Logf("  [%s/%s@%s] %s", f.Severity, f.Category, f.Anchor, firstRunes(f.Evidence, 60))
	}

	// ── 基线对照摘要（原项目 seg01–03 的会话史与判卷）
	for _, id := range []string{"seg01", "seg02", "seg03"} {
		hist := map[string]any{}
		if b, err := os.ReadFile(filepath.Join(src.Dir, "llm", "comp-"+id+".history.json")); err == nil {
			json.Unmarshal(b, &hist)
			msgs, _ := hist["messages"].([]any)
			t.Logf("基线 %s：会话 %d 条消息（2=首写即成，>2=含修复轮）", id, len(msgs))
		}
		for r := 1; r <= 3; r++ {
			fp := filepath.Join(src.Dir, "reviews", fmt.Sprintf("seg-%s-r%d.json", id, r))
			if b, err := os.ReadFile(fp); err == nil {
				var base struct {
					Dims     map[string]string `json:"dims"`
					Findings []struct {
						Severity string `json:"severity"`
					} `json:"findings"`
				}
				if json.Unmarshal(b, &base) == nil {
					t.Logf("基线 %s 段审 r%d：dims=%v findings=%d", id, r, base.Dims, len(base.Findings))
				}
			}
		}
	}
	t.Logf("== 比武产物目录 %s（renders/segs/para01.mp4 段落片；reviews/ 判卷）==", p.Dir)
}

// ── 小工具 ─────────────────────────────────────────────

func copyTree(src, dst string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		b, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		os.MkdirAll(filepath.Dir(dst), 0o755)
		return os.WriteFile(dst, b, 0o644)
	}
	os.MkdirAll(dst, 0o755)
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := copyTree(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func loadStylePackFor(t *testing.T, p *pipeline.Project) *contract.StylePack {
	t.Helper()
	b, err := os.ReadFile(p.Artifact("style/stylepack.json"))
	if err != nil {
		return nil
	}
	var sp contract.StylePack
	if json.Unmarshal(b, &sp) != nil {
		return nil
	}
	return &sp
}

func sentencePairsOf(sents []contract.Sentence) [][2]string {
	out := make([][2]string, 0, len(sents))
	for _, s := range sents {
		out = append(out, [2]string{s.ID, s.Text})
	}
	return out
}

func concatFacts(script *contract.Script, cells []paraCell) []string {
	var out []string
	for _, c := range cells {
		out = append(out, segFacts(script, c.seg)...)
	}
	return out
}

func joinNarration(cells []paraCell) string {
	var parts []string
	for _, c := range cells {
		parts = append(parts, c.seg.Narration)
	}
	return strings.Join(parts, " ")
}

func joinBriefs(cells []paraCell) string {
	var parts []string
	for i, c := range cells {
		parts = append(parts, fmt.Sprintf("分镜%d（%.1f–%.1fs）：%s", i+1, c.start, c.end, c.seg.VisualBrief))
	}
	return strings.Join(parts, "\n")
}

func firstRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
