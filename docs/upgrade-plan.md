# 表现力升级技术方案 v1

> 状态：**M-A、M-B、M-C 第一/二/三批已落地（2026-09-27/28，落地记录见文末 §11）**；M-C 主线收口，剩余仅后置评估项：整片连续 comp / three.js / 转场 v1b xfade（评估后定）+ 水墨并行线（原型闸未启动）。产品文档已同步（`docs/plan.md` §11 + `docs/overview.md`）。
> 依据：`docs/analyes/` 三篇参考片分析（群论之美 / 水中墨 / 地球四十六亿年）。
> 定位：本文档是**实施依据**——评审通过后按 §8 里程碑开工；与 `docs/plan.md`（产品权威文档）互补，不改动其产品形态与管线状态机。
> 一句话：底座不动（headless Chrome + GSAP seek 截帧 + ffmpeg），补词汇表、松闸门、上妆。

## 0. 结论摘要

三篇分析独立收敛到同一组结论：

1. **渲染底座不是瓶颈**——两个参考片本身就是网页渲染，与我们同代技术。
2. **核心缺口是表达词汇表**：无 canvas 程序化绘制通道、无公式排版、无动态粒子、无 3D；custom 逃生舱预算差一个量级。
3. **质检闸门反向作用**：空白>25%、重叠>25%、对比度强制米白，均按"信息卡片"立法，系统性误伤电影构图。
4. **审美供给缺失**：无后期链路（vignette/颗粒/色差全无）、灰底字幕条、段间硬切、无跨段 HUD。
5. **有状态物理**（水墨/流体）装不进 seek 截图模型，走"离线渲染 mp4 入素材库"旁路，与本方案正交并行。

## 1. 目标 / 非目标

**目标**：同一套管线，成片观感从"信息卡片"升级到参考片档次——程序化画面（粒子/弦图/线框/函数曲线）、公式排版、电影感后期、跨段 HUD、像样的转场与运镜。

**非目标**（三篇分析一致，明文锁定）：

- 不换渲染引擎，不引入视频生成 API（Sora/可灵/即梦类）；
- 不让 LLM 写有状态模拟或手写 GLSL（前者走出库素材，后者用参数化模板）;
- 卡片类段落的现有质检语义一行不改（回归保护，见 §9）。

## 2. 分期总览

| 期 | 内容 | 规模 | 依赖 |
|---|---|---|---|
| **P0** | 后期链路 / 全局 overlay / 字幕重做 / 去水印 | 天级，四项互相独立 | 无 |
| **P1** | **scene 通道**（核心）/ formula 公式 / 段间转场 / camera 关键帧 | 周级 | scene 原型闸通过 |
| **P2** | fx 扩充 / scrim token 化 / 风格提炼 / 方法库合并 / 音频升级 | 持续 | P1 落地 |
| **并行线** | 水墨素材（离线渲染→素材库→stitch 叠底） | 独立推进 | 原型闸 |
| **后置评估** | 整片连续 comp / three.js 本地供给 | 评估后定 | scene 通道验证后 |

## 3. P0：不加新能力，先涨一档

### P0-1 后期链路（性价比之王）

**动机**：参考片约一半"电影感"来自全片统一后期（暗角/颗粒/色差），我们是零后期。

**设计**：

- 落点 `pipeline.go stageStitch`（现 `pipeline.go:572`，BGM 混音所在），在同一次 ffmpeg 执行里对视频流加滤镜链，**不新增管线阶段**。
- 滤镜链（按参数合成，0 值跳过该项）：
  - 暗角：`vignette=angle=PI/{k}`（k 由强度参数换算）；
  - 颗粒：`noise=alls={n}:allf=t`（时域颗粒，n≈4–8）；
  - 色差：`rgbashift=rh=-1:bh=1`（轻微，可关）。
- **参数来源**：`styles.json` 每风格新增 `post: { vignette, grain, chroma }`（0–1 强度）；项目 `project.json` 可覆盖或 `post:false` 整体关闭。无 post 节的风格走内置默认（轻暗角+轻颗粒）。
- **编码代价**：stitch 从 `-c copy` 变为视频重编码一次（libx264 crf 18）。4 分钟 1080p 实测预计 1–3 分钟，一次性成本，可接受。混音与后期合并为一条命令图：`[视频]滤镜链→[+BGM 侧链闪避]→main.mp4`；无配乐项目同样走滤镜（此时仅视频滤镜+音频 copy）。
- **幂等**：`StitchNow` 重复执行直接覆盖 main.mp4，无状态残留；段级增量重渲（segStale）不受影响——后期只发生在整片级。

