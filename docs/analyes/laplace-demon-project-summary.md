# 《拉普拉斯妖》视频制作项目总结

> 日期：2026-10-13 · 工作区：`C:\Users\蔡雨\Desktop\video` · 项目：`videos/laplace-demon`
> 制作方式：ZCode agent + HyperFrames 引擎（general-video 工作流，全自主运行）

---

## 一、交付成果

**成片**：`videos/laplace-demon/renders/laplace-demon_2026-10-13_15-19-44.mp4`

| 项 | 值 |
|---|---|
| 时长 | 4 分 18.2 秒（258.2s，7746 帧 @30fps） |
| 画幅 | 1920×1080（16:9），H.264 + AAC，约 31 MB |
| 音频 | 人声 −16 LUFS（TP −1.5），BGM 垫底 0.16，响度峰值 −1.5 dB |
| 质量门禁 | `hyperframes check` 全绿（0 错误 0 警告，文字对比度 23/23 达 WCAG AA） |
| 视觉验收 | 三轮快照审查 + 成片抽样 5/5 帧 PASS |

### 影片结构（9 幕）

| 幕 | 时长 | 内容 |
|---|---|---|
| S1 冷开场 | 25.0s | "你此刻的每一个念头，可能早已被写定"——粒子汇聚成金色全知之眼，标题定场 |
| S2 诞生 | 30.5s | 1814 年拉普拉斯《概率的哲学随笔》，仿宋引文逐段浮现 |
| S3 钟表宇宙 | 34.7s | 里程碑横移：1687 牛顿引力 → 1758 哈雷彗星回归 → 1846 笔尖上发现海王星 → 嵌套齿轮巨钟 |
| S4 代价 | 30.7s | 10⁸⁰ 巨型计数器逐位跳涨、精度刻度尺无限细分、"镜子"意象 |
| S5a 第一击·热力学 | 28.5s | 分子云→统计直方图坍缩、追踪分子失败逃逸、熵箭头确立时间方向 |
| S5b 第二击·量子 | 25.5s | 波包被卡尺夹逼、Δx·Δp≥ħ/2、色差故障抖动、"初始条件不存在" |
| S5c 第三击·混沌 | 28.3s | 双轨迹指数发散、×10⁶ 误差读数、蝴蝶→风暴因果链 |
| S6 遗产 | 28.6s | 金眼回归，天气预报/航天轨道/蛋白质折叠/AI 四图标轨道环绕，镜头推入 |
| S7 终章 | 26.4s | 自由意志之问 → 金色逐字金句 → LAPLACE'S DEMON 落版 |

叙事弧线：钩子（宿命挑衅）→ 诞生 → 信仰 → 代价 → 三次死刑 → 遗产 → 回响。贯穿母题：全知之眼（S1 诞生、S6 回归）；章节视觉系统：Ⅰ/Ⅱ/Ⅲ 编号色带（金/青/紫）。

---

## 二、制作流程与关键技术决策

### 管线

```
BRIEF.md（意图）
→ STORYBOARD.md（分镜+设计令牌+节奏）
→ 配音：9 段中文 TTS + 响度归一（音频先行，实测时长驱动时间线）
→ BGM（ffmpeg 合成）+ SFX 选配（8 种）
→ 9 个场景子合成（compositions/frames/sN-*.html，手工 GSAP/Canvas/SVG）
→ 组装 index.html（场景轨 + 旁白轨 + BGM 轨 + SFX 轨）
→ lint → check（修复 2 处错误 + 若干警告）
→ 快照视觉审查 ×3 轮（多模态 agent，修复 6 处问题）
→ render（11m47s，硬件 GPU）→ 成片验证（流/时长/音量/抽样帧）
```

### 过程中解决的环境障碍（均已持久化到记忆）

