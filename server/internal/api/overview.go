// overview：GET /projects/:id/overview —— 项目全景只读视图。
// 一次回答五件事：现在什么状态（stages/last_run/producing）、每个环节跑多久
// （durations，produce 侧 stage.start/stage.finish 配对累加）、尝试过几次（jobs，
// render_jobs 镜像）、产物都在哪（artifacts，约定布局存在性扫描）、经历了什么
// （events，manifest 尾部）。磁盘是事实源：manifest/目录不会撒谎，本视图全部
// 现算，不落任何库。
package api

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"webpage-video-agent/server/internal/pipeline"
	"webpage-video-agent/server/internal/produce"
	"webpage-video-agent/server/internal/store"
)

type manifestEvent struct {
	TS     string `json:"ts"`
	MS     int64  `json:"ms,omitempty"` // unix 毫秒（前端算每步用时；老条目缺省时按 ts 兜底解析）
	Event  string `json:"event"`
	Detail string `json:"detail,omitempty"`
}

type lastRun struct {
	Kind    string  `json:"kind"` // produce | rework
	State   string  `json:"state"`
	Detail  string  `json:"detail,omitempty"`
	TS      string  `json:"ts,omitempty"`
	Seconds float64 `json:"seconds,omitempty"` // start→终态（或至今）
}

// scanManifest 单遍扫 manifest.jsonl：
//   - stages：各阶段终态（pipeline.Run 的 stage.done|error|await|todo，last-wins；
//     重放跳过的阶段秒写 done 属正常——真实耗时看 durations）
//   - durations：最近一轮里各环节耗时（produce 侧 stage.start/stage.finish 配对，
//     check 多轮修复累加；只统计最近一轮 start 之后的事件）
//   - run：最近一轮制作状态（有 start 无终态 = running）
//   - tail：事件尾部 20 条
//
// 只读展示；对账与终态汇报的权威解析在 produce/agent，此处不做业务判断。
func scanManifest(b []byte) (stages map[string]string, run *lastRun, durations map[string]float64, tail []manifestEvent) {
	stages = map[string]string{}
	durations = map[string]float64{}
	open := map[string]string{} // 配对中的 stage → 起 ts
	type mark struct {
		idx int
		ev  manifestEvent
	}
	var start, term *mark
	inRun := false // 最近一轮 start..终态窗口（窗口外不计时）
	for i, line := range strings.Split(string(b), "\n") {
		var ev manifestEvent
		if json.Unmarshal([]byte(line), &ev) != nil || ev.Event == "" {
			continue
		}
		tail = append(tail, ev)
		switch {
		case strings.HasPrefix(ev.Event, "stage."):
			suffix := strings.TrimPrefix(ev.Event, "stage.")
			name := ev.Detail // done/todo 载荷是阶段名；error/await 是「名: 信息」
			if j := strings.Index(name, ":"); j >= 0 {
				name = name[:j]
			}
			if name = strings.TrimSpace(name); name == "" {
				break
			}
			switch suffix {
			case "done", "error", "await", "todo":
				stages[name] = suffix
			case "start":
				if inRun {
					open[name] = ev.TS // 同环节多轮（check 修复）：重开计时，上一轮须已 finish
				}
			case "finish":
				if inRun {
					if ts0, ok := open[name]; ok {
						if d := tsDelta(ts0, ev.TS); d > 0 {
							durations[name] = round1(durations[name] + d)
						}
						delete(open, name)
					}
				}
			}
		case ev.Event == produce.EventProduceStart || ev.Event == produce.EventReworkStart:
			start = &mark{i, ev}
			term = nil
			inRun = true
			clear(open)
			clear(durations) // 每轮重开：durations 恒指最近一轮
		case ev.Event == produce.EventProduceDone, ev.Event == produce.EventProduceError, ev.Event == produce.EventProduceCancelled, ev.Event == produce.EventProduceInterrupted,
			ev.Event == produce.EventReworkDone, ev.Event == produce.EventReworkError, ev.Event == produce.EventReworkCancelled, ev.Event == produce.EventReworkInterrupted:
			term = &mark{i, ev}
			inRun = false
		}
	}
	if len(tail) > 20 {
		tail = tail[len(tail)-20:]
	}
	switch {
	case start != nil && (term == nil || start.idx > term.idx):
		run = &lastRun{Kind: runKind(start.ev.Event), State: "running", TS: start.ev.TS}
		if t0 := parseTS(start.ev.TS); !t0.IsZero() {
			run.Seconds = round1(time.Since(t0).Seconds())
			for name, ts0 := range open { // 进行中的环节给已耗时
				if t0 := parseTS(ts0); !t0.IsZero() {
					durations[name] = round1(time.Since(t0).Seconds())
				}
			}
		}
	case term != nil:
		e := term.ev.Event
		run = &lastRun{Kind: runKind(e), State: strings.SplitN(e, ".", 2)[1], Detail: term.ev.Detail, TS: term.ev.TS}
		if start != nil {
			run.Seconds = round1(tsDelta(start.ev.TS, term.ev.TS))
		}
	}
	return stages, run, durations, tail
}

