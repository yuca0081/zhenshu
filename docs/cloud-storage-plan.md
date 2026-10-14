# 云端存储接入方案：PG + MinIO（换机可复用）

> 背景：服务器（ubuntu@82.157.139.61，域名 yucaa.site）已部署共享 infra——PostgreSQL 17 + MinIO，
> 全部只听 `127.0.0.1`，开发机统一走 SSH 隧道访问（操作见《开发机连接服务器数据库 OSS 指南》）。
>
> 痛点：全部工作状态都在开发机 `data/` 目录（实测 892MB），换一台电脑项目、库、参考视频全部失联；
> 且当前开发机 `.env` 连 `PG_DSN` 都没配，聊天记录重启即丢（内存模式）。
>
> 目标：任何一台开发机 `git clone` + 填 `.env` + 开隧道 → 启动服务自动恢复全部项目/风格库/参考库/聊天案卷。

---

## 1. 现状盘点：数据都在哪

### 1.1 已接线 PG（改 .env 即生效，零代码）

`server/internal/store`（`store.go`）已实现双后端（PG / 内存），覆盖四张表：

| 表 | 内容 | 现状 |
|---|---|---|
| `projects` | 项目元数据镜像（按 `disk_id` 关联磁盘目录） | 代码就绪，**本机没配 DSN 在跑内存版** |
| `chat_messages` | 聊天案卷（追加-only 事件流） | 同上 |
| `render_jobs` | 制作任务记账 | 同上 |
| `users` | 用户账号 | 同上（启动时还会幂等自建本表） |

服务器侧 `zhenshu` 库已由 `deploy/infra/initdb/01-zhenshu.sh` 建好（库 + 角色），
但**表结构要确认**：`store.Open` 只自建 `users`，`projects/chat_messages/render_jobs`
依赖 `server/migrations/0001_init.sql` + `0002_disk_id.sql`（deploy.sh 部署时跑过；
开发机直连时需先验证，见 §5 Phase 0）。

### 1.2 建了没用的表（0001 里的僵尸表）

`0001_init.sql` 还规划了四张结构化表，**代码从未写入**：
`manuscripts`（文稿版本化）、`storyboards`（分镜版本化）、`style_packs`（风格模板）、
`method_notes`（经验卡）。`0003_assets.sql`（素材库镜像表）同样只有 DDL 没有代码。
`.env.example` 里的 `MINIO_*` 段（2026-09-28）与 0003 注释（"PG/MinIO 双写，磁盘仍为
渲染事实源……启动时 SyncRemote 双向对账"）都是**已预告但未兑现**的目标形态——
本方案就是把这句话做出来。

### 1.3 纯本地的文件（本方案的主战场）

以 `data/projects/p20261012-173637`（892MB 中的大头）为例：

| 路径 | 内容 | 实测大小 | 性质 | 去向 |
|---|---|---|---|---|
| `project.json` / `meta.json` | 项目元信息 | <1KB | 事实源（PG 只有镜像） | MinIO |
| `manuscripts/manuscript.json` | 文稿 | 小 | 事实源 | MinIO |
| `storyboards/storyboard.json` | 分镜 | 小 | 事实源 | MinIO |
| `style/`（skin、字体声明、样张） | 风格定义 | ~2.5MB | 事实源 | MinIO |
| `compositions/` + `.hf-seg/` + `.hf-clip/` | HyperFrames 段 HTML | ~4MB | 事实源 | MinIO |
| `llm/*.json` | LLM 产物快照 | ~125KB | 事实源 | MinIO |
| `audio/*.wav` | TTS 配音 | ~6MB | 事实源 | MinIO |
| `manifest.jsonl` | 事件流水 | ~360KB | 事实源 | MinIO |
| `renders/main.mp4` / `main_voice.mp4` | **成片** | 47MB×2 | 成品，重做费钱 | MinIO |
| `renders/timeline.json` | 时间线 | 1KB | 成品附属 | MinIO |
| `renders/segs/` | 段级渲染缓存 | 47MB | 可再生（重渲即得） | 排除（可选纳入） |
| `reports/`（prereview/replay 抽帧） | 审片证据 | ~86MB | 重跑烧 LLM 钱 | MinIO |
| `npm-cache/` | 渲染用 npm 缓存 | 204MB | 纯缓存 | **排除** |
| `assets/`（gsap/hf.js/katex） | 渲染运行时 | 1.6MB | 项目创建时从仓库 `ai/assets/` 复制 | **排除** |

