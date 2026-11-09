# 统一准备智能体设计（文稿+风格+分镜 一个 agent 一条循环）

状态：设计稿（2026-11-09）· 未实施
前身：docs/prep-agents-design.md（三工位 v3.1，现行）、docs/director-layer-plan-2026-10-30.md

## 1. 背景与动机

现行准备链是三个独立 ReAct agent 前馈串联：①文稿 → ②风格 → ③分镜，跨站只有
`get_script` / `get_style_package` 两条只读窄通道。三个问题：

1. **创作割裂**：①写文稿不知道②会选什么风格；②选风格不知道③会怎么切镜；
   ③发现节奏问题改不了①。相互影响只能靠人工终审发现问题后整链反馈重跑。
2. **接口即政策**：工位接口（系统提示词+schema）会沉淀创作政策。2026-11-09
   「角色强制令」事故（③分镜提示词硬性要求每镜两角色，新闻盘点片被迫画小人）
   是最新案例——政策写死在接口里，过时了要外科手术拆除。
3. **模型能力已到位**：1-3 分钟片三件套全文不过几千字，单上下文装得下。

用户定调（2026-11-09）：

> 给这个智能体所需要的工具，写好系统提示词，尤其是最后的产物要写好。
> 创作决定（要不要角色、什么风格、怎么切镜）全部由模型根据文稿与风格自己拿。

即：**过程放开（一个 agent 一条循环），产物焊死（契约+指纹+过闸函数）**。

## 2. 目标与非目标

目标：
- 一个 ReAct agent 单上下文顺序产出 文稿→风格→分镜，阶段间天然共享全部上下文；
- 允许**回写**：分镜阶段可以回头改文稿/换风格，指纹机制只过期受影响的工件；
- 下游零改动：落盘工件、打包终审（唯一人工门）、produce 段级流水线全部不变。

非目标：
- 不动 ②的样张渲染-判卷-修复子循环（render_sample 内部机制原样保留，它本质
  是嵌套子过程，agent 只看到结论摘要）；
- 不替代人工终审；
- 不承诺修复与工位划分无关的缺陷（字幕塌方、封面工具等另行处理）。

## 3. 总体形状

```
prepRunner（runner.go 分支：project.unified_prep=1 走新链，否则老三站链兜底）
  └─ unifiedAgent（loop.go 现成 ReAct 循环，MaxRounds 合并三站预算）
       阶段A 文稿   → checkpoint A：submit_script        → scripts/script.json
       阶段B 风格   → checkpoint B：submit_style         → llm/style_samples.json（StylePack）
       阶段C 分镜   → checkpoint C：submit_storyboard    → storyboards/draft.json + storyboard.json
       回写环：C 期间可调 revise_script / revise_style（≤各 1 次，帽见 §7）
```

- 阶段边界只是**检查点**不是上下文墙：C 阶段的模型天然记得 A 的每一句、B 的每一次样张判卷。
- 每个 checkpoint 复用现行过闸函数（§6），闸不过工具返回错误回喂，模型自行修复。
- 崩溃/取消续跑：按已落盘 checkpoint 续起（与老链「已完成工位走缓存」同语义）。

## 4. 工具清单（union 现有三站 12 个工具，不新造轮子）

| 阶段 | 工具 | 来源 | 改动 |
|---|---|---|---|
| A 文稿 | read_source | ①script.go | 原样（原始稿+材料拼接） |
| A 文稿 | web_search / fetch_page | ①script.go | 原样（主题模式检索） |
| A 文稿 | estimate_speech_duration | ①script.go | 原样（口播时长自检） |
| A 文稿 | submit_script | ①script.go | 过闸后**继续会话**不终止 loop（老链 submit 即站终） |
| B 风格 | get_script | ②style.go | 返回体并入想法文本（ideaForPeers，本日已改） |
| B 风格 | search_styles / get_style_detail / register_style | ②style.go | 原样（含本片临时条目方案乙） |
| B 风格 | render_sample | ②style.go | 原样（嵌套渲染-判卷-修复子循环，agent 只见结论） |
| B 风格 | submit_style | ②style.go | 过闸后继续会话 |
| C 分镜 | get_style_package | ③storyboard.go | 改为读会话内已确认的风格包（不再落盘回读） |
| C 分镜 | estimate_speech_duration / validate_storyboard | ③storyboard.go | 原样 |
| C 分镜 | submit_storyboard | ③storyboard.go | 原样（loop 唯一终止出口） |
| 回写 | revise_script / revise_style | 新增薄壳 | 参数=完整新工件 JSON；过闸+落盘+按指纹过期下游（§8）；帽内免费用 |

