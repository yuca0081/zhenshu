# 真素材"进片但看不见"修复（2026-11-03）

> 对象：p20261102-204948「素材链路测试·1分钟」（59.07s，2026-11-02 21:16 出片，终审通过）。
> 结论：素材确实进片了（seg01 片头 0–7.4s，`<img>` 声明 942×634 = 画面 28.8%，过 hero 闸门），
> 但**观众看不见**：三块 90% 不透明米色遮罩盖掉图面 79%，只剩中间 662×190 一条窄带（≈画面 6%）；
> 旁白点名的「5.1k stars」「DeepSeek Harness Team」是 HTML 叠字，真截图的 `☆ Star 5.1k` 徽章在顶部遮罩底下。
> 而且这一轮 9 段段审 **10 次全部"审查器故障降级"**（seg 级真检 0/9），终审判据里也没有"必需素材可见性"，
> 所以没有任何一道 AI 关卡看见这件事——成片是靠闸门（只核声明尺寸）放行的。

## 修复一：审查 Loop 的并行 tool_calls 消息契约（根因，最高优先）

旧版 `review.Loop.Run` / `prep.Loop.Run` 在 tool_calls 循环**体内**追加"系统附图" user 消息。
一轮并发 ≥2 条 tool_calls 时，先是 tool → user(图) → tool：tool 消息被隔断，GLM 端点回
`400 An assistant message with 'tool_calls' must be followed by tool messages`，审查器记故障、
降级放行。2026-11-02 那支片 9/9 段审全程盲跑即此。

改法（两遍扫描）：先执行本轮全部 tool_calls 并把 N 条 tool 消息**连续**追加；再把本轮所有
附图合并成**一条** user 消息接在块尾（`imageFollowUp` 一图一 URL，不拆消息）。
submit 当轮通过时附图不再外发，但仍进对话留痕（过程页缩略图/回放要用）。
坏载荷（空 URL/截断 data URL/本地路径）先过滤，全坏则不追加空附图消息、不占裁剪记账位。

- `server/internal/review/review.go`：Run 循环 + `appendRoundImgs`/`recordImgsOnly`/`validImageDataURLs`
- `server/internal/prep/loop.go`：同构修复（②风格/③分镜也会并发自检）
- 回归测试：`review/loop_toolmsg_test.go`、`prep/loop_toolmsg_test.go`
  —— 锁两条不变量：assistant(tool_calls) 后紧跟 N 条同 ID tool；本轮附图只有一条、在块尾、图数齐全。

## 修复二：把"素材看不看得见"变成判卷官能判的题

落地闸门（`required_images.go`）只核 `<img>` 的**声明**宽高，核不出"被兄弟节点盖住/被叠字代替"。
按"平台质量关卡优先发挥 AI 判卷"的既定口径，这一层交给判卷官，程序只负责把证据喂全：

1. **写入期硬措辞**（`requiredMaterialsNote`）：可见面积按"没被盖住的部分"算；遮罩/蒙层/角色身体/文字叠层压住的部分不算面积；禁止用 HTML 文字复刻素材里的关键内容（叠字只能做标注）；遮罩只许压次要区域且整片透出（≤30%），证据区一块不许盖；角色/道具/字幕不得站在素材上；素材入场 0.5s 内可辨。
2. **段审喂原图**（`review.SegInput.Materials/MaterialImgs` + `produce/audit_materials.go`）：把本段认领素材的原图（720px JPEG）与落地要求（role/面积下限/入场/摆法）一并注入 kickoff；system 新增「素材可见性（①内容一致的核心子判据）」四条 fail 形态（被遮住认不出 / 证据只由叠字代说 / 角色压在素材上 / 只在末帧闪）。
3. **终审兜底**（`review.FinalInput.Materials`）：注入全片真图清单（文件+认领段+要求面积），⑤content 查项补"没出现或认不出 = fail"——段审降级时这是最后一道网。

## 明确没做

- 不加"遮罩覆盖率"之类的静态 lint / 像素机检（同 2026-11-01 方案口径：不堆硬编码规则）。
- 闸门仍是"声明尺寸"闸门，不改成渲染测量——可见性判定留在 AI 判卷层。
- **真机重跑按用户 2026-11-03 指示暂停**：本仓库只落代码，不重跑该片、不重写 p20261102-204948 的段文件。

## 重启重跑时的验收标准（待办）

1. 段审零降级（manifest 无 `审查器故障降级`，`reviews/seg-*.tools.jsonl` 有真取证）。
2. seg01 段审/终审对素材可见性给出明确判断；若仍被遮罩，意见单应点名"可见面积不足/证据由叠字代说"。
3. 重写后成片里 `☆ Star 5.1k` 从原图可读，不再由叠字代说；素材入场即可辨。
4. 全片终审通过且意见单不再只谈节奏/风格。
