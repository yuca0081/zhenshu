# 分镜升级为镜头表：导演层方案（2026-10-30）

> 状态：**Phase 1 已落地**（2026-10-30 本轮：契约/API/③工位/前端/单测；Phase 2 的
> best-of-N 与 shot-aware 审核未做）。后端实验代码仍未提交。
> 关联：`docs/plan-v2.md`（总纲）、`docs/para-transition-plan-2026-10-29.md`（段落制）。
> 现场：`data/projects/p20261030-*`（本地测试项目，不入库）。

---

## 0. 一句话

平台已经有「文稿」和「分镜」，但现在的「分镜」是**段级内容拆解**（这段讲什么、大概什么画面、首末拍、时长），不是**镜头级调度**（谁在画面里、什么景别、机位在哪、镜头怎么动、角色做什么动作、怎么切）。

这轮实验证明：**同一个模型、同一个渲染器、同一个风格，只补一张镜头表，画面就能从「图标+字卡」变成「有腿的角色+舞台+观众席+机位变化」。**

所以方案不是「再加一个导演部门」，而是：

> **把现有分镜从「段级画面提示」升级成「镜头级调度表」；第一版 1 镜 = 1 段；再加「每个镜头拍 2–3 条、挑最好」的选片。**

---

## 1. 问题与证据

### 1.1 用户反馈

> 「只是画风像了，但是画面、运镜、故事完全比不上。」

### 1.2 参考片 vs 我们的片

参考片（BV1yPp96UEZU，本次风格来源）是**角色驱动的纸剧场寓言**：

- 角色有身体、腿、眼睛、表情、台词；
- 有舞台、幕布、聚光灯、观众席、屏幕、道具；
- 有全景/近景/观众视角/舞台视角的切换；
- 故事有村落、长老、比赛、失败、转折、结尾。

我们的第一版是**配了图的议论文**：

- 两个三角当图标；
- 条形图、字卡、标题；
- 固定全景，元素淡入/滑入；
- 没有角色行动，没有场景切换，没有对话，没有镜头语言。

### 1.3 现有「分镜」为什么不是导演

代码事实：

- `VisualBrief` 只有三个字段：`scene / first_frame / last_frame`
  （`server/internal/contract/prep.go:265`）。
- `StoryboardDraft` 的注释是「纯内容，不出现任何缝/转场/机制概念」
  （`server/internal/contract/prep.go:281`）。
- 一个分镜（`Segment`）= 一张 HTML + 一条 timeline；段落制下渲染单元可以合并
  多个分镜，但仍是一个 HTML/一条 timeline（`paraUnitPrompt`「从段落第一帧编排
  到最后一帧」，`server/internal/produce/paraprompt.go:66`）；**段/单元内没有切
  镜头概念**，切只发生在段与段、单元与单元之间。
- `Segment` 有旁白、画面提示、时长、`Beats`；`Beats.Expect` 是「观众此刻
  该看到什么」，不是「镜头怎么拍」。
- 审核是 content / device / first_last，奖励「元素都在、首末帧合规」，
  不奖励「这是不是一镜戏」。

平台有 `Arc / Motifs / Beats`，那是**叙事导演**（故事弧线、母题、节拍），
不是**视觉导演**（镜头、调度、表演）。两者容易混，但作用不同。

### 1.4 结论

缺的不是渲染器、不是模型、不是 IR，而是两层：

1. **镜头级信息**：景别、机位、运镜、舞台、角色动作、台词、转场、参考帧。
2. **选片**：一个镜头只生成一次，好坏全靠掷骰子；需要「拍 2–3 条、挑最好」。

---

## 2. 术语与粒度决策

| 词 | 代码 | 是什么 |
|---|---|---|
| 文稿 | `Script` / `scripts/script.json` | 旁白、句子、事实；回答「说什么」 |
| 段落 | `DraftParagraph` | 2–5 个分镜的话题分组；只有主题，无画面 |
| 分镜（旧） | `Segment` / `DraftSegment` | 旁白块 + 一个 HTML + 一条 timeline；段级内容拆解 |
| 镜头（新） | `Shot` | 3–10 秒的调度单位；回答「怎么拍」 |
| 镜头表 | `Segment` 增加镜头字段（一期） | 每段一条镜头调度；二期可段内多镜 |