1. **Node v24.14.0 的 `fs.cpSync` 原生损坏**（EIO，任何目标路径）：`hyperframes init` 因此失败。写 `cp-patch.cjs`（JS 递归复制实现替换），所有 CLI 命令经 `NODE_OPTIONS="--require .../cp-patch.cjs"` 注入。
2. **中国大陆网络约束**：OpenAI/Google/HuggingFace/hf-mirror/Edge TTS(403) 均不可用；百度/jsDelivr/ModelScope/清华 pip 镜像可用。由此推演出整套本地化媒体方案。
3. **TTS 选型（淘汰链）**：OpenAI TTS（连接超时 ✗）→ edge-tts（403 区域拒绝 ✗）→ 本地 Kokoro（需 HF 模型+espeak-ng ✗）→ Windows SAPI Kangkang（合成成功但语速 1/5、听感 3-4/10 ✗）→ **Piper `zh_CN-chaowen-medium`（ModelScope 镜像下载模型 + g2pw 本地化，QA 听审 7/10 ✓）**。语音链路：`piper_synth.py`（JSON manifest → 逐行 wav + durations.json）→ `ffmpeg loudnorm=I=-16:TP=-1.5:LRA=11`。
4. **BGM**：无可达曲库，用 ffmpeg 合成 D 小调暗色环境底噪（双段强度 + 交叉淡化 + 尾部淡出，268s→裁至 258.2s）。
5. **特效注册表**：`hyperframes catalog/add` 依赖的注册表主机超时，全部视觉特效改为手工确定性实现（种子化粒子系统、SVG 描线、轨道环、色差抖动、逐帧纯函数 canvas）。
6. **字体**：禁依赖 Google Fonts，取本地系统字体（SimHei 标题 / FangSong 引文 / DengXian Light 正文）+ 引擎内置 Oswald/Space Mono，TTF 复制进项目随构建内嵌。

### 三轮视觉审查修复的问题

| 轮次 | 发现 → 修复 |
|---|---|
| 第 1 轮（9 幕中点） | S4 计数器 `expEl` 未赋值（永远显示 10⁰）→ 补一行 + 增补"镜子"节拍填补 15–21s 视觉空洞；S5b 波包基线穿文字 → 年份块上移；S7 残留句压暗过度 → 提亮 |
| 第 2 轮（6 个补抽点） | 确认计数/镜子/四图标/穿字修复生效；S7 金句与残留句叠字是采样时机假象（逐字动画中途），仍微调残留句不透明度至 10–16% |
| 第 3 轮（终审 2 帧） | S5b 动量扩散条与文字带仍擦碰 27px → 整体下移 60px；终章金字 19 字完整、落版在位 ✓ |

---

## 三、与「帧述」（zhenshu）平台的对比

**核心发现**：帧述平台（`D:\workspace\develop\zhenshu`）本身就构建在 HyperFrames 引擎上（Go 编排 + `npx hyperframes render` 逐帧截图 + FFmpeg）。本次制作与平台是**同一技术底座上的两种生产形态**——它是"工厂 + CI"，我是"独立导演承包整片"。它钉 hyperframes@0.8.55，本次用 0.8.151。

### 相同点（同一种 DNA）

- 渲染内核一致：HTML `data-*` + GSAP paused timeline → 无头浏览器确定性逐帧渲染 → FFmpeg，30fps 1080p。
- 同一哲学："模型当创意总监直写场景代码"，平台/技能只管运行时契约。
- 音频先行：真实配音时长驱动时间线（它用 `SegTail=配音+0.35s`，我用实测 9 段时长排景窗口）。
- 同一套质检工具：`hyperframes check` + snapshot 抽帧审查为主门禁。
- 独立撞车：它的 `make_bgm.py`（numpy 合成和弦 pad 曲库）与本次 ffmpeg 合成 drone 几乎同构——都在"无可用音乐库"约束下收敛到程序化合成。

### 不同点

| 维度 | 本次（agent 工作流） | 帧述平台 |
|---|---|---|
| 生产形态 | 单会话单代理，9 景亲手写，整片一渲 | Go 流水线，每段独立 LLM 作业，段间并发 3（scenegen.go:62） |
| 中间产物 | STORYBOARD.md（自然语言+YAML） | 严格 JSON schema + `contract.ValidateStoryboard` 机械校验 |
| 设计系统 | 令牌写进 STORYBOARD，自写自守 | **独立阶段+唯一硬门**：六件套→样张→用户确认才放行，锁 skin.json 防漂移（design.go） |
| 质检 | check→快照审查 3 轮，循环由模型判断 | 五道闸（scene_lint 静态/check/确定性回放探针/渲染前预审/渲染后 frameQA）+ 固定轮数回喂 + 机械修复优先 + 失败回滚旧画面 |
| 字幕 | **未做** | 平台层机械装配：词级点亮、双语、对比度守卫，模型永不写字幕（assemble.py:222） |
| 音频对齐 | 行级时长，文字入场手工排 | FunASR 字级时间戳 + difflib 对齐，词级短语表喂场景 prompt |
| 重生成粒度 | 改一景=整片重渲 11 分钟 | 段级 sha256 指纹增量渲染，rework 只重做目标段，换 BGM 只重混 |
| 人的位置 | 全自主一句话到成片 | 样张确认硬门 + "引用此刻"对话式返工 |
| 可靠性 | 会话即状态（BRIEF/STORYBOARD 持久化） | manifest.jsonl 记账 + 断点续跑 + 自动重跑 ≤2 |

