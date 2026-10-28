# 存储重构需求（2026-10-28 拍板冻结，待开工）

> 起因：换电脑看不到历史项目。梳理后发现账（PG）与货（MinIO）早已上云，
> 缺的只是「前台从云上读」这最后一环。本文整理需求、现状审计结论与待拍板的
> 开放问题，作为实施前的确认基线。
>
> 关联文档：docs/storage-cloud-migration.md（一二期改造，已实施）、
> docs/style-library-cloud-storage.md（风格库上云，已实施）。

## 1. 目标

1. **跨机器**：新电脑登录后能看到完整项目列表，点开可看详情，可继续制作。
2. **表结构清账**：PG 表与代码实际读写对齐——死表清退，留下的每张表都有明确读者。
3. 生产链路写入行为不变（本地落盘 → 正本上云的写后上传纪律不动）。

## 2. 现状（2026-10-28 全清重启之后）

### 2.1 存储三角色

| 角色 | 位置 | 职责 |
|---|---|---|
| 货（正本） | MinIO 两桶 | `zhenshu`（风格库媒体，开 versioning）/ `zhenshu-data`（项目产物+参考库，key 镜像磁盘布局） |
| 账 | PG | 项目元数据、聊天案卷、风格库条目/词表、账号 |
| 工作区 | 本地 data/ | 干活用缓存，可清理可重建（GC 每小时跑，Keeper 大文件删前必验远端正本） |

### 2.2 PG 表审计结论（11 → 5）

逐表核对代码读写方后：

**保留 5 张（各有活跃读者）：**

| 表 | 用途 | 读者 |
|---|---|---|
| projects | 项目账本（disk_id/name/status/topic/aspect/storage_prefix/user_id/时间） | 拟改造为列表事实源（见 §4） |
| chat_messages | AI 消息案卷（追加-only，SSE 游标=全局自增 id） | 对话回放、LLM 历史窗口 |
| users | 登录账号（bcrypt，固定账号模型） | 鉴权 |
| styles | 风格库条目（payload JSONB 整包，媒体在 zhenshu 桶） | 风格库页、prep 选用 |
| style_vocab | 标签词表（打标提示词数据源） | 风格解析打标 |

**清退 6 张（零代码读写，v1/v2 遗产）：**

| 表 | 死因 |
|---|---|
| manuscripts | v1 设想内容进 PG；v3 事实源在磁盘 manuscript.json，Store 接口从无此方法 |
| storyboards | 同上（磁盘 storyboard.json） |
| style_packs | v2 风格模板，语义与 styles 不兼容（0005 注释存照「不复用不清理」） |
| method_notes | 经验卡从未走库；方法库实际是 git 里 data/library/methods |
| assets | 0003 规划的素材库镜像，配套 assetlib 包从未落地——孤儿表 |
| render_jobs | 制作起跑/终态只写不读镜像；manifest.jsonl 才是事实源（Keeper 上云），统计真要做时从 manifest 反填 |

用户定调：业务数据核心就两块——**项目表 + AI 消息记录**；users 是门锁（基础设施），
styles/style_vocab 是风格库账本（第三块业务数据，跨项目共享）。

### 2.3 唯一的断链

`listProjects`（api/server.go）扫本地磁盘目录读各项目 project.json——换电脑目录为空，
列表即空。PG 的 projects 行目前只是「自愈镜像」（写到、没读过）。

## 3. 方案（已对齐方向，细节见开放问题）

比喻版：账本早已在云上，前台还在翻工作台上的登记本。改造=前台改读云账本 +
点单时仓库送货上门。

1. **列表从账本出**：listProjects 改读 PG projects 表；本地缺货的项目标「在云端」。
2. **打开项目即恢复**：
   - 小件（契约 JSON/文本，几 MB）立即 Restore 落地——项目页马上可看；
   - 大件（视频）不整箱搬：播放走已有流式回源；继续制作时 prep/produce 开跑前
     的 Restore（已在线上）兜底。
3. **写入纪律不变**：所有改造集中在读路径，生产链路零改动。

## 3.5 过程数据四层分工（讨论定稿，2026-10-28）