### 粒度决策

**一期：1 镜 = 1 段。** 不引入段内多镜、不引入 cut/camera IR。

理由：

- 现有渲染/缓存/审核单位就是「段」；实验里 5 段 = 5 镜，已经能出戏。
- 段内多镜需要「一条 timeline 里切镜头 + 摄影机变换」，是二期的事；
  一期先把「每段有导演调度」跑通。
- 前端也因此简单：在现有分镜表每行加字段，不做嵌套镜头编辑器。

**二期：段内多镜。** 只在长镜头/连续调度不够用时引入；届时加 `shots[]`、
`cut` 事件、`camera/group` IR。

---

## 3. 本轮已经做过的修改

> 全部在本地工作区，未提交；`go test ./...` 全绿。

### 3.1 SceneIR + 编译器 + 局部 patch（先做的降本方案）

新增：

- `server/internal/contract/sceneir.go`：SceneIR 结构 + 校验（元素/cue/时间范围/
  目标存在性/T9 CSS transform 冲突）。
- `server/internal/produce/scenecompile.go`：IR → `<template>` + GSAP timeline 的
  确定性编译器；`set_cue / add_cue / remove_cue / set_element / add_element /
  remove_element` 六种局部 patch。
- `server/internal/produce/sceneir_prompt.go`：IR 生成/修复提示词、上一轮 IR 读取。
- `server/internal/produce/ir_smoke_manual_test.go`：真实模型冒烟（`IR_SMOKE=1`）。
- 测试：`sceneir_test.go`、`scenecompile_test.go`。

改：

- `server/internal/produce/scenegen.go`：项目级 `scene_ir=1` 时首写走 IR；
  IR 校验不过先给一轮改动单修复；仍不过自动回退 HTML。
- `server/internal/pipeline/pyexe.go`：补 `python3` 解析（部署镜像只有 python3）。
- `server/internal/pipeline/design.go`：`project.json` 的数值/布尔字段也能被
  `ReadProjectField` 读到（避免 `scene_ir: 1` 静默失效）。

结论：

- IR 解决的是**改稿/修复成本**（改一行 cue，不重抄整段），不是「画面平」的
  主因；暂缓为二期能力，代码保留。
- 第一版 104 秒片的最终产物其实退回了 HTML（`llm/comp-*.json` 无 `ir` 字段，
  history 的 user prompt 是 HTML 版）；`scene.ir` 只是首轮日志。
- 修复轮曾因模型回 `target:"##axis"` 打不中而整段回退；已加 `#` 归一化与测试。

### 3.2 镜头表注入（shot brief）

新增：

- `server/internal/produce/shotbrief.go`：`ShotPlan`（世界/角色表/镜头列表）+
  `promptBlock`（整片镜头表 + 本镜景别/机位/舞台/角色动作/台词/转场/参考帧 +
  硬要求）。

改：

- `server/internal/produce/scenegen.go`：有 `storyboards/shots.json`（或
  `shot_brief=1`）时，把导演指令块注入到 `sceneUserPrompt`/`paraUnitPrompt`
  最前面；`shots.json` 指纹按段并入场景缓存指纹（改某镜只过期该段）。
- 实验项目带 `shot_brief=1`，`unit=seg`（一段=一镜）。

硬要求（已写入 `shotbrief.go`）：

- 角色必须有身体（两条细腿）、眼睛、嘴；本镜要真的站/走/指/退/转头。
- 舞台至少两层前后景（幕布/聚光灯/观众席/屏幕/桌子/纸纹地面）。
- 观众/村民也是三角 + 细腿 + 眼睛；前景可压暗，但禁止圆角矩形/黑块。
- 标题/字卡放舞台上方纸带安全区；画面顶部不再放标题/章节名（片尾字卡除外）。
- 桌子按真实比例（桌板 22px、腿 12px/200px），不要细长木棍。
- 每镜至少两个角色同场（纯空镜除外），要有看向/指向/退让/转头的关系。
- 本镜必须有可感知的机位/景别/运镜变化，或镜头内切一次；禁止整段静态卡片。

