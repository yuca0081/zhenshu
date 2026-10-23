package prep

// artifacts.go —— 三工件的读写与失效级联。工件路径是全链路契约：
//
//	scripts/script.json      工件 A（文字稿）
//	style/stylepack.json     工件 B（风格包）
//	storyboards/draft.json   工件 C（分镜稿）
//
// 每个工位的 submit 同时写出对应的 legacy 兼容产物（manuscript.json /
// style_samples.json / storyboard.json），制作阶段零改动（翻译见 contract.Translate）。
// InvalidateFrom 实现反馈路由（终审后：改文字→重①（②③级联）/ 改风格→重②③ /
// 改结构场景→重③）与编辑路径的过期清理。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"webpage-video-agent/server/internal/artifacts"
	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/pipeline"
)

// cloudStore 产物正本后端（SetCloudStore 注入）：InvalidateFrom 作废本地工件时同步删
// 远端正本——不删的话，清理过的老项目回源工作区会把陈旧工件带回来（旧样张复活）。
var cloudStore artifacts.Store

// SetCloudStore 注入产物存储后端（server 启动时调用；不注入 = 纯本地现状）。
func SetCloudStore(s artifacts.Store) { cloudStore = s }

// 工位名（反馈路由的寻址单位）。
const (
	StationMusic      = "music"
	StationScript     = "script"
	StationStyle      = "style"
	StationStoryboard = "storyboard"
)

func scriptPath(p *pipeline.Project) string     { return p.Artifact("scripts/script.json") }
func stylePackPath(p *pipeline.Project) string  { return p.Artifact("style/stylepack.json") }
func draftPath(p *pipeline.Project) string      { return p.Artifact("storyboards/draft.json") }
func styleDraftPath(p *pipeline.Project) string { return p.Artifact("style/prep-draft.json") }

// ReplaceManuscript 文稿真源替换的统一语义（api saveManuscript 与 agent
// saveManuscriptContent 两入口共用——2026-10-19 中-34：两处手抄同一套「写
// article.txt → 删 manuscript.json → 有①工件则级联作废」，注释互相声称同
// 语义实际已漂移，收进一处）：article.txt 是唯一真源；manuscript.json 是
// 重放产物必须删（不删的话重新制作时 TTS 拿的还是旧稿）。返回是否触发了
// ①②③级联作废（调用方各自包装提示语）。
func ReplaceManuscript(p *pipeline.Project, content string) (invalidated bool, err error) {
	if err := os.WriteFile(p.Artifact("input/article.txt"), []byte(content), 0o644); err != nil {
		return false, err
	}
	_ = os.Remove(p.Artifact("manuscripts/manuscript.json"))
	if HasArtifact(p, StationScript) {
		InvalidateFrom(p, StationScript)
		return true, nil
	}
	return false, nil
}