**改动文件**：`server/internal/pipeline/pipeline.go`（stageStitch/mixBGM 合并滤镜图）、`data/library/registry/styles.json`（post 节）、`server/internal/contract`（若 post 参数需要跨端类型）。

**验收**：同一项目开关 post 各渲一次，人工对比暗角/颗粒可见且不脏画面；无 BGM 项目后期生效；`constants_drift_test` 模式补 post 默认值防漂移。

### P0-2 全局 overlay 层（跨段 HUD）

**动机**：段 = 独立 HTML，跨段一致的章节角标/计数器/时间轴无处容身，是"碎片感"的直接来源。参考片三锚点 HUD（左上章节名 / 右上实时计数器 / 底部时间轴）全部靠这一机制。

**设计**：

- **数据在分镜级**（跨段天然一致），不在每段 spec 里重复。`storyboard` 头部新增可选节：

  ```json
  "overlay": {
    "enabled": true,
    "chapter_counter": { "prefix": "距今", "unit": "亿年", "from": 46.0, "to": 0.0 },
    "timeline": ["冥古宙", "太古宙", "元古宙", "显生宙"]
  }
  ```

  每段新增可选字段 `chapter`（章节名，如 "VII 群与对称"）；相邻同章节段自动合并。分镜 prompt（`pipeline.go stageStoryboard`）增补生成指引，默认不启用。
- **渲染注入**：`assemble.py project_html` 按模板注入统一 overlay DOM（track-index 9，位于字幕带之上、内容之下），三锚点各自可缺省。
- **计数器确定性**：GSAP tween 一个代理对象 `{v: from}`，`onUpdate` 里 `textContent = prefix + v.toFixed(2) + unit`——值是 t 的纯函数，seek 模型兼容（与现有逐词字幕高亮同机制，已被验证）。
- **字体/颜色**：读风格 token（衬线族、accent 色），overlay 是风格的一部分而非独立皮肤。

**改动文件**：`server/internal/contract`（storyboard/seg 结构体 + 校验）、`pipeline.go`（分镜 prompt）、`ai/assemble.py`（注入）、前端时间轴暂不动。

**验收**：一条多段测试片，章节角标跨段位置/字体完全一致；计数器随时间单调变化且 seek 到任意 t 读数正确；overlay 关闭时与现状逐字节一致。

### P0-3 字幕条重做

**动机**：灰底胶囊字幕条是三部测试片共同的"掉档次"项。字幕是文案不是 UI。

**设计**：

- 字幕带 DOM 生成在 `stylepack.py` / `stylepack_generic.py` / `stylepack_skin.py` 的 capzone 节（track-index 8，三处复制——顺手抽成共享函数，消一次三端漂移）。
- **样式 token 化**：styles.json 每风格 `subtitle: { mode: "box"|"line"|"none" }`，默认 `line`：底部居中一行细字，无底框，加轻微 text-shadow 保证任意底色可读；词级高亮保留，形式从"色块"改为"主行亮度/颜色渐强"。
- `none` 档配合 P1 的双语字幕预留（见 §4 P1-1 之后规划，本文档不展开实现）。

**改动文件**：三个 stylepack 引擎的 capzone 段、styles.json subtitle 节。

**验收**：三种 mode 各出一帧样张；暗底/亮底/照片底三种背景下字幕均可读；词级高亮时序与现状一致（复用 tts_align 词级时间戳，不动对齐层）。

### P0-4 去水印（⚠ 需拍板，见 §10-D）

**现状**：生图走智谱 CogView 免费档，图上带"AI生成"水印（`fetch_images.py:19-22`），水印出现在成片画面上。

**候选路径**：

| 路径 | 成本 | 效果 |
|---|---|---|
| a. 换付费生图档 | 按量计费（数分/张级） | 直接消除，质量同步提升 |
| b. 本地 SD（如 SDXL/Flux 量化版） | 一次性部署 + 显存占用 | 零边际成本，需运维 |
| c. 程序化底图（CSS 纹理/渐变/噪声） | 零成本 | 无水印，但观感上限低于好照片 |

**推荐**：a 为默认（账号已有智谱体系，接入成本最低），c 作为未配 key 时的静默回退。实现侧统一：`fetch_images.py` 引擎优先级配置化（`IMAGE_GEN_ENGINE` env），回退链写进配置而非代码分支。

## 4. P1：scene 通道（核心投入）+ 三件配套

### P1-1 `kind=scene` 确定性逐帧画布

**动机**：参考片的核心画面（弦图/粒子/线框 3D/函数曲线/加色辉光）全部是**无状态纯函数绘制**——同一时刻 t 永远画出同一张图。这与我们"跳到任意 t 截图"的渲染模型完全兼容，只缺一条通道。先例：`demo/ak-pv-repro/build_ak.py` 已手工绕开 spec 直产 comp JSON 做出同类画面，本项 = 把旁路转正。

