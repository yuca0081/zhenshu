// design.go —— v2 笼子二「设计系统先行」（plan-v2 §4）：
// 模型通读全部分镜，为本片发明设计系统（皮肤×1-2 / 字体三角色 / 动效语言 /
// 贯穿装置 / 版式网格 / 负面清单），样张是它的视觉预览。证据全部来自
// docs/v2-analysis 11 份爆片逐帧评审：
//   - 高分来源是「项目级设计系统」不是模板——模型现场发明约束然后遵守（§1.2 发现 2）；
//   - 决定档次的是动效原理与时间表工程，不是代码行数（三模型对比评审）；
//   - 字体三角色分工 + 字距普遍拉开（穆阿蒂布评审）；
//   - 贯穿装置逐段变奏正是风格本身（列LEN访客卡×5 / 西西弗斯单版式状态机）；
//   - 双皮肤按乐段交替（穆阿蒂布黑霓虹×米色图版）。
//
// 载体：tokens 本体 = style/style_samples.json（不另立 tokens.json，防双源漂移）；
// 确认后机械渲染出 style/design.md（人读 + 门2 + scene_gen 注入源）与
// style/skin.json（引擎件，主皮肤，assemble 字幕带配色消费）。
package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"path/filepath"
	"webpage-video-agent/server/internal/contract"
)

const designSystemPrompt = "你是视频视觉总监。先通读全部分镜，为这一部片发明一套设计系统，全片每一段都遵守。只输出 JSON。"

// LoadStyleBrief 读项目风格简报（style/style_brief.json，v3 §4.3）。
// 不存在/损坏返回 nil——nil = 没选库内风格，设计阶段裸发明（合法常态）。
func LoadStyleBrief(p *Project) *contract.StyleBrief {
	b, err := os.ReadFile(p.Artifact("style/style_brief.json"))
	if err != nil {
		return nil
	}
	var brief contract.StyleBrief
	if json.Unmarshal(b, &brief) != nil || brief.EntryID == "" {
		return nil
	}
	return &brief
}

