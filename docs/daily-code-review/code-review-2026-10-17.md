# 全面代码巡检报告 · 2026-10-17 凌晨例行

- **巡检对象**：HEAD = `335041d`（工作区仅 4 份测试纪要未跟踪文档，无代码改动）
- **重点范围**：近两天的四工位架构改造——`c028f75` 段级流水线（+4241 行）、`7508f26` 准备阶段三工位（+4488 行）、`428229e`/`cf97f12` 风格库与项目产物上 MinIO+GC（+3700 行）、`76b5807` 想法页签、`362e50d` 预审收敛、`0121d87` 风格工位契约三修
- **方法**：机械检查全跑（build/vet/test/构建/语法）+ 四路并行子智能体分模块深审（段级流水线 / MinIO+GC / 准备工位 Go 侧 / 前端+ai 增量），并发正确性与数据安全为第一优先级

## 总评

四工位架构改造的骨架设计普遍成熟（三池单向依赖、刹车收口、journal 恢复、工件驱动重放、GC 三道安全阀方向都正确），但深审挖出 **9 个高危 bug**。最重的一串集中在「边界审完成」这条路径上——而真机实测（docs/test-result/e2e-prep-prod-20261016.md）里制作只跑完 3/19 段就被 429 限流打死，这条路径一次都没走通过，等于三个必现的死锁/死转埋伏在完全未验证的代码上。上云那侧没有真正丢数据的路径，但有一个让整套 GC 分类静默失效的前缀 bug。

---

## 高危 bug（9 个）

### 段级流水线（c028f75）——三连死锁/死转，全在真机未验证路径上

1. **`server/internal/produce/segpipe.go:939`（及 929）——边界审完成即自死锁冻结全管线**
   auditBoundary 持着 `c.mu` 锁去调 segGen，而 segGen 内部又要拿同一把不可重入的锁——任何一个边界审跑完，整个管线永久挂死，`allPassed()` 也跟着阻塞，连 ctx 取消都救不回来。
   改法：先在锁外算好 `sg, ng := c.segGen(s), c.segGen(n)`，再持锁赋值（两处同改）。

2. **`server/internal/produce/segpipe.go:875`——边界审被打回后该边界永远不再审，流水线死转**
   打回（high）路径不复位 launched 标记，代际永久失配，`if launched[i] { continue }` 使该边界再无审计机会，waitAllPassed 空转到用户手动取消。
   改法：auditBoundary 的 high 分支通过回调/共享标记复位 launched。

3. **`server/internal/review/verdict.go:108`——干净的成片交不了空意见单，终审必拒**
   终审 findings 为空时条件短路恒真——模型只能编造 low 意见凑数，或预算烧尽（finalBudget=2）直接整片报废并触发两轮自动重跑白烧。
   改法：条件改为 `p.dims != nil && countFails(v.Dims, p.dims) > 0`，终审允许空卷。

### 上云与 GC（cf97f12 / 428229e）

4. **`server/internal/artifacts/artifacts.go:208/237/287`——三分类（Keeper/Discard/Heavy）在生产路径整体失效**
   所有全前缀调用传 `ProjectKey(id,"")`（带尾斜杠），`TrimPrefix(key, prefix+"/")` 拼出双斜杠永不匹配，`Classify` 拿到整 key 后 Discard 目录判断全部失配 → 一切文件都被判成 Keeper：①QA 截帧/渲染壳等设计上不上传的 Discard 全量传上 MinIO；②Prune 永不清非 heavy 的本地可再生文件；③Restore 把误传的垃圾拉回本地——GC「省空间」目标反向膨胀。单测用无尾斜杠的 `"projects/p1"` 所以全绿，掩盖了 bug。
   改法：SyncUp/Restore/Prune 入口先 `TrimSuffix(prefix, "/")` 再拼 `prefix+"/"`，补带尾斜杠的用例。

5. **`server/internal/api/server.go:653/656/666`——GC 删掉本地成片后项目状态倒退成「无片」**
   `has_video`/"video" 状态只 `os.Stat` 本地 `renders/main.mp4`，GC 超窗删除后列表显示无片，但播放接口能从 MinIO 正常播——用户视角"成片丢了"。
   改法：本地 miss 时回退 `s.Artifacts.Stat(ProjectKey(id, "renders/main.mp4"))`（与 video handler 同口径）。

### 准备工位（7508f26 / 76b5807）

6. **`server/internal/api/server.go:1038` + `prep/runner.go:63` + `agent/tools.go:47`——prep 三个入口两两无互斥**
   想法页 runPrep 只查 Producer.IsRunning 不拿 turnMu；对话侧 rerunPrep/draft_storyboard 不查 Preps.IsRunning；saveManuscript 级联也不挡——同项目可并发两条 prep 链互相覆盖 script.json/stylepack.json/prep-draft.json 并双倍烧 LLM。
   改法：项目级运行锁收拢一处（runPrep 对 turnMu TryLock 失败即 409；对话侧执行前查 Preps.IsRunning 拒绝）。

