# 制作可靠性修复方案（基于 2026-10-17 两轮 GUI 实测根因定位）

> 证据链：`docs/test-result/gui-e2e-prep-production-2026-10-17.md`（第二轮实测）+
> manifest/service log 复盘 + segpipe.go/produce.go/llm 静态分析 + run10 活体楔死实验。
> 每条给出：根因（文件:行）→ 改法 → 验收判据。

## 结论先行

三次制作尝试（9+1 轮管线执行）全灭的根因不是一个 bug，是**三层防线各自失守的叠加**：

1. **审查判据不稳**（意见掷骰子、预算频繁耗尽）——制造 high 的源头；
2. **刹车判据过敏**（跨轮累计、阈值 2）——把随机 high 放大成整片死刑；
3. **失败路径无兜底**（楔死无看门狗、cancel 被收尾 Wait 卡死、重跑整片重写）——把"一次失败"变成"永远卡死 + 天价重试"。

修好第 3 层（看门狗+硬终局）平台就**永远不会再出现"转圈到天荒地老"**；修好第 2 层管线能**失败得便宜且快速**；修好第 1 层才能**真正稳定出片**。

---

## P0-1 楔死主嫌：`dispatchRewrite` 无 ctx 保护的阻塞发送

**现象**：run9（12:07 起静默 20+ 分钟）与 run10（12:55:41 起静默 12+ 分钟，600s LLM 超时已过仍无动静）两度全管线零活动；两次打回（seg12/seg02）与一次边界打回（b08→seg08）都没能启动任何重写（llm/ 目录无新文件）。

**根因**：`server/internal/produce/segpipe.go:838`
```go
func (c *coordinator) dispatchRewrite(s *segCtx, feedback string) {
    s.feedCh <- feedback   // 阻塞发送：不看 ctx、无超时、无 worker 存活保证
}
```
- worker 的接收点在 `chainWorker`（segpipe.go:608 `<-s.feedCh`）。若目标段 worker 不在等（已退出/从未启动），发送方 goroutine **永久阻塞且无视 ctx 取消**。
- **worker 缺失有实锤路径**：启动循环对账本里已 audited 的段**不起 worker**（segpipe.go:437 `continue // 已过段：worker 也不起——打回时现起`），但"现起"只在终审打回循环里实现了（segpipe.go:479 `spawnWorker(s)`）；**段审（:834）和边界审（:970）的 dispatchRewrite 都没有 spawnWorker**。run10 的 `b08 → seg08（前段）`（12:52:59）正是边界审把打回路由给一个恢复进场、没有 worker 的段——教科书式触发。
- 泄漏放大成全局死：被堵的审查 goroutine 永不返回 → `c.run` 收尾的 `auditWg.Wait()`（segpipe.go:487）永远等不到归零 → run 不返回 → `finishJob` 不执行 → **无终态落账 + `Runner.Cancel` 的 ctx 取消也救不了**（cancel 能让 waitAllPassed/worker 退场，但排不掉那个永远堵着的发送）→ UI 永远"运行中"。这就是 P1-2（挂死）和 P1-3（停止无效）的同一条因果链。

**改法**（三件套，缺一不可）：
1. `dispatchRewrite` 改为带 ctx 与超时的发送：
   ```go
   func (c *coordinator) dispatchRewrite(ctx context.Context, s *segCtx, feedback string) {
       select {
       case <-ctx.Done():
       case <-time.After(30 * time.Second):
           c.brakes.trip(fmt.Sprintf("%s 打回投递失败（worker 不在场）", s.seg.ID))
       case s.feedCh <- feedback:
       }
   }
   ```
2. 段审/边界审打回路径先保证 worker 存活：`c.spawnWorker(s)`（把 spawnWorker 提为方法/去闭包依赖），或等价地给 feedCh 缓冲 1 并在 spawn 时兜底。**原则：任何 dispatch 前必须有接收者。**
3. 收尾 Wait 全部加限时：`chainWg/auditWg/dispatchWg/trackerWg` 用带超时的等待（如 90s），超时即放弃等待照常落终态（泄漏 goroutine 随进程生命周期消亡，不能绑架用户的状态机）。