// LoadScript 读工件 A（不存在返回 os.ErrNotExist 语义错误）。
func LoadScript(p *pipeline.Project) (*contract.Script, error) {
	var s contract.Script
	if err := readJSONFile(scriptPath(p), &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// LoadStylePack 读工件 B。
func LoadStylePack(p *pipeline.Project) (*contract.StylePack, error) {
	var sp contract.StylePack
	if err := readJSONFile(stylePackPath(p), &sp); err != nil {
		return nil, err
	}
	return &sp, nil
}

// LoadDraft 读工件 C。
func LoadDraft(p *pipeline.Project) (*contract.StoryboardDraft, error) {
	var d contract.StoryboardDraft
	if err := readJSONFile(draftPath(p), &d); err != nil {
		return nil, err
	}
	return &d, nil
}

func readJSONFile(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s 解析失败: %w", filepath.Base(path), err)
	}
	return nil
}

func writeJSONFile(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return pipeline.WriteFileAtomic(path, pipeline.MustJSONBytes(v), 0o644)
}

// HasArtifact 工件是否已落盘（重放语义：存在即跳过）。
func HasArtifact(p *pipeline.Project, station string) bool {
	_, err := os.Stat(artifactPath(p, station))
	return err == nil
}

func artifactPath(p *pipeline.Project, station string) string {
	switch station {
	case StationMusic:
		return musicMapPath(p)
	case StationScript:
		return scriptPath(p)
	case StationStyle:
		return stylePackPath(p)
	case StationStoryboard:
		return draftPath(p)
	}
	return ""
}

// InvalidateFrom 反馈路由的失效半边：从 station 起连同下游全部作废。
// station 本身待重跑；上游工件保留。设计/样张衍生品一锅端（与 applyStyle 的
// 作废清单同口径，防旧图配新包）。
func InvalidateFrom(p *pipeline.Project, station string) {
	var files []string
	switch station {
	case StationMusic:
		// PV 音乐工位：三件套一锅端。下游（分镜/制作）M2 才接，暂无级联。
		files = []string{musicBriefPath(p), musicMapPath(p), musicTrackPath(p)}
	case StationScript:
		files = []string{
			scriptPath(p), stylePackPath(p), draftPath(p),
		}
	case StationStyle:
		files = []string{stylePackPath(p), draftPath(p)}
	case StationStoryboard:
		files = []string{draftPath(p)}
	default:
		return
	}
	// 设计/样张衍生品：style 工位及更早失效时全部作废（storyboard/music 失效不碰设计）
	if station == StationScript || station == StationStyle {
		files = append(files,
			p.Artifact("style/style_samples.json"), p.Artifact("style/skin.json"),
			p.Artifact("style/design.md"), p.Artifact("style/prep-draft.json"),
			p.Artifact("style/style_brief.json"), p.Artifact("llm/style_samples.json"),
			p.Artifact("style/sample-0.png"), p.Artifact("style/sample-1.png"),
			p.Artifact("style/sample-2.png"),
		)
	}
	for _, f := range files {
		_ = os.Remove(f)
	}
	if station == StationScript || station == StationStyle {
		_ = os.RemoveAll(p.Artifact("style/clips"))
		_ = os.RemoveAll(p.Artifact(".hf-clip"))
		_ = os.RemoveAll(p.Artifact(".hf-sample"))
	}
	// legacy 分镜随 draft 作废（翻译产物，重跑后重写）
	if station != "" && station != StationMusic {
		_ = os.Remove(p.Artifact("storyboards/storyboard.json"))
	}
	// 远端正本跟着作废（best-effort）：防清理过的老项目回源时陈旧工件复活
	if cloudStore != nil && cloudStore.Available() {
		var keys []string
		for _, f := range files {
			if rel, err := filepath.Rel(p.Dir, f); err == nil && rel != ".." {
				keys = append(keys, artifacts.ProjectKey(p.ID, filepath.ToSlash(rel)))
			}
		}
		artifacts.DeleteKeys(cloudStore, keys...)
		artifacts.DeletePrefixBestEffort(cloudStore, artifacts.ProjectKey(p.ID, "style/clips"))
	}
	p.Manifest("prep.invalidate", station)
}

// StyleStale 风格包是否已落后于用户最新选定/样张现状。applyStyle 家族（agent 工具
// 与 REST 同清单）换条目时：重写选定简报（style_brief.json）+清样张，但不动
// stylepack.json——包就挂在了旧条目上；「出样张」和「确认打包」两头若都只查包
// 在不在，就会拿旧设计放行（2026-10-17 彩虹项目实测：两次出样张无声短路）。
// 判定两条：①选定简报的条目 ≠ 包的 style_id（用户换选了）；②样张全没（②交卷
// 后至少 1 张，render_sample 固定落 style/sample-N.png）。没有风格包 = ②未完成，
// 不算过期——调用方各自走「未出」路径。
func StyleStale(p *pipeline.Project) (bool, string) {
	sp, err := LoadStylePack(p)
	if err != nil {
		return false, ""
	}
	if b := pipeline.LoadStyleBrief(p); b != nil && b.EntryID != sp.StyleID {
		name := b.EntryName
		if name == "" {
			name = b.EntryID
		}
		return true, fmt.Sprintf("用户已把风格换选为库内条目「%s」（%s），现有风格包还挂在旧条目 %s 上——必须按新选定条目重出风格包与样张（style_id 用 %s）",
			name, b.EntryID, sp.StyleID, b.EntryID)
	}
	if _, err := os.Stat(p.Artifact("style/sample-0.png")); err != nil {
		if _, err := os.Stat(p.Artifact("style/sample-1.png")); err != nil {
			if _, err := os.Stat(p.Artifact("style/sample-2.png")); err != nil {
				return true, "样张已作废（换选风格或清产物）但风格包还在——按现选定风格重出风格包与样张"
			}
		}
	}
	return false, ""
}

// targetSeconds 目标时长（project.json target_seconds；缺省 120s ≈ 540 字）。
func targetSeconds(p *pipeline.Project) float64 {
	b, err := os.ReadFile(p.Artifact("project.json"))
	if err != nil {
		return 120
	}
	var meta struct {
		TargetSeconds float64 `json:"target_seconds"`
	}
	if json.Unmarshal(b, &meta) != nil || meta.TargetSeconds <= 0 {
		return 120
	}
	return meta.TargetSeconds
}

// sessionLog 会话日志（llm/prep-<station>.jsonl）：phase 级回放的依据。
// 每步一行 {ts, round, kind, detail}；detail 截断防网页正文灌爆日志。
type sessionLog struct {
	p    *pipeline.Project
	name string
	fh   *os.File
}

func openSessionLog(p *pipeline.Project, station string) *sessionLog {
	path := p.Artifact("llm/prep-" + station + ".jsonl")
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	fh, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return &sessionLog{p: p, name: station} // 日志开不出来不挡主链路
	}
	return &sessionLog{p: p, name: station, fh: fh}
}

func (s *sessionLog) step(round int, kind, detail string) {
	if s.fh == nil {
		return
	}
	line, _ := json.Marshal(map[string]any{
		"ts": time.Now().Format(time.RFC3339), "round": round, "kind": kind,
		"detail": pipeline.TruncRunes(detail, 2000),
	})
	s.fh.Write(append(line, '\n'))
}

func (s *sessionLog) close() {
	if s.fh != nil {
		s.fh.Close()
	}
}

// manifestStep 每步记账钩子：manifest 一行 + 会话日志一行。
func manifestStep(p *pipeline.Project, station string, log *sessionLog) func(int, string, string) {
	return func(round int, kind, detail string) {
		p.Manifest("prep.step", fmt.Sprintf("%s r%d %s: %s", station, round, kind, pipeline.FirstLine(detail, 160)))
		log.step(round, kind, detail)
	}
}

// noteFile 读工位附注（input/style_notes.txt / input/storyboard_notes.txt）：
// 反馈重跑时的人工要求，文件在就一直生效，重新写入即替换（与 legacy 同语义）。
func noteFile(p *pipeline.Project, rel string) string {
	b, err := os.ReadFile(p.Artifact(rel))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
