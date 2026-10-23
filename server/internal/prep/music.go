package prep

// music.go —— 音乐驱动 PV 的①音乐工位（pv-music-driven-design「一站三交付」）：
// 一个 ReAct 会话交付三件套——曲 brief（创作意图）→ 曲+音乐地图（物理事实）。
// 自检环是合并成一站的核心理由：compose_track 出曲立刻产出地图，agent 拿地图
// 数字对 brief 意图比对，对不上改参重做（封顶 2 次），超限带伤交门。
// 护栏：做曲与地图都是白名单确定性工具（ai/make_pv_track.py），agent 只准调用
// 判读、不准手改数据——「地图=物理事实、同输入同输出」不丢。
//
// 工件：music/brief.json（意图）+ music/track.mp3（曲）+ music/music_map.json
// （地图，HasArtifact 的完成标记）。分镜工位（M2）与 PV 制作管线只认地图。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"webpage-video-agent/server/internal/pipeline"
)

// musicBudget 回合预算：一做一读一交三条腿走完约 4–6 回合，12 给足重做余量。
const musicBudget = 12

// musicComposeCap 做曲封顶 = 首做 + 重做 ≤2（护栏二；超限必须带伤交门）。
const musicComposeCap = 3

// PV 词汇表（与 ai/make_pv_track.py 白名单同源镜像——Go lint 给 agent 快反馈，
// python 落地时再权威校验一遍）。
var (
	pvKeys     = []string{"Am", "C", "Em", "G", "Dm", "F"}
	pvSections = []string{"intro", "verse", "verse2", "build", "chorus", "chorus2", "break", "bridge", "outro"}
	pvChordRe  = regexp.MustCompile(`^[A-G][#b]?(m|maj7|m7|7|sus4|dim|m7b5)?$`)
	pvNameRe   = regexp.MustCompile(`^[\w\-]{2,40}$`)
)

const (
	pvBpmLo, pvBpmHi         = 84, 150
	pvBarsLo, pvBarsHi       = 16, 64
	pvSecBarsLo, pvSecBarsHi = 2, 16
	pvDurLo, pvDurHi         = 45.0, 115.0 // PV 整曲时长带（秒）
)

// 工件路径。完成标记是地图（曲与 brief 都是它的投料/旁证）。
func musicBriefPath(p *pipeline.Project) string { return p.Artifact("music/brief.json") }
func musicMapPath(p *pipeline.Project) string   { return p.Artifact("music/music_map.json") }
func musicTrackPath(p *pipeline.Project) string { return p.Artifact("music/track.mp3") }

// ProjectMode 读 project.json 驱动模式（music=音乐驱动 PV）。缺失/读档失败/
// 字段异常一律回 script——老项目无此字段，兼容语义就是「文稿驱动是默认世界」。
func ProjectMode(p *pipeline.Project) string {
	b, err := os.ReadFile(p.Artifact("project.json"))
	if err != nil {
		return "script"
	}
	var meta struct {
		Mode string `json:"mode"`
	}
	if json.Unmarshal(b, &meta) != nil || meta.Mode != "music" {
		return "script"
	}
	return "music"
}

// pvBrief 曲 brief（agent 的结构化输出，compose_track 的入参）。
type pvBrief struct {
	Name     string        `json:"name,omitempty"`
	Key      string        `json:"key,omitempty"`
	Bpm      int           `json:"bpm"`
	Bars     int           `json:"bars"`
	Prog     []pvProgChord `json:"prog,omitempty"`
	Sections []pvSection   `json:"sections"`
}

type pvProgChord struct {
	Sym  string `json:"sym"`
	Bars int    `json:"bars"`
}

type pvSection struct {
	Name   string `json:"name"`
	Bars   int    `json:"bars"`
	Energy int    `json:"energy"`
}