#### 4.1.1 契约（spec.go）

- `specKinds` 增加 `scene`，每段 **≤1 个**（与 custom 计数并列校验）。
- `SpecElement` 新增：

  ```go
  Code string `json:"code,omitempty"` // scene 专用：__draw(t, ctx) 函数体
  Seed int    `json:"seed,omitempty"` // 随机种子，缺省 42
  ```

- 语义：scene 是**全画幅背景绘制层**——画满整个 Canvas，安全区/重叠/元素数/空白检查全部豁免（对齐 role=bg 的豁免先例）；字幕带仍由管线渲染在其上（track-index 8 > scene 的 0）。
- `reveal/exit/anim` 对 scene 无效（校验时忽略并提示）；入场淡入由渲染层统一加 0.3s opacity tween，不可配（防 LLM 滥用）。

#### 4.1.2 确定性约定（本方案的承重墙）

- 唯一入口：LLM 写函数体 `__draw(t, ctx, W, H, rng)`，渲染层包装成完整函数。约定：**同 t 必须同像素**。
- 随机性：注入内建 `mulberry32` seeded PRNG，函数体里 `rng()` 即确定性序列；每次 `__draw` 调用前以 `Seed` 重置，保证帧间序列不漂移。
- 禁令沿用 custom 的 jsBan 思路，scene 专用表：禁 `Math.random`、`Date`、`requestAnimationFrame`、`setTimeout/setInterval`、`fetch/XMLHttpRequest`、`eval`、`document`、`window`、外链；`Math.*` 其余全放行（三角/幂/取整是数学绘制的刚需）。
- 校验三层：① 正则黑名单（spec.go，落盘前）；② `node --check` 语法（stageCheck，现有基建）；③ **确定性回放测试**（见 4.1.5）。

#### 4.1.3 预算

- `scene` code 上限 **8000 字**起步（决策点 §10-A：另一方案主张 20k）。理由：配模板注入后 8k 够写 200 行级场景；预算每放宽一级，回喂修复的收敛难度上升一级。原型闸实测后再调。
- 预算内不叠 custom 上限（scene 不占 custom 的每段 ≤3 名额）。

#### 4.1.4 驱动（assemble.py / render_spec.py，hyperframes 零改动）

- `render_spec.py` 对 kind=scene 产出：全画幅 `<canvas id="{f}" width={W} height={H}>` + 内联 `<script>` 定义 `__draw_{f}`；
- 注册占位 tween（与普通元素同机制）：

  ```js
  tl.to({}, { duration: dur, onUpdate: function () {
      var t = tl.time();                 // 段内时间
      ctx.clearRect(0, 0, W, H);
      rng = mulberry32(seed);            // 每帧重置随机序列
      __draw_f(t, ctx, W, H, rng);
  } }, 0);
  ```

- **关键假设**：GSAP 时间线 seek 时 onUpdate 必触发。这是原型闸第一验证点（§8 M-A），假设不成立则改用 `tl.eventCallback` 或逐帧驱动方案，hyperframes 侧仍不动。
- Canvas 底色透明，风格背景（纸色/深色 token 底）垫在下层——scene 只画前景内容，风格统一性不受破坏。

#### 4.1.5 质检适配（只松 scene，不动卡片）

- **ValidateSpec**：scene 元素不进 bbox 重叠收集；豁免安全区/空白/元素数。
- **frameQA 四帧审查**（`frameqa.go`）：含 scene 的段跳过"空白>25%"与"视觉重心"判定，保留黑屏/裂图/开场穿帮三查；画面整体过暗不再标红（深底是故意的）。
- **对比度修复**（`applyContrastFixes`）：scene 段整体跳过；scene 之上的卡片元素照旧。
- **确定性回放测试**（新增，挂 stageCheck 末尾）：对 scene 段在 t=0 / 中点 / 尾帧三个时刻各截帧两次，像素 diff 必须 =0，>0 回喂修复。复用 frameQA 的抽帧基建，每 scene 段 +6 次截帧的成本。
- **降级路径**：3 轮回喂仍不过 → 该段失败（与现有段失败语义一致），不静默降级——半成品画面比失败更伤观感。

#### 4.1.6 prompt 与模板注入（与方法库主线汇合）

- genSpecs prompt 增补 scene 章节：使用时机（数学/物理/粒子/线框类画面）、确定性规则、代码骨架。
- 模板库：`data/library/methods/scene/` 收录弦图/粒子场/线框投影三份参考实现（各 ≤3k 字，自写不搬运），prompt 按段内容按需注入一份。**这就是方法库注入主线在 scene 域的第一批内容**，模板质量与 LLM 成活率直接相关，是 M-B 的主要工作量之一。

