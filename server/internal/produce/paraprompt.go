package produce

// paraprompt.go —— 段落制作业书（2026-10-29 换骨，para-transition-plan §5.1）。
// 比武验证版（para_contest_test.go）的生产化：同一结构 + 用户修改/修复轮/底稿
// 块（与 sceneUserPrompt 同语义）+ 观感补丁（比武短板：边界处旧元素优先保留
// 变形，避免整体退场留空台）。段落制不走素材申请（两段式）——v1 简化，比武
// 基线亦无素材。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/pipeline"
)

func paraUnitPrompt(seg contract.Segment, v voiceMeta, u contract.RenderUnit, units []contract.RenderUnit,
	instruction, feedback string, cv contract.Canvas, styleShared, styleSeg, golden, sceneTpl, compMethods, prevHTML string) string {

	var wb strings.Builder
	for i, w := range v.Words {
		if i > 0 && i%10 == 0 {
			wb.WriteString("\n")
		}
		fmt.Fprintf(&wb, "%d:%s@%.1fs ", i, w.Text, w.Start)
	}
	var cellLines strings.Builder
	for i, c := range u.Segs {
		fmt.Fprintf(&cellLines, "- 分镜%d %s 格子 [%.3f, %.3f]（旁白 %.2fs + 收尾垫 %.2fs）\n  标题：%s\n  旁白：%s\n  画面提示：%s\n",
			i+1, c.ID, c.Start, c.End, c.NarrDur, contract.SegTail, c.Key, c.Narration, c.Brief)
	}
	paraTotal := 0
	for _, x := range units {
		if x.ParaIdx > paraTotal {
			paraTotal = x.ParaIdx
		}
	}
	paraTotal++
	splitNote := ""
	if u.Parts > 1 {
		splitNote = fmt.Sprintf("；本段落因超长被拆成 %d 个渲染单元，这是第 %d 单元——与相邻同段落单元是同一舞台的延续，开头/收尾不要做「另起一页」处理", u.Parts, u.Part)
	}
	rules := "- 内容安全区 x∈[0,1080]、y∈[0,1594]；底部 1594px 起是平台字幕带，禁放内容\n" +
		"- 顶部两角是平台角标保留区：内容避开左上(x<280,y<150)与右上(x>800,y<150)两个角域\n" +
		"- 竖屏 1080×1920：构图自己按本段画面与设计系统定"
	if cv.Aspect == "16:9" {
		rules = "- 内容安全区 x∈[0,1920]、y∈[0,896]；底部 896px 起是平台字幕带，禁放内容\n" +
			"- 顶部两角是平台角标保留区：内容避开左上(x<500,y<150)与右上(x>1420,y<150)两个角域\n" +
			"- 横屏 1920×1080：构图自己按本段画面与设计系统定"
	}
	instrBlock := ""
	if instruction != "" {
		instrBlock = "\n## 用户修改要求（最高优先级——这是重画指令，与「各分镜信息」里的原画面提示冲突时一律以用户要求为准，逐字落实）\n" + instruction + "\n"
	}
	prevBlock := ""
	if prevHTML != "" {
		prevBlock = "\n## 上一版底稿（修复轮：看得见旧稿才谈得上定点修改；只改要求点，其余保持原样）\n```html\n" + prevHTML + "\n```\n"
	}
	fbBlock := ""
	if feedback != "" {
		fbBlock = "\n## 上一版问题与修复要求\n" + feedback + "\n"
	}
	return fmt.Sprintf(`为这一个段落的视频直接写场景文件 HTML（你是画面工程师，版式/动效/图形程序全部自己写）。
本作业与单段作业的唯一区别：你拿到的是一整个段落（%d 个分镜）的连续时间轴——
一张 HTML、一条 timeline，从段落第一帧编排到最后一帧。
分镜之间不是「换页」，是同一舞台的持续推进：过渡由你在时间轴里编排出来。

## 契约摘要（完整版 docs/contract.md；违反任意一条会被 lint 拒收）
1. 整个文件 = 一个 <template> 片段，无 DOCTYPE/html/head/body
2. 根元素 data-composition-id="%s"（逐字）data-width="%d" data-height="%d"；内部 #root 为舞台
3. 恰好一个 gsap.timeline({paused:true})，末尾注册 window.__timelines["%s"]
4. 全屏轨道层（背景等）带 class="clip" + data-start/data-duration/data-track-index
5. 每个有意义的可见元素带 data-hf-name="人话名"（≥3 个；元素级指认依赖它）
8. 字体：只准用渲染端实际安装的这些——微软雅黑 Microsoft YaHei、黑体 SimHei、宋体 SimSun、楷体 KaiTi、仿宋 FangSong、Consolas、Segoe UI、Georgia、Arial、Times New Roman、Courier New、Impact（外加 serif/sans-serif/monospace 通用族收尾）；
   例外：注入的设计系统展示字体可以直接用，平台保证声明与字体文件；
   引用即声明（@font-face{font-family:'X';src:local('X');}）；禁 PingFang SC、思源 Source Han/Noto 等未安装字体——lint 对未声明字体会一票否决整段；
   禁止在 CSS transform 里做居中再让 GSAP 补间同一元素——居中必须用 GSAP 的 xPercent/yPercent；
   元素的初始缩放/位移状态一律写进 GSAP 的 fromTo from 值，CSS 里禁写 transform 声明——lint 对 CSS transform 与 GSAP 补间同元素一票否决
9. 不写字幕——底部字幕带由平台注入；内容留在上 83%%，禁止出现 data-ph=/data-w= 标注
10. 元素揭示随旁白节拍分布（对齐文末词序表——时刻是段落绝对时间，不前载不拖尾）
11. 需要新框架：在 <template> 内写注释声明 <!-- vend: 包名@精确版本 -->，
    平台自动采购并注入（模型不写任何 <script> 标签）
12. 首帧即景——段落 t=0 背景与主体场景已在画面，禁整页从黑场/纯色空白淡入开场；
    末帧定格——段落结尾定格在内容帧，禁整页淡出到黑/白（两条针对整个段落的首尾；
    段落内部的分镜边界不受此约束——那里要的是连续编排不是定格）

## 时间轴网格（本段落的骨架——旁白贴格、画面穿格）
- 段落总时长 %.3f 秒 = %d 个分镜格子拼接；timeline 全长用它，格子边界如下
%s- 每个格子的旁白按词级时间戳贴合（见文末词序表，时刻已换算成段落绝对时间）
- 画面动画不受格子边界约束：同一舞台元素可以跨格子持续演进、运镜跨分镜延续——
  格子只是旁白的家，不是画面的墙；信息节拍跟旁白走（旁白每转一个信息点画面响应）
- 分镜之间的过渡由你自己编排（同舞台元素推进/运镜转折/构图重组/焦点点换），
  禁止把格子边界做成「换页」感：禁整页淡出再淡入、禁背景整层换掉重来——
  背景与舞台装置是段落级连续的，变的是前景内容与焦点；
  边界处旧元素优先保留变形（缩小/上移/变淡成新场景的引言或角标），避免整体
  退场留空台——空台一瞬间就是「分段感」的来源（2026-10-29 比武实测短板）

## 段落定位
- 第 %d/%d 段落「%s」·全段 %d 分镜%s（段落之间才是话题切换，本文件内部全是同一话题的延续）

%s
## 结构金样例（结构/时序/canvas 写法照这个模式；视觉内容必须按本段重写；源码从下一行起到「布局规则」前为止）
%s
## 布局规则
%s
- 每屏至少一个可辨认的视觉重心；禁止全屏无差别小元素平铺；段落适合铺氛围时优先写背景层（渐变/纹理/程序化画布），别停在纯色底
- 场景里的每一条可见文字都必须是观众该读的内容；字卡只提炼关键词/短语，不得逐字重复旁白句

## 动效手感（防呆板——节奏与编排的硬要求，手法本身自由）
- 入场手法从设计系统「入场手法菜单」取用并轮换：本段落至少三种手法、相邻分镜主手法不重复
- 旁白每转一个信息点画面必须响应；入场完成后静坐到段尾=PPT，打回
- 禁所有元素同秒砸入：入场错峰对齐旁白短语节拍，间隔有快慢变化
- 扫屏条/横移/滑轨类位移一律用 x/y transform 补间，禁补间 left/top
- 禁 ease:"none" 的平移/淡入裸入场（canvas 驱动线不受此限）：入场要有性格
- 次要元素跟随主元素滞后 0.1–0.3s 入场，成组出现要有主次
- 分镜边界附近是编排重点：下个分镜的首个信息点入场前，先用运镜/强调把视线引过去
%s
## 各分镜信息（格子边界与画面要点；契约/设计/样张/金样例为全段共享）
%s
## 词序表（reveal 对齐用；时刻=段落绝对时间，跨分镜连续编号）
%s
%s%s%s
## 输出（只输出一个 JSON 对象：第一个字符必须是 {、最后一个字符必须是 }，前后不许有任何字、标签、围栏或解释）
{"note":"画面思路一句话","html":"<template>…整个段落的场景文件…</template>","elements":[{"id":"元素DOM id","name":"人话名（与 data-hf-name 一致）"}]}
`, len(u.Segs), seg.ID, int(cv.W), int(cv.H), seg.ID,
		u.Dur, len(u.Segs), cellLines.String(),
		u.ParaIdx+1, paraTotal, plainTitle(u), len(u.Segs), splitNote,
		styleShared, golden, rules,
		styleSeg, seg.VisualBrief, wb.String(),
		instrBlock, prevBlock, fbBlock)
}

// plainTitle 段落主题（拆分注剥掉——定位行用原始主题）。
func plainTitle(u contract.RenderUnit) string {
	return strings.SplitN(u.Title, "（", 2)[0]
}

// unitPlanFingerprint 单元计划指纹：单元表里该单元的行内容哈希——段落重分组/
// 格子变化 → 该单元画面过期（分镜级指纹 sceneFingerprint 吃合并旁白，段内微调
// 能传导；分组/格子变化由这里独立传导）。
func unitPlanFingerprint(p *pipeline.Project, unitID string) string {
	for _, u := range LoadUnits(p) {
		if u.ID == unitID {
			b, err := json.Marshal(u)
			if err != nil {
				return ":err"
			}
			sum := sha256.Sum256(b)
			return ":" + hex.EncodeToString(sum[:4])
		}
	}
	return ""
}