// designBrief 设计作业正文（API 直调与会话请求共用一份，防两处漂移）。
// input/style_notes.txt 存在时追加用户修改要求（draft_style_samples 带 instruction 写入）。
// 风格简报存在时追加「强参考」注入块（v3 §4.5）：气质基调（词表 brief）+ 必答特色清单。
// 样张画布硬定 960×540：舞台 SampleFrame 缩放与 ai/snap_samples.py 截图都按此假设。
func designBrief(p *Project, sb *contract.Storyboard) string {
	notes := ""
	if b, err := os.ReadFile(p.Artifact("input/style_notes.txt")); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		notes = "\n\n## 用户修改要求（优先满足；未提及的方面保持连贯，不要推倒重来）\n" + string(b) + "\n"
	}
	return fmt.Sprintf(`你是视频视觉总监。先通读下面**全部段落**，然后为这一部片发明一套设计系统——像高分爆款工作室那样「为本片现场发明约束，然后全片遵守」。

## 铁律
- 系统是**发明**的，不是从风格库选一个套上。默认审美（绿 chip 黄底、大面积辉光、金句五件套）出现即失败，除非 concept 里明确论证它就是本片语言。
- 每个决定都要能回答「为什么配这部片」——写进 concept。
- 设计要在渲染物理内可执行：可补间属性只有 opacity/x/y/scale/rotation 系（数字滚动用 count 工具）；禁 CSS 动画/外链/视口单位。
%s
## 全部分镜（设计必须从这些内容里长出来）
%s
%s
## 设计系统六件套（缺一件会被校验打回）

### 1. concept 概念
一句话：本片视觉概念是什么、为什么这套系统配这部片（从分镜气质推导，不许套话）。

### 2. skins 皮肤（1–2 套）
- 必填槽位：bg 底色 / txt 文字色 / accent 强调色（#RRGGBB；txt 落 bg 上对比度必须 ≥4.5:1，实测校验会拒）/ font 字体栈（中文字体打头）。
- 可选槽位：mute 次级文字 / surface 卡片底 / panel_txt 面板字 / radius 圆角0–40 / border_w 描边0–6。
- css 自由 CSS（≤4000 字符）：纹理/质感/装饰/特效底——风格差异化的主战场，认真写。
  挂点 .skin-root（根容器）、.skin-paper（满画幅背景层）；CSS 变量 var(--bg/--txt/--accent/--mute)。
  动效质感用静态多层渐变/噪点/边框模拟（暗角、纸纹、网格、光斑、色带）。
- 双皮肤（加分项）：两套气质相反的皮肤 + skin_rule 写清切换规则（哪类段/哪个节拍穿哪套，可机械执行）。单皮肤省略 skin_rule。

### 3. fonts 字体三角色（禁一栈到底）
- display 大标题/大数字：重字重 + 字距拉开（letter-spacing 0.04em 级）；
- body 正文/说明：可读优先；
- data 数据/标签/代码：等宽优先，栈尾必须带 CJK 字体回退（数据标签也有中文）；
- 每个角色的栈都必须含中文字体（渲染端是本机字体，豆腐块=废片）。
- display 可以点名目录字体（平台自动下载+按本片用字子集化，写全名即可，中文名也认）：
  %s——栈形如「'目录字体','Microsoft YaHei',sans-serif」；目录外的花名（PingFang/思源/方正等）
  渲染端没有，一律不准写。

### 4. motion 动效语言（全片节奏一致性的来源，档次分水岭——生动还是像PPT，在这里定档）
可补间只有 opacity/x/y/scale/rotation 系（数字滚动用 count）。动效必须设计成**一套编排**，不是一个档位：
- entrances 入场手法菜单 3–5 条：每条 name/how/use_for 三件齐，手法之间必须一眼不同（参考族：逐行点亮/滑入揭示/字卡打字机/缩放落定/计数滚动/描边生长——参考族只是例子，按本片气质现场发明优先）；只交一种「淡入+浮入」=整片像PPT，打回；
- rhythm 段内节奏下限：旁白每推进一个信息点，画面必须有一次可见响应（新元素入场/已有元素强调/运镜变向）；写出量化档（如「每 3–5 秒一个可见事件」）；入场完成后静坐到段尾=失败；
- exits 出场：该退的元素在旁白收尾前退完，怎么退（淡出/反向滑出/缩小回收）写成规矩，禁拖尾；
- camera 运镜：是否持续运镜、什么幅度，按本片气质决定并写清依据——需要呼吸感的片可以设计安静段（定格/微幅呼吸也是运镜决策），但「全片没有一处可感知运动」仍是失败；
- transition 段间转场政策：发明一个配本片气质的招牌转场（简报含转场类特色时必须按其机制实现）；转场手法本体画进各段场景文件（上段收尾/下段开场自己编排），机械拼接层只认三种出口——措辞明说「黑场/闪黑」会用页内黑场叠化实现，明说「叠化/交叉」会用交叉叠化拼接，其余一律直切（段间节奏全靠你的场景编排接管）；「黑场一瞬」只在气质确实需要时用且须在 notes 里论证——它是观众感知里的「翻页」，滥用=PPT；
- 全片所有段从这套菜单取用并轮换，段间节奏一致性靠统一档位（ease/时长档）而不是靠「都一样呆」。

### 5. device 贯穿装置
- 全片反复出现、随内容变奏的招牌画面机制——观众记住这部片靠它（参考形态：同一张卡片每章亮相、单版式状态机）。
- per_seg 对着上面分镜逐段写：它怎么重复/变奏/演变。

### 6. layouts 版式（3–5 个）与 anti_patterns 负面清单（≤8 条）
- 版式按本片内容与气质**现场发明**构图（大字海报/主视觉分栏/数据焦点/满幅底图只是参考形态，不是菜单）；每个版式构图明显不同，段轮换用；
- 叠字可以是有意手法，但必须声明：若某版式的层次感依赖文字叠压（大字衬底词/标徽压引言/水印字），在该版式 layered 字段写明叠压关系与可读依据（衬底层低对比度、前景层带实底衬/描边）。没有叠层构图的版式禁止写 layered——实色字压实色字的「叠」不是手法是事故，不配声明；
- 负面清单 = 本片禁用的俗手，按本片内容挑真实风险（右半空版式/假密度/文字压图不可读……）。
- post 全片后期质感（可选，0–1）：暗角 vignette / 胶片颗粒 grain / 轻微色差 chroma。默认全 0 = 不加后期（干净的画面是正当选择）；只有本片气质确实需要这层质感才开（怀旧/胶片感可开颗粒，干净的 UI/科技风开了反而脏）。

### 7. entry_exit 标准入场/收尾动画模板（转场的载体，确认校验硬性要求）
- entry：本风格的标准入场动画一句话机制（如「水墨晕染显影，主体自中心洇开 0.4s」）；新场景段开场走它；
- exit：标准收尾动画一句话机制（如「整帧淡出收黑 0.3s」）；每段收尾走它；
- entry_dur：入场预算秒数（0.2–1.0，缺省 0.4——计入段长带）；
- 一套模板一段内生效，禁止逐段即兴发明转场手法（段间衔接的内容依据由分镜的首拍/末拍承担）。

## 样张 = 用主皮肤渲出的静态小画面（所见即所得，1–3 张）
- 颜色/字体必须来自主皮肤槽位（色值逐字一致）——样张即皮肤预览，两处不一致会被打回；
- 不用图片/网络资源，纯 CSS；多张样张构图版式必须明显不同，每张 desc 写明用了哪个版式；
- 画布硬约束（违反必被裁切）：每张 html 是完整自包含文档，画布固定 960×540：
  html,body{width:960px;height:540px;overflow:hidden}；内容全部落在画布内，不依赖滚动；
  禁 vw/vh；大字标题留边距；每张 html ≤2000 字符%s`, briefBlock(p), storyboardDigest(sb), directorDesignBlock(sb), FontCatalogList(), notes)
}