**验收**：模拟"打回一个账本已审段"（单测：起 coordinator，journal 预置 audited，边界审注入 high），管线应在 30s 内刹车/失败并落 produce.error，而非冻结。

## P0-2 全局看门狗：任何静默都必须转成可见失败

**根因**：`waitAllPassed`（segpipe.go:522）无限轮询、`c.run` 无整体 deadline；LLM 单次 600s（llm/chat.go:30）×3 次重试（llm/provider.go:162）= 最长 **30 分钟合法静默**，与真楔死无法从外面区分。

**改法**：coordinator 起 watchdog goroutine——记录"最后进展时间戳"（任一 manifest/journal/segState 变更即刷新），超过 `PRODUCE_STALL_TIMEOUT`（默认 12 分钟，覆盖 600s+余量）无进展 → `c.brakes.trip("管线 N 分钟无进展，看门狗终止")`。刹车路径已存在且能正确落 produce.error（11:27/11:49 两次验证过）。

**验收**：人为挂起一个工位（如测试钩子），12 分钟内应看到 produce.error 而非转圈。

## P0-3 cancel 硬终局：用户停止必须停

**根因**：`produce.go:219 Cancel()` 只 cancel ctx，终态落账排在 goroutine 的 `runWithAutoRetry → finishJob` 之后；收尾 Wait 一卡（P0-1），终态永远出不来。API 返回 202"信号已发出"，但用户视角是"停不下来"。

**改法**：`Cancel()` 改两段式——cancel ctx 后起 60s 定时器；到点仍 `IsRunning` 则：强制执行 `finishJob`（写 `produce.cancelled` 终态 + 从 `running` 表摘除 + Hub 通知），原 goroutine 打上"已废弃"标记（其后续 finishJob 变 no-op，需加代际/作者标记防双写）。原则：**用户的状态机不依赖泄漏 goroutine 的善终**。

**验收**：楔死态点停止，60s 内 UI 回到"制作暂停/已取消"。

## P1-1 刹车判据过敏：随机 high 被放大成整片死刑

**根因**：`segpipe.go:142-183 brakeTracker`——`catSegs[cat][track]` **跨轮累计、永不清除、无同时性要求**：第 1 轮 seg05 出过 content high、第 3 轮 seg12 再出 content high 即判"系统性"处死整片（实测 5 次刹车，类别漂移 content→first_last→subtitle，被打回段落逐轮不同=意见本身就是随机的）。审查意见并非独立同分布的"系统性信号"，而是判卷噪声。

**改法**（保守→进取任选）：
- a. 同类别≥2 轨道的判定加"**同一轮内**"约束（现在跨轮累计）；
- b. 段过审（audited）时清除该段在 `catSegs` 各类别的记录——修好了就不该继续记仇；
- c. 阈值 2 → `max(3, 段数/4)`；
- d. 刹车动作从"处死整片"降为"该类别相关段全部重修一轮 + 通知用户可停止"，把要不要继续交给用户/预算。

**验收**：单测构造"第 1 轮 segA content-high、第 2 轮 segB content-high、中间无其他"，不应刹车；同轮双段同类别才刹。

## P1-2 审查器预算频繁耗尽：边界审 ≤4 回合不够

**现象**：`review.bnd_error "回合预算耗尽"`（review.go:41 ErrBudgetExhausted）在 run9（b05、seg04）、run10（b04、b05）反复出现；降级放行哲学没问题，但边界审反复失收敛意味着判卷任务超载。

**改法**：边界审回合预算 4→6（review/agent.go）；预算耗尽降级时把"未完成审查"显式标进账本（现在只有 manifest 一行，UI 事件流有但容易被淹没）；若连续 2 轮同一边界预算耗尽，对该边界做一次"末首帧对"简化审查（减维度）而非直接放行。

