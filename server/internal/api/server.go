// Package api：HTTP 层（Gin）。REST + SSE；磁盘是产物事实源，PG 存案卷。
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
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
	"webpage-video-agent/server/internal/artifacts"
	"webpage-video-agent/server/internal/auth"
	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/events"
	"webpage-video-agent/server/internal/pipeline"
	"webpage-video-agent/server/internal/prep"
	"webpage-video-agent/server/internal/produce"
	"webpage-video-agent/server/internal/store"
	"webpage-video-agent/server/internal/stylelib"
	"webpage-video-agent/server/internal/styleref"
)

type Server struct {
	DataDir   string
	RootDir   string
	Store     store.Store
	Hub       *events.Hub
	Agent     *agent.Agent
	Producer  *produce.Runner
	Preps     *prep.Runner // 准备阶段运行器（想法页签/对话启动三工位链；nil = 未接线）
	Lib       *stylelib.Library
	Refs      *styleref.Store
	Artifacts artifacts.Store // 产物正本后端（nil/本地 = 读路径不走对象回源）

	// APIToken 非空时保留为机器通道（脚本/m0 类调用方）：X-API-Token 头（SSE 另收
	// ?token=），等同 admin 身份。人用的浏览器一律走登录会话 cookie。
	APIToken string

	// SessionSecret 会话 cookie 签名密钥；AuthActive=false = 本机开放模式
	//（SESSION_SECRET 与 API_TOKEN 都未配置）：全部接口不设防（与旧无鉴权行为一致），
	// 登录/归属守卫全部旁路。部署公网必须二选一配置。
	SessionSecret []byte
	AuthActive    bool

	// CookieSecure 强制会话 cookie 带 Secure 标记（COOKIE_SECURE=1/true）。默认自动：
	// 直连 TLS 或反代 X-Forwarded-Proto=https 时置位——Docker 部署 TLS 在反代终止
	// 时 Go 侧看不到 TLS，30 天长效 cookie 会走反代→后端明文链路（2026-10-26 审查）。
	CookieSecure bool

	// loginFails 登录失败窗口："ip|username" → *failWindow（见 login）。
	loginFails sync.Map

	// videoStat 远端成片 Stat 的短 TTL 记忆（videoStatEntry）：listProjects/
	// overview/projectView 轮询热路径上回退远端 Stat 是同步网络调用（MinIO 30s
	// 超时），N 项目×3-5s 一轮会把抖动放大成整页挂死（2026-10-19 审查中危）。
	// 正反结果都记 5s——状态灯场景 5s 陈旧无感。
	videoStat sync.Map

	turnMu sync.Map // projectID → *sync.Mutex：同项目对话轮串行（见 chat）

	// turnCancels projectID → context.CancelFunc：正在执行的对话轮取消函数（见 chat）。
	// 拿到 turnMu 后才注册、轮终了即删——cancelChat 只打得到在跑的那一轮。
	turnCancels sync.Map

	// chatSlots 对话轮排队信号量（容量 chatQueueMax）：每条 POST /chat 起一个
	// goroutine 排队等 turnMu，无上限时连点消息会堆出一串不可取消的排队轮。
	// 非阻塞占位，满即 429 拒新。sync.Once 惰性建 channel——Server 是字面量
	// 构造无构造函数，channel 字段的零值 nil 会让 select 永远走 default。
	chatSlotsOnce sync.Once
	chatSlots     chan struct{}
}

func (s *Server) Router() *gin.Engine {
	r := gin.Default()
	r.MaxMultipartMemory = 8 << 20

	// 登录会话：不进鉴权组（login/status 是准入入口本体）。用户管理挂在鉴权 + 管理员双守卫后。
	authG := r.Group("/api/auth", s.bodyGuard())
	authG.GET("/status", s.authStatus)
	authG.POST("/login", s.login)
	authG.POST("/logout", s.logout)
	authAdm := authG.Group("", s.requireUser(), s.requireAdmin())
	authAdm.GET("/users", s.listUsers)
	authAdm.POST("/users", s.createAccount)

	api := r.Group("/api", s.requireUser(), s.bodyGuard(), secureParams(), s.projectGuard())
	api.POST("/projects", s.createProject)
	api.GET("/projects", s.listProjects)
	api.GET("/projects/:id", s.projectView)
	api.DELETE("/projects/:id", s.deleteProject)
	api.GET("/projects/:id/manuscript", s.manuscript)
	api.PUT("/projects/:id/manuscript", s.saveManuscript)
	api.PUT("/projects/:id/storyboard", s.saveStoryboard)
	api.GET("/projects/:id/storyboard", s.storyboard)
	api.GET("/projects/:id/style", s.style)
	api.POST("/projects/:id/style/apply", s.applyStyle)
	// 准备阶段三工件（prep-agents-design）：打包终审视图与确认门
	api.GET("/projects/:id/prep", s.prepView)
	api.POST("/projects/:id/prep/confirm", s.confirmPrep)
	// 想法页签：启动/取消三工位链（异步，SSE 播报进度）
	api.POST("/projects/:id/prep/run", s.runPrep)
	api.POST("/projects/:id/prep/cancel", s.cancelPrep)
	// 项目材料库（主题页上传的参考资料，①文稿 read_source 消费）
	api.GET("/projects/:id/materials", s.listMaterials)
	api.POST("/projects/:id/materials", s.saveMaterial)
	api.DELETE("/projects/:id/materials", s.deleteMaterial)
	api.POST("/projects/:id/chat", s.chat)
	api.POST("/projects/:id/chat/cancel", s.cancelChat)
	api.GET("/projects/:id/messages", s.messages)
	api.GET("/projects/:id/events", s.sse)
	api.GET("/projects/:id/activity", s.activity)
	api.GET("/projects/:id/transcript", s.transcript)
	api.POST("/projects/:id/produce", s.startProduce)
	api.POST("/projects/:id/cancel", s.cancelProduce)
	api.POST("/projects/:id/bgm", s.setProjectBGM)
	api.GET("/projects/:id/video/main.mp4", s.video)
	api.GET("/projects/:id/cover.png", s.coverPNG)
	api.POST("/projects/:id/cover", s.regenCover)
	api.GET("/projects/:id/style/clips/:file", s.styleClip)
	api.GET("/projects/:id/audio_meta", s.audioMeta)
	api.GET("/bgm/library", s.bgmLibrary)
	api.GET("/bgm/audio/:name", s.bgmAudio)
	api.GET("/projects/:id/music/audio", s.musicAudio)
	api.GET("/library", s.listLibrary)
	api.GET("/library/:id/frames", s.libraryFrames)
	api.GET("/library/:id/media/:file", s.libraryMedia)
	api.GET("/vocab", s.listVocab)
	api.GET("/references", s.listReferences)
	api.GET("/references/:id/frames", s.referenceFrames)
	api.GET("/references/:id/media/:file", s.referenceMedia)
	// 全局资产（风格库/参考库）的写操作影响所有用户，普通账号不该能删库/发布/入库
	// （读路径照旧登录即可——项目归属守卫只覆盖 projects/:id，全局资产此前是全员可写）。
	libAdm := api.Group("", s.requireAdmin())
	libAdm.DELETE("/library/:id", s.deleteLibraryEntry)
	libAdm.POST("/library/:id/publish", s.publishPack)
	// 蒸馏工位（docs/style-kit-distillation-plan §3/§4）：懒触发——admin 单发开单，
	// 状态轮询同一资源位。全局资产写操作收管理员（2026-10-26 安全口径）。
	libAdm.POST("/library/:id/distill", s.startDistill)
	libAdm.GET("/library/:id/distill", s.distillStatus)
	libAdm.POST("/references", s.uploadReference)
	libAdm.POST("/references/:id/analyze", s.analyzeReference)
	libAdm.POST("/references/:id/confirm", s.confirmReference)
	libAdm.PUT("/references/:id/draft", s.updateReferenceDraft)
	// 项目解析（两路解析入口二，2026-10-29）：读项目定稿设计系统+样张帧 → 暂存条目
	// → 文本打标；与上传视频解析同走暂存区/修订/确认入库。同属全局资产写口径收管理员。
	libAdm.POST("/projects/:id/style/parse", s.parseProjectStyle)
	// 模型设置（docs/llm-settings-2026-10-29.md）：供应商台账 + 工位绑定，PUT 落盘
	// 即热生效。URL/Key 属运维面，admin 收口；GET 只出掩码。
	llmAdm := api.Group("", s.requireAdmin())
	llmAdm.GET("/llm-settings", s.getLLMSettings)
	llmAdm.PUT("/llm-settings", s.saveLLMSettings)
	llmAdm.POST("/llm-settings/test", s.testLLMProvider)
	// 留痕图片取件走通配路径（style/xxx、reports/prereview/round1/xxx 多级目录），
	// 进不了 secureParams（它禁参数里的斜杠）——单开一组，鉴权/项目归属照旧，
	// 穿越防线由 handler 侧路径清洗 + 目录/扩展名白名单承担。
	fileG := r.Group("/api", s.requireUser(), s.bodyGuard(), s.projectGuard())
	fileG.GET("/projects/:id/file/*path", s.projectFile)
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

// ── 鉴权：登录会话 + 项目归属 ─────────────────────────────────

// 会话身份在 gin context 里的键（currentUser 读取）。
const (
	ctxUID  = "auth_uid"
	ctxRole = "auth_role"
	ctxName = "auth_username"
)

// requireUser 准入守卫：开放模式旁路（同旧无鉴权行为）；否则认会话 cookie，
// 机器 token（X-API-Token / ?token=，常量时间比较）视作 admin。SSE（EventSource）
// 同源自动带 cookie，无需再走查询参数；?token= 仅服务机器调用方。
func (s *Server) requireUser() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !s.AuthActive {
			c.Next()
			return
		}
		if cl, err := s.sessionClaims(c); err == nil {
			c.Set(ctxUID, cl.UID)
			c.Set(ctxRole, cl.Role)
			c.Set(ctxName, cl.Username)
			c.Next()
			return
		}
		if s.machineToken(c) {
			c.Set(ctxUID, "") // 机器身份：无 uid，创建的项目归系统（仅管理员可见）
			c.Set(ctxRole, "admin")
			c.Set(ctxName, "machine")
			c.Next()
			return
		}
		c.JSON(401, gin.H{"error": "未登录或会话已过期"})
		c.Abort()
	}
}

