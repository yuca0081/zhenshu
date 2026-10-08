# 风格库 v3：标签 + 特色清单 —— 实施规格书

> 本文档自足：执行者只依赖本文档 + 仓库代码即可开工，无需其他会话上下文。
> 状态：**已实施（2026-10-08，M1–M3 全部落地）**。M2 的真机冒烟（完整出片验判据播报）
> 与 M4/M5 仍按原计划暂缓（见 §9）。范围 = M1–M3。

---

## 0. 给执行 AI 的开工须知（必读）

1. **先看 git status**。本仓库常有并行会话同时改代码。重点确认 `server/internal/styleref/`（可能存在 video.go / analyze.go 未提交改动，视频直读功能）与 `server/internal/llm/`。若这些文件有未提交改动且并非本任务所做：**不要回退、不要重写它们**，用精准编辑避开；styleref 相关改动若冲突，先暂停该子项向用户报告。
2. **环境是 Windows + Git Bash**。改完 Go 代码必须重启 server 进程才生效（旧进程镜像是历史大坑）。
3. **测试纪律**：任何涉及 LLM 的单测必须走 mock（仓里有先例：`internal/react`、`internal/agent` 的 mock 测试），**严禁在测试里烧真实 LLM/搜索配额**。
4. **不要擅自 git commit**。完成后跑完全套验证（§6）向用户报告，提交由用户决定。
5. **范围护栏**：本任务**不动**渲染管线的工作区文件语义（产物落盘、断点续跑"文件在=跳过"）、**不动** contract 的设计系统载体（StyleSamples 仍是设计阶段的产出与确认对象）、**不碰** `data/library/methods|media|vendor`（方法库/配乐/vendor JS，与风格库同层不同物）。
6. 每完成一个里程碑：`cd server && go build ./... && go vet ./... && go test ./...` 全绿，且 `cd web && npm run build && npx vue-tsc --noEmit` 干净，才算该期完成。

---

## 1. 背景与目标（为什么改）

本项目是"网页短视频工厂"：Go 后端（HTTP + SSE + ReAct 主管 + 制作管线）+ Vue3 前端，把文稿/分镜做成渲染好的短视频。风格库现状（v2）把参考视频解析成"风格包"（色板 tokens、字体、HTML 样张、皮肤等）存 `data/library/styles.json`，制作时整包拷进项目。

**v2 的病根**：公认风格（赛博朋克、包豪斯、粘土…）是大模型固有知识，提取 tokens/样张去"教"它属多此一举，且解析慢（多轮 LLM+校验打回）、条目曾发生"题材物件中毒"事故（风格样张里的猫脸进了成片）。

**v3 目标**：库降维成"策展目录 + 绝活收藏夹"——
- 公认风格只打**标签**（引用模型固有知识，不提取教学）；
- 一支参考片真正独有的手法（运镜/转场/母题/动效/素材/配色）提炼成**特色清单**，制作时选条目即整包生效、单条可关，作为"强参考"喂给设计阶段；
- 证据（原片+抽帧）保留：打标证据、QA 考卷、回看素材。

## 2. 前提与基础条件

| 项 | 要求 |
|---|---|
| 运行环境 | Windows + Git Bash；Go（server/）；Node+npm（web/）；ffmpeg 在 PATH（管线依赖 `pipeline.FFmpegPath`） |
| LLM | API 模式（GLM 编码套餐，`llm.FromEnv` 角色化取用）；解析与设计沿用现有角色配置，**不新增角色** |
| 数据库 | **M1–M3 不需要**。PG/MinIO 属 M4（暂缓，见 §9）；`internal/store` 现状是 PG 镜像 + 内存兜底，本任务不碰 |
| 库数据现状 | `data/library/styles.json` = `[]`（空库，零迁移负担）；`style_media/` 空；`data/references/` 下有历史解析残留目录，可当测试素材 |
| 代码基线 | 以下修复已完成且全绿（2026-10-08）：默认兜底包已删（不选风格=模型自由发挥，走 `pipeline.Run(p,"style_samples")`）；新增 `DELETE /api/library/:id`（`stylelib.ErrNoEntry`→404）；stylelib 旧迁移代码/UpdateEntry/BackfillMedia/stylebackfill CLI 已删；`stylelib.New` 为进程内单例（同库同锁）；createProject 认 style_id+stylepack_id 双字段；题材中立清扫已完成 |
| 题材中立纪律 | **所有新增提示词/校验/示例词禁止出现"科普/解说"等题材预设**；风格条目的 subject/genre/keywords 是检索元数据，**永不进任何生成提示词**；特色 mechanism 必须写机制不写物件（见 §4.1 校验规则） |

