package produce

// sceneir_prompt.go —— SceneIR 生成/修复提示词（2026-10-30 试点）。
//
// 与 sceneUserPrompt 的区别：画面表达仍由模型自由写 HTML/CSS，但时间轴写成
// cues 表；平台编译成 HTML/GSAP。提示词刻意不引入「标题/卡片/三栏」等版式词，
// 避免退化成模板。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	openai "github.com/sashabaranov/go-openai"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/pipeline"
)

func sceneIROn(p *pipeline.Project) bool {
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("SCENE_IR"))); v == "1" || v == "true" || v == "on" {
		return true
	}
	return strings.TrimSpace(pipeline.ReadProjectField(p, "scene_ir")) == "1"
}

func sceneIRSystem() string {
	return "你是视频的画面导演兼工程师。你交付 SceneIR：每个元素的 HTML/CSS 由你自由设计，" +
		"时间轴写成 cues 表；平台的确定性编译器负责生成 <template> 与 GSAP timeline。" +
		"不要输出整页 HTML，不要手写 gsap 调用。只输出 JSON。"
}

// sceneIRUserPrompt 首写/整段重画的 IR 作业书。
func sceneIRUserPrompt(seg contract.Segment, v voiceMeta, instruction, feedback string, cv contract.Canvas,
	styleShared, styleSeg, paraNote string) string {
	var wb strings.Builder
	for i, w := range v.Words {
		if i > 0 && i%10 == 0 {
			wb.WriteString("\n")
		}
		fmt.Fprintf(&wb, "%d:%s@%.1fs ", i, w.Text, w.Start)
	}
	total := v.DurationS + contract.SegTail
	paraLine := ""
	if paraNote != "" {
		paraLine = "- 段落定位：" + paraNote + "\n"
	}
	instrBlock := ""
	if instruction != "" {
		instrBlock = "\n## 用户修改要求（最高优先级，逐字落实）\n" + instruction + "\n"
	}
	fbBlock := ""
	if feedback != "" {
		fbBlock = "\n## 上一版问题与修复要求（逐条修正，其余保持原样）\n" + feedback + "\n"
	}
	rules := fmt.Sprintf(`- 内容安全区 x∈[0,%d]、y∈[0,%d]；底部 %dpx 起是平台字幕带，禁放内容
- 顶部两角是平台角标保留区：左上/右上各留一块空白
- 画幅 %d×%d：构图自己定，主体尺寸/位置/层次都由你编排`, int(cv.W), int(cv.Y1), int(cv.CapTop), int(cv.W), int(cv.H))

	return fmt.Sprintf(`为这一段视频设计 SceneIR（平台编译成可渲染场景）。

## 你控制什么
- 每个元素的 HTML/CSS：版式、配色、字体、SVG/Canvas/WebGL 图形、质感，全部你写。平台不提供版式槽位、不套组件样式、不自动排版。
- box 已经决定图层的位置/尺寸：元素 CSS **不要**给 #元素id 写 position/left/top/transform（会和 box/GSAP 冲突）。层内居中用 flex/margin/子元素布局；图层运动全部写 cues。
- cues 时间表：元素何时进、何时动、何时退。平台把它编译成唯一一条 paused GSAP timeline。
- extra_js 只留给 canvas/WebGL 的 draw(t) 接线；没有 canvas 就留空。

## 硬约束（违反会被 lint 拒收）
- elements[].html 是片段：不要 <template>/<html>/<head>/<body>/<script>/<style>；不要写元素自身的外层 id（平台会包一层 #<id>）。
- 每个元素必须有 name（人话名，至少 3 个元素）；html 不能为空。
- 字体只用渲染端已有的：微软雅黑/黑体/宋体/楷体/仿宋/Consolas/Segoe UI/Georgia/Arial/Times New Roman/Courier New/Impact，或设计系统注入的展示字体；字体栈里不要出现 PingFang/思源/Noto。
- 禁网络请求、外链资源、<video>/<audio>；图片只能用 assets/img/ 下的本地素材，没有就不用。
- 确定性：禁 Date.now/new Date/performance.now/setTimeout/setInterval/requestAnimationFrame/Math.random；canvas 用注入的种子 rng。
- 禁 CSS animation/transition/@keyframes；运动全部写 cues（或 canvas 的 f(t)）。
- 首帧即景：t=0 背景与主体已在画面，不要整页从黑/白淡入；末帧定格在内容帧，不要整页淡出收黑。
- 不写字幕；底部字幕带由平台注入；不要出现 data-ph=/data-w=。
- 内容忠实：画面提示里的主体/动作/首末拍必须可见；旁白点到的实体必须可辨认；画面文字与旁白/提示一致，不自创数据。
- 画面文字只放观众该读的内容；字卡提炼关键词，不要逐字重复旁白整句。

## 动效手感
- 入场按旁白节拍分布，不要所有元素同秒砸入；相邻信息点之间要有快慢变化。
- 至少两种入场手法；次要元素跟随主元素滞后 0.1–0.3s；结尾该退的元素提前退完、留下的停稳。
- 位移/缩放/旋转用 x/y/scale/rotation 补间（不要补 left/top）；居中用 xPercent/yPercent，不要用 CSS transform 居中再让 GSAP 补间。
- 禁 ease:"none" 的裸入场；canvas 内运动把缓动写进 f(t) 包络。

## 设计系统与风格（全片共享）
%s
%s

## 布局与安全区
%s

## 本段信息
- 段 id（data-composition-id 与 timeline 注册逐字用它）：%s
- 标题：%s
%s- 旁白（%d 词，%.1f 秒）：%s
- 段总时长：%.3f 秒（cues 的 at/dur 都以此为上限；含 %.2fs 收尾垫）
- 画面提示：%s
%s%s
## 词序表（cue 的 at 对齐用；时刻=段内绝对秒）
%s

## SceneIR 结构
{
  "background": "舞台底色（CSS 颜色/渐变，可空）",
  "css": "本段全局 CSS（可选；选择器自己写，建议以 #元素id 开头）",
  "elements": [
    {
      "id": "字母开头，仅字母数字_-，全段唯一",
      "name": "人话名（data-hf-name）",
      "html": "该元素的 HTML 片段（自由写；不要外层 id）",
      "css": "该元素的 CSS（可选）",
      "box": {"x": 0, "y": 0, "w": %d, "h": %d},
      "start": 0,
      "duration": %.3f,
      "track": 0,
      "z": 0
    }
  ],
  "cues": [
    {"target": "#元素id", "kind": "fromTo", "from": {"opacity": 0, "y": 24}, "to": {"opacity": 1, "y": 0}, "at": 0.3, "dur": 0.6, "ease": "power2.out"},
    {"target": "#元素id", "kind": "to", "to": {"rotation": 8}, "at": 1.2, "dur": 1.2, "ease": "sine.inOut"}
  ],
  "extra_js": "仅 canvas/WebGL 用；否则空字符串"
}

## 时间表规则
- kind：fromTo（从 from 到 to）/ to（从当前状态到 to）/ set（瞬间设置）。
- at/dur：段内绝对秒，0 起；at+dur 不得超出段总时长。
- target：#元素id，或 #元素id .子选择器（元素内部子节点）。
- to/from 的值用数字/字符串/布尔/数组，不要嵌套对象。
- 每个元素至少一条入场 cue；元素退场用 opacity/scale/x 补间，不要删元素。

## 输出（只输出一个 JSON 对象，第一个字符 {，最后一个 }，前后无解释/围栏）
{"note":"画面思路一句话","ir":{"background":"#...","css":"...","elements":[...],"cues":[...],"extra_js":""}}
`, styleShared, styleSeg, rules, seg.ID, seg.Key, paraLine, len(v.Words), v.DurationS, seg.Narration, total, contract.SegTail,
		seg.VisualBrief, instrBlock, fbBlock, wb.String(), int(cv.W), int(cv.H), total)
}