**改动文件**：`spec.go`（契约+校验）、`render_spec.py`（canvas 生成+驱动）、`frameqa.go`/`produce.go`（QA 豁免+回放测试）、`pipeline.go`（分镜/spec prompt）、`data/library/methods/scene/*`。

**验收（三级）**：① 原型闸：手写代码（不过 LLM）单段复刻"乘法表弦图 ×2→×3 生长"，onUpdate-on-seek 假设验证 + 回放 diff=0 + 豁免链路走通；② 链路闸：LLM 全自动产 scene 段，连续 10 段统计成活率 ≥7/10（不达标优先补模板不放宽校验）；③ 成片闸：真实选题出一条 60s 测试片（scene+formula+overlay+转场+后期全开），与现有管线成片盲评。

### P1-2 `kind=formula` 公式通道

- KaTeX **离线打包**进 `ai/assets/katex/`（css+fonts+js；外链在 cssBan 禁区，必须本地）。
- `SpecElement` 复用 x/y/fs/color + 新增 `tex string`（≤500 字，黑名单禁 `\href`/`\includegraphics`/外链）。
- 渲染：`render_spec.py` 产出容器 + `katex.render(tex, el, {throwOnError:false})`——KaTeX 排版是同步 DOM 生成，无异步无随机，seek 兼容；字号/颜色映射进容器 style。
- 校验：tex 长度+黑名单进 ValidateSpec；渲染失败（KaTeX throwOnError）在 check 阶段暴露为红字回喂。
- 验收：行内公式/分式/矩阵/希腊字母各一帧样张，任意 seek 时刻截图一致。

### P1-3 段间转场（两档递进）

**约束**：concat `-c copy` 模型下没有重叠帧，真交叉叠化必须整片重编码。

- **v1（assemble 层，零重编码）：黑场微叠化**——每段片头 0.25s fade-in、片尾 0.25s fade-out（吃进 SegTail=0.35 的既有余量，音画不冲突）。效果：硬切的"跳"感消除，代价是段间有 1–2 帧暗部过渡。
- **v1b（stitch 层，可选）：ffmpeg xfade 交叉叠化**——段时长表已有（probeDuration），在 P0-1 的那条重编码命令图里串 xfade 链。效果对齐参考片，实现复杂度中等。**建议**：v1 先上、看效果，v1b 在 M-B 末评估（决策点 §10-C）。
- 转场类型配置进 styles.json（`transition: "cut"|"fade"|"dissolve"`），默认 fade。

### P1-4 camera 关键帧化

- 现状：camera 5 预设全是微动（zoom 1↔1.05、pan ±36px）。
- `CompSpec` 新增可选 `camera_spec: { from: {zoom, pan_x, pan_y, rot}, to: {...}, ease: "power1.inOut"|... }`；与词表 camera 并存，camera_spec 优先。范围钳制：zoom 1–1.6、pan ±120px、rot ±6°（ValidateSpec 裁剪）。
- 渲染沿用现有内容层 transform tween（字幕带在 cam 外不动，机制不变）。
- 验收：推-拉-环绕三段样张；现有 5 词表行为逐字节不变（回归）。

## 5. P2：质感与供给侧（持续，逐项独立）

| # | 项 | 要点 |
|---|---|---|
| 1 | 背景 fx 扩充 | noise/vignette/动态粒子（沿用 `_stars` 的 LCG 确定性先例）；文字辉光进 token（text-shadow 词表化） |
| 2 | scrim token 化 | 取消背景图强制纸色 scrim（`render_spec.py:337-363`），浓度改由风格 token 决定；深色风格允许低 scrim 保留照片影调。注意与对比度闸门联动回归 |
| 3 | 风格提炼 | 用参考片走既有 `extract_stylepack` 提炼「暗空霓虹」（深空黑金青+衬线+HUD+双语字幕模板）；抽 20 帧进样张矩阵做 few-shot |
| 4 | 方法库合并 | scene 模板、"高级感公式"（大留白+单主体/暗底浅字纪律/转场公式）并入 methodlib 主线 |
| 5 | 音频升级 | TTS 付费档评估（CosyVoice 级）；BGM 曲库按情绪扩充；转场 sfx 层（riser/whoosh，stitch 侧链闪避基建已有） |
| 6 | 后置评估 | **整片连续 comp**（连续叙事题材一个 comp 渲到底，ak-pv-repro 路线转正，双模式并存）；**three.js 本地供给**（scene v1 用手写解析投影先验证，three.js 待 scene 通道稳定后评估体积与确定性成本） |