## 3. 现状代码地图（改动前先看这里）

### 后端
| 位置 | 现状 | v3 命运 |
|---|---|---|
| `server/internal/stylelib/stylelib.go` | `Style` 结构（id/name/direction/desc/subject/genre/keywords/tokens/layouts/concept/fonts/motion/device/anti_patterns/keyframes/samples/skin/origin_project/published/created_at）；`Library`（List/Get/ByDirection/DraftFromProject/IngestVideoRef/Publish/Delete/ErrNoEntry）；`StyleSamplesJSON()`；`SkinFromTokens` 在 skin.go | **schema 换新（§4.1）**；`StyleSamplesJSON`/`ByDirection`/skin.go 派生在 M3 退役 |
| `server/internal/stylelib/media.go` | `MediaDir/PickKeyframes/KeyframePaths/SnapshotMedia`（style_media/<id>/ 快照，硬链接优先） | **保留**，帧证据复用 |
| `server/internal/styleref/styleref.go` | `Ref`/`StyleDraft`（含死字段 Photo）/`analyzeCompositions`/`synthesize`（五件套合成 prompt）/`validateStyle`/`ValidateDesignSystem`/`readFeatures` | **synthesize prompt 重写为打标+特色提炼（§4.4）**；Photo 死字段顺手删 |
| `server/internal/styleref/analyze.go` | `runAgent`（抽帧→detectStructure 判单/多风格→递归细拆→逐段合成） | 流程骨架**保留**，逐段合成换新作业 |
| `server/internal/styleref/video.go` | （并行 WIP）视频直读校验 | 若已落地：camera 类特色识别接 video_url；未落地：按帧通道先行（§4.4） |
| `ai/extract_video_features.py` | 抽帧（≤36 帧）+网格图+色板 k-means+硬切点 | **保留不动** |
| `server/internal/agent/agent.go` | 工具 `draft_style_samples`（选定条目→拷 StyleSamplesJSON；无选定→自由发挥）、`search_styles`、`extract_stylepack`；`selectedStyle` | 选定分支改写 brief（§5 M1）；检索输出换新字段 |
| `server/internal/pipeline/design.go` | `designSystemPrompt`/`designBrief`/`designJSONShape`/`DesignDoc`/`LockDesign`；铁律"系统是发明的" | 注入简报块 + 自报落点（§4.5） |
| `server/internal/pipeline/pipeline.go` | `stageStyleSamples`（草稿校验 3 轮→落 final→LockDesign）；`Run(ctx,p,until)` 从头跑到 until、产物在则跳过 | 校验处加落点覆盖检查（§4.5） |
| `server/internal/contract/` | `StyleSamples`（设计系统载体，含 skins/fonts/motion/device/layouts/anti_patterns/samples/confirmed）；`ValidateStyleSamples(Draft)` | 加可选字段 `signature_absorption`（§4.5），其余不动 |
| `server/internal/produce/frameqa.go` | 成片后画面 QA：样张+真迹考卷、风格漂移/题材中毒判据、修复轮回滚 | 加特色判据清单（§4.6，M2） |
| `server/internal/produce/stylenote.go` | `designNoteFor`/`projectStyle`（style_id 锚定+ByDirection 兜底）/`styleKeyframeImages` | M1 起条目无 tokens/keyframes 字段：`projectStyle` 的 tokens 兜底分支随 schema 退役，真迹图改从简报证据帧取 |
| `server/internal/api/server.go` | 路由：GET/DELETE `/library`、POST `/library/:id/publish`、GET frames/media；`applyStyle`（现行为：整包拷 style_samples.json 并置 confirmed）；`confirmReference`→`ingestRefStyle`（变形+makeCover+PickKeyframes+IngestVideoRef+SnapshotMedia） | applyStyle 改写 brief（§5 M1）；ingestRefStyle 换新载荷 |
| `server/cmd/server/main.go` | 装配：store/lib/producer/agent/refs | 不动（除字段传递需要） |