// requireAdmin 管理员守卫（用户管理端点）。开放模式旁路——开放模式本就全部不设防。
func (s *Server) requireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !s.AuthActive {
			c.Next()
			return
		}
		if _, role := s.currentUser(c); role == "admin" {
			c.Next()
			return
		}
		c.JSON(403, gin.H{"error": "需要管理员权限"})
		c.Abort()
	}
}

// projectGuard 项目归属守卫：统一拦 /api/projects/:id… 全部读写（含 messages/video/
// produce 等绕过 s.project() 的 handler，一处收口）。归属记在 project.json（磁盘
// 快照），DB 行同步落列；旧项目无 user_id = 系统项目，仅管理员可见。
// 归属不符与不存在同回 404——不向探测者暴露项目存在性。
// 本地缺货的云端项目（换机场景）：账本行在、归属对得上 → 先恢复小件再放行
// （打开项目即恢复，docs/storage-rework-2026-10-28.md §3.2）。
func (s *Server) projectGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !s.AuthActive {
			c.Next()
			return
		}
		if !strings.HasPrefix(c.Request.URL.Path, "/api/projects/") {
			c.Next()
			return
		}
		id := c.Param("id")
		if id == "" {
			c.Next()
			return
		}
		pj := filepath.Join(s.DataDir, "projects", id, "project.json")
		owner, err := projectOwner(pj)
		if err == nil && s.canAccess(c, owner) {
			c.Next()
			return
		}
		// 磁盘没有 project.json：可能是云端项目首触。账本行可查且归属对得上就
		// 恢复小件后重验；账本也无行 = 真不存在，照旧 404。
		if err != nil && s.restoreOpenProject(c, id) {
			if owner, err := projectOwner(pj); err == nil && s.canAccess(c, owner) {
				c.Next()
				return
			}
		}
		c.JSON(404, gin.H{"error": "项目不存在"})
		c.Abort()
	}
}

// openRestored 已做过「打开即恢复」的项目（进程内一次；失败也标记，防每次请求
// 都同步等一轮远端 List）。
var openRestored sync.Map

// restoreOpenProject 云端项目首触的小件恢复：账本行归属校验通过才动手（不向
// 无权者泄露云端有无此项目——404 口径不变）。只回源 ≤openRestoreMaxFile 的
// Keeper（契约/transcripts/manifest/样张图），视频等大件留给流式回源与开跑前
// 的 EnsureWorkspace 全量恢复。
func (s *Server) restoreOpenProject(c *gin.Context, id string) bool {
	if s.Artifacts == nil || !s.Artifacts.Available() {
		return false
	}
	if _, loaded := openRestored.LoadOrStore(id, struct{}{}); loaded {
		return false // 本进程已试过（成功与否），不再阻塞请求
	}
	row, err := s.Store.GetProject(id)
	if err != nil || !s.canAccess(c, row.UserID) {
		return false
	}
	n, err := artifacts.RestoreSmall(s.Artifacts, s.DataDir, artifacts.LocalPrefix(id),
		openRestoreMaxFile, openRestoreMaxTotal)
	if err != nil {
		log.Printf("[artifacts] %s 打开恢复中断: %v", id, err)
		return false
	}
	if n > 0 {
		log.Printf("[artifacts] %s 打开恢复 %d 个小件", id, n)
	}
	return true
}

// 打开恢复的尺寸闸：单件 ≤16MB（契约/transcripts/样张图都远小于此，视频/字体
// 天然出局）；整次 ≤128MB（判卷截图类大户封顶，首开体验有界）。
const (
	openRestoreMaxFile  = 16 << 20
	openRestoreMaxTotal = 128 << 20
)

// projectOwner 读 project.json 的 user_id；文件缺失/损坏 = 项目不存在（err）。
func projectOwner(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var meta struct {
		UserID string `json:"user_id"`
	}
	if json.Unmarshal(b, &meta) != nil {
		return "", fmt.Errorf("project.json 损坏")
	}
	return meta.UserID, nil
}

// canAccess 管理员全通；普通用户仅限自己的项目（无主项目不可见）。
func (s *Server) canAccess(c *gin.Context, owner string) bool {
	uid, role := s.currentUser(c)
	return role == "admin" || (owner != "" && owner == uid)
}

func (s *Server) currentUser(c *gin.Context) (uid, role string) {
	if v, ok := c.Get(ctxUID); ok {
		uid, _ = v.(string)
	}
	if v, ok := c.Get(ctxRole); ok {
		role, _ = v.(string)
	}
	return uid, role
}

// sessionClaims 从 cookie 取并验会话。
func (s *Server) sessionClaims(c *gin.Context) (auth.Claims, error) {
	v, err := c.Cookie(sessionCookie)
	if err != nil || v == "" {
		return auth.Claims{}, auth.ErrBadSession
	}
	return auth.VerifySession(s.SessionSecret, v, time.Now())
}

// machineToken 机器通道：X-API-Token 头（SSE 另收 ?token=），常量时间比较。
func (s *Server) machineToken(c *gin.Context) bool {
	if s.APIToken == "" {
		return false
	}
	tok := c.GetHeader("X-API-Token")
	if tok == "" {
		tok = c.Query("token")
	}
	return tok != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(s.APIToken)) == 1
}

// ── 登录会话端点 ─────────────────────────────────────────────

const (
	sessionCookie = "zs_session"
	sessionTTL    = 30 * 24 * time.Hour
)

// authStatus 启动首查：是否需要登录 + 当前用户（一并回答，前端一次往返定去留）。
func (s *Server) authStatus(c *gin.Context) {
	out := gin.H{"auth_required": s.AuthActive, "user": nil}
	if !s.AuthActive {
		out["user"] = gin.H{"id": "", "username": "local", "role": "admin"} // 开放模式 = 本机全权
	} else if cl, err := s.sessionClaims(c); err == nil {
		out["user"] = gin.H{"id": cl.UID, "username": cl.Username, "role": cl.Role}
	}
	c.JSON(200, out)
}

// login 用户名口令 → HttpOnly 签名会话 cookie。失败口径统一（用户名/密码不分家，
// 不暴露账号存在性），并按 ip+用户名 限失败窗口。
func (s *Server) login(c *gin.Context) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || in.Username == "" || in.Password == "" {
		c.JSON(400, gin.H{"error": "用户名和密码不能为空"})
		return
	}
	if !s.allowLoginAttempt(c.ClientIP(), in.Username) {
		c.JSON(429, gin.H{"error": "失败次数过多，15 分钟后再试"})
		return
	}
	u, err := s.Store.UserByName(in.Username)
	if err != nil || !auth.CheckPassword(u.PasswordHash, in.Password) {
		s.recordLoginFail(c.ClientIP(), in.Username)
		c.JSON(401, gin.H{"error": "用户名或密码错误"})
		return
	}
	s.clearLoginFails(c.ClientIP(), in.Username)
	s.setSession(c, auth.Claims{
		UID: u.ID, Username: u.Username, Role: u.Role,
		Exp: time.Now().Add(sessionTTL).Unix(),
	})
	c.JSON(200, gin.H{"id": u.ID, "username": u.Username, "role": u.Role})
}

// logout 清会话 cookie（MaxAge=-1 即删）。无状态签名无需服务端注销记账。
func (s *Server) logout(c *gin.Context) {
	s.setSession(c, auth.Claims{}) // 空 Exp → 过期 cookie，浏览器即删
	c.JSON(200, gin.H{"ok": true})
}

// setSession 写会话 cookie。cl.Exp 为零值时写立即过期的删除型 cookie（logout 用）。
func (s *Server) setSession(c *gin.Context, cl auth.Claims) {
	val := ""
	exp := time.Unix(cl.Exp, 0)
	if cl.Exp > 0 {
		val = auth.SignSession(s.SessionSecret, cl)
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name: sessionCookie, Value: val, Path: "/",
		Expires:  exp,
		MaxAge:   int(time.Until(exp).Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.cookieSecure(c),
	})
}

// cookieSecure 会话 cookie 的 Secure 判定：COOKIE_SECURE 显式强制，或直连 TLS，
// 或反代告知 https（X-Forwarded-Proto）。转发头只在「反代如实设置」的前提下可信；
// 直连部署下客户端伪造该头只会让自己的 cookie 带 Secure（无害），反代部署下缺了
// 它 30 天长效 cookie 就恒走明文链路——两害相权取其听。
func (s *Server) cookieSecure(c *gin.Context) bool {
	if s.CookieSecure {
		return true
	}
	return c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https")
}

// createAccount 管理员建账号（固定账号模型，无自助注册）。
func (s *Server) createAccount(c *gin.Context) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	if !auth.ValidUsername(in.Username) {
		c.JSON(400, gin.H{"error": "用户名须为 2-32 位字母/数字/_/-"})
		return
	}
	if len(in.Password) < 6 {
		c.JSON(400, gin.H{"error": "密码至少 6 位"})
		return
	}
	if in.Role == "" {
		in.Role = "user"
	}
	if in.Role != "admin" && in.Role != "user" {
		c.JSON(400, gin.H{"error": "role 只能是 admin 或 user"})
		return
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	u := &store.User{Username: in.Username, Role: in.Role, PasswordHash: hash}
	if err := s.Store.CreateUser(u); err != nil {
		if errors.Is(err, store.ErrDuplicateUser) {
			c.JSON(409, gin.H{"error": "用户名已存在"})
			return
		}
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, u)
}

func (s *Server) listUsers(c *gin.Context) {
	users, err := s.Store.ListUsers()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if users == nil {
		users = []store.User{}
	}
	c.JSON(200, users)
}

// ── 登录限速 ─────────────────────────────────────────────────

// failWindow 失败计数窗口：达到上限且未到期 = 拒绝。到期后首笔失败重开窗口。
type failWindow struct {
	mu    sync.Mutex // sync.Map 只护 map 本身，值对象字段并发读写是数据竞争（2026-10-19 中-33）
	n     int
	until time.Time
}

const (
	loginMaxFails = 5
	loginWindow   = 15 * time.Minute
)