库与参考（换机必失联的另一半）：

| 路径 | 内容 | 性质 |
|---|---|---|
| `data/library/styles.json` + `style_media/` | 风格模板库 | 事实源 |
| `data/library/methods/` | 方法库（经验卡/场景配方） | 事实源 |
| `data/library/media/audio/` | 素材库（0003 的镜像对象） | 事实源 |
| `data/library/vendor/fonts/` | 字体 vendoring | 准静态 |
| `data/references/<id>/` | 参考视频库（视频本体 + ref.json） | 事实源，上传费钱费时 |

另：whisper/HF 模型缓存（服务器部署挂 `data/hf`）属可再生（重下 ~1.5GB），不迁移。

### 1.4 一条决定架构走向的既有事实

**磁盘是事实源，DB 是镜像。** 这是代码里明写的口径：
`api/server.go:583`（listProjects "扫磁盘（事实源），确保 DB 有行"）、
`store.go:103`（render_jobs "磁盘 manifest 仍是事实源，这里是可查询镜像"）。
且制作管线重度依赖本地文件路径：ffmpeg、Node/HyperFrames 截图、python 字体工具
全部直接读写 `data/projects/<id>/` 下的相对路径。**所以方向不是"迁移进数据库"，
而是"磁盘保持事实源 + 云端做可恢复镜像"。**

---

## 2. 方案选型

| 方案 | 做法 | 优点 | 缺点 | 结论 |
|---|---|---|---|---|
| A. 外挂定时同步 | rclone/restic 定时把 `data/` 推到 MinIO | 零代码，当天生效 | 与应用生命周期脱钩；制作中截断出半成品副本；恢复粒度粗；另一台机器不会自动拉 | 否（可作过渡保险） |
| **B. 应用内镜像（推荐）** | 磁盘保持事实源；关键写点后增量上云；启动时 SyncRemote 对账拉取 | 与既有设计口径一致；知道何时是安全同步点；增量、可排除缓存；新机开箱即恢复 | 要写一个 mirror 包（~2-3 天） | **选 B** |
| C. 云为事实源 | 文稿/分镜进 PG、媒体进 MinIO，本地只做缓存 | 单一真源，天然多机并发 | 管线全部改造（读写路径、原子性、失败语义），现阶段收益不配风险 | 否（远期再议） |

---

## 3. 云端布局与同步协议（方案 B 展开）

### 3.1 PG（维持现状，扩一处）

- 现有四张镜像表照旧：配了 `PG_DSN` 就写，连不上 fail-fast 拒启（已有语义，不动）。
- Phase 3（可选）把僵尸表逐张接线，届时才有"结构化上云"，不阻塞主线。

### 3.2 MinIO：一个 bucket，三段前缀

在服务器 MinIO 上**新建独立 bucket `zhenshu`**（控制台走隧道 `localhost:9001`，
或 `mc mb`；不动 yuca 项目的 `yuca` 桶）。建议顺手开启 bucket versioning
（一钮）——误覆盖可回滚，个人规模容量代价可忽略。

```text
zhenshu/projects/<disk_id>/<相对路径>     ← data/projects/<id>/（按 §1.3 白名单）
zhenshu/lib/<相对路径>                    ← data/library/（styles.json、style_media、methods、media、vendor/fonts）
zhenshu/references/<ref_id>/<相对路径>    ← data/references/<id>/
```

### 3.3 同步协议

**上行（本机 → 云）四个触发点 + 一个兜底：**

| 触发点 | 位置 | 同步范围 |
|---|---|---|
| 项目创建 | `api/server.go` createProject | 该项目初始档案 |
| 文稿/分镜保存、风格确认 | PUT manuscript / PUT storyboard / style confirm | 项目档案（小 JSON，即时推） |
| 制作终态 | `producer.OnDone`（main.go 已有钩子位，成片/报告此时才齐） | 整项目（含 renders/reports，大头） |
| 项目删除 | `api/server.go` deleteProject | 删除远端 `zhenshu/projects/<disk_id>/` 前缀 |
| 兜底扫描 | 每 5 分钟 + 优雅停机前 flush | 按 (mtime,size) 增量推脏文件 |

