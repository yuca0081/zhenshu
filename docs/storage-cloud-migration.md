# 存储改造：产物正本上 MinIO，本地盘降级为工作区+缓存

2026-10-16 定稿并实施。动机：项目与参考库累积，服务器磁盘吃紧；本地目录从「事实源」
转为「工作区+缓存」，产物正本进对象存储，磁盘占用与项目数解耦。

前作：`docs/style-library-cloud-storage.md`（风格库 2026-10-15 已迁 PG+MinIO）。
本次把同一套「双后端 + 读穿缓存」模式推广到**项目产物**与**参考库**。

## 1. 核心判断

- **磁盘大头是媒体 blob**（终片/母版/段片 mp4、配音 wav、QA 截帧、参考原片 ≤200MB/个、
  npm-cache 垃圾），不是 JSON。所以正确形态是**货进 MinIO、PG 继续只记账**，而不是把
  文件塞进关系库。
- **渲染契约不能断**：npx hyperframes / ffmpeg / python（tts_align、assemble、font_tool、
  snap_samples）只认本地路径。制作期间本地工作区必须存在——因此本地目录保留，只在
  项目空闲后清理，需要时从 MinIO 回源。
- **小 JSON 工件不建 PG 表**：15+ 种工件建表是巨大 schema 改造，且它们只有 KB 级、
  无跨项目查询需求。保持文件契约，按原目录布局整包作为 blob 进 MinIO
  （`projects/<id>/<相对路径>`），恢复时原样落地。
- **Redis 不引入**（tech-stack.md 已有决策：队列走 PG SKIP LOCKED）。
- **`data/hf`（whisper 模型缓存）、`data/library/`（BGM/字体/方法库）不迁**：一次性
  全局数据，不是随项目增长的空间源；deploy.sh 已随包同步。

## 2. 对象布局

独立桶 **`zhenshu-data`**（`MINIO_DATA_BUCKET`，缺省即此名），**不开 versioning**：

```
zhenshu-data/
├── projects/<项目ID>/<项目内相对路径>     # 与磁盘布局 1:1 镜像
│   ├── project.json / manifest.jsonl / input/…
│   ├── renders/{main.mp4, main_voice.mp4, timeline.json, segs/segNN.mp4(.sum)}
│   ├── audio/segNN.wav / audio_meta.json
│   ├── style/{style_samples.json, sample-N.png, clips/clip-N.mp4, skin.json, …}
│   └── llm/comp-<seg>.json / compositions/registry/<seg>.json / …
└── references/<refID>/{video.<ext>, ref.json, features.json, frame-*.png, clip-*.mp4}
```

key 镜像磁盘布局，回源=按 key 落回原位，排查时本地一个地方仍能看全。

**为什么不开 versioning**：段片重渲、终片重混会反复覆写同一 key；风格库桶（`zhenshu`）
的 versioning「误删后悔药」在产物桶会退化成「每个历史版本整份留在同一块磁盘上」，
与省空间的初衷背道而驰。覆写即弃旧版。

## 3. 文件三分类（`internal/artifacts.Classify`）

| 类别 | 去留 | 内容 |
|---|---|---|
| **Keeper** | 上传 + 平时留本地 | 全部 JSON/txt/md、`manifest.jsonl`、`.sum`/`.check.stamp` 指纹、终片/母版/段片、配音 wav、样张/小样、`assets/img`、字体资产 |
| **Discard** | 不上传，完结清理删本地 | `reports/frameqa\|prereview\|replay\|director/` 截帧、`.hf-seg/`、`.hf-clip/`、`compositions/frames\|caps/`、`renders/preview/`、`llm/*.request.md`、`llm/prep-*.jsonl` |
| **Garbage** | 不上传，随时无条件删 | `npm-cache/`、`*.tmp*`/`.atomic-*` 残留 |

Discard 类都是**可确定性再生成**的：渲染壳由 assemble.py 从帧 HTML + 音频重建，帧 HTML
由 compositions 阶段从已上传的缓存契约（`llm/comp-<seg>.json`）重建，QA 截帧由重跑
snapshot 重建。放弃它们不影响任何回放/重做语义，只影响「回看历史审查截图」。

## 4. 安全不变量（铁律）

1. **删本地前必须确认远端正本在**：Keeper 大文件清理前逐一 `Stat`，远端不在就跳过并
   记日志。漏传的代价只是暂时多占磁盘，绝不丢数据。
