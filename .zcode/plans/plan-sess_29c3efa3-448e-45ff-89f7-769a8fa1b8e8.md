# M2-1：预览双模（MP4 ↔ 活合成物）+ 悬停点选命名元素

## 已验证的关键事实
- `compositions/frames/segNN.html` 是 `<template>` 片段：每个可见元素带 `id` + `data-hf-name="人话名"`，段内唯一 gsap timeline 注册到 `window.__timelines["segNN"]`，字体全 `local()` 系统字体，无外部 URL → **srcdoc 壳方案可行，不需要 408K 的 hyperframes runtime**（只需项目内 `assets/gsap.min.js`，渲染引擎同款）
- 段长 = TTS `duration_s + 0.35s`（尾垫），段起始按此累计（assemble.py:28-32）
- **发现既有 bug**：Timeline.vue:30 段起始按裸 `duration_s` 累计，与 MP4 实际错位（第 N 段偏 0.35×N 秒，第 10 段偏 ~3.15s）→ 必须一并修，否则帧级 seek 全歪
- PG `chat_messages.refs JSONB` 列已预留（migrations/0001:86），Go store.Msg 尚无该字段

## 改动清单

### 步骤 0：提交工作区现有 ~1000 行未提交改动（GLM provider 切换 + UI 打磨）

### 后端（Go，server/）
1. **两个新端点**（server.go）：
   - `GET /api/projects/:id/frames/:seg` → serve `compositions/frames/segNN.html`（校验 `^seg\d+$`，ServeContent）
   - `GET /api/projects/:id/assets/*filepath` → serve 项目 `assets/`（gsap.min.js；filepath.Clean + 前缀校验防穿越）
2. **refs 结构化落盘**：store.Msg 加 Refs 字段，SaveMsg/查询带出（PG 列已在、内存 store 同步）；chat handler 收 `{content, refs}`；agent.go 把 refs 格式化成引用块注入 prompt（如 `用户引用：段3 · 45.2s · 元素「箭头：模型→工具」(seg03-arrow1)`）。rework 工具签名不动——元素名随 instruction 文本流入 spec prompt（已验证该链路存在，produce.go:353/498）

### 前端（Vue，web/src/）
3. **新组件 `LiveFrame.vue`**（核心，~300 行）：
   - fetch 帧 HTML → 组 srcdoc 壳：doctype + `<script src="/api/projects/:id/assets/gsap.min.js">` + 片段原样 + 激活/交互脚本；`sandbox="allow-scripts"`（无 allow-same-origin，全走 postMessage）
   - 激活脚本：`window.__timelines = window.__timelines || {}` → template.content 挂 body（script 节点重建执行）→ transform scale 适配舞台与画幅（9:16/16:9）→ ready 消息回报段时长
   - 交互：`[data-hf-name]` 悬停描边 + 人话名 tooltip；点击 → postMessage `pick {segId, id, name, t}`；父发 `seek {t}` → `tl.pause().time(t)`（rAF 节流拖动擦洗）
   - 协议：`live:ready / live:seek / live:pick`（iframe→parent 用 `window.parent.postMessage`）
4. **StagePanel.vue**：视频态工具条加「🔍 检视」开关；开启时 player 区 `<video>` ↔ `<LiveFrame>` 切换（以当前播放位置定位段+局部时刻），Timeline `@seek` 改驱 iframe；`producing` 时禁用（rework 中帧文件在重写）；元素点选 → 上抛元素级引用
5. **引用升级**：`ChatRef` 扩展 `{t?, elementId?, elementName?}`，chip 三态：`📎 段3` / `📎 段3 · 0:45 ·「箭头：模型→工具」`；消息气泡渲染 msg.refs chips；发送改走结构化 refs（不再拍平拼文本）；types.ts/api.ts 同步
6. **Timeline.vue**：段长改 `duration_s + 0.35`，修与 MP4 的对齐 bug

### 不做（M2-2+）
圈选、秒/帧独立引用按钮、嘴说兜底、段级独立渲染（rework 后仍整片重渲）、元素列表面板

## 验收
- 暂停任意时刻开检视：iframe 画面与 MP4 同帧一致（布局/内容/动效状态，抽查首/中/末 3 段）
- 悬停任意元素出描边+人话名；点选出元素级 📎 引用；聊天「段3 那个箭头改红色」→ rework 生效
- 9:16 与 16:9 均正常缩放；Timeline 与 MP4 对齐修复后播放头/字幕不再漂移