7. **`server/internal/api/server.go:715`——删项目不取消 prep 链**
   目录删掉后后台 goroutine 继续 `MkdirAll` 重建目录写工件，收尾 SyncUp 把已删项目的产物重新上传远端（云端留尸）。
   改法：删除前 `s.Preps.Cancel(id)` 并按 IsRunning 拒绝（与制作同口径）。

8. **`server/internal/prep/runner.go:91`——「换个想法重跑」在半成品态不作废旧产物**
   force 只在 storyboard 工件已存在时才 InvalidateFrom：上轮失败停在①②（storyboard 缺）时 force 不作废，新想法只落盘不消费，用户以为全重跑、实际拿旧①②产物续跑③。
   改法：force 为真时无条件 `InvalidateFrom(p, StationScript)`。

### 前端

9. **`web/src/components/IdeaPane.vue:79`（配合 125/143）——「换个想法重跑」按钮功能不可用**
   rerunOpen=true 时勾选框被 `v-if="!rerunOpen && exists"` 隐藏，forceRerun 恒为 false，`runPrep(..., force=false)` 必被后端 409 拒，报错文案还让用户去「勾选」一个该模式下根本不显示的框。与第 8 条是同一条用户路径的前后端两半。
   改法：start() 里 rerunOpen 为真时强制传 force=true。

---

## 中危（13 个）

### 段级流水线

