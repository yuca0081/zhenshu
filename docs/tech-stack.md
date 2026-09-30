# 技术选型（v2）

> 状态：2026-09-21 定稿（含前端第二轮决定，§8；选型已全部收敛）。
> v1 → v2 变化：数据库 SQLite → **PostgreSQL（跨服务器接入自有实例）**；新增 **MinIO 对象存储与存储分层**；
> 部署到**独立新服务器**（app/renderer 两容器），跨网络连接原服务器的 PostgreSQL/MinIO；明确 1.0 自用 → 商业化的分阶段路线与接缝。
> 原则不变：渲染引擎当黑盒 CLI（Node 只是运行时依赖，不写 TS 应用代码）；一切服务于"先把全链路打通出成品"；
> 不为未来规模提前买单，但留好商业化接缝（§6）。

## 1. 选型总览

| 层 | 选型 | 说明 |
|---|---|---|
| 前端框架 | Vue 3（组合式 API）+ Vite | 维持 v1 |
| 前端语言 | **TypeScript** | 2026-09-21 定：检视协议/element_registry/引用 refs[] 结构化面大，TS 收益明确；服务端仍纯 Go |
| UI 组件库 | Naive UI | 维持 v1 |
| 状态/路由 | Pinia + vue-router | 维持 v1 |
| 后端 | Go + Gin（单二进制：API + SSE + 编排 + 渲染编排） | 维持 v1 |
| ORM | GORM + pgx 驱动 | 驱动随数据库换 |
| 数据库 | **PostgreSQL（复用自有服务器实例：独立数据库 + 最小权限账号）** | 替换 SQLite |
| 大文件存储 | **MinIO（复用自有服务器实例：独立 bucket）+ 本地磁盘工作区分层（§3）** | 替换纯文件系统 |
| 任务队列 | **PG jobs 表 + `FOR UPDATE SKIP LOCKED` + Go worker 池** | 不引入 Redis |
| 实时通道 | SSE（一条：聊天流 + 进度 + 段完成事件）；用户动作走 POST | 不用 WebSocket |
| LLM | DeepSeek 主选，GLM/Qwen 备选；go-openai + function calling（ReAct 循环 Go 侧驱动）+ JSON Schema 校验；模型分层：对话/规划/合成物三档 | M0 先会话模式（§7，AI 本人担任、零 key），bake-off 时切 API 分档冻结 |
| TTS | 火山引擎豆包主选；CosyVoice 自托管备选 | 维持 v1 |
| 时间戳对齐 | funASR（Paraformer 字级）兜底 | 维持 v1 |
| 渲染引擎 | HyperFrames 黑盒 CLI，锁 patch 版本 | 维持 v1 |
| 渲染运行时 | Node 22 LTS + FFmpeg（renderer 容器内） | 维持 v1 |
| 认证 | 1.0：单用户口令/免鉴权 + 可空鉴权中间件；商业化再上注册/积分 | §6 |
| 日志 | slog 结构化 JSON + 每项目 run manifest | 维持 v1 |
| 部署 | 自有云服务器 + 两个新容器（app / renderer）；PG/MinIO 连既有实例 | §5 |

## 2. 复用原服务器服务的边界（跨服务器接入）

| 服务 | 决策 | 说明 |
|---|---|---|
| PostgreSQL | ✅ 接入 | 原服务器实例上新建独立数据库、专用账号只授这个库；防火墙仅放行新服务器 IP |
| MinIO | ✅ 接入 | 新建独立 bucket；对象 key 按 project 命名空间组织（§6）；端口同样只放行新服务器 IP |
| Redis | ⏸ 暂不接入 | 队列用 PG 单表即可且与业务数据同事务；将来限流/缓存/多实例广播时零成本接入 |
| Elasticsearch | ⏸ 暂不接入 | 单用户检索 PG 全文检索够用；将来模板库/视频库搜索再评估 |

跨服务器两条硬要求：① 原服务器防火墙对 PG/MinIO 端口**只放行新服务器 IP**；② 两台机器若同厂商同地域（内网/VPC 互通），成片上传 MinIO 无带宽顾虑；**跨厂商则先实测带宽**再定存储方案（大文件走这条线）。

## 3. 存储分层（钉死）

| 层 | 放什么 | 原因 |
|---|---|---|
| 本地磁盘（renderer/app 容器的工作区卷） | 每项目目录：合成物 HTML、管线中间产物、check 报告 | 小文件、高频改写；HyperFrames 的 lint/check/render 只认磁盘上的项目目录 |
| MinIO（档案库） | 成片 mp4、音频 wav、快照、风格模板包、字体包 | 大文件、写一次读多次；下载走预签名 URL，app 不代理流量 |
| PostgreSQL | 元数据 + 对象 key + 版本 + jobs | 只存引用，不存文件 |

流转：管线在工作区操作 → 产物上传 MinIO → DB 记 key 与版本。段重渲只动工作区内该段文件 → 新产物覆盖上传 → 该段版本 +1。

## 4. 任务队列与渲染并发

