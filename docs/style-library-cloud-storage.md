# 风格库云存储设计(PG + MinIO)

> 状态:已实施(2026-10-15 定稿并落地)。取代已删除的全量镜像方案(原 docs/cloud-storage-plan.md,git 历史可查)。

## 1. 背景与范围

服务器(infra 网络)已部署 PostgreSQL 17 与 MinIO,但风格库仍全部落在本地磁盘:

- 条目账本:`data/library/styles.json`(整读整写、原子替换、进程内互斥锁)
- 词表:`data/library/vocab.json`
- 媒体:`data/library/style_media/<id>/`(原片快照 + cover + 证据帧)

**本方案只管风格库**(风格条目 + 词表 + style_media)。项目工作区(data/projects)的存储另议;参考库(data/references)与风格库完全同构,是否同期待拍板(见 §10)。

重设计的动机:换机/重装不丢库、正本上云任意环境共享、行级写入消灭整文件写竞争、结构化查询(词表核对/筛选/未来语义检索)顺理成章。

## 2. 总纲

1. **账进 PG**:条目(`styles` 表)+ 词表(`style_vocab` 表)。账是事实源。
2. **货进 MinIO**:媒体帧与原片快照,共享桶 `zhenshu`,前缀 `style-media/<style_id>/`。
3. **URL 永不入库,只存键**:endpoint 因环境而变(开发机隧道 `127.0.0.1:9000`、服务器容器 `minio:9000`、未来公网子域名),那是配置;键才是数据。URL 一律运行时拼——给前端拼应用路由(`/api/library/:id/media/:file`,浏览器永远不直连 MinIO),给 Go 自己拼 `bucket + key` 取对象。

账/货分离的提交语义:**账是提交点,孤儿货无害**(§6)。

## 3. 数据模型(迁移 0005)

