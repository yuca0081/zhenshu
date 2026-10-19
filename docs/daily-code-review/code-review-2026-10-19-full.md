# 全面代码审查报告 · 2026-10-19（架构 / 整洁度 / 效率 / 注释四维）

- **审查对象**：HEAD = `bfd92e7`（工作区干净，无并行会话 WIP）
- **范围**：Go `server/` 3.65 万行（144 文件）· 前端 `web/src` 约 6000 行 · `ai/` Python 11 脚本 2677 行 · `server/cmd/`
- **方法**：机械检查全跑（build / vet / 全量 go test / 前端 vite 构建 / gofmt / Python 语法）+ 七路并行子智能体分模块深审（其中 Python 路被账户限流打断，由主会话亲自通读补审）+ **全部高危逐条人工复核**（本报告标注〔✓复核〕的均为复审人亲自读码确认，无一轻信子智能体）
- **口径**：高 = 死锁 / 丢数据 / 坏结果 / 功能不可用；中 = 明显浪费 / 维护陷阱 / 边界错误；低 = 整洁度 / 注释 / 风格

---

## 一、总评（四维）

| 维度 | 评价 | 一句话 |
|---|---|---|
| 架构设计 | **良** | 包依赖是干净的单向分层（contract/store/llm 等打底，api 顶层组装，无循环）；段级流水线「三池+刹车+journal 恢复」骨架成熟。欠账三笔：`agent` 包横跨 9 个包近上帝化、`server.go` 约 2200 行单文件混排、ReAct 循环骨架在 prep/review 各抄一份已漂移 |
| 整洁度 | **中上** | 超 150 行函数全仓仅 4 个、TODO 为 0；但死代码清单不短（director M2 半退役仍接线约 370 行、shrinkImage、.hyperframes-ok 等），双实现漂移点多（saveManuscript / ProjectCanvas / 库筛选样板 / snapshot 调用） |
| 效率 | **中** | 热点集中在：Chat 路绕过并发闸、抽帧全串行、activity/token 每请求全量读 manifest、sceneGen 每次重读样张图、前端无节流轮询 + 1.6MB 单包无代码分割 |
| 注释 | **罕见地好** | 「为什么 + 实测事故日期」式注释密度是这套代码最大的资产；但**失实注释恰好集中在刚改过的区域**（chat.go 300s、segpipe「幂等」「取走即清」、runPrep「同路」等约 14 处），比没有注释更危险 |

**最重要的发现单独说**：10-17 巡检的 9 个高危，本次逐一回访对账——**1 个真修、1 个半修、7 个未修**。两个「修复提交」（d78bcd4、7b462e8）修的其实是同日《制作可靠性修复方案》里的 P0 项，**并没有覆盖巡检报告的 9 高危清单**（详见下节对账表）。机械检查全绿（build/vet/test/前端构建）说明不了任何事——这批问题全部藏在并发窗口、失败路径和跨端事件链上，单测与 happy-path 实测都照不出来。

---

## 二、10-17 九高危对账表（本节为 10-19 审查时点的对账；第十节为当日施工后的落地状态）

| # | 10-17 报的问题 | 审查时点现状 | 本次对应条目 |
|---|---|---|---|
| 1 | segpipe auditBoundary 持锁自死锁 | **已修（真修）**：LLM 调用已挪锁外，临界区只剩三行赋值 | — |
| 2 | 边界审打回后 launched 不复位 → 死转 | **未修**（施工时由并行会话以 bndBusy+defer 方案修复并补回归测试） | 高-5〔✓复核〕 |
| 3 | verdict.go 终审空意见单必拒 | **未修**（d78bcd4 根本没碰 verdict.go） | 高-1〔✓复核〕 |
| 4 | artifacts 前缀双斜杠 → 三分类整体失效 | **未修**，单测仍用无尾斜杠路径，全绿掩盖 | 高-2〔✓复核〕 |
| 5 | GC 删本地成片后 has_video 倒退 | **未修**，且比当时知道的更广（重做入口也被挡） | 高-3〔✓复核〕 |
| 6 | prep 三入口无互斥 | **半修**：制作中禁跑准备已挡；对话侧 vs 想法页仍可双开 | 高-6〔✓复核〕 |
| 7 | 删项目不取消 prep 链（云端留尸） | **未修**，deleteProject 只挡制作与对话轮 | 高-4〔✓复核〕 |
| 8 | force 只在分镜工件存在时作废（半成品态新想法不消费） | **未修**，runner.go:90-94 结构原样 | 中-1〔✓复核〕 |
| 9 | IdeaPane 换想法重跑按钮必 409 | **未修**，rerunOpen 模式勾选框仍被 v-if 隐藏、force 恒 false | 高-7〔✓复核〕 |

> 教训归档：**「提交说明写了修复」≠「修复落地」**。后续修复提交建议在提交说明里逐条引用巡检条目编号，并回写巡检报告的对账状态；夜巡 cron 也可加一步「上月高危回访」。

---

## 三、高危（12 条，全部〔✓复核〕）

### 旧账未修（7 条）

**高-1 终审空意见单必拒 — `server/internal/review/verdict.go:108`**
终审档案 `profFinal.dims=nil`，校验条件 `len(v.Findings)==0 && (p.dims==nil || …)` 里 `p.dims==nil` 恒真 → 干净成片交「没意见」的空单子必被打回，错误文案还文不对题。后果：模型被迫编造 low 意见凑数（污染 reviews/ 账本），或 2 轮预算烧尽 → 终审整片失败 → 叠加自动重跑白烧两轮。改法：条件改为 `p.dims != nil && countFails(v.Dims, p.dims) > 0`，补终审空卷单测。

**高-2 云端三分类整体失效（前缀双斜杠）— `server/internal/artifacts/artifacts.go:212/240/291` + 调用方**
所有「整实体」调用都传 `ProjectKey(id,"")`（带尾斜杠），三处 `TrimPrefix(key, prefix+"/")` 拼出 `projects/p1//` 永不匹配 → `Classify` 拿到整 key，所有规则失配 → **一切文件判 Keeper**。①QA 截帧/渲染壳等设计上不上传的全量传上 MinIO（存储反向膨胀）；②GC「腾磁盘」目标落空（Discard/Garbage 永不删）；③Restore 把垃圾拉回本地。`artifacts_test.go` 用例不带尾斜杠所以全绿。改法：入口统一 `TrimSuffix(prefix,"/")` 再拼，补带尾斜杠用例。

