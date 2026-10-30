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

## 远程库存储（2026-10-30 已实施，原「二期候选」第 3 条落地）

配置事实源从本机文件升级为远程库：`app_settings` 表（migrations/0009，通用 KV：
key/value JSONB/updated_at），行键 `llm_settings` 存 Settings 全量 JSON。
**多台工作机配同一 `PG_DSN` 即共享一套供应商台账与工位绑定**，API 与前端表单不变。

实现（server/internal/llm/settingskv.go）：

- **接线**：main.go 在 `store.Open` 成功且 `Kind()=="pg"` 后 `llm.AttachKV(st)`；
  内存模式（无 PG_DSN）不接，纯文件行为不变（开发/demo 零破坏）。
- **种子/回灌**（AttachKV 首次对接）：DB 有值 → 以库为准覆盖快照，并把库值回写
  本地文件（文件降级为缓存，断网重启仍有配置）；DB 无值而本地文件有 → 文件内容
  种子进库（老机配置零手工迁移）；都没有 → 各自为空。
- **同步**：30s 轮询拉新，归一化比较（PG jsonb 落库会重排键序去空白，原始字节
  比较会误报变更，两侧过 map 再紧凑序列化后比对）。每次 LLM 作业仍只读内存快照，
  解析链零 DB 开销不变；正在跑的作业拿旧值跑完（与文件模式同语义）。
- **保存**：PUT 校验 → 写库 → 换快照 → 写文件缓存。库写失败**不阻塞本机**：
  快照+文件照常生效，PUT 回 `{ok:true, warning:…}`，面板黄条提示「其他工作机
  暂不会同步」；纯文件模式保持旧语义（落盘失败报错不换快照）。
- **观察**：GET 加 `storage` 段（backend: db/file + updated_at 远程行最近写入
  时间），面板头部常显事实源提示。
- **安全口径不变**：key 在 PG 明文（与旧文件 0600 同级，PG 访问权在运维手里）；
  API 永不回明文；`.env` 保持每机兜底层（单机想覆盖共享值时用）。
- **并发语义**：双管理员并发保存 = last-write-wins（运维面单管理员常态，可接受）；
  refresh 与保存并发存在毫秒级窗口可能拿旧值盖新值，下个轮询周期自愈。

多机接入步骤：各工作机 `.env` 配同一 `PG_DSN`（表由 store.Open 幂等自建，无需
手工跑迁移）→ 启动即共享；`.env` 的 LLM_API_KEY 等仅作兜底，面板保存即全机生效。

## 台账形状 v3：一条数据 = 一个端点 + 模型列表（2026-10-30）

v2 的台账是「端点+单模型」一条，同一 URL 换模型就得重复填 URL/Key。v3 改为
**一条数据 = 一个 OpenAI 兼容端点（URL+Key）+ `models` 模型名列表**，工位/档位
绑定时从所选条目的列表里选具体模型（`Binding.Model`，空 = 列表第一个，列表也
空 = 沿用 .env 模型名）。

- 结构：`ProviderCfg{label, base_url, api_key, models[]}` + `Binding{provider,
  model?, effort?}`（settings.go）；`SaveSettings` 落盘 version=3。
- 兼容：v2 旧 JSON 的单 `model` 字段由 `normalizeSettings` 迁移进 `Models`
  （旧库行/旧缓存文件零手工迁移，读时迁移、下次保存即写 v3 形状）。
- 校验：绑定模型必须在所选供应商列表内（`checkBoundModel`）；normalize 防悬空
  （列表里删掉的模型名，绑定处自动掐掉、保 provider/effort 回落默认）。
- 选型链（provider.go resolveRole）：绑定模型 > 列表第一个 > .env（LLM_<ROLE>_MODEL
  → LLM_MODEL）> 内置默认；URL/Key 沿用「台账有值优先，空回落 .env」。
- 面板：「大模型」页签每条带模型列表编辑（加行/删行）；「工位图」绑定处供应商
  下拉旁多一个模型下拉（选项=所选条目列表，未选供应商时禁用）；删模型名自动
  摘引用它的绑定模型。
- 库内现行数据（2026-10-30 手工迁移）：一条 `glm` 条目 = 编码套餐端点 + Key +
  models [glm-5.3, glm-5.3-flash]，三档绑定 plan→glm-5.3、visual/dialogue→
  glm-5.3-flash、effort low，两模型探针实测连通。