**对本机的实际影响**：平台渲染阶段同样会踩中 Node cpSync 损坏（Go 最终落到 `npx hyperframes`），在这台机器运行也需 `NODE_OPTIONS=--require cp-patch.cjs`；其云 TTS 级联（百炼→Edge）在本机只剩百炼一条腿（需 DASHSCOPE key），Piper 方案可作其第三级兜底。

### 互相借鉴清单

**平台 ← 本次制作（创意层与叙事层）**

1. **全片叙事弧线层**：概念 pitch、三幕结构、贯穿母题、金句收尾。平台分镜只到"每段 8–16s visual_brief"粒度（pipeline.go:373），建议 schema 增加 `arc`（全局情感曲线）与 `motif`（复用意象注册表）字段。
2. **SFX 语义布点**：音效跟叙事节拍（riser 配汇聚/impact 配三击/chime 配引文），而非按段边界机械布点。分镜 schema 加可选 `sfx_cue`。
3. **带"预期内容"的审查考卷**：本次审查按"此刻应该出现什么"能发现内容缺失（如 10⁰ bug 的根因是一行赋值缺失）；frameQA 考卷只查视觉缺陷不查内容缺失——把 visual_brief 注入审查 prompt 是廉价升级。
4. **复杂编排进方法库**：canvas 粒子聚形、跨景意象复用、章节色带系统可作 few-shot 收进 `data/library/methods/`，对抗金样例导致的风格保守化。

**本次 ← 平台（工程层，下次用 HyperFrames 应照搬）**

1. **字幕带**：本次最大缺陷（4 分钟无字幕）。采用"平台层按词级时间戳机械装配、模型不碰"的设计。
2. **词级对齐**：行级时长→词级锚点，kinetic 字幕节拍精确贴每个字，替代手工排时。
3. **机械优先修复链**：对比度建议色直接回填 CSS、JS 逐块 `node --check` 先于浏览器、只取阻塞行——机械修复在前、模型推理兜底，省整轮推理。
4. **段级增量渲染 + 失败回退**：改哪段重渲哪段；修复轮失败回滚旧画面、修不好标红不阻塞成片。
5. **BGM sidechaincompress 闪避**（threshold 0.035/ratio 7）替代固定音量垫底。

**一句话总结**：平台把 HyperFrames 当发动机造了一座带五道质检闸门的工厂，强在契约、增量、可靠性；本次像开着同款发动机的独立导演，强在叙事设计、创意编排、跨景呼应。互补路径：把本次的叙事弧线/预期考卷/语义音效喂进平台 schema；把平台的字幕带/词级对齐/机械修复链/段级增量搬进 agent 工作流。

---

## 四、复现与环境备忘

```bash
# 所有 hyperframes CLI 命令前必须（本机 Node cpSync 损坏）：
export NODE_OPTIONS="--require C:/Users/蔡雨/Desktop/video/cp-patch.cjs"

# 预览 / 校验 / 渲染
cd videos/laplace-demon
npm run dev        # Studio 预览
npm run check      # 质检门禁
npm run render     # 渲染 MP4

# 重新生成配音（改文案后）
py .media/piper_synth.py .media/lines.json   # 输出 .media/audio/lines/*.wav + durations.json
for f in .media/audio/lines/s*.wav; do ...   # loudnorm 归一到 assets/audio/vo-*.wav
# 之后同步更新 index.html 中各 vo data-duration 与场景窗口

# BGM 重新合成
bash .media/make-bgm.sh
```

关键路径：分镜 `STORYBOARD.md` · 意图 `BRIEF.md` · 场景 `compositions/frames/s*.html` · 总装 `index.html` · 配音模型 `.media/piper/`（含 g2pw，ModelScope 镜像下载）· 记忆（跨会话）：`~/.zcode/cli/memories/projects/video-62ce0202d61511ee/memory/`。

遗留可选项：① 若需顶级真人旁白质感，接入 HeyGen 登录或可用云 TTS key 后仅重生成音轨再重渲；② 当前无字幕轨，如需发布到B站/抖音建议按平台字幕带方案补一轮；③ `npx hyperframes usage` 在本环境不可读（unsupported_harness），配额消耗未跟踪。