### 3.3 实验与结果

| 编号 | 项目 | 时长 | 改动 | 终审折算 | token | 调用 | 墙钟 |
|---|---|---|---:|---|---:|---:|---:|
| E1 | `p20261030-144338` | 103.9s | 旧管线（无镜头表） | 4.8 | 91.5 万 | 148 | 37 分 |
| E2 | `p20261030-155230` | 49.7s | 第一版 5 镜镜头表 | 4.8 | 15.3 万 | 24 | 8.6 分 |
| E3 | `p20261030-160500` | 51.7s | 修观众/标题 | 4.2 | 18.2 万 | 18 | 10.5 分 |
| E4 | `p20261030-162000` | 55.6s | 去顶部标题 + 片尾字卡 | **5.0** | 27.3 万 | 26 | 12 分 |
| E5 | `p20261030-163500` | 55.6s | 定点修 seg02/03 | 4.5 | 25.9 万 | 23 | 9.4 分 |

> 「终审折算」是平台 `style.usage` 记账分，不是电影质量分；只作横向参考。

结论：

- E2/E4 明显从「PPT」变成「有角色/舞台/机位」的戏；镜头表是有效杠杆。
- E4 整体最好（5.0），但 seg03 桌面戏太空。
- E5 修了 seg03，却把 seg04 的「空舞台」故事点弄丢、片尾字卡变弱（4.5）。
- **单次生成不稳定**：全片重掷会「修一镜、坏另一镜」；需要 best-of-N 选片。
- E1 的 91.5 万 token 里，风格工位 31.4 万、审核 20.9 万；镜头表实验因为复用
  风格、关闭段审，边际成本才降到 15–27 万/片。

### 3.4 小修

- `pipeline.PythonExe`：补 `python3`（Debian 容器只有 python3，没有 python 别名）。
- `pipeline.readProjectField`：接受 string/bool/number。
- `sceneSidecarFingerprint`：并入按段的 `shots.json` 指纹（改一镜只过期该段）。
- 实验期 `PRODUCE_SEG_AUDIT=off`：现有段审的 content/first_last 会把「有戏但
  首末帧不完美」的镜头打回成静态卡片，先关；最终审保留。

### 3.5 Phase 1 落地记录（2026-10-30 本轮）

已实现（`go test ./...` 全绿，前端 `vue-tsc`/`vite build` 通过）：

| 层 | 文件 | 落地内容 |
|---|---|---|
| 契约 | `contract/prep.go`、`contract/types.go` | `ShotDirection/ShotCast`、`ShotGate`、`ValidateDraftShots`；`Segment/DraftSegment.Shot`；`Storyboard/DraftSegment.World/CastLegend`（全片世界/角色表，③产出、导演块复用）；`Translate` 深拷贝镜头 |
| ③ | `prep/storyboard.go` | 提示词升级为「分镜师兼视觉导演」；`shot` 进工具 schema（景别/机位/舞台/角色/动作/台词/转场/参考帧）；validate/submit 要求每段镜头完整 |
| API | `api/server.go` | `saveStoryboard` 规整/保留镜头并**回写 draft.json**（终审与分镜页同源，允许边补边存）；`prepView` 返回 `shot_gate`；`confirmPrep` 硬门（启用镜头层即必须 100%，老项目 ready=0 不拦） |
| 生成 | `produce/shotbrief.go`、`scenegen.go`、`units.go` | 导演块改从 `Segment.Shot` 读取（`shots.json` 降级为实验/导入）；镜头字段进段级场景指纹；有镜头层时强制 `unit=seg`（1 镜=1 段） |
| 前端 | `web/src/types.ts`、`StagePanel.vue`、`PrepPane.vue` | 分镜表「镜头」列 + 展开编辑（景别/机位/舞台/角色增删/动作/台词/转场/参考帧）；终审镜头板、完整性徽章、缺镜提示、确认按钮禁用与后端门呼应 |