### 前端（web/src）
| 文件 | 现状 | v3 命运 |
|---|---|---|
| `types.ts` | `Style` 接口（五件套/tokens/samples/skin…） | 换新 schema（§4.1） |
| `api.ts` | listLibrary/publishPack/deleteLibrary/applyStyle/confirmReference… | applyStyle 载荷加特色开关数组 |
| `LibraryPanel.vue` | 条目卡片（SampleFrame 样张预览/删除/入库） | 卡片改版：标签 chips + 特色数，无样张 iframe |
| `TemplateDetail.vue` | 详情抽屉（样张 iframe + 抽帧回看 + 入库） | 改版：标签+特色清单+证据帧；RefVideos 确认入库前可增删改特色 |
| `GatesModal.vue` | 风格硬门弹窗（选卡片→applyStyle 即时生效） | 选条目→特色开关面板（默认全开）→开工写 brief；**不再"即时生效"**（§4.5 行为变化） |
| `RefVideos.vue` / `App.vue` / `LibraryCenter.vue` / `SampleFrame.vue` / `format.ts` | 解析上传轮询 / 页面装配 / 样张缩放 / 封面色 | 随动 |

### 数据目录
```
data/library/
  styles.json        # 条目（v3 换 schema）
  vocab.json         # 【新增】风格词表（§4.2）
  style_media/<id>/  # 原片+抽帧快照（机制不变）
  media|methods|vendor/   # 【勿动】配乐音效/方法库/vendor JS，与风格库无关
data/references/<id>/     # 参考视频工作目录（video.ext/ref.json/frame-*.png/features.json）
data/projects/<id>/style/ # style_samples.json（设计系统载体，保留）、skin.json、design.md、【新增】style_brief.json
```

---

## 4. 目标设计

### 4.1 条目 schema（styles.json，v3）

```go
type Style struct {
    ID            string        `json:"id"`                  // vid-<refID>[-sNN] | sp<ts>（沿用现有生成规则，M3 再谈合一）
    Name          string        `json:"name"`
    Tags          StyleTags     `json:"tags"`
    TagEvidence   []TagEvidence `json:"tag_evidence,omitempty"`  // 每个 tag 至少一条
    Signatures    []Signature   `json:"signatures,omitempty"`    // 0–6 条
    Subject       string        `json:"subject,omitempty"`       // 原片题材一句话，只供检索，永不进生成提示词
    OriginProject string        `json:"origin_project,omitempty"`// "video:<refID>" 或项目 id
    Published     bool          `json:"published"`
    CreatedAt     time.Time     `json:"created_at"`
}
type StyleTags struct {
    Main string   `json:"main"`             // 词表 id，必填
    Aux   []string `json:"aux,omitempty"`    // ≤2，均为词表 id
}
type TagEvidence struct {
    Tag    string   `json:"tag"`
    Note   string   `json:"note"`             // 人话指认："第2段：紫青撞色+网格地平线"
    Frames []string `json:"frames,omitempty"` // style_media/<id>/ 下的 frame-*.png 文件名
}
type Signature struct {
    ID        string   `json:"id"`              // sig1..sigN（条目内唯一）
    Kind      string   `json:"kind"`            // camera|transition|motif|motion|material|palette（枚举硬校验）
    Name      string   `json:"name"`            // 人话短名
    Mechanism string   `json:"mechanism"`       // 机制描述，1-2 句，可执行
    Frames    []string `json:"frames,omitempty"`// 证据帧 ≥1，文件必须存在于 style_media/<id>/
    UseHint   string   `json:"use_hint,omitempty"` // 什么节拍/场景适合用
}
```

**校验规则（入库与加载都执行）**：
- `tags.main` 必填且必须在词表中（新词走 §4.2 申请流程先入表再引用）；`aux` ≤2 且同上；
- `signatures` ≤6；`kind` 枚举校验；`mechanism` 非空；`frames` 引用的文件必须真实存在；
- 机制不写物件：**机械不做词汇黑名单**（误杀率高），由三处兜底——打标提示词纪律 + 入库确认页人工过目 + QA 题材中毒反向判据（沿用现有）；
- **删除字段（相对 v2）**：direction/desc/genre/keywords/tokens/layouts/concept/fonts/motion/device/anti_patterns/keyframes/samples/skin 全部不再写入。`direction` 语义由 `tags.main` 的词表名 + 简报承担。

### 4.2 风格词表（data/library/vocab.json，新增）

```go
type Vocab struct{ Entries map[string]VocabEntry `json:"entries"` } // key = id
type VocabEntry struct {
    ID        string    `json:"id"`              // 小写蛇形，如 claymation
    Name      string    `json:"name"`            // 正式名，如 粘土定格
    Aliases   []string  `json:"aliases,omitempty"` // 中英+俗称，查重用
    Brief     string    `json:"brief"`           // 一句机制描述：色域/质感/光/字体气质
    Tells     string    `json:"tells,omitempty"` // 判别特征：帧里认什么
    CreatedAt time.Time `json:"created_at"`
}
```