**高-3 GC 删本地成片后项目「变回无片」— `server/internal/api/server.go:659`（listProjects）+ `agent/agent.go:151`（State.HasVideo）+ `server.go:1515`（startRework）**
三处都只 `os.Stat` 本地 `renders/main.mp4`。GC 按设计删本地后：列表显示无片、projectView `has_video=false`（但播放接口有远端回退照常能播），**段级重做直接 400「成片未就绪」**——用户看着能播的片子却不能重做。改法：抽 `s.hasFinalVideo(id)`，本地 miss 回退 `Artifacts.Stat`，四处统一口径。

**高-4 删项目不取消 prep 链 — `server/internal/api/server.go:786-811`**
deleteProject 只挡 Producer 和对话轮。prep 后台链会 `MkdirAll` 重建已删目录继续写盘，收尾 SyncUp 再把整套产物传回 MinIO——磁盘留半删残骸 + 云端留尸，且 `DeletePrefixBestEffort` 先删、SyncUp 后传，顺序上正好「复活」。改法：入口先查 `Preps.IsRunning` 拒绝，RemoveAll 前 `Preps.Cancel(id)`。

**高-5 边界审打回后该边界永无重审 → 全管线死转 — `server/internal/produce/segpipe.go:1039/1046/1143`**
复位在飞标志只发生在「当前代际已审过」分支；打回（high）分支只 `dispatchRewrite`，既不更新 `b.genP/genN` 也不复位 `launched[i]`。目标段 gen 只增不减 → 代际永不相等 → `if launched[i] { continue }` 永跳过 → `waitAllPassed` 死等 → 看门狗 32 分钟才刹车，autoRetry 两次重跑又死在同一处，**最坏 3×32min 全烧**。改法：launched 提为 `boundaryCtx.auditInFlight`（c.mu 保护），所有完成路径（过/降级/打回/刹车）都复位；**必须连带中-6 的轮次上限一起做**，否则把这个潜伏的无限修复循环放出来。

**高-6 prep 三入口互斥半修 — `api/server.go:1109`（runPrep）+ `agent/tools.go:34`（rerunPrep）+ `tools.go:1501`（saveManuscriptContent）**
「制作中禁跑准备」已有；但 runPrep 不拿 `turnMu`（只查 Producer.IsRunning），对话侧 rerunPrep 是同步 `prep.RunStrict`、不登记 `Preps.running`。对话轮正在分钟级重跑工位时，用户点想法页「自动完成」→ 第二条链并发开跑，两条链对同一项目 `InvalidateFrom` + submit 落盘互踩（半新半旧工件）+ 双倍烧 LLM。`/prep` 状态接口还显示「没在跑」诱导用户去点。改法：runPrep 对 turnMu TryLock 失败即 409（confirmStyle 同款）；对话侧执行前查 `Preps.IsRunning` 拒绝。

**高-7 IdeaPane「换个想法重跑」点了必 409 — `web/src/components/IdeaPane.vue:76/79/143`**
rerunOpen=true 时勾选框被 `v-if="!rerunOpen && exists"` 隐藏，`forceRerun` 恒 false，`runPrep(..., force=false)` 在工件已存在时必被后端 409 拒绝——10-17 就报过、改法（rerunOpen 时强制 force=true）至今未落，而且 76 行还用的是已知会死的 `window.confirm`。改法：start() 里 rerunOpen 为真时强制传 force=true；confirm 换自绘两段式（见中-28）。

### 新发现（5 条）

**高-8 Chat/ChatTemp 完全绕过账户级并发闸 llmGate — `server/internal/llm/chat.go:57`（chatTemp 直接 postChat）对照 `provider.go:199`（唯一过闸点）**
10-17 限流事故后建的闸只装在 `generate` 路上；而**准备三工位 ReAct 循环和全部审片循环（prep/loop.go:88、review、段审/边界审）走的 Chat 路是绕闸直发**。多项目并行时这正是 429 惊群最后一个没堵的口子——等于小区装了门禁、侧门常年敞开；连运维笔记都写着「所有 chat 调用过包级 llmGate」，文档与代码不符。改法：过闸逻辑下沉到 `postChat`（或 chatTemp 重试环里照搬 generate 的过闸块）。

**高-9 Rework 不重置终态认领 finished — `server/internal/produce/produce.go:374-434`**
`delete(finished, projectID)` 全文件只在 Start（178 行）。复现：制作失败（finishJob 已认领 finished）→ 用户重做某段 → 重做失败/取消 → `claimFinish` 返回 false → **rework.error 终态不落 manifest、render_jobs 永远 running、聊天无汇报、UI 卡「运行中」**，重启 Reconcile 补成 interrupted、口径仍错。改法：Rework 的 claimRun 后同样 delete(finished)（或把重置收进 claimRun 绑成原子操作）。

**高-10 draft_style_samples 缺 Producing 门禁 — `server/internal/agent/tools.go:316-322`**
同文件 10 个工具都有 `if s.Producing { 拒绝 }`，唯独它没有。制作运行中在对话里让它重出样张 → `rerunPrep(StationStyle)` → `InvalidateFrom` 删 stylepack/样张/设计衍生品——全是制作管线的硬门与渲染输入，本轮制作坏结果甚至失败。改法：gate 开头补 Producing 拒绝。

**高-11 fallbackOldScene 在段级流水线永不可达 — `server/internal/produce/scenegen.go:250/258`（门槛 `only == ""`）对照 `segpipe.go:745`（恒传 `only=s.seg.ID`）**
单段写画面三轮契约不过/元素不足时，旧画面兜底永远够不着 → `fail()` → 刹车 → **整 run 失败**。10-18 刚为「顽固段不拖死整片」给段审加了带伤放行，写画面侧同一类失败却没有降级路径——上一轮已过 check 的产物明明还在（修复轮不删合成物），却不能沿用。10-18 实测「段审-重写不收敛是最大拦路虎」正是这条链路的近亲。改法：把 `only == ""` 从门槛去掉（保留「用户点名改的段不许静默沿用」语义），与段审带伤记账统一口径。

**高-12 前端 rework-started 事件无人监听 — `web/src/App.vue:560-576`（绑了 12 个事件唯独漏它）对照 `StagePanel.vue:217`（emit）**
点「按最新内容重做」后 `view.producing` 全程 false：**VideoPane 停止按钮（`v-if="view.producing"`）整轮缺失、用户无法中途停止**，过程页 5s 轮询与制作中呼吸灯也不启动，直到终态事件才恢复。一行修复：模板补 `@rework-started="refresh"`；顺带删 VideoPane/StagePanel 的死中转 emit。