// briefBlock 强参考注入块（v3 §4.5）：简报存在时给设计阶段立「用户选定的硬要求」。
// 特色只给机制（物件换成本片内容——吸收机制而非临摹原片），落点登记要求随后由
// designJSONShape 的 signature_absorption 字段承接。
func briefBlock(p *Project) string {
	brief := LoadStyleBrief(p)
	if brief == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## 本片风格参考（用户选定，硬要求）\n")
	if tb := tagBriefLine(brief, brief.Tags.Main); tb != "" {
		fmt.Fprintf(&b, "气质基调：%s", tb)
	} else {
		fmt.Fprintf(&b, "气质基调：%s", brief.EntryName)
	}
	for _, aux := range brief.Tags.Aux {
		if tb := tagBriefLine(brief, aux); tb != "" {
			fmt.Fprintf(&b, "；辅：%s", tb)
		}
	}
	b.WriteString("\n- 吸收的是机制不是物件：参考风格的题材/物件与本项目无关，换成本片内容实现同款手法。\n")
	if len(brief.Signatures) > 0 {
		b.WriteString("必答特色（每条都必须被设计系统吸收，并在 signature_absorption 中登记落点）：\n")
		for i, sg := range brief.Signatures {
			fmt.Fprintf(&b, "%d. [%s] %s——机制：%s", i+1, sg.Kind, sg.Name, sg.Mechanism)
			if strings.TrimSpace(sg.UseHint) != "" {
				fmt.Fprintf(&b, "（适用：%s）", sg.UseHint)
			}
			b.WriteString("（证据帧已附）\n")
		}
	} else {
		b.WriteString("（该条目没有开启的特色，气质对齐即可）\n")
	}
	b.WriteString("\n")
	return b.String()
}

func tagBriefLine(brief *contract.StyleBrief, id string) string {
	for _, tb := range brief.TagBriefs {
		if tb.ID == id {
			return tb.Name + "——" + tb.Brief
		}
	}
	return ""
}

// storyboardDigest 分镜摘要：设计的原料——每段标题/旁白/画面提示各截一段，
// 让模型看到的是整部片的弧线与装置机会，不是单段气质。
func storyboardDigest(sb *contract.Storyboard) string {
	var b strings.Builder
	for _, seg := range sb.Segments {
		fmt.Fprintf(&b, "- %s（%s，约%.0f 秒）旁白：%s\n  画面提示：%s\n",
			seg.ID, seg.Key, seg.DurationHint, TruncRunes(seg.Narration, 90), TruncRunes(seg.VisualBrief, 60))
	}
	return b.String()
}

