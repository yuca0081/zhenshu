# 开源参考项目与借鉴映射

> 状态：2026-09-22。调研目的：渲染模块已有 HyperFrames，Agent 侧（分镜/模板生成、循环、自修复）
> 找成熟开源项目"抄架构不抄代码"。每个项目标注借什么、不借什么，映射到本仓方案的章节。
> 原则：所有借鉴服务于 §4.7 的 Agent 设计（ReAct + 状态机护栏），不推翻已定架构。

## 总映射表

| 我们的模块 | 参考项目 | 借什么 | 不借什么 |
|---|---|---|---|
| 主管 Agent 循环（§4.7） | **pi**（github.com/badlogic/pi-mono，TS，Apache） | 极简循环骨架、工具定义模式、事件流、分层包结构 | 权限系统（它没有；我们用状态机门禁替代） |
| 视频管线编排（§3.4） | **MoneyPrinterTurbo**（github.com/harry0703/MoneyPrinterTurbo，Python，MIT，56K★） | 分阶段可中断管线、TTS 时间戳双路径、LLM 供应商抽象、批量任务预校验 | 素材路线（图库拼接 ≠ 代码渲染，那是我们的差异化） |
| 合成物自修复循环（§4.5） | **OpenHands / gpt-engineer** 的 write-execute-fix 模式 | JSON 诊断回喂 → 定点修复的循环形态 | 其自主 agent 外壳（我们是固定循环 ≤3 次） |
| 逐词字幕实现（§4.1） | **script-to-video**（script→word-synced explainer，Kokoro，浏览器渲染） | Kokoro + 逐词时间戳消费的实现细节 | 整体架构（与我们验证片同路线，规模更小） |

## 1. pi —— Agent 循环的第一参考

关键事实：agent 循环核心 ~418 行 TypeScript；系统提示 + 工具定义 < 1000 token；
Terminal-Bench 与大型 agent 同场竞技；无内置权限/计划/子代理（用文件与外部工具替代）。

包结构（印证我们的分层）：

- `pi-ai`：统一多供应商 LLM API（OpenAI/Anthropic/Google…）⇄ 我们的 go-openai 薄封装
- `pi-agent-core`：agent 运行时（工具调用 + 状态管理）⇄ 我们的自写 ReAct 循环
- `pi-durable`：持久化对话/任务/文档运行时 ⇄ 印证 chat_messages 追加式事件流 + 文档版本化
- `pi-telemetry`：厂商中立遥测 ⇄ 我们的 run manifest + meta 成本记账

具体借鉴：

1. **循环骨架**：`收消息 → LLM → 工具调用 → 结果回填 → 再循环`，一个 while 搞定，不需要框架。
2. **工具定义**：schema 化 name/description/params，对齐 function calling；工具少而粗（它只有 read/write/edit/bash 四个核心）。
3. **两级事件流**：agent 级 / turn 级流式事件订阅 ⇄ 我们 SSE 的进度播报 + chat_messages 的 type 行。
4. **极简主义纪律**：它证明了"改进产品的方式是换提示词/计划，不是加代码"——与我们 §2.3 流程模型三层（硬约束/默认流程/AI 裁量）同源。

我们比它多的：状态机门禁（工具按项目状态发放，越界拒绝）；它刻意不做权限，沙箱外置。

## 2. MoneyPrinterTurbo —— 管线编排参考

关键事实：topic → 脚本 → TTS → 素材 → 字幕 → BGM → 成片，一键全流程；MIT。

具体借鉴：

1. **分阶段可中断管线**（`--stop-at <stage>`）：管线每步产物落盘、可从任意步续跑 ⇄ 我们"每步落盘可重放"的独立实现验证。
2. **TTS 时间戳双路径**：edge 模式（TTS 自带时间戳，快）vs whisper 模式（本地 ASR 反打，更准）⇄ 与"火山时间戳（路径A）/ funASR 兜底（路径B）"完全同构——**选型获得外部验证**。
3. **LLM 供应商抽象**：十余家供应商 + OpenRouter/OneAPI/LiteLLM 网关；我们是一个薄函数切 DeepSeek/GLM/Qwen（克制版，够用）。
4. **批量任务**：JSON/JSONL 批量、跑前预校验、单条失败不阻塞、末尾 JSON 汇总——M2+ 批量渲染的现成模式。

不借：素材策略（Pexels/Pixabay 图库拼接）——画面天花板低，我们的确定性 HTML 渲染正是差异化；
它的"一键发布到 TikTok/YouTube"暂不需要。

## 3. OpenHands / gpt-engineer —— 自修复循环模式

write-execute-fix：LLM 写码 → 执行/检查 → 结构化错误回喂 → 定点修复。
我们的变体：`生成合成物 → hyperframes lint/check（--json）→ 诊断回喂 → 修复（≤3 次）`。
区别：它们的循环由 agent 自主驱动，我们的修复循环是**写死的固定循环**（§4.7 内层），LLM 只在修复点介入。

重要印证（arXiv：Self-Improving AI Coding Agents Through Accumulated Corrections）：
coding agent 重复犯同类错误，根因是**缺少保留纠错记录的机制**——我们的方法库（§4.6 踩坑经验卡）
正是该问题的解：每次修复成功的 lint 问题自动沉淀 gotcha 卡，生成时注入 → 不重犯。
落地动作：自修复成功的案例进入 MethodNote 的候选源（用户确认入库）。

## 4. DeepSeek —— 澄清：是模型供应商，不是框架参考

DeepSeek 未开源 agent 框架。落地姿势：M0 bake-off 的对话/规划档选它的 V3/V4 系列
（官方 API 原生 function calling；R1 不支持 function calling，不选作主管 Agent 模型），
循环是我们自己的。api-docs.deepseek.com。

## 5. 其他顺带记录

- **ShortGPT**：面向 LLM 的"视频编辑语言"（editing DSL）思路有意思，但我们的"语言"就是 HTML 本身 + 引用卡片，更直接，不引入 DSL。
- **script-to-video**（GitHub）：script → 逐词同步解说视频，Kokoro 语音 + 浏览器渲染，与验证片同路线；M0 做逐词字幕时可对看实现。
- 检索中发现的相关文章：pi 作者复盘（mariozechner.at）、pi 循环解剖（ai.plainenglish.io / TorchTree）——写主管 Agent prompt 与循环时值得再读。