## 6. 并行线：水墨素材（与主线零依赖）

方案见 `docs/analyes/ink-fluid-analysis.md`（路线 1 已选定），要点转录：

1. `tools/` 离线造墨工具：开源 WebGL-Fluid-Simulation（MIT）+ 自加 target-driven 汇聚，固定种子固定步长逐帧截帧拼 mp4；
2. 素材库加视频素材类型（tags/时长/封面帧，沿用 id 引用）；
3. 引擎 role=bg 接受视频素材；LLM 只挑 id 不产内容；
4. 合成走 stitch ffmpeg 叠底（段 HTML 截透明底 overlay）。

**与主线的两个共享技术点**（水墨线原型闸顺带验证）：① WebGL canvas 截图需 `preserveDrawingBuffer:true` 否则黑帧；② hyperframes 透明底截图支持（`omitBackground`）——不支持则叠底改 blend 模式近似。
**节奏**：只做原型闸（固定种子 10s 墨滴+一字成形）即可随时插队跑，达标才动引擎侧，不占主线排期。

## 7. 回归保护

- **基线**：`videos/` 三部风格测试片为基线；P0/P1 每批合并前重渲其中一部，人工对比无意外回退。
- **豁免边界硬约束**：所有质检放宽严格绑定 `kind=scene`/overlay 开关/`subtitle.mode`，卡片段默认路径的判定代码零修改；`contrast_fix_test` 等现有测试全量保持通过。
- **常量漂移门禁**：scene 预算/overlay track-index/post 默认值进 `constants_drift_test` 同款门禁。

## 8. 里程碑

| 里程碑 | 内容 | 出口判据 |
|---|---|---|
| **M-A**（~3 天） | P0 四项 + scene 原型闸 | 后期/overlay/字幕上线并出对比样片；弦图原型 diff=0 |
| **M-B**（~2 周） | scene 转正全链 + formula + 转场 v1 + camera 关键帧 | 链路闸成活率 ≥7/10；60s 全开测试片 |
| **M-C**（持续） | 双语字幕/章节进 overlay、fx 扩充、风格提炼、方法库合并 | 真实选题成片盲评不输参考片档次 |
| **水墨线**（插队） | 原型闸 → 链路闸 → 按词现做 | 每级闸达标才进下级 |

## 9. 风险与对策

| 风险 | 对策 |
|---|---|
| GSAP seek 时 onUpdate 不触发（scene 承重假设） | 原型闸第一天验证；备选：eventCallback / 逐帧显式驱动，hyperframes 侧均零改动 |
| LLM 写 8k 代码成活率低 | 模板注入为主 + 回喂修复；成活率不达标先加模板不放宽校验；慢失败优于坏画面 |
| 全画幅逐帧 JS 绘制拖慢截图 | 按 scene 段实测耗时；慢段降采样 15fps + ffmpeg 插帧（预案，v1 不做） |
| 质检放宽引发卡片段质量回退 | 豁免绑定 kind，硬约束见 §7；基线重渲对比 |
| stitch 重编码引入画质/时长漂移 | crf 18 + concat 边界帧对齐验证（首尾帧逐帧比对） |
| KaTeX 字体加载时序 | 字体本地打包 + document.fonts.ready 由 hyperframes 现有资源等待机制覆盖（check 阶段验证） |

## 10. 待拍板事项

- **A. scene 代码预算**：8k 字起步（推荐，配模板）vs 20k 字（群论篇主张）。推荐 8k，原型闸后按成活率调。
- **B. three.js 是否进运行时**：推荐 v1 不引（scene 用解析投影验证通道），M-C 后评估。
- **C. 转场方案**：推荐 v1 黑场微叠化（零重编码），v1b xfade 交叉叠化在 M-B 末看效果决定。
- **D. 去水印路径**：付费生图档（推荐默认）/ 本地 SD / 程序化底图回退。涉及 API 计费，需要你定。

---

## 11. 落地记录（2026-09-27）

### M-A ✅（P0 四项 + scene 原型闸）