// directorDesignBlock 导演统筹注入（设计侧）：叙事弧线/母题给设计系统当呼应对象
// ——母题的视觉形态可进 concept/贯穿装置，各章情绪差异可进双皮肤 skin_rule。
// 无导演字段返回空串（老项目零感知）。
func directorDesignBlock(sb *contract.Storyboard) string {
	if sb.Arc == nil && len(sb.Motifs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## 导演统筹（本片叙事结构——设计系统要呼应它）\n")
	if sb.Arc != nil {
		fmt.Fprintf(&b, "- 主线：%s\n- 弧线：", sb.Arc.Spine)
		for i, ch := range sb.Arc.Chapters {
			if i > 0 {
				b.WriteString(" → ")
			}
			fmt.Fprintf(&b, "「%s」%s（张力 %d/5，%s–%s）",
				ch.Title, ch.Mood, ch.Tension, ch.Segs[0], ch.Segs[len(ch.Segs)-1])
		}
		b.WriteString("\n")
	}
	for _, m := range sb.Motifs {
		roles := make([]string, len(m.Appearances))
		for i, ap := range m.Appearances {
			roles[i] = ap.SegID + "(" + ap.Role + ")"
		}
		fmt.Fprintf(&b, "- 母题「%s」：%s——出场 %s\n", m.Name, m.Desc, strings.Join(roles, "、"))
	}
	b.WriteString("- 设计侧动作：母题的视觉形态写进 concept 或贯穿装置（device）；各章情绪差异可用双皮肤 skin_rule 承接；张力是节奏不是图形，别把弧线画进画面\n\n")
	return b.String()
}

func designJSONShape() string {
	return `{
  "direction": "方向名（如：黑金史诗·交界地挽歌）",
  "concept": "本片视觉概念一句话：为什么这套系统配这部片",
  "skins": [
    { "name": "主皮肤名",
      "slots": { "bg": "#0D0A07", "txt": "#E8DCC0", "accent": "#C9A227",
                 "font": "'SimSun','Microsoft YaHei',serif",
                 "mute": "#9C8F72", "surface": "#1A1410", "panel_txt": "#EFE6CF",
                 "radius": 0, "border_w": 1 }
      "css": "/* 自由 CSS ≤4000 字符：纹理/特效/装饰，挂 .skin-paper/.skin-root，用 var(--bg) 等 */" }
  ],
  "skin_rule": "双皮肤时必填：哪类段/哪个节拍穿哪套；单皮肤省略此字段",
  "fonts": {
    "display": { "stack": "'SimHei','Microsoft YaHei',sans-serif", "weight": "900", "tracking": "0.06em" },
    "body":    { "stack": "'KaiTi','Microsoft YaHei',serif", "weight": "400", "tracking": "normal" },
    "data":    { "stack": "Consolas,'Microsoft YaHei',monospace", "weight": "700", "tracking": "0.02em" }
  },
  "motion": { "ease": "power3.out", "dur_in": 0.5, "dur_settle": 0.25, "stagger": 0.08,
    "entrances": [
      { "name": "终端逐行点亮", "how": "整块 opacity:0→1，子元素 y:14→0 逐个 stagger 0.09", "use_for": "标题/面板组" },
      { "name": "滑入揭示", "how": "x:±60→0 配 power2.inOut，底层面板先行 0.15s", "use_for": "左右分栏的主画面" },
      { "name": "字卡打字机", "how": "文字按字符 span 化，opacity 逐字点亮 0.03s/字", "use_for": "等宽日志/数据行" },
      { "name": "缩放落定", "how": "scale:1.06→1 配 back.out(1.5)，用于强调单主体", "use_for": "大字海报主体/关键证据卡" }
    ],
    "rhythm": "旁白每推进一个信息点画面响应一次；约每 3–5 秒一个可见事件（新元素入场/已有元素强调变色/运镜变向），入场后静坐到段尾=失败",
    "exits": "旁白收尾前 0.3s 该退的退完：淡出或反向滑出，禁拖尾",
    "camera": "全段持续运动：10 秒内 scale≥1.08 或位移≥画面宽度 5%，方向与叙事呼应",
    "transition": "本片招牌转场（简报含转场特色时按其机制实现；「黑场一瞬」须在 notes 论证才可用）",
    "notes": "性格一句话：什么怎么动、禁什么动" },
  "device": { "name": "装置名（如：访客卡片）", "desc": "装置是什么、观众为什么会记住它",
              "per_seg": "seg01 首次亮相…seg02 变奏…seg03 起每章复现…" },
  "entry_exit": { "entry": "标准入场动画机制一句话（新场景开场走它）", "exit": "标准收尾动画机制一句话", "entry_dur": 0.4 },
  "post": { "vignette": 0, "grain": 0, "chroma": 0 },
  "layouts": [ { "id": "hero-title", "name": "大字海报", "desc": "构图描述：主体位置/元素铺排/留白方向",
                 "layered": "仅叠层版式写：哪层压哪层、为何可读（衬底低对比/前景有实底）；无叠层留空不写" } ],
  "anti_patterns": ["大面积辉光", "金句+引号+装饰线+作者+边框五件套", "右半空版式"],
  "post": { "vignette": 0, "grain": 0, "chroma": 0 },
  "signature_absorption": [ { "sig_id": "sig1", "lands_in": "device|motion|skins|camera_plan|transition_policy|material_policy", "how": "一句话写清这条特色怎么落在该组件" } ],
  "samples": [
    { "tag": "样张 A · 标题帧", "desc": "用了哪个版式、展示什么", "html": "<!doctype html>…整文档 960×540…" }
  ],
  "confirmed": false
}`
}

// designAbsorptionNote 输出要求里对落点表的补充说明（有简报才提）。
func designAbsorptionNote(p *Project) string {
	brief := LoadStyleBrief(p)
	if brief == nil || len(brief.Signatures) == 0 {
		return ""
	}
	return fmt.Sprintf("\n- signature_absorption：简报里每条开启的特色（%s）都必须登记，lands_in 只能取 %s；漏登/越界会被打回重出\n- 动态特色必须「真实现」不只登记：transition 类 → motion.transition、camera 类 → motion.camera、motion 类 → motion.entrances，都按特色机制写成可用手法；登记了却没落地一样打回\n",
		joinSigIDs(brief), contract.AbsorptionLandNames())
}

func joinSigIDs(brief *contract.StyleBrief) string {
	ids := make([]string, 0, len(brief.Signatures))
	for _, sg := range brief.Signatures {
		ids = append(ids, sg.ID)
	}
	return strings.Join(ids, ",")
}

func designUserPrompt(p *Project, sb *contract.Storyboard) string {
	return designBrief(p, sb) + "\n\n## 输出要求\n只输出 JSON，无 markdown 围栏：\n" + designJSONShape() + designAbsorptionNote(p)
}

func designRequest(p *Project, sb *contract.Storyboard, out string) string {
	return fmt.Sprintf(`# 作业：本片设计系统（v2 笼子二 · LLM 作业 #2）

%s

## 输出要求
把 JSON 写到: %s（confirmed 先置 false，用户确认后由管线改为 true——这是硬门）

%s
%s

## 校验（缺一件都会被拒）
- direction/concept 非空；skins 1–2 套（双皮肤必须给 skin_rule）；samples 1–3 张且每张 html 非空
- fonts.display/body 栈含中文字体；motion.ease/notes 非空、dur_in 0.1–1.5、dur_settle 0.05–1.0、stagger 0–0.5；
  entrances 3–5 条（name/how/use_for 三件齐、手法名互异）、rhythm/exits/camera/transition 各自非空
- device.name/desc/per_seg 三件齐；entry_exit.entry/exit 非空（entry_dur 0.2–1.0）——标准入场/收尾动画模板是转场的载体
`, designBrief(p, sb), out, designJSONShape(), designAbsorptionNote(p))
}

// DesignDocWithBrief 设计系统 → 注入文档（scene_gen 每段注入源 + 确认后 design.md
// 落盘内容）。机械渲染，与 tokens（style_samples.json）零漂移；设计块缺失时只渲染
// 已有部分（旧项目降级为方向+皮肤，不炸）。segIdx ≥ 1 时附加本段版式指派（按段
// 轮换）。带风格简报渲染（v3 §4.5：落点表写进 design.md，强参考的特色与落点对
// scene_gen 可见）；brief 为 nil 退化为旧渲染（老项目/未选风格）。
func DesignDocWithBrief(ss *contract.StyleSamples, segIdx int, brief *contract.StyleBrief) string {
	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f, a...) }
	w("## 本片设计系统：%s（每一段都必须遵守的全片规矩）\n", ss.Direction)
	w("\n### 风格≠题材（先读这条，优先级最高）\n")
	w("- 设计系统管的是**怎么画**（配色/字体/动效/构图骨架）；**画什么**永远由本段画面提示说了算——\n")
	w("  禁止把本段主体降级成小卡片/贴纸去迁就版式，版式是为本段主体服务的。\n")
	w("- 版式与装置描述里出现的具体物件（城市/建筑/卡片/角色等）是风格来源视频的**示例物**，\n")
	w("  只取构图骨架（位置/比例/层次/留白/生长节拍），填进去的必须是本段该画的东西。\n")
	w("- 附在图里的样张与原片真迹同理：只取配色/质感/构图密度，图里画的物件是风格来源\n")
	w("  视频的题材，禁止照着画。\n")
	w("- 风格来源视频自身的题材元素与品牌字样（原片名/原片编号/Logo 类字串）禁止画进画面。\n")
	if ss.Concept != "" {
		w("- 概念：%s\n", ss.Concept)
	}
	skins := ss.EffectiveSkins()
	for i := range skins {
		sk := &skins[i]
		label := "主皮肤"
		if i == 1 {
			label = "副皮肤"
		}
		if len(skins) == 1 {
			label = "皮肤"
		}
		w("\n### %s：%s\n", label, sk.Name)
		s := sk.Slots
		w("- 槽位：底色 %s / 文字 %s / 强调 %s / 字体 %s", s.BG, s.TXT, s.Accent, s.Font)
		if s.Mute != "" {
			w(" / 次级 %s", s.Mute)
		}
		if s.Surface != "" {
			w(" / 面板底 %s", s.Surface)
		}
		w("\n- 配色措辞向皮肤气质靠（底色/强调色的邻近词）；custom 特效底也用这套色\n")
		if strings.TrimSpace(sk.CSS) != "" {
			w("- 全片质感 CSS（挂 .skin-paper/.skin-root，后写可覆盖）:\n```css\n%s\n```\n", sk.CSS)
		}
	}
	if len(skins) == 2 && strings.TrimSpace(ss.SkinRule) != "" {
		w("\n### 皮肤切换规则（按段执行，不许段间即兴）\n- %s\n", ss.SkinRule)
	}
	if f := ss.Fonts; f != nil {
		w("\n### 字体角色（三栈分工，禁一栈到底）\n")
		role := func(label string, fr contract.FontRole) {
			if strings.TrimSpace(fr.Stack) == "" {
				return
			}
			w("- %s：%s", label, fr.Stack)
			if fr.Weight != "" && fr.Weight != "normal" {
				w(" / 字重 %s", fr.Weight)
			}
			if fr.Tracking != "" && fr.Tracking != "normal" {
				w(" / 字距 %s", fr.Tracking)
			}
			w("\n")
		}
		role("大标题/大数字 display", f.Display)
		role("正文/说明 body", f.Body)
		role("数据/标签/代码 data", f.Data)
	}
	if m := ss.Motion; m != nil && strings.TrimSpace(m.Ease) != "" {
		w("\n### 动效语言（可补间只有 opacity/x/y/scale/rotation 系；数字滚动用 hf.count）\n")
		w("- 缓动族 %s；进场 %.2fs / 落定 %.2fs / 同组错峰 %.2fs\n", m.Ease, m.DurIn, m.DurSettle, m.Stagger)
		if len(m.Entrances) > 0 {
			w("- 入场手法菜单（全片轮换取用，禁整片只用一条；每段至少两种）：\n")
			for _, mv := range m.Entrances {
				w("  · %s——%s（适用：%s）\n", mv.Name, mv.How, mv.UseFor)
			}
		}
		if strings.TrimSpace(m.Rhythm) != "" {
			w("- 段内节奏（下限，违反=返工）：%s\n", m.Rhythm)
		}
		if strings.TrimSpace(m.Exits) != "" {
			w("- 出场：%s\n", m.Exits)
		}
		if strings.TrimSpace(m.Camera) != "" {
			w("- 运镜：%s\n", m.Camera)
		}
		if strings.TrimSpace(m.Transition) != "" {
			w("- 段间转场（按此执行，禁退回黑场默认）：%s\n", m.Transition)
		}
		if strings.TrimSpace(m.Notes) != "" {
			w("- %s\n", m.Notes)
		}
	}
	if d := ss.Device; d != nil && strings.TrimSpace(d.Name) != "" {
		w("\n### 贯穿装置：%s（全片签名，观众记住这部片靠它）\n", d.Name)
		if strings.TrimSpace(d.Desc) != "" {
			w("- %s\n", d.Desc)
		}
		// per_seg 是对着分镜逐段写的。库内条目（StyleID 锚定）的 per_seg 写的是原视频的
		// 段落（seg01/seg02…），段号与本片错位，注入等于误导——只留装置机制，逐段变奏
		// 由各段对画面提示自行发挥；本片自己的设计系统（设计阶段现发明的）才逐段指派。
		if strings.TrimSpace(d.PerSeg) != "" && ss.StyleID == "" {
			w("- 逐段运用：%s\n", d.PerSeg)
		}
	}
	// 标准入场/收尾动画模板（转场载体，prep-agents-design §4）：一套模板一段内生效。
	// 逐段即兴发明转场是退役了的缝概念还魂；段间衔接的内容依据在分镜首拍/末拍。
	if ee := ss.EntryExit; ee != nil && strings.TrimSpace(ee.Entry) != "" {
		w("\n### 标准入场/收尾动画模板（转场的载体——一套模板一段内生效，禁逐段即兴）\n")
		entryDur := ee.EntryDur
		if entryDur <= 0 {
			entryDur = 0.4
		}
		w("- 入场（新场景段开场 0–%.1fs）：%s\n", entryDur, ee.Entry)
		if strings.TrimSpace(ee.Exit) != "" {
			w("- 收尾（每段旁白收尾前）：%s\n", ee.Exit)
		}
		w("- 段间衔接的内容依据（接什么/怎么接）看本段画面提示的首拍/末拍行；模板只管「怎么显影/怎么收」\n")
	}
	if len(ss.Layouts) > 0 {
		w("\n### 本片版式（先定构图，再往里填元素；禁退回「标题+小卡平铺」默认网格）\n")
		if segIdx >= 1 {
			asg := ss.Layouts[(segIdx-1)%len(ss.Layouts)]
			w("- **本段指定版式：%s（%s）——%s**（画面提示与指定版式明显冲突时可改选其他版式）\n", asg.ID, asg.Name, asg.Desc)
			if strings.TrimSpace(asg.Layered) != "" {
				w("- 本段指定版式声明叠层：%s——允许叠压的元素，在上层块加 data-layout-allow-overlap；声明之外的叠压仍会被 layout 检查拒绝\n", asg.Layered)
			}
		}
		for _, l := range ss.Layouts {
			w("- %s（%s）：%s\n", l.ID, l.Name, l.Desc)
			if strings.TrimSpace(l.Layered) != "" {
				w("  - 声明叠层：%s\n", l.Layered)
			}
		}
	}
	if len(ss.AntiPatterns) > 0 {
		w("\n### 本片负面清单（出现即返工）\n")
		for _, a := range ss.AntiPatterns {
			if strings.TrimSpace(a) != "" {
				w("- %s\n", a)
			}
		}
	}
	if ss.Post != nil {
		w("\n### 全片后期质感（stitch 统一施加；0=关）\n- 暗角 %.2f / 颗粒 %.2f / 色差 %.2f", ss.Post.Vignette, ss.Post.Grain, ss.Post.Chroma)
		if ss.Post.Vignette == 0 && ss.Post.Grain == 0 && ss.Post.Chroma == 0 {
			w("（不加后期，画面保持原样）")
		}
		w("\n")
	}
	// v3：特色落点表（简报特色 × 设计交卷自报落点）。渲染画面按落点执行对应手法；
	// 特色机制只写「怎么做到」，物件永远跟画面提示走。
	if brief != nil && len(brief.Signatures) > 0 {
		w("\n### 本片强参考特色（用户选定的硬要求，按落点执行）\n")
		landed := map[string]contract.SigAbsorption{}
		for _, a := range ss.SignatureAbsorption {
			landed[a.SigID] = a
		}
		for _, sg := range brief.Signatures {
			a := landed[sg.ID]
			line := "- [%s] %s——机制：%s"
			args := []any{sg.Kind, sg.Name, sg.Mechanism}
			if a.SigID != "" {
				line += "｜落点 %s：%s"
				args = append(args, a.LandsIn, a.How)
			}
			if strings.TrimSpace(sg.UseHint) != "" {
				line += "（适用：%s）"
				args = append(args, sg.UseHint)
			}
			w(line+"\n", args...)
		}
		w("- 特色吸收的是机制：物件与题材永远跟本段画面提示走，禁止临摹参考片里的具体物件\n")
	}
	return b.String()
}