// allowLoginAttempt 窗口内失败未达上限（或已到期）才放行。
func (s *Server) allowLoginAttempt(ip, username string) bool {
	v, ok := s.loginFails.Load(ip + "|" + username)
	if !ok {
		return true
	}
	f := v.(*failWindow)
	f.mu.Lock()
	defer f.mu.Unlock()
	return time.Now().After(f.until) || f.n < loginMaxFails
}

func (s *Server) recordLoginFail(ip, username string) {
	k := ip + "|" + username
	now := time.Now()
	if v, ok := s.loginFails.Load(k); ok {
		f := v.(*failWindow)
		f.mu.Lock()
		if now.After(f.until) {
			f.n, f.until = 0, now.Add(loginWindow)
		}
		f.n++
		f.mu.Unlock()
		return
	}
	s.loginFails.LoadOrStore(k, &failWindow{n: 1, until: now.Add(loginWindow)})
}

func (s *Server) clearLoginFails(ip, username string) {
	s.loginFails.Delete(ip + "|" + username)
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
		Mode        string `json:"mode"`         // script（默认，文稿驱动）| music（音乐驱动 PV）
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
	// 新建只要求名称（2026-10-30 起主题挪进工作区「主题」页签——那里能写想法、
	// 传材料，是比弹窗里的单行框更完整的输入位）。带稿直建是老路径，兼容保留。
	_ = in.Topic
	if in.Aspect != "16:9" && in.Aspect != "9:16" {
		in.Aspect = "9:16"
	}
	// 驱动模式：只认 script/music，空值与未知值一律回文稿驱动（老客户端不传
	// mode、字段拼错都不能把项目建进半开状态）。music 可先建占位——准备工位
	// （做曲→音乐地图→分镜）M1 上线，制作入口 startProduce 有显式拦截。
	if in.Mode != "music" {
		in.Mode = "script"
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
		if strings.TrimSpace(in.Name) == "" {
			in.Name = "未命名视频"
		}
	}
	// id 秒级时间戳：并发/连点创建会同秒撞号——os.Mkdir 原子占号（先 Stat 后建
	// 有 TOCTOU 缝：双方都探到不存在再都 MkdirAll，同目录互踩 project.json）。
	// 与 styleref.Save 同一写法。
	id := "p" + time.Now().Format("20060102-150405")
	p := pipeline.NewProject(s.DataDir, id)
	// os.Mkdir 只建最后一层：全新数据目录还没有 projects/ 时，占号会报路径不存在
	if err := os.MkdirAll(filepath.Join(s.DataDir, "projects"), 0o755); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	for n := 2; ; n++ {
		if err := os.Mkdir(p.Dir, 0o755); err == nil {
			break
		} else if !os.IsExist(err) {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		id = fmt.Sprintf("p%s-%d", time.Now().Format("20060102-150405"), n)
		p = pipeline.NewProject(s.DataDir, id)
	}
	for _, d := range []string{
		"input", "llm", "manuscripts", "storyboards", "style",
		"audio", "compositions/frames", "compositions/registry", "renders",
	} {
		_ = os.MkdirAll(filepath.Join(p.Dir, d), 0o755)
	}
	// 用户域前缀（<user_id>/<项目ID>/，系统项目回镜像布局）：先灌 artifacts 前缀表
	// 再落账——此后本项目的全部 key 组装与上传都走这条映射（账本为准，代码不拼装归属）。
	uid, _ := s.currentUser(c)
	storagePrefix := uid + "/" + id
	if uid == "" {
		storagePrefix = "projects/" + id // 机器 token 建的系统项目：无用户域，回镜像布局
	}
	artifacts.SeedPrefix(id, storagePrefix)
	if in.Manuscript != "" {
		if err := os.WriteFile(p.Artifact("input/article.txt"), []byte(in.Manuscript), 0o644); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		s.putArtifact(artifacts.ProjectKey(id, "input/article.txt"), p.Artifact("input/article.txt"))
	}
	meta, _ := json.MarshalIndent(map[string]any{
		"id": id, "name": in.Name, "status": "created", "aspect": in.Aspect,
		"topic": in.Topic, "style_id": styleID, "mode": in.Mode,
		"user_id":    uid, // 归属（磁盘快照；projectGuard 读它，账本行同列）
		"created_at": time.Now().Format(time.RFC3339),
	}, "", "  ")
	_ = os.WriteFile(p.Artifact("project.json"), meta, 0o644)
	// 项目账本行：可检索元数据 + 桶内相对路径 + 驱动模式（列表事实源，方案甲）。
	// 建行先行于内容上传，保证 PG 行/前缀表与首批 key 同时就位。
	_ = s.Store.CreateProject(id, in.Name, uid)
	_ = s.Store.UpdateProjectMeta(id, store.ProjectMeta{
		Topic: in.Topic, Aspect: in.Aspect, Mode: in.Mode,
		Status: "created", StoragePrefix: storagePrefix,
	})
	s.putArtifact(artifacts.ProjectKey(id, "project.json"), p.Artifact("project.json"))
	createdLabel := in.Aspect
	if in.Mode == "music" {
		createdLabel = "音乐驱动 PV · " + in.Aspect
	}
	p.Manifest("project.created", fmt.Sprintf("%s（%s）", in.Name, createdLabel))
	s.Hub.Emit(id, "msg", "", "项目已创建："+in.Name)
	c.JSON(200, gin.H{"id": id, "name": in.Name, "aspect": in.Aspect})
}

func (s *Server) listProjects(c *gin.Context) {
	// 列表事实源 = PG projects 账本（方案甲，docs/storage-rework-2026-10-28.md §4）：
	// 跨机器换机也能看到全部项目；本地盘只作展示态补充（producing 灯/成片在否），
	// 不再是数据来源——磁盘扫描与账行自愈指纹机制就此退役。
	uid, role := s.currentUser(c)
	filterByOwner := s.AuthActive && role != "admin"
	type row struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Status      string `json:"status"`
		CreatedAt   string `json:"created_at"`
		HasVideo    bool   `json:"has_video"`
		LastProduce any    `json:"last_produce"`
		Topic       string `json:"topic,omitempty"`
		Mode        string `json:"mode,omitempty"` // music=音乐驱动 PV（其余/空=文稿，省字段）
		Local       bool   `json:"local"`          // 工作区在本机（false=只在云端，打开时会先恢复小件）
	}
	// 预分配非 nil：空列表必须序列化成 [] 而不是 null（前端读 .length 会崩）
	out := make([]row, 0, 16)
	if s.Store.Kind() != "pg" {
		// 内存存储（demo 容器/临时无库开发）：没有账本可读，磁盘扫描保命——
		// 只补 EnsureProject 行（demo 聊天案卷要行），元数据同步不再回写。
		entries, _ := os.ReadDir(filepath.Join(s.DataDir, "projects"))
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), "_") {
				continue
			}
			b, err := os.ReadFile(filepath.Join(s.DataDir, "projects", e.Name(), "project.json"))
			if err != nil {
				continue
			}
			var meta struct {
				Name      string `json:"name"`
				Status    string `json:"status"`
				CreatedAt string `json:"created_at"`
				UserID    string `json:"user_id"`
				Topic     string `json:"topic"`
				Mode      string `json:"mode"`
			}
			if json.Unmarshal(b, &meta) != nil || meta.Name == "" {
				continue
			}
			if filterByOwner && meta.UserID != uid {
				continue
			}
			_ = s.Store.EnsureProject(e.Name(), meta.Name)
			status := meta.Status
			if s.Producer.IsRunning(e.Name()) {
				status = "producing"
			}
			var lastProduce any
			if ev, _, ts := produce.LastTerminal(pipeline.NewProject(s.DataDir, e.Name())); ev != "" {
				lastProduce = gin.H{"event": ev, "ts": ts}
			}
			out = append(out, row{e.Name(), meta.Name, status, meta.CreatedAt,
				s.hasFinalVideo(e.Name()), lastProduce, meta.Topic, meta.Mode, true})
		}
	} else {
		rows, err := s.Store.ListProjects()
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		for _, r := range rows {
			if r.Name == "" {
				continue
			}
			if filterByOwner && r.UserID != uid {
				continue // 系统项目 user_id 空 ≠ 任何普通用户的 uid，天然被滤掉
			}
			status := r.Status
			if s.Producer.IsRunning(r.ID) {
				status = "producing" // 本机在跑：展示态优先于账面（账面由 startProduce 落）
			}
			var lastProduce any
			if r.LastEvent != "" {
				ts := r.LastEventTs
				if ts.IsZero() {
					ts = r.UpdatedAt
				}
				lastProduce = gin.H{"event": r.LastEvent, "ts": ts.Format(time.RFC3339)}
			}
			_, diskErr := os.Stat(filepath.Join(s.DataDir, "projects", r.ID))
			out = append(out, row{r.ID, r.Name, status, r.CreatedAt.Format(time.RFC3339),
				s.hasFinalVideo(r.ID), lastProduce, r.Topic, r.Mode, diskErr == nil})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	c.JSON(200, out)
}

// hasFinalVideo 成片在否的统一口径：本地 miss 回退远端正本 Stat（与 video handler
// 同源）——GC 清理后的成片只在云端，只查本地会把能正常播放的片子报成「无片」，
// 段级重做入口也被误拒（2026-10-19 高-3）。listProjects/projectView/overview/
// startRework（经 agent.State）四处都必须走这一口径。
func (s *Server) hasFinalVideo(id string) bool {
	if _, err := os.Stat(filepath.Join(s.DataDir, "projects", id, "renders", "main.mp4")); err == nil {
		return true
	}
	if s.Artifacts == nil {
		return false
	}
	if v, ok := s.videoStat.Load(id); ok {
		if e := v.(videoStatEntry); time.Since(e.at) < videoStatTTL {
			return e.ok
		}
	}
	ok := s.Artifacts.Stat(artifacts.ProjectKey(id, "renders/main.mp4"))
	s.videoStat.Store(id, videoStatEntry{ok: ok, at: time.Now()})
	return ok
}

// videoStatTTL / videoStatEntry 见 Server.videoStat 注释。
const videoStatTTL = 5 * time.Second

type videoStatEntry struct {
	ok bool
	at time.Time
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
	bgmName, bgmReason := "", ""
	if b, err := os.ReadFile(p.Artifact("project.json")); err == nil {
		var meta struct {
			Aspect    string `json:"aspect"`
			Topic     string `json:"topic"`
			BGM       string `json:"bgm"`
			BGMReason string `json:"bgm_reason"`
		}
		if json.Unmarshal(b, &meta) == nil {
			if meta.Aspect != "" {
				aspect = meta.Aspect
			}
			topic = meta.Topic
			bgmName, bgmReason = strings.TrimSpace(meta.BGM), meta.BGMReason
		}
	}
	mode := projectMode(p)
	var lastProduce any
	if ev, _, ts := produce.LastTerminal(p); ev != "" {
		lastProduce = gin.H{"event": ev, "ts": ts}
	}
	var cover any
	if cs := produce.LoadCover(p); cs != nil {
		cover = gin.H{"t": cs.T, "title": cs.Title}
	}
	pt, ct, ncalls, ncached := agentTokenTotals(p.Artifact("manifest.jsonl"))
	c.JSON(200, gin.H{
		"id": id, "name": st.Name, "aspect": aspect, "topic": topic, "mode": mode,
		"bgm":   gin.H{"name": bgmName, "reason": bgmReason},
		"cover": cover,
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
		// 全程 token 消耗（manifest 全量 agent.done 累计，「过程」页头部展示）
		"tokens": gin.H{"prompt": pt, "completion": ct, "calls": ncalls, "cached": ncached},
	})
}

// projectMode 读 project.json 驱动模式（script=文稿驱动现行管线｜music=音乐
// 驱动 PV）。实现收在 prep 包（prep 链按它分流），api 这层只是转接。
func projectMode(p *pipeline.Project) string {
	return prep.ProjectMode(p)
}

// agentTokenTotals manifest 全量扫 agent.done，累计整个项目所有智能体任务的 token
// 用量与调用次数（「过程」页「全程」合计；活动流接口只回尾部窗口，算不了全程）。
// 无 manifest / 空项目返回零值，不报错。projectView 每次轮询都调——按
// (size,mtime) 记忆化：manifest 追加一行必然改变二者之一，缓存随之失效，
// 结果与全量重算等价，却把 I/O 从「随历史线性」压回常数（2026-10-19 中-31；
// overview 注释与 activity 的 ReadJSONLTail 同一教训，不能开倒车）。
type tokenTotalsEntry struct {
	size, modNS int64
	p, c, n, cd int
}

var tokenTotalsCache sync.Map // path → tokenTotalsEntry

func agentTokenTotals(path string) (prompt, completion, calls, cached int) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, 0, 0, 0
	}
	if v, ok := tokenTotalsCache.Load(path); ok {
		if e := v.(tokenTotalsEntry); e.size == info.Size() && e.modNS == info.ModTime().UnixNano() {
			return e.p, e.c, e.n, e.cd
		}
	}
	prompt, completion, calls, cached = scanAgentTokenTotals(path)
	tokenTotalsCache.Store(path, tokenTotalsEntry{info.Size(), info.ModTime().UnixNano(), prompt, completion, calls, cached})
	return prompt, completion, calls, cached
}