**下行（云 → 本机）：启动时 `SyncRemote`，一次全量对账：**

1. LIST `zhenshu/projects/` 各前缀（个人规模，一次 V2 列举开销可忽略）；
2. 云端有本地无 → 整项目拉到 `data/projects/`（跳过排除清单）；
3. 本地有云端无 → 推上去；
4. 两边都有 → 按 (size, mtime) 新者胜，相同跳过；
5. 拉下的项目若 PG 无行，`EnsureProject` 自愈（机制已存在）。

**冲突原则**：磁盘为事实源（既有口径），同 mtime 冲突本地赢；真冲突靠 bucket
versioning 兜底。个人单机开发场景下冲突窗口极小，不为此做复杂合并。

**降级口径**（沿 0003 注释的既定设计）：MinIO 不可达只 warn 不挡业务——渲染永远
以本地盘为准，镜像挂了最多"这次没备份"；PG 维持现有 fail-fast 不变。

**前端零改动**：视频/样张/参考媒体仍走现有本地盘路由（`/projects/:id/video/main.mp4`
等），镜像层对浏览器完全透明。

### 3.4 依赖与配置

- 新增 Go 依赖：`github.com/minio/minio-go/v7`（唯一 S3 客户端，无重依赖）。
- `.env` 新增段（沿用 `.env.example` 已预留的键名）：

```properties
PG_DSN=host=127.0.0.1 port=5432 user=<见指南> password=<见指南> dbname=zhenshu sslmode=disable
MINIO_ENDPOINT=127.0.0.1:9000   # minio-go 端点不带 scheme，http/https 由 MINIO_SECURE 控制
MINIO_ACCESS_KEY=<见指南>
MINIO_SECRET_KEY=<见指南>
MINIO_BUCKET=zhenshu
MINIO_SECURE=0
SYNC_REMOTE=1                   # 启动时自动 SyncRemote；缺省 0=只推不拉（防新机误覆盖）
```

- 凭证只进本机 `.env`（已 gitignore），文档与代码一律占位符。

### 3.5 存储增长治理（数据分级 + 生命周期）

镜像上云后服务器磁盘随项目数线性增长。治理靠分级，**不靠搬进 PG**（大文件进库走
TOAST/大对象会表膨胀、拖慢备份与 vacuum；"账在 DB、货在对象存储"是标准分工，
方案 C 的改造成本换不来任何存储收益）：

| 级别 | 内容 | 云端策略 | 本地策略 |
|---|---|---|---|
| 成品 | `renders/main*.mp4`、`timeline.json` | 永久保留 | 保留 |
| 工作状态 | 文稿/分镜/皮肤/compositions/audio/manifest | 永久保留（体积小） | 保留 |
| 审片证据 | `reports/`（~86MB/项目） | MinIO ILM 按前缀过期（如 90 天） | 保留（本地为唯一副本，清了=接受可重跑） |
| 中间产物 | `renders/segs/`、`.hf-clip/` | 不镜像 | produce 终态后自动清理（重做需重渲，时间换空间） |
| 纯缓存 | `npm-cache/` | 不镜像（§1.3 已排除） | 定期清（~204MB/项目；可改为进程级共享缓存目录） |

- 容量测算：白名单口径每项目上云 ~0.2GB；一年 200 个项目 ≈ 40GB，逼近默认云盘上限——
  到期给 MinIO 数据卷（`/opt/docker/minio/data`）扩挂数据盘即可。
- 真到 TB 级：MinIO 后面换大容量盘/独立对象存储，S3 协议不变、应用零改动。
  "数据早晚都在服务器上、本地只是工作副本"就是这个方案的稳态，不存在二次迁移。

---

## 4. 分期落地


### Phase 0：零代码，半小时见效（建议今天就做）