## P1-3 打回重写的经济学：失败一次 = 整片重烧

**现象**：自动重跑/「继续」每轮重写全部场景代码（llm.usage `scene segXX` 反复出现）；TTS 已有跨轮缓存（11:31 起 tts 秒级完成 ✓），画面层 `对齐复用/0 段重算` 也存在（重启续跑日志 ✓），但**段审打回后的重写没有复用推进**——每轮从 write 全链重走。

**改法**：重跑时对"账本已 audited 且指纹未变"的段直接落 stAudited（恢复逻辑 segpipe.go:404 已有！），只对未过段起链——**这条已经实现，但被 P0-1 的 worker 缺失打穿了**。修好 P0-1 后需回归验证"续跑只补未过段"。另：`maxSegRework=2`（segpipe.go:47）对 check 打回+审查打回混合场景偏紧，配合已有的 `fallbackOldScene`（scenegen.go:290，宁要旧设计的合格段）哲学，审查打回超限可降级沿用旧画面而非刹车杀整片（保持"只用于整片、rework 不适用"的现纪律）。

## P2 前端与体验（快速清单）

| 问题 | 位置 | 改法 |
|---|---|---|
| window.confirm 阻塞弹窗（昨日挂死根因，仍源码在） | `PrepPane.vue:25`、`App.vue:462`、`IdeaPane.vue:76`、`StagePanel.vue:226` | 换应用内确认弹层（naive-ui dialog），本项目已是 naive-ui 全家桶，成本半小时 |
| 新建弹窗创建后不自动关 | `NewProjectModal.vue` | 创建成功回调里关弹窗+清 draft |
| 准备期产物不可见：文稿页签 0 字、样张出 12 分钟后才显示 | 想法/文稿/风格三页签状态联动 | ①文字稿落盘即回填文稿页签（只读态）；②style_samples.json 一落盘风格页签即切样张态（现在等整个 prep 完成） |
| 事件流名不副实（准备期空白却叫用户看账） | 想法页文案 + 事件流面板 | prep.step 事件进面板（数据已有），或文案改"制作事件流（准备阶段看工位卡片）" |
| "一般几分钟"预期失准（实际 15-25 分钟） | 想法页/事件流文案 | 改"一般 15~25 分钟（视分段数）" |
| 智能体"有进展我随时时报"未兑现 | agent 侧 | 段审通过/打回的关键节点（非每段）聚合插话，如"6/13 段过审，2 段回炉" |
| 诊断基建 | `server/cmd/server/main.go:158` | DEBUG_PPROF 默认开（本机部署），或写进 .env.example 注释——本次若有 pprof，楔死现场一抓便知 |

## 已澄清的非问题（避免误修）

- **"对话栏 Enter 静默失效"改判**：用真实键击（CUA type+Enter）测试，发送正常（POST /chat 202）。昨今两日复现的失败全部发生在 Playwright fill+press 合成输入下——**自动化输入假象，非平台 bug**。前端 ChatPanel 的 IME 防护（isComposing/229）逻辑本身正确。昨日报告 P2-4 应同步改判。
- 终审 confirm 死按钮（昨日 P1-1）：本环境未复现，但 `window.confirm` 仍在源码，见 P2 快速清单第 1 条——风险未除，只是换了内核没炸。

## 修复顺序建议

1. **当天可完成**：P0-1 三件套 + P0-2 看门狗 + P0-3 硬终局（都是 segpipe.go/produce.go 局部改动，带单测）；
2. **次日**：P1-1 刹车判据（含单测）+ P1-2 边界预算 + P1-3 回归验证；
3. **随后**：P2 前端清单逐项扫掉；
4. 每步完成后跑一次"重启续跑"（现在零成本：段落全复用）+ 夜跑 3 连验证收敛率。
