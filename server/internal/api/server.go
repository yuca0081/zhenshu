// Package api：HTTP 层（Gin）。REST + SSE；磁盘是产物事实源，PG 存案卷。
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"webpage-video-agent/server/internal/agent"
	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/events"
	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/pipeline"
	"webpage-video-agent/server/internal/produce"
	"webpage-video-agent/server/internal/store"
	"webpage-video-agent/server/internal/stylelib"
	"webpage-video-agent/server/internal/styleref"
	"webpage-video-agent/server/internal/webfetch"
)

type Server struct {
	DataDir  string
	RootDir  string
	Store    store.Store
	Hub      *events.Hub
	Agent    *agent.Agent
	Producer *produce.Runner
	Lib      *stylelib.Library
	Refs     *styleref.Store

	// APIToken 非空时全部 /api 路由要求 X-API-Token 头（SSE 另收 ?token=）。
	// 部署侧公网暴露（Dockerfile ADDR=:8080）必须配置；本机默认回环可留空。
	APIToken string

	turnMu sync.Map // projectID → *sync.Mutex：同项目对话轮串行（见 chat）
}

func (s *Server) Router() *gin.Engine {
	r := gin.Default()
	r.MaxMultipartMemory = 8 << 20

	api := r.Group("/api", s.requireToken(), s.bodyGuard(), secureParams())
	api.POST("/projects", s.createProject)
	api.GET("/projects", s.listProjects)
	api.GET("/projects/:id", s.projectView)
	api.DELETE("/projects/:id", s.deleteProject)
	api.GET("/projects/:id/overview", s.overview)
	api.GET("/projects/:id/manuscript", s.manuscript)
	api.PUT("/projects/:id/manuscript", s.saveManuscript)
	api.PUT("/projects/:id/storyboard", s.saveStoryboard)
	api.GET("/projects/:id/storyboard", s.storyboard)
	api.GET("/projects/:id/style", s.style)
	api.POST("/projects/:id/style/apply", s.applyStyle)
	api.POST("/projects/:id/style/confirm", s.confirmStyle)
	api.POST("/projects/:id/chat", s.chat)
	api.POST("/draft/manuscript", s.draftManuscript)
	api.POST("/extract_link", s.extractLink)
	api.GET("/projects/:id/messages", s.messages)
	api.GET("/projects/:id/events", s.sse)
	api.POST("/projects/:id/produce", s.startProduce)
	api.POST("/projects/:id/rework", s.startRework)
	api.GET("/projects/:id/rework/last", s.reworkLast)
	api.POST("/projects/:id/cancel", s.cancelProduce)
	api.GET("/projects/:id/video/main.mp4", s.video)
	api.GET("/projects/:id/style/clips/:file", s.styleClip)
	api.GET("/projects/:id/audio_meta", s.audioMeta)
	api.GET("/library", s.listLibrary)
	api.DELETE("/library/:id", s.deleteLibraryEntry)
	api.GET("/library/:id/frames", s.libraryFrames)
	api.GET("/library/:id/media/:file", s.libraryMedia)
	api.POST("/library/:id/publish", s.publishPack)
	api.GET("/vocab", s.listVocab)
	api.POST("/references", s.uploadReference)
	api.GET("/references", s.listReferences)
	api.GET("/references/:id/frames", s.referenceFrames)
	api.GET("/references/:id/media/:file", s.referenceMedia)
	api.POST("/references/:id/analyze", s.analyzeReference)
	api.POST("/references/:id/confirm", s.confirmReference)
	api.PUT("/references/:id/draft", s.updateReferenceDraft)
	// 临时视频接口不进 tokened 组（大模型不会带 X-API-Token）：防线是 HMAC 签名 +
	// 有效期 + clip-*.mp4 白名单（styleref.VerifyVideo/VideoFile），只暴露解析剪辑。
	r.GET("/api/llm-video/:id/:name", s.llmVideo)

	// 前端产物（web/dist）存在则托管
	dist := filepath.Join(s.RootDir, "web", "dist")
	if st, err := os.Stat(dist); err == nil && st.IsDir() {
		r.Static("/assets", filepath.Join(dist, "assets"))
		r.NoRoute(func(c *gin.Context) {
			if strings.HasPrefix(c.Request.URL.Path, "/api") {
				c.JSON(404, gin.H{"error": "not found"})
				return
			}
			// SPA 入口不缓存：发版后浏览器立刻拿到新壳（带 hash 的 assets 才长期缓存）
			c.Header("Cache-Control", "no-store")
			c.File(filepath.Join(dist, "index.html"))
		})
	}
	return r
}

// requireToken API_TOKEN 鉴权：常量时间比较，防时序侧信道。留空 = 本机回环模式不设防。
// SSE（EventSource 无法自定义头）额外接受 ?token= 查询参数。
func (s *Server) requireToken() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s.APIToken == "" {
			c.Next()
			return
		}
		tok := c.GetHeader("X-API-Token")
		if tok == "" {
			tok = c.Query("token")
		}
		if tok != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(s.APIToken)) == 1 {
			c.Next()
			return
		}
		c.Header("WWW-Authenticate", `Bearer realm="zhenshu"`)
		c.JSON(401, gin.H{"error": "未授权：缺少或错误的 API token"})
		c.Abort()
	}
}

