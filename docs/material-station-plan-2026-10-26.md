# 素材工位扩展方案：分镜阶段预采集 + 多图源（material station plan）

> 2026-10-26 讨论收敛成文。状态：**未实施，待拍板**。
> 起因：用户提出——用平台做视频需要 X（Twitter）截图或某网站的图片，能不能在分镜阶段就完成素材收集。
> 事实源：server/internal/produce/materials.go（素材工位现状）、server/internal/produce/scenegen.go（两段式场景生成）、docs/production-stage-design.md。
> 实施拍板后须同步更新 docs/production-architecture-v3-interactive.html。

## 0. 一句话

素材申请从「场景生成时按段现采」前移到「分镜定稿时全片预采」，图源从「仅 Met 公版」扩到「用户上传 / 直链 URL / 网页截图」三档——**申请单制与登记簿白名单两根支柱原样不动**，生产端几乎零改动。

## 1. 现状与痛点

### 1.1 现在素材怎么进来的

链路：文稿 → **分镜**（`draft_storyboard`）→ 风格样张（用户确认门）→ 设计系统 → 段级制作。素材申请单挂在最末的场景生成第一段式（scenegen.go:339）：

- 模型判断本段要不要「真实世界的实物图像」，交 `image_requests`（英文检索词 + 用途一句话），每段 ≤2 条、全片 ≤24 条；
- 平台 `fulfillRequests` 按申请单去 **Met 开放藏品 API** 检索，只取 `IsPublicDomain` 且有主图的条目，下载原图（≤64MB）→ ffmpeg 压图（宽 ≤1600，JPEG q4）→ 落 `assets/img/`；
- 每张图在 `assets/materials.json` 登记留痕（来源/标题/作者/年代/credit/原图 URL）；
- 第二段式：真图 base64 附给模型，出带图终稿；场景 HTML 的 `assets/img/` 引用过**白名单校验**（清单外直接打回）。

两条铁律：**模型只有申请权，执行永远由平台代码代劳**；**素材是加分项**——检索落空/下载失败一律按无素材继续，不挡出片。

### 1.2 一个关键既有事实（本方案的地基）

场景生成**天然支持素材先行**：`materialsForSeg` 命中登记簿（本段已有素材）时直接带图**单段**生成，并关闭申请单入口（scenegen.go:656 `allowAsk` 注释）。也就是说——只要有人在场景生成之前把登记簿填好，生产端自动切换到单段省一轮 LLM，**一行生产代码都不用改**。

### 1.3 三个痛点

1. **等待窗口浪费**：风格样张要用户确认，这段墙钟时间里分镜已定每段画什么，却什么素材都没采。
2. **两段式税**：带素材段在场景生成要两轮 LLM（交单→等下载→出终稿），素材前置后变一轮。
3. **图源单一**：Met 只有艺术公版藏品。做新闻/时事/产品/科普类片子——X 帖子截图、网页配图——现在**没得用**，这是图源缺口不是质量问题。

## 2. 目标与非目标

**目标**：①素材申请时点前移到分镜；②图源扩到用户上传/URL/网页截图；③混合授权口径进登记簿，保住「公版+留痕」的叙事不破功。

**非目标（明确不做）**：

- **X 自动抓取**——登录墙（未登录看不到内容）、官方 API 收费、无头浏览器带登录态既脆弱（改版即断）又有 ToS 风险。v1 一律用户自己截图上传顶住。
- 搜索引擎图库式任意抓图（授权不可辨）。
- 视频/音频素材（音频已本地合成零版权问题，视频素材另案）。

## 3. 总体设计：三刀递进，每刀独立可用

```
第一刀（架构红利）      第二刀（图源扩展）           第三刀（截图）
分镜预采集·仍只有Met  →  用户上传 + URL直抓(SSRF硬门) →  网页截图(复用Chromium)
契约+采集步骤+提示词      上传端点+fetcher接口化        capture_page工具
生产端零改动              登记簿加License维度           X仍走用户上传
```