// validatePVBrief 白名单 lint：结构错误在 Go 这层就地打回（省一次 python 往返），
// 创作质量（能量对比）也在这里圈——python 侧是同一套的权威复核。
func validatePVBrief(b *pvBrief) []string {
	var errs []string
	add := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }
	if b.Bpm < pvBpmLo || b.Bpm > pvBpmHi {
		add("bpm 要 %d–%d，收到 %d", pvBpmLo, pvBpmHi, b.Bpm)
	}
	if b.Bars < pvBarsLo || b.Bars > pvBarsHi {
		add("bars 要 %d–%d，收到 %d", pvBarsLo, pvBarsHi, b.Bars)
	}
	name := b.Name
	if name == "" {
		name = "pv_track"
	}
	if !pvNameRe.MatchString(name) {
		add("name %q 只许 2–40 个字母/数字/下划线/连字符（它要当 mp3 文件名）", name)
	}
	// 时长账：bars×bpm 决定曲长，必须落在 PV 带——agent 用它倒推选型
	if b.Bpm >= pvBpmLo && b.Bars >= pvBarsLo {
		if exp := float64(b.Bars) * 4 * 60.0 / float64(b.Bpm); exp < pvDurLo || exp > pvDurHi {
			add("按 bars×bpm 算出的时长 %.1fs 不在 PV 带 %.0f–%.0f 秒——调 bars 或 bpm", exp, pvDurLo, pvDurHi)
		}
	}
	if len(b.Prog) == 0 {
		key := b.Key
		if key == "" {
			key = "Am"
		}
		if !pvKeyBuiltIn(key) {
			add("key %q 没有内置和弦进行（可选：%s），要么换 key 要么自己给 prog", key, strings.Join(pvKeys, "、"))
		}
	}
	cycle := 0
	for _, c := range b.Prog {
		if !pvChordRe.MatchString(c.Sym) {
			add("和弦记号 %q 不认识（形如 Am / F / Cmaj7 / Dm7 / G / Esus4）", c.Sym)
		}
		if c.Bars < 1 || c.Bars > 8 {
			add("和弦 %s 每轮占 %d 小节，超出 1–8", c.Sym, c.Bars)
		}
		cycle += c.Bars
	}
	if len(b.Prog) > 0 && cycle > b.Bars {
		add("和弦进行一轮 %d 小节 > 全曲 %d 小节", cycle, b.Bars)
	}
	if len(b.Sections) < 2 || len(b.Sections) > 10 {
		add("sections 要 2–10 段，收到 %d", len(b.Sections))
		return errs
	}
	total := 0
	chorusMax, verseMax := 0, 0
	for _, s := range b.Sections {
		if !pvSectionBuiltIn(s.Name) {
			add("段落名 %q 不在词汇表（可选：%s）", s.Name, strings.Join(pvSections, "、"))
		}
		if s.Bars < pvSecBarsLo || s.Bars > pvSecBarsHi {
			add("段落 %s 的 bars 要 %d–%d，收到 %d", s.Name, pvSecBarsLo, pvSecBarsHi, s.Bars)
		}
		if s.Energy < 1 || s.Energy > 5 {
			add("段落 %s 的 energy 要 1–5，收到 %d", s.Name, s.Energy)
			continue
		}
		if strings.HasPrefix(s.Name, "chorus") && s.Energy > chorusMax {
			chorusMax = s.Energy
		}
		if strings.HasPrefix(s.Name, "verse") && s.Energy > verseMax {
			verseMax = s.Energy
		}
		total += s.Bars
	}
	if total != b.Bars {
		add("各段 bars 合计 %d ≠ 全曲 %d——段落要恰好铺满整曲", total, b.Bars)
	}
	if chorusMax == 0 {
		add("缺副歌：至少要有一段 chorus/chorus2（PV 的能量锚点）")
	} else if chorusMax < 4 {
		add("副歌能量 %d < 4——能量弧立不起来", chorusMax)
	}
	if chorusMax > 0 && verseMax > 0 && chorusMax-verseMax < 2 {
		add("副歌-主歌能量差 %d < 2，段落对比不足", chorusMax-verseMax)
	}
	return errs
}