1. 开隧道（Windows 若本机 5432 被占用 `-L 15432:...`，DSN 端口同步改）。
2. 验证服务器 `zhenshu` 库表结构：`psql ".../zhenshu" -c '\dt'`；
   缺表则按序跑 `server/migrations/0001_init.sql`、`0002_disk_id.sql`（幂等语句，安全）。
3. 开发机 `.env` 加 `PG_DSN` → 重启服务。

   **立刻获得**：聊天案卷、项目镜像、任务记账、用户账号全部云端化——重启/换机不再丢聊天。
   **仍未解决**：项目档案（文稿/成片/库/参考）还在本地 → Phase 1/2。

### Phase 1：mirror 包 + 项目档案镜像（核心工作量，~2-3 天）

1. `server/internal/mirror` 新包：MinIO 客户端封装（上传/下载/列举/删前缀）、
   白名单/排除清单（§1.3 表）、(mtime,size) 增量判定、`SyncRemote`。
2. 启动接线：`config.Load` 读 `MINIO_*`；未配 = 纯本地（现状）；配了但连不上 = warn 降级。
3. 触发点接线（§3.3 表）。
4. 启动日志打印镜像健康状态（对象数/上次同步时间），问题可一眼看出。
5. 测试：本地起 MinIO 容器跑双实例同步用例（推→清空→拉→逐文件比对）。

### Phase 2：库与参考上云 + assets 表接线（~1-2 天）

1. `data/library/`、`data/references/` 接入同一 mirror（前缀 `lib/`、`references/`）。
2. 兑现素材库原计划：catalog ↔ `assets` 表（PG）↔ MinIO 对象三方对账（0003 落地）。
3. 字体 vendoring 随库同步后，字体工具不再依赖"本机装过什么字体"（此前踩过坑，见字体相关排查记录）。

### Phase 3：结构化增强（可选，按需）

1. `manuscripts` / `storyboards`：每次保存追加版本行（表已在）→ 历史回溯/对比。
2. `method_notes` / `style_packs` 接线 → 方法库跨项目可查询。
3. 服务器 `yuca` 库已有 pgvector + zhparser 先例，`zhenshu` 库可按需装同款扩展，
   方法库/风格库做语义检索——放在所有数据都上云之后才有意义。

---

## 5. 换电脑操作手册（方案落地后的用户旅程）

```bash
git clone <repo> && cd zhenshu
cp .env.example .env        # 填 PG_DSN / MINIO_*（凭证见《开发机连接服务器数据库 OSS 指南》）
ssh -N -L 5432:127.0.0.1:5432 -L 9000:127.0.0.1:9000 ubuntu@82.157.139.61 &
bash build.sh && ./bin/server   # 启动即 SyncRemote：拉项目/库/参考 → 数分钟后与原机等价
```

要跑制作还需本机有 ffmpeg / Node / python 依赖（与现在新机初始化相同，不在本方案范围）。

---

## 6. 安全与运维注意

- **凭证不入 git**：`.env` 已 gitignore；本文档与代码注释只留占位符。
- **不开公网口**：5432/9000 维持仅回环 + 隧道，不在安全组放行（指南明令）。
- **生产库纪律**：migrations 全部幂等语句；`deleteProject` 会真删 DB 行与远端前缀——
  开 versioning 的 bucket 是唯一后悔药。
- **备份双向**：云端即"异地备份"，但服务器若单盘，定期 `pg_dump` + `mc mirror` 拉回本地，
  反向也留一份。
- **部署侧联动**：服务器 `deploy/compose.yml` 已在 infra 网络（PG=`postgres:5432`、
  MinIO=`minio:9000`，deploy.sh 会改写 `.env`）；Phase 1 落地后服务器与开发机天然共享
  同一份云档案（同一 bucket 前缀）。demo 演示镜像（数据烧进镜像）不受影响，暂不迁移。

## 7. 工作量小结

| 阶段 | 工作量 | 收益 |
|---|---|---|
| Phase 0 | 0.5h，零代码 | 聊天/账号/任务云端化，重启不丢 |
| Phase 1 | 2-3 天 | **项目档案可移植**（换机核心痛点解决） |
| Phase 2 | 1-2 天 | 风格库/方法库/参考库可移植 + 素材库原计划兑现 |
| Phase 3 | 按需 | 版本化历史、跨项目查询、语义检索 |
