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
	"strconv"
	"strings"

	"webpage-video-agent/server/internal/contract"
)

const designSystemPrompt = "你是视频视觉总监。先通读全部分镜，为这一部片发明一套设计系统，全片每一段都遵守。只输出 JSON。"

// designBrief 设计作业正文（API 直调与会话请求共用一份，防两处漂移）。
// input/style_notes.txt 存在时追加用户修改要求（draft_style_samples 带 instruction 写入）。
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

## 全部分镜（设计必须从这些内容里长出来）
%s
## 设计系统六件套（缺一件会被校验打回）

### 1. concept 概念
一句话：本片视觉概念是什么、为什么这套系统配这部片（从分镜气质推导，不许套话）。

### 2. skins 皮肤（1–2 套）
- 必填槽位：bg 底色 / txt 文字色 / accent 强调色（#RRGGBB；txt 落 bg 上对比度必须 ≥4.5:1，实测校验会拒）/ font 字体栈（中文字体打头）。
- 可选槽位：mute 次级文字 / surface 卡片底 / panel_txt 面板字 / radius 圆角0–40 / border_w 描边0–6 / img_rot 配图旋转-8~8。
- css 自由 CSS（≤4000 字符）：纹理/质感/装饰/特效底——风格差异化的主战场，认真写。
  挂点 .skin-root（根容器）、.skin-paper（满画幅背景层）；CSS 变量 var(--bg/--txt/--accent/--mute)。
  动效质感用静态多层渐变/噪点/边框模拟（暗角、纸纹、网格、光斑、色带）。
- 双皮肤（加分项）：两套气质相反的皮肤 + skin_rule 写清切换规则（哪类段/哪个节拍穿哪套，可机械执行）。单皮肤省略 skin_rule。

### 3. fonts 字体三角色（禁一栈到底）
- display 大标题/大数字：重字重 + 字距拉开（letter-spacing 0.04em 级）；
- body 正文/说明：可读优先；
- data 数据/标签/代码：等宽优先，栈尾必须带 CJK 字体回退（数据标签也有中文）；
- 每个角色的栈都必须含中文字体（渲染端是本机字体，豆腐块=废片）。

### 4. motion 动效语言（全片节奏一致性的来源，档次分水岭）
- ease 缓动族（如 power3.out）；dur_in 进场时长档 / dur_settle 落定档 / stagger 同组错峰间隔（秒）；
- notes 一句话性格：什么怎么动、禁什么动、段间转场习惯。全片所有段的动效都在这套档位里取值。

### 5. device 贯穿装置
- 全片反复出现、随内容变奏的招牌画面机制——观众记住这部片靠它（参考形态：同一张卡片每章亮相、单版式状态机）。
- per_seg 对着上面分镜逐段写：它怎么重复/变奏/演变。

### 6. layouts 版式（3–5 个）与 anti_patterns 负面清单（≤8 条）
- 每个版式构图明显不同（大字海报 / 主视觉分栏 / 数据焦点 / 满幅底图+侧栏各取其一），不要每张都排成「标题+元素网格」；段轮换用；
- 负面清单 = 本片禁用的俗手，按本片内容挑真实风险（右半空版式/假密度/文字压图不可读……）。

## 样张 = 用主皮肤渲出的静态小画面（所见即所得，1–3 张）
- 颜色/字体必须来自主皮肤槽位（色值逐字一致）——样张即皮肤预览，两处不一致会被打回；
- 不用图片/网络资源，纯 CSS；多张样张构图版式必须明显不同，每张 desc 写明用了哪个版式；
- 画布硬约束（违反必被裁切）：每张 html 是完整自包含文档，画布固定 960×540：
  html,body{width:960px;height:540px;overflow:hidden}；内容全部落在画布内，不依赖滚动；
  禁 vw/vh；大字标题留边距；每张 html ≤2000 字符%s`, storyboardDigest(sb), notes)
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

func designJSONShape() string {
	return `{
  "direction": "方向名（如：黑金史诗·交界地挽歌）",
  "concept": "本片视觉概念一句话：为什么这套系统配这部片",
  "skins": [
    { "name": "主皮肤名",
      "slots": { "bg": "#0D0A07", "txt": "#E8DCC0", "accent": "#C9A227",
                 "font": "'SimSun','Microsoft YaHei',serif",
                 "mute": "#9C8F72", "surface": "#1A1410", "panel_txt": "#EFE6CF",
                 "radius": 0, "border_w": 1, "img_rot": 0 },
      "css": "/* 自由 CSS ≤4000 字符：纹理/特效/装饰，挂 .skin-paper/.skin-root，用 var(--bg) 等 */" }
  ],
  "skin_rule": "双皮肤时必填：哪类段/哪个节拍穿哪套；单皮肤省略此字段",
  "fonts": {
    "display": { "stack": "'SimHei','Microsoft YaHei',sans-serif", "weight": "900", "tracking": "0.06em" },
    "body":    { "stack": "'KaiTi','Microsoft YaHei',serif", "weight": "400", "tracking": "normal" },
    "data":    { "stack": "Consolas,'Microsoft YaHei',monospace", "weight": "700", "tracking": "0.02em" }
  },
  "motion": { "ease": "power3.out", "dur_in": 0.5, "dur_settle": 0.25, "stagger": 0.08,
              "notes": "大字从下方浮入+淡入；数字用 count 滚动；禁弹跳/回弹；段尾安静 0.25s 再切" },
  "device": { "name": "装置名（如：访客卡片）", "desc": "装置是什么、观众为什么会记住它",
              "per_seg": "seg01 首次亮相…seg02 变奏…seg03 起每章复现…" },
  "layouts": [ { "id": "hero-title", "name": "大字海报", "desc": "构图描述：主体位置/元素铺排/留白方向" } ],
  "anti_patterns": ["大面积辉光", "金句+引号+装饰线+作者+边框五件套", "右半空版式"],
  "samples": [
    { "tag": "样张 A · 标题帧", "desc": "用了哪个版式、展示什么", "html": "<!doctype html>…整文档 960×540…" }
  ],
  "confirmed": false
}`
}

func designUserPrompt(p *Project, sb *contract.Storyboard) string {
	return designBrief(p, sb) + "\n\n## 输出要求\n只输出 JSON，无 markdown 围栏：\n" + designJSONShape()
}

func designRequest(p *Project, sb *contract.Storyboard, out string) string {
	return fmt.Sprintf(`# 作业：本片设计系统（v2 笼子二 · LLM 作业 #2）