func scanAgentTokenTotals(path string) (prompt, completion, calls, cached int) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, 0, 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		var ev struct {
			Event            string `json:"event"`
			PromptTokens     int    `json:"prompt_tokens"`
			CompletionTokens int    `json:"completion_tokens"`
			Calls            int    `json:"calls"`
			CachedTokens     int    `json:"cached_tokens"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil || ev.Event != "agent.done" {
			continue
		}
		prompt += ev.PromptTokens
		completion += ev.CompletionTokens
		calls += ev.Calls
		cached += ev.CachedTokens
	}
	return prompt, completion, calls, cached
}

// 留痕图片取件白名单：目录前缀 + 扩展名双闸——「过程」页对话里的缩略图/放大图
// 只可能落在这些目录（样张/真迹/素材；段审锚点帧/边界末首帧/终审帧格缓存于
// reports/audit/frames）。清单外一律 403，路径清洗防穿越。
var transcriptFileDirs = []string{"style/", "assets/img/", "reports/audit/", "reports/frameqa/", "reports/prereview/"}
var transcriptFileExts = map[string]bool{".png": true, ".jpg": true, ".jpeg": true}

// projectFile 项目内媒体取件：GET /projects/:id/file/style/sample-0.png。
// 「过程」页对话留痕里的注入图（llm/transcripts 的 images 字段）靠它渲染缩略图
// 与点击放大。鉴权走 /api 组中间件；读不到 404。
func (s *Server) projectFile(c *gin.Context) {
	p, ok := s.project(c)
	if !ok {
		return
	}
	rel := strings.TrimPrefix(c.Param("path"), "/")
	if rel == "" || filepath.ToSlash(filepath.Clean(rel)) != rel {
		c.JSON(400, gin.H{"error": "路径不合法"})
		return
	}
	allowed := false
	for _, d := range transcriptFileDirs {
		if strings.HasPrefix(rel, d) {
			allowed = true
			break
		}
	}
	if !allowed || !transcriptFileExts[strings.ToLower(filepath.Ext(rel))] {
		c.JSON(403, gin.H{"error": "文件不在取件白名单"})
		return
	}
	abs := p.Artifact(filepath.FromSlash(rel))
	if _, err := os.Stat(abs); err != nil {
		c.JSON(404, gin.H{"error": "文件不存在"})
		return
	}
	c.File(abs)
}

// deleteProject 删除项目：磁盘目录 + DB 镜像（行/聊天/任务）。
// 制作中/准备链运行中拒绝——后台链还在写目录，RemoveAll 会与写方竞速留半删
// 残骸，prep 收尾 SyncUp 还会把已删项目的产物重新传回云端（留尸）。
func (s *Server) deleteProject(c *gin.Context) {
	id := c.Param("id")
	if s.Producer != nil && s.Producer.IsRunning(id) {
		c.JSON(409, gin.H{"error": "制作/重做进行中，先停止再删除"})
		return
	}
	// prep 链同口径（2026-10-19 高-4）：先请后台链收手（协作式取消），仍在跑就拒绝
	if s.Preps != nil {
		if s.Preps.IsRunning(id) {
			c.JSON(409, gin.H{"error": "准备阶段运行中，先停止再删除"})
			return
		}
		s.Preps.Cancel(id)
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
	// 存储层已上抛 DB 错误（bad59fa 中-32），这里必须接住：吞成假成功会让镜像行
	// 变孤儿且无从排查（2026-10-19 审查实锤：`_ =` 丢弃，修复只做了半截）
	dbErr := s.Store.DeleteProject(id)
	artifacts.DeletePrefixBestEffort(s.Artifacts, artifacts.ProjectKey(id, "")) // 远端正本跟着走
	s.Hub.Forget(id)                                                            // 重放窗跟着项目走：不删的话删了的项目在 SSE 侧阴魂不散
	// 在跑的对话轮先取消再清账：项目目录已删，轮还握着旧句柄继续跑只会写出幽灵产物
	if v, loaded := s.turnCancels.LoadAndDelete(id); loaded {
		v.(context.CancelFunc)()
	}
	s.turnMu.Delete(id)     // 对话轮互斥锁同理（sync.Map 只增不减的慢泄漏）
	openRestored.Delete(id) // 打开恢复记账同步清掉，同名重建能再触发
	if dbErr != nil {
		// 磁盘已删、DB 行清不掉=镜像残留：如实 500 不装成功；后续清理与 DB 无关已照做
		log.Printf("[delete] 项目 %s 磁盘已删但 DB 行清理失败: %v", id, dbErr)
		c.JSON(500, gin.H{"error": "项目已删除，但记录清理失败（可能残留镜像行，请查服务端日志）"})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

func (s *Server) manuscript(c *gin.Context) {
	p, ok := s.project(c) // 不存在的项目统一 404「项目不存在」，不与「文稿不存在」混谈
	if !ok {
		return
	}
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
	// 制作中拒绝保存：管线正拿着旧稿配音/拼画面，半道换 article.txt 会让本轮
	// 产物一半旧一半新（与 deleteProject 的 IsRunning 拒绝同一口径）。
	if s.Producer != nil && s.Producer.IsRunning(p.ID) {
		c.JSON(409, gin.H{"error": "制作/重做进行中，先等本条完成再改稿"})
		return
	}
	// prep 链运行中同拒（2026-10-19 中-30）：改稿级联作废与后台链的工件写入互踩
	if s.Preps != nil && s.Preps.IsRunning(p.ID) {
		c.JSON(409, gin.H{"error": "准备阶段运行中，先等完成再改稿"})
		return
	}
	id := p.ID
	// 落盘+级联作废走统一语义（2026-10-19 中-34，与 agent saveManuscriptContent 同源）
	prepInvalidated, err := prep.ReplaceManuscript(p, content)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	_, sbErr := os.Stat(p.Artifact("storyboards/storyboard.json"))
	s.updateStatus(id, "manuscript")
	words := len([]rune(content))
	p.Manifest("manuscript.saved", fmt.Sprintf("%d 字", words))
	s.Hub.Emit(id, "msg", "", fmt.Sprintf("文稿已更新（%d 字）", words))
	c.JSON(200, gin.H{"ok": true, "word_count": words, "storyboard_stale": sbErr == nil, "prep_invalidated": prepInvalidated})
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
	_ = os.RemoveAll(p.Artifact(".hf-sample"))
	// 远端正本跟着作废（防清理过的老项目回源时旧样张复活）
	if s.Artifacts != nil && s.Artifacts.Available() {
		var keys []string
		for _, f := range []string{
			"style/style_samples.json", "style/skin.json", "style/design.md", "llm/style_samples.json",
			"style/sample-0.png", "style/sample-1.png", "style/sample-2.png",
		} {
			keys = append(keys, artifacts.ProjectKey(projID, f))
		}
		artifacts.DeleteKeys(s.Artifacts, keys...)
		artifacts.DeletePrefixBestEffort(s.Artifacts, artifacts.ProjectKey(projID, "style/clips"))
	}
	// 选定条目记入 project.json：读-改-写全程持 per-project 锁——agent 轮内的
	// SetProjectField 与此处并发时，双方各基于旧快照整文件回写、后写覆盖先写
	//（style_id 被抹掉那类丢更新），单笔原子写防撕裂不防丢更新。锁必须只在
	// 这一段持有：下面的 updateStatus 内部 SetProjectField 要拿同一把锁。
	mu := pipeline.ProjectMu(p)
	mu.Lock()
	if b, err := os.ReadFile(p.Artifact("project.json")); err == nil {
		var meta map[string]any
		if json.Unmarshal(b, &meta) == nil {
			meta["style_id"] = pack.ID
			_ = pipeline.WriteFileAtomic(p.Artifact("project.json"), pipeline.MustJSONBytes(meta), 0o644)
		}
	}
	mu.Unlock()
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
	p, ok := s.project(c)
	if !ok {
		return
	}
	// 制作中拒绝保存：管线正按旧分镜渲染/寻址，半道换 storyboard.json 会串段
	//（与 deleteProject 的 IsRunning 拒绝同一口径）。
	if s.Producer != nil && s.Producer.IsRunning(p.ID) {
		c.JSON(409, gin.H{"error": "制作/重做进行中，先等本条完成再改分镜"})
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
		if _, dup := byIdx[sg.Idx]; dup {
			// 存量重复 idx 会让 byIdx 折叠、多个段静默回填同一 ID 串段——拒收并明示
			c.JSON(500, gin.H{"error": fmt.Sprintf("存量分镜段序重复（idx=%d），请在对话里重新生成分镜", sg.Idx)})
			return
		}
		byIdx[sg.Idx] = sg.ID
	}
	inIdx := map[int]bool{}
	for i := range in.Segments {
		id, ok := byIdx[in.Segments[i].Idx]
		if !ok {
			c.JSON(400, gin.H{"error": fmt.Sprintf("第 %d 段不存在——段序不可增删改", in.Segments[i].Idx)})
			return
		}
		if inIdx[in.Segments[i].Idx] { // 请求里重复 idx 同样会把两段回填成同一 ID
			c.JSON(400, gin.H{"error": fmt.Sprintf("第 %d 段重复出现——每段一份", in.Segments[i].Idx)})
			return
		}
		inIdx[in.Segments[i].Idx] = true
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

// runPrep 启动三工位链（想法页签「自动完成」按钮）：异步后台跑，进度走 SSE
// （stage prep/*），完成/失败经 OnDone 落聊天框。对话侧重跑不经过这里——走
// agent 工具的同步执行路径，与 Preps.running 同一把互斥（2026-10-19 高-6 收口）。
// ── 项目材料库（主题页上传的参考资料，①文稿 read_source 消费）─────────────

// prepLocked 材料增删的互斥口径：准备链/对话轮在跑时材料是「本链路的输入」，
// 改它会让 read_source 的口径在链中途漂移——拒绝，等这轮结束。
func (s *Server) prepLocked(id string) bool {
	if s.Preps != nil && s.Preps.IsRunning(id) {
		return true
	}
	if mu, ok := s.turnMu.Load(id); ok {
		m := mu.(*sync.Mutex)
		if !m.TryLock() {
			return true
		}
		m.Unlock()
	}
	return false
}

func (s *Server) listMaterials(c *gin.Context) {
	p, ok := s.project(c)
	if !ok {
		return
	}
	c.JSON(200, prep.ListMaterials(p))
}

func (s *Server) saveMaterial(c *gin.Context) {
	p, ok := s.project(c)
	if !ok {
		return
	}
	if s.prepLocked(p.ID) {
		c.JSON(409, gin.H{"error": "准备阶段/对话进行中——材料已在本次链路读取，等这轮结束后再改"})
		return
	}
	var in struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(400, gin.H{"error": "参数不合法: " + err.Error()})
		return
	}
	list, err := prep.SaveMaterial(p, in.Name, in.Content)
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	p.Manifest("materials.saved", in.Name)
	c.JSON(200, gin.H{"ok": true, "materials": list})
}

func (s *Server) deleteMaterial(c *gin.Context) {
	p, ok := s.project(c)
	if !ok {
		return
	}
	if s.prepLocked(p.ID) {
		c.JSON(409, gin.H{"error": "准备阶段/对话进行中——材料已在本次链路读取，等这轮结束后再改"})
		return
	}
	list, err := prep.DeleteMaterial(p, c.Query("name"))
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true, "materials": list})
}

func (s *Server) runPrep(c *gin.Context) {
	id := c.Param("id")
	if s.Preps == nil {
		c.JSON(500, gin.H{"error": "准备运行器未接线"})
		return
	}
	if _, err := os.Stat(filepath.Join(s.DataDir, "projects", id)); err != nil {
		c.JSON(404, gin.H{"error": "项目不存在"})
		return
	}
	if s.Producer != nil && s.Producer.IsRunning(id) {
		c.JSON(409, gin.H{"error": "制作运行中，先等本条完成再跑准备工位"})
		return
	}
	// 对话轮互斥探测（2026-10-19 高-6）：对话里的 rerun_prep/draft_* 是同步重跑
	// （分钟级），不登记 Preps.running——不探这一下，这里能开出第二条链与对话侧
	// 互踩工件。锁只用于探测「此刻有无对话轮在跑」，TryLock 成功立即放（链本身
	// 由 Preps.running 互斥，不长期占着对话轮的锁）。
	if mu, ok := s.turnMu.Load(id); ok {
		m := mu.(*sync.Mutex)
		if !m.TryLock() {
			c.JSON(409, gin.H{"error": "该项目有对话正在进行，等回复完再启动准备阶段"})
			return
		}
		m.Unlock()
	}
	var in struct {
		Idea  string `json:"idea"`
		Force bool   `json:"force"`
	}
	_ = c.ShouldBindJSON(&in)
	// 音乐驱动 PV：主题必填（地图推不出情绪色彩层，主题+终审门人工纠正才是
	// 三层兜底的地基——pv-music-driven-design 模式A契约）。
	music := projectMode(pipeline.NewProject(s.DataDir, id)) == "music"
	if music && strings.TrimSpace(in.Idea) == "" {
		c.JSON(400, gin.H{"error": "音乐驱动项目要先给主题（一句话，做什么氛围的曲子）"})
		return
	}
	started, err := s.Preps.Start(id, in.Idea, in.Force)
	if err != nil {
		switch {
		case errors.Is(err, prep.ErrPrepRunning):
			c.JSON(409, gin.H{"error": "准备阶段已在运行中"})
		case errors.Is(err, prep.ErrPrepLegacy):
			c.JSON(400, gin.H{"error": err.Error()})
		case errors.Is(err, prep.ErrPrepNoLLM):
			c.JSON(400, gin.H{"error": err.Error()})
		default:
			c.JSON(409, gin.H{"error": err.Error()})
		}
		return
	}
	if !started {
		c.JSON(200, gin.H{"ok": true, "started": false})
		return
	}
	note := "准备阶段已启动：文稿 → 风格 → 分镜，进度在「想法」页签与事件流。"
	if music {
		note = "PV 准备工位已启动：做曲 → 音乐地图，完成后在「想法」页签试听。"
	} else if in.Idea != "" {
		note = "准备阶段已带着你的想法启动：文稿 → 风格 → 分镜，完成后去「终审」页签确认。"
	}
	s.Hub.Emit(id, "msg", "", note)
	c.JSON(202, gin.H{"ok": true, "started": true})
}

// cancelPrep 取消在跑的准备链（工位回合边界退出；终态经 OnDone 落聊天框）。
func (s *Server) cancelPrep(c *gin.Context) {
	id := c.Param("id")
	if s.Preps == nil || !s.Preps.Cancel(id) {
		c.JSON(409, gin.H{"error": "没有正在运行准备阶段"})
		return
	}
	c.JSON(202, gin.H{"ok": true})
}

// ── 准备阶段：打包终审（全流程唯一人工门，prep-agents-design §2）──────

// prepView 三工件打包视图（终审页签数据源）：文字稿（句编号+事实清单）、
// 风格包（描述+剪辑语法+样张）、分镜稿（段落+段表）。非新路径项目返回
// exists=false，前端不显示终审页签。
func (s *Server) prepView(c *gin.Context) {
	p, ok := s.project(c)
	if !ok {
		return
	}
	// 想法页签的状态三件（exists=false 的早返回也要带：链在跑时页签全靠它）
	state := gin.H{
		"running": s.Preps != nil && s.Preps.IsRunning(p.ID),
		"legacy":  prep.LegacyProject(p),
		"idea":    strings.TrimSpace(readFileLoose(p.Artifact("input/prep_idea.txt"))),
		"mode":    projectMode(p),
	}
	// 音乐驱动 PV：文稿三工件不适用，回三件套视图（想法页签的做曲面板）
	if state["mode"] == "music" {
		out := gin.H{"exists": false, "confirmed": false, "music": prep.MusicPrepView(p)}
		for k, v := range state {
			out[k] = v
		}
		c.JSON(200, out)
		return
	}
	script, err := prep.LoadScript(p)
	if err != nil {
		out := gin.H{"exists": false, "confirmed": false}
		for k, v := range state {
			out[k] = v
		}
		c.JSON(200, out)
		return
	}
	out := gin.H{"exists": true, "confirmed": false}
	for k, v := range state {
		out[k] = v
	}
	// stylepack 只读一次盘（中-L1：旧版同请求 LoadStylePack×2，两次读+解析 JSON）
	var styleName string
	if sp, err := prep.LoadStylePack(p); err == nil {
		// 确认态与制作硬门（segpipe 读 stylepack.Confirmed）同源。曾经以
		// style_samples.json 为准——风格页老确认只写样张不写包，徽章亮了门不开，
		// 「已确认可开工」却开不了工的死局（2026-10-17 彩虹项目实测）
		out["confirmed"] = sp.Confirmed
		direction := ""
		if sp.DesignSystem != nil {
			direction = sp.DesignSystem.Direction
		}
		out["stylepack"] = gin.H{
			"style_id": sp.StyleID, "description": sp.Description,
			"grammar": sp.Grammar, "sample_ids": sp.SampleIDs,
			"direction": direction, "confirmed": sp.Confirmed,
		}
		out["design"] = sp.DesignSystem
		if st, ok := s.Lib.Get(sp.StyleID); ok {
			styleName = st.Name
		}
	}
	if dr, err := prep.LoadDraft(p); err == nil {
		out["draft"] = dr
	}
	out["style_name"] = styleName
	out["script"] = script
	out["storyboard_ready"] = func() bool {
		_, err := p.LoadStoryboard()
		return err == nil
	}()
	c.JSON(200, out)
}

// readFileLoose 宽松读：读失败返回空串（想法未落盘是常态，不是错误）。
func readFileLoose(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

// confirmPrep 打包终审确认（全流程唯一人工门）：文字稿+风格包+分镜稿一次确认。
// 确认 = 设计系统锁定（design.md/skin.json 落盘）+ 风格包工件置 confirmed——
// 制作阶段的既有硬门（style_samples.confirmed）从这里拿到 true。
func (s *Server) confirmPrep(c *gin.Context) {
	id := c.Param("id")
	// 对话轮互斥与 confirmStyle 同源：prep 工位可能还在对话轮里重跑，
	// 半道确认会被最后一笔打回未确认（TryLock 语义见 confirmStyle 注释）
	mu, _ := s.turnMu.LoadOrStore(id, &sync.Mutex{})
	if !mu.(*sync.Mutex).TryLock() {
		c.JSON(409, gin.H{"error": "AI 还在准备工位上工作，等它说完再确认"})
		return
	}
	defer mu.(*sync.Mutex).Unlock()
	p := pipeline.NewProject(s.DataDir, id)
	// 音乐驱动 PV 的打包门（brief+试听+地图+镜头表）随分镜工位（M2）上线，
	// 现在没有可确认的包——明确报错比假装确认好。
	if projectMode(p) == "music" {
		c.JSON(400, gin.H{"error": "PV 打包终审门随分镜工位（M2）上线——现在可在「想法」页签试听做曲结果"})
		return
	}
	if _, err := prep.LoadScript(p); err != nil {
		c.JSON(400, gin.H{"error": "这个项目没有准备工件（非新路径），在「风格」页签走样张确认"})
		return
	}
	sp, err := prep.LoadStylePack(p)
	if err != nil {
		c.JSON(400, gin.H{"error": "风格包未出，先让 Agent 跑完准备工位"})
		return
	}
	dr, err := prep.LoadDraft(p)
	if err != nil {
		c.JSON(400, gin.H{"error": "分镜稿未出，先让 Agent 跑完准备工位"})
		return
	}
	// 换风格过期侦测：简报与包不一致/样张被清 = ②过期，打包不许带病放行
	//（否则按旧风格包开工，用户刚选的新风格被静默丢弃——2026-10-17 彩虹项目实测）
	if stale, why := prep.StyleStale(p); stale {
		c.JSON(409, gin.H{"error": "风格已换选但样张没重出：" + why + "。先回「风格」页签重新出样张，再回来打包"})
		return
	}
	var script *contract.Script
	script, _ = prep.LoadScript(p)
	// 三工件校验兜底（submit 已各自过闸，这里防手改盘）
	if verr := contract.ValidateStylePack(sp); verr != nil {
		c.JSON(400, gin.H{"error": "风格包不合格，重新跑风格工位：" + verr.Error()})
		return
	}
	if verr := contract.ValidateStoryboardDraft(dr, script, sp); verr != nil {
		c.JSON(400, gin.H{"error": "分镜稿不合格，重新跑分镜工位：" + verr.Error()})
		return
	}
	// 设计系统锁定（design.md/skin.json/project.json 接线），confirmed 落两处：
	// stylepack.json（工件 B）+ style_samples.json（制作硬门消费方）
	if sp.DesignSystem != nil {
		if verr := contract.ValidateStyleSamplesDraft(sp.DesignSystem); verr != nil {
			c.JSON(400, gin.H{"error": "设计系统不合格：" + verr.Error()})
			return
		}
		sp.DesignSystem.Confirmed = true
		if err := pipeline.LockDesign(p, sp.DesignSystem); err != nil {
			c.JSON(500, gin.H{"error": "设计锁定失败：" + err.Error()})
			return
		}
		if err := pipeline.WriteFileAtomic(p.Artifact("style/style_samples.json"), pipeline.MustJSONBytes(sp.DesignSystem), 0o644); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
	}
	sp.Confirmed = true
	if err := pipeline.WriteFileAtomic(p.Artifact("style/stylepack.json"), pipeline.MustJSONBytes(sp), 0o644); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	direction := ""
	if sp.DesignSystem != nil {
		direction = sp.DesignSystem.Direction
	}
	p.Manifest("prep.confirmed", fmt.Sprintf("打包终审通过（%s，%d 段）", direction, len(dr.Segments)))
	s.updateStatus(id, "style")
	s.Hub.Emit(id, "prep_confirmed", "", direction)
	c.JSON(200, gin.H{"ok": true, "direction": direction})
}

// ── 聊天 ─────────────────────────────────────────────────────

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
	// 落库失败必须 5xx：202 却查无此消息＝用户看着输入框清空白等一场空
	// （2026-10-30 GUI 实测：存储抖动窗口内多次发送静默蒸发，前端还按成功清了草稿）
	if err := s.Store.SaveMsg(m); err != nil {
		log.Printf("[api] SaveMsg(%s) 失败: %v", id, err)
		c.JSON(500, gin.H{"error": "消息保存失败（存储暂不可用），请稍后重发——内容已保留在输入框"})
		return
	}
	s.Hub.Emit(id, "msg", "", in.Content)
	// 同项目对话轮互斥：连发两条消息会并发跑两个 Agent 循环，互相增删共享产物
	// （如 update_storyboard 删 spec 时另一轮正读）。排队等上一轮结束；但排队
	// 本身要有上限——每条 POST 无限起 goroutine 等锁，连点会堆出不可取消的轮。
	if !s.acquireChatSlot() {
		c.JSON(429, gin.H{"error": "已有排队的消息，稍后再试"})
		return
	}
	mu, _ := s.turnMu.LoadOrStore(id, &sync.Mutex{})
	go func() {
		mu := mu.(*sync.Mutex)
		mu.Lock()
		defer mu.Unlock()
		defer s.releaseChatSlot() // 轮结束才释放槽位（排队+执行全程占槽）
		// 对话轮级时限：卡死的 LLM 调用（上游挂住时 10min×3 重试）会把 turnMu 下的
		// 后续对话轮全部拖成沉默；超时给用户明确交代。拿锁后才起表：注册表里永远
		// 是"正在执行"那轮的取消函数，取消按钮打不到还在排队的幽灵轮。
		turnCtx, cancelTurn := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancelTurn()
		// 归属上下文进轮：list_projects 按它过滤、create_project 按它落 user_id
		//（开放模式 uid/role 为空 = 不过滤/系统项目，与列表接口同语义）
		uid, role := s.currentUser(c)
		turnCtx = agent.WithUser(turnCtx, uid, role)
		s.turnCancels.Store(id, cancelTurn)
		defer s.turnCancels.Delete(id)
		s.Agent.Run(turnCtx, id, in.Content, in.Refs)
	}()
	c.JSON(202, gin.H{"ok": true})
}

// cancelChat 中断当前项目正在执行的对话轮（取消其 turnCtx，Agent 侧收尾播报
// 「已中断」）。没有在跑的轮时 ok=false——前端把它当"顺手清锁"处理。
func (s *Server) cancelChat(c *gin.Context) {
	id := c.Param("id")
	ok := false
	if v, loaded := s.turnCancels.LoadAndDelete(id); loaded {
		v.(context.CancelFunc)()
		ok = true
	}
	c.JSON(200, gin.H{"ok": ok})
}

// chatQueueMax 对话轮排队上限（不含正在执行的一轮）：够吸收连点，又不至于
// 堆出一串用户等不到、也无法取消的幽灵轮。
const chatQueueMax = 2

// acquireChatSlot 非阻塞占一个对话轮排队槽位；false = 队列已满，调用方应拒新。
func (s *Server) acquireChatSlot() bool {
	s.chatSlotsOnce.Do(func() { s.chatSlots = make(chan struct{}, chatQueueMax) })
	select {
	case s.chatSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *Server) releaseChatSlot() { <-s.chatSlots }

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
		// 存储故障吞成空列表，用户看到的是「聊天记录没了」——至少留痕可排查
		log.Printf("[api] ListMsgs(%s) 失败: %v", id, err)
		msgs = nil
	}
	if msgs == nil {
		// 空 must 回 [] 不能回 null：内存存储（未配 PG）重启后历史清零是常态，
		// 前端 loadMsgs 拿到 null 直接 .filter 崩掉对话栏（与 activity 接口同一纪律）
		msgs = []store.Msg{}
	}
	c.JSON(200, msgs)
}

// ── 制作 & 视频 ─────────────────────────────────────────────

func (s *Server) startProduce(c *gin.Context) {
	// 音乐驱动项目走 PV 管线（做曲→地图→分镜，M1 开发中）：前端不露文稿制作
	// 入口，这里拦直调 API／带稿误建后手滑——明确报错比让文稿管线吞一个空
	// 主题项目跑出怪片好。
	if p, ok := s.project(c); ok && projectMode(p) == "music" {
		c.JSON(400, gin.H{"error": "音乐驱动项目走 PV 管线（做曲→音乐地图→分镜，开发中），文稿制作入口不适用"})
		return
	}
	id := c.Param("id")
	st := s.Agent.State(id)
	if !st.HasManuscript || !st.HasStoryboard || !st.StyleConfirmed {
		c.JSON(400, gin.H{"error": "三前置未齐（文稿/分镜/风格确认），硬门不放行"})
		return
	}
	// prep 链运行中拒绝（2026-10-19 中-30）：重跑链开头会 InvalidateFrom 作废
	// 三工件，期间开制作可能读到半作废的 stylepack/storyboard
	if s.Preps != nil && s.Preps.IsRunning(id) {
		c.JSON(409, gin.H{"error": "准备阶段重跑中，先等完成再开制作"})
		return
	}
	// 成片最新挡整片重做（2026-10-23 中-1）：与主管工具门禁同一判别
	// （agent.VideoFresh），API 直调不再绕过——改某段画面走段级重做，
	// 改了文稿/分镜/风格才整片重做。
	if agent.VideoFresh(st) {
		c.JSON(400, gin.H{"error": "成片已就绪且为最新，无需重新制作——改某段画面在舞台上点选后说要求（段级重做）；改了文稿/分镜/风格再整片重做"})
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

// ── BGM 曲库与项目配乐（时间轴音乐轴/曲库下拉/试听的数据面）────────────

// bgmLibrary 曲库清单（曲名/情绪/bpm/快慢/能量/时长）：下拉与试听数据源。
func (s *Server) bgmLibrary(c *gin.Context) {
	c.JSON(200, pipeline.ListBGMChoices())
}

// bgmAudio 曲库音频取件：曲名过曲库校验（防目录穿越），试听播放器用。
func (s *Server) bgmAudio(c *gin.Context) {
	path, err := pipeline.BGMFilePath(c.Param("name"))
	if err != nil {
		c.JSON(404, gin.H{"error": err.Error()})
		return
	}
	c.File(path)
}

// musicAudio 音乐驱动 PV 的整曲试听（音乐工位产物 music/track.mp3）。
func (s *Server) musicAudio(c *gin.Context) {
	p, ok := s.project(c)
	if !ok {
		return
	}
	path := p.Artifact("music/track.mp3")
	if _, err := os.Stat(path); err != nil {
		c.JSON(404, gin.H{"error": "曲子还没做好（先在「想法」页签跑准备工位）"})
		return
	}
	c.File(path)
}

// setProjectBGM 项目配乐设置/更换/关闭（与对话工具 set_bgm 同语义同混音行为）：
// 成片母版在 → StitchNow 立即重混（几秒到分钟级，画面不动）；不在 → 下次制作生效。
func (s *Server) setProjectBGM(c *gin.Context) {
	p, ok := s.project(c)
	if !ok {
		return
	}
	if s.Producer != nil && s.Producer.IsRunning(p.ID) {
		c.JSON(409, gin.H{"error": "制作运行中，先等本条完成"})
		return
	}
	var in struct {
		Track string `json:"track"`
	}
	_ = c.ShouldBindJSON(&in)
	if err := pipeline.SetBGM(p, in.Track); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	track := strings.ToLower(strings.TrimSpace(in.Track))
	if track == "off" {
		track = ""
	}
	remixed := false
	var mixErr string
	if track != "" {
		if _, err := os.Stat(p.Artifact("renders/main_voice.mp4")); err == nil {
			if err := pipeline.StitchNow(p); err != nil {
				mixErr = err.Error()
			} else {
				remixed = true
			}
		}
	}
	msg := "已设置，下次制作生效"
	if track == "" {
		msg = "配乐已关闭"
	}
	if remixed {
		msg = "已重混进成片（人声闪避自动压低音乐，画面未动）"
	}
	out := gin.H{"ok": mixErr == "", "bgm": track, "remixed": remixed, "message": msg}
	if mixErr != "" {
		out["error"] = "设置成功但混音失败：" + mixErr
	}
	c.JSON(200, out)
}

// audioMeta 时间轴数据源：每段配音时长 + 字级时间戳（TTS 对齐产物），供前端画字幕/音频轨。
// xfade 成片附带 renders/timeline.json（实际段边界，比裸配音累计每边界早 xfadeDur），
// 合并为 timeline 字段——前端 segTable 有它就用真实边界。
func (s *Server) audioMeta(c *gin.Context) {
	p, ok := s.project(c) // 不存在的项目统一 404，不与「未出配音」混谈
	if !ok {
		return
	}
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
	id := c.Param("id")
	path := pipeline.NewProject(s.DataDir, id).Artifact("renders/main.mp4")
	if _, err := os.Stat(path); err != nil && !(s.Artifacts != nil && s.Artifacts.Stat(artifacts.ProjectKey(id, "renders/main.mp4"))) {
		c.JSON(404, gin.H{"error": "成片未就绪"})
		return
	}
	s.serveArtifact(c, artifacts.ProjectKey(id, "renders/main.mp4"), path, "main.mp4", "no-store", "成片未就绪")
}

// coverPNG 封面取件（renders/cover.png，出片尾巴自动产出/项目页按需重做）：
// 本地 miss 回退远端正本，与 video 同源口径。
func (s *Server) coverPNG(c *gin.Context) {
	id := c.Param("id")
	path := pipeline.NewProject(s.DataDir, id).Artifact("renders/cover.png")
	if _, err := os.Stat(path); err != nil && !(s.Artifacts != nil && s.Artifacts.Stat(artifacts.ProjectKey(id, "renders/cover.png"))) {
		c.JSON(404, gin.H{"error": "封面未生成"})
		return
	}
	s.serveArtifact(c, artifacts.ProjectKey(id, "renders/cover.png"), path, "cover.png", "no-store", "封面未生成")
}

// regenCover 按需重做封面（produce.MakeCover，同步——抽帧+无头截图秒级到十几秒）：
// body {t?, title?} 缺省沿用既有封面账面，再缺省兜底（帧位=总时长30%段中点，
// 标题=项目名）。制作中拒绝（收尾自动出封面，双写手互斥）；成片不在本机拒绝
// （抽不了帧，云端-only 项目先在列表页恢复到本机）。
func (s *Server) regenCover(c *gin.Context) {
	p, ok := s.project(c)
	if !ok {
		return
	}
	if s.Producer == nil {
		c.JSON(500, gin.H{"error": "制作器未接线（demo 容器无制作能力）"})
		return
	}
	if s.Producer.IsRunning(p.ID) {
		c.JSON(409, gin.H{"error": "制作进行中——收工时会自动出封面，先等这轮结束"})
		return
	}
	if _, err := os.Stat(p.Artifact("renders/main.mp4")); err != nil {
		c.JSON(409, gin.H{"error": "成片不在本机，抽不了封面帧"})
		return
	}
	var in struct {
		T     *float64 `json:"t"`
		Title *string  `json:"title"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(400, gin.H{"error": "参数不合法: " + err.Error()})
		return
	}
	existing := produce.LoadCover(p)
	t := 0.0
	switch {
	case in.T != nil:
		t = *in.T
	case existing != nil:
		t = existing.T
	default:
		picked, ok := produce.PickCoverT(p)
		if !ok {
			c.JSON(409, gin.H{"error": "没有 timeline（成片未拼接完成），定不了帧位"})
			return
		}
		t = picked
	}
	title := ""
	switch {
	case in.Title != nil:
		title = strings.TrimSpace(*in.Title)
	case existing != nil:
		title = existing.Title
	}
	if title == "" {
		title = produce.DefaultCoverTitle(p)
	}
	if err := s.Producer.MakeCover(c.Request.Context(), p, t, title); err != nil {
		c.JSON(500, gin.H{"error": "封面生成失败: " + err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true, "t": t, "title": title})
}

