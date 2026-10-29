# 方案 v3：下线自动沉淀，新增"项目解析"路（与上传视频解析并列为两路）

## 架构

```
路1（现有）: 上传视频 → ffmpeg抽帧+RoleVisual多模态打标 ┐
                                                        ├→ references/ 暂存区草稿 → 修订 → 确认入库 → 风格库
路2（新增）: 项目 → 读设计系统JSON+拷样张帧 → RolePlan文本打标 ┘
```

依据：成片是设计系统的确定性渲染，`style/style_samples.json` 比视频更精确（CSS 色值/字体名/动效毫秒/负面清单），项目路不抽帧、不用多模态。唯一 LLM 步是文本打标。

## 改动一：摘除自动沉淀 + 退役旧直写路径

- `agent/tools.go:581-624`：删 `extract_stylepack` 工具表项。
- `agent/agent.go:631`：删系统提示词尾句"出片后主动 extract_stylepack 沉淀一次风格。"；`:617` 注释同步。
- `agent/agent.go:391-407`：删 `draftedStyle`（`selectedStyle` 保留，`apply_style` 在用）。
- `agent/agent.go:409-451`：`designDigest` 搬到 styleref 包导出为 `DesignDigest(p *pipeline.Project)`（styleref 已 import pipeline/contract，无环）。
- `stylelib/stylelib.go:251-310`：删 `DraftFromProject` + `ProjectDraftIn`——项目→风格库唯一入口变为新路（确认入库即发布，与上传路一致；存量未发布条目仍可用库页"入库"按钮）。

## 改动二：暂存区 Ref 支持"项目来源"

- `styleref/styleref.go:34-42` `Ref` 增加 `Source string`（""|"upload"|"project"，omitempty）+ `ProjectID string`，随 ref.json 免费序列化；老数据零迁移（空 source 视为 upload）。
- 新增 `Store.SaveProject(name, projectID, frameSrcs)`（仿 `Save` :173-209）：同款 `os.Mkdir` 抢号、保持 `ref` 前缀（`List()` :222 前缀过滤可见），拷样张帧为 `frame-*.png`，写 ref.json，无视频文件。
- 幂等：已有 `Source=="project" && ProjectID==id` 的条目则复用。

## 改动三：解析器唯一分叉点

`styleref/analyze.go:38-57` `runAgent` 按 `Source` 分支：

- **upload**：现状不动（找视频→python 抽帧→多模态）。
- **project**：读 `style/style_samples.json`（缺失→fail"项目没有定稿的设计系统"）→ `DesignDigest` 造摘要 → `llm.FromEnv(llm.RolePlan)` → `DraftFromDesign`（retag.go 原样复用：词表查重/申请新词，3 分钟超时）→ 草稿写 ref.json。样张帧已在目录，能挂主标签证据就挂，否则留空不阻塞。

下游零改动：修订 PUT、TemplateDetail 弹窗、confirm→`ingestRefStyle`（帧校验兼容空帧/样张帧）、`SnapshotMedia`、makeCover、GC/云同步（均按 `ref*` 目录通用处理）。唯一小分支：确认入库时 `OriginProject` project 路写真实项目 ID（upload 路维持 `"video:"+refID`）。

## 改动四：API 新端点

`POST /api/projects/:id/style/parse`（server.go libAdm 组，与 reference 写操作同权限 requireUser+projectGuard+requireAdmin）：

1. 预检 style_samples.json 存在（无→400"项目还没有定稿的设计系统"）。
2. 幂等查重（已有→200 返回既有 ref_id）。
3. `SaveProject` + `s.Refs.Analyze(refID)`（复用现有异步机制：running 幂等/并发闸 2/错误落 ref.json）→ 202 `{ref_id}`。

无新 GET：前端轮询现有 `GET /api/references` 即可。

## 改动五：前端

- **按钮**：`StagePanel.vue` head-right（:264-287）"下载成片"左侧，同款胶囊样式；`v-if="view.has_video"`；producing/请求中禁用，文案"解析中…"。
- **点击流**：POST → toast"解析已开始" → 自动跳风格库页：App.vue 加 `materialsTab` 状态，`LibraryCenter.vue:11` tab ref 改为接受 `initialTab` prop；`applyHash`（:439）支持 `#/materials/videos`。
- **api.ts**：加 `parseProjectStyle(id)`。
- **RefVideos.vue**：`source==="project"` 卡片不渲染视频行，挂「项目解析」NTag + 项目名；「重新解析」照常（重跑文本打标）。

## 项目解析五步（实现参照）

1. 预检+建暂存条目（无 LLM）→ 2. 拷样张/判卷帧为证据帧（无 LLM）→ 3. DesignDigest 机械摘要（无 LLM）→ 4. DraftFromDesign 文本打标（唯一 LLM 步：词表比对+新词语义查重）→ 5. 草稿落暂存区，之后修订/入库与上传路完全合流。

## 测试与验收

- api 测试（仿 styleflow_test.go 无视频夹具）：① 夹具项目 POST parse→202、source=project；② 无 key 时 analyze 落 error"需要 API 模式"；③ 幂等返回同一 ref_id；④ confirm→库条目 Published:true 且 OriginProject=项目 ID。
- `go build ./...` + `go test ./...` 全绿；`npm run build` 过类型。
- 手动冒烟：出片项目点按钮→跳视频解析页见"解析中"→完成→修订→入库→库条目含样张证据帧；重复点击不建第二条。

## 注释与文档

`retag.go:3,18`、`styleref.go:528`、`stylelib.go` 相关注释改为"项目解析路"措辞；`docs/style-library-v3.md`、`docs/overview.md` 改为"两路解析"表述。