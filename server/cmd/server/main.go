// server — 帧述 M1 HTTP 服务（API + SSE + Agent + 制作编排）。
//
//	cd server && go run ./cmd/server   （默认 127.0.0.1:8080 仅本机可调；DATA_DIR 相对 .env 解析，启动目录无关）
package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"webpage-video-agent/server/internal/agent"
	"webpage-video-agent/server/internal/api"
	"webpage-video-agent/server/internal/artifacts"
	"webpage-video-agent/server/internal/auth"
	"webpage-video-agent/server/internal/config"
	"webpage-video-agent/server/internal/events"
	"webpage-video-agent/server/internal/pipeline"
	"webpage-video-agent/server/internal/prep"
	"webpage-video-agent/server/internal/produce"
	"webpage-video-agent/server/internal/store"
	"webpage-video-agent/server/internal/stylelib"
	"webpage-video-agent/server/internal/styleref"
)

func main() {
	// 存储维护子命令（离线跑，不占 8080）：-storage-sync 存量回填 / -storage-prune 立即清理
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-storage-sync":
			runStorageSync(os.Args[2:])
			return
		case "-storage-prune":
			runStoragePrune(os.Args[2:])
			return
		}
	}
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	root, _ := filepath.Abs(filepath.Join(cfg.DataDir, ".."))
	if _, err := os.Stat(filepath.Join(root, "ai")); err != nil {
		log.Fatalf("仓库根推断失败（%s 下无 ai/），检查 DATA_DIR", root)
	}

	pipeline.EnsureFFmpeg()
	pipeline.SetRootDir(root)
	pipeline.SetDataDir(cfg.DataDir)

	st, err := store.Open(cfg.PGDSN)
	if err != nil {
		log.Fatal(err)
	}
	// 风格库媒体后端：MinIO（配了且 PG 已接；连不上 fail-fast，媒体正本在对象存储）
	// 或本地目录（现状行为，demo/无 PG 环境用）。docs/style-library-cloud-storage.md。
	var media stylelib.MediaStore
	if cfg.MinIO.Endpoint != "" {
		if st.DB() == nil {
			log.Printf("⚠  MINIO_* 已配但 PG 未接（风格库账本在内存/文件）——MinIO 无从挂靠，忽略之，媒体走本地目录")
		} else {
			m, err := stylelib.NewMinioMedia(cfg.MinIO.Endpoint, cfg.MinIO.AccessKey, cfg.MinIO.SecretKey,
				cfg.MinIO.Bucket, cfg.MinIO.Secure, cfg.DataDir)
			if err != nil {
				log.Fatal(err)
			}
			media = m
		}
	}
	hub := events.NewHub()
	lib := stylelib.NewWith(cfg.DataDir, st.DB(), media)
	lib.Migrate() // 化石导入（PG 账本才生效）：styles.json/vocab.json → 0005 表 + 媒体上 MinIO

	// 项目产物/参考库正本后端：MinIO（配了就连，连不上拒启——正本在对象存储，
	// 静默降级本地会让人以为数据都在）或本地目录（现状行为，demo/无 MinIO 环境）。
	// 与风格库媒体相互独立：独立桶（不开 versioning，覆写即弃旧版）、不设 PG 前置
	//（产物层没有账，前缀即清单）。docs/storage-cloud-migration.md。
	art := artifacts.NewLocal(cfg.DataDir)
	if cfg.MinIO.Endpoint != "" {
		m, err := artifacts.NewMinio(cfg.MinIO.Endpoint, cfg.MinIO.AccessKey, cfg.MinIO.SecretKey,
			cfg.MinIO.DataBucket, cfg.MinIO.Secure, cfg.DataDir)
		if err != nil {
			log.Fatal(err)
		}
		art = m
	}
	pipeline.SetArtifacts(art)
	prep.SetCloudStore(art)

	producer := &produce.Runner{
		DataDir: cfg.DataDir, RootDir: root, Hub: hub, Store: st, Artifacts: art,
		// 自动续跑不开（用户要求 2026-10-17）：启动对账只补「中断」账+聊天通知，
		// 要不要重跑由用户自己在界面上点，服务重启不再自动起任务。
		AutoResume: false,
	}
	ag := &agent.Agent{
		DataDir: cfg.DataDir, RootDir: root, Store: st, Lib: lib, Producer: producer, Artifacts: art,
		Emit:    hub.Emit,
		OnEvent: func(id, event, detail string) { hub.Emit(id, event, "", detail) },
	}
	// 制作终态（成片/失败/取消）必须落进聊天框——异步管线跑完时对话轮早已结束
	producer.OnDone = ag.NotifyResult
	producer.OnNotice = ag.PostNotice // 非终态插话（如自动续跑通知）：让用户知道流程自己在走
	// 启动对账：上次进程死在制作中途的项目补写终态+聊天通知（必须先于任何新任务）
	producer.Reconcile()
	refs := styleref.NewStore(cfg.DataDir, root)
	refs.Lib = lib       // 参考解析打标要查风格词表/落新词（同一实例，含账本后端选型）
	refs.Artifacts = art // 参考库产物上正本（解析完成即整套上传）
	ag.Refs = refs       // 参考库补线（list/analyze 参考工具用）
	refs.Reconcile()     // 启动对账：重启前卡在 running 的引用改写为 error（与制作同职责）
	// 准备阶段三工位（①文稿②风格③分镜）接线进管线：stagePrep 经钩子调用
	//（prep 依赖 pipeline，反向接线防 import 环）。产物齐全即重放跳过，老项目不碰。
	prepDeps := prep.Deps{RootDir: root, Lib: lib, Emit: hub.Emit, Artifacts: art}
	pipeline.PrepFn = func(ctx context.Context, p *pipeline.Project) error {
		return prep.Run(ctx, p, prepDeps)
	}
	// 准备阶段异步运行器（想法页签/对话入口共用）：终态回写状态 + 落聊天框
	preps := prep.NewRunner(cfg.DataDir, prepDeps)
	ag.Preps = preps // 对话侧重跑走 RunInline，与异步链共用同一把互斥（2026-10-19 高-6）
	preps.OnDone = func(id string, err error) {
		pp := pipeline.NewProject(cfg.DataDir, id)
		switch {
		case err == nil:
			agent.WriteProjectStatus(cfg.DataDir, st, id, "style")
			ag.PostNotice(id, "✅ 准备阶段完成（"+prep.Summary(pp)+"）——在「终审」页签打包确认后即可开工")
		case errors.Is(err, context.Canceled):
			ag.PostNotice(id, "⏹ 准备阶段已取消，可在「想法」页签随时重来")
		default:
			ag.PostNotice(id, "❌ 准备阶段没能跑完："+pipeline.FirstLine(err.Error(), 160)+"——「想法」页签可重试，或在对话里让我接着修")
		}
	}
	// 会话密钥与初始管理员：SESSION_SECRET > API_TOKEN 派生 > 随机（开放模式）。
	// 仅鉴权启用时引导建管理员——开放模式没有登录，建了也是死数据。
	sessionSecret, authActive := auth.DeriveSecret(os.Getenv("SESSION_SECRET"), os.Getenv("API_TOKEN"))
	if authActive {
		if _, err := auth.EnsureBootstrapAdmin(st, os.Getenv("INITIAL_ADMIN_USER"), os.Getenv("INITIAL_ADMIN_PASSWORD")); err != nil {
			log.Fatal(err)
		}
	}
	// 磁盘清理器：完结项目/参考超保留窗口（DISK_GC_DELAY，默认 24h）后删本地大文件
	//（正本已在 MinIO；契约小文件留本地）。运行中项目由 busy 钩子保护。
	startStorageGC(art, cfg.DataDir, cfg.GCDelay, func(id string) bool {
		return producer.IsRunning(id) || preps.IsRunning(id)
	})
	srv := &api.Server{
		DataDir: cfg.DataDir, RootDir: root, Store: st, Hub: hub, Agent: ag, Producer: producer, Preps: preps, Lib: lib, Refs: refs,
		Artifacts:     art,
		APIToken:      os.Getenv("API_TOKEN"),
		SessionSecret: sessionSecret, AuthActive: authActive,
	}

	// 开发诊断：DEBUG_PPROF=1 时挂 net/http/pprof（goroutine 转储定位段级流水线卡点用）。
	// 端口可用 DEBUG_PPROF_ADDR 改（默认 6060；被占时 ListenAndServe 失败只打日志不拒启）。
	if os.Getenv("DEBUG_PPROF") == "1" {
		addr := os.Getenv("DEBUG_PPROF_ADDR")
		if addr == "" {
			addr = "127.0.0.1:6060"
		}
		go func() {
			log.Println("pprof 监听 " + addr)
			if err := http.ListenAndServe(addr, nil); err != nil {
				log.Println("⚠ pprof 没起来（端口被占？换 DEBUG_PPROF_ADDR）: " + err.Error())
			}
		}()
	}

	gin.SetMode(gin.ReleaseMode)
	addr := os.Getenv("ADDR")
	if addr == "" {
		// 默认只绑本机回环：鉴权未上前公网不可达；部署侧 Dockerfile 已显式 ADDR=:8080
		addr = "127.0.0.1:8080"
	}
	// 公网/非回环绑定但没配任何鉴权（开放模式）：把"鉴权留给商业化"的前提缺口喊出来
	//（不拒启，留给部署者决策）
	if !srv.AuthActive {
		host, _, splitErr := net.SplitHostPort(addr)
		if splitErr == nil {
			if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
				log.Printf("⚠  ADDR=%s 非回环且未设 SESSION_SECRET/API_TOKEN——本机开放模式：所有接口（含 LLM 烧钱/文件上传）对可达者完全开放，强烈建议 .env 配置", addr)
			}
		}
	}
	httpSrv := &http.Server{
		Addr:    addr,
		Handler: srv.Router(),
		// 慢速攻击防线：头部 15s 内必须到齐；空闲连接 2 分钟回收。不设 WriteTimeout
		// ——SSE 长连接靠心跳保活，写超时会把它们全数掐断。
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	log.Printf("帧述 M1 服务启动 %s（store=%s llm_mode=%s data=%s）", addr, st.Kind(), cfg.LLMMode, cfg.DataDir)

	// 优雅停机：先停止接请求，再取消运行中的制作/重做并等终态落盘
	// （超时强退，残留 start 由下次启动 Reconcile 兜底）。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	log.Println("收到停机信号，正在收尾…")
	shutCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutCtx)
	producer.Shutdown(shutCtx)
	log.Println("已退出")
}