// styleClip 动态样张 mp4（prep② 直渲的 sample-<N> 与 legacy 迷你真段 clip-<N>）。
// 文件名白名单——style/clips/ 里还有场景片段 html 和装配清单，那些不对外。
var styleClipRe = regexp.MustCompile(`^(?:clip|sample)-\d+\.mp4$`)

func (s *Server) styleClip(c *gin.Context) {
	name := c.Param("file")
	if !styleClipRe.MatchString(name) {
		c.JSON(404, gin.H{"error": "文件不存在"})
		return
	}
	id := c.Param("id")
	path := pipeline.NewProject(s.DataDir, id).Artifact("style/clips/" + name)
	if _, err := os.Stat(path); err != nil && !(s.Artifacts != nil && s.Artifacts.Stat(artifacts.ProjectKey(id, "style/clips/"+name))) {
		c.JSON(404, gin.H{"error": "小样未就绪"})
		return
	}
	s.serveArtifact(c, artifacts.ProjectKey(id, "style/clips/"+name), path, name, "no-store", "小样未就绪")
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

// startDistill 开蒸馏单（懒触发的人工拍板口）：起容器-画师-判卷-入库全链异步跑，
// 并发=1（蒸馏房占用中回 409）。判卷与进度走 GET 同资源位轮询。
func (s *Server) startDistill(c *gin.Context) {
	d := prep.Deps{RootDir: s.RootDir, Lib: s.Lib}
	if err := prep.StartDistill(d, c.Param("id")); err != nil {
		if errors.Is(err, stylelib.ErrNoEntry) {
			c.JSON(404, gin.H{"error": err.Error()})
			return
		}
		c.JSON(409, gin.H{"error": err.Error()})
		return
	}
	c.JSON(202, gin.H{"started": true})
}

// distillStatus 蒸馏状态：全局在跑单 + 本条目最近一单。
func (s *Server) distillStatus(c *gin.Context) {
	snap := prep.DistillSnapshot()
	last := map[string]any(nil)
	if m, ok := snap["last"].(map[string]map[string]any); ok {
		last = m[c.Param("id")]
	}
	c.JSON(200, gin.H{"running": snap["running"], "last": last})
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
			// 具名事件而非注释帧：EventSource 对 ": ping" 不可见，前端拿不到
			// 「最近心跳」就做不了活性看门狗（半开挂死无法自愈，2026-10-30 GUI 实测）
			io.WriteString(c.Writer, "event: ping\ndata: {}\n\n")
			fl.Flush()
		}
	}
}

