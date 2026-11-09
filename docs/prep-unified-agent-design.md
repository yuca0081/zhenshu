# 统一准备智能体设计 rev2：无阶段（文稿+风格+分镜 一个 agent 一条循环）

状态：rev2 已实施（2026-11-09，§12 步 1–5 落库；E 系列实验待跑）· 前身：docs/prep-agents-design.md（三工位 v3.1，现行兜底）、rev1（同日本档首版）

## 0. rev1 → rev2 演进（本次讨论的三个转折）

rev1 已把三工位并为一个 agent 单上下文，但仍保留 A→B→C 阶段与 submit/revise
仪式。rev2 把残余的政策层清零：

1. **阶段删除**：三件互相影响，"文稿→风格→分镜"的单向顺序本身就是没拆干净
   的政策（视觉先行也是正当路线）。顺序成为模型的创作决定。
2. **submit 仪式删除**：submit 的三合一职责（落盘/过闸/宣告完成）拆开——
   落盘+过闸合并为 write_*（保存即校验），宣告完成由 finish_prep 终态检查
   取代。revise_* 回写壳随之消失（write 幂等天然覆盖）。
3. **模式删除**：source_mode（topic/manuscript）退役——查证全仓零消费者；
   真正的执法点是行为闸（用过检索 → fact_sheet 必须带 URL），与模式声明
   无关。入口统一为：主题页想法 + 上传材料（一篇完整稿 = 材料很厚的一种
   情况，模型自行走改写或检索路线）。按用户 2026-11-09 拍板暂时取掉。

原则不变：**过程放开（怎么干模型定），终态焊死（契约+指纹+过闸）**。

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

> 给这个智能体所需要的工具，写好系统提示词。创作决定（顺序、要不要角色、
> 什么风格、怎么切镜）全部由模型根据材料自己拿。

## 2. 目标与非目标

目标：
- 一个 ReAct agent 单上下文产出 文稿/风格/分镜，工作顺序与返工节奏由模型定；
- 三件产物以 write_* 随时落盘（保存即校验+失效提醒），finish_prep 终态放行；
- 下游零改动：落盘工件、打包终审（唯一人工门）、produce 段级流水线全部不变。

非目标：
- 不动样张渲染-判卷-修复子循环（render_sample 内部机制原样，agent 只见结论）；
- 不替代人工终审；
- 不承诺修复与准备链无关的缺陷（字幕塌方、封面工具等另行处理）。

## 3. 总体形状

```
prepRunner（runner.go 分支：project.unified_prep=1 走新链，否则老三站链兜底）
  └─ unifiedAgent（loop.go 现成 ReAct 循环；总池 ~70min，链闸 80min 兜底）
       工具五类：读输入 / 查风格库 / 出样张 / 校时长 / 写工件 / 完工
       写工件：write_script / write_stylepack / write_storyboard（保存即校验）
       完工：finish_prep（终态咬合检查，loop 唯一出口；预算耗尽自动执行）
       崩溃续跑：runner 把盘上已过闸工件注入开场，模型从缺的补起
```

- 顺序自由，终态焊死：改稿瞬间 write 返回失效提醒（分镜引用悬空），模型改到
  重新咬合；finish 只认三件在盘、过闸、指纹咬合。
- 会话贯穿始终：写稿的取舍、样张判卷的意见、切镜的决定全在同一上下文。

## 4. 工具清单（11 个）