制作过程产生四类「流」，存储形态按一条总判据定：

> **要检索的进库（账），要跟着项目走的进对象存储（货）。**
> 检索 = 跨项目查、按条件筛、要游标增量；跟项目走 = 恢复时整箱搬、删除时一锅端。

| 流 | 形态 | 理由 |
|---|---|---|
| manifest.jsonl（机器事件流） | 文件（Keeper 上云） | 读者全是单项目尾部/全量读，无跨项目查询；89 个写入点须零依赖（demo 无 PG）；追加-only 审计日志是 JSONL 的本行 |
| llm/transcripts/*.jsonl（工位 agent 对话留痕） | 文件（Keeper 上云） | 完工快照、点开任务卡才读、按任务号点读——档案不是状态机 |
| chat_messages（主助手对话） | PG 表 | 功能件：LLM 记忆窗口每轮取尾部 100 条、SSE 游标靠全局自增 id。**拍板范围：只存主管 agent（主助手）的对话，含其工具调用与 token 消耗（meta）**；工位 agent 的留痕与 token 账留在文件（transcripts + manifest 的 llm.usage） |
| events.Hub（SSE 实时推送） | 内存 | 现场直播不存档，回看靠 manifest |

边界澄清（易混三问）：
- 一句话判别 agent 对话归属：**对话里有用户参与的进表（chat_messages，活状态：
  每轮取、断线续、模型记忆）；agent 自言自语的工作循环进文件（transcripts，
  完工档案：写一次、点开才看）**。即「聊天记录进库，工作日志跟货走」。
- manifest 与 chat_messages 不重复：用户的话与 agent 回复正文只在表里；60 多种工位事件
  只在文件里；终态一处双写是「机器版对账 vs 人话版通知」两种渲染，刻意为之。
- agent 工位对话不进库：搬进表的代价（项目完整性劈开、demo 断粮、冷数据泡大库）
  换回零收益。
- render_jobs 即此判据的反面案例：把「跟项目走的货」抄进库等未来读者 = 只写不读的死表。

重迁触发信号：真出现跨项目统计 / 跨任务搜对话需求时，以文件为事实源、按需反填索引表。

前端读取链（现状）：activity 接口尾部读 manifest 发现任务卡（agent.start/done 带
task 号与 token 账）→ 用户展开任务卡才调 /transcript?task= 懒加载该 agent 的对话
留痕。两个接口都是本地文件现算、零库查询；跨机器靠「打开项目先恢复小件」覆盖
（manifest + transcripts 都是几 KB 级 Keeper）。

## 4. 决策记录（2026-10-28 用户拍板）与遗留开放问题

**已拍板（方案冻结，暂不开工）：**

- **Q4 → 方案甲**：PG 行是项目元数据账本，实际产物正本在 MinIO；project.json
  降级为随货快照（全量字段、管线零依赖读取）。
- **Q1 → 随 Q4 自动解决**：列表所需字段进账本——projects 加列 mode / last_event /
  last_event_ts，写点接在状态转移处。
- **chat_messages 范围定稿**：只存主管 agent（主助手）的对话，含工具调用与 token
  消耗；工位 agent 对话留痕不进库（现状已是如此，此条为正式确认）。
- **Q5 → 串行换机**：同一项目同时只在一台机器开工，开制作时 PG advisory lock
  租约占坑；多端并发属未来另一次架构升级。
- **Q2 → 确认清退**：render_jobs 随六张死表一起 DROP（代码改动已在工作区备好，
  待开工窗口执行：编译测试 → 线上跑 0007）。
- **桶更名与用户域路径（同日追加拍板）**：
  - `zhenshu-data` → **`zhenshu-project`**（制作文件）；`zhenshu` → **`zhenshu-style`**
    （风格库，仍开 versioning）。`yuca` 桶不动（他项目红线）。
  - 项目对象路径 `projects/<ID>/` → **`<user_id>/<项目ID>/`**：用户域隔离，桶里
    一眼可归属；projects.storage_prefix 落该前缀（账本为准，代码不拼装归属）。
  - 用户段用 **user_id（UUID）不用 username**：用户名可改、UUID 稳定；可读名由
    PG join 提供。
  - `references/<refID>/` 保持 zhenshu-project 顶层：风格入库的工作文件，非项目
    产物，不进用户域。
  - **本地磁盘布局不变**（仍 data/projects/<ID>/）：key 不再逐字镜像磁盘，
    artifacts 层加「远端 key ↔ 本地路径」映射（Restore/Localize/SyncUp 走映射），
    管线零改动。「key 镜像磁盘」不变量改为「storage_prefix 记于账本 + 层内映射」。
  - 实施要点：全清后两旧桶为空——建新桶（zhenshu-style 开 versioning、
    zhenshu-project 不开）→ 删旧桶，零迁移；.env / .env.example / deploy.sh 同步
    改桶名（注意 .env.example 当前归另一会话改动，开工时协调）。

**仍开放：Q3 顺手项**（建议至少做第 ① 件——制作终态全量补传，否则跨机器恢复
会缺制作期小件）。

---

以下为各问题的原始讨论记录（保留供追溯）：

### Q1：列表缺的三个字段从哪来（已随 Q4 解决）

mode（music=音乐驱动 PV）、has_video、last_produce 目前只活在磁盘/manifest 里。
（复盘注：本问实为 Q4 的边界情形——Q4 拍板后本问自动有答案。）

- **方案 A：projects 加列**（mode TEXT、last_event TEXT、last_event_ts TIMESTAMPTZ），
  制作终态时顺手写账。列表=纯 PG 查询，最快；代价=账本要加厚、多一处写点。
- **方案 B：零 ALTER，列表直读 MinIO 各项目 project.json**（每个几 KB，可缓存），
  has_video/last_produce 仍按 manifest 事件回放或从 project.json 补字段。
  代价=列表从 1 次 SQL 变 N 次对象读（缓存可摊销）。
  （倾向 B：磁盘 project.json 事实源地位不动，账本不加厚。）

### Q2：render_jobs 清退的执行确认（已拍板：清退）

代码删减已在本地工作区完成（store/produce/segpipe/main/test 共 6 文件 + 0007 迁移
SQL 已写），**未提交、未动线上库**。确认后执行：编译测试 → 线上跑 0007 DROP。

### Q3：顺手项（可不做）

- **制作终态补一次全项目 SyncUp**（2026-10-28 梳理上传时机时发现：prep 链终了有
  全量补传、produce 链没有——compositions/registry、reports/audit、reviews/journal
  等制作期小件无专属检查点，须等下次 prep 或手动 -storage-sync 才上云；跨机器
  恢复会缺。本地不丢：GC 永不删小件。修法=终态处一行 SyncUp）；
- reports/seam、reports/audit 从 Keeper 改 Discard（审片截图类，可再生，现按正本上云属误伤）；
- main.mp4 / main_voice.mp4 双终片版本策略是否收敛为单份。

附：现有上传时机全景（检查点混合制）——段片/母版/project.json 产生即传
（putArtifact 单文件）；audio、renders 阶段整前缀补传；prep 链终了全量补传；
全部 best-effort，失败不挡制作、后续检查点补传。

### Q4：项目元数据的事实源归属（已拍板：方案甲）

讨论轨迹复盘：render_jobs 去留、manifest/chat/transcripts「进库吗」、Q1 字段放哪——
每一问都是同一问题的化身：项目档案以谁为准？现状是未裁决的双重事实源：
磁盘 project.json 自称事实源，PG 行是自愈镜像（指纹同步机制本身就是「两套真相
可能不一致」的补丁），而 listProjects 读磁盘、改造又想让 PG 出列表。

- **甲：PG 行是账本（一等公民）**——可检索字段以 PG 为准（写点在状态转移处），
  project.json 降级为随货快照（全量字段、管线零依赖读取）。与 chat/users/styles
  的 PG 为准家族一致；自愈指纹机制可退役。（倾向甲。）
- **乙：project.json 是唯一事实源**——PG 只是列表索引，列表直读对象存储。零
  ALTER，但「数据库当对象存储的缓存」定位别扭。

### Q5：跨机器的目标形态（已拍板：串行换机 + 租约闸）

现方案只解决串行换机。系统内 manifest 写入互斥/制作 running 标记/ensured 指纹
全是进程内状态，两台机器同时对同一项目开工会撕裂 manifest、互踩产物，且无任何
跨机锁。需拍板：

- **串行为主（推荐）**：开制作时加 PG advisory lock 租约占坑，或至少文档化
  「同一项目同时只在一台机器开工」铁律；
- 多端并发：属另一次架构升级，不在本方案范围。

## 5. 实施清单（2026-10-28 开工，代码侧完成 ✅，线上操作待确认）

1. [x] render_jobs 清退（Q2 已拍板）：工作区改动编译 + 测试通过；**线上 0007 已执行**
   （2026-10-28 确认后跑，DROP 后验证表清单剩 5 张：projects/chat_messages/users/styles/style_vocab）
2. [x] 桶更名与用户域路径：config 默认桶名 zhenshu-project / zhenshu-style；
   artifacts 层 key↔本地路径映射（前缀表播种自 projects.storage_prefix，
   LocalPrefix 批量入口 + toKeyRel/toLocalRel 层内换算）；.env / .env.example 已同步。
   **新两桶已在探针启动时自动创建**（zhenshu-style 开 versioning ✓、
   zhenshu-project 不开 ✓，已实测确认）；**旧空桶 zhenshu / zhenshu-data 已删**
   （2026-10-28 确认后执行，删前逐桶再验空；yuca 未动）
3. [x] 账本加列（Q4/Q1）：0008 迁移（store.Open 幂等 ALTER 已随探针启动生效）+
   写点接线——createProject 落 mode/storage_prefix（用户域），NotifyResult 终态落
   last_event/last_event_ts，UpdateStatus 家族同步 updated_at
4. [x] listProjects 改造：PG projects 出列表（归属过滤走 user_id 列、mode/
   last_produce 直出账本、`local` 字段标注「在云端」）；磁盘扫描与 ensured 指纹
   退役（内存存储保留磁盘扫描只为 demo/无库环境保命）；IsRunning/hasFinalVideo
   降为展示补充。前端列表「在云端」标记已带
5. [x] 打开项目恢复钩子：projectGuard 云端项目首触 → RestoreSmall（单件 ≤16MB、
   整次 ≤128MB，进程内一次）→ 重验归属放行；activity/transcript 小件恢复后自然可用
6. [x] 制作租约闸（Q5）：store.TryProductionLease（PG advisory lock 会话级占坑，
   进程崩溃自动释放）接 produce.Start/Rework；第二台机器同开直接拒绝并提示
7. [x] 播放/预览路径确认走流式回源（video/styleClip/references 全部
   serveArtifact：本地 miss → Artifacts.Open 流式，Range/304 照常）
8. [~] 换电脑验收场景：**待线上 0007 执行 + 服务器发版后端到端走查**（本机侧
   PG 列表/建项/用户域 key/删除已用探针实测通过；MinIO 实测 key =
   `<user_uuid>/<项目ID>/project.json`）

Q3 顺手项：第 ① 件（制作终态全量补传）已在工作区先行完成（produce.finishJob
syncUp 全量）；第 ②③ 件未动。

**剩余线上操作：**
- 服务器 /opt/zhenshu/.env 若钉了 MINIO_BUCKET=zhenshu，同步改 zhenshu-style（并确认
  MINIO_DATA_BUCKET 未钉旧名）；随下次 deploy.sh 发版生效
- 换电脑端到端验收场景走查（§6）——需先发版；本机侧 PG 列表/建项/用户域 key/
  删除已用探针实测通过，MinIO 实测 key = `<user_uuid>/<项目ID>/project.json`

## 6. 验收场景（换电脑）

新电脑 clone + 配 .env → 登录 → 项目列表秒出（账本）→ 点开老项目：几秒内契约文件
恢复完，过程页/判卷/设计系统可看 → 点播放：视频流式回源可看 → 点继续制作：
prep 开跑前自动整箱 Restore，正常出片。
