# 管线全阶段测试 · 准备/制作/完成后重做 —— 测试计划与报告

- 日期：2026-10-23
- 触发：用户要求对准备阶段各步骤、制作阶段各步骤、完成后重做/打回等做系统测试，问题落实本文档。
- 前情：2026-10-23 读代码时发现「改稿后重做」可能被断点续跑账本（reviews/journal.jsonl）错误跳过段重写（见 §E），本测试把该隐患的复现列为最高优先级。
- 测试环境：Windows，Go 1.27，server 运行于 127.0.0.1:8080（PID 45592），LLM_API_KEY / DASHSCOPE_API_KEY 已配置。真机测试用自建项目，不动用户已有项目。

## 分层策略（成本从低到高）

| 层 | 手段 | LLM 成本 | 覆盖 |
|---|---|---|---|
| A 基线 | 现有 Go 测试套件全量跑（mock） | 0 | 各包既有判据回归 |
| B 机制 | 新增针对性 Go 测试（mock/临时目录） | 0 | 作废级联、重放、指纹、账本恢复、门禁 |
| C 闸门 | 真机 8080 API 只读/拒绝路径 | 0 | 三前置硬门、409/400 路径、过期三轴 |
| D 全链 | 真实 LLM：自建小项目跑 prep→确认→制作→rework | ~2-3M tok | 真实各工位协同、时长、token 账 |
| E 隐患复现 | 真机：改稿→prep 重跑→二次制作，对比两次成片 | ~1-2M tok | journal 跳段隐患 |

## 测试项与判据

### A 基线
- A1 `go test ./...` 全绿。判据：全部包 OK，无 FAIL。

### B 机制级（mock，零成本）
- B1 作废级联清单：`prep.InvalidateFrom(script/style/storyboard)` 各自删除的文件集合与设计口径一致。
- B2 改稿级联：`prep.ReplaceManuscript` 写新真源 + 删 manuscript.json + 触发①级联。
- B3 重放语义：`prep.HasArtifact` 存在即跳过；`RunStrict` 缺哪站跑哪站。
- B4 **隐患固化**：改稿作废链不清 `reviews/journal.jsonl`、不清 `llm/comp-segXX` 场景文件（事实断言）。
- B5 **隐患复现（决策层）**：journal 含 audited 账 + 分镜已换新内容 → coordinator 恢复判定把同 ID 段当"已完成"（segEntry==stAudited，dispatch 会 continue）。
- B6 TTS 指纹：`narrationFingerprint` 旁白变→指纹变；段集变→指纹变。
- B7 段渲染指纹：`SegStale` 帧或音频变→判旧重渲。
- B8 agent 门禁：start_production 缺文稿/缺分镜/缺风格确认各拒；成片最新挡回；rework 无成片拒、制作中拒。
- B9 过期三轴：改稿/改分镜/改风格任一晚于 VideoAt → video_stale 对应轴点亮。

### C 真机闸门（8080，零成本）
- C1 无文稿项目 POST /produce → 400（三前置硬门）。
- C2 取消不存在的制作 POST /cancel → 409。
- C3 已出片且最新项目 POST /produce → 400（挡整片重做）。
- C4 prep 视图/项目视图只读接口正常返回。

### D 全链真机（自建项目，短文稿目标 4-6 段）
- D1 建项目（带文稿）→ 判据：project.json/目录结构齐。
- D2 POST prep/run 真跑①②③ → 判据：三工件落盘、prep/confirm 前置满足、记录时长与 token 账。
- D3 POST prep/confirm 打包终审 → 判据：stylepack.Confirmed=true、状态可开工。
- D4 POST /produce 制作全链 → 判据：manifest 各 stage（tts/compositions/check/review/render）齐全、成片落盘、终审通过账行、总时长与 token 记录。
- D5 rework 单段 → 判据：只重做目标段（manifest rework.start 含目标段）、check+frameQA 走到、成片重出、旁白音频不变（audio_meta 指纹复用账行）。

