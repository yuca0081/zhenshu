# 段-分镜改造双件落地：方案甲 para 令牌 + 段落制比武实验（2026-10-29）

按 `docs/para-transition-plan-2026-10-29.md` 开工当日完成：**方案甲（机械转场分层）已落地并真片验证**；**方案乙比武实验已跑**，两量化指标全胜、观感大步改善但未满分。方案书 §7/§8 已同步拍板与结果。

## 一、方案甲：para 令牌（段落制分层转场）

- **令牌语义**：段内 fade 叠化 0.4s + 段界 smoothleft 推切 0.5s；音频裁尾对接（outgoing 裁到转场起点、incoming 满音量顶接），不用 acrossfade（旁白开头不吃淡入）。
- **接线**：`transitionTokenOf` 缺省映射改 para（明说直切/硬切仍尊重 cut，叠化类措辞 xfade）；老项目 project.json 已写死的令牌不变。令牌三档：cut / xfade / para。
- **真片验证**（p20261023-110234，31 段 183.57s，零 token 重拼）：
  - 数学：成片 171.07s，收缩 12.50s = 25 段内×0.4 + 5 段界×0.5，逐秒吻合；timeline.json 起点全部符合 Σd−ΣD。
  - 音频：段界静音带实测 [25.26, 25.47]，incoming 首字 25.47 满音量起（=段起点+0.13s 首音垫，与 TTS 实测一致）——旁白零损伤。
  - 机理：合成红蓝片证实 smoothleft 为右缘软边推入；真片 5 处段界滤镜图按分类执行（manifest 记账 25+5）。
  - 样张：`data/projects/p20261023-110234/reports/para-sample/`（含完整 para 版成片 main-para.mp4 与边界抽帧；原 cut 版已恢复，备份在 renders/backup-cut-20261029/）。
- **已知观察**：smoothleft 边缘渐变宽，**暗画面段落间观感偏溶解**（推切缝被暗 incoming 掩盖）。若人评「翻页感」不足，改 `pipeline.go` 的 `paraEdgeKind` 常量换硬边型（wipeleft/slideleft）重拼即可，一行改动。

## 二、方案乙比武：段落制创作（§5.1 作业书直发工位）

材料：同片段落 1（seg01–03「破题：说明书之问」，3 分镜，网格 18.996s）。
驱动：`server/internal/produce/para_contest_test.go`（`PARA_CONTEST=<项目ID>` 跑），
复用工位全套机器（同系统提示词、同 provider/NoThink、同契约检查+lint、同
hyperframes check、同 RenderSeg 渲染、同 review.Service 段审），沙盒在
`data/paracontest/`（不进真实项目目录）。

### 三指标

| 指标 | 段落制（比武） | 分镜单写（基线，原片 seg01–03） |
|---|---|---|
| 契约一次过率 | **1/1 一次过**（0 修复轮；prompt 10149 / compl 4615 tokens） | **1/3**（seg01 一轮修复、seg02 一次过、seg03 两轮修复） |
| 段审 | **六维全 pass**（content/device/fact/first_last/style/subtitle），仅 2 条 low | seg01：device fail + 2 low；seg02：当轮无意见单；seg03：r1 过（1 low）但 r2 出 1 条 high（content） |
| 渲染 | 段落片 19.00s == 网格 19.00s；机检过（诊断全为已摘闸美术类） | —（基线为既有成片） |
| 过渡观感 | 机器印象（终审权在用户）：**背景舞台全程连续、无黑屏无硬切、每个边界后新信息点按旁白节拍入场**；短板=前景装置在分镜边界整体退场，留约 0.3s「空台换气」，非旧元素保留变形的连续演进 | 基线=每分镜独立成页 + 拼接层硬切（问题起点） |

观感证据帧：`data/paracontest/projects/p20261023-110234/reports/boundary-frames/`
（两边界各前/中/后三帧）；整段可看 `reports/para-contest-clip.mp4`（19s）。

### 结论与建议

1. **两量化指标全胜**（一次过率 1/1 vs 1/3；段审全绿零 high vs 基线各带伤），
   观感从「两张画硬切」升级为「同一舞台换景」——**建议进第二步换骨**。
2. 换骨提示词可吸收本次短板：段落作业书加一条编排要求——「分镜边界处旧元素
   优先保留变形（缩小/上移/变淡成新场景引言或角标），避免整体退场留空台」。
3. 强制拆上限（>5 分镜或 45s）维持方案书原议。
4. 比武成本一条段落 ≈1.5 万 token（含金样例+设计系统注入），与三段单写合计相当——
   换骨后段落并发生效，总成本结构不变、返工爆炸半径按拆上限受控。

## 三、改动清单

| 文件 | 内容 |
|---|---|
| `server/internal/pipeline/pipeline.go` | para 全套（常量/段落层直读/边界分类/泛化视频链/裁尾音频链/paraConcat/共用执行器/分发） |
| `server/internal/pipeline/design.go` | transitionTokenOf 默认 para + 直切/硬切→cut；schema 提示语 cut\|xfade\|para |
| `server/internal/pipeline/{para_test,para_verify_test}.go` | 单测（含泛化回归闸）+ 真片重拼夹具（PARA_VERIFY 门） |
| `server/internal/produce/para_contest_test.go` | 比武驱动（PARA_CONTEST 门；PARA_CONTEST_KEEP=1 复用产物续跑） |
| `docs/para-transition-plan-2026-10-29.md` | §7 拍板+实施坐标、§8 结果 |
| `docs/production-architecture-v3-interactive.html` | 拼接节点/concatSegs 信息卡同步 para |

全量 `go test ./...` 绿；比武与真片验证均为 env 门控测试，默认跳过不进 CI 日常路径。