关键取舍：

- **双源收口**：分镜页写 `storyboard.json`，终审读 `draft.json`——保存时按段 id
  回写镜头，避免「补好镜头但终审还报缺」。
- **老项目零感知**：`ready=0` 视为未启用镜头层，不显示缺镜、不拦确认；一旦
  任一段有镜头，就要求全片完整。
- **相机/切镜 IR 仍留二期**：一期每段仍是一张 HTML/一条 timeline，只是提示词
  里有了镜头调度；段内多镜需要 `cut`/`camera` IR，未在本轮做。

---

## 4. 目标方案：一期「1 镜 = 1 段」

### 4.1 数据模型

一期不新增嵌套数组，直接在 `Segment` / `DraftSegment` 上加镜头字段：

```go
// contract.ShotDirection（内嵌到 Segment / DraftSegment）
type ShotDirection struct {
    Size       string     `json:"size"`        // wide|medium|close|overhead|full
    Camera     string     `json:"camera"`      // 机位/运镜
    Stage      string     `json:"stage"`       // 舞台/道具/前后景
    Cast       []ShotCast `json:"cast"`        // 谁、姿态、情绪
    Action     string     `json:"action"`      // 这一镜发生什么
    Dialogue   string     `json:"dialogue"`    // 短台词/字卡
    Transition string     `json:"transition"`  // 与下一镜接法
    RefFrame   string     `json:"ref_frame"`   // 项目内相对路径（可空）
    RefNote    string     `json:"ref_note"`    // 参考帧说明
}

type ShotCast struct {
    Who     string `json:"who"`
    Pose    string `json:"pose"`
    Emotion string `json:"emotion,omitempty"`
}

// Storyboard / StoryboardDraft 顶层（本轮落地时补的共享层）
World      string `json:"world,omitempty"`       // 全片世界/舞台设定
CastLegend string `json:"cast_legend,omitempty"` // 全片角色表（外形/身体/眼睛/性格）
```

约束：

- 一期：每段 1 镜；段长 3–10 秒；超长旁白在分镜阶段拆段。
- `size/camera/stage/action` 必填；`cast` 1–4；`dialogue` 可空。
- `world/cast_legend` 进导演块，保证同一角色跨镜不变形；老项目为空串。
- 旧项目无字段 → 走旧路径，零感知。

### 4.2 生成：分镜工位升级

`prep` ③（`prep/storyboard.go`）输出从「段级画面提示」升级：

1. 先定故事脊柱（弧线/装置/段节拍）；
2. 每段拆成 1 个镜头（3–10 秒），写 `ShotDirection`；
3. 角色表/世界设定全片复用；
4. 参考帧从风格库/参考片抽，写 `ref_frame/ref_note`；
5. rubric 自检：可画性、动作性、角色反应、镜头变化、首末拍。

### 4.3 场景生成

- `sceneGen` 从 `Segment` 读取镜头字段，注入「导演指令块」；
- 不再以 `storyboards/shots.json` 为唯一事实源；`shots.json` 仅保留为实验/
  导入导出格式；
- **有镜头层时强制 `unit=seg`**：段落制会把多个分镜并进一个 HTML，一镜=一段
  的前提不成立；已有段落单元缓存在此情形自然不再命中。
- 每段仍是「一个 HTML + 一条 timeline」，所以现有渲染/缓存/审核/拼接零改动；
- 镜头指纹按段并入 `sceneFingerprint`（改景别/机位/舞台/动作只过期该段）。

### 4.4 人工门

- `confirmPrep` 硬门：每段镜头字段完整才许确认；
- 用户在「分镜」页签直接改镜头字段；
- 确认后锁定；改镜头需重新打开终审（或走反馈重跑）。

### 4.5 审核

- 一期：段审继续可关；终审保留；
- 二期：加 shot-aware 检查（机位/景别变化、角色动作、反应、静态卡时长），
  不再用「元素都在」奖励静态卡片。