- **P0-1 后期链路**：`pipeline.go stitchFinal`——色差→暗角→颗粒滤镜链 + BGM 侧链混音一条命令图（crf 18 重编码一次）。参数链 project.json `post`（false=关/对象=整组替换）> 风格 tokens.post > 默认 `{0.35,0.4,0}`；13 风格已配 post 节（深色系强/浅色系轻）。ffmpeg 滤镜能力探测（`-filters` 缓存），缺滤镜自动跳该项不炸终装。无后期路径保持 `-c:v copy` 老行为。`post_test.go` 解析回环。
- **P0-2 overlay**：契约 `Storyboard.Overlay`（chapter_counter/timeline）+ `Segment.Chapter`（"中文|English"竖线分）；`assemble.py overlay_block` 注入每段页面（含 .hf-seg 单段文件——计数器按段全局时间片取起止值，线性 tween seek 确定）；三锚点：左上章节（衬线+英文 small caps）/右上计数器（tabular-nums）/底部进度线（章节刻度）。分镜 prompt 加生成指引（默认不开）。干跑验证：seg 计数起值/进度区间正确、未启用项目零污染。
- **P0-3 字幕共享层**：`ai/engines/subtitle.py` 四引擎（楷体/flat/generic/skin）同源，三模式 box|line|none。默认 **line**（无框细字，投影随文字色明暗自适应：浅字暗影/深字亮影）；generic 的 tokens.cap.mode 历史值 bar/pill→box、plain→line；skin 走 extra.subtitle_mode；none 档连 capzone div 带短语 DOM 一起省略。时序逻辑（短语硬切+词级渐亮）与旧实现逐字符一致。
- **P0-4 去水印**：`IMG_GEN_HD=1` 切 cogview-4 无水印付费档（按量计费，失败自动回退免费档并降级提示）；默认仍免费档（cogview-3-flash）不悄悄花钱。`.env.example` 已注明。
- **scene 原型闸 ✅**：`demo/scene-prototype/`（chord.html + render1.mp4 + GATE.md）——onUpdate-on-seek 成立、两渲三时刻 md5 全同、加色混合/seeded PRNG/硬件 GPU 捕获可用、6s 1080p30 全画幅 canvas ≈18-20s。

### M-B ✅（scene 转正 + formula + 转场 v1 + camera 关键帧）

- **scene 契约**（`spec.go`）：`kind=scene` 每段 ≤1、`Code`（≤`SceneCodeMax`=8000 字，drift 门禁钉住 Py 同值）+ `Seed`（0–99999 缺省 42）；专用黑名单 `sceneCodeBan`（禁 Math.random/Date/rAF/定时器/DOM/出网/import，Math 三角幂取整放行）；scene 段元素数下限放宽到 1；anim/exit/reveal 拒收；Sanitize 缺 code 删、seed 越界钳 42。`scene_test.go` 覆盖禁令/预算/数量/下限/清洗。
- **scene 渲染**（`render_spec.py render_scene`）：全画幅 canvas + 驱动壳（可信模板 document 可用）——`mulberry32(seed)` 每帧重置 + 占位 tween onUpdate 取 `tl.time()` 调 `window.__draw_<eid>`；code 过 Py 侧黑名单 + `node --check` 双闸（@CODE@ 占位符拼接防花括号洗坏）；入场统一 0.3s 淡入。层序 role=bg(0)→scene(1)→内容(2)。
- **formula 通道**：KaTeX 0.16.11 离线打包 `ai/assets/katex/`（1.5MB，随仓库分发，assemble 拷进项目 assets + 主页面注入 css/js）；`kind=formula`（tex ≤500 字，禁 \href 等出网命令，fs 24–160）；`katex.render` 同步排版 seek 确定，坏公式 throwOnError:false 落红字 check 可见；bbox 按 tex 长度保守估参与重叠检查。
- **camera 关键帧**：`CompSpec.CameraSpec`（from/to{zoom 1–1.6, pan ±120px, rot ±6°}+ease 三词表），与 camera 词表并存且优先；`camera_spec_js` 全参数 fromTo tween（含钳制）；越界 ValidateSpec 报错回喂、SanitizeSpec 钳进界。
- **转场 v1**：`assemble.py` 每段页面（index + .hf-seg）注入 `#xfade` 黑场层——段首 0.25s 淡入 + 段尾 0.25s 淡出（t 线性函数，确定性）；styles.json `tokens.transition`（cut|fade，缺省 fade）。
- **frameQA 豁免**（`frameqa.go`）：scene 段提示词整组关闭「空白>25%/视觉重心」判定，换为「只查全黑/撕裂/叠加文字不可读/canvas 静止」；canvas 像素不算元素。
- **确定性回放门禁**（`produce.go sceneReplayCheck`）：check 过后、放行前，对全部 scene 段各取 3 个全局时刻（避开转场渐变区），全片主组合 `hyperframes snapshot` 两轮截帧（不支持 -c 单段，主组合一次覆盖所有段；每轮一次浏览器冷启）→ PNG 解码像素 sha256 逐帧比对；失败按「✗ 段ID」格式报错进 failingSegs 修复轮。`scene_replay_test.go` 端到端：夹具正向 diff=0 通过 + 注入 Math.random 反向被拦且报错带段 ID。
- **prompt + 模板库**：genSpecs prompt 增补 scene 通道（使用时机/确定性硬约束/审美建议）+ formula 语义映射 + camera_spec 输出位；`data/library/methods/scene/`（chord.js 弦图 / particles.js 粒子场 / wireframe.js 线框投影，各 ≤1.1k 字）按段关键词命中注入一份仿写（.gitignore 已白名单 methods/）。模板过 Go/Py 双黑名单 + node 语法闸。
- **链路闸 ✅**（`data/projects/p20260927-mbgate`，本机夹具不入库）：2 段全开（线框 scene + KaTeX 公式 + camera_spec 镜头 + overlay 计数器 + line 字幕 + xfade 转场）真渲通过——注入点齐全、区域色彩特征逐项确认（线框/公式区 1142 独立色/金色计数器/标题/字幕带/粒子）、**整链双渲三时刻 md5 全同**。seg01 含 katex 渲染 4.3s 段 ≈57s。