// irRepairPrompt 修复轮：只回改动单，不重抄文件。
func irRepairPrompt(ir *contract.SceneIR, issues []string, segDur float64) string {
	b, _ := json.Marshal(ir)
	irJSON := string(b)
	if len(irJSON) > 40000 {
		irJSON = irJSON[:40000] + "\n…（已截断；只按问题清单定位）"
	}
	return fmt.Sprintf(`下面是这一段的 SceneIR（画面元素 + 时间 cue）与检查/审片问题清单。
只输出改动单，不要重抄整个 IR，不要改与问题无关的画面。

## 问题清单（逐条修；每条都必须有对应改动）
- %s

## 可用 op
- {"op":"set_cue","target":"#元素id","prop":"主属性名","at":1.2,"dur":0.6,"ease":"power2.out","to":{...},"from":{...},"kind":"fromTo|to|set"}
  改已有 cue 的时间/值；同 target 同 prop 的第一条会被改。找不到且给了 to 时新增。
- {"op":"add_cue","cue":{"target":"#元素id","kind":"to","to":{...},"at":1.2,"dur":0.6}}
- {"op":"remove_cue","target":"#元素id","prop":"opacity"}
- {"op":"set_element","target":"#元素id","element":{"html":"...","css":"...","name":"...","box":{...}}}
  改元素内容/样式；只覆盖给出的字段（html/css/name/box），时间轴不受影响。
- {"op":"add_element","element":{"id":"...","name":"...","html":"...","box":{...}}}
- {"op":"remove_element","target":"#元素id"}

## 规则
- 时间错误（出现晚/早、没淡入、停留太短/太长）优先用 set_cue 改 at/dur/ease/to，不要重写元素。
- 文字/配色/布局错误用 set_element 改 html/css；不要动别的元素。
- 不要删除元素来掩盖问题；不要改 target 的 id。
- 改动后必须仍然满足：目标存在、at/dur 在 0–%.3f 内、同元素同属性不冲突、至少 3 个元素。
- 只输出 JSON：{"note":"一句话","ops":[...]}

## 当前 SceneIR
%s
`, strings.Join(issues, "\n- "), segDur, irJSON)
}