// LockDesign 确认后的设计系统落盘：design.md（人读 + 门2 + scene_gen 注入源，含
// 强参考特色落点表）+ skin.json（引擎件=主皮肤，assemble 字幕带配色消费）。
// tokens 本体就是 style/style_samples.json——不另立 tokens.json，防双源漂移（宪法 #1）。
// 同时把设计决策接线到机械层（此前机械层自作主张，出口行为模型管不到）：
//   - transition 令牌：motion.transition 现场发明的转场 → 机械映射 cut|fade|xfade
//     写 project.json，assemble 页内黑场与 Go 拼接都消费它（缺省 cut——段间节奏
//     由场景内的退场/入场编排接管，平台不再默认黑场翻页）；
//   - post 后期槽位：design post {vignette,grain,chroma}（0–1，全 0=关）写
//     project.json——暗角/颗粒不再是全片默认，是设计系统发明的可选质感。
func LockDesign(p *Project, ss *contract.StyleSamples) error {
	changed, matched := clampDesignFonts(ss)
	if changed {
		p.Manifest("design.fontclamp", "设计字体栈含渲染端不可用字体，已过滤为系统字体")
	}
	if len(matched) > 0 {
		p.Manifest("design.fontcatalog", "设计点名目录字体："+strings.Join(mapKeys(matched), "、"))
	}
	if err := writeFontAssetRequest(p, matched); err != nil {
		return fmt.Errorf("字体准备申请书落盘失败: %w", err)
	}
	skins := ss.EffectiveSkins()
	if len(skins) == 0 {
		return fmt.Errorf("设计系统缺皮肤（skins/skin）")
	}
	if err := writeFile(p.Artifact("style/design.md"), DesignDocWithBrief(ss, 0, LoadStyleBrief(p))); err != nil {
		return fmt.Errorf("design.md 落盘失败: %w", err)
	}
	sbj, err := json.MarshalIndent(skins[0], "", "  ")
	if err != nil {
		return err
	}
	if err := writeFile(p.Artifact("style/skin.json"), string(sbj)); err != nil {
		return fmt.Errorf("skin.json 落盘失败: %w", err)
	}
	// 设计决策 → 机械令牌（幂等：内容指纹变了才重写）。project.json 缺失（测试
	// 夹具/裸项目）只跳过接线不失败——令牌落不进机械层，设计本体照常锁定。
	if _, err := os.Stat(p.Artifact("project.json")); err == nil {
		if tok := transitionTokenOf(ss); tok != "" {
			// 幂等短路（与 writePostFX 同理）：令牌没变不动盘，防每轮重放白写
			if readProjectField(p, "transition") != tok {
				if err := SetProjectField(p, "transition", tok); err != nil {
					return fmt.Errorf("转场令牌落盘失败: %w", err)
				}
			}
			p.Manifest("design.transition", tok)
		}
		if err := writePostFX(p, ss); err != nil {
			return fmt.Errorf("post 后期槽位落盘失败: %w", err)
		}
	}
	p.Manifest("design.version", designVersion(ss))
	p.Manifest("design.skins", strconv.Itoa(len(skins)))
	return nil
}