- **冷启动 = 空表**（已拍板）：第一条片入库时打的标签经查重后自动成为第一批词条。
- **收录标准**（写进打标提示词）：只收有公认视觉定义的风格名词（设计史/影视行话立得住），拒收"高级感/简洁大气"类空话。
- **查重（申请新词必经）**：新词的 name+aliases 与现有全部词条的 name+aliases 归一化比对（小写去空白精确匹配）+ 一次 LLM 语义查重（把候选词与现有词条列表给它，问"有没有其实是同一个东西的不同名字"，命中则挂别名到已有词条而非新建）。
- 存取：`stylelib` 内新增 vocab 读写（同 styles.json 的锁与原子写模式，读失败不落盘）。
- 同义词归词表管；**条目不去重**（同标签多条目正常，特色各自不同）。

### 4.3 风格简报（项目内 style/style_brief.json，新增）

```go
type StyleBrief struct {
    EntryID   string      `json:"entry_id"`
    EntryName string      `json:"entry_name"`
    Tags      StyleTags   `json:"tags"`
    TagBriefs []TagBrief  `json:"tag_briefs"`   // {ID, Name, Brief} 从词表取，供设计提示词直接引用
    Signatures []Signature `json:"signatures"`  // 开着（未关）的特色，含原文 mechanism
    Evidence  []string    `json:"evidence"`     // 证据帧绝对路径（从 style_media 取，≤6 张）
}
```
- 写入者：`applyStyle` API（前端开关数组）与 agent 选定条目分支，**同一文件同一格式**；重选条目 = 整体重写。
- 简报是项目的本地拷贝，与库解耦：条目后来被删除/修改不影响已选项目。
- 不选条目 = 文件不存在 = 设计阶段裸发明（现状已上线）。

### 4.4 入库流程（解析重做）

```
上传视频 → ai/extract_video_features.py 抽帧取色（不动）
→ detectStructure 判单/多风格 + 段拆分（不动）
→ 逐段/整片一次轻解析（synthesize prompt 重写）：
    工具「查风格词表」（返回词条 id/name/brief/tells）→ 打标签（主1+辅≤2，每个带证据帧指认）
    → 提炼特色清单（≤6，六类枚举，机制化写作+证据帧）
    → 词表没有的标签 → 「申请新词」（name+aliases+证据）→ 查重通过 → 写 vocab.json → 引用
→ 前端确认页：标签/特色/抽帧回看，用户可增删改特色
→ 确认入库 = IngestVideoRef（新载荷）+ SnapshotMedia（机制不变）
```

**多模态双通道**：静态=帧+网格图喂视觉模型（构图/密度/配色/字体/质感，基建现成）；动态=video_url 直读（运镜/转场节奏，GLM ≤200MB 只收 URL，依赖并行会话的签名端点）。**通道未落地前按帧通道实现，落地后 camera/transition 识别升级，接口留好**（解析函数接受可选的视频 URL 参数，为空则纯帧）。

**入库不再产出**：五件套/tokens/HTML 样张/skin/layouts。解析轮次从"3 轮带错重喂"降为 1 轮 + 结构校验失败重喂 ≤2 轮。

### 4.5 使用流程（选条目 → 参考发明）

1. **选条目**（GatesModal/风格面板）：展示标签（含词表 brief）+ 特色清单（证据帧缩略图），**每条特色开关默认全开**；点开工即确认，不另设门。
2. **写简报**：`applyStyle` 落 `style/style_brief.json`。**行为变化（相对 v2 要写进用户报告）**：选库内风格不再"即时生效跳过样张确认"——v3 条目没有自带样张，设计系统必须本片现发明，样张确认硬门对所有人成立。
3. **设计阶段（design.go）**：`designBrief` 在存在 brief 时追加注入块：
   ```
   ## 本片风格参考（用户选定，硬要求）
   气质基调：<main 词条 name + brief>；辅：<aux…>
   必答特色（每条都必须被设计系统吸收，并在 signature_absorption 中登记落点）：
   1. [transition] 道具递接转场——机制：…（证据帧已附）
   ```
   `designJSONShape` 增加：
   ```json
   "signature_absorption": [ { "sig_id": "sig1", "lands_in": "device|motion|skins|camera_plan|transition_policy|material_policy", "how": "一句话怎么落" } ]
   ```