---

## 四、中危（按域分组；标〔✓复核〕为复审人亲自抽验）

### 制作 produce / 段级流水线（14 条）

1. **〔✓复核〕prep force 半成品不作废**（10-17 #8，`prep/runner.go:90-94`）：上轮失败停在①②时 force 走不到 `InvalidateFrom`，新想法只落 input 不被消费。被高-7 卡着暂时难触达，修完高-7 即暴露。
2. **Start 持全局锁做磁盘 IO/DB 写**（`produce.go:164-181`）：mu 是整个 produce 包的全局锁，锁内 os.Stat + Manifest 落盘 + DB 写，其他项目的 IsRunning/Start 全排队。Rework 已示范正确写法（锁内只做 map 操作）。
3. **〔✓复核〕materials.json 非原子写 + 读者无锁**（`materials.go:78-87` 写 vs 103/168/197/280 读）：段级并发下一个段 truncate 重写、另一段读半截 JSON → 素材「消失」、白名单整组跳过，间歇性难复现。改用 `pipeline.WriteFileAtomic` 一行搞定。
4. **director.go M2 审片段是半退役仍接线**（`director.go:333-513` 约 370 行 + 3 个测试文件）：prep 项目（10-16 后所有新项目）走 segpipe 永不触达，仅 legacy 重跑可达，但文件头注释仍把它描述为现行架构。M1 部分（directorNoteFor/directorFingerprint）新旧路径共用不能删。短期补退役状态注释，中期拆分处置。
5. **Rework 缺 orphaned 收尾等待**（`produce.go:152-163` vs `379-391`）：Start 有 3 分钟等待防双开，Rework 没有——取消后管线楔死的 60 秒窗口内点重做，两条管线并发写同一项目。
6. **hyperframes snapshot 调用双处复制 + 版本号硬编码两份**（`produce.go:1140-1161` vs `prereview.go:233-247`）：`hyperframes@0.8.55` 与正确性参数（--no-browser-gpu，seg07 事故换来的）靠人肉同步，将来只改一处必出「回放门禁与渲染器不同源」的假失败。抽共用函数+版本提常量。
7. **边界审无轮次上限 + 段 round>2 恒带伤放行 → 修复循环无上界**（`segpipe.go:1086` Round 恒 1；952-963）：修好高-5 后此洞暴露——b01 打回 → 段 3 轮起带伤放行 → 重审再打回 → 无限循环，签名刹车在「单边界+单段」场景凑不满阈值。改法：auditBoundary 记 per-boundary 轮次，超限按当前代际降级通过 + 记账。
8. **takeStitchFixes 恒空清单（机制性死路）**（`segpipe.go:1160-1175`）：清单只认 high+stitch_fix，但含 high 的边界永远被打回重画，stitch 又只跑在全部边界通过之后——「能用拼接参数修的别判 redraw」的提示词与执行层自相矛盾，`review.stitch_fix_applied` 永不出现；函数无清空动作，「取走即清」注释失实。
9. **终审故障不降级**（`segpipe.go:1191-1194`）：段审、边界审故障都记账降级放行，唯独终审一次 LLM 抖动/预算烧尽就整片失败再叠两轮重跑。同款记账降级即可。
10. **prereview 单段失败 = 静默漏审**（`prereview.go:321-324/340-343`）：出错段既不算未过也不进渲后复查名单，其余段全过时 frameQA 整体跳过——该段从预审到渲后**零视觉审查**（是「没看」不是「看过没问题」）。改法：出错段并入 Repaired 让渲后兜底。
11. **跨轮邻段衔接承诺不入段缓存指纹**（`scenegen.go:37-46`）：分镜只改 A 段 brief 时 B 段旁车指纹不变 → 缓存命中 → B 沿用旧画面，与 A 的首尾衔接承诺静默失效（运行内修复轮不受影响，缺口在跨运行）。改法：指纹追加前后段 `Brief.FirstFrame/LastFrame` 摘要。
12. **segCheck 第三轮 ensure 失败 nil 解引用 panic**（`segcheck.go:151/176-177`）：三轮都死在 ensureSegCheckDir 时 blocking 为 nil，`TailLines(blocking.Error())` panic——guardWorker 会兜住，但用户看到的是裸 panic 而非真因。
13. **带伤放行的「伤」后续无人管 + 完成通知失实**（`segpipe.go:960-962` vs `:317`）：带伤放行/降级放行的运行也通知「全片制作完成（…全部通过）」；意见单躺 reviews/ 等用户自己发现，终审也不读段意见单加重审权重。改法：收尾扫描 journal 聚合带伤段，通知如实标注。
14. **renderSegment 恢复进场无条件重渲**（`segpipe.go:808-815`）：不查 segStale 指纹直接 RenderSeg，断点续跑对未变段片整段分钟级重渲；stitch 侧反而会跳过——两套判旧口径不一致，480 行「各自幂等」注释掩盖了这笔开销。

### 跨 Go/Python（1 条）

15. **assemble.py 并发无互斥 + 非原子写**（`segpipe.go:755` 写池 ≤4 并发全量跑；`ai/assemble.py:486/519-521/537` write_text 直写）：一 worker 拷 `.hf-seg/<seg>.html` 时另一 worker 的 assemble 正在重写同一文件 → 拷到半截 → check 幻影失败 → 白烧 LLM 修复轮。Go 侧给 vend 配了 `vendMu` 却对 assemble 没有互斥。次级：单段写触发全段壳重建 O(N²)。改法：per-project 互斥包住 assemble 调用，或 assemble.py 全部 tmp+rename。

### pipeline / react（3 条）

16. **〔✓复核〕writePostFX 幂等短路永不生效**（`design.go:606-618`）：`meta["post"]` 是 map（按键字母序序列化），want 是 struct（按字段序），两串恒不相等 → 每轮管线重放都白写 project.json + 两次 putArtifact 上传，「值变化才动盘」注释与实际相反。改法：比较前把 want 归一化成 map。
17. **manifestMu 是实例字段锁，护不住跨 NewProject 实例的并发追加**（`pipeline.go:57/90-117`）：同一项目两个 *Project 实例各持各的锁，目前靠 O_APPEND 单行写的 OS 原子性兜底——这是文件系统副作用不是设计保证，而注释宣称「终态一行丢失=对账全盲」。改用 ProjectMu 同款全局锁表。
18. **stageStoryboard 导演统筹先于分镜本体校验**（`pipeline.go:410-424`）：API 分镜本体不合格时最多 3 次导演 LLM 调用全白烧才轮到自检拒绝。先 ValidateStoryboard 快速拦一道再进导演轮。