// bodyGuard 请求体上限：JSON 路由 2MB，参考视频上传 210MB（单文件 200MB 上限 + multipart 开销）。
// 无上限时一个 GB 级 body 全量进内存即可 OOM。
func (s *Server) bodyGuard() gin.HandlerFunc {
	const jsonMax = 2 << 20
	const uploadMax = 210 << 20
	return func(c *gin.Context) {
		limit := int64(jsonMax)
		if c.Request.Method == "POST" && strings.HasPrefix(c.Request.URL.Path, "/api/references") {
			limit = uploadMax
		}
		if c.Request.ContentLength > limit {
			c.JSON(413, gin.H{"error": "请求体过大"})
			c.Abort()
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		c.Next()
	}
}

// secureParams 路由参数白名单：拒绝路径分隔符与 ".."。gin 的 param 已做百分号解码，
// `..%5C..` 到这里是 `..\..`——Windows 下 filepath.Join 认 `\` 为分隔符，
// 必须在入口统一拒绝。
func secureParams() gin.HandlerFunc {
	return func(c *gin.Context) {
		for _, p := range c.Params {
			if strings.Contains(p.Value, "..") || strings.ContainsAny(p.Value, `/\`) {
				c.JSON(400, gin.H{"error": "非法路径参数"})
				c.Abort()
				return
			}
		}
		c.Next()
	}
}

// ── 项目 ─────────────────────────────────────────────────────

func (s *Server) createProject(c *gin.Context) {
	var in struct {
		Name        string `json:"name"`
		Topic       string `json:"topic"`        // 一句话主题（文稿弹窗 AI 起草的种子）
		Manuscript  string `json:"manuscript"`   // 可选：直接带稿创建（不走弹窗补）
		Aspect      string `json:"aspect"`       // 9:16（默认，plan §7 M1）| 16:9
		StyleID     string `json:"style_id"`     // 风格库条目（可空 = 不选，样张阶段模型自由发挥）
		StylePackID string `json:"stylepack_id"` // 旧字段名，过渡期兼容（applyStyle 同款双字段）
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	in.Topic = strings.TrimSpace(in.Topic)
	in.Manuscript = strings.TrimSpace(in.Manuscript)
	styleID := in.StyleID
	if styleID == "" {
		styleID = in.StylePackID
	}
	// 新建只要求名称+主题（文稿/风格是三前置，进工作区后在对话栏标签里补）；
	// 带稿直建是老路径，兼容保留。
	if in.Topic == "" && in.Manuscript == "" {
		c.JSON(400, gin.H{"error": "主题必填（一句话说清要讲什么，例：蚊子为什么嗡嗡叫）"})
		return
	}
	if in.Aspect != "16:9" && in.Aspect != "9:16" {
		in.Aspect = "9:16"
	}
	if styleID != "" {
		if pack, ok := s.Lib.Get(styleID); !ok || !pack.Published {
			c.JSON(400, gin.H{"error": "风格库条目不存在或未入库"})
			return
		}
	}
	if in.Name == "" {
		src := in.Topic
		if src == "" {
			src = strings.SplitN(in.Manuscript, "\n", 2)[0]
		}
		in.Name = src
		if r := []rune(in.Name); len(r) > 24 {
			in.Name = string(r[:24])
		}
	}
	// id 秒级时间戳：并发/连点创建会同秒撞号——目录已存在则追加序号
	id := "p" + time.Now().Format("20060102-150405")
	for n := 2; ; n++ {
		if _, err := os.Stat(pipeline.NewProject(s.DataDir, id).Dir); os.IsNotExist(err) {
			break
		}
		id = fmt.Sprintf("p%s-%d", time.Now().Format("20060102-150405"), n)
	}
	p := pipeline.NewProject(s.DataDir, id)
	for _, d := range []string{
		"input", "llm", "manuscripts", "storyboards", "style",
		"audio", "compositions/frames", "compositions/registry", "renders",
	} {
		_ = os.MkdirAll(filepath.Join(p.Dir, d), 0o755)
	}
	if in.Manuscript != "" {
		if err := os.WriteFile(p.Artifact("input/article.txt"), []byte(in.Manuscript), 0o644); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
	}
	meta, _ := json.MarshalIndent(map[string]any{
		"id": id, "name": in.Name, "status": "created", "aspect": in.Aspect,
		"topic": in.Topic, "style_id": styleID,
		"created_at": time.Now().Format(time.RFC3339),
	}, "", "  ")
	_ = os.WriteFile(p.Artifact("project.json"), meta, 0o644)
	p.Manifest("project.created", fmt.Sprintf("%s（%s）", in.Name, in.Aspect))
	_ = s.Store.CreateProject(id, in.Name)
	s.Hub.Emit(id, "msg", "", "项目已创建："+in.Name)
	c.JSON(200, gin.H{"id": id, "name": in.Name, "aspect": in.Aspect})
}

func (s *Server) listProjects(c *gin.Context) {
	// 扫磁盘（事实源），确保 DB 有行；合并状态
	entries, _ := os.ReadDir(filepath.Join(s.DataDir, "projects"))
	type row struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Status      string `json:"status"`
		CreatedAt   string `json:"created_at"`
		HasVideo    bool   `json:"has_video"`
		LastProduce any    `json:"last_produce"`
	}
	// 预分配非 nil：空列表必须序列化成 [] 而不是 null（前端读 .length 会崩）
	out := make([]row, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), "_") {
			continue
		}
		pj := filepath.Join(s.DataDir, "projects", e.Name(), "project.json")
		b, err := os.ReadFile(pj)
		if err != nil {
			continue
		}
		var meta struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			Status    string `json:"status"`
			CreatedAt string `json:"created_at"`
		}
		if json.Unmarshal(b, &meta) != nil || meta.Name == "" {
			continue
		}
		_ = s.Store.EnsureProject(e.Name(), meta.Name)
		_, hasVideo := os.Stat(filepath.Join(s.DataDir, "projects", e.Name(), "renders", "main.mp4"))
		if s.Producer.IsRunning(e.Name()) {
			meta.Status = "producing"
		} else if hasVideo == nil {
			meta.Status = "video"
		}
		// last_produce：最近一轮制作终态（与 agent 状态摘要同一读法 produce.LastTerminal）。
		// status 故意不动（video/style 语义下游都吃），失败由前端按 last_produce 标红，
		// 否则「失败后被抹平成 video」的老问题换个视图继续活。
		var lastProduce any
		if ev, _, ts := produce.LastTerminal(pipeline.NewProject(s.DataDir, e.Name())); ev != "" {
			lastProduce = gin.H{"event": ev, "ts": ts}
		}
		out = append(out, row{e.Name(), meta.Name, meta.Status, meta.CreatedAt, hasVideo == nil, lastProduce})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	c.JSON(200, out)
}

func (s *Server) projectView(c *gin.Context) {
	p, ok := s.project(c)
	if !ok {
		return
	}
	id := p.ID
	st := s.Agent.State(id)
	aspect := "16:9" // 旧项目无字段，与已渲成片一致
	topic := ""
	if b, err := os.ReadFile(p.Artifact("project.json")); err == nil {
		var meta struct {
			Aspect string `json:"aspect"`
			Topic  string `json:"topic"`
		}
		if json.Unmarshal(b, &meta) == nil {
			if meta.Aspect != "" {
				aspect = meta.Aspect
			}
			topic = meta.Topic
		}
	}
	var lastProduce any
	if ev, _, ts := produce.LastTerminal(p); ev != "" {
		lastProduce = gin.H{"event": ev, "ts": ts}
	}
	c.JSON(200, gin.H{
		"id": id, "name": st.Name, "aspect": aspect, "topic": topic,
		"gates": gin.H{
			"manuscript": st.HasManuscript, "storyboard": st.HasStoryboard, "style_confirmed": st.StyleConfirmed,
			"style_draft": st.HasStyle, "style_direction": st.StyleDirection,
		},
		"seg_count": st.SegCount, "total_hint": st.TotalHint,
		"has_video": st.HasVideo, "producing": st.Producing,
		"video_stale": gin.H{ // 成片生成后上游又改过 → 前端页签黄灯 + 重做入口
			"manuscript": st.Stale.Manuscript, "storyboard": st.Stale.Storyboard, "style": st.Stale.Style,
		},
		// 最近一轮制作终态（与 agent 摘要同一事实源 produce.LastTerminal），前端标"上次制作失败"
		"last_produce": lastProduce,
	})
}

// deleteProject 删除项目：磁盘目录 + DB 镜像（行/聊天/任务）。
// 制作中拒绝——管线还在写目录，RemoveAll 会与写方竞速留半删残骸。
func (s *Server) deleteProject(c *gin.Context) {
	id := c.Param("id")
	if s.Producer != nil && s.Producer.IsRunning(id) {
		c.JSON(409, gin.H{"error": "制作/重做进行中，先停止再删除"})
		return
	}
	p := pipeline.NewProject(s.DataDir, id)
	if _, err := os.Stat(p.Dir); err != nil {
		c.JSON(404, gin.H{"error": "项目不存在"})
		return
	}
	if err := os.RemoveAll(p.Dir); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	_ = s.Store.DeleteProject(id)
	c.JSON(200, gin.H{"ok": true})
}

func (s *Server) manuscript(c *gin.Context) {
	p := pipeline.NewProject(s.DataDir, c.Param("id"))
	var m struct {
		Content   string `json:"content"`
		WordCount int    `json:"word_count"`
	}
	if b, err := os.ReadFile(p.Artifact("manuscripts/manuscript.json")); err == nil {
		_ = json.Unmarshal(b, &m)
	} else if b, err := os.ReadFile(p.Artifact("input/article.txt")); err == nil {
		m.Content = strings.TrimSpace(string(b))
		m.WordCount = len([]rune(m.Content))
	} else {
		c.JSON(404, gin.H{"error": "文稿不存在"})
		return
	}
	c.JSON(200, m)
}

// saveManuscript 文稿弹窗保存。article.txt 是唯一真源；manuscript.json 是管线产物
// （重放语义：存在即跳过）——改稿必须删掉它，否则重新制作时 TTS 拿的还是旧稿。
// 已有分镜不硬删：响应带 storyboard_stale，由前端提示"分镜基于旧稿，需重新生成"。
func (s *Server) saveManuscript(c *gin.Context) {
	var in struct {
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	content := strings.TrimSpace(in.Content)
	if content == "" {
		c.JSON(400, gin.H{"error": "文稿不能为空"})
		return
	}
	p, ok := s.project(c)
	if !ok {
		return
	}
	id := p.ID
	if err := os.WriteFile(p.Artifact("input/article.txt"), []byte(content), 0o644); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	_ = os.Remove(p.Artifact("manuscripts/manuscript.json"))
	_, hasSb := os.Stat(p.Artifact("storyboards/storyboard.json"))
	s.updateStatus(id, "manuscript")
	words := len([]rune(content))
	p.Manifest("manuscript.saved", fmt.Sprintf("%d 字", words))
	s.Hub.Emit(id, "msg", "", fmt.Sprintf("文稿已更新（%d 字）", words))
	c.JSON(200, gin.H{"ok": true, "word_count": words, "storyboard_stale": hasSb == nil})
}

// applyStyle 风格弹窗选定库内条目（v3 §4.5）：写 style/style_brief.json（含特色
// 开关——disabled 是用户关掉的特色 id，默认全开），并把选定条目记入 project.json。
// **行为变化（相对 v2）**：不再「即时生效跳过样张确认」——v3 条目没有自带样张，
// 设计系统必须本片现发明，样张确认硬门对所有人成立；旧样张产物一并清掉，
// 风格门回到「等出样张」。重选条目 = brief 整体重写。
func (s *Server) applyStyle(c *gin.Context) {
	var in struct {
		StyleID     string   `json:"style_id"`
		StylePackID string   `json:"stylepack_id"` // 旧字段名，前端过渡期兼容
		Disabled    []string `json:"disabled"`     // 关掉的特色 id（默认全开）
	}
	if err := c.ShouldBindJSON(&in); err != nil || (strings.TrimSpace(in.StyleID) == "" && strings.TrimSpace(in.StylePackID) == "") {
		c.JSON(400, gin.H{"error": "缺少 style_id"})
		return
	}
	id := strings.TrimSpace(in.StyleID)
	if id == "" {
		id = strings.TrimSpace(in.StylePackID)
	}
	pack, ok := s.Lib.Get(id)
	if !ok || !pack.Published {
		c.JSON(400, gin.H{"error": "风格不存在或未入库"})
		return
	}
	projID := c.Param("id")
	p := pipeline.NewProject(s.DataDir, projID)
	if _, err := os.Stat(p.Dir); err != nil {
		c.JSON(404, gin.H{"error": "项目不存在"})
		return
	}
	disabled := map[string]bool{}
	for _, sig := range in.Disabled {
		disabled[sig] = true
	}
	brief, err := s.Lib.BuildBrief(pack.ID, disabled)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if err := stylelib.WriteBriefFile(p.Artifact("style/style_brief.json"), brief); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	// 旧样张产物作废（强参考换了，设计必须重发明）：样张/皮肤/设计文档/LLM 中间产物，
	// 迷你真段（style/clips + .hf-clip 工程壳）整目录作废
	for _, f := range []string{
		"style/style_samples.json", "style/skin.json", "style/design.md", "llm/style_samples.json",
		"style/sample-0.png", "style/sample-1.png", "style/sample-2.png",
	} {
		_ = os.Remove(p.Artifact(f))
	}
	_ = os.RemoveAll(p.Artifact("style/clips"))
	_ = os.RemoveAll(p.Artifact(".hf-clip"))
	if b, err := os.ReadFile(p.Artifact("project.json")); err == nil {
		var meta map[string]any
		if json.Unmarshal(b, &meta) == nil {
			meta["style_id"] = pack.ID
			_ = pipeline.WriteFileAtomic(p.Artifact("project.json"), pipeline.MustJSONBytes(meta), 0o644)
		}
	}
	// 风格门回到「等出样张」：状态按剩余前置降级（有分镜=storyboard，有稿=manuscript）
	back := "created"
	if _, err := os.Stat(p.Artifact("input/article.txt")); err == nil {
		back = "manuscript"
	}
	if _, err := p.LoadStoryboard(); err == nil {
		back = "storyboard"
	}
	s.updateStatus(projID, back)
	p.Manifest("style.applied", fmt.Sprintf("库内风格「%s」＋特色×%d（强参考）", pack.Name, len(brief.Signatures)))
	s.Hub.Emit(projID, "style_applied", "", pack.Name)
	c.JSON(200, gin.H{"ok": true, "name": pack.Name, "signatures": len(brief.Signatures)})
}

func (s *Server) storyboard(c *gin.Context) {
	p := pipeline.NewProject(s.DataDir, c.Param("id"))
	sb, err := p.LoadStoryboard()
	if err != nil {
		c.JSON(404, gin.H{"error": "分镜未生成"})
		return
	}
	c.Data(200, "application/json", pipeline.MustJSONBytes(sb))
}

// saveStoryboard 手动编辑分镜落盘（storyboards/storyboard.json，与主管生成同格式同文件）。
// 段身份字段（id/idx）锁死——它们是时间轴/画面注册表/聊天引用的锚；文案/画面要点/时长自由改。
// 段数不可变：增删段走对话重新生成。落盘即换 mtime，成片过期灯（video_stale.storyboard）自动亮。
func (s *Server) saveStoryboard(c *gin.Context) {
	p := pipeline.NewProject(s.DataDir, c.Param("id"))
	if _, err := os.Stat(p.Dir); err != nil {
		c.JSON(404, gin.H{"error": "项目不存在"})
		return
	}
	prev, err := p.LoadStoryboard()
	if err != nil {
		c.JSON(400, gin.H{"error": "还没有分镜——先在对话里生成分镜"})
		return
	}
	var in contract.Storyboard
	if err := c.ShouldBindJSON(&in); err != nil || len(in.Segments) == 0 {
		c.JSON(400, gin.H{"error": "分镜格式不对（缺 segments）"})
		return
	}
	if len(in.Segments) != len(prev.Segments) {
		c.JSON(400, gin.H{"error": fmt.Sprintf("段数不能变（现在 %d 段）；要增删段请在对话里让我重新生成分镜", len(prev.Segments))})
		return
	}
	byIdx := map[int]string{}
	for _, sg := range prev.Segments {
		byIdx[sg.Idx] = sg.ID
	}
	for i := range in.Segments {
		id, ok := byIdx[in.Segments[i].Idx]
		if !ok {
			c.JSON(400, gin.H{"error": fmt.Sprintf("第 %d 段不存在——段序不可增删改", in.Segments[i].Idx)})
			return
		}
		if strings.TrimSpace(in.Segments[i].Narration) == "" {
			c.JSON(400, gin.H{"error": fmt.Sprintf("第 %d 段旁白为空", in.Segments[i].Idx)})
			return
		}
		in.Segments[i].ID = id
	}
	if err := pipeline.WriteFileAtomic(p.Artifact("storyboards/storyboard.json"), pipeline.MustJSONBytes(&in), 0o644); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	p.Manifest("storyboard.edited", fmt.Sprintf("手动编辑分镜（%d 段）", len(in.Segments)))
	c.JSON(200, gin.H{"ok": true})
}

func (s *Server) style(c *gin.Context) {
	p := pipeline.NewProject(s.DataDir, c.Param("id"))
	b, err := os.ReadFile(p.Artifact("style/style_samples.json"))
	if err != nil {
		c.JSON(404, gin.H{"error": "风格样张未出"})
		return
	}
	c.Data(200, "application/json", b)
}

// confirmStyle 用户在舞台点头确认风格——三前置硬门的唯一用户门。
// §12.2：确认样张 = 确认风格文件，skin 锁定落盘 style/skin.json（渲染只读它）。
// v2 笼子二：确认的是完整设计系统（皮肤/字体角色/动效/装置），lockDesign 机械
// 渲染 design.md 并记版本指纹——设计一改，按旧版画的段自动过期。
func (s *Server) confirmStyle(c *gin.Context) {
	id := c.Param("id")
	p := pipeline.NewProject(s.DataDir, id)
	path := p.Artifact("style/style_samples.json")
	b, err := os.ReadFile(path)
	if err != nil {
		c.JSON(400, gin.H{"error": "样张未出，先让 Agent 出样张"})
		return
	}
	var ss contract.StyleSamples
	if err := json.Unmarshal(b, &ss); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	// 严格档（设计系统四件套齐备）：老项目没有设计块的会被拒，重出样张即迁移。
	if verr := contract.ValidateStyleSamplesDraft(&ss); verr != nil {
		c.JSON(400, gin.H{"error": "设计系统不合格，请重新生成样张：" + verr.Error()})
		return
	}
	ss.Confirmed = true
	if err := os.WriteFile(path, pipeline.MustJSONBytes(ss), 0o644); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if err := pipeline.LockDesign(p, &ss); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	p.Manifest("style.confirmed", ss.Direction)
	s.updateStatus(id, "style")
	s.Hub.Emit(id, "style_confirmed", "", ss.Direction)
	c.JSON(200, gin.H{"ok": true, "direction": ss.Direction})
}

// ── 聊天 ─────────────────────────────────────────────────────

// draftManuscript 主题 → 口播文稿草稿（创建前 AI 辅助起草）。
// 文稿硬门不变：草稿仍进文稿框，用户可改可删，创建时照常校验非空。
// 篇幅按字数直驱（分钟换算在 UI 侧，文稿环节不再出现时间概念）。
func (s *Server) draftManuscript(c *gin.Context) {
	var in struct {
		Topic string `json:"topic"`
		Words int    `json:"words"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.Topic) == "" {
		c.JSON(400, gin.H{"error": "主题不能为空"})
		return
	}
	if in.Words <= 0 {
		in.Words = 500
	}
	if in.Words > 5000 { // 上限：更长的一次性成稿后半段易注水，宜分段起草
		in.Words = 5000
	}
	p, err := llm.FromEnv(llm.RoleDialogue)
	if err != nil {
		c.JSON(503, gin.H{"error": "LLM 未配置（LLM_API_KEY），请直接粘贴文稿"})
		return
	}
	// 180s：撰稿工位是自主循环，时事题材会先联网核实（预算帽 2 次搜索）再成稿
	ctx, cancel := context.WithTimeout(c.Request.Context(), 180*time.Second)
	defer cancel()
	content, err := agent.DraftManuscript(ctx, p, in.Topic, in.Words)
	if err != nil {
		c.JSON(500, gin.H{"error": "起草失败：" + err.Error()})
		return
	}
	c.JSON(200, gin.H{"content": content})
}

func (s *Server) chat(c *gin.Context) {
	id := c.Param("id")
	var in struct {
		Content string      `json:"content"`
		Refs    []store.Ref `json:"refs"` // 引用卡片（段/元素，M2 起结构化）
	}
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.Content) == "" {
		c.JSON(400, gin.H{"error": "消息不能为空"})
		return
	}
	if _, ok := s.project(c); !ok {
		return
	}
	in.Refs = sanitizeRefs(in.Refs)
	m := &store.Msg{ProjectID: id, Role: "user", Type: "text", Content: in.Content, Refs: in.Refs}
	_ = s.Store.SaveMsg(m)
	s.Hub.Emit(id, "msg", "", in.Content)
	// 同项目对话轮互斥：连发两条消息会并发跑两个 Agent 循环，互相增删共享产物
	// （如 update_storyboard 删 spec 时另一轮正读）。排队等上一轮结束，不拒绝。
	mu, _ := s.turnMu.LoadOrStore(id, &sync.Mutex{})
	go func() {
		mu := mu.(*sync.Mutex)
		mu.Lock()
		defer mu.Unlock()
		s.Agent.Run(id, in.Content, in.Refs)
	}()
	c.JSON(202, gin.H{"ok": true})
}

// sanitizeRefs 引用卡片是模型上下文也是落盘数据：限量、限长、剔无效。
func sanitizeRefs(refs []store.Ref) []store.Ref {
	if len(refs) > 8 {
		refs = refs[:8]
	}
	out := refs[:0]
	for _, r := range refs {
		if r.Idx < 1 {
			continue
		}
		r.Key = clip(r.Key, 64)
		r.ElementID = clip(r.ElementID, 64)
		r.ElementName = clip(r.ElementName, 64)
		if r.T < 0 {
			r.T = 0
		}
		out = append(out, r)
	}
	return out
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func (s *Server) messages(c *gin.Context) {
	id := c.Param("id")
	var after int64
	fmt.Sscanf(c.Query("after"), "%d", &after)
	msgs, err := s.Store.ListMsgs(id, after, 200)
	if err != nil {
		msgs = nil
	}
	c.JSON(200, msgs)
}

// ── 制作 & 视频 ─────────────────────────────────────────────

func (s *Server) startProduce(c *gin.Context) {
	id := c.Param("id")
	st := s.Agent.State(id)
	if !st.HasManuscript || !st.HasStoryboard || !st.StyleConfirmed {
		c.JSON(400, gin.H{"error": "三前置未齐（文稿/分镜/风格确认），硬门不放行"})
		return
	}
	ok, err := s.Producer.Start(id)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	s.updateStatus(id, "producing")
	c.JSON(202, gin.H{"ok": true, "started": ok})
}

// cancelProduce 取消进行中的制作/重做（M1 遗留 cancel_production）。
// 已在收尾的任务可能来不及响应，返回 ok=true 表示取消信号已发出。
func (s *Server) cancelProduce(c *gin.Context) {
	if !s.Producer.Cancel(c.Param("id")) {
		c.JSON(409, gin.H{"error": "没有正在进行的制作任务"})
		return
	}
	c.JSON(202, gin.H{"ok": true})
}

// startRework 一键段级重做（时间轴段块「重做」按钮直连，绕过对话轮）。
// instruction 可空 = 依次取目标段的历史指令复用（「用上次的要求再试一次」）。
func (s *Server) startRework(c *gin.Context) {
	id := c.Param("id")
	var in struct {
		Segments    []string `json:"segments"`
		Instruction string   `json:"instruction"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if len(in.Segments) == 0 {
		c.JSON(400, gin.H{"error": "缺少目标段"})
		return
	}
	st := s.Agent.State(id)
	if !st.HasVideo {
		c.JSON(400, gin.H{"error": "成片未就绪，先制作"})
		return
	}
	if s.Producer.IsRunning(id) {
		c.JSON(409, gin.H{"error": "制作/重做已在运行中"})
		return
	}
	p := pipeline.NewProject(s.DataDir, id)
	seen := map[string]bool{}
	var segIDs, labels []string
	for _, ref := range in.Segments {
		segID, label, err := s.Agent.ResolveSegment(p, ref)
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		if seen[segID] {
			continue
		}
		seen[segID] = true
		segIDs = append(segIDs, segID)
		labels = append(labels, label)
	}
	instr := strings.TrimSpace(in.Instruction)
	if instr == "" {
		for _, segID := range segIDs {
			if last := produce.LastReworkInstruction(p, segID); last != "" {
				instr = last
				break
			}
		}
	}
	if instr == "" {
		c.JSON(400, gin.H{"error": "缺少修改要求（该段也没有历史指令可复用）"})
		return
	}
	ok, err := s.Producer.Rework(id, segIDs, instr)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if !ok {
		c.JSON(409, gin.H{"error": "制作/重做已在运行中"})
		return
	}
	s.updateStatus(id, "producing")
	c.JSON(202, gin.H{"ok": true, "segs": segIDs, "labels": labels, "instruction": instr})
}

// reworkLast 某段最近一次重做指令（按钮预填用）。
func (s *Server) reworkLast(c *gin.Context) {
	p := pipeline.NewProject(s.DataDir, c.Param("id"))
	c.JSON(200, gin.H{"instruction": produce.LastReworkInstruction(p, c.Query("seg"))})
}

// extractLink 链接提取（plan §10 开放问题落地）：抓网页 → readability 抽正文，
// 文稿弹窗「从链接导入」用。提取失败让用户回退粘贴，不硬造。
func (s *Server) extractLink(c *gin.Context) {
	var in struct {
		URL string `json:"url"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.URL) == "" {
		c.JSON(400, gin.H{"error": "链接不能为空"})
		return
	}
	art, err := webfetch.Fetch(c.Request.Context(), in.URL)
	if err != nil {
		code := 502
		switch {
		case errors.Is(err, webfetch.ErrBadURL):
			code = 400
		case errors.Is(err, webfetch.ErrUnreadable):
			code = 422
		}
		msg := err.Error()
		if code == 422 {
			msg += "，请直接粘贴"
		}
		c.JSON(code, gin.H{"error": msg})
		return
	}
	c.JSON(200, gin.H{"title": art.Title, "content": art.Content, "words": art.Words})
}

// audioMeta 时间轴数据源：每段配音时长 + 字级时间戳（TTS 对齐产物），供前端画字幕/音频轨。
// xfade 成片附带 renders/timeline.json（实际段边界，比裸配音累计每边界早 xfadeDur），
// 合并为 timeline 字段——前端 segTable 有它就用真实边界。
func (s *Server) audioMeta(c *gin.Context) {
	p := pipeline.NewProject(s.DataDir, c.Param("id"))
	b, err := os.ReadFile(p.Artifact("audio_meta.json"))
	if err != nil {
		c.JSON(404, gin.H{"error": "音频对齐数据不存在（未出配音）"})
		return
	}
	if tl, err := os.ReadFile(p.Artifact("renders/timeline.json")); err == nil {
		var m map[string]json.RawMessage
		if json.Unmarshal(b, &m) == nil {
			m["timeline"] = json.RawMessage(tl)
			if merged, err := json.Marshal(m); err == nil {
				b = merged
			}
		}
	}
	c.Data(200, "application/json", b)
}

func (s *Server) video(c *gin.Context) {
	path := pipeline.NewProject(s.DataDir, c.Param("id")).Artifact("renders/main.mp4")
	if _, err := os.Stat(path); err != nil {
		c.JSON(404, gin.H{"error": "成片未就绪"})
		return
	}
	serveFile(c, path, "main.mp4", "no-store")
}

// styleClip 迷你真段 mp4（样张动态小样）。文件名白名单 clip-<N>.mp4——
// style/clips/ 里还有场景片段 html 和装配清单，那些不对外。
var styleClipRe = regexp.MustCompile(`^clip-\d+\.mp4$`)

func (s *Server) styleClip(c *gin.Context) {
	name := c.Param("file")
	if !styleClipRe.MatchString(name) {
		c.JSON(404, gin.H{"error": "文件不存在"})
		return
	}
	path := pipeline.NewProject(s.DataDir, c.Param("id")).Artifact("style/clips/" + name)
	if _, err := os.Stat(path); err != nil {
		c.JSON(404, gin.H{"error": "小样未就绪"})
		return
	}
	serveFile(c, path, name, "no-store")
}

// ── 风格库（起草自动 + 入库确认）─────────────────────────────

func (s *Server) listLibrary(c *gin.Context) {
	styles, err := s.Lib.List()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if styles == nil {
		styles = []stylelib.Style{}
	}
	c.JSON(200, styles)
}

func (s *Server) publishPack(c *gin.Context) {
	pack, err := s.Lib.Publish(c.Param("id"))
	if err != nil {
		if errors.Is(err, stylelib.ErrNoEntry) {
			c.JSON(404, gin.H{"error": err.Error()})
			return
		}
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, pack)
}

// deleteLibraryEntry 删除风格条目（含媒体快照）。已出片项目不受影响——样张/皮肤在
// 选用时已拷进项目各自的 style/。
func (s *Server) deleteLibraryEntry(c *gin.Context) {
	if err := s.Lib.Delete(c.Param("id")); err != nil {
		if errors.Is(err, stylelib.ErrNoEntry) {
			c.JSON(404, gin.H{"error": err.Error()})
			return
		}
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

func isSlug(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func isPngName(s string) bool {
	if !strings.HasSuffix(s, ".png") || len(s) <= 4 {
		return false
	}
	return isSlug(strings.TrimSuffix(s, ".png"))
}

// ── SSE ──────────────────────────────────────────────────────

func (s *Server) sse(c *gin.Context) {
	id := c.Param("id")
	if _, ok := s.project(c); !ok {
		return
	}
	ch, replay, cancel := s.Hub.Subscribe(id)
	defer cancel()
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")
	fl, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(500, gin.H{"error": "streaming unsupported"})
		return
	}
	io.WriteString(c.Writer, "retry: 3000\n\n")
	for _, ev := range replay { // 断线重连补最近事件
		io.WriteString(c.Writer, "event: "+ev.Type+"\ndata: "+ev.JSON()+"\n\n")
	}
	fl.Flush()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case ev, alive := <-ch:
			if !alive {
				return
			}
			io.WriteString(c.Writer, "event: "+ev.Type+"\ndata: "+ev.JSON()+"\n\n")
			fl.Flush()
		case <-heartbeat.C:
			io.WriteString(c.Writer, ": ping\n\n")
			fl.Flush()
		}
	}
}

func (s *Server) updateStatus(id, status string) {
	_ = pipeline.SetProjectField(pipeline.NewProject(s.DataDir, id), "status", status)
	_ = s.Store.UpdateStatus(id, status)
}

// project 按路由 :id 构造 Project 并确认存在；不存在时已回 404，调用方直接 return。
func (s *Server) project(c *gin.Context) (*pipeline.Project, bool) {
	p := pipeline.NewProject(s.DataDir, c.Param("id"))
	if _, err := os.Stat(p.Dir); err != nil {
		c.JSON(404, gin.H{"error": "项目不存在"})
		return nil, false
	}
	return p, true
}

// serveFile 媒体/静态文件回源（打开失败 404；name 进 Content-Disposition 推断，
// cache 为 Cache-Control 值）。video/frame/asset/媒体三 handler 共用。
func serveFile(c *gin.Context, path, name, cache string) {
	f, err := os.Open(path)
	if err != nil {
		c.JSON(404, gin.H{"error": "文件不存在"})
		return
	}
	defer f.Close()
	if st, _ := f.Stat(); st == nil || st.IsDir() {
		c.JSON(404, gin.H{"error": "文件不存在"})
		return
	}
	c.Header("Cache-Control", cache)
	http.ServeContent(c.Writer, c.Request, name, time.Now(), f)
}

// ── 参考视频解析（风格库「视频解析」）────────────────────────

// uploadReference 上传视频 → 落盘登记 → 立即异步解析。
func (s *Server) uploadReference(c *gin.Context) {
	fh, err := c.FormFile("video")
	if err != nil {
		c.JSON(400, gin.H{"error": "缺少 video 文件字段"})
		return
	}
	name := filepath.Base(fh.Filename)
	var refVideoExt = map[string]bool{".mp4": true, ".mov": true, ".webm": true, ".m4v": true}
	if !refVideoExt[strings.ToLower(filepath.Ext(name))] {
		c.JSON(400, gin.H{"error": "只支持 mp4/mov/webm/m4v 视频文件"})
		return
	}
	const refVideoMax = 200 << 20 // 200MB：风格参考片段足够，防超大文件塞满磁盘
	if fh.Size > refVideoMax {
		c.JSON(400, gin.H{"error": "视频超过 200MB 上限，请先裁剪再上传"})
		return
	}
	ref, err := s.Refs.Save(name, func(dst string) error {
		return c.SaveUploadedFile(fh, dst)
	})
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	_, _ = s.Refs.Analyze(ref.ID) // 上传即解析；状态轮询 GET /api/references
	c.JSON(200, ref)
}

func (s *Server) listReferences(c *gin.Context) {
	refs, err := s.Refs.List()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if refs == nil {
		refs = []styleref.Ref{}
	}
	c.JSON(200, refs)
}

// analyzeReference 解析（重试入口；上传时已自动起过一次）。
func (s *Server) analyzeReference(c *gin.Context) {
	started, err := s.Refs.Analyze(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if !started {
		c.JSON(409, gin.H{"error": "已在解析中"})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

// confirmReference 解析结果入库：styles.json 写「标签+特色」条目（一视频一条目）。
func (s *Server) confirmReference(c *gin.Context) {
	ref, err := s.Refs.Get(c.Param("id"))
	if err != nil {
		c.JSON(404, gin.H{"error": "引用不存在"})
		return
	}
	if ref.Status != "done" || ref.Style == nil {
		c.JSON(400, gin.H{"error": "还没解析完成，不能入库"})
		return
	}

	st, err := s.ingestRefStyle(ref.ID, ref.Name, ref.Style, filepath.Join(s.DataDir, "references", ref.ID))
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if err := s.Refs.MarkConfirmed(ref.ID); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true, "style_id": st.ID})
}

// ingestRefStyle 一条参考风格入库：帧引用存在性校验（对着解析帧目录）+ 代表帧
// 封面 + IngestVideoRef（新载荷：标签+特色+题材一句话）+ 原片/抽帧快照。
// 条目名继承原视频标题（origName=上传文件名），风格名只在标题清洗不出时兜底——
// 风格标签是家族名，多条目会撞名，不能当条目名用。
// seedID 是 IngestVideoRef 的 refID 种子（重复入库同 seed upsert 幂等）。
func (s *Server) ingestRefStyle(seedID, origName string, d *styleref.StyleDraft, dir string) (*stylelib.Style, error) {
	feats := styleref.ReadFeatures(dir)
	valid := map[string]bool{}
	for _, f := range feats {
		valid[filepath.Base(f.Path)] = true
	}
	// 帧引用实存校验（§4.1：入库时执行——确认页人工改动可能引入坏引用）
	for _, ev := range d.TagEvidence {
		for _, f := range ev.Frames {
			if !valid[f] {
				return nil, fmt.Errorf("标签 %s 的证据帧 %s 不存在", ev.Tag, f)
			}
		}
	}
	for _, sg := range d.Signatures {
		for _, f := range sg.Frames {
			if !valid[f] {
				return nil, fmt.Errorf("特色 %s 的证据帧 %s 不存在", sg.Name, f)
			}
		}
	}
	makeCover(dir, feats, firstEvidenceFrame(d)) // 代表帧拷成 cover.png（卡片封面/回看），失败不挡入库
	st, err := s.Lib.IngestVideoRef(seedID, stylelib.VideoStyleIn{
		Name:        videoTitle(origName, d.Name),
		Subject:     d.Subject,
		Tags:        d.Tags,
		TagEvidence: d.TagEvidence,
		Signatures:  d.Signatures,
	})
	if err != nil {
		return nil, err
	}
	// 原片+抽帧快照进风格目录：参考视频库删除后风格详情仍可回看（失败不挡入库，
	// 详情抽屉对老条目/缺快照自动回退 references 目录）
	_ = s.Lib.SnapshotMedia(st.ID, dir)
	return st, nil
}

// llmVideo 临时视频接口（视频直读，2026-10-08）：把参考视频的 480p 剪辑交给大模型拉取。
// 不要求 X-API-Token（调用方是模型侧的取媒体器）——防线三道：HMAC 签名（与文件绑定，
// 改一个字符即失效）+ 6 小时有效期 + 文件名白名单（只认 clip-*.mp4，原片永不出这个口子）。
// 链接只在解析时按需签发，无链接则此路由对公网无意义。
func (s *Server) llmVideo(c *gin.Context) {
	id, name := c.Param("id"), c.Param("name")
	exp, err := strconv.ParseInt(c.Query("exp"), 10, 64)
	if err != nil {
		c.JSON(403, gin.H{"error": "缺 exp"})
		return
	}
	if !s.Refs.VerifyVideo(id, name, exp, c.Query("sig")) {
		c.JSON(403, gin.H{"error": "签名无效或已过期"})
		return
	}
	path, ok := s.Refs.VideoFile(id, name)
	if !ok {
		c.JSON(404, gin.H{"error": "文件不存在"})
		return
	}
	serveFile(c, path, name, "no-store")
}

// listVocab 风格词表（只读，前端展示词条 brief 用；空表 = 冷启动正常态）。
func (s *Server) listVocab(c *gin.Context) {
	entries := s.Lib.VocabList()
	if entries == nil {
		entries = []stylelib.VocabEntry{}
	}
	c.JSON(200, entries)
}

// updateReferenceDraft 确认页编辑解析草稿（§4.4：入库前可增删改特色）。
func (s *Server) updateReferenceDraft(c *gin.Context) {
	var in struct {
		Style *styleref.StyleDraft `json:"style"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || in.Style == nil {
		c.JSON(400, gin.H{"error": "缺少 style 草稿"})
		return
	}
	if err := stylelib.ValidateSignatures(in.Style.Signatures); err != nil {
		c.JSON(400, gin.H{"error": "特色清单不合格：" + err.Error()})
		return
	}
	if err := s.Refs.UpdateDraft(c.Param("id"), in.Style); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

// firstEvidenceFrame 大模型标的第一个标签证据帧（它认定的风格代表画面），作封面首选。
func firstEvidenceFrame(d *styleref.StyleDraft) string {
	for _, ev := range d.TagEvidence {
		if len(ev.Frames) > 0 {
			return ev.Frames[0]
		}
	}
	return ""
}

// videoTitle 上传文件名清洗成可读标题：NN_标题_BVxxx[_P 后缀].mp4 → 标题（多P补 P 号）。
// 清洗不出非编号内容时回退 fallback（解析草稿的风格名）。
func videoTitle(filename, fallback string) string {
	base := strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
	part := ""
	if i := strings.Index(base, "__"); i > 0 { // 批量上传的多P标签「标题_BVxxx__P02_标题 p02」
		if m := partNumRe.FindStringSubmatch(base[i+2:]); m != nil {
			part = " P" + m[1]
		}
		base = base[:i]
	}
	base = strings.TrimSpace(leadNumRe.ReplaceAllString(bvSuffixRe.ReplaceAllString(base, ""), ""))
	if base == "" || strings.EqualFold(base, "video") { // 裸 video.mp4 没有标题信息
		return fallback
	}
	return base + part
}

var (
	leadNumRe = regexp.MustCompile(`^\d+\s*[_-]\s*`)
	bvSuffixRe = regexp.MustCompile(`[_\s]*BV[0-9A-Za-z]{6,14}\s*$`)
	partNumRe = regexp.MustCompile(`(?i)^\s*P0?(\d+)`)
)

// makeCover 代表帧拷成 cover.png，返回其时间点；无帧返回 -1。
// 优先用 prefer 指定的帧（标签证据首选帧），没有/对不上退中位帧。
func makeCover(dir string, frames []styleref.FeatureFrame, prefer string) float64 {
	pick := -1
	if prefer != "" {
		for i, f := range frames {
			if filepath.Base(f.Path) == prefer {
				pick = i
				break
			}
		}
	}
	if pick < 0 {
		if len(frames) == 0 {
			return -1
		}
		pick = len(frames) / 2
	}
	b, err := os.ReadFile(frames[pick].Path)
	if err != nil || len(b) == 0 {
		return -1
	}
	if err := os.WriteFile(filepath.Join(dir, "cover.png"), b, 0o644); err != nil {
		return -1
	}
	return frames[pick].T
}

// scanMediaDir 目录抽帧清单：frame-*.png（白名单名校验）+ 是否带原视频。
// referenceFrames 与 libraryFrames 共用。
func scanMediaDir(dir string) (frames []string, hasVideo bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch name := e.Name(); {
		case isPngName(name) && strings.HasPrefix(name, "frame-"):
			frames = append(frames, name)
		case name == "video.mp4":
			hasVideo = true
		}
	}
	return frames, hasVideo
}

// referenceFrames 参考视频抽帧截图清单（详情抽屉展示原片长相用）。
func (s *Server) referenceFrames(c *gin.Context) {
	id := c.Param("id")
	if !isSlug(id) {
		c.JSON(400, gin.H{"error": "非法路径"})
		return
	}
	frames, hasVideo := scanMediaDir(filepath.Join(s.DataDir, "references", id))
	if frames == nil && !hasVideo { // 目录读不了 = 引用不存在
		if _, err := os.Stat(filepath.Join(s.DataDir, "references", id)); err != nil {
			c.JSON(404, gin.H{"error": "引用不存在"})
			return
		}
	}
	if frames == nil {
		frames = []string{}
	}
	c.JSON(200, gin.H{"frames": frames, "has_video": hasVideo})
}

// refMediaName 参考媒体文件名白名单：抽帧 frame-*.png / 原视频 video.mp4 /
// 封面 cover.png（入库时代表帧拷贝，封面样张的 <img> 引用它）。
func refMediaName(name string) bool {
	if name == "video.mp4" || name == "cover.png" {
		return true
	}
	return isPngName(name) && strings.HasPrefix(name, "frame-")
}

// styleMediaResolve 风格媒体定位：快照目录（style_media/<id>）优先，
// 缺文件回退参考视频目录（origin_project="video:<refID>" 的老条目）。
// 返回 (文件路径, ok)；ok=false 时调用方回 404。
func (s *Server) styleMediaResolve(styleID, name string) (string, bool) {
	if !isSlug(styleID) || !refMediaName(name) {
		return "", false
	}
	if p := filepath.Join(s.Lib.MediaDir(styleID), name); fileExists(p) {
		return p, true
	}
	st, ok := s.Lib.Get(styleID)
	if !ok || !strings.HasPrefix(st.OriginProject, "video:") {
		return "", false
	}
	refID := strings.TrimPrefix(st.OriginProject, "video:")
	p := filepath.Join(s.DataDir, "references", refID, name)
	return p, fileExists(p)
}

// libraryFrames 风格媒体抽帧清单：快照目录（style_media/<id>）优先，
// 空（老条目/快照失败）回退 origin 指向的参考视频目录。
func (s *Server) libraryFrames(c *gin.Context) {
	id := c.Param("id")
	if !isSlug(id) {
		c.JSON(400, gin.H{"error": "非法路径"})
		return
	}
	frames, hasVideo := scanMediaDir(s.Lib.MediaDir(id))
	if len(frames) == 0 { // 抽帧缺失就回退（快照只带 video 时也要能列出参考抽帧）
		if st, ok := s.Lib.Get(id); ok && strings.HasPrefix(st.OriginProject, "video:") {
			frames2, hv2 := scanMediaDir(filepath.Join(s.DataDir, "references", strings.TrimPrefix(st.OriginProject, "video:")))
			frames = frames2
			if !hasVideo {
				hasVideo = hv2
			}
		}
	}
	if frames == nil {
		frames = []string{}
	}
	c.JSON(200, gin.H{"frames": frames, "has_video": hasVideo})
}

// libraryMedia 风格媒体本体（路径只出自快照/参考目录拼装，无遍历面）。
func (s *Server) libraryMedia(c *gin.Context) {
	p, ok := s.styleMediaResolve(c.Param("id"), c.Param("file"))
	if !ok {
		c.JSON(404, gin.H{"error": "文件不存在"})
		return
	}
	serveFile(c, p, filepath.Base(p), "max-age=3600")
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// referenceMedia 参考引用媒体文件（抽帧 png / 原视频 mp4，白名单文件名防目录穿越）。
func (s *Server) referenceMedia(c *gin.Context) {
	id, name := c.Param("id"), c.Param("file")
	if !isSlug(id) {
		c.JSON(400, gin.H{"error": "非法路径"})
		return
	}
	if !refMediaName(name) {
		c.JSON(400, gin.H{"error": "非法文件名"})
		return
	}
	serveFile(c, filepath.Join(s.DataDir, "references", id, name), name, "max-age=3600")
}