- jobs 表 + `SELECT ... FOR UPDATE SKIP LOCKED`，Go worker 池消费；两条并发线：**制作管线**（串行步骤：TTS → 合成 → 检查）与**段级渲染**（并行 N 段，N 可配置）。
- **渲染并发度**：渲染 = 无头 Chrome + FFmpeg（重 CPU/内存）。新服务器独占部署、无资源竞争，并发度按新服务器核数/内存配置（M0 实测单段占用后定，经验起点 `min(核数-1, 4)`）。
- M0 在开发机（Windows 直跑 CLI）验证管线；M1 才把 renderer 落到新服务器。

## 5. 部署形态

单容器落地（app 进程内直接 exec node/python/ffmpeg，渲染并发度按 §4）：
镜像内预装 Node 22 + 锁版 hyperframes + FFmpeg + Chromium + faster-whisper；
host 网络直连宿主机 PG(5432)/MinIO(9000)；`data/projects` 宿主卷跨发版保留。
构建、传输、迁移、起容器全部收敛在 `deploy/deploy.sh` 一键脚本，细节见 `docs/deploy.md`。

密钥与连接串：`.env` + godotenv，沿用 v1。

## 6. 商业化接缝（1.0 不做功能，只留结构）

1. 业务表全部带 `nullable user_id`（1.0 填同一个系统用户）。
2. API 一层可空鉴权中间件（1.0 放行；商业化只改这层）。
3. MinIO bucket / 对象 key 按项目（将来按用户）命名空间。
4. 单条成本记账从 M0 开始（LLM/TTS/渲染分项），商业化时"积分"直接从成本账长出来。

分阶段路线：**1.0 自用打通全链路出成品（当前目标）→ 1.x 加鉴权/多用户/配额积分/安全加固（商业化）**。

## 7. M0 第一周验证清单

> **LLM 作业双模式（2026-09-22 定）**：每个 LLM 步骤定义为文件契约（请求 packet → 产物文件 → schema 校验），双实现——
> ①**会话模式**：由开发会话中的 AI 直接担任（零 key 零成本；本仓验证片已验证此模式），M0 起步先用它跑通管线、出样片、打磨 prompt，副产品是 prompt 库雏形；
> ②**API 模式**：DeepSeek（key 已有）/ GLM / Qwen（待注册），管线稳定后切入做 bake-off 与自动化数据。
> TTS 同理：M0 先用离线 Kokoro（验证片已证明逐词时间戳可行），火山实测后置。
> 下表 1–3 项因此后置到管线跑通之后；4–7 项会话模式下即可完成。

| # | 验证项 | 通过标准 |
|---|---|---|
| 1 | 火山 TTS 字级时间戳（10 段旁白） | 时间戳与音频对齐无肉眼可辨偏差 |
| 2 | funASR 对齐精度（同 10 段） | 字级误差 < 50ms（兜底达标） |
| 3 | LLM 三家 bake-off（10 篇文章，分对话/规划/合成物三档） | script.json 一次通过率、合成物 HTML 生成质量与一次通过率、单条成本，按档锁定模型 |
| 4 | HyperFrames 渲染基线（开发机 draft + delivery 各 10 次） | 耗时/失败率记录在案 |
| 5 | 元素命名 lint 规则 | 规则可写、LLM 生成通过率无明显劣化 |
| 6 | 段间转场重叠区渲染拼接 | 推拉/溶解类转场拼接后无错位；不行则 M1 先只支持硬切 |
| 7 | 单段渲染资源基线（CPU/内存） | 得出新服务器渲染并发度配置 |

## 8. 前端决定（2026-09-21 第二轮，原待定项全部收敛）

1. **TypeScript：上**。检视协议、element_registry、引用卡片 refs[] 都是强结构化数据，TS 边界收益明确；服务端仍纯 Go，不写 TS 后端代码。
2. **时间轴：自研**（Vue 3 + TS，SVG 为主）。评估结论：Vue 生态没有成熟的三轨媒体时间轴组件；React 生态的 react-timeline-editor 是动画关键帧编辑器、形态不符；通用开源 NLE 时间轴基本不存在。自研范围可控——我们的时间轴是**只读导航型**（播放/seek/点选引用），不是剪辑型（无拖拽、无裁剪、无多轨编辑），规模小（<20 段）。三轨（视频/旁白/字幕）同源于分镜表：视频轨 = 段块，旁白轨 = 段块 + 波形，字幕轨 = 段块 + 文字缩略；波形用 TTS 时预生成的 peaks 数据画条形图，不引波形库。
3. **iframe 检视协议：自定义、零依赖**。消息约十种（seek / inspect 开关 / ready+元素注册表上报 / hover / select / region 解析…），M0 与 `element_registry[]` schema 一并定；前后端共享一份 TS 类型定义。

## 9. 负面清单

LangChain 系 / RabbitMQ / K8s / 微服务 / MongoDB / GraphQL / Node·TS 服务端代码（渲染运行时除外，服务端是 Go）/
任何 agent 编排框架（线性管线 + 重试用 Go 循环足够）/ Redis 与 ES 暂不接入（§2）。
