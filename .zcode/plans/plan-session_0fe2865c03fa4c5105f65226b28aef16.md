# zhenshu 前端 Rhine 风格整体改造方案

## 目标

把 zhenshu 的前端（Vue 3 + Naive UI，web/src，22 组件约 6500 行）整体换成 yuca-next 的 Rhine 设计语言：晨雾/月影双主题、HUD 四角构图、零圆角直角美学、发丝线+大写微标+零填充编号、玻璃面板、首页 3D 玻璃书架、页面切换过渡、MiSans 字体。**只换皮不动逻辑**：api/types/SSE/工位流程/脏标记机制全部原样保留，DOM 结构尽量不动，只改样式层和 App.vue 的导航壳。

## 核心思路：搬令牌，不搬框架

yuca-next 是 React+Tailwind，zhenshu 是 Vue+Naive UI，框架不搬。要搬的是**设计体系本身**：一套 CSS 变量令牌 + 一层语义类 + 一套动效纪律。zhenshu 现状是零令牌、数百处硬编码 hex（金色 #f0c674 出现 74 次），所以本次的体力活大头是「把散落的字面量收敛到令牌」——这本身也是换肤的前提。

## 新增文件（6 个）

| 文件 | 内容 |
|---|---|
| `web/src/styles/rhine.css` | 体系根：`@import misans` 字体；`:root[data-theme='day'/'night']` 双主题令牌（照抄 yuca 色板，另补 `--ok/--err/--info` 状态色的日/夜两档，Rhine 色板里没有状态色）；`.micro/.hairline/.num/.panel/.btn/.field/.data-row/.stat-card/.side-item/.module-card/.theme-capsule` 语义类；keyframes 库（rise/swap-in/dialog-in-out/fade/vt-old-lift/vt-new-rise/grid-drift/word-reel）；`html.theming` 650ms 全局缓变；View Transition CSS（hud-top/hud-bottom 锁定不参与转场）；全局滚动条 |
| `web/src/theme.ts` | 主题 composable：`theme` ref（'day'/'night'）+ localStorage（键 `zhenshu-theme`）+ setTheme（挂 `.theming` 650ms）+ `naiveTheme` computed（day→null 走 Naive 亮色，night→darkTheme）+ `naiveOverrides` computed（按主题生成：primaryColor 赭棕/鎏金、borderRadius '0px'、cardColor/modalColor/inputColor 映射到 --surface/--panel、borderColor/textColor 映射令牌、fontFamily） |
| `web/src/components/ThemeCapsule.vue` | 晨雾/月影滑动胶囊切换器（照抄 yuca CSS） |
| `web/src/components/RIcon.vue` + `web/src/icons.data.ts` | 图标载体 + SVG path 表（从 yuca 移植 32 个线性图标，按 zhenshu 需要补 2-3 个：影片/风格库/上传），替代现在的 unicode 字符（＋/↓/⇪） |
| `web/src/components/ProjectShelf.vue` | 3D 玻璃书架：移植 yuca `shelf-scene.ts`（vanilla three 类）+ `ShelfCanvas` 薄包装，改造成项目书架——每块玻璃板用 Canvas 2D 画项目封面（项目名折行 + 零填充编号 + 日期 + 状态短划，颜色读 CSS 令牌随主题联动），拖拽翻页/点击打开项目/键盘 ←→，组件卸载时 dispose；WebGL 不可用时降级为网格卡片 |
| `web/src/vt.ts` | 页面切换过渡助手：`startVt(fn)` 包一层 `document.startViewTransition`（特性检测 + prefers-reduced-motion 兜底），App.vue 的 page 切换/打开项目走它 |

## 改造步骤（按依赖顺序，每步构建可过）

**第 1 步 · 地基**：新建 rhine.css + theme.ts，main.ts 引入 css；index.html 加 `<html data-theme="night">` + 首帧前恢复内联脚本（防闪色，默认落在月影）；`web/package.json` 新增 `three`、`misans` 依赖；App.vue 的 `n-config-provider` 从硬编码 darkTheme 改绑 theme.ts 的 computed；`createDiscreteApi` 的 configProviderProps 改成响应式跟随主题。