### M-C 进度（2026-09-27 第一批）

- **fx 扩充**（stylepack_generic）：背景 fx 词表 +vignette 暗角 / noise 噪点颗粒（LCG 24 点/160px tile 平铺，胶片感）/ particles 双层密星（准粒子场，静态确定性）；`_stars` 参数化（seed/zone/半径）供复用。全部确定性（LCG 先例），无 CSS 动画。
- **文字辉光 token**：`tokens.text_glow: true` → title/big/label 主色双层 text-shadow（深空霓虹质感）。cosmos/techiso/meshglass/keynote-dark 已开。
- **scrim token 化**（render_spec.render_bg_image）：`tokens.scrim_mode: paper（默认）| dark | none`——深色风格配 dark 后背景图不再被纸色 scrim 洗平（黑色压暗保照片影调）；paper 模式回归不变。cosmos/techiso/meshglass/keynote-dark/voxmap/chalkboard/vid-ref 已配 dark。
- **「暗空霓虹」风格入库**（neon-void）：群论之美设计语言提炼——深空黑 #05070F + 金 #F5C542/青 #37D6C0 辉光 + 宋体衬线 + text_glow + vignette/noise/particles 全 fx + line 字幕 + 强后期（0.6/0.5/0.3）+ dark scrim；三个版式（章节题居中/发光单主体/数据墙）。注册表 15 风格。
- **scene 成活率基准 ✅ 10/10**：`scene_survival_test.go`（SCENE_SURVIVAL=1，~7 分钟）——LLM 全自动 10 段数学选题（弦图/粒子/线框/曲线/波纹/素数/网络/吸引子/傅里叶/旋臂）→ scene 使用 10/10、渲出 10/10、画面非空 10/10、确定性回放 10/10（验收线 70%）。基准三轮迭代顺带修出四项真问题：
  1. 回放门禁加 LSB 容差（max 通道差 ≤2 且差异占比 ≤0.05%）——加色混合下 GPU 光栅化有 1/255 级抖动（实测 900 粒子段两轮 16/2073600 像素差 1），非逻辑非确定；真非确定仍拦（反向用例维持）。
  2. render_scene 剥函数声明壳——LLM 高频把整段 `function __draw(){...}` 写进本该是函数体的 code（语法闸拦下静默丢元素），现在任意位置的声明自动剥壳取体内，预计算语句保留在前。
  3. prompt/元素菜单强化「code 是函数体本身，禁止 function 声明」。
  4. 判空抽帧前 MkdirAll reports（scaffold 不建该目录，ffmpeg 写不存在的目录直接失败）。
### M-C 第二批（2026-09-28）

- **双语字幕全链**：制作流程 1.5 步 `translateSubtitles`（TTS 后）——project.json `bi_subtitle: true` 开启，短语级中文一次性 LLM 翻译写回 audio_meta.json `voices[].phrases_en`（幂等、数量错位宁缺勿错、失败不挡片）；渲染层 subtitle.py 英文次行（衬线斜体小字 0.58em，box 胶囊内/line 行下），四引擎接线；渲染层认开关（关掉即不显示，旧翻译残留无效）；agent 工具 `set_subtitle`（en/off，开启后自动整片重制）。真渲验证：双语/单语同帧字幕带差异 12.1%（整带新增一行文字）。
- **BGM 曲库 3→6 首**（make_bgm.py 程序化合成，同款确定性引擎）：epic（小调史诗推进，历史/宏大叙事）、mysterious（减和弦悬浮暗垫，谜题/心理）、warm（大调慢琶音，人物/传记/公益）。

### M-C 第三批（2026-09-28，主线收口）

