# 模型设置面板（2026-10-29）

顶栏「设置」按钮（管理员可见）进入独立设置页（`#/settings`，不占主导航页签位），
页内「大模型 / 工位图」双页签。工位的模型与 URL 不再只靠 .env——面板写入
`data/llm-config.json`，保存即热生效，无需重启。

## 两层结构

```
data/llm-config.json
├── providers    大模型台账：id/名称/URL/Key/模型名（N 个端点）
├── role_default 三档兜底绑定：dialogue/plan/visual → 供应商
└── stations     工位覆盖绑定：单个工位 → 供应商（+可选 effort）
```

解析链（`llm.resolveRole`，FromEnv 与视图共用）：**工位绑定 → 档位兜底 → .env →
内置默认**。供应商字段留空 = 沿用 .env 对应值（可只配模型名复用 .env 的 URL/Key，
零配置加第二档模型）。供应商与思考档是两条独立通道：工位可以只调 effort 不换模型
（判卷降档 low 的典型用法）。文件全空时行为与纯 .env 完全一致，零迁移。

## 工位清单（15 个，server/internal/llm/settings.go StationRegistry）

| 分组 | 工位 | 兜底档 |
|---|---|---|
| 准备 | 文稿 / 风格（文字）/ 风格（看图）/ 分镜 / 导演定调 | plan / plan / visual / plan / plan |
| 制作 | 场景生成 / 帧检查 / 审核 / 配乐 / 样张片段 | plan / visual / visual / dialogue / plan |
| 风格库 | 画材包蒸馏 / 视频解析 / 打标 | visual / visual / plan |
| 通用 | 项目对话 / 看图转述 | dialogue / visual |

蒸馏判卷官（distillJudgeProv）走「审核」工位——判卷模型面板一处换；
`DISTILL_JUDGE_MODEL` 仍是工位之上的后置覆盖（配了就赢）。

## 关键实现事实

- **热生效**：全部约 20 个调用点都是作业现场 `llm.FromEnv(工位常量)`，不缓存
  Provider——PUT 落盘换 `atomic.Pointer` 快照，下一次作业即用新值。正在跑的
  作业拿旧值跑完（可接受）。
- **不串缓存**：磁盘缓存键含模型名（diskcache.cacheKey），换模型天然隔离。
- **prep 预检**（prep.Available）改查 prep 链四个工位而非规划档：工位各自绑定后，
  规划档没 key 不再误杀 prep。
- **Key 只进不出**：GET 只回 `has_key` + 尾 4 位；PUT 空 key = 保持已存，
  `clear_api_key=true` 显式清除；文件权限 0600（含 key）。
- **校验与宽容两条路**：PUT（SaveSettings）先严格校验（悬空供应商引用/坏 effort/
  坏 URL/坏工位键 → 400）再宽容清洗；启动读盘（LoadSettings）纯宽容——手改文件
  写错不拒启，悬空绑定只掐供应商引用、保留 effort。
- **探针**：POST /api/llm-settings/test 按 provider id 或表单值直测（表单值优先，
  空字段按已存台账→.env 兜底），8-token 真调用，45s 超时。GLM 端点 8 token 可能
  全花在思考上——回空串按「连通正常」展示。
- **权限**：三端点全挂 requireAdmin（开放模式旁路照旧）；⚙ 按钮仅
  `user.role === 'admin'` 显示。

## 端点

- `GET /api/llm-settings` — 台账（掩码）+ 绑定 + 工位元数据 + 每工位/档生效值
  （source: station/role/env/default）+ .env 兜底现状
- `PUT /api/llm-settings` — 整体保存（providers 数组 + role_default + stations）
- `POST /api/llm-settings/test` — `{provider?, base_url?, api_key?, model?}`

## 仍留 .env 的钮（不进面板）

LLM_API_KEY（兜底 Key）、LLM_EFFORT（兜底档）、LLM_MAX_CONCURRENCY、
LLM_CALL_TIMEOUT、DISTILL_JUDGE_MODEL（蒸馏判卷后置覆盖）——运维钮不是工位钮。

## 验证记录（2026-10-29）

- 单测：llm 包 7 个用例（env 层/工位覆盖/effort-only/回读/校验拒绝/normalize/
  生效值标注）；全仓 go test 绿；web vue-tsc + vite build 绿。
- 冒烟（隔离 DATA_DIR + 开放模式 + 内存存储，127.0.0.1:18098）：GET 空设置 →
  PUT 双供应商 + 档位绑定 + 工位覆盖 + effort-only → GET 生效值全链正确
  （judge=station 层、bgm_pick=effort-only、prep_script=role 层）→ 文件落盘
  格式核对 → 探针 glm-main 真调用 2.4s ok、未知 id 404。

## 二期候选（未做）

- 按页签粒度（文稿/分镜各配各的）：新增 role 枚举即可，管线不动；
- 多供应商分闸（llmGate 现为全局账户闸，双平台共挤偏保守但安全）；
- 多实例部署配置共享（届时文件搬 PG 表，API 不变）。