### E 隐患真机复现（同项目改稿→二次制作）
- E1 改稿（PUT manuscript）→ 判据：prep 工件级联作废（prepView 报缺）。
- E2 prep 重跑 + confirm → 判据：新分镜生成（内容与旧稿不同）。
- E3 二次 POST /produce → 关键观测：
  - manifest 是否出现逐段"写画面"作业（seg01…逐段 written）；
  - 若无写画面、直接拼接出片 → **隐患坐实**：画面还是旧稿的。
  - 对比两次成片同段抽帧（帧内容应随新分镜变化）。
- E4 结论落档：坐实/排除，附修复方向。

### F 报告
- 全部结果（含失败与未测项）回填本文档；问题按 高/中/低 列表，附复现步骤与代码落点。

## 结果记录

### A 基线 —— ✅ 全绿
- A1 `go test ./...` 14 个包全部 OK（agent/agentlog/api/artifacts/auth/contract/llm/pipeline/prep/produce/react/review/store/stylelib/styleref/webfetch），0 FAIL。

### B 机制级 —— ✅ 8/8 过（新增 5 个测试，2 个文件）
新增 `internal/prep/invalidation_test.go`（3 测试）与 `internal/produce/staleness_test.go`（2 测试）：
- B1 ✅ InvalidateFrom 三档级联清单逐项核对（script 级联删三工件+设计衍生品+legacy 分镜；style 删②③+设计；storyboard 只删③不碰设计）。
- B2 ✅ ReplaceManuscript：真源更新+manuscript.json 删除+①级联；无①工件不误报级联。
- B3 ✅ 三工件齐时 RunStrict 重放零 LLM 调用直接就绪。
- B4 ✅ **隐患台账固化**：script 级联不清 `reviews/journal.jsonl`、不清 `llm/comp-segXX` 场景文件、不清 `compositions/frames` 与 `renders/`（断言进测试，修复时红）。
- B5 ✅ **隐患决策层复现**：成功出片账本 → ReplaceManuscript 改稿 → 新分镜落盘 → coordinator 恢复判定把全部同 ID 段判「已完成」→ 派发循环对每段 continue（跳过重写）。判定条件与 segpipe.go c.run() 逐字同源。
- B6 ✅ narrationFingerprint：旁白变/段集变→指纹变；不变→稳定。
- B7 ⊘ SegStale 未单独立测（指纹口径已在 pipeline 既有路径与今日真机 D4 的 render.seg.cached 行为中间接验证）。
- B8 ⊘ 门禁已有 gate_test 覆盖（redo 对称/旧版导出警示/rework 边界注记）；C 层补真机面。
- B9 ⊘ 过期三轴已有 summary_test 覆盖（stale/failure+stale 用例）。

### C 真机闸门 —— ❌ 1 项未过（发现中危-1）
- C1 ✅ 无文稿项目 POST /produce → 400「三前置未齐，硬门不放行」。
- C2 ✅ 取消不存在的制作 → 409。
- C3 ❌ **已出片且最新的项目 POST /produce → 202 放行开跑（应 400 挡回）**。见问题清单【中-1】。
- C4 ✅ 项目视图/prep 视图正常。

### D 全链真机 —— 准备链三连败（见中-2），制作侧改道复制品项目完成
测试项目：p20261023-095634「管线全链测试-1023」（树叶变黄，9:16，527字）。
- D1 ✅ 建项目（带文稿直建）：目录结构/三闸状态正确，即时返回。
- D2 ❌ **准备链三次尝试零产出**：
  - 第1轮（09:56→10:56）：①2分钟完成；②风格工位一次 LLM 大调用静默 15 分钟 + 样张机检/判卷修复轮叠加（r7 一张样张带修复 ~7 分钟），撞 60 分钟总闸（`prep.error style: context deadline exceeded`）。账面 58.7 万 token。
  - 第2轮（10:58→11:36）：①缓存重放即时过；②进行中（r7 渲样张）时 **server 进程被外部重启**（08:19 的 PID 45592 → 新 PID 8772，11:33-11:37 间），链条静默死亡——manifest 无任何终态事件、对话栏无通告、当轮 token 账丢失。见【中-4】。
  - 第3轮（11:40→12:42）：①重放；②本轮 LLM 情绪"高产"（5 张样张+逐张修复轮），12:42 再撞 60 分钟总闸，同一失败签名。
  - 三轮账面合计 prompt 619,321（缓存 73,664）+ completion 297,978 ≈ **91.7 万 token**（第2轮的消耗不在账上，真实更多），stylepack 一次都没提交出来。