---

## 5. 二期：每镜 best-of-N + 选片

问题：一次生成好坏随机；E4/E5 就是证据。

做法：

1. 每个关键镜头生成 2–3 个候选（同 shot brief，不同 seed/温度）。
2. 每个候选渲 2–3 帧。
3. 多模态判卷按 shot brief + 参考帧打分：
   - 角色是否在场、有腿有表情；
   - 景别/机位是否符合；
   - 该镜的戏是否发生（谁看谁、谁退、谁指）；
   - 屏幕/道具内容是否正确（如「空舞台」）。
4. 取最高分；人可在门 2 覆盖。
5. 只对失败镜重掷，不整片重来。

成本：每镜多 2 个候选 ≈ 多 5–8 万 token/片，远低于整片重掷（25–27 万）。

---

## 6. 前端改造

### 6.1 原则

- **不新增「导演」页面**；镜头就是分镜表的下一层字段。
- 一期 1 镜 = 1 段 → 现有分镜表每行加字段，不做嵌套镜头编辑器。
- 二期段内多镜 → 再加 `shots[]` 与嵌套卡。

### 6.2 页面/组件

**「分镜」页签（`web/src/components/StagePanel.vue`）**

现有表：段 / 标题 / 旁白 / 画面 / 秒。

一期改为：段 / 标题 / 旁白 / **镜头** / 秒；「镜头」列预览
`中景 · 横摇`，点开展开编辑区：

- 景别（NSelect：全景/中景/近景/俯拍/字卡）
- 机位/运镜（NInput）
- 舞台（textarea）
- 角色（可增删 who/pose/emotion）
- 动作（textarea）
- 台词（input）
- 转场（input）
- 参考帧（缩略图 + 路径）
- 「保存修改」仍走现有 `saveStoryboard`，body 带镜头字段；`sbDirty` 置脏，
  保存后成片过期灯亮。

**「终审」页签（`web/src/components/PrepPane.vue`）**

在「③ 分镜稿」表格下加「镜头板」：

- 胶片条：每段一个缩略图（`ref_frame` 或 style 样张），横向滚动；
- 镜头卡：`镜1 · 中景 · 横摇` + 舞台/角色/动作/台词；
- 完整性：`14 段 / 14 镜`，缺字段标黄；
- 「确认打包」在镜头不完整时禁用（后端也拦）；
- 每张卡「编辑镜头」→ 跳到分镜页签并展开对应段。

**时间轴（二期）**

- 段内多镜时在段块里画镜头刻度；hover 显示景别/运镜；点击 seek。

#### 线框（一期）

```
「分镜」页签
┌────┬──────────┬──────────────┬───────────────────────┬─────┐
│段  │标题      │旁白          │镜头                   │秒   │
├────┼──────────┼──────────────┼───────────────────────┼─────┤
│ 3  │手艺三步  │他的手艺，就是│中景 · 横摇        ▾  │ 12  │
│    │          │把它们拼起来… │┌─────────────────────┐│     │
│    │          │              ││景别[中景] 机位[从…] ││     │
│    │          │              ││舞台[木桌/屏幕/时钟] ││     │
│    │          │              ││角色[AI 俯身 高效][+]││     │
│    │          │              ││动作[脚尖连点三下…]  ││     │
│    │          │              ││台词[十块钱,十分钟]  ││     │
│    │          │              ││参考帧[缩略图]       ││     │
│    │          │              │└─────────────────────┘│     │
└────┴──────────┴──────────────┴───────────────────────┴─────┘

「终审」页签
③ 分镜稿  14 段 / 14 镜         [镜头完整性 14/14]
胶片条：[镜1][镜2][镜3][镜4]…（横向滚动，点开看大图）
段03 手艺三步
┌ 镜1 · 中景 · 横摇 ──────────────────────────────┐
│ [参考帧]  舞台：木桌/屏幕/时钟                   │
│           角色：AI(俯身) 村民(围拢)              │
│           动作：脚尖连点三下，画面格飞进废纸篓    │
│           台词：十块钱，十分钟                   │
│                                     [编辑镜头]   │
└──────────────────────────────────────────────────┘
```

