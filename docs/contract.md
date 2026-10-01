# 渲染器入参契约(contract.md)——笼子一全文

> v2 定稿(2026-09-30,M0'-a)。本文是场景文件的唯一技术法律:模型可以自由决定写什么画面、用什么框架,但写出的文件必须通过本契约才能进渲染器。
> 执行方式:静态部分由 `ai/scene_lint.py` 机械检查(Tier 0),运行时部分由 hyperframes check + 确定性回放探针把守(见 plan-v2 §5)。
> 配套:plan-v2.md §3(条款来源与讨论记录)、ai/contract-example/seg-golden.html(金样例,lint 全绿的参照物)。

## 0. 一句话

**契约管"时间归谁、状态从哪来、资产在哪",不管"画面长什么样"。** 框架任意(GSAP/Canvas/WebGL/Three…),时钟唯一。

## 1. 场景文件格式(段 = 一个自包含 HTML)

每段一个文件,即 HyperFrames sub-composition 片段:

```
<template>
  <style> …自由 CSS(遵守 §2/§3)… </style>
  <div data-composition-id="segNN" data-width="1920" data-height="1080">
    <div id="root"> …画面内容… </div>
  </div>
  <script>
    (function () {
      var tl = gsap.timeline({ paused: true });
      …全部动效编排…
      window.__timelines["segNN"] = tl;
    })();
  </script>
</template>
```

硬性结构规则:

| # | 规则 | 检查 |
|---|---|---|
| F1 | 整个文件就是一个 `<template>` 片段,无 DOCTYPE/html/head/body | lint |
| F2 | 根元素 `data-composition-id="segNN"`(=段 id,逐字一致);`#root` 为舞台 | lint |
| F3 | 恰好一个 `gsap.timeline({paused:true})`,注册到 `window.__timelines["segNN"]` | lint |
| F4 | 每个可见元素(参与剪辑的)带 `class="clip"` + `data-start` + `data-duration` + `data-track-index` | lint(计数≥3) |
| F5 | 每个有意义的可见元素带 `data-hf-name="人话名"`(元素级指认的语义标注;plan §6 的 data-el 落到这个 v1 既有属性上) | lint(计数≥3) |
| F6 | 画幅固定 `data-width/data-height`(来自 project.json,16:9 或 9:16);禁 vw/vh/百分比定位舞台;禁滚动 | lint(vw/vh)+check |
| F7 | 内容区 = 上 83%;底部 ~17% 是平台字幕带(assemble 注入),模型**不写字幕**——禁止出现 `data-ph=` / `data-w=` 逐词标注(字幕层=TTS 时间戳机械装配) | lint(禁标) |
| F8 | 一段一个文件,文件名=段号;段内多镜头用元素 data-start 编排,不拆文件 | 管线 |

## 2. 时间与确定性(第一不变量:任意 t 可渲染)

| # | 规则 | 检查 |
|---|---|---|
| T1 | **平台时钟是唯一时钟**:一切状态变化挂在唯一的 paused timeline 上(或由它驱动的 draw(t));禁框架自带 ticker/loop 自走 | lint+回放探针 |
| T2 | 禁 `Date.now` / `new Date` / `performance.now` / `setTimeout` / `setInterval` / `requestAnimationFrame` | lint |
| T3 | 禁 `Math.random` / 未种子化随机;随机一律用注入的种子化 rng(金样例的 `__rng32(seed)` 形态) | lint |
| T4 | Canvas/WebGL:绘制函数 = `window["__draw_<id>"] = function(t, ctx, W, H, rng)`(时间的纯函数,每帧从零重画);由 `tl.to({},{duration,onUpdate:…draw(t)…})` 驱动;运动参数化 `f(t)`,禁逐帧累积状态 | lint(模式)+回放探针 |
| T5 | 禁 CSS `animation` / `@keyframes` / `transition`(合成器时钟,seek 不到) | lint |
| T6 | 禁 Worker / SharedArrayBuffer / MessageChannel 参与画面状态(调度时序不确定) | lint |
| T7 | 禁 `<video>` / `<audio>` 元素(音视频是平台层:音轨由 assemble 注入,视频素材走帧序列) | lint |
| T8 | 确定性回放探针:同段同 t 两轮(不同历史路径)截帧像素一致
| T9 | 禁 CSS transform 与 GSAP 补间同元素混用(GSAP 覆盖整个 transform,CSS 居中被静默丢弃);居中用 xPercent/yPercent(2026-09-30 补:bakeoff 实测模型高发) | lint |(容差:通道差≤8、差异像素≤0.05%——GPU 光栅化 LSB 抖动) | 运行时(sceneReplayCheck) |

## 3. 资产与环境

| # | 规则 | 检查 |
|---|---|---|
| A1 | 禁一切网络:`fetch` / `XMLHttpRequest` / `WebSocket` / `EventSource` / 外链 `http(s)://`(脚本、样式、图片、字体) | lint |
| A2 | 字体:只用本地可得的字体栈,且引用即声明(`@font-face{…src:local(…)}`);OS 自带中文字体(宋/黑/楷/仿宋/雅黑/Noto SC)按 assemble.py 的 local 声明惯例 | lint+check |
| A3 | 框架库文件必须来自平台 vendor 本地内联(现阶段:gsap 由 wrapper 提供,场景内不引 `<script src>`);需新框架时场景内声明 `<!-- vend: lib@x.y.z -->`,lint 对 manifest 已登记库放行、未登记库报缺库(即采购申请单);采购/验收/台账见 docs/vend-stage.md,vendor 卷 = `data/library/vendor` | lint |
| A4 | 图片:只能引用 `assets/img/` 下的本地素材(素材库 lib:<id> 解析后的落盘路径);禁 data-URI 大图(>64KB 视为素材,应入库) | lint |

## 4. 时序数值

| # | 规则 | 检查 |
|---|---|---|
| C1 | `data-start/data-duration` 用绝对秒(段内时间轴,0 起);数值精度 3 位;总时长=该段旁白时长+0.35 尾(SegTail,assemble 口径) | prompt+check |
| C2 | 揭示随旁白节拍分布(不前载、不拖尾):场景生成 prompt 附逐词时间戳,元素进出点对齐短语边界 | frameQA 对照 |
| C3 | 比例时序(§2.4 的 frac 模式)为 M0'-b 目标态,当前版本用绝对秒(TTS 已先行落地) | —— |

## 5. 执行映射(检查点归属)

| 层 | 谁 | 查什么 |
|---|---|---|
| 生成时 | sceneGen prompt + Go 结构校验 | F1-F3/F5/F7、字幕禁标、elements 注册表非空 |
| 静态门禁 | `ai/scene_lint.py` | §1-§3 全部 lint 项(零误报才阻断;违规逐条回喂修复≤2轮) |
| 运行时门禁 | `hyperframes check` | 布局重叠/对比度/字体声明/溢出(既有能力,不变) |
| 确定性探针 | `sceneReplayCheck`(两轮 snapshot 像素对比) | T1-T4 的实际效果(v1 已有,保留) |
| 视觉抽检 | frameQA(Tier 1,是非题) | 裁切/重叠/空白/提前穿帮(revealPlan 改从帧 HTML 的 data-start 解析) |
| 人工 | 门2 草稿确认 | 审美(Tier 2,机器不拍板) |

## 6. 修复协议(外科手术式)

检查失败 → 违规清单+问题帧+模型自己的场景文件一起回喂 → 模型修自己的代码 → 复检。**禁止**默认回退模板/换风格(那是杀创意的机制)。修不好标"转人工"交门2,不静默降级。

## 7. 与 v1 的差异备忘

- v1 的 spec→render_spec 路径已拆除:语义槽位、元素菜单(elements.json)、图标校验、搜图本地化(fetch_images)不再出现在主路径;`ai/render_spec.py` 已删,`ai/engines/`(皮肤/字幕引擎)保留——subtitle.py 由 assemble.py 作平台字幕层复用。
- 字幕带从"帧内 render_spec 生成"上移为 assemble 平台层(与音轨/overlay 同级):场景文件只写上 83% 内容。
- `data-hf-name` 语义标注保留(v1 已有,元素点击/检视指认基础设施不变)。
- 确定性回放探针、对比度廉价修复(applyContrastFixes)、段片增量渲染、xfade——v1 资产原样保留。