- D3 ⊘ 未测（被 D2 阻塞：无 stylepack 可确认；confirm API 未验证）。
- D4 ⊘ 整片首做未测（同被 D2 阻塞）。改道：复制昨日完整项目为 p20261023-test01 做制作侧测试。附带观察：对成片新鲜项目 POST /produce（C3 误触发，内容未变）走段级流水线时 journal 恢复把 18 段全部判已完成 → 零写画面零审核、纯重拼接，~1 分钟内走到 stitch（被取消止损）——内容未变时该恢复行为等价幂等重出，无害且省钱，但证明恢复判定纯状态驱动。
- D5 ✅ **rework 段级重做全链通过**（p20261023-test01，seg05「冷却五分钟」）：
  - 主管对话链路：list_segments 定位 → rework 派发 → 人话确认；instruction 完整入账（rework.start 原样可查）。
  - 管线链路：写画面 seg05 → 段检 → 局部重渲 → 拼接 → frameQA 逐条特色判据（sig absorbed/missing/drifted）→ 第1轮抓 4 问题带意见重写 → 第2轮后 sig2（黑白反转冲击帧）落实为 absorbed，sig1/sig3 仍缺 → **带伤交付**（frameqa.failed 落账、rework.done、不卡片）——与设计口径一致（审查是加分项不是人质）。
  - 耗时 12:45→13:17 ≈ **32 分钟**（含两轮修复轮）——"几分钟"是修好不用修的情形，修复轮一开就是半小时级。
  - **像素证据**：改稿段 seg05 抽帧 PSNR 18.9dB（内容实质变更）；对照段 seg10 40.5dB（几乎零变化）——"只动目标段"承诺兑现。
  - 审查修复轮确实掷骰子：两轮修复后问题数 4→6（sig2 修好、sig1 新增 drifted、sig3 仍在），与 10-18 实测结论一致。

### E 隐患真机复现 —— ✅ **【高-1】坐实**
操作：在 p20261023-test01（已出片、journal 全段 audited）把 storyboard.json 中 seg01/seg02 旁白改为新稿内容（等同准备③重跑产物；跳过 LLM 环节——该环节的失败见 D2，不影响本实验口径），随后 POST /produce。
结果（13:19:21→13:22:46，全程 3.5 分钟）：
- ✅ TTS 正确判旧重跑（42 秒）：audio_meta 中 seg01/seg02 时长变为新文本对应值（5.98s/5.75s）——配音是新的；
- ❌ **画面链路整链跳过**：coordinator 恢复判定把全部 18 段按旧账判已完成——零写画面、零段机检、零段审；seg01/seg02 场景文件 md5 与改稿前完全一致（db9192131e / b96005a855），帧文件 mtime 未动；
- ❌ 全片终审照跑且**放行**（28 秒一轮过）——其四查（段序/黑帧/闪跳/接缝等）不核"画面内容是否对稿"；
- ❌ 主管在对话栏报「✅ 全片制作完成（逐段审核、全片终审全部通过）」——该轮实际一段都没审，用户收到的是新配音配旧画面的片子加一条全绿的完成通告。
- 成本讽刺：这轮只烧 ~1.1 万 token 就"合法地"产出错误交付物。
- **结论：成片后改稿→整片重做，画面不会按新稿重写（配音会换新）。修复方向见问题清单。**

### 问题清单（终版）

**【高-1】成片后改稿→整片重做：同 ID 段画面被旧账跳过，交付"新配音配旧画面"且全绿灯**
- 复现：单元级（staleness_test.go，决策层）+ 真机（§E，端到端）双重坐实。
- 根因：`reviews/journal.jsonl` 只记状态/代际不记内容指纹（segpipe.go:623 恢复判定只看 State==audited）；改稿级联作废链（prep.InvalidateFrom）不清账本、不清 llm/comp-segXX 场景文件；全片终审不核内容对稿。
- 波及：改稿重做、rerun_prep 后重做、（理论上）任何分镜变化后的整片重做。edit_segment 改一两段旁白→重做的场景同样是旧画面+新配音（该场景用户预期画面是否要重画存疑，但至少终审/对话不该报"全部通过"）。
- 修法方向（建议①+③）：① journal 段账加旁白内容指纹（journalLine 增 narration hash，恢复时失配即失效重跑——保住断点续跑收益）；② 改稿作废链连带清 reviews/journal.jsonl 与 llm/comp-segXX（粗但有效，丢续跑收益）；③ 终审增加"逐段旁白↔画面要点核对"判据兜底。