设计纪律：工具 description 只写**怎么用**（参数/返回/闸），不写创作方针。
创作方针只允许出现在系统提示词的「任务」一句话里，其余交给模型。

## 5. 系统提示词（草案全文——本设计的核心交付物）

```text
你是这支视频的编导负责人，对文稿、视觉风格、分镜三件产物负全责。用户给了想法
（get_script 里的「用户想法」）与材料，你按 A 文稿 → B 风格 → C 分镜 顺序工作，
每个阶段以一次 submit 收尾：submit_script、submit_style、submit_storyboard。
三件都要过闸才能交包，闸会给出具体错误，照着修到零错误再交。

各阶段要点：
- A 文稿：先 read_source 吃透原始稿与材料（主题模式用 web_search 检索），口播
  时长用 estimate_speech_duration 自检对齐目标时长。硬事实逐条给出来源。
- B 风格：search_styles 有筛选地浏览，render_sample 出样张并按判卷意见修复，
  视觉上站得住再 submit_style。剪辑语法（密度/段长带）要服务文稿的节奏。
- C 分镜：用 estimate_speech_duration 校段长，validate_storyboard 修到零错误，
  submit_storyboard 收尾。分镜要与你在 B 定下的风格同一视觉词汇。
- 回写：C 期间发现文稿节奏或风格选择撑不住画面，可用 revise_script /
  revise_style 返工（各至多一次），改完重新过对应闸再继续。不要为改而改。

画面里出现什么、怎么表演、有没有角色形象，由你根据题材、文稿口吻与风格气质
决定；定了角色就在 cast_legend 写清外形/身体/眼睛/性格并全片保持一致。
除本提示词列出的流程与契约外，没有任何附加创作限制。
```

要点说明：
- **零创作政策**：不出现「每镜至少两个角色」「禁止静态卡片」这类条款（前者已
  证明过时，后者降格为产物闸与制作侧导演块的职责，见 §6/§9）；
- 角色只用一句中性契约带过（「定了角色就写清并保持一致」），把决定权完全交给模型；
- 流程纪律（submit 收尾、闸修复、回写帽）是唯一被强调的东西——这正是用户说的
  「写好系统提示词」的分寸：**写过程与合同，不写品味**。

## 6. 产物契约（焊死的三件——字段级）

下游/终审/produce 链认文件不认过程，三件产物与现行完全同构：

### checkpoint A：`scripts/script.json`（contract.Script）
| 字段 | 契约 | 过闸 |
|---|---|---|
| source_mode | topic \| manuscript | — |
| text / sentences[] | 全文 + 编号句 N1..Nn（原生句终符，拼接还原正文） | ValidateScriptText |
| fact_sheet[] | 断言→URL，硬事实逐条溯源 | — |
| word_count / target_seconds | target_seconds 缺省 120s，想法里「X分钟」已由 applyIdeaDuration 预写 | EstimateSpeechDuration ≤ MaxDurationSec |
| fingerprint | 内容指纹（narrationFingerprint 上游） | — |

### checkpoint B：`llm/style_samples.json`（contract.StylePack，Confirmed 由终审写）
| 字段 | 契约 | 过闸 |
|---|---|---|
| style_id | 库内条目或本次衍生登记 id | ValidateStylePack |
| description | 风格描述+选择理由（供分镜与导演块理解语境） | 非空 |
| grammar | density / continuity / band_min·band_max（段长带，ClampedBand 有交集） | ValidateStylePack |
| design_system | 皮肤、字体三角色、动效语言、贯穿装置、版式、负面清单、标准入场/收尾模板 | ValidateStyleSamplesDraft |
| sample_ids | 样张 ≥1（真迹帧在 style/，判卷修复产物） | ValidateStylePack |

