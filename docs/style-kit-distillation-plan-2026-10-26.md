# 风格画材包·蒸馏工位方案（style kit distillation）

> 2026-10-26 定稿讨论收敛。状态：**第 0/1 步已完成（见 §8 落地表），第 2/3 步未实施**。
> 起因：meme67-art-history 仓库（github.com/Tunai-0511/meme67-art-history）的启发 + demo/p5brush-sample 四画风矩阵实测。
> 实施拍板后须同步更新 docs/production-architecture-v3-interactive.html。

## 0. 一句话

风格库从「只存形容词和真迹」升级为「**还存可执行画材**」：给一个 Docker 容器、一个大模型画师、一段视频，让它自由选型用代码还原关键画面，交付一个结果包（标签/特色/框架/代码/样图/依赖/笔记）入库；制作期画面工位从「每次从零造笔画一幅画」变成「用现成画材摆画面」。

## 1. 背景与动机

### 1.1 平台现状缺口

- 风格库 v3（server/internal/stylelib/stylelib.go）每条目存：标签+打标证据、特色 Signatures（**机制描述是纯文字**）、分面、题材（仅检索）、使用统计；媒体只有封面+真迹帧。**关于"怎么画"的全部知识都是文字，零可执行资产**。
- 画面工位每段从零写裸 canvas：风格全靠提示词描述，笔触/纹理函数现造——token 贵、风格不稳、质感通道太完美（"像素感缺""生硬"的病根之一）。
- 平台为什么停在裸 canvas：三分模型能力（小众画图库不在语料里），七分架构经济学——依赖冻结方针没有框架入口、token 经济学排斥注入库文档、场景契约/lint 按裸 canvas 习惯写、QA 只判内容不判质感（无质量信号回路）。

### 1.2 meme67 的启示

- 17 场景全代码逐帧画，无外部素材；共享 30KB kit.js（笔刷工厂+水彩/排线/流场原语+烘焙缓存+2D 图层混合），子代理只写构图；两份简报制（总纲 STYLE.md + 2KB 单场景硬参数简报）；纪律=确定性随机禁令+逐帧看 PNG 至少迭代 3 轮。
- 画质配方=分层架构：底漆层（2D 渐变+暖色地，防光谱混色变绿）→ 解析流场+明度场查 10 档色阶 → 四层笔触分工（底/主/高光/暗缝，约 5000 笔）→ 短搭笔轮廓 → 场景纵深 → 绢底 fbm+逐像素墨色 → 做旧三层 → 颗粒+暗角收尾。
- 其子代理拿的就是 shell+文件读写，纪律全写在简报里——**工具极简、纪律在提示词**，与本方案的 agent 设计一致。

### 1.3 已验证资产（demo/p5brush-sample/）

- 四画风矩阵（梵高星夜/北宋水墨/浮世绘/铜版蚀刻）全部多模态验收通过；每张一颗种子重跑逐像素一致。
- 本机 headless Chrome + `--use-angle=swiftshader --enable-unsafe-swiftshader` 跑 p5 2.3.4 + p5.brush 2.2.3（WEBGL）实测可行。
- p5.brush 2.2.3 五个隐性坑（画材包套件层必须挡掉）：

| # | 坑 | 症状 | 对策 |
|---|---|---|---|
| 1 | `brush.add()` 的 `pressure` 隐性必填 | 不传则渲染期读 `pressure.type` 直接炸（源码 stroke.js:387），README 未写 | 自定义笔必带 pressure |
| 2 | 流场激活后不自动关 | spline/circle/flowLine 被涡流"吹跑"（树干甩进田里）；line/polygon 不受影响 | 用完立刻 `noField()` |
| 3 | **`brush.spline()` 传两个点=静默不渲染** | 与笔型/长度无关（矩阵探针铁证：两点 spline 全灭，line/三点 spline/flowLine 全活） | 两点笔触走 `brush.line` |
| 4 | `wash()` 拿当前画笔给填充多边形描边 | wash 前不 noStroke，彩笔会把整圈轮廓+底边全勾出来 | wash 前 `noStroke()` |
| 5 | 'ink' 不是内置笔 | 借 kit.js 的 flush 技巧报 Brush not found | 用内置 'pen'（pen/rotring/2B/HB/2H/cpencil/pastel/crayon/charcoal/spray/marker） |

另：npmmirror 的 unpkg files API 无白名单，拉包走 `npm pack --registry=https://registry.npmmirror.com`。

## 2. 总体架构

三个角色（沿用毛坯房比喻）：