```sql
-- 0005_styles.sql:风格库 v3 上云——账进 PG,货在 MinIO(zhenshu/style-media/<style_id>/)
-- 取代磁盘事实源 data/library/styles.json;启动时一次性迁移(表空且旧文件存在 → 逐条导入并上传媒体)。
-- 注意:0001 的 style_packs 是 v2 遗留僵尸表,语义与 v3 条目不兼容,不复用、不清理,留作历史。

CREATE TABLE IF NOT EXISTS styles (
  id             TEXT PRIMARY KEY,               -- 条目 ID:vid-<refID>(参考视频解析)| sp<YYYYMMDD-HHMMSS>(成片反哺);与旧 styles.json 同构,迁移不换号
  name           TEXT NOT NULL,                  -- 风格名:人工可读,库页展示与选用入口
  tags_main      TEXT NOT NULL,                  -- 主标签,必填;须在 style_vocab 内(写入时 Go 侧核对,不上 DB 约束——词表会演进,约束改起来烦)
  tags_aux       JSONB NOT NULL DEFAULT '[]',    -- 辅标签字符串数组,≤2 个(Go 侧校验上限)。用 JSONB 不用 TEXT[]:pgx 把空切片编成 NULL(撞 NOT NULL)、gorm 不认裸 []string;数组内查询无需求
  subject        TEXT NOT NULL DEFAULT '',       -- 原片题材一句话:只供库页检索,永不进生成提示词(防题材中毒的红线,立此存照)
  origin_project TEXT NOT NULL DEFAULT '',       -- 溯源:"video:<refID>"=视频解析入库 | 项目 id=成片反哺;DraftFromProject 的幂等查重键之一
  published      BOOLEAN NOT NULL DEFAULT false, -- false=成片反哺草稿,待用户库页确认;true=已入库可选用;仅视频解析路径直接 true
  payload        JSONB NOT NULL,                 -- 整包嵌套数据:tag_evidence[](每个 tag 的证据帧)+ signatures[](0–6 条特色:kind 六类枚举、mechanism 必填、frames=MinIO 对象文件名);结构由 Go ValidateSignatures 校验,读方整读整写,故意不拆关系表
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),  -- 首建时间;同 ID 重解析覆盖时保留原值(列表排序稳定,对齐旧 upsert 语义)
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()   -- 最近写入;GORM 自动维护;前端媒体 URL 的 cache-buster(?v=)可用它
);

CREATE INDEX IF NOT EXISTS idx_styles_published ON styles(published);   -- 库页"已入库/草稿"两个视图
CREATE INDEX IF NOT EXISTS idx_styles_tags_main ON styles(tags_main);   -- 按主标签筛选(现在用不上,先备着)

-- 词表:主/辅标签共用;Go 侧 vocab.Has()/VocabList() 的数据源,从 data/library/vocab.json 迁入。
-- 注意词表不止一个词:id + 正式名 + 俗称 + 机制一句话 + 判别特征,后四样都要注入打标提示词。
CREATE TABLE IF NOT EXISTS style_vocab (
  id         TEXT PRIMARY KEY,               -- 词表 id(小写蛇形英文,如 cassette_futurism)
  name       TEXT NOT NULL,                  -- 正式名(卡带未来主义)
  aliases    JSONB NOT NULL DEFAULT '[]',    -- 中英俗称字符串数组(JSONB 理由同 tags_aux);新词查重(归一化精确 + LLM 语义)与检索用
  brief      TEXT NOT NULL DEFAULT '',       -- 一句机制描述:色域/质感/光/字体气质(注入打标提示词)
  tells      TEXT NOT NULL DEFAULT '',       -- 判别特征:帧里认什么(注入打标提示词)
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

**payload 为什么是 JSONB 不拆表**:tag_evidence 与 signatures 永远整包读(装进 StyleBrief、库页详情)、整包写(入库/确认)、整体校验(Go `ValidateSignatures`)。不存在"把 kind=camera 的特色单独查出来"的查询;真到要跨条目搜特色那天,JSONB 的 `payload->'signatures'` 路径查询 + GIN 索引路没堵死。

**校验职责分工**:数据合法性(枚举、≤6 条、mechanism 非空、标签在词表内、renumberSigs 编号)全部是 Go 写入侧的事,照用现有代码;PG 只负责存取与行级唯一。数据库不当校验器。

## 4. MinIO 对象布局

```
zhenshu/                                  ← 共享桶(同服务器其他项目共用一个 MinIO,桶名隔离)
└── style-media/<style_id>/
    ├── video.mp4                         ← 原片快照(确认入库时自参考视频目录收进)
    ├── cover.png                        ← 代表帧(随重入库覆盖)
    └── frame-*.png                      ← 全片自适应抽帧(≤36 张,被引用与否都收——回看用)