### checkpoint C：`storyboards/draft.json`（StoryboardDraft）+ 翻译 `storyboards/storyboard.json`
| 字段 | 契约 | 过闸 |
|---|---|---|
| world | 全片世界/舞台 | 非空 |
| cast_legend | 角色表（**可空**；写了则镜头里的 cast 必须能对上） | ValidateDraftShots 一致性门 |
| paragraphs | 段落层铺满 | ValidateStoryboardDraft |
| segments[] | narration_refs 恰好铺满 N1..Nn、id/idx 连续、段长带内、shot 结构 | ValidateStoryboardDraft + ShotDirection.Validate（角色 0–4 可选） |
| （翻译产物）storyboard.json | 制作契约，produce 零改动衔接 | ValidateStoryboard |

三件齐 → 打包终审（人工，唯一门）→ stylepack.Confirmed=true → produce。

## 7. 预算与故障

- 阶段配额继承老链实测值：A 6min / B 40min（样张子循环大头）/ C 25min；
  回写帽：revise_script ≤1 次、revise_style ≤1 次，每次返工从当前阶段配额扣。
- 链级总闸 80min 原样保留（runner 兜底）。
- 崩溃续跑：已过闸工件在盘即视为 checkpoint 完成，重跑从缺的阶段续（老链同语义）。
- 取消：链闸/用户取消在回合边界退出（loop.go 现成机制）。

## 8. 回写与指纹失效

- revise_script：新 script.json 落盘 → stylepack.fingerprint 与 draft.fingerprint
  依赖 script 指纹者标记过期（复用现有 invalidation 机制，参考 sceneSidecarFingerprint
  的内容哈希做法）；模型需重过 B/C（若其内容引用了句编号，③必须重排 refs）。
- revise_style：新 stylepack 落盘 → draft 过期同理；样张真迹随 style_id 走，缓存命中。
- 一致性门兜底任何角色/舞台引用漂移（ValidateDraftShots 已于 2026-11-09 加固）。

## 9. 实验方案（E 系列，沿 director-layer-plan 惯例）

- 开关：project.json `unified_prep=1`；前端暂不暴露，curl/对话建实验项目。
- 对照：同一想法+材料各跑 3 片（老链 vs 新链），指标：
  ①终审折算分（平台记账） ②token/调用/墙钟 ③工件返工次数（revise 触发数）
  ④人工终审修改意见数（越少=三件越协调）。
- 判定：新链终审分不劣于老链 且 三件协调度（④）更好 → 切默认；否则留实验档。
- 风险与缓解：
  - 上下文膨胀（样张判卷轮次多）→ render_sample 只回喂结论摘要，图片不出子循环；
  - 大循环病理拖延 → 阶段配额+链闸双保险；
  - 回写风暴 → 帽=各 1 次，超帽只能带人工终审意见重跑。

## 10. 落地拆步

1. runner.go 分支 + unifiedAgent 骨架（loop.go 复用，MaxRounds 合并预算）
2. 三站工具改造为 union（submit 不终止会话；get_style_package 读会话态）
3. revise_* 薄壳 + 指纹过期
4. 前端零改动确认（prepView 三件打包视图原样）；事件流照 manifest 现有词表
5. E 系列实验 → 数据 → 决策

## 11. 与本次角色去强制化的关系

2026-11-09 已拆掉三工位接口里的角色强制政策（Validate 0–4、schema 摘 required、
提示词中性化、shotbrief 按本镜有无 cast 分叉、一致性门）。本设计是同一哲学的
终点：政策从接口里清零，创作决定权全部上收模型，合同（产物契约+一致性门）焊死。
新链提示词（§5）天然不含任何角色条款——老链提示词里的残余条款在切默认时一并退役。
