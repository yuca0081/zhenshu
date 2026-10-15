# 分镜稿样例：同一文稿，两种风格（2026-10-15 续讨论配套）

> 配套 [production-architecture-discussion.md](production-architecture-discussion.md) 第六节（准备阶段三步走：文稿 → 风格 → 分镜）。核心展示：**纯文字稿唯一，分镜稿随风格重排**——段落、切段、缝型（`seam_in`）、场景规划全部是风格相关决策，发生在人工门之后。样例中 `//` 注释仅为讲解，真契约文件是严格 JSON。

## 共同输入：纯文字稿（①文稿工位产出，门前冻结）

| # | 旁白（口语化改写后） |
|---|---|
| N1 | 1935年，薛定谔为了嘲讽量子力学，设计了一个思想实验：把一只猫关进盒子，盒子里有一套由量子事件触发的毒气装置。 |
| N2 | 在你打开盒子之前，这套装置处于什么状态？按照哥本哈根诠释，答案是：既没触发，也没没触发。 |
| N3 | 也就是说，盒子里的猫，同时是活的，又是死的——两种状态叠加在一起。 |
| N4 | 数学上，这叫波函数。它不是猫的状态，而是所有可能性的加权清单，每一种可能性都有一个系数，叫振幅。 |
| N5 | 只要没有人看，这份清单就会按照薛定谔方程演化，叠加着，纠缠着，仿佛宇宙对「到底发生了什么」保持了沉默。 |
| N6 | 直到你打开盒子——测量的那一刻，清单瞬间坍缩成一个确定的结果。 |
| N7 | 猫要么活，要么死，各有一半的概率。但坍缩为什么发生、怎么发生，物理学家吵了快一百年。 |
| N8 | 所以薛定谔的猫从来不是关于猫的，它是在问：「观测」到底是个什么东西？这个问题，至今没有公认答案。 |

## 风格卡：剪辑语法（②风格工位新增字段，随样张过人工门）

| 字段 | 风格 A · 羊皮纸手稿风 | 风格 B · 水墨连贯风 |
|---|---|---|
| 切分密度 | 密：一段一"页"，单段 6–10s | 疏：长镜头，单段 12–18s |
| 缝型倾向 | cut 为主；段落间机械翻页转场 | match 为主；fade 做情绪缓冲 |
| 同舞台延续 | 弱（页与页天然独立） | 强（一个水墨舞台讲到底） |
| 段长带（校验用） | 6–10s | 12–18s |

（全局钳制仍适用：段长 5–20s，保渲染与审核经济性。话题完整性 > 风格密度：话题没讲完，风格再想切也不能切。）

## 版本 A：羊皮纸手稿风（8 段 · 缝型 5×cut + 2×翻页 fade + 1×cut 收束）

```jsonc
{
  "style": "parchment-manuscript",
  "paragraphs": [
    { "title": "提出问题：盒子里那只猫",   "segs": ["seg01", "seg02", "seg03"] },
    { "title": "波函数：可能性的加权清单", "segs": ["seg04", "seg05"] },
    { "title": "测量与百年争论",           "segs": ["seg06", "seg07", "seg08"] }
  ],
  "segments": [
    { "id": "seg01", "idx": 1, "key": "第一页 · 实验装置",
      "narration": "N1",
      "visual_brief": "羊皮纸底，墨线勾勒盒与猫的插图页，页首花体标题",
      "duration_hint": 9 },
    { "id": "seg02", "idx": 2, "seam_in": "cut", "key": "第二页 · 状态之问",
      "narration": "N2",
      "visual_brief": "翻到新页：问题手写体居中，旁注小图标（开/关的盒盖）",
      "duration_hint": 8 },
    { "id": "seg03", "idx": 3, "seam_in": "cut", "key": "第三页 · 叠加态",
      "narration": "N3",
      "visual_brief": "新页插图：猫的墨线轮廓与半透明重影并列，页角红蜡封印",
      "duration_hint": 8 },
    { "id": "seg04", "idx": 4, "seam_in": "fade", "key": "翻页 · 波函数",
      "narration": "N4",
      "visual_brief": "翻页过渡后新章：羊皮纸上绘制坐标网格与波函数曲线，墨色加深处方体标注",
      "duration_hint": 9 },
    { "id": "seg05", "idx": 5, "seam_in": "cut", "key": "演化清单",
      "narration": "N5",
      "visual_brief": "同页延续：曲线分作两股墨线缠绕，边角沙漏插图",
      "duration_hint": 8 },
    { "id": "seg06", "idx": 6, "seam_in": "fade", "key": "翻页 · 测量",
      "narration": "N6",
      "visual_brief": "翻页过渡：新页盒盖大开插图，一道金墨从缝里泻出",
      "duration_hint": 7 },
    { "id": "seg07", "idx": 7, "seam_in": "cut", "key": "百年争论",
      "narration": "N7",
      "visual_brief": "新页：左右分栏各一行墨字（活/死），天平插图居中",
      "duration_hint": 9 },
    { "id": "seg08", "idx": 8, "seam_in": "cut", "key": "尾页 · 提问",
      "narration": "N8",
      "visual_brief": "末页大字提问居中，页脚落款与年份",
      "duration_hint": 10 }
  ]
}
```

