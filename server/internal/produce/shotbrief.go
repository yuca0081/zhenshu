package produce

// shotbrief.go —— 导演镜头表（2026-10-30 60 秒对照实验）。
//
// 生产管线原来的输入是「分镜稿：场景/首拍/末拍」，本质是版式规划；本文件在
// sceneGen 的提示词最前面注入一层「导演指令」：世界、角色表、整片镜头表、本镜
// 景别/机位/运镜/舞台/动作/台词/转场。用来验证「画面平」是缺导演层，还是
// 渲染/模型表达力不够。
//
// 只在项目有 storyboards/shots.json（或 shot_brief=1）时生效；老项目零影响。

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/pipeline"
)

// ShotCast 本镜出场角色：谁、姿态/动作、情绪。
type ShotCast struct {
	Who     string `json:"who"`
	Pose    string `json:"pose"`
	Emotion string `json:"emotion,omitempty"`
}

// ShotBrief 一个镜头（本实验里 shot = 一个渲染段）。
type ShotBrief struct {
	Seg        string     `json:"seg"`
	Title      string     `json:"title"`
	Size       string     `json:"size"`       // 景别：wide/medium/close/overhead...
	Camera     string     `json:"camera"`     // 机位与运镜
	Stage      string     `json:"stage"`      // 舞台/道具/前后景
	Cast       []ShotCast `json:"cast"`       // 出场角色与动作
	Action     string     `json:"action"`     // 这一镜发生什么（戏）
	Dialogue   string     `json:"dialogue"`   // 短台词/字卡（不是旁白复述）
	Transition string     `json:"transition"` // 与下一镜的接法
	RefFrame   string     `json:"ref_frame,omitempty"`
	RefNote    string     `json:"ref_note,omitempty"`
}

// ShotPlan storyboards/shots.json 全文。
type ShotPlan struct {
	World      string      `json:"world"`       // 世界/舞台设定
	CastLegend string      `json:"cast_legend"` // 角色表（形状/身体/表情/性格）
	Shots      []ShotBrief `json:"shots"`
}

// shotBriefOn 项目级开关：显式 shot_brief=1，或存在 storyboards/shots.json，
// 或分镜表里已经有镜头字段（③新产出）。
func shotBriefOn(p *pipeline.Project) bool {
	if strings.TrimSpace(pipeline.ReadProjectField(p, "shot_brief")) == "1" {
		return true
	}
	if _, err := os.Stat(p.Artifact("storyboards/shots.json")); err == nil {
		return true
	}
	if sb, err := p.LoadStoryboard(); err == nil {
		return storyboardHasShots(sb)
	}
	return false
}

// storyboardHasShots 分镜表是否启用了镜头层（任一段有 shot 即算）。
func storyboardHasShots(sb *contract.Storyboard) bool {
	if sb == nil {
		return false
	}
	for _, seg := range sb.Segments {
		if seg.Shot != nil {
			return true
		}
	}
	return false
}

// loadShotPlan 导演镜头表的唯一读取入口：一期以 Segment.Shot 为准，
// shots.json 只作实验/导入导出（缺 World/CastLegend 时从它补）。
func loadShotPlan(p *pipeline.Project) *ShotPlan {
	file := loadShotPlanFile(p)
	if sb, err := p.LoadStoryboard(); err == nil {
		if sp := shotPlanFromStoryboard(sb); sp != nil {
			if file != nil {
				if strings.TrimSpace(sp.World) == "" {
					sp.World = file.World
				}
				if strings.TrimSpace(sp.CastLegend) == "" {
					sp.CastLegend = file.CastLegend
				}
			}
			return sp
		}
	}
	return file
}

// loadShotPlanFile 读实验/导入格式 storyboards/shots.json。
func loadShotPlanFile(p *pipeline.Project) *ShotPlan {
	b, err := os.ReadFile(p.Artifact("storyboards/shots.json"))
	if err != nil {
		return nil
	}
	var sp ShotPlan
	if json.Unmarshal(b, &sp) != nil || len(sp.Shots) == 0 {
		return nil
	}
	return &sp
}

// shotPlanFromStoryboard 把 Segment.Shot 拼成导演块需要的整片镜头表。
// 没有任何镜头字段返回 nil（老项目走原提示词）。
func shotPlanFromStoryboard(sb *contract.Storyboard) *ShotPlan {
	if sb == nil {
		return nil
	}
	sp := &ShotPlan{World: sb.World, CastLegend: sb.CastLegend}
	for _, seg := range sb.Segments {
		if seg.Shot == nil {
			continue
		}
		cast := make([]ShotCast, 0, len(seg.Shot.Cast))
		for _, c := range seg.Shot.Cast {
			cast = append(cast, ShotCast{Who: c.Who, Pose: c.Pose, Emotion: c.Emotion})
		}
		sp.Shots = append(sp.Shots, ShotBrief{
			Seg: seg.ID, Title: seg.Key,
			Size: seg.Shot.Size, Camera: seg.Shot.Camera, Stage: seg.Shot.Stage,
			Cast: cast, Action: seg.Shot.Action, Dialogue: seg.Shot.Dialogue,
			Transition: seg.Shot.Transition, RefFrame: seg.Shot.RefFrame, RefNote: seg.Shot.RefNote,
		})
	}
	if len(sp.Shots) == 0 {
		return nil
	}
	return sp
}