// parseTS/tsDelta manifest 的 RFC3339 时间戳（本地时区带偏移，解析即绝对时刻）。
func parseTS(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func tsDelta(a, b string) float64 {
	ta, tb := parseTS(a), parseTS(b)
	if ta.IsZero() || tb.IsZero() {
		return -1
	}
	return tb.Sub(ta).Seconds()
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }

func runKind(event string) string {
	if strings.HasPrefix(event, "rework.") {
		return "rework"
	}
	return "produce"
}

type artifactEntry struct {
	Kind  string `json:"kind"`
	Path  string `json:"path"` // 项目目录内相对路径（正斜杠），可直接拼 URL/命令
	Bytes int64  `json:"bytes"`
	MTime string `json:"mtime"`
}

// scanArtifacts 约定布局存在性扫描：在的产物才出现，缺的不占位——目录不会撒谎。
// 逐文件登记（audio/segNN.wav、renders/segs/segNN.mp4 …），report 类按子目录聚合。
func scanArtifacts(p *pipeline.Project) []artifactEntry {
	var out []artifactEntry
	entry := func(kind, rel string, st os.FileInfo) {
		out = append(out, artifactEntry{kind, filepath.ToSlash(rel), st.Size(), st.ModTime().Format(time.RFC3339)})
	}
	add := func(kind, rel string) {
		if st, err := os.Stat(p.Artifact(rel)); err == nil && !st.IsDir() {
			entry(kind, rel, st)
		}
	}
	glob := func(kind, dir, pattern string) {
		ms, _ := filepath.Glob(filepath.Join(p.Dir, dir, pattern))
		sort.Strings(ms)
		for _, m := range ms {
			if st, err := os.Stat(m); err == nil && !st.IsDir() {
				rel, _ := filepath.Rel(p.Dir, m)
				entry(kind, rel, st)
			}
		}
	}

	add("manifest", "manifest.jsonl")
	add("project_meta", "project.json")
	add("source_article", "input/article.txt")
	add("manuscript", "manuscripts/manuscript.json")
	add("storyboard", "storyboards/storyboard.json")
	add("style", "style/style_samples.json")
	add("style", "style/skin.json")
	add("audio_meta", "audio_meta.json")
	add("timeline", "renders/timeline.json")
	add("final_video", "renders/main.mp4")
	add("voice_master", "renders/main_voice.mp4") // 人声母版（stitch 后期妆之前）
	glob("audio", "audio", "seg*.wav")
	glob("scene_spec", "llm", "comp-seg*.json")
	glob("segment_source", ".hf-seg", "seg*.html") // assemble 生成的单段渲染工程
	glob("segment_html", filepath.Join("compositions", "frames"), "seg*.html")
	glob("segment_video", filepath.Join("renders", "segs"), "seg*.mp4")

	// report 类按子目录聚合（frameqa 抽帧 / replay 回放比对，文件多而无逐个价值）
	if dirs, err := os.ReadDir(p.Artifact("reports")); err == nil {
		names := make([]string, 0, len(dirs))
		for _, d := range dirs {
			if d.IsDir() {
				names = append(names, d.Name())
			}
		}
		sort.Strings(names)
		for _, n := range names {
			rel := filepath.ToSlash(filepath.Join("reports", n))
			var total int64
			var latest time.Time
			_ = filepath.WalkDir(p.Artifact(rel), func(_ string, d os.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					if fi, e := d.Info(); e == nil {
						total += fi.Size()
						if fi.ModTime().After(latest) {
							latest = fi.ModTime()
						}
					}
				}
				return nil
			})
			out = append(out, artifactEntry{"report", rel, total, latest.Format(time.RFC3339)})
		}
	}
	return out
}