### 3.1 第一刀：分镜预采集（图源仍 Met）

**契约**（contract/types.go）：`Segment` 加可选字段，照 `Brief`/`Beats` 的「老项目零感知」先例：

```go
// MaterialAsk 分镜阶段的真图素材申请单条目（可选）：分镜定每段画什么时一并提单，
// 平台在进入制作前预采集入库；场景生成照旧可补单（兜底路径）。
MaterialAsk []MaterialAsk `json:"material_ask,omitempty"`

type MaterialAsk struct {
    Query string `json:"query"` // Met 检索词（具体英文实物名词）
    Why   string `json:"why"`   // 画面哪里要用（一句话）
}
```

**采集时机**：分镜保存成功后即后台触发采集（draft_storyboard 落盘后起 goroutine）。分镜改稿重触发，**成本≈0**——登记簿 `findByQuery` 同词幂等，改稿多提的词才真下载。

**生产端接线：零改动**。预采条目写进 `materials.json`（`Seg` = 分镜段 id，与生产段 id 同一命名空间 seg01…），场景生成 `materialsForSeg` 自动命中→该段带图单段生成。`image_requests` 兜底路径保留：模型看着画面稿仍可补单，走现有 `fulfillRequests`（同词复用天然去重）。

**改动面**：contract 字段 + draft_storyboard 提示词加申请单说明 + 分镜保存后的采集步骤。预计一天级。

**收益**：①下载/压图与样张确认等待并行；②带素材段每段省一轮 LLM；③跨段同词在规划时合并（现状靠生产时撞登记簿）。

**代价（要认账）**：分镜阶段只有 VisualBrief 文本，提单比「看着第一段画面稿」泛化，会有一部分采了不用的图。兜底：全片 24 上限 + Met 下载免费 + 不用不占场景。

### 3.2 第二刀：图源扩展——用户上传 + 直链 URL

**登记簿口径**：`Material.Source` 从 `"met"` 扩为 `"met" | "user" | "url" | "shot"`，新增 `License` 字段：

| License | 含义 | credit 行策略 |
|---|---|---|
| `public-domain` | 公版（Met 现状，老条目缺省值） | 显示馆藏 credit 行（现状） |
| `quoted-reference` | 引用性使用（网页图/截图，报道/评论语境） | 显示来源 URL |
| `user-provided` | 用户自证有权使用 | 不显示 |

schema 演进向后兼容：老 materials.json 无 license 字段读入时缺省 `public-domain`。

**用户上传**：照 `uploadReference`（api/server.go:1883）同款模式加 `POST /api/projects/:id/materials`，落 `assets/img/` + 写登记簿。**授权责任放在提交动作**——上传即用户自证有权使用，平台记账不背书。

**URL 直抓**：新端点 / agent 工具 `submit_material_url`，后端 fetch 复用 64MB 上限/压图/登记管线。**SSRF 防护是硬门不是细节**——服务器在 Docker 里连着 infra 网络（PG/MinIO 都在私网），必须：

- 拦私网/环回/link-local 全段（10/172.16/192.168/127/169.254——含云元数据 169.254.169.254）；
- **DNS 解析后校验 IP**，不是只看域名字符串（域名可指向内网）;
- 禁自动跟随重定向，或逐跳重新校验；
- 沿用 64MB 上限 + 60s 超时 + 压图拒绝垃圾。

**白名单校验零改动**：`materialWhitelistCheck` 只认登记簿，不关心图从哪来。

### 3.3 第三刀：网页截图（复用渲染壳 Chromium）

新工具 `capture_page`：headless Chromium `--headless --screenshot` 直截公开页 viewport（**不走 hyperframes 渲染链**，平台进程树里本来就有 Chromium，直接调更轻），截得图按 `shot` + `quoted-reference` 入登记簿，Credit 记来源页 URL。

只对公开可访问页面生效——**X 帖子截图类需求仍走用户上传**（登录墙，见非目标）。典型用途：新闻报道配图页、文档页、数据看板等无需登录的页面。