**第 2 步 · HUD 外壳**：App.vue 导航区改成四角构图——左上「帧述」品牌字 + `.micro` 副标（如 ZHENSU · VIDEO STUDIO）、右上 ThemeCapsule + 全大写导航（首页 HOME/风格库 LIBRARY/设置 SETTINGS）、底部一条 `.micro` 状态栏（左 ●SYS ONLINE + 当前项目名，右 用户名 + tabular 时钟），header/footer 定 `view-transition-name`；页签切换和项目打开包上 startVt。AccountMenu/登录后下拉换 Rhine 皮。

**第 3 步 · 首页**：HomeView 重写——顶部问候 + hairline，中部 ProjectShelf 玻璃书架（最近项目），底部箭头+刻度滑尺+零填充计数控制条，下方保留「新建项目/逛风格库」module-card 网格（gap-px 网格缝 1px 分隔线 + 左侧强调刻线 hover + 错峰 rise 入场）；NewProjectModal 换直角玻璃皮。

**第 4 步 · 工作区三栏**：HistoryRail → `.side-item` 家族（激活左侧 2px 强调刻线展开）；StagePanel 页签行 → RhineTabs 式零填充编号页签（01 想法…06 视频），保留红绿灯/过期点/运行灯四态 dot（换 `--ok/--wait/--run/--stale` 令牌色）；Idea/MusicIdea/Manuscript/Style/Prep/Video 六个页签 + ChatPanel 对话栏全部换令牌：按钮换 `.btn` 方角、卡片换 `.panel`、状态胶囊换方角+微标。

**第 5 步 · 复杂部件**：Timeline（时间轴标尺换 `.ruler` 刻度风格、轨道色走令牌、播放头换 3px 强调色竖针）；ActivityPane（事件行换 `.data-row`、任务卡换 `.panel`、token 账本换 `.num`）；分镜 inline 表格（发丝线边框 + tabular 数字）。

**第 6 步 · 其余页面**：LibraryCenter/LibraryPanel/RefVideos/TemplateDetail（风格卡片换 module-card 语法、详情弹窗换 dialog-panel）、LlmSettingsPage（表格/表单换 .field/.data-row）、LoginView（抄 yuca 登录页：grid-drift 网格漂移背景 + 顶部光带 + 左品牌右表单）。

**第 7 步 · 收尾扫荡**：全局 grep 残留 hex 字面量清零（令牌化）；所有 `:deep()` 覆盖里假设暗色的规则复查；两套主题下逐页过对比度；`prefers-reduced-motion` 兜底确认。

## 验证

- `vue-tsc --noEmit` + `vite build` 过。
- 起 `npm run dev`（5173，代理 8080），用浏览器逐页检查：登录页、首页书架（拖拽/点击开项目/主题联动）、工作区六页签、时间轴、过程页、风格库、设置页；每页在晨雾/月影两主题下各看一遍，截图核对。
- 明确不动的东西：后端 Go、api.ts/types.ts/segtime.ts、SSE 与制作流程逻辑、`:key="current"` 重挂载机制、publishPack 等业务回调。

## 风险与对策

- **Naive UI 亮色主题**：现有 `:deep()` 覆盖全按暗色写，第 6-7 步统一审计；themeOverrides 把 Naive 的 surface/border/text 全映射到令牌，理论上一处生效。
- **day 主题对比度**：状态色（绿/红/蓝）在亮底上要压深，令牌里按主题分两档定义，不共用。
- **three.js 体积**：只 import 用到的核心模块，不做按需优化（本地工具，可接受）。
- **服务端部署**：dist 需重新 `npm run build` 才落到 Go 静态目录（8080 才能看到新 UI），验证阶段先走 5173 dev。