// promptBlock 本镜导演指令。返回空串 = 本镜不在镜头表里，退回原提示词。
func (sp *ShotPlan) promptBlock(segID string) string {
	if sp == nil {
		return ""
	}
	var cur *ShotBrief
	for i := range sp.Shots {
		if sp.Shots[i].Seg == segID {
			cur = &sp.Shots[i]
			break
		}
	}
	if cur == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("## 导演指令（最高优先级：这是一段有角色的戏，不是信息图/PPT；与下文“版式/元素入场”规则冲突时，以本段为准）\n\n")
	if s := strings.TrimSpace(sp.World); s != "" {
		b.WriteString("### 世界与舞台\n" + s + "\n\n")
	}
	if s := strings.TrimSpace(sp.CastLegend); s != "" {
		b.WriteString("### 角色表（每个角色都要有身体、腿、眼睛/嘴；不是没有身体的色块）\n" + s + "\n\n")
	}
	b.WriteString("### 整片镜头表（用于连续性；本镜与前后镜是同一个世界、同一批角色）\n")
	for _, s := range sp.Shots {
		mark := "  "
		if s.Seg == segID {
			mark = "→ "
		}
		fmt.Fprintf(&b, "%s%s《%s》%s｜%s｜%s\n", mark, s.Seg, s.Title, s.Size, s.Camera, s.Action)
	}
	b.WriteString("\n### 本镜（" + cur.Seg + "《" + cur.Title + "》）\n")
	fmt.Fprintf(&b, "- 景别：%s\n- 机位/运镜：%s\n- 舞台/道具/前后景：%s\n", cur.Size, cur.Camera, cur.Stage)
	if len(cur.Cast) > 0 {
		b.WriteString("- 出场角色与动作：\n")
		for _, c := range cur.Cast {
			fmt.Fprintf(&b, "  - %s：%s", c.Who, c.Pose)
			if c.Emotion != "" {
				fmt.Fprintf(&b, "（%s）", c.Emotion)
			}
			b.WriteString("\n")
		}
	}
	fmt.Fprintf(&b, "- 本镜的戏（必须看见发生）：%s\n", cur.Action)
	if s := strings.TrimSpace(cur.Dialogue); s != "" {
		fmt.Fprintf(&b, "- 台词/字卡（只放短句，不要复述整句旁白）：%s\n", s)
	}
	if s := strings.TrimSpace(cur.Transition); s != "" {
		fmt.Fprintf(&b, "- 与下一镜的接法：%s\n", s)
	}
	if s := strings.TrimSpace(cur.RefNote); s != "" {
		fmt.Fprintf(&b, "- 参考帧（只借构图/光/角色关系，不抄内容、不带参考片字幕水印）：%s\n", s)
	}
	if s := strings.TrimSpace(cur.RefFrame); s != "" {
		fmt.Fprintf(&b, "- 参考帧文件：%s\n", s)
	}
	b.WriteString(`
### 本镜硬要求（违反任意一条 = 画面平，试验失败）
- 角色是“演员”：必须画身体（两条细腿）、眼睛、嘴；本镜里要真的站/走/指/退/转头/耸肩/前倾，不能只是把图标换成三角。
- 舞台是“场景”：至少两层前后景（幕布/聚光灯/观众席/屏幕/桌子/纸纹地面），角色在舞台上有站位关系与遮挡。
- 观众/村民也是角色：小三角 + 两条细腿 + 眼睛；前景观众席可以压暗成剪影，但轮廓必须是三角，禁止用圆角矩形/黑块/纯色条代替。
- 标题/字卡放在舞台上方纸带安全区（y≈40–130px，或幕布上方的米纸边距），禁止压幕布横梁、角色脸、屏幕内容；台词卡放在角色嘴部附近或画面下三分之一，不要和标题抢同一块。
- 画面顶部不要再放标题/章节名：本片只有片尾 seg06 有字卡；台词只放屏幕里或角色旁；旁白字幕由平台注入。
- 桌子/道具按真实比例：桌板厚 18–24px，桌腿宽 10–14px、高 180–220px；不要画成细长木棍/牙签。
- 本镜至少两个角色同场（纯空镜除外），并且要有看向/指向/退让/转头的关系；禁止所有人各做各的入场。
- 镜头是“镜头”：本镜必须有可感知的机位/景别/运镜变化，或镜头内切一次；禁止整段只做“元素淡入 + 静态字卡”。
- 表演是“戏”：至少一个角色对另一个角色做出反应（看、指、退、让位、抬头、低头），不要所有人各做各的入场。
- 画面文字只放短台词/标题（≤12 字/行），不要写整句旁白；旁白字幕由平台注入，禁止 data-ph/data-w。
- 只借参考帧的构图/光/角色关系；不要出现参考片里的字幕、水印、具体台词。
`)
	return b.String()
}