2. **MinIO 未接线（`Available()=false`）清理整体不运行**：localStore 后端下本地即正本。
3. **本地作废文件必须同步删远端**：`InvalidateFrom`（prep 反馈路由）、agent/api 的
   换风格作废、删项目——否则清理过的老项目回源时陈旧工件会「复活」。
4. **fail-fast 语义沿袭**：配了 `MINIO_*` 连不上 → 拒启（正本在云，静默降级会让人
   以为数据都在）。

## 5. 写路径：检查点上传（write-behind）

上传是**最佳努力**：失败只记日志不打断制作（本地仍是工作区），缺口由后续检查点、
终态全量补传或 `-storage-sync` 补齐。检查点：

| 时机 | 位置 | 上传内容 |
|---|---|---|
| 项目创建 | api.createProject | `project.json`、`input/article.txt` |
| 每段渲染完成 | pipeline.stageRender | `renders/segs/<seg>.mp4` + `.sum`（最贵产物立即上正本） |
| 段拼接完成 | pipeline.stageRender | `renders/` 前缀兜底补传（母版/timeline/list.txt） |
| 成片完成 | pipeline.stageStitch | `renders/main.mp4`（StitchNow 换 BGM 同样触发） |
| project.json 变更 | pipeline.SetProjectField | `project.json` |
| TTS 完成 | produce.run | `audio/` 前缀 + `audio_meta.json` |
| 制作/重做终态（成功/失败/取消） | produce.finishJob | 全项目前缀兜底补传（含 manifest） |
| 准备链终了 | prep.Runner | 全项目前缀补传（工件/样张/manifest） |
| 参考解析完成 | styleref.Analyze | `references/<id>/` 全套 + ref.json（saveRef 内即时传） |

## 6. 读路径：本地优先，缺则回源

前端**零改动**（URL 永不入库的对偶：出口永远走 `/api` 路由）：

- `GET /projects/:id/video/main.mp4`、`/style/clips/:file`、`/references/:id/media/:file`：
  本地直读（热路径零网络开销）→ 缺则 `Artifacts.Open`（minio.Object 自带 Seek，
  Range/304 照常工作）→ 皆无 404。
- `GET /llm-video/:id/:name`：本地缺则 `Localize` 读穿落地再回源（视觉模型是整文件
  拉取，缓存正合适）。
- `GET /references/:id/frames`：本地目录扫不出 → 远端 `List` 同白名单过滤。

## 7. 回源（Restore）与透明恢复

**清理过的老项目重开/续跑/重做完全透明**：produce/prep 的启动 goroutine、
`pipeline.StitchNow` 开跑前执行 `artifacts.Restore`——远端前缀下本地缺失的 Keeper
文件拉回原位（Discard 不回，届时由 assemble/compositions 从缓存契约确定性重建）。
本地齐全的项目只有一次远端 List 的代价。

## 8. 清理器（GC）

`cmd/server/gc.go`：启动 5 分钟后首扫，此后每小时。条件＝项目不在制作/准备中
（busy 钩子查 produce/prep 运行表）**且**最近活动（manifest/project.json/ref.json
mtime）早于 `DISK_GC_DELAY`（缺省 24h；`0`=完结即清，负值=永不清理）。清理即
`artifacts.Prune`：Discard/Garbage 直接删、Keeper 大文件验远端后删、Keeper 小文件
永留。删完自底向上摘空目录。

维护子命令（离线跑）：

```
bin/server -storage-sync    # 存量回填（幂等）：全部项目+参考补传 MinIO
bin/server -storage-prune   # 立即清理（务必先停服）：与 GC 同规则的本地清理
```

## 9. 决策记录（防翻烧饼）

- **JSON 工件不进 PG**：无查询需求、体量 KB 级、建 15+ 张表的改造收益为零；PG 继续
  只存「案卷」（projects/chat_messages/render_jobs/users/styles）。
- **产物独立桶、不开 versioning**：见 §2；风格库桶的 versioning 是它的场景（低频覆写、
  误删后悔药），不是产物场景的。
- **产物层不设 PG 前置**：风格库要求 PG 已接是因为账本挂靠 PG；产物没有账（前缀即
  清单），只依赖 MinIO 存活。
