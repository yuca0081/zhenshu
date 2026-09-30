// Package api：HTTP 层（Gin）。REST + SSE；磁盘是产物事实源，PG 存案卷。
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-shiori/go-readability"

	"webpage-video-agent/server/internal/agent"
	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/events"
	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/methodlib"
	"webpage-video-agent/server/internal/pipeline"
	"webpage-video-agent/server/internal/produce"
	"webpage-video-agent/server/internal/store"
	"webpage-video-agent/server/internal/assetlib"
	"webpage-video-agent/server/internal/styleref"
)

type Server struct {
	DataDir  string
	RootDir  string
	Store    store.Store
	Hub      *events.Hub
	Agent    *agent.Agent
	Producer *produce.Runner
	Lib      *methodlib.Library
	Refs     *styleref.Store
	Assets   *assetlib.Library

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
	api.GET("/projects/:id/overview", s.overview)
	api.GET("/projects/:id/manuscript", s.manuscript)
	api.PUT("/projects/:id/manuscript", s.saveManuscript)
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
	api.GET("/projects/:id/audio_meta", s.audioMeta)
	api.GET("/projects/:id/frames/:seg", s.frame)
	api.GET("/projects/:id/assets/*filepath", s.asset)
	api.GET("/library", s.listLibrary)
	api.POST("/library/:id/publish", s.publishPack)
	api.GET("/assets", s.assets)
	api.GET("/assets/:id/file", s.assetFile)
	api.GET("/registry", s.registry)
	api.GET("/gallery", s.gallery)
	api.GET("/gallery/:style/:file", s.galleryFile)
	api.POST("/references", s.uploadReference)
	api.GET("/references", s.listReferences)
	api.GET("/references/:id/frames", s.referenceFrames)
	api.GET("/references/:id/media/:file", s.referenceMedia)
	api.POST("/references/:id/analyze", s.analyzeReference)
	api.POST("/references/:id/confirm", s.confirmReference)

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
// `..%5C..` 到这里是 `..\..`——Windows 下 filepath.Join 认 `\` 为分隔符，path.Clean
// （asset handler 用）只认 `/` 拦不住它，必须在入口统一拒绝。
func secureParams() gin.HandlerFunc {
	return func(c *gin.Context) {
		for _, p := range c.Params {
			v := p.Value
			bad := strings.Contains(v, "..") || strings.ContainsAny(v, `/\`)
			if p.Key == "filepath" { // 资产子路径合法含 "/"；仍禁 ".." 与 "\"
				bad = strings.Contains(v, "..") || strings.Contains(v, `\`)
			}
			if bad {
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
		StylePackID string `json:"stylepack_id"` // 方法库复用（可空 = 冷启动自拟）
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	in.Topic = strings.TrimSpace(in.Topic)
	in.Manuscript = strings.TrimSpace(in.Manuscript)
	// 新建只要求名称+主题（文稿/风格是三前置，进工作区后在对话栏标签里补）；
	// 带稿直建是老路径，兼容保留。
	if in.Topic == "" && in.Manuscript == "" {
		c.JSON(400, gin.H{"error": "主题必填（一句话说清要讲什么，例：蚊子为什么嗡嗡叫）"})
		return
	}
	if in.Aspect != "16:9" && in.Aspect != "9:16" {
		in.Aspect = "9:16"
	}
	if in.StylePackID != "" {
		if pack, ok := s.Lib.Get(in.StylePackID); !ok || !pack.Published {
			c.JSON(400, gin.H{"error": "风格包不存在或未发布"})
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
		"topic": in.Topic, "stylepack_id": in.StylePackID,
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
		ID        string `json:"id"`
		Name      string `json:"name"`
		Status    string `json:"status"`
		CreatedAt string `json:"created_at"`
		HasVideo  bool   `json:"has_video"`
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
		out = append(out, row{e.Name(), meta.Name, meta.Status, meta.CreatedAt, hasVideo == nil})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	c.JSON(200, out)
}

func (s *Server) projectView(c *gin.Context) {
	id := c.Param("id")
	p := pipeline.NewProject(s.DataDir, id)
	if _, err := os.Stat(p.Dir); err != nil {
		c.JSON(404, gin.H{"error": "项目不存在"})
		return
	}
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
	c.JSON(200, gin.H{
		"id": id, "name": st.Name, "aspect": aspect, "topic": topic,
		"gates": gin.H{
			"manuscript": st.HasManuscript, "storyboard": st.HasStoryboard, "style_confirmed": st.StyleConfirmed,
			"style_draft": st.HasStyle, "style_direction": st.StyleDirection,
		},
		"seg_count": st.SegCount, "total_hint": st.TotalHint,
		"has_video": st.HasVideo, "producing": st.Producing,
		// 检视壳复刻成片观感用：转场模式 + stitch 后期妆（与 main.mp4 同源口径）
		"transition": pipeline.TransitionFor(p),
		"post":       pipeline.PostFor(p),
	})
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
	id := c.Param("id")
	p := pipeline.NewProject(s.DataDir, id)
	if _, err := os.Stat(p.Dir); err != nil {
		c.JSON(404, gin.H{"error": "项目不存在"})
		return
	}
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

// applyStyle 风格弹窗选定库内风格包：写 style_samples.json 并直接标记已确认
// （弹窗里预览样张后显式点「确认使用」= 硬门通过，无需再回舞台点一遍）。
// 同时把选定包记入 project.json，后续 extract_stylepack / 复用链路读同一字段。
func (s *Server) applyStyle(c *gin.Context) {
	var in struct {
		StylePackID string `json:"stylepack_id"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.StylePackID) == "" {
		c.JSON(400, gin.H{"error": "缺少 stylepack_id"})
		return
	}
	pack, ok := s.Lib.Get(strings.TrimSpace(in.StylePackID))
	if !ok || !pack.Published {
		c.JSON(400, gin.H{"error": "风格包不存在或未发布"})
		return
	}
	id := c.Param("id")
	p := pipeline.NewProject(s.DataDir, id)
	if _, err := os.Stat(p.Dir); err != nil {
		c.JSON(404, gin.H{"error": "项目不存在"})
		return
	}
	if err := os.MkdirAll(filepath.Dir(p.Artifact("style/style_samples.json")), 0o755); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	// 弹窗预览 + 显式确认一步完成：confirmed 翻 true（StyleSamplesJSON 默认 false 等舞台点头）
	var ss map[string]any
	if err := json.Unmarshal(pack.StyleSamplesJSON(), &ss); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	ss["confirmed"] = true
	if err := os.WriteFile(p.Artifact("style/style_samples.json"), mustJSON(ss), 0o644); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	// 模板自带风格文件则一并锁定进项目（§12.3：皮肤定死是模板强一致的来源）
	if pack.Skin != nil {
		if sb, merr := json.MarshalIndent(pack.Skin, "", "  "); merr == nil {
			_ = pipeline.WriteFileAtomic(p.Artifact("style/skin.json"), sb, 0o644)
		}
	} else {
		_ = os.Remove(p.Artifact("style/skin.json")) // 换模板防残留旧皮肤
	}
	if b, err := os.ReadFile(p.Artifact("project.json")); err == nil {
		var meta map[string]any
		if json.Unmarshal(b, &meta) == nil {
			meta["stylepack_id"] = pack.ID
			_ = pipeline.WriteFileAtomic(p.Artifact("project.json"), mustJSON(meta), 0o644)
		}
	}
	s.updateStatus(id, "style")
	p.Manifest("style.applied", fmt.Sprintf("库内风格包「%s」", pack.Name))
	s.Hub.Emit(id, "style_confirmed", "", pack.Direction)
	c.JSON(200, gin.H{"ok": true, "direction": pack.Direction})
}

func (s *Server) storyboard(c *gin.Context) {
	p := pipeline.NewProject(s.DataDir, c.Param("id"))
	sb, err := p.LoadStoryboard()
	if err != nil {
		c.JSON(404, gin.H{"error": "分镜未生成"})
		return
	}
	c.Data(200, "application/json", mustJSON(sb))
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
	if verr := contract.ValidateSkin(ss.Skin); verr != nil {
		c.JSON(400, gin.H{"error": "风格文件不合格，请重新生成样张：" + verr.Error()})
		return
	}
	ss.Confirmed = true
	if err := os.WriteFile(path, mustJSON(ss), 0o644); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if ss.Skin != nil {
		if sb, merr := json.MarshalIndent(ss.Skin, "", "  "); merr == nil {
			if werr := os.WriteFile(p.Artifact("style/skin.json"), sb, 0o644); werr != nil {
				c.JSON(500, gin.H{"error": werr.Error()})
				return
			}
		}
	}
	p.Manifest("style.confirmed", ss.Direction)
	s.updateStatus(id, "style")
	s.Hub.Emit(id, "style_confirmed", "", ss.Direction)
	c.JSON(200, gin.H{"ok": true, "direction": ss.Direction})
}

// ── 聊天 ─────────────────────────────────────────────────────

// draftManuscript 主题 → 口播文稿草稿（创建前 AI 辅助起草）。
// 文稿硬门不变：草稿仍进文稿框，用户可改可删，创建时照常校验非空。
func (s *Server) draftManuscript(c *gin.Context) {
	var in struct {
		Topic   string  `json:"topic"`
		Minutes float64 `json:"minutes"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.Topic) == "" {
		c.JSON(400, gin.H{"error": "主题不能为空"})
		return
	}
	if in.Minutes <= 0 {
		in.Minutes = 1
	}
	if in.Minutes > 5 {
		in.Minutes = 5
	}
	p, err := llm.FromEnv(llm.RoleDialogue)
	if err != nil {
		c.JSON(503, gin.H{"error": "LLM 未配置（LLM_API_KEY），请直接粘贴文稿"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 120*time.Second)
	defer cancel()
	words := int(in.Minutes * 60 * 4.2) // 口播语速 ≈ 4.2 字/秒
	var out struct {
		Content string `json:"content"`
	}
	system := "你是「帧述」的口播科普撰稿人，为短视频写口播文稿。硬性要求：" +
		"1) 第一句就是钩子（提问/反常识/画面感），不写标题和开场白；" +
		"2) 2~4 个递进的知识点，口语化短句，像讲给朋友听，少形容词堆砌；" +
		"3) 结尾一句收束或抛一个问题；" +
		"4) 只输出正文：不带标题、小节序号、markdown、emoji、舞台指示；" +
		"5) 数字和事实宁缺毋滥，不确定的不编。输出 JSON：{\"content\": \"文稿全文\"}。"
	user := fmt.Sprintf("主题：%s\n目标时长：约 %.0f 分钟（正文约 %d 字，±20%%）。", strings.TrimSpace(in.Topic), in.Minutes, words)
	if _, err := p.GenerateJSON(ctx, system, user, &out); err != nil {
		c.JSON(500, gin.H{"error": "起草失败：" + err.Error()})
		return
	}
	if strings.TrimSpace(out.Content) == "" {
		c.JSON(500, gin.H{"error": "起草结果为空，请重试或直接粘贴文稿"})
		return
	}
	c.JSON(200, gin.H{"content": strings.TrimSpace(out.Content)})
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
	if _, err := os.Stat(pipeline.NewProject(s.DataDir, id).Dir); err != nil {
		c.JSON(404, gin.H{"error": "项目不存在"})
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

// startRework 一键段级重做（检视态「重做此段」按钮直连，绕过对话轮）。
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
	u, err := url.Parse(strings.TrimSpace(in.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		c.JSON(400, gin.H{"error": "只支持 http/https 链接"})
		return
	}
	if badHost(u.Hostname()) {
		c.JSON(400, gin.H{"error": "不允许的地址"})
		return
	}
	client := &http.Client{
		Timeout: 20 * time.Second,
		// 逐跳校验重定向目标：默认客户端会跟满 10 跳且不复查，公网 302 → 127.0.0.1 即绕过
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("重定向过多")
			}
			if badHost(req.URL.Hostname()) {
				return fmt.Errorf("重定向到不允许的地址")
			}
			return nil
		},
	}
	req, _ := http.NewRequestWithContext(c.Request.Context(), "GET", u.String(), nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; ZhenshuBot/1.0; +link-import)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := client.Do(req)
	if err != nil {
		c.JSON(502, gin.H{"error": "抓取失败：" + err.Error()})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		c.JSON(502, gin.H{"error": fmt.Sprintf("目标站返回 %d", resp.StatusCode)})
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		c.JSON(502, gin.H{"error": "读取失败：" + err.Error()})
		return
	}
	art, err := readability.FromReader(strings.NewReader(string(body)), u)
	if err != nil {
		c.JSON(422, gin.H{"error": "正文提取失败（该页面可能不是文章页），请直接粘贴"})
		return
	}
	content := strings.TrimSpace(art.TextContent)
	if len([]rune(content)) < 100 {
		c.JSON(422, gin.H{"error": "提取到的正文太短（可能不是文章页），请直接粘贴"})
		return
	}
	c.JSON(200, gin.H{"title": strings.TrimSpace(art.Title), "content": content, "words": len([]rune(content))})
}

// badHost SSRF 防线：字面量检查 + DNS 解析复核。只查字面量拦不住自建域名 A 记录指向
// 内网（云 metadata 169.254.169.254 / host 网络下的宿主回环服务）——必须解析后再验。
func badHost(host string) bool {
	if host == "" || strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return true // 解析失败一律拒绝（防靠 NXDOMAIN 行为差异绕过）
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
			return true
		}
	}
	return false
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
	c.Header("Cache-Control", "no-store")
	http.ServeContent(c.Writer, c.Request, "main.mp4", time.Now(), mustOpen(path))
}

// frame 修改态活合成物：单段帧 HTML（<template> 片段，plan §4.3 预览双模）。
// 前端 LiveFrame 以 srcdoc 壳消费：壳内引项目 assets/gsap.min.js 并自行激活时间轴。
func (s *Server) frame(c *gin.Context) {
	seg := c.Param("seg")
	if !isSegID(seg) {
		c.JSON(400, gin.H{"error": "非法段标识"})
		return
	}
	f, err := os.Open(pipeline.NewProject(s.DataDir, c.Param("id")).Artifact("compositions/frames/" + seg + ".html"))
	if err != nil {
		c.JSON(404, gin.H{"error": "合成物不存在"})
		return
	}
	defer f.Close()
	c.Header("Cache-Control", "no-store")
	http.ServeContent(c.Writer, c.Request, seg+".html", time.Now(), f)
}

// asset 项目资产（gsap.min.js 等）——活合成物壳的确定性依赖，渲染引擎同款本地 gsap，无 CDN。
func (s *Server) asset(c *gin.Context) {
	// path.Clean 以 "/" 锚定后 ".." 全部被解析掉，再校验只剩相对路径
	rel := strings.TrimPrefix(path.Clean("/"+strings.TrimPrefix(c.Param("filepath"), "/")), "/")
	if rel == "" {
		c.JSON(400, gin.H{"error": "非法路径"})
		return
	}
	f, err := os.Open(filepath.Join(pipeline.NewProject(s.DataDir, c.Param("id")).Dir, "assets", filepath.FromSlash(rel)))
	if err != nil {
		c.JSON(404, gin.H{"error": "资产不存在"})
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	if st == nil || st.IsDir() {
		c.JSON(404, gin.H{"error": "资产不存在"})
		return
	}
	http.ServeContent(c.Writer, c.Request, st.Name(), time.Now(), f)
}

func isSegID(s string) bool {
	if !strings.HasPrefix(s, "seg") || len(s) == 3 {
		return false
	}
	for _, r := range s[3:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ── 方法库（plan.md §4.6：起草自动 + 入库确认）───────────────

func (s *Server) listLibrary(c *gin.Context) {
	packs, err := s.Lib.List()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if packs == nil {
		packs = []methodlib.StylePack{}
	}
	c.JSON(200, packs)
}

func (s *Server) publishPack(c *gin.Context) {
	pack, err := s.Lib.Publish(c.Param("id"))
	if err != nil {
		c.JSON(404, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, pack)
}

// ── 素材中心（素材库 catalog / 元素注册表 / 风格注册表 / 样张矩阵）──

// assets 素材库统一检索（asset-library.md §5：type 圈范围 + tags/desc 匹配）。
func (s *Server) assets(c *gin.Context) {
	if s.Assets == nil {
		c.JSON(500, gin.H{"error": "素材库未初始化"})
		return
	}
	entries := s.Assets.Search(c.Query("type"), c.Query("q"), 300)
	if entries == nil {
		entries = []assetlib.Entry{}
	}
	c.JSON(200, gin.H{"entries": entries})
}

// assetFile 素材本体（路径只出自 catalog 条目，无遍历面）。
func (s *Server) assetFile(c *gin.Context) {
	e, ok := s.Assets.Get(c.Param("id"))
	if !ok {
		c.JSON(404, gin.H{"error": "素材不存在"})
		return
	}
	c.File(filepath.Join(s.Assets.Root(), filepath.FromSlash(e.File)))
}

// registry 元素 + 风格注册表（styles 带 tokens/layouts 全量——素材中心详情抽屉
// 按 direction join 模板包用；elements 原样透传）。
func (s *Server) registry(c *gin.Context) {
	eb, err := os.ReadFile(filepath.Join(s.DataDir, "library", "registry", "elements.json"))
	if err != nil {
		c.JSON(500, gin.H{"error": "元素注册表缺失：" + err.Error()})
		return
	}
	sb, err := os.ReadFile(filepath.Join(s.DataDir, "library", "registry", "styles.json"))
	if err != nil {
		c.JSON(500, gin.H{"error": "风格注册表缺失：" + err.Error()})
		return
	}
	var styles []struct {
		ID        string          `json:"id"`
		Direction string          `json:"direction"`
		Genre     string          `json:"genre"`
		Keywords  []string        `json:"keywords"`
		Photo     string          `json:"photo"`
		Tokens    json.RawMessage `json:"tokens"`
		Layouts   []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Desc string `json:"desc"`
		} `json:"layouts"`
	}
	if err := json.Unmarshal(sb, &styles); err != nil {
		c.JSON(500, gin.H{"error": "styles.json 损坏：" + err.Error()})
		return
	}
	c.JSON(200, gin.H{"elements": json.RawMessage(eb), "styles": styles})
}

// gallery 样张矩阵清单：{styleId: [kind...]}（_overview 也计入，前端作风格封面）。
func (s *Server) gallery(c *gin.Context) {
	dir := filepath.Join(s.DataDir, "library", "registry", "gallery")
	out := map[string][]string{}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		var kinds []string
		files, _ := os.ReadDir(filepath.Join(dir, e.Name()))
		for _, f := range files {
			if name := strings.TrimSuffix(f.Name(), ".png"); name != f.Name() && name != "" {
				kinds = append(kinds, name)
			}
		}
		if len(kinds) > 0 {
			out[e.Name()] = kinds
		}
	}
	c.JSON(200, out)
}

// galleryFile 样张 PNG（重生成即换内容，短缓存即可）。
func (s *Server) galleryFile(c *gin.Context) {
	style, file := c.Param("style"), c.Param("file")
	if !isSlug(style) || !isPngName(file) {
		c.JSON(400, gin.H{"error": "非法路径"})
		return
	}
	f, err := os.Open(filepath.Join(s.DataDir, "library", "registry", "gallery", style, file))
	if err != nil {
		c.JSON(404, gin.H{"error": "样张不存在"})
		return
	}
	defer f.Close()
	c.Header("Cache-Control", "max-age=60")
	http.ServeContent(c.Writer, c.Request, file, time.Now(), f)
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
	if _, err := os.Stat(pipeline.NewProject(s.DataDir, id).Dir); err != nil {
		c.JSON(404, gin.H{"error": "项目不存在"})
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
	p := pipeline.NewProject(s.DataDir, id)
	if b, err := os.ReadFile(p.Artifact("project.json")); err == nil {
		var meta map[string]any
		if json.Unmarshal(b, &meta) == nil {
			meta["status"] = status
			_ = pipeline.WriteFileAtomic(p.Artifact("project.json"), mustJSON(meta), 0o644)
		}
	}
	_ = s.Store.UpdateStatus(id, status)
}

func mustJSON(v any) []byte {
	b, _ := json.MarshalIndent(v, "", "  ")
	return b
}

func mustOpen(path string) *os.File {
	f, _ := os.Open(path)
	return f
}

// ── 参考视频解析（素材中心「视频解析」）────────────────────────

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

// confirmReference 解析结果入库：styles.json 注册（引擎吃 tokens）+ 方法库 pack 草稿。
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
	regID := styleref.RegistryID(ref.ID)
	if err := styleref.AppendRegistry(s.DataDir, regID, ref.Style); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	desc := ref.Style.Genre
	if desc != "" {
		desc = "从参考视频「" + ref.Name + "」解析提炼；" + desc
	} else {
		desc = "从参考视频「" + ref.Name + "」解析提炼"
	}
	pack, err := s.Lib.AddVideoPack(ref.ID, ref.Style.Direction, ref.Style.Direction, desc,
		ref.Style.Samples[0].HTML, ref.Style.Samples[0].Tag)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if err := s.Refs.MarkConfirmed(ref.ID); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true, "registry_id": regID, "pack": pack})
}

// referenceFrames 参考视频抽帧截图清单（详情抽屉展示原片长相用）。目录扫 frame-*.png，
// 附是否带原视频。
func (s *Server) referenceFrames(c *gin.Context) {
	id := c.Param("id")
	if !isSlug(id) {
		c.JSON(400, gin.H{"error": "非法路径"})
		return
	}
	dir := filepath.Join(s.DataDir, "references", id)
	entries, err := os.ReadDir(dir)
	if err != nil {
		c.JSON(404, gin.H{"error": "引用不存在"})
		return
	}
	frames := []string{}
	hasVideo := false
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		switch {
		case isPngName(name) && strings.HasPrefix(name, "frame-"):
			frames = append(frames, name)
		case name == "video.mp4":
			hasVideo = true
		}
	}
	c.JSON(200, gin.H{"frames": frames, "has_video": hasVideo})
}

// referenceMedia 参考引用媒体文件（抽帧 png / 原视频 mp4，白名单文件名防目录穿越）。
func (s *Server) referenceMedia(c *gin.Context) {
	id, name := c.Param("id"), c.Param("file")
	if !isSlug(id) {
		c.JSON(400, gin.H{"error": "非法路径"})
		return
	}
	allowed := isPngName(name) && strings.HasPrefix(name, "frame-")
	allowed = allowed || name == "video.mp4"
	if !allowed {
		c.JSON(400, gin.H{"error": "非法文件名"})
		return
	}
	f, err := os.Open(filepath.Join(s.DataDir, "references", id, name))
	if err != nil {
		c.JSON(404, gin.H{"error": "文件不存在"})
		return
	}
	defer f.Close()
	c.Header("Cache-Control", "max-age=3600")
	http.ServeContent(c.Writer, c.Request, name, time.Now(), f)
}