- **方法库注入主线合并（P2-4 ✅）**：`data/library/methods/` 清单化——`methods.json` 声明条目（domain scene|comp / file / keywords / title），加方法=加文件不改代码；produce 的 `methodsFor` 按段内容关键词命中注入：scene 域取首个命中（参考实现仿写，原 `sceneTplRules` 三模板迁入清单），comp 域最多两篇。comp 域新增三份「高级感公式」（三篇参考片分析的沉淀）：`comp/minimal-focus.md` 大留白+单主体（章节题/金句公式）、`comp/dark-glow.md` 暗底浅字纪律（三级文字层级/禁中灰长文）、`comp/transition-handoff.md` 段间衔接公式（段首不抢拍/段尾留白收束/跨段同位接力）。守卫：`methods_test.go`（清单↔文件一致、scene 模板过确定性黑名单 `contract.SceneCodeViolation`、comp ≤900 字且必有反模式段）。
- **转场 sfx 层（P2-5 ✅）**：`ai/make_sfx.py` 程序化合成（同 make_bgm 哲学，固定种子确定性）——whoosh 0.6s 段边界气流（高通噪声+呼吸包络+400→120Hz 下滑体）、riser 1.2s 章节上扬（两八度指数上滑+噪声涌起），产物入库 `data/library/media/audio/sfx/`（mp3+清单）。stitch 侧：段片 ffprobe 时长累计出整片边界（与 concat 同源），章节切换位放 riser、普通边界放 whoosh，`asplit+adelay+amix` 铺成与成片等长的音效垫（实测峰值 -14.8dB，垫在人声下），与后期/BGM 两条既有命令图合并；素材缺失/探测失败静默跳过（妆不挡闸）。开关：project.json `transition_sfx`（on/off 显式落值，风格默认开时也能压掉）> 风格 tokens.transition_sfx > 默认关；neon-void 已配 true。agent 工具 `set_sfx`（on/off/查状态，成片就绪秒级重混）。`sfx_test.go` 覆盖布点数学/滤镜图/开关解析链。
- **TTS 付费档（P2-5 ✅）**：`tts_align.py` 模型/音色环境化（TTS_MODEL/TTS_VOICE）+ 项目音色路由 CosyVoice 短名（alex/benjamin/charles/david）——项目 voice 命中且配 key 锁付费档，未配 key 警告回落 Edge 晓晓（不静默掉 Kokoro）；agent `set_voice` 接受 CosyVoice 音色；Go/Py 音色表漂移门禁 `voice_alias_test.go`。评估结案 `docs/analyes/tts-paid-tier-evaluation.md`：¥105/1M UTF-8 字节，4 分钟片 ≈¥0.0002——成本可忽略，推荐正式采用（配 key 即默认），待办仅剩四音色 A/B 人工定档。
- **BGM 曲库 6→9 首**：melancholy（悲情小调慢垫，离别/灾难）、upbeat（欢快大调快琶音，美食/生活/萌宠）、nostalgic（怀旧七和弦，历史/回忆）。
- **回归**：server 全量测试绿；方法/sfx/TTS 新增四组单测；既有 BGM/post/contract 测试无改动通过；无 sfx 项目 stitch 命令图与改前逐参数一致（copyFile 老路径不变）。

- M-C 剩余收口（2026-09-29）：
  - **转场 v1b xfade ✅**：tokens.transition=xfade → concat 侧链式 xfade/acrossfade（crf18 与后期同价一次重编码），实际段边界写 renders/timeline.json，sfx 布点/前端时间轴以它为准；段过短/滤镜缺失回退 concat；neon-void 先行启用。真片验收：8 段叠化、边界中点亮度 119（黑场 ~20-40）、时长收缩 0.4×7 与 timeline 一致。
  - **水墨原型闸 ✅**：`demo/ink-prototype/GATE.md`——CPU 稳定流体（半拉格朗日+Jacobi+涡度约束），状态式但「任意 t=从 0 重放」确定性，两渲三时刻 md5 全同，6s@1080p30 ≈15s。正式造墨工具（tools/make_ink + target 汇聚）未立项。
  - **整片连续 comp / three.js 评估 ✅**：`docs/analyes/cont-comp-threejs-evaluation.md`——连续 comp 随 M-D 立项（双模式并存，修改单元降级要明示）；three.js 缓议（需求未到+确定性门禁成本，重开条件已写死）。
  - **TTS 四音色 A/B 定档**：唯一遗留——SILICONFLOW_API_KEY 未配，配 key 后一次性出样人工定档。
- 基准夹具 data/projects/p-survival-bench 不入库（data/* 忽略），survival.md 报告随夹具保留在本机。