// updateStatus 状态落两处（project.json + Store 行）。实现收敛在 agent 包的
// WriteProjectStatus：与 agent 轮内终态回写是同一件事，两份手抄迟早漂移。
func (s *Server) updateStatus(id, status string) {
	agent.WriteProjectStatus(s.DataDir, s.Store, id, status)
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

// serveArtifact 产物媒体回源：本地热路径直读（零网络开销）；本地被清理器删过
// 则从对象存储流式回源（minio.Object 自带 Seek，Range/304 照常工作）。两者皆无
// → missing 文案的 404。前端零改动（URL 永不入库的对偶：出口永远走 /api 路由）。
func (s *Server) serveArtifact(c *gin.Context, key, localPath, name, cache, missing string) {
	if f, err := os.Open(localPath); err == nil {
		st, serr := f.Stat()
		if serr == nil && !st.IsDir() {
			defer f.Close()
			c.Header("Cache-Control", cache)
			http.ServeContent(c.Writer, c.Request, name, st.ModTime(), f)
			return
		}
		f.Close()
	}
	if s.Artifacts != nil && s.Artifacts.Available() {
		if rc, _, modTime, err := s.Artifacts.Open(key); err == nil {
			defer rc.Close()
			c.Header("Cache-Control", cache)
			http.ServeContent(c.Writer, c.Request, name, modTime, rc)
			return
		}
	}
	c.JSON(404, gin.H{"error": missing})
}

// putArtifact 检查点上传（best-effort；nil/本地后端为无操作）。
func (s *Server) putArtifact(key, srcPath string) {
	if s.Artifacts == nil || !s.Artifacts.Available() {
		return
	}
	if err := s.Artifacts.Put(key, srcPath); err != nil {
		log.Printf("[artifacts] 上传 %s 失败: %v", key, err)
	}
}

// ── 参考视频解析（风格库「视频解析」）────────────────────────

// refVideoExt 参考视频扩展名白名单（常量表，包级一份：不值得逐请求重建 map）。
var refVideoExt = map[string]bool{".mp4": true, ".mov": true, ".webm": true, ".m4v": true}

// uploadReference 上传视频 → 落盘登记 → 立即异步解析。
func (s *Server) uploadReference(c *gin.Context) {
	fh, err := c.FormFile("video")
	if err != nil {
		c.JSON(400, gin.H{"error": "缺少 video 文件字段"})
		return
	}
	name := filepath.Base(fh.Filename)
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

// parseProjectStyle 项目解析（两路解析入口二）：读项目定稿设计系统+样张帧建暂存
// 条目，异步文本打标（RolePlan 纯文本脑，不抽帧不碰多模态）——与上传视频解析同一
// 暂存区/修订/确认入库流程，前端轮询 GET /api/references 看进度。
func (s *Server) parseProjectStyle(c *gin.Context) {
	p, ok := s.project(c)
	if !ok {
		return
	}
	if _, err := os.Stat(p.Artifact("style/style_samples.json")); err != nil {
		c.JSON(400, gin.H{"error": "项目还没有定稿的设计系统（先完成风格样张并确认）"})
		return
	}
	// 幂等：本项目已有解析条目 → 复用（重复点击不建第二条；重解析走卡片上的「重新解析」）
	refs, err := s.Refs.List()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	for _, r := range refs {
		if r.Source == styleref.SourceProject && r.ProjectID == p.ID {
			c.JSON(200, gin.H{"ok": true, "ref_id": r.ID, "existing": true})
			return
		}
	}
	ref, err := s.Refs.SaveProject(projectDisplayName(p), p.ID, projectSampleFrames(p))
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if _, err := s.Refs.Analyze(ref.ID); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(202, gin.H{"ok": true, "ref_id": ref.ID})
}

// projectDisplayName 项目陈列名：project.json 快照的 name，缺失回退项目 ID。
func projectDisplayName(p *pipeline.Project) string {
	name := p.ID
	if b, err := os.ReadFile(p.Artifact("project.json")); err == nil {
		var meta struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(b, &meta) == nil && strings.TrimSpace(meta.Name) != "" {
			name = strings.TrimSpace(meta.Name)
		}
	}
	return name
}

// projectSampleFrames 项目里现成的风格证据帧（设计样张 poster，所见即所得）：
// 制作过程已渲染好，项目解析路零 ffmpeg 直接拷。没有（老项目）就空——草稿帧允许空。
func projectSampleFrames(p *pipeline.Project) []string {
	matches, _ := filepath.Glob(p.Artifact("style/sample-*.png"))
	return matches
}

// confirmReference 解析结果入库：styles.json 写「标签+特色」条目（一片一条目）。
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

	// 条目出处按解析路分：上传视频 = video:<refID>；项目解析 = 真实项目 ID（可追溯）
	origin := "video:" + ref.ID
	if ref.Source == styleref.SourceProject {
		origin = ref.ProjectID
	}
	st, err := s.ingestRefStyle(ref.ID, ref.Name, ref.Style, filepath.Join(s.DataDir, "references", ref.ID), origin)
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
// 封面 + IngestVideoRef（新载荷：标签+特色+题材一句话+分面三轴）+ 原片/抽帧快照。
// 条目名 = LLM 起的独特陈列名（2026-10-24 翻转：此前拿视频标题当条目名，营销
// 标题「震惊瘫坐!!」入库既难辨识也不好友好检索；视频标题只在 LLM 缺名时兜底）；
// 同名撞车由库侧带题材词消歧（uniqueName）。
// seedID 是 IngestVideoRef 的 refID 种子（重复入库同 seed upsert 幂等）；
// origin 是条目出处（OriginProject），按解析路由 confirmReference 传入。
func (s *Server) ingestRefStyle(seedID, origName string, d *styleref.StyleDraft, dir, origin string) (*stylelib.Style, error) {
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
	name := strings.TrimSpace(d.Name)
	if name == "" {
		name = videoTitle(origName, d.Name) // LLM 缺名：视频标题兜底
	}
	st, err := s.Lib.IngestVideoRef(seedID, stylelib.VideoStyleIn{
		Name:        name,
		Subject:     d.Subject,
		Facets:      d.Facets,
		Tags:        d.Tags,
		TagEvidence: d.TagEvidence,
		Signatures:  d.Signatures,
		Origin:      origin,
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
	if !ok && s.Artifacts != nil && s.Artifacts.Available() {
		// 本地被清理：临时拉回剪辑（视觉模型侧是整文件拉取，读穿缓存正合适）
		if lp, err := s.Artifacts.Localize(artifacts.ReferenceKey(id, name)); err == nil {
			path, ok = lp, true
		}
	}
	if !ok {
		c.JSON(404, gin.H{"error": "文件不存在"})
		return
	}
	s.serveArtifact(c, artifacts.ReferenceKey(id, name), path, name, "no-store", "文件不存在")
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
	leadNumRe  = regexp.MustCompile(`^\d+\s*[_-]\s*`)
	bvSuffixRe = regexp.MustCompile(`[_\s]*BV[0-9A-Za-z]{6,14}\s*$`)
	partNumRe  = regexp.MustCompile(`(?i)^\s*P0?(\d+)`)
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

// referenceFrames 参考视频抽帧截图清单（详情抽屉展示原片长相用；本地被清理后
// 从对象存储列清单——媒体本体仍走 referenceMedia 回源）。
func (s *Server) referenceFrames(c *gin.Context) {
	id := c.Param("id")
	if !isSlug(id) {
		c.JSON(400, gin.H{"error": "非法路径"})
		return
	}
	frames, hasVideo := scanMediaDir(filepath.Join(s.DataDir, "references", id))
	if frames == nil && !hasVideo { // 目录读不了 = 引用不存在（本地与远端皆无）
		if _, err := os.Stat(filepath.Join(s.DataDir, "references", id)); err != nil &&
			!(s.Artifacts != nil && s.Artifacts.Available() && s.hasRemoteRefMedia(id)) {
			c.JSON(404, gin.H{"error": "引用不存在"})
			return
		}
	}
	if frames == nil && s.Artifacts != nil && s.Artifacts.Available() {
		frames, hasVideo = s.remoteRefMedia(id)
	}
	if frames == nil {
		frames = []string{}
	}
	c.JSON(200, gin.H{"frames": frames, "has_video": hasVideo})
}

// remoteRefMedia 对象存储侧的参考媒体清单（与 scanMediaDir 同一白名单口径）。
func (s *Server) remoteRefMedia(id string) (frames []string, hasVideo bool) {
	keys, err := s.Artifacts.List(artifacts.ReferenceKey(id, ""))
	if err != nil {
		return nil, false
	}
	prefix := artifacts.ReferenceKey(id, "")
	for _, k := range keys {
		name := strings.TrimPrefix(k, prefix)
		switch {
		case isPngName(name) && strings.HasPrefix(name, "frame-"):
			frames = append(frames, name)
		case name == "video.mp4":
			hasVideo = true
		}
	}
	return frames, hasVideo
}

func (s *Server) hasRemoteRefMedia(id string) bool {
	f, v := s.remoteRefMedia(id)
	return len(f) > 0 || v
}

// refMediaName 参考媒体文件名白名单：抽帧 frame-*.png / 原视频 video.mp4 /
// 封面 cover.png（入库时代表帧拷贝，封面样张的 <img> 引用它）。
// 与风格媒体同款名单，规则收在 stylelib 一处。
func refMediaName(name string) bool { return stylelib.ValidMediaName(name) }

// libraryFrames 风格媒体抽帧清单：媒体后端（MinIO 或本地快照目录）优先，
// 空（老条目/快照失败）回退 origin 指向的参考视频目录。定位逻辑在 stylelib。
func (s *Server) libraryFrames(c *gin.Context) {
	id := c.Param("id")
	if !isSlug(id) {
		c.JSON(400, gin.H{"error": "非法路径"})
		return
	}
	frames, hasVideo := s.Lib.MediaNames(id)
	if frames == nil {
		frames = []string{}
	}
	c.JSON(200, gin.H{"frames": frames, "has_video": hasVideo})
}

// libraryMedia 风格媒体本体（文件名白名单在 Library 侧，无遍历面；
// MinIO 后端为对象流回源，本地后端为文件回源，api 无感）。
func (s *Server) libraryMedia(c *gin.Context) {
	rc, _, modTime, ok := s.Lib.OpenMedia(c.Param("id"), c.Param("file"))
	if !ok {
		c.JSON(404, gin.H{"error": "文件不存在"})
		return
	}
	defer rc.Close()
	c.Header("Cache-Control", "max-age=3600")
	http.ServeContent(c.Writer, c.Request, c.Param("file"), modTime, rc)
}

// referenceMedia 参考引用媒体文件（抽帧 png / 原视频 mp4，白名单文件名防目录穿越；
// 本地被清理后从对象存储回源）。
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
	local := filepath.Join(s.DataDir, "references", id, name)
	s.serveArtifact(c, artifacts.ReferenceKey(id, name), local, name, "max-age=3600", "文件不存在")
}
