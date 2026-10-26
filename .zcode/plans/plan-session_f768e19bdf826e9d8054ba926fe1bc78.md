# 样张阶段字体自装 + 终审依赖清单

## 决策（已与用户对齐）
- 取消字体「申请-人工入库」人工阶段：样张阶段模型可自装任意字体，来源**完全不限**，仅靠现有机械闸兜底（font_tool 的 20MB/zip 64MB 上限、TTF/OTF 魔数校验、3 次重试）。
- 人工从「守门」挪到「验收」：submit_style 后，终审页（PrepPane ②风格包卡片）列出本次自装了哪些库（vend_libs，已有）和哪些字体（新增），字体带协议与来源链接。
- `font_requests` 参数整条退役（模型已知晓改为自装路径）。
- 制作阶段照旧零自采，但**自装字体自动带进制作**（design_system 记账 + 申请书保留 + 段级钳制放行）。

## 声明语法（与 vend 声明同构，复用 font_requests 的「名|协议|URL」格式）
```html
<!-- font: Fusion Pixel 12px Prop zh-Hans | OFL | https://api.github.com/repos/TakWolf/fusion-pixel-font/releases/assets/646862498 -->
```
平台解析 → font_tool 下载（URL 无关性已具备，**font_tool.py 零改动**）→ 机器级缓存 → 按本片文稿子集化 woff2 data URL → 渲染即真字体。缓存键 id=slug(Family)、version=sha256(url)[:12]，同 URL 跨项目缓存命中。

## 改动清单

### 1. contract/types.go
- 新增 `CustomFont{Family, License, URL string}`；`StyleSamples` 加 `CustomFonts []CustomFont \`json:"custom_fonts,omitempty"\``。
- `ValidateStyleSamples`：上限 6 款、三字段非空、按 Family 小写去重。

### 2. pipeline/fontcatalog.go
- `fontDeclRe` + `CustomFontDecls(html)`：解析声明、去重；Family 命中目录或系统白名单时跳过（目录/系统优先，防重复装）。
- `writeFontAssetRequest(p, matched, customs)`：目录匹配与自装条目**合并落申请书**（自装条目带合成 id/version/url/license）；两者皆空才删文件（修复现状：空匹配直接删文件会误杀自装条目）。

### 3. prep/style.go（核心）
- `styleSystem`（:44 区）、`render_sample` 描述（:705）、修复提示词（:1009）：放宽字体约束为「目录字体＋自装声明＋系统字体」，教声明语法；提示「字体必须在样张渲出前装好，判卷才作数」。
- `ensureSampleFonts`（:596-618）重写：
  - 需求集 = `MatchSampleFonts(htmls)` ∪ 各 HTML 的自装声明；
  - 早退条件从「font_assets.json 存在」改为「需求全部已 resolved」（顺带修复现状潜伏缺口：后渲样张新增目录字体时永不补装）；
  - 目录字体失败保持静默降级记账（现有语义）；**自装字体失败返回失败清单**；
  - manifest：`prep.style_fonts`（目录，现有）、新增 `prep.style_font_custom`（每款「样张自装字体 Family (协议) ← url」）。
- render_sample Run（:776-781）：自装失败 → Observation 回喂「换来源重试 / 换目录字体后重新 render_sample」，与 vend 失败同款话术。
- submit_style（:1255-1367）：
  - 删 `font_requests` 参数/落盘/manifest `prep.font_request`/回显（:1255-1258, 1277, 1307-1314, 1365-1367）；
  - `ds.CustomFonts = 自装声明提取`（:1306 区，与 VendLibs 并排，去重排序）；
  - 字体闸（:1319-1325）改传 `pipeline.LoadSampleFontAssets(r.p)`——修复现状 `UnresolvedDesignFonts` 内部 `assets=nil` 误杀；
  - 回话改为「自装字体：N 款（…）」。

### 4. pipeline/samplecheck.go
- `UnresolvedDesignFonts(ss, assets)` 加 assets 参数（`fontFamiliesUsable` 本就支持，只是没传）。全仓唯一调用点在 prep/style.go:1320。

### 5. pipeline/design.go（制作携带链，三处坑的修复）
- `clampFontStack(stack, matched, custom map[string]bool)`：自装 Family 放行保留、`cjkSource=true`（假设注明：自装即服务于中文画面）；`clampDesignFonts` 从 `ss.CustomFonts` 构造集合——修复坑①：LockDesign 钳制剥掉自装字体。
- `LockDesign`（:708-717）：`writeFontAssetRequest(p, matched, ss.CustomFonts)` 合并写——修复坑②：申请书被目录匹配整写覆盖/误删。
- `ClampSegmentFonts`（:806-820）：新增 `loadCustomFontSet(p)`（读 style/stylepack.json，fallback style_samples.json 的 custom_fonts）传入钳制——修复坑③：制作段帧把自装族剥掉。3 个调用点（segcheck.go:246,339；segpipe.go:938）签名不变。

### 6. 前端（终审清单）
- `web/src/types.ts`：StyleSamples 补 `fonts?`（三角色 stack/weight/tracking）、`vend_libs?: string[]`、`custom_fonts?: {family,license?,url?}[]`。
- `web/src/components/PrepPane.vue` ②风格包卡片（grammar 行后 ~:130）插「自采依赖」区：
  - 库：`<code>` chips 列 vend_libs（无则「无」）；
  - 字体：fonts 三角色栈拆 token 成 chips ＋ custom_fonts chip 带协议标签、url 渲染 `<a>` 源链接；
  - 复用 fact_sheet 的 `<details>` 折叠模式，条目多时收起。

### 7. 过程页小补（顺带）
- `ActivityPane.vue` 事件 switch（:134-254 现被 default 丢弃）加 case：`prep.vend` / `prep.style_fonts` / `prep.style_font_custom` / `prep.style_font_degrade` 渲染为普通行——安装过程在过程页也可见。

### 8. 测试
- 新 `server/internal/prep/style_font_test.go`：声明解析（正常/去重/被目录遮蔽/格式错）、ensure 覆盖判断（已装跳过、新族补装）、submit 提取 CustomFonts、字体闸带 assets 放行自装族。
- `fontclamp_test.go` 扩：clampFontStack 保留自装族；LockDesign 申请书合并不丢自装条目；ClampSegmentFonts 放行自装族。
- 前端 `npm run build` 过类型。

### 9. 文档与提交
- 事实源文档（prep-agents-design.md / production-stage-design.md 相关段）把「字体申请-人工入库」改为「样张自装+终审披露」，删 font_requests 描述。
- 提交信息按仓库惯例中文 feat 单提交。

## 验证路径
1. `go test ./...` 全绿。
2. 真机冒烟：起 server，走一个样张项目，样张 HTML 写自装声明（用 fusion-pixel 的 release URL 当「非目录来源」验证下载/子集/渲染），观察：首渲真字体、manifest 记 `prep.style_font_custom`、终审页出现「自采依赖」清单（库+字体+协议+源链接）、confirm 后制作段帧不被钳制剥字体。

## 已核实的边界
- font_tool 下载分支已按 URL 形态分流（release assets zip 走 octet-stream+解包取 zh_hans；其余 vnd.github.raw），任意 URL 天然可下，零改动。
- 失败语义：自装失败=响亮回喂模型当场换；目录失败=静默降级记账（现状）；两不挡确认门，终审页如实披露。
- 探查 agent 报的 vendor_tool `shutil` 未导入为误报，已亲自核原文（:23 有 import），不修。