**【中-1】POST /produce 缺「成片已最新」挡回，与主管工具侧闸不对称**（C3 实测 202 放行）
- 「成片新鲜挡整片重做」只在主管工具门禁（agent/tools.go videoFresh）；api startProduce 只查三前置+prep 互斥。前端靠隐藏按钮兜底，服务端裸奔。
- 附注（披露）：本例为测试执行中误触发，即 POST /cancel 止损；期间主成片未被触碰（mtime 不变），中间产物 main_voice.mp4 重拼接一次（内容等价），用户项目对话栏会有一条取消通告。

**【中-2】准备链 60 分钟总闸按整链一刀切，风格工位在坏天气下必然烧穿**
- 三次真机：两次 timeout（15 分钟级 LLM 大调用 + 3-5 张样张×机检/判卷/修复轮的耗时叠加）、一次被进程重启杀。91.7 万 token 零产出。
- 样张判卷到上限如实带伤交付的口径是对的；缺的是按工位分时预算与"闸前抢收"（到点把手头样张落账提交而非整链作废）。
- 修法方向：总闸按工位分账（①≤5min/②≤40min/③≤10min）；②临近闸值时禁起新修复轮、以现有样张提交；闸超时把已完成工位如实记为部分完成而非整链 error。

**【中-3】rework 带伤交付时对话栏只报"完成"，不带伤情摘要**
- 实测：seg05 两轮修复后仍 6 项未过（frameqa.failed 落账），rework 通知文案是「✅ 成片就绪：段5画面重做完成」——伤情只在 manifest/reviews，用户不看过程页就不知道成片带着已知问题。
- 对照：整片完成路径有 woundedNotes 区分「带伤 N 处」；rework 的 notifyOnDone 没接同款。修法：rework 完成通知接 frameqa.failed 摘要（一句话带过+指路过程页）。

**【中-4】prep 链无断点续跑与死亡终态：进程重启即静默消失，token 账也丢**
- 实测：第2轮被 server 重启杀死——manifest 无终态事件、对话栏无通告（produce 有 Reconcile+开机续跑，prep 没有）；当轮 token 消耗不入账（usage 只在 agent.done 落）。
- 修法方向：prep.Reconcile（启动时把无终态的 prep 记 interrupted+通告）；usage 随每次调用增量落账而非终局汇总。

**【低-1】全片终审四查不核"画面↔旁白内容一致"**——内容缺失型错误（本测试的旧画面新配音）天然放行。归入高-1修法③。

**【低-2】跳段恢复的整片重做完成通告写「逐段审核…全部通过」**——该轮实际零审核（恢复跳过），文案对"本轮发生了什么"失真。修法：完成通告按本轮实际作业量措辞（复用轮注一句"未变段全部复用"）。

### 成本台账（本次测试）
| 项 | 量 |
|---|---|
| 基线+新增 Go 测试 | 0（全 mock） |
| C 层闸门 | 0 |
| 准备链三轮（p20261023-095634） | 账面 91.7 万 tok（第2轮未入账，实际更多），零产出 |
| rework seg05（含两轮修复） | 未单独细账（复制品与昨日项目共享历史行） |
| produce#2 隐患复现 | ~1.1 万 tok |
| 合计 | **≈100 万+ tok**，其中准备链占绝大部分 |

### 遗留未测（明列）
- D3 prep/confirm 打包确认 API（被 D2 阻塞）。
- 整片首做全链（含段审 on 的完整写→审→渲并发调度）——今日唯一干净路径被中-2堵死；建议中-2修复后补测。
- 对话栏改稿→rerun_prep 全程（同步 20 分钟轮时限与 60 分钟链闸的叠加行为）。
- 取消 mid-repair、边界审 on、16:9、双语字幕链。