```
┌─ 监工（Go，宿主，prep/music.go 同套路）────────────────┐
│  起容器 → 递考卷袋 → 循环转发命令(docker exec) →        │
│  收图喂多模态 → 掐表/数轮次 → 收卷 → 出料过滤           │
└───────────────┬──────────────────────────────────────┘
                │ docker exec / out/ 文件夹
┌───────────────▼─ 毛坯房（一次性 Docker 容器）──────────┐
│  画师的手：bash / 写文件 / 读文件 / grep               │
│  房间里的脚本：render.mjs（渲染）/ judge.mjs（判卷）/   │
│  sheet.mjs（拼九宫格对照片）/ explore.sh               │
│  每单自由 npm install（探索自由，lockfile 即账单）      │
└───────────────────────────────────────────────────────┘
   画师的大脑（LLM）在门外：API key 永不进房间
```

- **大脑在门外**：agent 循环住在 Go 服务器（与四工位同架构），LLM API key 是最值钱的钥匙，绝不进沙箱；模型经 docker exec 操控房间。
- **眼睛**：渲染/拼图在房内跑，PNG 落 `out/`，门外监工取图喂多模态模型。
- 拔网线只拔房间自己的网：断掉下载代码外传的路，大脑照常思考。

## 3. 流水线

1. **解析入库（现有+小动作）**：照常出标签/简报/真迹帧，真迹帧打「待蒸馏」标记。**不自动蒸馏**——懒触发，风格快常用了才开单。
2. **蒸馏单（新增）**：见 §4。
3. **毕业检查单（薄，先人工后脚本）**：结果包可先入库**存放**，但画面工位使用前必须过三条：① 版本钉死（@latest 不许进）；② 依赖下载 vend 存档；③ 许可证过目（GPL 类商用小心）。跑顺后加第 ④ 条：冻结版重跑判卷确认不掉分。**制作管线永远只见冻品。**
4. **制作消费**：画面工位提示词注入画材包小抄（cheat_sheet），从「从零画一幅画」降级为「用现成画材摆画面」；裸 canvas 保留为兜底。

## 4. 蒸馏单设计

### 4.1 Agent 能力（刻意极简）

只给四样通用能力：**跑命令、写文件、读文件、搜文本**；本体用多模态模型（读 PNG 即看画）。**不造任何专用工具**——工具白名单是生产工位（无沙箱、宿主直跑）的正确做法；沙箱里容器本身就是墙，再叠工具闸门属于重复设防，且自造接口模型不熟易出错。

写进提示词的三条规矩（一句话级，不是架构）：

1. **预算上限**：最多迭代 N 轮、每轮时限；到限必须交当前最好版本（带伤交卷，标还原分）——照搬音乐工位「封顶 3 次带伤交门」。
2. **必须看图**：每轮看一张九宫格对照片（真迹 3 格+自渲染数格拼一张图，一轮一次多模态调用，token 可控）；交付笔记要引用亲眼看到的像素证据——防盲画。
3. **还原效果不还原内容**：判卷盯风格特征（笔触方向/色阶/质感/颗粒），不做逐像素比对（不逼它抄画）。

### 4.2 判卷与换题考试

- 判卷=正式打分（judge.mjs，真迹对照，风格特征维度），与画师自查（看对照片）分离。
- **换题考试（防描红）**：及格后用标准中性测试图（圆+方块+波浪）让画材包再画一次——真画法换什么题都有那味儿，描红换题就露馅。人眼看一眼即可起步，不必做闸。

### 4.3 交付包清单（8 样）

风格标签 / 特色描述（可升级为参数版：形容词→精确色值+笔触参数）/ 技术框架（用了什么+为什么）/ 样例代码 / 还原样图 / 换题样图 / 依赖清单（精确版本）/ 配方笔记（≤14 行：路线、已知弱点）。

## 5. 沙箱实现

### 5.1 选址与资源

- 云服务器（已装 Docker 29.9，Ubuntu，4 核 3.6G 内存，**生产数据库同机**）。
- 容器限 `--memory 2g --cpus 2`；**并发=1，多单排队**；错峰优先（夜里）。
- 本机（Windows）不装 Docker，不参与。

### 5.2 Dockerfile（地基，一次 build 反复用）

```dockerfile
FROM node:24-slim
# 无头浏览器+抽帧+中文字体（判卷要看得清画面里的字）
RUN apt-get update && apt-get install -y --no-install-recommends \
      chromium ffmpeg fonts-noto-cjk \
    && rm -rf /var/lib/apt/lists/*
RUN npm config set registry https://registry.npmmirror.com
# 烤进地基的工具脚本：渲染/判卷/拼对照片（改脚本=升版本号重建）
COPY tools/ /opt/distill-tools/
USER node
WORKDIR /task
```

要点：浏览器烙在地基（puppeteer-core 指系统 chromium，不每次下 150MB）；工具脚本属地基（每单工具版本也钉死）；**agent 每单自由装的画材包不进 Dockerfile**（运行时房内 npm install）。

### 5.3 运行节奏