一期不做段内多镜，所以表里每行就是一个镜头；二期再加“段内多镜”和嵌套卡。

### 6.3 类型/API

`web/src/types.ts`：

```ts
export interface ShotCast { who: string; pose: string; emotion?: string }
export interface ShotDirection {
  size: 'wide' | 'medium' | 'close' | 'overhead' | 'full'
  camera: string
  stage: string
  cast: ShotCast[]
  action: string
  dialogue?: string
  transition?: string
  ref_frame?: string
  ref_note?: string
}
export interface Segment {
  // 现有字段...
  shot?: ShotDirection   // 一期：1 镜/段
}
```

`web/src/api.ts`：`saveStoryboard` 的 `Storyboard` 类型自动带上 `shot`；
无需新端点。

### 6.4 后端配套（必须先做）

- `contract.Segment` / `contract.DraftSegment` 加 `Shot *ShotDirection`；
- `saveStoryboard` 规整/保留并落盘，同时回写 draft.json（完整性交 confirmPrep
  的镜头门；未知字段会被 Gin 静默丢弃，所以必须显式接住）；
- `prepView` 返回 `draft.segments[].shot`；
- `confirmPrep` 加完整性硬门；
- `sceneGen` 从 `Segment.Shot` 读取（`shots.json` 仅实验/导入）；
- `sceneSidecarFingerprint` 按段纳入 shot 指纹。

### 6.5 分期

| 期 | 内容 | 验收 |
|---|---|---|
| A | 后端 shot 字段 + 透传；终审/分镜只读镜头卡 | 能看到 `14 段 / 14 镜`，字段齐全 |
| B | 分镜 inline 编辑、保存、确认门 | 改一镜能保存；缺字段不能确认 |
| C | 时间轴镜头刻度、重做这一镜、best-of-N 候选 | 只重做失败镜；候选可选 |

---

## 7. 后端改造清单

| 文件 | 改动 |
|---|---|
| `server/internal/contract/types.go` | `Segment.Shot *ShotDirection` |
| `server/internal/contract/prep.go` | `DraftSegment.Shot`、`ShotDirection/ShotCast` |
| `server/internal/api/server.go` | `saveStoryboard` 规整/回写（允许边补边存）；`prepView` 透传 `shot_gate`；`confirmPrep` 硬门 |
| `server/internal/prep/storyboard.go` | ③ 工位产出镜头字段；rubric/自检 |
| `server/internal/prep/artifacts.go` | 翻译 draft → storyboard 时复制 shot |
| `server/internal/produce/scenegen.go` | 从 `Segment.Shot` 生成导演块；指纹 |
| `server/internal/produce/shotbrief.go` | 保留为 prompt 构造器；数据源改为 Segment |
| `server/internal/review/*` | 二期 shot-aware 检查 |

兼容：

- 旧项目无 `shot` → 走旧 `sceneUserPrompt`/`paraUnitPrompt`，行为不变；
- `shots.json` 实验路径保留，作为导入/导出；新项目以 Segment 为准。

---

## 8. 路线图与验收

### Phase 0（已完成）

- 实验证明「镜头表」有效；E4 最好（5.0），E5 证明单次生成不稳定。
- 代码：IR pilot、shot brief、per-seg 指纹、python3、scalar field。

### Phase 1（代码已完成，待真实跑片验收）

- 后端：`ShotDirection` 契约 + `saveStoryboard`/`prepView`/`confirmPrep` +
  `sceneGen` 消费 —— 已落地；
- 前端：分镜行镜头字段 + 终审镜头板 + 确认门 —— 已落地；
- 产出：用真实 prep 自动生成镜头表（不再手写 `shots.json`），跑一条 60 秒片
  —— **尚未跑**（本轮只做代码与单测）。

验收：

