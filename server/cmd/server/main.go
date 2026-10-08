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
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"webpage-video-agent/server/internal/agent"
	"webpage-video-agent/server/internal/api"
	"webpage-video-agent/server/internal/config"
	"webpage-video-agent/server/internal/events"
	"webpage-video-agent/server/internal/pipeline"
	"webpage-video-agent/server/internal/produce"
	"webpage-video-agent/server/internal/store"
	"webpage-video-agent/server/internal/stylelib"
	"webpage-video-agent/server/internal/styleref"
)

func main() {
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
	hub := events.NewHub()
	lib := stylelib.New(cfg.DataDir)
	producer := &produce.Runner{
		DataDir: cfg.DataDir, RootDir: root, Hub: hub, Store: st,
	}
	ag := &agent.Agent{
		DataDir: cfg.DataDir, Store: st, Lib: lib, Producer: producer,
		Emit:    hub.Emit,
		OnEvent: func(id, event, detail string) { hub.Emit(id, event, "", detail) },
	}
	// 制作终态（成片/失败/取消）必须落进聊天框——异步管线跑完时对话轮早已结束
	producer.OnDone = ag.NotifyResult
	// 启动对账：上次进程死在制作中途的项目补写终态+聊天通知（必须先于任何新任务）
	producer.Reconcile()
	refs := styleref.NewStore(cfg.DataDir, root)
	srv := &api.Server{
		DataDir: cfg.DataDir, RootDir: root, Store: st, Hub: hub, Agent: ag, Producer: producer, Lib: lib, Refs: refs,
		APIToken: os.Getenv("API_TOKEN"),
	}

	gin.SetMode(gin.ReleaseMode)
	addr := os.Getenv("ADDR")
	if addr == "" {
		// 默认只绑本机回环：鉴权未上前公网不可达；部署侧 Dockerfile 已显式 ADDR=:8080
		addr = "127.0.0.1:8080"
	}
	// 公网/非回环绑定但没配 API_TOKEN：把"鉴权留给商业化"的前提缺口喊出来（不拒启，留给部署者决策）
	if srv.APIToken == "" {
		host, _, splitErr := net.SplitHostPort(addr)
		if splitErr == nil {
			if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
				log.Printf("⚠  ADDR=%s 非回环且未设 API_TOKEN——所有接口（含 LLM 烧钱/文件上传）对可达者完全开放，强烈建议 .env 配置 API_TOKEN", addr)
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