## 版本 B：水墨连贯风（6 段 · 缝型 3×match + 2×fade + 1×cut）

```jsonc
{
  "style": "ink-continuous",
  "paragraphs": [
    { "title": "提出问题：盒子里那只猫",   "segs": ["seg01", "seg02"] },
    { "title": "波函数：可能性的加权清单", "segs": ["seg03"] },
    { "title": "测量与百年争论",           "segs": ["seg04", "seg05", "seg06"] }
  ],
  "segments": [
    { "id": "seg01", "idx": 1, "key": "开场 · 盒中之猫",
      "narration": "N1, N2",
      "visual_brief": "水墨留白舞台：一猫一盒居中，浓墨勾盒、淡墨写猫，题跋落款",
      "duration_hint": 15 },
    { "id": "seg02", "idx": 2, "seam_in": "match", "key": "叠加显形",
      "narration": "N3",
      "visual_brief": "同一舞台推近：猫化作浓淡两团墨影交叠（首拍延续上段末拍构图与墨色）",
      "duration_hint": 10 },
    { "id": "seg03", "idx": 3, "seam_in": "match", "key": "波函数长卷",
      "narration": "N4, N5",
      "visual_brief": "墨影化入一条水墨长卷：波函数曲线如墨龙蜿蜒，两股缠绕、渐淡入虚（延续上段末拍墨势）",
      "duration_hint": 16 },
    { "id": "seg04", "idx": 4, "seam_in": "fade", "key": "测量一瞬",
      "narration": "N6",
      "visual_brief": "墨色晕染缓冲后：一笔重墨劈开长卷，盒盖意象重现，留白骤然扩大",
      "duration_hint": 10 },
    { "id": "seg05", "idx": 5, "seam_in": "match", "key": "坍缩定格",
      "narration": "N7",
      "visual_brief": "同一舞台：墨影凝为实猫一态，浓墨定格（延续上段末拍）",
      "duration_hint": 12 },
    { "id": "seg06", "idx": 6, "seam_in": "cut", "key": "收束 · 天地问",
      "narration": "N8",
      "visual_brief": "硬切大远景：全卷收入画框，一行题问悬于留白之上",
      "duration_hint": 14 }
  ]
}
```

## 对比要点

| 维度 | 羊皮纸版 | 水墨版 |
|---|---|---|
| 段数 / 均长 | 8 段 / ≈8.5s | 6 段 / ≈13s |
| 缝型分布 | cut 6 · fade(翻页) 2 | match 3 · fade 2 · cut 1 |
| 场景规划 | 页页独立，翻页即转场 | 一个水墨舞台贯穿，首末拍成对设计 |
| 审核考卷差异 | 查"该翻页处翻页、该切处切" | 查 match 缝首末帧接得住、fade 晕染自然 |

叙述文本完全一致，分镜稿完全不同——这就是"剪辑语法"字段的机制含义。时长校验按各自段长带执行（A：6–10s；B：12–18s），全局钳制 5–20s 兜底。