4. **落点校验**：`contract.StyleSamples` 加可选字段 `SignatureAbsorption []SigAbsorption`（json tag `signature_absorption,omitempty`）；`stageStyleSamples` 在 `ValidateStyleSamplesDraft` 通过后追加检查：brief 存在时，`signature_absorption` 必须覆盖 brief 全部 sig_id（lands_in ∈ 枚举），缺/越枚举=按校验失败回喂重出（沿用现有 3 轮循环）。`DesignDoc` 渲染时把落点表写进 design.md。
5. **证据帧注入**：`styleKeyframeImages` 改从 brief 的 evidence 取图（原来从条目 Keyframes 取），压缩缓存机制不变。
6. QA 考卷对照物 = 项目样张截图 + 简报证据帧；题材中毒反向判据保留。

### 4.6 特色验收机制（三层 + 实例走读）

**第一层：发明时自报落点**——见 §4.5 第 3/4 步（design 交卷必须登记每条特色落在哪个组件；机械数数，缺=打回）。

**第二层：QA 逐条判据**——`frameqa` 读 brief + 落点表构造"特色判据清单"：每条特色由 mechanism+use_hint 套模板生成一句是非题（"检查：段间切换是否为前一画面物件推出+后一画面跟进？物件是否重复？"）；视觉模型逐条判 **absorbed / drifted / missing**；missing|drifted → 判据原句进修复要求 → 段级重做 → 复验（复用现有修复循环与事件播报）。

**第三层：帧还是视频片段（分档混用）**：
- **帧判**：palette/motif/motion 落定态——静帧直接验；
- **片判**（掐点截"考核片段"，走 video_url 或本地截段）：transition=段边界前后 ±3s；camera/motion 节奏=该段整段（单段十几秒、几 MB；段时长从 audio_meta.json/时间轴已知）。**不送整片**。
- **成本策略**：第一遍全量帧筛 missing；勾了动态特色才追加片段判；没勾动态特色零视频调用。
- **现状衔接**：video_url 通道落地前，transition 退化为边界帧序列（边界前后各 3-5 帧）、camera 只验有无（回放人兜底）；落地后自动升级，业务代码不变。

**实例走读**（选定"故障风数据片"，其特色"道具递接转场"开关开着）：选条目开工 → brief 落盘 → 设计交卷登记"转场落在 transition_policy：章节数字卡被上一段柱状图推出画外" → 做片 → QA 掐着 seg03→seg04 边界截帧/截段 + 判据句喂模型 → 判 missing → 判据原句进修复要求重画该段 → 复验 absorbed → 回放终审。

---

## 5. 任务分解（里程碑与依赖）

> **执行顺序修正（重要）**：v3 条目没有样张/皮肤/tokens，旧消费路径（StyleSamplesJSON 拷贝、designNoteFor 的 tokens 兜底）会在 schema 切换瞬间失效。因此 **M1 是"一刀切换"**：入库侧 + 简报 + 设计注入 + 前端选条目必须同一期完成并跑通冒烟；**不允许**只切入库不切消费的中间态提交形态。

### M1 核心切换（一条链：上传→打标入库→选用→设计吸收）
1. `stylelib`：新 schema + 校验（§4.1）；vocab 读写与查重（§4.2）；`IngestVideoRef` 新载荷；保留 Delete/Publish/媒体快照；**临时保留** `Get/List`（检索用）。
2. `styleref`：synthesize prompt 重写（打标+特色+查词表/申请新词两工具）；`StyleDraft` 换新字段（顺手删 Photo 死字段）；validateStyle 换 §4.1 规则；`ValidateDesignSystem` 若仅 stylebackfill/旧校验用则一并退役。
3. `api`：`ingestRefStyle` 换新载荷；`applyStyle` 改写 brief（接收特色开关数组）；`/api/vocab` 只读端点（前端展示词条 brief，可选）。
4. `agent`：`draft_style_samples` 选定分支改"确保 brief 落盘后走 pipeline.Run(style_samples)"；`search_styles` 输出新字段；`extract_stylepack`（成片反哺入库）改为起草"标签+特色"草稿（成片只有本片无原片帧，样张证据帧缺省，允许 frames 为空的草稿，人工在库页补）。
5. `pipeline/contract`：brief 注入 + signature_absorption 字段与落点校验 + DesignDoc 落点表（§4.5）。
6. `produce/stylenote.go`：`projectStyle` 的 tokens/ByDirection 兜底分支移除（M1 就删，因为 schema 已无此字段）；真迹图切 brief evidence。
7. `web`：types/api/LibraryPanel/TemplateDetail/GatesModal 按 §3 表改版；特色开关面板（默认全开）。
8. **测试**：schema/vocab/查重/落点校验/brief 写入全部单测（LLM mock）；smoke=上传→入库→选用→design 产出含落点表→确认风格（不必出片）。