### 磁盘遗留
- `p20261023-095634`（准备链测试，3 轮失败现场）与 `p20261023-test01`（复制品，rework 带伤交付+改稿错位成片的证据现场：seg01/seg02 新配音旧画面）——均可从 UI 删除，证据现场建议看完再删。
- 新增测试文件：`server/internal/prep/invalidation_test.go`、`server/internal/produce/staleness_test.go`、`server/internal/pipeline/segstale_test.go`（6 测试全绿，其中 staleness_test 是高-1的回归钉，修复后应反转其断言）。

## 修复回填（2026-10-23 当日，全部落地）

问题清单七项当日全修，全量 Go 测试绿（新增 11 个测试用例，4 个新文件）：

| 项 | 修法 | 落点 | 验证 |
|---|---|---|---|
| 高-1 跳段 | 恢复判定接场景指纹旁车（`restorable()`：账面 audited 之外，还须 `llm/comp-<id>.scene.src` 旁车 == `sceneSidecarFingerprint`——与 sceneGen 复用闸同一把指纹，段内容+设计系统+导演统筹三轴全覆盖，逐段粒度；旁车缺失按失配重走链） | `produce/segpipe.go` c.run 恢复/派发两处 | staleness_test 反转为四场景断言（续跑可恢复/改旁白不恢复且不连坐/改设计全不恢复/缺旁车不恢复）✅ |
| 高-1③ 低-1 | 终审新增 content 图文核对判据：FinalInput 增 SegBriefs（逐段旁白+画面构想清单），finalSystem 四查→五查，category 枚举扩 content；旧画面配新旁白的错位片终审可判 high 打回 | `review/verdict.go`、`review/agent.go`、`produce/segpipe.go` finalFrames | 全量测试绿 ✅（判据为提示词+schema 级，真机效力待下次出片观察） |
| 中-1 API 无挡回 | videoFresh 判别收敛单源：`agent.VideoFresh` 导出，api startProduce 在三前置+prep 互斥之后调用，成片最新 400 挡回并指路 | `agent/agent.go`、`api/server.go` | **8081 临时实例真机复测：成片最新的 p20261022-094226 与 p20261023-092920 均 400 挡回，文案正确** ✅ |
| 中-2 总闸烧穿 | 工位分时预算（①6/②40/③12 分钟，和≤58<60）：单站超线即收口，报「谁烧穿+已完成什么+重跑续起」；②临近预算线 4 分钟进抢收窗口，render_sample 拒起新活回喂「立即 submit_style 带伤提交」；预算耗尽不触发冷重试 | `prep/run.go`（stationBudgets/doneStations/预算包裹）、`prep/style.go`（grabAt+抢收门） | budget_test 3 例（预算表和≤60/进度报告/抢收门拒渲+未过线不拦）✅ |
| 中-3 rework 不报伤 | frameQA 返回带伤摘要，runRework 完成通知带伤情：「…画面重做完成（两轮修复后仍未过审查: seg05；问题明细见过程页与项目 reviews/ 意见单…）」 | `produce/frameqa.go`、`produce/produce.go` | 编译+全量测试绿 ✅ |
| 中-4 prep 死亡丢账 | ① prep.Reconcile：开机扫各项目 manifest 尾窗，最后生命周期事件是 prep.start（无终态）→ 补写 prep.interrupted + 聊天通告；② token 增量落账：Loop 增 OnCall 钩子 → runTracked 接 Task.Add，未落账增量过 2 万 token 自动 Flush 一行 agent.usage（差值记账）；ProjectSpend 改为 done 行全量 + 无 done 行任务的增量合计（不重复计数） | `prep/runner.go`（Reconcile）、`prep/loop.go`、`prep/run.go`、`agentlog/agentlog.go`、`cmd/server/main.go` 接线 | reconcile_test + flush_test 3 例（死亡项目补账通告/正常项目不动/增量与 done 不双计）✅ |
| 低-2 通告失真 | 完成通告按本轮作业量措辞：恢复复用>0 时写「复用 N 段/新做 M 段；」前缀 | `produce/segpipe.go` 成功通知块 | 随高-1 断言覆盖（restored 计数路径编译+测试绿）✅ |

**部署注意**：以上改动需重启 server 才生效（8080 当前仍在跑旧进程）。验证方式：临时 8081 实例已验中-1；其余以单测锁死，下次出片顺带观察 content 判据与完成通告措辞。