- **不上传 Discard**：QA 截帧/渲染壳可再生成，占空间两到四成；代价仅「不能回看历史
  审查截图」。
- **Redis 不引入**：清理调度用进程内 ticker 足够（单实例部署）；多实例时再议。
- **本地盘仍是制作期事实源**：管线的重放/对账语义（manifest、指纹、阶段产物）一行
  未改——本改造只在「盘上有什么」这一层做文章，不动「流程怎么跑」。

## 10. 实施记录

- 新包 `server/internal/artifacts`：`Store` 接口（Put/Open/Stat/Delete/DeletePrefix/
  List/Localize/Fetch/Available）+ `localStore`（现状行为）/`minioStore` 双实现 +
  Classify 三分类 + SyncUp/Restore/Prune 批量操作 + 单测。
- 接线：config（`MINIO_DATA_BUCKET`/`DISK_GC_DELAY`）→ main 构造注入
  pipeline（包级 SetArtifacts）/produce.Runner/prep.Deps+SetCloudStore/styleref.Store/
  agent.Agent/api.Server；读路径 video/styleClip/referenceMedia/llmVideo/frames 切
  `serveArtifact`；作废点（prep InvalidateFrom、agent regenerateStyle+use_style、api
  applyStyle）同步删远端；删项目同步删前缀。
- 验证：`go build/vet ./...` 全绿、`go test ./...` 全绿（含 artifacts 单测）；不配
  `MINIO_ENDPOINT` 时全部接线退化为 no-op，行为与改造前逐字节一致（localStore 语义）。

## 11. 已知边界

- 制作中途断电：已过检查点的产物在 MinIO、未过的只在本盘——`-storage-sync` 可补；
  GC 的远端验证保证这种情况只浪费空间不丢数据。
- 换风格后旧样张数变少（3→2）：远端旧 `sample-2.png` 已随作废删除，不会复活。
- `llm/*.request.md` 会话请求日志随清理消失：排查线上问题请在清理窗口内取证，或
  `DISK_GC_DELAY=-1` 临时关清理。
- MinIO 与应用同机部署：上传/回源实际是同盘拷贝，快但**不提供容灾**；将来 MinIO
  迁独立盘/独立机器，key 化设计（URL 永不入库）保证零改造。

## 12. 项目一行账（2026-10-16 二期补充，用户拍板）

需求演进：文字类内容（文稿/分镜稿）进库便于检索的提议，讨论后定案为——

**「一个项目在数据库里就是一行账：可检索元数据 + MinIO 相对路径；内容本体
（文稿/分镜稿/媒体）不入库，单一归属 MinIO。」**

- 不做「PG 副本 + MinIO blob」双写文字内容：两处正文必然漂移，单一归属最稳。
- 检索取舍：按项目字段筛（主题/画幅/风格/状态）走 SQL；**跨项目全文搜文稿内容
  做不到**（内容在对象存储里），这是本次明确接受的代价。
- 实现（migration `0006_project_meta.sql` + `store.Open` 幂等 ALTER 同款自管口径）：
  - `projects` 表加列：`topic`、`storage_prefix`、`updated_at`（aspect/status/name 原有）。
    `style_id`/`target_seconds` 经讨论**不入账**：风格可不来自风格库（prep ② 自造时
    project.json 的 style_id 为空），时长用户拍板不要——两者仍留磁盘事实源。
  - `Store.UpdateProjectMeta(diskID, ProjectMeta)`：幂等覆盖写，磁盘 project.json
    是事实源，PG 是检索镜像。
  - 写入时机两处：① `createProject` 创建即写；② `listProjects` 账行自愈——`ensured`
    的值从占位升级为「盘上元数据指纹」，指纹变了才写库（写库频率=盘上变化率，
    高频轮询零 N+1）。SetProjectField/applyStyle 改了盘上值，下一次列表自动对齐。
- 「URL 永不入库」原则表述修正：**带域名/端点的 URL 永不入库；桶内相对路径 key
  允许入账**（storage_prefix 如 `projects/p20261016-115229/`）——换 MinIO 域名、
  换机器、换桶名都不影响这行账。
- `manuscripts`/`storyboards` 化石表（0001 建、Go 从未写）维持不动，留档；将来若
  真需要内容级检索，优先考虑在 agent 工具层做（查 MinIO key → 取文 → 内存过滤），
  仍不迁内容入库。