### M2 验收闭环
1. `frameqa`：特色判据清单生成、三档判定、修复轮接判据、播报事件（§4.6）。
2. 考核片段：ffmpeg 掐段边界 ±3s / 单段整段截取（缓存进项目 style/qa/）；video_url 通道可用则走通道，否则边界帧序列。
3. smoke=完整出片一次（真实 LLM，须用户点头才跑）：勾 palette+transition 的条目走全链，确认判据播报与修复触发。

### M3 清扫
1. 退役删除：`Style.Direction/Desc/Genre/Keywords/Tokens/Layouts/Concept/Fonts/Motion/Device/AntiPatterns/Keyframes/Samples/Skin` 字段、`SkinFromTokens`（skin.go 整文件）、`StyleSamplesJSON`、`ByDirection`、`VideoStyleID`（若确认无引用）、前端 `coverStyle` 的 tokens 分支、`SampleFrame` 在库页的使用、旧 `search_styles` 的 PaletteNote 行。
2. 两套 id（sp/vid-）合一评估（能小步做就做，做不动写进报告）。
3. 文档：`docs/overview.md` 风格库章节重写为 v3 叙事；本文档标记"已实施"。

## 6. 整体验收标准（每期 done 的定义）

- `go build ./... && go vet ./... && go test ./...` 全绿；`npm run build` + `vue-tsc --noEmit` 干净；
- 单测覆盖：schema 校验、词表查重、brief 写入、落点覆盖校验、判据生成（全部 mock LLM）；
- M1 冒烟：上传一段真实视频 → 标签+特色入库（词表自动长出第一批词条）→ 新项目选条目 → 样张确认（design.md 含落点表）；
- M2 冒烟：完整出片，特色判据播报可见；
- 人工检查：新增提示词无题材预设字样；`data/library/methods|media|vendor` 未被触碰。

## 7. 兼容与迁移

- 库为空（`[]`）→ 无存量迁移；`styles.json` 直接按新 schema 写。
- 老项目不受影响：其 `style/style_samples.json` 本地自足；`projectStyle` 对老项目按 style_id 查库查不到时静默返回 nil（无 tokens 兜底也不再注入，画面靠项目自己的设计系统文档，行为可接受）。
- `data/references/` 历史目录保留，可作测试素材。

## 8. 风险与对策

| 风险 | 对策 |
|---|---|
| 标签打歪/漂移 | 证据帧强制指认 + 词表 tells 统一判据 |
| 特色写成物件（题材中毒后门） | 打标提示词纪律 + 入库确认页人工过目 + QA 题材中毒反向判据（不做词汇黑名单，误杀率高） |
| 气质还原变弱（丢精确色板/样张） | palette 列为特色类型可提取；不够再把"样张对照"加回 |
| 强参考变枷锁 | 特色开关默认全开但随手可关；设计提示词明确"吸收机制、物件换成本片内容" |
| M1 中间态断链 | 入库+消费同一期切换（§5 执行顺序修正），期内全链冒烟后才算完成 |
| PG/MinIO 双写不一致（M4） | 先传对象后写元数据行；孤儿对象定期清扫 |
| 并行会话冲突 | 开工前 git status 分辨 WIP；styleref 冲突即停手报告（§0） |

## 9. 范围外（设计保留，本次不做）

- **M4 存储层**：PG（style_entries/style_vocab 元数据表）+ MinIO（媒体对象事实源，磁盘退为渲染缓存）+ objectStore 接口（minio/disk 双实现）+ 三条媒体路由改造 + 五张 v1 死表（manuscripts/storyboards/style_packs/method_notes/assets）清理。触发条件=服务器部署软件整理完成；降级阶梯：全配=完整模式 / 只配 PG=元数据入库 / 没配=全本地 / 配了连不上=拒启。
- **M5 运镜增强**：video_url 视频直读接入解析与考核片段（依赖并行会话基建）。
- 条目编辑 API（M1–M3 只有删除/发布；编辑与补录特色留在库页"删除重析"的人工流程）。