// repairSceneIR 调模型产出改动单并套用；失败返回错误，由调用方回退整份重写。
func repairSceneIR(ctx context.Context, prov *llm.Provider, ir *contract.SceneIR, issues []string, segDur float64, rec *llm.Transcript) (*contract.SceneIR, openai.Usage, error) {
	var out struct {
		Note string      `json:"note"`
		Ops  []SceneIROp `json:"ops"`
	}
	u, err := prov.GenerateJSONNoThink(ctx, sceneIRSystem(), irRepairPrompt(ir, issues, segDur), &out)
	if err != nil {
		return nil, u, err
	}
	if len(out.Ops) == 0 {
		return nil, u, fmt.Errorf("修复输出没有 ops（note=%q）", out.Note)
	}
	next, err := ApplySceneIROps(ir, out.Ops)
	if err != nil {
		return nil, u, err
	}
	// 结构校验用占位画幅（这里只查元素/cue 结构与时间，画幅由编译期按项目画布再查）。
	if verrs := contract.ValidateSceneIR(next, segDur, 1, 1); len(verrs) > 0 {
		return nil, u, fmt.Errorf("改动单套用后 IR 校验未过：%s", strings.Join(verrs, "；"))
	}
	if rec != nil {
		rec.Note("IR 改动单：%d 条（%s）", len(out.Ops), out.Note)
	}
	return next, u, nil
}

// loadSceneIR 读上一轮落盘的 IR（llm/comp-<seg>.json 的 ir 字段）。没有/解析失败
// 返回 nil——调用方按 HTML 场景处理。
func loadSceneIR(p *pipeline.Project, segID string) *contract.SceneIR {
	b, err := os.ReadFile(p.Artifact("llm/comp-" + segID + ".json"))
	if err != nil {
		return nil
	}
	var payload struct {
		IR *contract.SceneIR `json:"ir"`
	}
	if json.Unmarshal(b, &payload) != nil || payload.IR == nil {
		return nil
	}
	return payload.IR
}