// transitionTokenOf 设计系统的转场政策 → 机械令牌。现场发明的转场千姿百态，
// 机械层只认三种出口：cut（直切——默认，段间节奏交给场景内退场/入场编排）、
// fade（页内黑场微叠化）、xfade（真交叉叠化）。映射只认**未被否定的**关键词：
// 「段尾黑场一瞬」→ fade，「禁黑场翻页」不算（设计在禁它）；明说「叠化/交叉」
// → xfade；其余一切（含缺省）→ cut——招牌转场的手法本体本来就该画在场景
// 文件里，机械层不越权实现。
func transitionTokenOf(ss *contract.StyleSamples) string {
	if ss == nil || ss.Motion == nil {
		return "cut"
	}
	t := strings.ToLower(ss.Motion.Transition)
	switch {
	case hasUnnegated(t, "黑场"), hasUnnegated(t, "闪黑"), hasUnnegated(t, "黑一瞬"):
		return "fade"
	case hasUnnegated(t, "叠化"), hasUnnegated(t, "交叉"), hasUnnegated(t, "淡入淡出"):
		return "xfade"
	}
	return "cut"
}

// hasUnnegated t 里存在未被否定的 kw 出现：关键词前 2 字内出现否定词
// （禁/不/无/没/别/非/拒绝/远离）即视为否定——「禁黑场翻页」≠ 用黑场。
func hasUnnegated(t, kw string) bool {
	neg := []string{"禁", "不", "无", "没", "别", "非", "拒绝", "远离"}
	for i := 0; ; {
		j := strings.Index(t[i:], kw)
		if j < 0 {
			return false
		}
		start := i + j
		lo := start - 6 // 最多回看 3 个 CJK 字（否定词紧贴关键词）
		if lo < 0 {
			lo = 0
		}
		prefix := t[lo:start]
		negated := false
		for _, n := range neg {
			if strings.Contains(prefix, n) {
				negated = true
				break
			}
		}
		if !negated {
			return true
		}
		i = start + len(kw)
	}
}