```bash
# 烙地基（低频；脚本变了才 :1→:2）
docker build -t distill-base:1 /srv/distill/

# 每单开房（监工执行的就是这条）
timeout 900 docker run --rm -i \
  --memory 2g --cpus 2 \
  --cap-drop=ALL --security-opt no-new-privileges \
  --user 1000:1000 \
  -v $TASK/in:/task/in:ro -v $TASK/out:/task/out:rw \
  distill-base:1 bash /task/in/explore.sh
```

- **一单一房，用完烧（--rm）**；一单内多轮迭代在同一容器，到点杀掉。
- **承重墙三规矩**：① 屋里不放任何密码钥匙（DB 连接串/SSH key 绝不进容器 env）——恶意包最坏只能烧电费；② 用完销毁不复用；③ 交接只走 `out/` 小窗口，宿主侧过滤器只放行 `.js/.json/.png/lock/.md`，单文件+总量设上限。
- **npm 默认 `--ignore-scripts`**（画图库九成纯 JS 不需要构建脚本，挡掉大半投毒面）；真需要脚本再单独放行，容器兜底。
- **网络分段（可选加固）**：装包期联网，干活期可 `--network none` 断网渲染——大脑在门外不受影响。
- 一期不共享 npm 缓存卷（干净优先，npmmirror 已够快）；排队排爆=迁独立按小时计费机器的信号（三期）。

## 6. 风格库 schema 扩展

贴着现有 `Style` struct 长，不另起炉灶；未蒸馏条目 Kit 为空，新老互不干扰：

```go
type StyleKit struct {
    Framework  string          // 用了什么框架 + 一句话为什么（选型本身是风格知识）
    Deps       []StyleKitDep   // {name, version(精确), license}
    Files      []string        // 画材包代码，style_media/<id>/kit/ 下，账本只存文件名（URL 永不入库）
    CheatSheet string          // API 小抄（≤~2k token）——画面工位提示词真正注入的东西，对标 TagBrief 角色
    Score      StyleKitScore   // {reproduce 还原分, neutral 换题分, rounds 迭代轮数}
    Notes      string          // 配方笔记
}
```

- 媒体目录新增：`repro-*.png`（还原样图）、`neutral-*.png`（换题样图，兼作 QA 对照锚）。
- 项目侧 `StyleBrief` 相应带 kit 引用+小抄。
- **治理复用现成轮子**：新 kit 入库=未验证（`Unverified` 模式），第一个用它出片过审→转正；使用统计闭环照旧。
- **防重蹈 v2 覆辙**：v3 大删 v2 的提取式教学字段，教训是「存一堆没人用的文字」。画材包不犯此病的根据：它是**还原过关才进的可执行代码**，不是提取文本；带伤必标分；小抄要短。

## 7. 成本账

- 一次蒸馏几 M token，**每风格只付一次**；懒触发保证只给快常用的风格付钱。
- 回报：画面工位提示词缩水（2k token 小抄顶替整篇画法描述）+ 一次写对率上升（坑被套件挡掉）+ 质感上限抬升。
- 与 P0 止血不冲突：这是摊销逻辑，花在刀刃上的探索成本。

## 8. 落地顺序

| 步 | 内容 | 说明 |
|---|---|---|
| **0** | 人工出第一份画材包 | ✅ 2026-10-26 完成：条目 vid-ref20261026-103650，demo/stylekit-pixelstage，考题终审 A34/B31.5/C31，三方对比 82 vs 56 验收立住 |
| **1** | 拍板 zhenshu-kit 规范+毕业检查单 | ✅ 2026-10-26 定稿：docs/zhenshu-kit-spec-2026-10-26.md（四类语义 API/五坑封装/去题材化/判卷口径/毕业七查；D1-D3 已拍板，cheat_sheet 正文已入库为证） |
| **2** | 蒸馏工位自动化 | Dockerfile+监工+容器+提示词；懒触发接线解析打标 |
| **3** | 画面工位接入 | 提示词改造+scene_lint 适配+裸 canvas 兜底；与 P2 组件编译器合并设计 |

二期可选：同题多画师并行、择优毕业（best-of-N）。

## 9. 未决问题

- 蒸馏判卷的评分维度权重（笔触/色阶/质感/颗粒）——第 0 步人工校准。
- cheat_sheet 的注入时机与格式（设计阶段 or 画面工位 or 两处）——随第 3 步定。
- kit 与特色 Signatures 的关系（特色升参数版落在哪一侧）——第 1 步拍板。
- MinIO/PG 后端下 kit 文件的组织（沿 style_media 惯例预计无障碍，待接线验证）。

## 10. 关联

- 实验场：`demo/p5brush-sample/`（对比样张+四画风矩阵+五坑实测）。
- 参考仓库：Tunai-0511/meme67-art-history（kit.js/vangogh.js/song.js 精读笔记见会话记录）。
- 同路合并：docs/cost-optimization-plan-2026-10-20.md 的 P2 组件编译器。
- 风格库现状：docs/style-library-v3.md、server/internal/stylelib/stylelib.go。