## 4. 关键决策表

| # | 决策 | 备选 | 理由 |
|---|---|---|---|
| D1 | 预采挂在**分镜**而非设计系统 | 设计阶段提单 | 分镜已有每段 VisualBrief（内容层）；设计系统是视觉风格层不管内容；时点更早 |
| D2 | **保留**场景生成申请单兜底 | 预采唯一路径 | 素材是加分项哲学不动；分镜提单泛、画面稿提单准，双路径共用登记簿互不干扰 |
| D3 | X 自动抓取不做 | 无头浏览器带登录态 | 登录墙 + API 收费 + ToS + 改版脆弱；用户截图上传零风险零维护 |
| D4 | License 进登记簿、credit 分档展示 | 全部按公版处理 | 平台叙事从「纯公版」变「混合授权」必须留痕，否则「公版+留痕」破功 |
| D5 | SSRF 校验解析后 IP | 只校验 URL 字符串 | 域名可解析到内网；Docker 内网有真实 infra 服务可打 |
| D6 | 分镜保存即触发预采 | start_production 后统一采 | 等待窗口最大化利用；登记簿幂等使改稿重触发成本≈0 |

## 5. 改动面清单

| 文件 | 改动 |
|---|---|
| server/internal/contract/types.go | `Segment.MaterialAsk` + `MaterialAsk` 结构体 |
| server/internal/produce/materials.go | fetcher 接口化（met/url/shot 共用登记与压图管线）；`Material.License` 字段 |
| server/internal/agent/* | draft_storyboard 提示词加申请单段；新工具 submit_material_url / capture_page（白名单注册） |
| server/internal/api/server.go | 上传端点 POST /api/projects/:id/materials；URL 抓取端点 |
| server/internal/contract（分镜稿工件） | 分镜 markdown/JSON 工件加素材申请节 |
| 前端 ActivityPane/PrepPane | 预采进度事件展示；素材上传入口 |

## 6. 风险与边界

- **SSRF（最大安全面）**：见 §3.2 四条硬门。上线前必须过一次「内网 URL 必拒」的专项测试（含 DNS 重绑定式的解析后校验用例）。
- **版权姿态变化**：从「纯公版」到「混合授权」是产品叙事的变化，不只是技术字段。quoted-reference 档在片尾 credit 按来源 URL 留痕，但「报道性使用」的法律边界归用户自负（提交动作即自证），平台文档要写明。
- **冗余采图**：分镜提单泛化 → 采了不用。上限 24 + 同词复用 + Met 免费，成本可忽略；URL/上传档不设自动预采（用户手动给，天然不冗余）。
- **改稿一致性**：分镜改稿后 `MaterialAsk` 变化，预采重触发是增量的（同词幂等）；被删掉的申请不回收已采图（登记簿条目无主后不影响白名单——白名单按登记簿全量放行，与「素材是加分项」一致）。

## 7. 落地顺序与验收

| 刀 | 交付 | 验收 |
|---|---|---|
| 1 | 契约字段 + 保存后预采 + 提示词 | 分镜保存后 materials.json 出现条目；带素材段单段出稿（两段式消失）；场景生成补单兜底仍可用；老项目无 material_ask 零感知 |
| 2 | 上传端点 + URL 直抓 + License 字段 | 内网/环回/元数据 URL 全被拒；外网图入库可引用过白名单；上传图登记 user-provided；credit 分档显示 |
| 3 | capture_page | 公开页截图入库登记 shot/quoted-reference；X 登录墙页面明确报「请截图上传」 |

## 8. 未决问题（拍板点）

1. 预采触发时点：分镜保存即采（D6 倾向）vs start_production 后——**倾向保存即采**，理由见 D6。
2. URL 抓取入口：agent 工具、用户 UI 按钮都给还是二选一——**倾向 UI 先做**（授权责任落在人）。
3. 片尾 credit 的分档展示格式（现有 credit 行渲染在哪、要不要按片尾统一出）——实施第一刀时顺手查现状再定。