- **`segpipe.go:476-481` 终审打回时恢复进场的已过段先盲写一轮**：首轮 feedback=""，终审意见被缓冲要等「盲写→检→渲→审」整轮（分钟级）跑完才消费；且恢复的 round 是历史值，首打回即可能撞「打回超限」刹车。改法：spawnWorker 支持传入初始 feedback 直接进重写。
- **`segpipe.go:994-995` stitch_fix 机制性死路**：清单只认 `high && stitch_fix`，但带 high 的边界永不过审到不了拼接位，清单恒空；high stitch_fix 反被当重画意见喂段重写，与提示词自相矛盾。改法：high+stitch_fix 不进重画路由、记清单放行该边界。
- **`segpipe.go:689` renderSegment 无条件重渲不查 segStale 指纹**：断点续跑时已渲好未过审的段整段重渲（stitch 侧反而会跳过），同一段片两套判旧口径。改法：先按指纹判新鲜再渲。
- **`ai/assemble.py:485-509` × `segpipe.go:629` 无互斥并发跑**：写池 ≤4 worker 各自全量重写 index.html 和全部 .hf-seg/*.html（非原子），与并发拷贝/渲染读盘构成撕裂窗口，拷到半截段工程会误判 check 失败烧修复轮。改法：per-project assemble 互斥或 tmp+rename 原子写。
- **`scenegen.go:36-58` 邻段承诺不入段缓存指纹**：directorNoteFor 注入邻段 Brief 首/末拍，但 sceneFingerprint 只哈希本段字段——邻段改稿后本段命中旧缓存，衔接承诺不生效。改法：指纹追加邻段 Brief 摘要。
- **`segpipe.go:1018-1021` 终审审查器故障不降级**：段审/边界审故障都「降级放行」，终审一次 LLM 抖动/预算烧尽却直接整片失败（再叠两轮自动重跑）。改法：与段审同哲学记账降级。
- **`review/agent.go:95` + `tools.go:203` videoProbeProv 包级全局并发无锁读写**：data race（-race 必报；实际写同型对象破坏性低）。改法：参数/atomic.Value 传入。

### GC

- **`server/cmd/server/gc.go:66-81` 参考库清扫无 busy 保护**：busy 钩子只查 producer/preps，styleref running 没接线——重跑解析期间 GC 可删本地文件打断任务（远端有正本不丢数据，但任务失败）。改法：IsRunning 并进 busy 钩子。
- **`gc.go:53-60` busy 检查与 Prune 长遍历之间 TOCTOU 窗口**：busy 通过后用户立刻开工，Prune 遍历中删掉刚回源的段片，首跑失败靠 auto-retry 兜底（烧一轮）。改法：busy 回调传进 Prune 按文件复查。
- **`stylelib/stylelib.go:429-455` 回退本地媒体路径失效**：注释明言"参考视频目录恒在本地盘"，GC 会删 references 本地大文件——回退 references 的老条目媒体 404（对象其实在桶里）。改法：回退分支改走 artifacts Localize/List。

### 准备工位

- **`prep/storyboard.go:144`（同型 script.go:304 / style.go:710）+ `server.go:1171` submit 落盘顺序有崩溃窗口**：先写跳站标记后写 legacy 产物，中途崩则重放判「站已完成」但 manuscript.json/storyboard.json 缺失，下游静默用旧路径再生成/终审开门后被三前置卡死。改法：legacy 产物先落、prep 工件标记最后写。
- **`prep/style.go:436 vs 416` render_sample 文案与实现相反**：文案说「重新调用会替换当前张」，实现是 append 且 3 张封顶直接拒——模型迭代 3 次后被锁死，被迫带最差版本提交。改法：实现按 idx 真替换，或改文案并允许剔除。
- **`prep/artifacts.go:187` + `run.go:86` 会话日志 O_TRUNC 打开**：冷重试整站重跑时把上一次 llm/prep-*.jsonl 整个截掉，与「双记账供回放」的包注释相悖。改法：O_APPEND 或文件名带尝试序号。

### 前端

- **`PrepPane.vue:147` 反馈输入框无中文输入法守卫**：@keydown.enter 没拦 isComposing/keyCode===229，拼音候选上屏的 Enter 把半截反馈误发。改法：套用 ChatPanel.vue:17-21 的组合期守卫。
- **`ActivityFeed.vue:215` + `IdeaPane.vue:101` prep.* 事件全部落 default 不上屏**：准备阶段几十分钟事件流全程空白，与运行态文案「详细动作在右侧事件流有账」矛盾。改法：补 prep.* 分支或改文案。
- **`VideoPane.vue:58-60` readyToStart 误导老项目**：只看 gates.style_confirmed，legacy 项目确认风格后误显「终审已通过」。改法：补 prep 维度或文案改两路径通用。

---

## 低危（压缩列出）

**吞错误类**
- `segpipe.go:365` journal 追加写失败无声（账本是恢复事实源，应至少 stderr/manifest 留痕）
- `segpipe.go:811/935/1022` saveVerdict 失败被 `_ =` 吞——修复轮静默退化成首审
- `prep/runner.go:128` 终态 SyncUp 错误被 `err == nil && n > 0` 静默吞掉，无日志

**并发/阻塞类**
- `segpipe.go:842` dispatchRewrite 无 ctx 保护的无界阻塞发送（worker panic 退出后缓冲满即卡死 audit goroutine）
- `pipeline.go:1951` 渲染管线内同步 FPutObject（上限 30min），公网 MinIO 每段尾部阻塞秒到分钟级，可改异步队列

**逻辑/边界类**
- `prep/runner.go:156` 中文数字「十五→105 分钟」算错后被超限静默丢弃，用户说了时长没生效也无提示
- `prep/runner.go:98` idea 无长度上限，超长想法直接吹①② kickoff 上下文（落盘前 TruncRunes 2000）
- `prep/loop.go:66-74` 重名工具处理抄了 react.Core 半截：模型看到旧 schema、exec 跑新 Run（当前无重名，潜伏坑）
- `stylelib/migrate.go:76-89` 词表化石缺失而 styles.json 非空时零条目导入还算成功并改名化石——条目被静默永久搁置

**性能类**
- `server.go:1098` prepView 里 LoadStylePack/style_samples.json 各读两次（轮询每轮双读）
- `segpipe.go:1256` finalFeedback 循环内每条 high 重新 LoadTimeline 读盘

**前端低危**
- `StagePanel.vue:185` 点事件流浮层内滚动条误判为空白区直接关浮层（高频操作）
- `PrepPane.vue:106` 终审样张只走静态 html 忽略 s.video 迷你真段——唯一人工确认门看到的预览反而比风格页签少
- `api.ts:110-115` draftManuscript/extractLink 已无调用方成死码
- `types.ts` 新增 PrepScript.source_mode/.text、stylepack.sample_ids/.confirmed、draft.plan 前端全未使用
- `ManuscriptPane.vue:41` `as` 断言绕类型（字段确实存在，应进返回类型）
- 过时注释/文案 5 处：StagePanel「四页签」实为六页、VideoPane「AI 起草」入口已迁、store.go:138 UpdateProjectMeta 注释、.env.example MinIO 生效条件写窄了、loop.go 预算注释数值过时

---

## 机械检查（全绿）

`go build` / `go vet` / `go test`（15 包全过）/ 前端 `npm run build` / 全部 Python 脚本 `py_compile`——通过。
gofmt 有 **7 个文件漂移**（contract/prep.go、prep_test.go、skin_test.go、design.go、segpipe.go、govern_test.go、stylelib.go，均为近两日新代码），`gofmt -w` 一把清。

## 修复优先级建议

1. **段级流水线三连（#1/#2/#3）**——新架构的成片路径根本走不通；修完建议补 fake-LLM 的「边界审+终审闭环」集成测试再放量（现有测试只覆盖纯函数，核心并发路径零覆盖）
2. **GC 前缀 bug（#4）**——分类失效已在上传的存量垃圾，越晚修回填越贵；连带修 #5 状态倒退
3. **prep 锁缺口（#6/#7/#8）+ 前端 rerun（#9）**——同一条用户路径的前后端两半，在想法页签被高频使用前补上
4. 其余中危按模块顺手清；低危可攒批处理