// tailManifest manifest 尾部 n 条（坏行剔除，时间正序）。activity 接口（事件流
// 面板 500 条全量回放）的数据口。尾部窗口读走 produce.ReadJSONLTail：manifest
// 随制作轮次无限增长，逐次轮询都全量读+解析会让 I/O 随历史线性上涨。
// 恒返回非 nil（读不了 = 空切片）：空列表必须序列化成 [] 不能 null（前端直吃）。
func tailManifest(path string, n int) []manifestEvent {
	lines, err := produce.ReadJSONLTail(path, n)
	out := make([]manifestEvent, 0, len(lines))
	if err != nil {
		return out
	}
	// 坏行（撕裂/半行）与空事件行照旧剔除——unmarshalJSONL 只滤解析失败，
	// 这里还要求 event 字段非空（口径与 scanManifest 一致）
	for _, line := range lines {
		var e manifestEvent
		if json.Unmarshal(line, &e) != nil || e.Event == "" {
			continue
		}
		out = append(out, e)
	}
	return out
}

// activity 制作活动流：GET /projects/:id/activity —— manifest.jsonl 原样事件
// （尾部 500 条，时间正序）。前端中间区右上角事件流面板的数据源：每步用时由
// stage.start/finish 的毫秒差算出，打回（check.repair / prereview / frameqa）
// 与终态事件原样给前端翻译。轻量只读（不像 overview 全树扫产物），供制作中轮询。
// 无 manifest（新建项目没开过工）回 [] 而不是 404/null：前端 .length/.map 直吃。
func (s *Server) activity(c *gin.Context) {
	p, ok := s.project(c)
	if !ok {
		return
	}
	// 无 manifest（新建项目没开过工）回 [] 而不是 404/null：前端 .length/.map 直吃
	c.JSON(200, tailManifest(p.Artifact("manifest.jsonl"), 500))
}

func (s *Server) overview(c *gin.Context) {
	id := c.Param("id")
	p, ok := s.project(c) // 与其余 :id 接口同语义：不存在的项目统一 404
	if !ok {
		return
	}
	name, status, aspect, topic, created := "", "", "16:9", "", ""
	if b, err := os.ReadFile(p.Artifact("project.json")); err == nil {
		var meta struct {
			Name      string `json:"name"`
			Status    string `json:"status"`
			Aspect    string `json:"aspect"`
			Topic     string `json:"topic"`
			CreatedAt string `json:"created_at"`
		}
		if json.Unmarshal(b, &meta) == nil {
			name, status, topic, created = meta.Name, meta.Status, meta.Topic, meta.CreatedAt
			if meta.Aspect != "" {
				aspect = meta.Aspect
			}
		}
	}
	producing := s.Producer.IsRunning(id)
	if producing {
		status = "producing"
	} else if _, err := os.Stat(p.Artifact("renders/main.mp4")); err == nil {
		status = "video" // 与项目列表同口径：成片在即 video
	}
	b, _ := os.ReadFile(p.Artifact("manifest.jsonl"))
	stages, run, durations, events := scanManifest(b)
	jobs, err := s.Store.ListRenderJobs(id)
	if err != nil { // 库不可用不挡全景：jobs 给空，manifest 侧 last_run 仍在
		jobs = nil
	}
	if jobs == nil {
		// 空必须回 [] 不能回 null（与 activity/messages 同一纪律）：前端直接
		// .length/.map，null 就是白屏
		jobs = []store.RenderJobRow{}
	}
	artifacts := scanArtifacts(p)
	if artifacts == nil {
		artifacts = []artifactEntry{}
	}
	c.JSON(200, gin.H{
		"id": id, "name": name, "status": status, "aspect": aspect, "topic": topic, "created_at": created,
		"producing": producing,
		"stages":    stages,
		"last_run":  run,
		"durations": durations, // 最近一轮各环节耗时（秒）；进行中的环节给已耗时
		"jobs":      jobs,
		"artifacts": artifacts,
		"events":    events,
	})
}