// reFontFamilyDecl CSS font-family 声明（<style> 块与内联样式同抓）。
var reFontFamilyDecl = regexp.MustCompile(`(?i)(font-family\s*:\s*)([^;{}"]+)`)

// ClampSegmentFonts 段帧字体栈机械归一（段级流水线 written 位）：模型屡教不改的
// 字体问题在落盘侧一次收口——目录字体的中文别名/大小写写法归一成规范 Family
// （'站酷快乐体' → 'ZCOOL KuaiLe'，与 LockDesign 同一 clampFontStack）；渲染端
// 没有且目录没有的字体（思源黑体/PingFang SC 等）直接剥除退到栈内后备。
// 只动 font-family 声明值，选择器与内容不碰。幂等。
func ClampSegmentFonts(p *Project, segID string) {
	path := filepath.Join(p.Artifact("compositions/frames"), segID+".html")
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	html := string(b)
	fixed := reFontFamilyDecl.ReplaceAllStringFunc(html, func(m string) string {
		loc := reFontFamilyDecl.FindStringSubmatch(m)
		return loc[1] + clampFontStack(loc[2], nil)
	})
	if fixed != html {
		_ = writeFile(path, fixed)
	}
}

// postFXOf 设计 post 槽位（0–1）→ project.json "post"。设计没写 post 节 = 明确
// 不加后期（写 false），机械层不再自作主张叠默认暗角颗粒——质感是设计决策。
func postFXOf(ss *contract.StyleSamples) any {
	if ss == nil || ss.Post == nil {
		return false
	}
	return ss.Post
}