```

- key 拼装规则收敛在 Go 侧一个函数:`mediaKey(styleID, file) = "style-media/" + styleID + "/" + file`;payload 里的 frames 只存文件名,前缀规则改一处全局生效。
- 桶开启 **versioning**:Delete 连媒体前缀一起删,versioning 是唯一的后悔药。
- 每个条目的媒体是不可变快照;同视频重解析 = 整前缀重传覆盖。

## 5. 访问路径(新旧行为对照)

`stylelib.Library` 对外方法签名全部不变(List/Get/IngestVideoRef/DraftFromProject/Publish/Delete),api 层与 agent 层一行不改,改的都是内部实现:

| 操作 | 现在(文件) | 之后(PG + MinIO) |
|---|---|---|
| List / Get | 读 styles.json 全量 + 加载期校验 + 缺帧剪枝 | `SELECT … ORDER BY created_at DESC`;加载期校验与缺帧剪枝**退役**(账在库,行必然写入时合法) |
| 媒体服务 | `http.ServeContent(本地文件)` | 路由不变,内部换 MinIO `GetObject` 流式回传;**前端零改动** |
| IngestVideoRef(视频解析确认) | SnapshotMedia(硬链接快照)→ upsert styles.json | PutObject 媒体 → UPSERT 账行(§6) |
| DraftFromProject(成片反哺) | 扫全库文件查重 | 一条 SELECT(origin_project / 项目 style_id 查重)→ UPSERT 草稿 |
| Publish | 改内存标记 + 整文件重写 | `UPDATE styles SET published=true WHERE id=…` |
| Delete | 整文件重写 + `RemoveAll(MediaDir)` | `DELETE` 行(提交点)→ 删 MinIO 前缀,删失败留孤儿(无害) |
| 项目选用条目 | 直读 style_media 本地路径 | 统一走 mediaStore 访问器;需落进项目目录的(样张/简报证据帧)即一次普通下载,进项目后跟项目区存储走,与库解耦 |

## 6. 写入事务语义

跨 PG + MinIO 无分布式事务,靠**次序 + 容忍**保证正确:

- **先货后账,账是提交点**。路径 A(视频解析确认):先 MinIO PutObject 整包媒体,成功后 UPSERT 账行(payload 里的帧引用指向刚传上去的对象)。中途挂掉 → 没有账,只有孤儿对象,无害(此规模不配对账任务)。
- **路径 B(成片反哺)是纯账先行的特例**:本片没有原片帧,payload 的 frames 为空数组,入库后人工在库页回看补录——补录 = 重传媒体 + UPDATE payload,和路径 A 同一次序。
- 行插入后个别帧缺失 → 前端单帧 404,可容忍(对齐现状"部分成功不回滚,媒体是冗余不是事实源"的纪律)。

## 7. 数据迁移(一次性,启动时)

幂等迁移,无双事实源过渡期:

1. 服务启动,`styles` 表为空且 `data/library/styles.json` 存在 → 逐条:payload 原样序列化入库(帧引用是文件名,格式零改动)、`style_media/<id>/` 按前缀上传 MinIO、`vocab.json` → `style_vocab`;
2. 迁移完打日志(条目数/媒体对象数);styles.json 与 vocab.json **留作化石,不再读**;
3. 服务器侧(容器)与开发机跑同一份迁移逻辑,先到者迁、后到者空表跳过——两环境天然共享同一份云上正本。

## 8. 买到什么 / 付出什么

**买到**:
- 行级 UPSERT 消灭整文件写竞争——现状 styles.json 整读整写,两进程并发写即互相覆盖(代码里多处"读失败绝不当空表,防清库事故"的判断防的就是它);行级写入兄弟条目绝不被误伤;
- fail-fast 语义顺理成章:库接入 `store.Open` 既有纪律(PG 配了连不上拒启),与聊天案卷同待遇,不存在"库静默丢"的暗坑;
- 换机/重装零迁移:正本在云,任何环境起来就是同一张表同一个前缀,没有"同步"概念;
- 查询白送:按标签/发布状态筛选、分页;projects.style_id 与 styles 的关联查询成为可能;词表有了主键约束。

**付出**:
- 手改 styles.json 即生效的工作流终结(看数据走 psql,改数据走 API);
- 库操作依赖 PG/MinIO 可达(隧道断了写不了库,读也断);
- 一次性迁移代码 + mediaStore 访问层的开发量。

## 9. 决策记录(防翻烧饼)

| 被否决项 | 一句话理由 |
|---|---|
| 整库文件镜像(旧 cloud-storage-plan.md,已删) | 磁盘事实源 + 云镜像里数据库没有存在感;风格库是天然目录型资产,该直接住进 PG |
| payload 拆关系表(signatures/evidence 各一张表) | 整存整取的数据无单条查询需求,拆表只翻倍写入复杂度 |
| PG 里存媒体 URL | URL = endpoint + bucket + key,前两者是环境/部署配置,烧进数据后换环境要刷全表;存键,URL 运行时拼 |
| 复用 0001 的 style_packs 表 | v2 遗留僵尸表,语义与 v3 条目不兼容 |
| 浏览器直连 MinIO(预签名 URL) | 拓扑决定 MinIO 只在内网,浏览器只够得着 Go 服务;未来开 Caddy 子域名可平滑升级,存键方案不受影响 |

## 10. 范围外备忘

- **参考库(data/references)完全同构**(ref.json 账 + 视频/帧货),是否同期实施待拍板;
- 项目工作区(data/projects)存储另议——制作管线是文件生物,方案形状与库不同;
- 素材库 assets 镜像表(0003)随素材库方案于 2026-10-01 退役,不接线;
- 服务器侧 deploy.sh 需把 `MINIO_ENDPOINT` 改写为 `minio:9000`(infra 网络服务名,现有 sed 规则已有此改写)。

## 11. 实施记录(2026-10-15 落地)

- **依赖**:`github.com/minio/minio-go/v7` 入 `server/go.mod`。
- **配置**:`config.Load` 增读 `MINIO_ENDPOINT/ACCESS_KEY/SECRET_KEY/BUCKET(缺省 zhenshu)/SECURE`。落地口径:**仅在 PG 已接时生效**(账本在 PG,MinIO 才有挂靠处;PG 未接而 MinIO 已配 = 启动打警告并忽略,回退本地媒体);**配了连不上 = 拒启**(fail-fast 同 store.Open 语义——媒体正本在云,静默降级会让人以为库还在)。未配 = 本地目录媒体。
- **后端组合**:`Library = catalog + MediaStore` 两维各自选型——账本 PG(`pgCatalog`,GORM)或文件回退(`fileCatalog`,styles.json/vocab.json 原行为,demo 容器与无 PG 环境用);媒体 MinIO(`minioMedia`)或本地目录(`dirMedia`)。`stylelib.New` = 纯本地(测试/回退),`NewWith(dataDir, db, media)` = main 接线口。
- **建表**:启动时 `pgCatalog.ensureSchema` 幂等自建 styles/style_vocab(与 users 同款自管口径),`0005_styles.sql` 留作运维对账。
- **媒体服务**:`/api/library/:id/media/:file` 走 `Library.OpenMedia`(MinIO=对象流直回源,本地=文件);`/frames` 走 `Library.MediaNames`;老条目 references 回退逻辑收进 Library;文件名白名单 `ValidMediaName` 收在 stylelib 一处(api 参考媒体复用)。**白名单字符集含小数点**(抽帧名 frame-0.8.png 带时间戳),斜杠与「..」段拒收。
- **简报证据**:`BuildBrief` 走 `media.Localize`——MinIO 后端读穿缓存到 `data/library/style_media/<id>/`(与本地后端同址,排查一处看全),设计阶段多模态照旧吃本地路径。
- **化石导入**:`Library.Migrate()` 启动执行,PG 账本才生效。表空且化石存在 → 媒体先传(Stat 幂等跳过,老条目 references 回退)→ 词表+条目单事务导入(坏条目跳过喊出来)→ 化石改名 `.imported` 作成功标记;崩溃回滚后表仍空,下次重来;两台机器后到者表非空自动跳过,不会拿陈旧化石覆盖云端。
- **缺帧剪枝**:全部后端退役(设计原案只说 PG 模式退役,落地时拉齐——MinIO 逐帧 Stat 太贵,缺失由前端单帧 404 兜底);`TestListSkipsInvalidEntries` 固化此行为。
- **测试**:payload 往返/媒体白名单/dirMedia Open 形状单测;`TestPgCatalogIntegration`(`TEST_PG_DSN` 门控)与 `TestMinioMediaIntegration`(`TEST_MINIO_*` 门控)——SSH 隧道通了填环境变量即跑真库往返(upsert 幂等/保首建时间/词表别名/删除/对象流 Seek)。**两支集成测试已于 2026-10-15 对服务器真库跑通**(MinIO 公网 9000 直连,PG 走 SSH 隧道),并因此修掉三个单元测试测不出的问题。
- **真库暴露的三个修正**(设计稿与实现的偏差,以此为准):① `tags_aux`/`aliases` 用 **JSONB 不用 TEXT[]**——pgx 把空切片编成 NULL(撞 NOT NULL 约束),gorm schema 解析器又不认裸 `[]string`;别名/标签是短字符串数组,无数组内查询需求,JSONB 由 json 同机制承载(挂别名随之从 SQL `array_append` 改为调用方算好全量列表整体写)。② pgx 编码层修正只影响 PG 后端,文件后端无感。③ gorm 查询须显式 `.Table("styles"/"style_vocab")`——结构体名推导会得到 `style_rows` 这类错误表名。
- 前端与 API 契约零改动;`styleref.Store` 增加 `Lib` 字段(main 接线,打标查词表/落新词与库同实例)。

## 附录:样例数据(一条条目的完整形态)

**PG styles 一行**(列值):

| 列 | 值 |
|---|---|
| id | `vid-20261012-143000` |
| name | `卡带未来主义操作界面合集` |
| tags_main | `cassette_futurism` |
| tags_aux | `{retrofuturism, industrial}` |
| subject | `七八十年代科幻电影里的计算机操作界面混剪` |
| origin_project | `video:20261012-143000` |
| published | `true` |

**payload(JSONB)**:

```json
{
  "tag_evidence": [
    {
      "tag": "cassette_futurism",
      "note": "第2段：CRT 绿色荧光屏+琥珀单色显像管，面板带机械按键",
      "frames": ["frame-004.png", "frame-011.png"]
    },
    {
      "tag": "retrofuturism",
      "note": "全片网格地平线+矢量线框基调，70-80 年代未来想象",
      "frames": ["frame-017.png"]
    }
  ],
  "signatures": [
    {
      "id": "sig1",
      "kind": "palette",
      "name": "磷光绿琥珀双显色",
      "mechanism": "全场压到近乎单色：信息展示区用磷光绿(#33ff66 系)或琥珀(#ffb000 系)荧光色，其余面积一律深灰黑；两色从不同屏混用",
      "frames": ["frame-004.png", "frame-009.png"],
      "use_hint": "全局定调；荧光色只给信息区，正文标题不占用"
    },
    {
      "id": "sig2",
      "kind": "motif",
      "name": "网格地平线",
      "mechanism": "背景以透视网格线向消失点收束，作纵深锚点",
      "frames": ["frame-017.png"],
      "use_hint": "转场或大留白段落垫底，不与密集图文抢"
    },
    {
      "id": "sig3",
      "kind": "motion",
      "name": "扫描线刷新入场",
      "mechanism": "新元素以自上而下扫描线揭示：亮线扫过、下方内容显现，结束后余辉闪烁 2 帧",
      "frames": ["frame-011.png"],
      "use_hint": "每段首个进入的信息块；列表不逐条用"
    },
    {
      "id": "sig4",
      "kind": "transition",
      "name": "信号切换黑场",
      "mechanism": "段间模拟 CRT 信号中断：画面纵向撕裂收缩成一条水平亮线后黑场，黑场 ≤0.3s",
      "frames": ["frame-020.png"],
      "use_hint": "章节切换；同向连续叙事不用"
    }
  ]
}
```

**PG style_vocab 一行**:

```
('cassette_futurism', '卡带未来主义', '{cassette futurism, CRT-futurism}',
 '显像管屏幕+机械按键面板的计算机美学;荧光单色显示,塑料米黄/深灰机身',
 'CRT 扫描线、绿色或琥珀单色荧光屏、方形按键阵列')
```

**MinIO 对象树**:

```
zhenshu/style-media/vid-20261012-143000/
├── video.mp4        ← 原片快照
├── cover.png        ← 代表帧(frame-004 拷贝)
├── frame-002.png … frame-020.png(全片抽帧,未引用的也在——回看用)
```

纪律在数据上的体现:mechanism 全是机制、无题材物件(这套特色拿去做题材完全不同的片子也成立);frames 只是文件名,前缀 Go 侧拼;题材话术被隔离在 subject,永不进生成提示词。