### agent / prep / review（6 条）

19. **noGate 工具也做全量扫盘**（`agent/agent.go:699`）：`Gate: func() { return t.gate(a.State(p.ID)) }` 参数先求值，11 个免门禁工具（web_search/list_projects 等）每次调用也先全量扫 8 个文件 + manifest 尾，与 638 行「一轮只扫一次」注释矛盾。nil 判别短路即可。
20. **videoProbeProv 全局变量数据竞争**（`review/agent.go:97` 写 vs `review/tools.go:206` 并发读，无锁）：段审/边界审 goroutine 并发调用 auditProvider。并入 vidMu 或改参数传递。
21. **三工位 submit 落盘顺序有崩溃窗口 + script.go:309 吞错**（`script.go:304-309`/`style.go:710-716`/`storyboard.go:144-153`）：工件先写、legacy 产物后写且 `_=` 吞错，中途崩时下游读到陈旧 manuscript.json。legacy 先落、工件标记最后写，错误回流。
22. **样张渲染缓存命中不回填草稿，可把②卡死**（`prep/style.go:409-411`）：缓存键只含 HTML 且失败也标记；InvalidateFrom 清样张与草稿但不清 `llm/prep-render-cache` → 级联重跑时模型重交同 HTML 命中缓存返回「未重渲」→ submit_style 永远报「还没有样张」。改法：命中时幂等回填或缓存键并入 stylepack 代际。
23. **三份 ReAct 循环骨架重复且已漂移**（`react/react.go` vs `prep/loop.go` vs `review/review.go`）：Observation/Tool/Loop.Run/decodeArgs 等近乎全拷两份，review 版已长出独有特性，修一个 bug 要改三处；且两份拷贝里重名工具注册用 schemas 索引写 l.Tools，正确性依赖两个 slice 悄悄对齐。review.Loop 泛化后并入 react 包。
24. **审核抽帧全程串行**（`review/agent.go:332-339/460-468/559-565` + `tools.go:167-174`）：锚点帧逐帧一次 ffmpeg 进程串行跑，终审 12-16 帧同样；帧间独立，errgroup 并行可把墙钟压到 ~1/N。

### llm / stylelib / styleref（5 条）

25. **decodeMCPMessage 与注释不符**（`llm/websearch.go:158-174`）：注释说取「带 result/error 的帧」，实际任何 JSON 对象都能解进去——MCP 流常在结果帧前夹 ping/notifications 帧，会被当答案返回，报出「搜索执行失败：」（错误文本为空）。先验 `result`/`error` 键再解。
26. **styleref ref.json 读-改-写全程无锁**（`styleref.go:210-233`/`analyze.go:30-36`）：confirm 慢窗口（拷贝/上传秒~分钟级）中用户点「重新解析」，runAgent 完成时用旧副本整体回写 → **confirmed 标记丢失**。变异收进 Store.mu 的 load-modify-save。
27. **解析状态 running 无崩溃对账**（`styleref/analyze.go:35` + `cmd/server/main.go:112`）：进程重启后引用永久卡「解析中」，UI 与 agent 工具都这么显示。启动时扫一遍 references 把 running 改写成 error；解析 ctx 接进程根 ctx。
28. **webfetch 每次调用新建 http.Client+Transport**（`webfetch.go:67-89`）：零连接复用，Agent 连环抓十几个页面全付重复 TCP+TLS 握手；同仓 llm 侧都是包级复用并写明理由。提为包级变量。
29. **stylelib Library.Get 全表扫描+全量反序列化**（`stylelib.go:127-139`）：Get 被 BuildBrief/BumpUsage/Derive/媒体回退高频调用，PG 后端每查一条 = 整表 SELECT + 逐行 JSON 反序列化。接口加 GetStyle(id) 走 WHERE。

### api / store / 前端（9 条）

30. **prep/produce 互斥不对称**（`api/server.go:1472-1486` startProduce 不查 Preps.IsRunning；saveManuscript:856 同病）：重跑作废三工件期间可开制作，管线可能读到半作废 stylepack/storyboard。三入口互斥做成对称表或统一 assertNotBusy。
31. **〔✓复核〕agentTokenTotals 每请求全量读+解析 manifest.jsonl**（`server.go:703/725-744`）：10-19 新加的 token 双账开倒车——overview 注释自己写明「manifest 随轮次无限增长，逐次全量读 I/O 线性上涨」，activity 接口为此专门改成了 ReadJSONLTail。做增量缓存（mtime/已读字节记忆）。
32. **pgStore.DeleteProject 吞 DB 查询失败**（`store.go:263-267`）：findUUID 特意区分「DB 失败」与「无行」，DeleteProject 却一刀切全吞——瞬时故障时镜像行变孤儿且上层报「删除成功」。区分上抛。
33. **〔✓复核〕登录失败窗口数据竞争**（`server.go:443-464`）：sync.Map 只护 map，`f.n++`/`f.until` 无锁读写，-race 必报，限速窗偶发失准。值对象内置 mutex。
34. **saveManuscript 双实现漂移**（`server.go:837-878` vs `agent/tools.go:1501-1531`）：注释互相声称「与对方同语义」，实际 api 版多响应字段、agent 版多提示，已分叉。落盘+级联作废收进 `prep.ReplaceManuscript`。
35. **〔✓复核·主会话自查〕applyIdeaDuration 中文数字算错**（`prep/runner.go:146-171`）：`minutes*10+v` 的进位写法对「十二」算出 102 分钟（应 12），超 900s 上限后静默丢弃——用户说「做个十二分钟的视频」目标时长悄悄失效；且续跑场景新 idea 不进③的 kickoff 却已改 target_seconds，想法半静默失效。复合中文数字按十进制解析（X十Y = X*10+Y）。
36. **window.confirm/alert 残留 4 处**（App.vue:462/466、StagePanel.vue:213、IdeaPane.vue:76）：自绘确认已拍板（原生弹窗 10-16 死按钮、Playwright 30s 超时同源），但最危险的删项目/作废重跑入口还在用原生弹窗。统一换两段式或 NPopconfirm。
37. **〔✓复核〕TemplateDetail 挂载即抛 TDZ**（TemplateDetail.vue:37 用 `pickerFor`、:81 才声明）：文件头注释记了这个坑并把 editing 提前，却漏了 pickerFor——每次进风格库/视频解析页向全局 errorHandler 抛一次异常，污染白屏排查日志。声明提到 watch 之前。
38. **PrepPane 反馈框 Enter 无 IME 组合守卫**（PrepPane.vue:163）：拼音选词的 Enter 会把半截反馈发出去触发工位重跑。同仓三处都有守卫，照抄 ChatPanel 的 onEnter。