func pvKeyBuiltIn(k string) bool {
	for _, v := range pvKeys {
		if v == k {
			return true
		}
	}
	return false
}

func pvSectionBuiltIn(s string) bool {
	for _, v := range pvSections {
		if v == s {
			return true
		}
	}
	return false
}

// musicMap 地图 JSON 的读取投影（只要判读要用的字段，beats 大数组不进会话）。
type musicMap struct {
	Name          string  `json:"name"`
	Key           string  `json:"key"`
	Bpm           int     `json:"bpm"`
	BeatPeriod    float64 `json:"beat_period"`
	BarSeconds    float64 `json:"bar_seconds"`
	Duration      float64 `json:"duration"`
	ScoreDuration float64 `json:"score_duration"`
	Sections      []struct {
		Name     string  `json:"name"`
		StartBar int     `json:"start_bar"`
		EndBar   int     `json:"end_bar"`
		Start    float64 `json:"start"`
		End      float64 `json:"end"`
		Energy   int     `json:"energy"`
	} `json:"sections"`
}

func loadMusicMap(p *pipeline.Project) (*musicMap, error) {
	var m musicMap
	if err := readJSONFile(musicMapPath(p), &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// MusicPrepView 三件套状态与判读摘要（api prepView 的 music 字段）：brief/map
// 未落盘的键缺省，audio 是曲子 mp3 在盘标记（试听走 /music/audio 路由）。
func MusicPrepView(p *pipeline.Project) map[string]any {
	out := map[string]any{"brief": false, "audio": false}
	if _, err := os.Stat(musicBriefPath(p)); err == nil {
		out["brief"] = true
		var br pvBrief
		if err := readJSONFile(musicBriefPath(p), &br); err == nil {
			secs := make([]map[string]any, 0, len(br.Sections))
			for _, s := range br.Sections {
				secs = append(secs, map[string]any{"name": s.Name, "bars": s.Bars, "energy": s.Energy})
			}
			out["brief_summary"] = map[string]any{
				"name": br.Name, "key": br.Key, "bpm": br.Bpm, "bars": br.Bars, "sections": secs,
			}
		}
	}
	if _, err := os.Stat(musicTrackPath(p)); err == nil {
		out["audio"] = true
	}
	if mp, err := loadMusicMap(p); err == nil {
		secs := make([]map[string]any, 0, len(mp.Sections))
		for _, s := range mp.Sections {
			secs = append(secs, map[string]any{"name": s.Name, "start": s.Start, "end": s.End, "energy": s.Energy})
		}
		out["map"] = map[string]any{
			"name": mp.Name, "key": mp.Key, "bpm": mp.Bpm,
			"duration": mp.Duration, "sections": secs,
		}
	}
	return out
}

// composeRun 做曲工具的执行底座（测试注入点）：跑 ai/make_pv_track.py
// --brief --out，返回 (stdout/stderr 尾部, 退出码, 错误)。
var composeRun = func(ctx context.Context, d Deps, p *pipeline.Project, briefPath string) (string, int, error) {
	if d.RootDir == "" {
		return "", 0, errors.New("RootDir 未接线（跑不了 ai/ 脚本）")
	}
	cctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	cmd := pipeline.CommandContext(cctx, pipeline.PythonExe(),
		filepath.Join(d.RootDir, "ai", "make_pv_track.py"),
		"--brief", briefPath, "--out", p.Artifact("music"))
	cmd.Dir = d.RootDir
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	out := pipeline.TailLines(buf.String(), 16)
	if err == nil {
		return out, 0, nil
	}
	if cctx.Err() != nil {
		return out, 0, cctx.Err()
	}
	// 退出码 ≥0 是预期通路（2 自检未过 / 3 brief 校验未过），走返回值不走 error；
	// 只有起不来进程这类故障才算 error（-1）。
	if code := exitCodeOf(err); code >= 0 {
		return out, code, nil
	}
	return out, -1, fmt.Errorf("make_pv_track 启动失败: %w", err)
}

// exitCodeOf 提取进程退出码（*exec.ExitError）；其余错误按 -1（工具故障）。
func exitCodeOf(err error) int {
	var ee interface{ ExitCode() int }
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

const musicSystem = `你是音乐制作人，为音乐驱动 PV 造一首纯音乐整曲（无人声无歌词）。
流程：把项目主题变成曲 brief（结构化编曲意图）→ 调 compose_track 做曲并产出音乐地图 → 对照自检 → submit_music 提交。
规则：
- 音乐地图是分镜唯一认的物理事实。你只准调工具产出它，绝不手编数据；你的创作全部体现在 brief 里。
- brief 词汇表圈死在 compose_track 参数 schema 里（段落名/能量 1–5/BPM 带/和弦记号）。工具会做确定性映射：段落原型 × 能量缩放，你不碰音符层。
- 能量弧要立起来：intro 铺垫、verse 叙事、build 爬坡、chorus 爆发、break/bridge 喘息回落、outro 收尾——按主题选段落弧线，别机械套模板。副歌能量至少 4 且比主歌高 2 级以上。
- 自检环：compose_track 返回的地图数字必须对回你的 brief 意图（BPM/时长/段落能量）。对不上就改 brief 重做（做曲封顶 3 次调用）；超限仍有偏差，用 submit_music 带伤提交并在 deviations 写明差在哪。
- 段落 bars 总和必须恰好铺满全曲 bars；按 bars×bpm 倒推时长（PV 带 45–115 秒），用户想法里点了时长就按它选型。
唯一出口是 submit_music。`

func runMusicStation(ctx context.Context, p *pipeline.Project, d Deps) error {
	log := openSessionLog(p, StationMusic)
	defer log.close()
	loop := &Loop{
		Prov:      mustTextProv(),
		System:    musicSystem,
		Kickoff:   musicKickoff(p),
		Tools:     musicTools(p, d),
		Submit:    "submit_music",
		MaxRounds: musicBudget,
		OnStep:    manifestStep(p, StationMusic, log),
	}
	d.emit(p.ID, "stage", "prep/music", "running")
	if err := runTracked(ctx, p, "prep.music", "准备①音乐", loop); err != nil {
		p.Manifest("prep.music_error", pipeline.FirstLine(err.Error(), 200))
		return fmt.Errorf("音乐工位: %w", err)
	}
	p.Manifest("prep.music_saved", "三件套落盘（brief/曲/地图）")
	return nil
}

func musicKickoff(p *pipeline.Project) string {
	topic := strings.TrimSpace(noteFile(p, IdeaFile))
	if topic == "" {
		b, err := os.ReadFile(p.Artifact("project.json"))
		if err == nil {
			var meta struct {
				Topic string `json:"topic"`
			}
			if json.Unmarshal(b, &meta) == nil {
				topic = strings.TrimSpace(meta.Topic)
			}
		}
	}
	k := "开工。项目主题：" + topic
	if sec := targetSeconds(p); sec >= pvDurLo && sec <= pvDurHi {
		k += fmt.Sprintf("。用户目标时长约 %.0f 秒——按它倒推 bars×bpm 选型", sec)
	}
	return k + "。先写 brief 再调 compose_track。"
}

func musicTools(p *pipeline.Project, d Deps) []Tool {
	composes := 0
	return []Tool{
		{
			Def: toolSchema("compose_track", "按 brief 做曲：确定性映射成编曲（段落原型×能量缩放）→ FluidSynth 渲染 → 谱面直出音乐地图 → 机械自检。brief 校验不过/自检未过都会逐条报错回来，改参重调。", map[string]any{
				"type": "object",
				"properties": map[string]any{
					"brief": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"name": map[string]any{"type": "string", "description": "曲名（2–40 个字母/数字/下划线/连字符，当 mp3 文件名）"},
							"key":  map[string]any{"type": "string", "description": "调性，缺省 Am。内置进行的 key：" + strings.Join(pvKeys, "/")},
							"bpm":  map[string]any{"type": "integer", "description": fmt.Sprintf("速度 %d–%d", pvBpmLo, pvBpmHi)},
							"bars": map[string]any{"type": "integer", "description": fmt.Sprintf("全曲小节数 %d–%d（与 bpm 共同决定时长，PV 带 45–115 秒）", pvBarsLo, pvBarsHi)},
							"prog": map[string]any{
								"type": "array", "description": "和弦进行（可选；缺省用 key 的内置进行）。一轮循环平铺全曲，一轮小节数不得超过全曲 bars",
								"items": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"sym":  map[string]any{"type": "string", "description": "和弦记号，如 Am/F/Cmaj7/Dm7/G/Esus4"},
										"bars": map[string]any{"type": "integer", "description": "该和弦占几小节（1–8）"},
									},
									"required": []string{"sym", "bars"},
								},
							},
							"sections": map[string]any{
								"type": "array", "description": "段落表（按序编排，bars 合计必须恰好铺满全曲）。能量弧：副歌 ≥4 且比主歌高 ≥2 级",
								"items": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"name":   map[string]any{"type": "string", "description": "段落原型：" + strings.Join(pvSections, "/")},
										"bars":   map[string]any{"type": "integer", "description": "本段小节数（2–16）"},
										"energy": map[string]any{"type": "integer", "description": "能量 1–5：驱动声部力度与鼓组密度"},
									},
									"required": []string{"name", "bars", "energy"},
								},
							},
						},
						"required": []string{"bpm", "bars", "sections"},
					},
				},
				"required": []string{"brief"},
			}),
			Run: func(ctx context.Context, args string) Observation {
				if composes >= musicComposeCap {
					return Observation{Text: fmt.Sprintf("做曲次数已封顶（%d 次）：对照 read_music_map 的现状，用 submit_music 带伤提交并在 deviations 写明偏差。", musicComposeCap)}
				}
				var in struct {
					Brief *pvBrief `json:"brief"`
				}
				if err := decodeArgs(args, &in); err != nil || in.Brief == nil {
					if err == nil {
						err = errors.New("缺 brief")
					}
					return Observation{Text: "参数错误：" + err.Error()}
				}
				if errs := validatePVBrief(in.Brief); len(errs) > 0 {
					return Observation{Text: "brief 校验未过（逐条修完再调）：\n- " + strings.Join(errs, "\n- ")}
				}
				if in.Brief.Name == "" {
					in.Brief.Name = "pv_" + p.ID
				}
				if in.Brief.Key == "" {
					in.Brief.Key = "Am"
				}
				composes++
				b, err := json.Marshal(in.Brief)
				if err != nil {
					return Observation{Text: "brief 序列化失败：" + err.Error()}
				}
				briefPath := musicBriefPath(p)
				if err := os.MkdirAll(filepath.Dir(briefPath), 0o755); err != nil {
					return Observation{Text: "brief 落盘失败：" + err.Error()}
				}
				if err := os.WriteFile(briefPath, b, 0o644); err != nil {
					return Observation{Text: "brief 落盘失败：" + err.Error()}
				}
				out, code, err := composeRun(ctx, d, p, briefPath)
				if err != nil {
					return Observation{Text: "做曲工具故障（可原样重调一次，连续故障说明环境缺件）：\n" + firstLines(out) + err.Error()}
				}
				switch code {
				case 0:
					mp, err := loadMusicMap(p)
					if err != nil {
						return Observation{Text: "地图读取失败（工具账不对，请原样重调）：" + err.Error()}
					}
					return Observation{Text: "做曲完成，自检环 PASS。" + formatMapForPeers(mp) + "\n对照你的 brief 意图复核一遍，没问题就 submit_music。"}
				case 2:
					return Observation{Text: "自检环未过（地图↔brief 对不上，可改参重做或带伤提交）：\n" + out}
				case 3:
					return Observation{Text: "brief 校验未过（工具侧权威复核，逐条修）：\n" + out}
				default:
					return Observation{Text: "做曲失败（退出码 " + fmt.Sprint(code) + "）：\n" + out}
				}
			},
		},
		{
			Def: toolSchema("read_music_map", "读取当前音乐地图（BPM/时长/段落表/能量弧）。compose_track 成功后已自动回显，这里用于重做后复核或提交前终查。", map[string]any{
				"type": "object", "properties": map[string]any{},
			}),
			Run: func(context.Context, string) Observation {
				mp, err := loadMusicMap(p)
				if err != nil {
					return Observation{Text: "还没有地图：先 compose_track。" + err.Error()}
				}
				return Observation{Text: formatMapForPeers(mp)}
			},
		},
		{
			Def: toolSchema("submit_music", "提交音乐三件套（唯一出口）：曲+地图+brief 已落盘，带一句话总结与已知偏差。", map[string]any{
				"type": "object",
				"properties": map[string]any{
					"summary":    map[string]any{"type": "string", "description": "一句话：什么主题、什么结构、能量弧怎么走的"},
					"deviations": map[string]any{"type": "string", "description": "与 brief 意图的已知偏差（带伤提交时必填，无则空）"},
				},
				"required": []string{"summary"},
			}),
			Run: func(_ context.Context, args string) Observation {
				var in struct {
					Summary    string `json:"summary"`
					Deviations string `json:"deviations"`
				}
				if err := decodeArgs(args, &in); err != nil {
					return Observation{Text: "参数错误：" + err.Error()}
				}
				if strings.TrimSpace(in.Summary) == "" {
					return Observation{Text: "summary 不能空：一句话说明曲子结构与能量弧。"}
				}
				if _, err := os.Stat(musicTrackPath(p)); err != nil {
					return Observation{Text: "曲子不在盘上：先 compose_track（做曲成功才有 mp3）。"}
				}
				mp, err := loadMusicMap(p)
				if err != nil {
					return Observation{Text: "地图不在盘上：先 compose_track。" + err.Error()}
				}
				detail := fmt.Sprintf("%s｜%s %dBPM %.1fs %d 段", in.Summary, mp.Key, mp.Bpm, mp.Duration, len(mp.Sections))
				if strings.TrimSpace(in.Deviations) != "" {
					detail += "｜偏差：" + in.Deviations
				}
				p.Manifest("music.saved", pipeline.FirstLine(detail, 200))
				d.emit(p.ID, "stage", "prep/music",
					fmt.Sprintf("done：%s %dBPM %.0fs %d 段", mp.Key, mp.Bpm, mp.Duration, len(mp.Sections)))
				return Observation{
					Text: fmt.Sprintf("音乐三件套已提交：%s %dBPM / %.1f 秒 / %d 段。等待打包终审（试听后确认）。",
						mp.Key, mp.Bpm, mp.Duration, len(mp.Sections)),
					Done: true,
				}
			},
		},
	}
}

// formatMapForPeers 地图的判读视图（beats 大数组不进会话——agent 用段落/能量
// 层做判断，毫秒级拍格是分镜与卡点闸的事）。
func formatMapForPeers(mp *musicMap) string {
	var b strings.Builder
	fmt.Fprintf(&b, "地图：%s｜%s 调｜%dBPM｜%.1f 秒（谱面 %.1f）｜一小节 %.2fs、一拍 %.0fms\n",
		mp.Name, mp.Key, mp.Bpm, mp.Duration, mp.ScoreDuration, mp.BarSeconds, mp.BeatPeriod*1000)
	for _, s := range mp.Sections {
		fmt.Fprintf(&b, "- %-8s %6.1f–%-6.1f 能量 %d（bar %d–%d）\n",
			s.Name, s.Start, s.End, s.Energy, s.StartBar, s.EndBar)
	}
	return b.String()
}

func firstLines(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return s + "\n"
}