- 每段 1 镜，字段完整率 100%；
- 前端能改镜头并保存，确认门拦得住缺字段；
- 成片有角色/舞台/机位，不再是固定全景图标；
- 单镜一次过率 > 70%（人工看片，5 分制 ≥ 4）；
- 成本 ≤ 30 万 token/分钟（含 prep）。

### Phase 2

- best-of-N 选片（2–3 候选/镜）；
- director 自动生成镜头表（强模型 + 参考帧）；
- shot-aware 审核；
- 时间轴镜头刻度、重做这一镜。

### Phase 3

- 段内多镜 + `cut` + `camera/group` IR；
- 镜头级缓存/批量变体；
- 成本档案（每分钟 token/金额）。

---

## 9. 风险与开放问题

1. **一镜=一段 vs 段内多镜**：一期牺牲长镜头连续性，换实现简单；二期再补 cut。
2. **TTS 粒度**：镜头变短 → 段变多 → TTS 调用/字幕段变多；需要确认 TTS 成本
   与字幕可读性。
3. **模型稳定性**：E4/E5 已证明单次生成不稳；best-of-N 是必需品，不是优化项。
4. **参考帧资产**：`ref_frame` 存项目内路径，需考虑版权/清理/大小；只作参考，
   不直接进成片。
5. **审核冲突**：现有段审 content/first_last 会把「有戏但首末帧不完美」的镜头
   打回；要么先关段审，要么重写为 shot-aware。
6. **前端复杂度**：分镜表已很宽；镜头字段建议用展开行/抽屉，不要把所有字段
   塞进表格列。
7. **兼容**：旧项目无 shot 字段必须零感知；`shots.json` 与新 Segment 双源要
   明确「Segment 为准」。

---

## 10. 附录

### A. 实验 shots.json 形态（E4/E5）

```json
{
  "world": "纸剧场「剪辑村」：米纸底、黑色地平线、聚光灯、观众席、旧屏幕、木桌……",
  "cast_legend": "剪辑师：炭黑三角，两条细腿，圆点眼+短横嘴……",
  "shots": [
    {
      "seg": "seg03",
      "title": "十分钟交片",
      "size": "close",
      "camera": "从 AI 单眼特写推近桌面，再横移到屏幕",
      "stage": "木桌/键盘/小屏幕/铜时钟；桌板 22px、桌腿 12px/200px",
      "cast": [
        {"who": "AI", "pose": "俯身，脚尖点地", "emotion": "高效、机械"},
        {"who": "村民", "pose": "从两侧围拢", "emotion": "好奇"}
      ],
      "action": "AI 脚尖连点三下，画面格飞进废纸篓；成片推上屏幕",
      "dialogue": "十块钱，十分钟",
      "transition": "成片亮起的白闪匹配切到观众视角",
      "ref_frame": "tmp/ref/r80.png",
      "ref_note": "参考 80s：桌面近景+暗部包围"
    }
  ]
}
```

### B. 现场项目

| 项目 | 说明 |
|---|---|
| `p20261030-144338` | 旧管线 104s（风格皮） |
| `p20261030-155230` | 第一版 5 镜 49.7s |
| `p20261030-160500` | 观众/标题修正 51.7s |
| `p20261030-162000` | 去顶部标题 + 片尾卡 55.6s（5.0） |
| `p20261030-163500` | 定点修 seg02/03 55.6s（4.5） |

### C. 相关文件

- 方案：本文；`docs/plan-v2.md`；`docs/para-transition-plan-2026-10-29.md`
- 后端：`server/internal/contract/sceneir.go`、`scenecompile.go`、
  `sceneir_prompt.go`、`shotbrief.go`、`scenegen.go`
- 前端：`web/src/components/StagePanel.vue`、`PrepPane.vue`、`types.ts`
- 实验：`server/internal/produce/ir_smoke_manual_test.go`、
  `scenecompile_test.go`、`sceneir_test.go`

### D. 验证命令

```bash
# 后端单测
cd server && go test ./...

# 编译
cd server && go build ./...

# 手动 IR 冒烟（真实 LLM）
cd server && IR_SMOKE=1 go test ./internal/produce -run TestSceneIRSmoke -v
```