### 前端效率与结构（4 条）

39. **activity 无节流全量拉 + 轮询不看页签可见性**（App.vue:331/339/380/201）：SSE 每条 progress/stage 都触发一次最多 2000 行的 GET，与 5s 定时器叠加；loadProject 那条路已有 throttled 方案，activity 没用上。
40. **App.vue 上帝组件 + 两份清场已漂移**（App.vue:67-116/209-230/469-483）：20+ 项目级 ref、StagePanel 一次收 15 prop + 12 事件；selectProject 与 onDeleteProject 各手抄一遍清场，chatDraft/progressLine 两处都没清（切项目草稿串台）。抽 useProject() composable 收敛。
41. **类型契约靠 cast 嗅探 + 构建不跑类型检查**（ManuscriptPane.vue:41、api.ts:75-80、build 脚本无 vue-tsc）：`prep_invalidated` 字段改名零信号。build 改 `vue-tsc --noEmit && vite build`。
42. **库/词表重复请求 + KeepAlive 页签轮询不停**（StylePane/ LibraryPanel/ RefVideos 各拉 vocab；IdeaPane 切页后 3s 轮询照跑全程 15-25 分钟）：onDeactivated/onActivated 成对补上。

---

## 五、低危（分组精简；完整细节可按定位回查）

**整洁度 / 死代码**
- `review/tools.go:225 shrinkImage` 无调用；`pipeline.go:801 .hyperframes-ok` 无读者；`director.go:379 shotAt` 死变量；`produce.go:1341/1406 pngDiff*` 是测试专用 helper 混在生产文件；`materials.go fetchOne idx` 形同虚设；`segpipe.go:343 auditReq.round` 死字段；前端 `api.ts confirmStyle/draftManuscript/extractLink` 三方法无调用、VideoPane rework-started 死 emit；`contract/prep.go:411 _ = i` 死代码。
- 双实现/样板重复：ProjectCanvas（produce vs styleclips，且注释引用已不存在的 produce.canvasFor）；库筛选样板（StylePane vs LibraryPanel）；mini_js/工程底座（assemble vs assemble_samples）。
- 巨型文件：`produce.go` 约 2060 行混六类职责（像素 diff 引擎/字幕翻译/HTML 机械修复可各自成文件）；`server.go` 约 2200 行按资源拆分；`agent` 包依赖 9 个包近上帝化。
- 仓库卫生：`demo/` 105 个实验文件住主仓；`cmd/m0` legacy CLI（仅 pipeline 依赖，留Debug用可但建议注明）；`docs/analyes` 目录名拼写（analyses）。

**效率**
- gofmt 真实偏差 6 文件（contract/prep.go、pipeline/design.go、stylelib/stylelib.go 等，均结构体对齐类）；前端单 JS 包 1.6MB（gzip 451KB）无代码分割，建议 manualChunks 或动态 import。
- sceneGen 每次调用重读 3 张样张 PNG+9 张关键帧并重编码 base64（scenegen.go:104-107，可按项目缓存）；finalFeedback 循环内反复 LoadTimeline（segpipe.go:1432）；coordinator.log 持锁做 Manifest 磁盘 IO（segpipe.go:447）；prepView 同请求 LoadStylePack 两次（server.go:1193/1213）；hasFFmpegFilter 首探持锁跑子进程（pipeline.go:1689）；`videoStyleID` 函数体内编译正则（stylelib.go:286）；chatTemp 429 退避无抖动（chat.go:72，违背自家 10-17 事故教训）；ActivityPane 行 key 用自增序号致 2000 行全量重 patch。
- Python：extract_video_features 抽帧串行（60 帧各起一进程）；scene_lint J1 每脚本块起一次 node --check。

**注释失实清单（全部已定位，建议修代码时顺手改）**
`llm/chat.go:28`「Chat 保持 300s」（实际统一 600s）；`api/server.go:1107` runPrep「对话入口同路」（不同路，掩盖高-6）；`segpipe.go:480`「各自幂等」（render 不幂等，见中-14）；`segpipe.go:456`「logNoState」（函数早改名）；`segpipe.go:1099`「已持 c.mu」（锁外）；`segpipe.go:1159`「取走即清」（无清空）；`styleclips.go:236`「每次重编译」（包级只编译一次，实防函数体内编译）；`events/hub.go:12` 事件类型清单不全；`director.go:1-7` 文件头把 M2 描述为现行架构；`produce.go:1-8` 包注释只写 legacy 链路不提 prep 路由；`runner.go:146`「钳到 1-15 分钟」（实为静默丢弃）；前端 IdeaPane「制作账在视频页签事件流」（已迁过程页）；NewProjectModal 注释描述已退役弹窗流程；format.ts 注释引用不存在的 ActivityFeed。

**Python ai/ 其他低危（主会话亲自通读结论：无高危，整体质量高）**
av_common.ffmpeg_encode 丢弃 stderr（失败无线索）；vendor_tool curl 缺失时裸 traceback（其余工具都有人话）；tts_align 音色切换先删全部 wav、中途失败留「meta 有账文件无货」中间态（重跑自愈）；tts_bailian 的 dashscope 同步调用无自身超时（靠 Go 看门狗）；font_tool 的 font_assets.json / strip 操作非原子写（有备份兜底）；assemble_samples 顺手改写源片段 frag（组装工位带写副作用）；extract_video_features probe_duration 的 check=True 崩时裸 traceback（与下一行人话报错不一致）；assemble.py `xfade` 变量名实指「页内黑场 fade」易误读。

**待作者拍板（不一定是错）**
- Rework 对 prep 项目走 legacy 管线（绕过 segpipe 审核工位/journal/刹车）——若有意（重做=单段旁路）请注释写明；
- frameQA extractFrames 失败整制作失败（与「审查类故障降级放行」哲学相悖，但「环境缺失早失败省钱」也自洽）；
- 词表落表先于草稿终检（失败解析留孤儿词条，与「词表是生长的登记册」哲学可共存，注释声明取舍即可）。

---

## 六、机械检查与全局结论