| 类 | 工具 | 来源与改动 |
|---|---|---|
| 读输入 | read_source | ①script.go 改造：返回想法+材料+目标时长；**模式术语退役**——材料薄（一句话）时直接说"先 web_search 补料再动笔" |
| 读输入 | web_search / fetch_page | ①script.go 原样（检索缓存机制保留） |
| 校时长 | estimate_speech_duration | ①③共用，原样 |
| 查库 | search_styles / get_style_detail / get_style_usage | ②style.go 原样（三级漏斗+真迹画像+词条展开+0命中防骗） |
| 查库 | register_style | ②style.go 原样（本片专用条目、限 1 个；衍生条目真迹退回基风格） |
| 样张 | render_sample | 内循环原样（渲染-判卷-修复黑盒只回结论）；仅 scene_brief 措辞放宽："画本片真实构思内容"（无阶段下口播稿可能未定稿，防通用示例的本意不变） |
| 写工件 | write_script | 新：submit_script 改造——保存即校验（句编号/时长上限/**行为闸**：用过检索则 fact_sheet 逐条带 URL）+ 失效提醒（盘上分镜引用了被改句子时列出） |
| 写工件 | write_stylepack | 新：submit_style 改造——保存即校验（style_id 有效/sample_ids≥1/段长带合法）+ 失效提醒（段长带改动列出超带分镜段） |
| 写工件 | write_storyboard | 新：submit_storyboard 改造——保存即校验（ValidateStoryboardDraft + ValidateDraftShots 两道全跑）+ 对当前盘上稿/风格包的咬合校验 |
| 完工 | finish_prep | 新：三件在盘+过闸+指纹咬合（分镜句引用 ⊆ 最终稿、段长 ⊆ 最终风格带、样张属于最终风格包）；缺口列清单回喂；预算耗尽自动执行 |

删除：submit×3（职责拆入 write_* 与 finish_prep）、revise×2（write 幂等覆盖）、
get_script / get_style_package（单上下文不需要；崩溃续跑走开场注入）。

设计纪律不变：工具 description 只写**怎么用**（参数/返回/闸），不写创作方针；
方针只许出现在系统提示词，且只写过程与合同，不写品味。

## 5. 系统提示词（rev2 草案全文——本设计的核心交付物）

```text
你是这支视频的编导负责人，对三件产物负全责：口播文稿（write_script）、风格包
（write_stylepack，设计系统要经样张验证站得住）、分镜（write_storyboard）。
开场已附用户想法与材料，动手前先 read_source 吃透——材料只是一句话想法时，
先 web_search 检索再动笔。

三件随时可写可改——write 保存即校验：过闸错误、以及「这次修改让哪些下游失
效」都会跟着保存回给你，照着修到零错误、改到重新咬合。硬事实逐条给出来源。
全部完成时调用 finish_prep：三件在盘、过闸、相互咬合（分镜句引用对得上最终
稿、段长落在最终风格带内）才放行。

画面里出现什么、怎么表演、有没有角色形象，由你根据题材、文稿口吻与风格气质
决定；定了角色就在 cast_legend 写清外形/身体/眼睛/性格并全片保持一致。除本
提示词列出的流程与契约外，没有任何附加创作限制。
```

相对 rev1 删除：A→B→C 顺序、四条阶段要点（下沉工具 description）、回写帽
章节（"不要为改而改"交给时间闸，不写不信任条款）、模式措辞。三段结构：
权责与材料 / 过程与终态合同 / 创作放权。

## 6. 产物契约（焊死的三件——字段级）

下游/终审/produce 链认文件不认过程，三件产物与现行同构（除 source_mode 退役）：

### 文稿 `scripts/script.json`（contract.Script）
| 字段 | 契约 | 过闸 |
|---|---|---|
| text / sentences[] | 全文 + 编号句 N1..Nn（原生句终符，拼接还原正文） | ValidateScriptText |
| fact_sheet[] | 断言→URL；**行为闸**：本轮用过 web_search 则必非空 | submit 内置 |
| word_count / target_seconds | target_seconds 缺省 120s（想法里「X分钟」由 applyIdeaDuration 预写） | EstimateSpeechDuration ≤ MaxDurationSec |
| fingerprint | 内容指纹（上游） | — |
| ~~source_mode~~ | **退役**（2026-11-09 拍板：全仓零消费者；NewScript 构造同步删参，types.ts 同步） | — |

### 风格包 `llm/style_samples.json`（contract.StylePack，Confirmed 由终审写）
| 字段 | 契约 | 过闸 |
|---|---|---|
| style_id | 库内条目或本片衍生登记 id | ValidateStylePack |
| description | 风格描述+选择理由（供分镜与导演块理解语境） | 非空 |
| grammar | density / continuity / band_min·band_max（段长带，ClampedBand 有交集） | ValidateStylePack |
| design_system | 皮肤、字体三角色、动效语言、贯穿装置、版式、负面清单、标准入场/收尾模板 | ValidateStyleSamplesDraft |
| sample_ids | 样张 ≥1（真迹帧在 style/，判卷修复产物）——**样张先于风格包定稿**（样张是"视觉站得住"的物证；不规定发生在什么阶段，write_stylepack 闸只认终点货齐） | ValidateStylePack |

### 分镜 `storyboards/draft.json`（StoryboardDraft）+ 翻译 `storyboards/storyboard.json`
| 字段 | 契约 | 过闸 |
|---|---|---|
| world | 全片世界/舞台 | 非空 |
| cast_legend | 角色表（**可空**；写了则镜头里的 cast 必须能对上） | 一致性门（见 §11 前置项） |
| paragraphs | 段落层铺满 | ValidateStoryboardDraft |
| segments[] | narration_refs 恰好铺满 N1..Nn、id/idx 连续、段长带内、shot 结构 | ValidateStoryboardDraft + ShotDirection.Validate |
| （翻译产物）storyboard.json | 制作契约，produce 零改动衔接 | ValidateStoryboard |

三件齐 → 打包终审（人工，唯一门）→ stylepack.Confirmed=true → produce。

## 7. 预算与故障

- **总池制**：老链 A6/B40/C25 阶段配额合并为总池 ~70min，模型自调度（样张
  子循环是大头的经验值保留在 render_sample 内部额度里）；链级总闸 80min 原样。
- **病理上限**：write_* 每件 ≤10 次，只防死循环空转，不防正常迭代（时间闸
  才是主约束）。
- **耗尽自动终检**：预算到线时 runner 自动跑一次 finish_prep 逻辑——能过就
  过，不能过带缺口清单报错退出，模型忘调 finish 也不浪费成果。
- 崩溃续跑：盘上过闸工件注入开场（新会话从缺的补起，与老链"已完成走缓存"
  同语义）；取消在回合边界退出（loop.go 现成机制）。

## 8. 改动传播（原"回写与指纹失效"，机制不变语义升级）

- write_script 落盘新稿 → 盘上 storyboard 的句引用比对：悬空引用随 write
  返回值即时列出（不等 finish 才发现）；分镜工件指纹标记过期。
- write_stylepack 落盘新带 → 超带分镜段即时列出；draft 指纹过期。样张真迹
  随 style_id 走，缓存命中。
- write_storyboard 每次都对当前盘上稿/风格包做咬合校验（引用存在、带内）。
- finish_prep 是终点复核：三件相互咬合的最终裁决，任何漂移在这里现形。

## 9. 输入入口（统一，零新增）

- 唯一入口 = 现行主题页：想法文本（prep_idea.txt）+ 上传材料（input/materials/）。
- 一篇完整稿 = 上传材料的一种（材料很厚的情况）——模型 read_source 后自行
  走改写或检索路线，**无模式分支**。readSource 的"一句话主题（按主题模式
  处理）"措辞改为"材料只有一句话——先 web_search 补料再动笔"。
- article.txt 独立通道退役（带稿走材料上传）；行为闸保留：用过 web_search
  则 fact_sheet 必须逐条带 URL。

## 10. 实验方案（E 系列，沿 director-layer-plan 惯例）

- 开关：project.json `unified_prep=1`；前端暂不暴露，curl/对话建实验项目。
- 对照：同一想法+材料各跑 3 片（老链 vs 新链），指标：
  ①终审折算分（平台记账） ②token/调用/墙钟 ③write 次数分布（返工节奏）
  ④人工终审修改意见数（越少=三件越协调） ⑤**乱序行为观察**——模型实际产出
  顺序（是否出现风格先行/分镜驱动改稿），直接验证"互相影响"是否被利用。
- 判定：新链终审分不劣于老链 且 ④更好 → 切默认；否则留实验档。
- 风险与缓解：
  - 上下文膨胀（样张判卷轮次多）→ render_sample 只回结论摘要，图片不出子循环；
  - 鸡生蛋空转（三件互等）→ 时间闸 + 耗尽自动终检兜底；
  - 版本混乱（上下文记忆 vs 盘上工件）→ 盘上工件是唯一真相，write 返回带版本号。

## 11. 实施前置项（必须先做）

rev1 §11 曾声称 2026-11-09 已完成"角色去强制化 + ValidateDraftShots 一致性
门"，**查证未落库**（prep.go 自 e7d02d8 后未动，ShotDirection.Validate 角色
仍为 1-4 必须、无 cast_legend 对账）。实施 rev2 前先落这批（若在并行会话
的 WIP 里，先对状态再叠改）：

- ShotDirection.Validate 角色 0–4 可选（角色强制令拆除的落点）；
- ValidateDraftShots 增 cast_legend ↔ cast 一致性对账；
- schema 摘 required、提示词中性化、shotbrief 按本镜有无 cast 分叉。

## 12. 落地拆步

1. ✅ §11 前置项落库（f87731a，独立提交，老链先受益）
2. ✅ runner.go 分支 + unifiedAgent 骨架 + 崩溃续跑注入（874d83b，与步 3 合一提交）
3. ✅ write_* 三工具（保存即校验+失效提醒）+ finish_prep + 预算耗尽自动终检（874d83b）
4. ✅ 工具描述收敛（scene_brief 放宽、模式措辞清理、readSource 措辞）+
   source_mode 退役（791562b）
5. ✅ 前端进度改三件过闸状态（c8573fb；建项目接口增 unified 参数 01a02b6）
6. ⏳ E 系列实验 → 数据 → 决策（实验项目：`POST /api/projects` 带
   `unified:true`；老链对照组同参数不带 unified；同一想法+材料各跑 3 片）