%s

## 输出要求
把 JSON 写到: %s（confirmed 先置 false，用户确认后由管线改为 true——这是硬门）

%s

## 校验（缺一件都会被拒）
- direction/concept 非空；skins 1–2 套（双皮肤必须给 skin_rule）；samples 1–3 张且每张 html 非空
- fonts.display/body 栈含中文字体；motion.ease/notes 非空、dur_in 0.1–1.5、dur_settle 0.05–1.0、stagger 0–0.5
- device.name/desc/per_seg 三件齐
`, designBrief(p, sb), out, designJSONShape())
}

// DesignDoc 设计系统 → 注入文档（scene_gen 每段注入源 + 确认后 design.md 落盘内容）。
// 机械渲染，与 tokens（style_samples.json）零漂移；设计块缺失时只渲染已有部分
// （库内老包/旧项目降级为方向+皮肤，不炸）。segIdx ≥ 1 时附加本段版式指派（按段轮换）。
func DesignDoc(ss *contract.StyleSamples, segIdx int) string {
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
	if len(ss.Layouts) > 0 {
		w("\n### 本片版式（先定构图，再往里填元素；禁退回「标题+小卡平铺」默认网格）\n")
		if segIdx >= 1 {
			asg := ss.Layouts[(segIdx-1)%len(ss.Layouts)]
			w("- **本段指定版式：%s（%s）——%s**（画面提示与指定版式明显冲突时可改选其他版式）\n", asg.ID, asg.Name, asg.Desc)
		}
		for _, l := range ss.Layouts {
			w("- %s（%s）：%s\n", l.ID, l.Name, l.Desc)
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
	return b.String()
}

// LockDesign 确认后的设计系统落盘：design.md（人读 + 门2 + scene_gen 注入源）+
// skin.json（引擎件=主皮肤，assemble 字幕带配色消费）。tokens 本体就是
// style/style_samples.json——不另立 tokens.json，防双源漂移（宪法 #1）。
// 库内老包（无设计块）不在此落——applyPack 直写 confirmed 文件走兜底注入。
func LockDesign(p *Project, ss *contract.StyleSamples) error {
	skins := ss.EffectiveSkins()
	if len(skins) == 0 {
		return fmt.Errorf("设计系统缺皮肤（skins/skin）")
	}
	if err := writeFile(p.Artifact("style/design.md"), DesignDoc(ss, 0)); err != nil {
		return fmt.Errorf("design.md 落盘失败: %w", err)
	}
	sbj, err := json.MarshalIndent(skins[0], "", "  ")
	if err != nil {
		return err
	}
	if err := writeFile(p.Artifact("style/skin.json"), string(sbj)); err != nil {
		return fmt.Errorf("skin.json 落盘失败: %w", err)
	}
	p.Manifest("design.version", designVersion(ss))
	p.Manifest("design.skins", strconv.Itoa(len(skins)))
	return nil
}

// designVersion 设计系统内容指纹：style_samples.json 任一字段改动都会换版本。
// sceneGen 段级旁车带着它（sceneFingerprint:designVersion）——设计一改，
// 按旧版画的段自动过期重生成；确认两次相同内容版本不变，不误伤缓存。
func designVersion(ss *contract.StyleSamples) string {
	sum := sha256.Sum256([]byte(mustJSON(ss)))
	return hex.EncodeToString(sum[:8])
}