- `go build` / `go vet` / 全部 20 包 `go test`：**绿**。前端 `vite build`：**过**。11 个 Python 脚本语法：**过**。
- gofmt：6 文件真实偏差（CRLF 误报已排除）。
- 包依赖图：单向分层无循环（contract/config/events/artifacts/store/llm/react/webfetch 为底，api 顶层组装）。
- 全仓 TODO/FIXME：0；超 150 行函数仅 4 个（最长 scenegen.sceneGen 206 行）。
- 生产代码 time.Sleep 仅 2 处（produce.go:324 一秒轮询、segpipe.go:584 四二九冷却 2 分钟）。

## 七、修复优先级建议

- **P0（出片链路上的死结/丢账，建议立刻）**：高-1（verdict 空卷）、高-5（launched 死转，连带中-7 轮次上限一起修）、高-11（fallbackOldScene）、高-9（Rework finished）、高-12（rework-started 一行修）、高-7（IdeaPane force，连带中-1）。
- **P0.5（云与并发安全）**：高-2（artifacts 双斜杠+补测试）、高-4（删项目取消 prep）、高-6/中-30（互斥收口一张表）、高-8（llmGate 下沉 postChat）。
- **P1（体验与浪费）**：高-3（hasVideo 统一口径）、高-10（Producing 门）、中-3（materials 原子写）、中-10（prereview 漏审）、中-13（带伤通知如实）、中-31（token 增量缓存）、中-36（confirm 清场）。
- **P2**：其余中危与低危按域批量处理；失实注释随对应代码修复顺手改。

## 八、做得好的地方（防误伤清单）

- **注释文化是全仓最大资产**：几乎每个非显然决策都带「为什么+实测事故日期」（seg07 渲染器掷硬币、10-17 八 worker 同秒重试、10-18 代际误杀、幽灵轮排队、SSE 不设 WriteTimeout 的原因……），这些注释本身就是防回归护栏——本次挑出的十几处失实注释反而是「好注释太多」的副产品。
- **取消与进程树收口**（ctxCommand 整树击杀 + WaitDelay）、**jsonltail 尾窗读取**（把无限账本读成本常数）、**三层缓存防吐旧画面**（旁车指纹/src-fp 内容哈希/segStale）是同类代码里的优等生。
- **安全细节**：webfetch 双层 SSRF 防线（DNS 复核+拨号层复核+逐跳重定向）、登录常量时间比较、404 不暴露存在性、HMAC 临时视频口、vendor 哈希不符拒收不重试。
- **Python ai/ 无高危**：级联回落、指纹闸、幂等写保 mtime、原子替换、cp936 输出守卫，工程纪律整齐。
- 前端 reqSeq 代次作废+在途去重（含 A→B→A 边界）、IME Enter 守卫三处齐备、资源清理纪律普遍良好。

## 十、修复落地记录（2026-10-19 下午施工，未重启服务、未跑真机）

**验证状态**：`go build`/`go vet` 全绿；`go test ./...` 全部 20 包通过（含并行会话补的边界重派回归测试）；前端 `vue-tsc --noEmit` 零错误 + `vite build` 通过（构建脚本已改为类型检查+构建双门）；Python 11 脚本 py_compile 全过。改动 66 文件 +1425/−535。

**已落地**（按报告编号）：
- 高危 12/12：高-1/3/4/6/7/8/9/10/11/12 本会话修复；高-5（bndBusy 死转）由并行会话修复+回归测试，本会话在其上补齐**中-7 轮次上限**（超 2 轮带伤放行）与真实轮次落盘——两条必须成对，缺一不可。
- 中危 42 条中 **38 条**已修：中-1/2/3/5/6/7/8/9/10/12/13/14/15(Go 互斥+Python 原子写)/16/17/18/19/20/21/22/24/25/26/27/28/29/30/31/32/33/34/35/36/37/38/39/40(最小修：两份清场补齐)/42。
- 低危：死代码清仓（shrinkImage/.hyperframes-ok/shotAt/pngDiff\*移测试/auditReq.round/api.ts 三方法/_=i）、gofmt 全仓归零、注释失实 14 处中 12 处已改（含 director 文件头退役标注、produce 包注释补双路由说明）、前端 L 项（LoginView 双发/removeRef 可单删/ActivityPane 稳定 key/pickedIdx computed/卸载合并）、Python 低危全批（av_common stderr/vendor curl/字体与样张原子写/extract 人话报错/tts_align 中间态收敛）。

**刻意未做**（等测试窗口或需拍板，不是遗漏）：
1. **中-23 ReAct 三份拷贝合并**——最大的一刀，行为面广，无真机验证窗口不动刀；期间已修复其重名注册的索引隐患提示未动（原逻辑依赖两 slice 对齐仍成立）。
2. 中-40 useProject composable 全量重构——只做了最小修（清场补齐 chatDraft/progressLine/prep/activity），composable 化留给专门 PR。
3. produce.go/server.go 拆文件、vite manualChunks 代码分割、库/词表三份请求收敛（M7）、sceneGen 样张图缓存（L4）、hasFFmpegFilter 锁外探测——纯结构优化，收益/风险比不赶在本批。
4. 「待拍板 3 条」（Rework 走 legacy 管线 / frameQA 抽帧 fatal / 词表先落表）——按报告约定不动。
5. 修复轮渲后 frameQA 对 fallback 沿用段必查（高-11 的 ⚠ 配套）——需要动 produce.go 修复轮名单流向，等真机验证窗口一起做。

**施工方式备忘**：全程未重启服务、未触碰运行中项目的数据目录；子智能体限流阵亡的 Python 部分由主会话直改；与并行会话（萤火虫实测修复线）在工作区共存，其 segpipe/transcript 改动已并入并通过全量测试。

---

## 九、逐条解决方案（施工蓝图；落地状态见第十节）

> 粒度说明：高危给到「改哪、怎么改、怎么验」；中危给方案要点；低危给批量处理办法。标⚠的方案有设计取舍，动工前值得再想一分钟。

### P0 批：出片链路死结与丢账（高-1 / 高-5+中-7 / 高-9 / 高-10 / 高-11 / 高-12 / 中-1）

**高-1 verdict 空卷必拒** — 改 `review/verdict.go:108` 一行：
```go
if len(v.Findings) == 0 && p.dims != nil && countFails(v.Dims, p.dims) > 0 {
```
补单测：终审档案（dims=nil）+ 空 findings → 通过；段审档案 + 有 fail 维度 + 空 findings → 仍报错（防回归）。`go test ./internal/review` 验证。