// readProjectField 读 project.json 顶层字段（缺失/损坏返回零值；幂等短路用）。
func readProjectField(p *Project, key string) string {
	b, err := os.ReadFile(p.Artifact("project.json"))
	if err != nil {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return ""
	}
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

// writePostFX post 槽位接线（幂等条件写：值变化才动盘）。比较前先归一化：
// meta["post"] 反序列化成 map 后 Marshal 按键字母序输出，want 是 struct 按字段
// 序输出——两串恒不相等，旧版短路永不生效，每轮管线重放都白写 project.json
// +重复上传正本（2026-10-19 中-16）。
func writePostFX(p *Project, ss *contract.StyleSamples) error {
	want := postFXOf(ss)
	b, _ := os.ReadFile(p.Artifact("project.json"))
	var meta map[string]any
	if json.Unmarshal(b, &meta) == nil {
		if wj, err := json.Marshal(want); err == nil {
			var wnorm map[string]any
			if json.Unmarshal(wj, &wnorm) == nil {
				cur, _ := json.Marshal(meta["post"])
				wn, _ := json.Marshal(wnorm)
				if string(cur) == string(wn) {
					return nil
				}
			}
		}
	}
	return SetProjectField(p, "post", want)
}

// cssGenericFonts CSS 通用字体族关键字：任何渲染端都能解析，不需要声明。
var cssGenericFonts = map[string]bool{
	"serif": true, "sans-serif": true, "monospace": true, "cursive": true,
	"fantasy": true, "system-ui": true, "ui-serif": true, "ui-sans-serif": true,
	"ui-monospace": true,
}

// cjkFontSet 系统白名单里的中文字体半区：过滤后栈里必须还剩中文可用的字源
// （系统中文字体或目录字体），否则中文会豆腐块。
var cjkFontSet = map[string]bool{
	"microsoft yahei": true, "微软雅黑": true, "simhei": true, "黑体": true,
	"simsun": true, "宋体": true, "nsimsun": true, "新宋体": true,
	"kaiti": true, "楷体": true, "stkaiti": true, "stsong": true,
	"stzhongsong": true, "fangsong": true, "仿宋": true,
	"noto serif sc": true, "noto sans sc": true,
}

// clampFontStack 字体栈解析：设计发明会起花名（PingFang/站酷/思源等渲染端既没装
// 也没打包的字体），场景提示有「引用即声明」纪律兜得住，字幕带（assemble 消费
// skin slots.font）是机械装配没有这道关——未声明字体直接全灭字幕（2026-10-11
// 夜跑实录：21 个字幕文件一票全否）。处理顺序：系统白名单与通用族保留 →
// 目录字体（fontCatalog，平台下载+子集化+真声明）改写为规范名并登记 →
// 其余花名剥除；剥完中文失去字源（只剩通用族/西文）时整体回退雅黑。
// 幂等：已干净的栈原样返回（designVersion 不漂移）。
func clampFontStack(stack string, matched map[string]*CatalogFont) string {
	var keep []string
	seen := map[string]bool{}
	cjkSource := false
	for _, tok := range strings.Split(stack, ",") {
		name := strings.Trim(strings.TrimSpace(tok), "'\"")
		lower := strings.ToLower(name)
		if name == "" || seen[lower] {
			continue
		}
		generic := cssGenericFonts[lower]
		catalog := (*CatalogFont)(nil)
		if !generic && !osFontWhitelist[lower] {
			catalog = matchCatalogFont(name)
			if catalog == nil {
				continue
			}
		}
		seen[lower] = true
		switch {
		case generic:
			keep = append(keep, lower)
		case catalog != nil:
			keep = append(keep, "'"+catalog.Family+"'")
			cjkSource = true
			if matched != nil {
				matched[catalog.Family] = catalog
			}
		default:
			keep = append(keep, "'"+titleCaseFont(lower)+"'")
			if cjkFontSet[lower] {
				cjkSource = true
			}
		}
	}
	if len(keep) == 0 || !cjkSource {
		return "'Microsoft YaHei',sans-serif"
	}
	return strings.Join(keep, ",")
}

// clampDesignFonts 就地解析设计系统里的全部字体栈（三角色 + 各皮肤槽位），
// 返回是否有改动与点名的目录字体（LockDesign 据此写准备申请书）。
// 样张 HTML 是预览件不解析（所见即所得只在确认前承诺）。
func clampDesignFonts(ss *contract.StyleSamples) (bool, map[string]*CatalogFont) {
	changed := false
	matched := map[string]*CatalogFont{}
	clamp := func(stack *string) {
		if *stack == "" {
			return
		}
		if c := clampFontStack(*stack, matched); c != *stack {
			*stack = c
			changed = true
		}
	}
	if ss.Fonts != nil {
		clamp(&ss.Fonts.Display.Stack)
		clamp(&ss.Fonts.Body.Stack)
		clamp(&ss.Fonts.Data.Stack)
	}
	for i := range ss.Skins {
		clamp(&ss.Skins[i].Slots.Font)
	}
	if ss.Skin != nil {
		clamp(&ss.Skin.Slots.Font)
	}
	return changed, matched
}

// mapKeys 目录字体的 manifest 摘要用（键即规范家族名）。
func mapKeys(m map[string]*CatalogFont) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// designVersion 设计系统内容指纹：style_samples.json 任一字段改动都会换版本。
// sceneGen 段级旁车带着它（sceneFingerprint:designVersion）——设计一改，
// 按旧版画的段自动过期重生成；确认两次相同内容版本不变，不误伤缓存。
func designVersion(ss *contract.StyleSamples) string {
	sum := sha256.Sum256([]byte(mustJSON(ss)))
	return hex.EncodeToString(sum[:8])
}
