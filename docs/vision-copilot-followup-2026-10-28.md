# 视觉副驾换脑 · 收尾待办（2026-10-28）

背景一句话：主脑换 glm-5.3（纯文本，端点对图硬拒收），看图活走 glm-5.3-flash 视觉副驾
（`llm.VisionDigest` 单发细读成文字画像）；判卷链整段留 flash。架构与速查表见
`docs/production-architecture-v3-interactive.html`「视觉副驾 · 大杯换脑」块。

## 已定案的事实（别再重新讨论）

- **探针（2026-10-28，编码端点）**：glm-5.3 带图 → HTTP 400（code 1210：
  `messages.content.type 参数非法，取值范围 ['text']`）；同请求纯文本正常；
  glm-5.3-flash 带图正常读图（纯色图答对颜色）。复现：base64 纯色 PNG 塞
  `image_url` 块 POST `/chat/completions`，两模型对照。
- **模型分档**：`LLM_PLAN_MODEL=glm-5.3`（①②③的脑＋④写画面的脑）；`LLM_VISUAL_MODEL=glm-5.3-flash`
  （视觉副驾＋判卷链：样张判卷/frameQA/段审/终审/入库质检）；Dialogue 默认 flash
  （主管对话/配乐/翻译）。`LLM_EFFORT=low` 已补回（10-27 思考失控教训，曾一度丢失）。
- **写画面注入 = 三条文字通道**：样张源码（权威规范，一直就有）＋ 真迹逐帧指认
  （style_brief 的 EvidenceNotes，原有）＋ 风格画像（新增：副驾每片一次细读
  样张截图+真迹，全段逐字共享进稳定前缀；独立记账任务 `seg.stylebook`）。
- **缓存红利（设计层已兑现，实测待办 3）**：写画面会话纯文本后，跨段前缀共享、
  修复轮续写不再因丢图断前缀——2026-10-26 基线：全站命中率 39.8%、seg.write 22%。

今天已落盘的改动（全部 `go test` 零失败）：`llm/copilot.go`（新）、`llm/provider.go`
（GenerateJSONNoThinkTemp）、`prep/style.go`（②换脑+细读替代附图）、`produce/scenegen.go`
（④换脑+styleBook+素材细读）、`produce/materials.go`、`agent/agent.go`（主管状态出
system）、`prep/style_refimg_test.go`、`.env`（三行）、架构 HTML（分档表/速查表/节点详情）。

待办 1 边界化重构已落盘（2026-10-28 下午，`go build`+全量 `go test` 零失败）：
`prep/script.go`（①检索纪律行出 system→web_search 描述补「1–2 轮」）、`prep/style.go`
（②system 收敛为流程+政策；真迹画像进 get_style_detail 描述、场景约束/整组张数进
render_sample 描述、转场禁即兴进 submit_style 的 entry_exit 字段契约）、
`prep/storyboard.go`（③去「按句编号」冗余）、`agent/agent.go`（主管：URL 形态/搜索
额度/set_voice·extract_stylepack 连锁/rework 机制括注出 system，📎≈rework 与起草
默认不落盘等政策保留）+ `agent/tools.go`（export/web_search/fetch_url/rework/
extract_stylepack 五处描述承接）——架构 HTML 三工位提示词全文+主管摘要+速查表边界
注记已同步。另：补完 0007 迁移（drop render_jobs）遗留的编译断点——`store/store.go`
DeleteProject 残留 s.jobs 清理块、`produce/produce.go` 残留未用 store import。

## 待办

- [x] **1. 系统提示词边界化重构**（用户提案，2026-10-28；同日落盘，见上方改动清单）
      边界：**单工具机制**（如「read_source 是材料唯一入口」「estimate≈4.5字/秒」）
      搬进工具 description；**跨工具流程**（先读后写、submit 前自检）与**政策**
      （起草默认不落盘、📎引用≈rework 意图）留 system——它们没有别的地方可住。
      涉及 prep ①②③ system + 工具 schema + 主管 systemPrompt。
      注意：token 零和（工具描述同样在前缀里），收益是可维护性与注意力分配；
      验收 = 下一部片照常出，不是省 token。
- [ ] **2. ② 样张工位冒烟**——今天全部改动的最终验收，一次验三条新通路：
      副驾细读回喂（get_style_detail 真迹画像）→ 大杯写样张 HTML → flash 判卷对照真迹。
      通过标准：样张循环完整走通、`agent.done` 里细读调用有账、全程无 400。
- [ ] **3. 首部 GLM 片出片后拉缓存账**：manifest 的 `cached_tokens` 聚合，
      对比上面 39.8% / 22% 基线。重点看 seg.write（预期显著上升）。
- [ ] **4.（观察项，等 3 的数据）风格画像是否裁掉样张截图部分**：样张源码已是
      权威规范，截图细读可能冗余；真迹部分必须保留（真迹无源码可替代）。

## 顺延（旧账，别丢）

- 判卷官方差 ±8–18 分 → 多官中位/像素锚定（style-kit 计划第 2 步，已定案未实施）。
- 架构 HTML 是代码的手工副本：改任何提示词/预算后必须同步（各节点「系统提示词」
  行与速查表）；长期解法是从代码常量生成文档，未排期。