**高-5 launched 死转（必须连带中-7 轮次上限一起修）**
- 把 tracker 里的局部 `launched` map 挪进 coordinator 成 `bndInFlight map[string]bool`（c.mu 保护）；auditBoundary 的**所有出口**（通过/审查器故障降级/打回/刹车）在锁内 `delete(c.bndInFlight, b.id)`——tracker 派发前查这张表。
- 轮次上限：boundaryCtx 加 `round int`；auditBoundary 调 `saveVerdict(c.p, "bnd", b.id, round, &v)` 用真实轮次（别恒传 1）；round > 2 时不再打回，按当前代际记账通过，journal detail 写「边界审轮次耗尽降级」。
- 验证：segpipe_test 加「边界打回后下一轮能重新派发」用例；真机故意造一个坏边界观察三轮后降级。

**高-9 Rework 不重置 finished** — `produce.go` Rework 的 `gen := claimRun(...)` 之后（仍在 mu 临界区内）补 `delete(finished, projectID)`；更稳的做法是把 delete 收进 claimRun，让「签代际票」和「重置终态认领」绑成原子动作。补测试：制作失败 → Rework 失败 → manifest 仍出现 rework.error 终态行。

**高-10 draft_style_samples 缺 Producing 门** — tools.go:316 gate 开头照抄邻居：
```go
if s.Producing { return "制作运行中，不能重出样张——先等本条完成" }
```

**高-11 fallbackOldScene 不可达** — `scenegen.go:250/258` 门槛去掉 `only == ""`，只留 `instruction == "" && feedback == ""`（用户点名改的段仍不许静默沿用）。沿用时 journal 记「顽固段带伤沿用旧画面」，与中-13 的通知聚合同口径。⚠取舍：带伤沿用 vs 整片报废，方向与 10-18 段审带伤放行一致，但沿用的旧画面未经本轮判卷，建议渲后 frameQA 对这些段必查（并入 Repaired 名单）。

**高-12 rework-started 断线** — App.vue StagePanel 标签补 `@rework-started="refresh"`；顺手删 VideoPane.vue:28 的死 emit 声明和 StagePanel:324 的中转。一行修 + 两行清。

**中-1 force 半成品不作废** — runner.go Start 里 force 分支不再包在 `HasArtifact(StationStoryboard)` 里：force 为真无条件 `InvalidateFrom(p, StationScript)`（InvalidateFrom 本身幂等，无工件时是空操作）。

### P0.5 批：云安全与并发互斥（高-2 / 高-4 / 高-6+中-30 / 高-8）

**高-2 artifacts 双斜杠** — SyncUp/Restore/Prune 三入口第一行统一 `prefix = strings.TrimSuffix(prefix, "/")`（或抽 `relOf(key, prefix)` helper 三处共用）；`artifacts_test.go` 补 `prefix := "projects/p1/"` 带尾斜杠分支，断言 npm-cache 判 Garbage、QA 截帧判 Discard。修完后跑一次 `storagemaint` 看日志分类是否符合预期。⚠注意：修好那天起 Prune 才真正开始删本地 Discard/Garbage——观察首轮 GC 的删除量，别被吓到。

**高-4 删项目不取消 prep** — deleteProject 开头加 `if s.Preps != nil && s.Preps.IsRunning(id) { 409 "准备阶段运行中，先停止再删除" }`；RemoveAll 之前 `s.Preps.Cancel(id)`（与制作同口径）。

**高-6 + 中-30 三入口互斥收口** — 方向：**所有 prep 执行收进 Runner 一把锁**：
- `prep` 包加 `RunInline(ctx, id, station, instruction)`——内部走 Start 同款 running map 临界区（在跑即拒绝），对话侧 rerunPrep/draft_storyboard 改调它，同步等结果；
- api 的 runPrep 保持 `Preps.Start`（本就互斥），另在启动前 `turnMu` TryLock 失败即 409（防「对话轮正在跑」窗口），TryLock 成功立即 Unlock（锁只用于瞬时探测，不长期持有）；
- saveManuscript（api+agent 两处）与 startProduce、deleteProject 补 `Preps.IsRunning` 检查——五个入口做成一张对称表或统一 `assertNotBusy(id)`。
- 验证：并发 curl（runPrep × chat rerun）不再双开；`/prep` 状态接口在对话内重跑期间显示运行中。

**高-8 Chat 绕过 llmGate** — chatTemp 的重试循环里，退避等待之后、postChat 之前插过闸块（照搬 generate:198-203 的写法）：
```go
select { case <-ctx.Done(): … case llmGate <- struct{}{}: }
resp, err := func() (…) { defer func() { <-llmGate }(); return p.postChat(ctx, req) }()
```
顺手修三处：provider.go:23 注释改真、chat.go:72 退避加 jitter（照搬 generate 的 +rand）、删 chat.go:64 死赋值。验证：单测把 LLM_MAX_CONCURRENCY=1，用假端点并发打 Chat，断言在途不超 1。

### P1 批：正确性修补（高-3 / 高-7 / 中-3 / 中-8~15 / 中-19~22）

**高-3 has_video 统一口径** — api 抽 `func (s *Server) hasFinalVideo(id string) bool`：本地 Stat miss 回退 `s.Artifacts.Stat(ProjectKey(id, "renders/main.mp4"))`；listProjects/startRework/overview 三处替换；agent.State 的 HasVideo 通过给 Agent 注入 artifacts 句柄或 `HasVideo func(id) bool` 回调同源化。

**高-7 IdeaPane 重跑按钮** — `start()` 改 `const force = rerunOpen.value || forceRerun.value` 传给 runPrep；76 行 window.confirm 与中-36 一并换自绘确认。手测：工件已存在 → 换想法重跑 → 直接开跑不 409。

**produce/segpipe 组**：
- 中-3 materials 原子写：saveMaterials 改调 `pipeline.WriteFileAtomic`（一行）；loadMaterials 注释写明「读靠原子替换保证」。
- 中-8 stitch_fix：auditBoundary 打回路由前先摘出 stitch_fix 项——若全部 high 都是 stitch_fix，按当前代际记账通过并把该边界入 takeStitchFixes 清单；清单消费后 saveVerdict 打 consumed 标（或删 verdict），让「取走即清」注释变真。
- 中-9 终审降级：segpipe.go:1191 err 分支改 journal 记 `final.degraded` 后继续出片（与段审同款）；预算烧尽（ErrBudgetExhausted）同路径。
- 中-10 prereview 漏审：两处失败 `return` 前把该段 append 进 `res.Repaired`（渲后复查名单兜底）。
- 中-11 邻段指纹：sceneSidecarFingerprint 追加前/后段 `Brief.FirstFrame/LastFrame` 的哈希摘要。
- 中-12 nil deref：`if blocking != nil` 分支包裹 TailLines，else 直接返回 err。
- 中-13 带伤通知：runSegmentPipeline 收尾扫 journal 带伤/降级段，notifyOnDone 文案改「全片制作完成（N 段带伤，明细见 reviews/ 与过程页）」+ manifest 汇总行。
- 中-14 无条件重渲：renderSegment 渲前调 segStale 判新鲜即跳过（.sum 旁车 RenderSeg 已写好）；segpipe.go:480 注释同步改口。

**agent/prep 组**：
- 中-19 noGate 扫盘：`Gate: func() string { if t.gate == nil { return "" }; return t.gate(a.State(p.ID)) }`，noGate 工具 gate 字段改 nil。
- 中-20 videoProbeProv：写并入 vidMu；videoSupported 改参数传 prov。
- 中-21 submit 落盘顺序：三工位统一「legacy 产物先写 → 工件标记最后写」；script.go:309 的 `_ =` 回流给模型。
- 中-22 渲染缓存：cachedRender 命中时幂等回填 draft.Samples + 镜像 PNG（缓存产物本就在盘上）；或缓存键并入 stylepack 代际，且 InvalidateFrom 一并清 llm/prep-render-cache。⚠推荐前者（回填），彻底解决「命中即卡死」。

### P2 批：效率、整洁与注释

**效率热点**（各自独立、可分批）：
- 中-24 抽帧并行：extractFrame 批量化 errgroup（并发 4），帧缓存键含 t 无冲突；终审 serviceFS 提循环外。
- 中-31 token 缓存：agentTokenTotals 按 (path,size,mtime) 记忆化，或读侧维护累计账。
- 中-16 writePostFX：比较前 `json.Unmarshal(json.Marshal(want), &m2)` 再比；transition 令牌同款短路。
- 中-28 webfetch：client/Transport 提包级。
- 中-29 stylelib：catalog 接口加 GetStyle(id)，PG 走 WHERE。
- 其余（sceneGen 样张图缓存、finalFeedback 提 LoadTimeline、prepView 两次 LoadStylePanel、videoStyleID 正则提包级、chatTemp jitter 已并入高-8、ActivityPane 稳定 key）见对应条目顺手修。
- 前端包体：vite manualChunks 拆 vendor（vue/naive-ui 各一块）或页签级动态 import。

**整洁度批量**：
- 死代码一次性清仓（低危清单已列全：shrinkImage / .hyperframes-ok / shotAt / pngDiff\* 移测试 / fetchOne idx / auditReq.round / api.ts 三方法 / VideoPane 死 emit / `_ = i`）。
- 中-4 director M2：短期先在文件头补退役注释；中期 M1（directorNoteFor/directorFingerprint）拆到 scenegen 侧，M2 与 3 个测试文件等 legacy 项目清零后整删。
- 双实现收敛：中-34 saveManuscript 收进 `prep.ReplaceManuscript`；ProjectCanvas / 库筛选样板 / snapshot 调用（中-6 抽 `snapMainComposition` + `hyperframesVersion` 常量）各自单点化。
- 中-23 ReAct 三拷贝：review.Loop 把 ChatTemp/OnTool/TaskID 泛化后并入 react 包（ChatTemp 作可选配置），prep/review 改引 react；重名注册照 react.Core:73 用工具 slice 索引。⚠这是本批最大的一刀，单独一个 PR 做，前后行为用既有测试锁住。
- gofmt：`gofmt -w` 一次跑掉 6 文件偏差（CRLF 环境注意 git 配置）。

**前端结构**（中-36~42）：
- confirm/alert 全换两段式或 NPopconfirm，alert 换 topMsg；中-37 TDZ 把 pickerFor 等声明提到 watch 前；中-38 照抄 ChatPanel 的 onEnter；中-39 activity 套 3s 节流 + 5s 定时器单源；中-40 抽 useProject() composable 收敛状态与清场；中-41 build 改 `vue-tsc --noEmit && vite build` 并补返回类型；中-42 补 onDeactivated/onActivated 成对。

**其余中危**（未归入上面各批的）：中-2 Start 锁内只留检查+claimRun，IO 移锁外失败回滚；中-5 抽 awaitNotOrphaned 共用；中-17 manifestMu 并入 ProjectMu 锁表；中-18 先 ValidateStoryboard 再 directorPass；中-25 MCP 帧先验 result/error 键；中-26 styleref 变异收进 Store.mu 的 mutateRef；中-27 启动扫 references 把 running 改 error + 解析 ctx 接进程根；中-32 区分无行/DB 错上抛；中-33 failWindow 内嵌 mutex；中-35 中文数字按十进制解析（X十Y = X\*10+Y，十Y = 10+Y），注释「钳到」改「丢弃」。

**注释失实 14 处**：随对应代码修复顺手改（高-6 改 runPrep 注释、中-14 改「幂等」、中-8 改「取走即清」、高-8 改 300s 注释等）；其余独立小 PR 一波清掉。

**待拍板 3 条**（Rework 走 legacy 管线 / frameQA 抽帧 fatal / 词表先落表）：拍板前不动，拍板后按结论补注释或改行为。

### 施工与验证顺序建议

1. **第一批（半天~一天）**：P0 七项——全是小刀口，出片链路先立稳；每项补一个回归测试。
2. **第二批（约一天）**：P0.5 四项——互斥收口是行为变化最大的一个，动完跑一次「对话重跑 + 想法页自动完成」并发实测。
3. **第三批**：P1 按域批量；其中中-22（渲染缓存回填）和中-13（带伤通知）建议同 PR，口径统一。
4. **第四批**：P2 死代码清仓 + gofmt + 注释一波清；中-23（ReAct 合并）单独收尾。
5. **每批收尾**：`go build && go vet && go test ./...` + 前端 `vue-tsc --noEmit && vite build`；涉及制作链路的批加一次真机冒烟（三体类短项目即可）。
6. **提交纪律**：每条修复在提交说明里引用本报告条目编号（如「修 高-5」），并在本文档第二节对账表回写状态——别再让「说明写了」和「代码改了」分家。

---

*报告人：ZCode 全维审查（机械检查 + 7 路并行深审 + 高危全量人工复核）；Python 路因子智能体被限流由主会话亲自补审。发现总计：高危 12（旧账 7 + 新发现 5）、中危 42、低危约 40、待拍板 3。第九节解决方案为施工蓝图，标注 ⚠ 处含设计取舍。*
