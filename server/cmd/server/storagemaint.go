package main

// storagemaint.go —— 存储维护子命令（离线跑，不占 8080）：
//
//	server -storage-sync    存量回填：data/projects、data/references 全量补传 MinIO
//	                        （幂等：远端已存在的跳过）。服务器首次上云跑一遍，
//	                        之后由制作检查点自动增量；本命令也是缺口的修补工具。
//	server -storage-prune   立即清理：对全部项目/参考执行与后台 GC 相同的本地清理
//	                        （Keeper 大文件逐个确认远端正本后才删）。务必先停服——
//	                        本命令不感知运行态，运行中项目的产物被删会断渲染。
//
// 两者都要求 MINIO_* 已配置；未配 = 本地即正本，无远端可依。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"webpage-video-agent/server/internal/artifacts"
	"webpage-video-agent/server/internal/config"
)

func newArtifactsStore() (artifacts.Store, *config.Config) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	if cfg.MinIO.Endpoint == "" {
		fmt.Println("未配置 MINIO_*：本地即正本，没有可回填/依据的远端——先在 .env 配好对象存储")
		os.Exit(1)
	}
	art, err := artifacts.NewMinio(cfg.MinIO.Endpoint, cfg.MinIO.AccessKey, cfg.MinIO.SecretKey,
		cfg.MinIO.DataBucket, cfg.MinIO.Secure, cfg.DataDir)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	return art, cfg
}

// entityPrefixes 列出项目区/参考库下全部实体的对象前缀。
func entityPrefixes(dataDir, kind string) []string {
	var out []string
	entries, err := os.ReadDir(filepath.Join(dataDir, kind))
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if kind == "projects" {
			if strings.HasPrefix(e.Name(), "_") {
				continue
			}
			out = append(out, artifacts.ProjectKey(e.Name(), ""))
		} else {
			if !strings.HasPrefix(e.Name(), "ref") {
				continue
			}
			out = append(out, artifacts.ReferenceKey(e.Name(), ""))
		}
	}
	return out
}

func runStorageSync(_ []string) {
	art, cfg := newArtifactsStore()
	total := 0
	for _, kind := range []string{"projects", "references"} {
		for _, prefix := range entityPrefixes(cfg.DataDir, kind) {
			n, err := artifacts.SyncUp(art, cfg.DataDir, prefix)
			if err != nil {
				fmt.Printf("  %s 补传中断: %v\n", prefix, err)
			}
			if n > 0 {
				fmt.Printf("  %s 上传 %d 个文件\n", prefix, n)
				total += n
			}
		}
	}
	fmt.Printf("回填完成：共上传 %d 个文件（远端已有的已跳过）\n", total)
}

func runStoragePrune(_ []string) {
	art, cfg := newArtifactsStore()
	fmt.Print("⚠  将删除本地可弃/已上云的大文件（契约小文件保留）。请确认服务已停止，回车继续，Ctrl+C 中止…")
	fmt.Scanln()
	var total int64
	for _, kind := range []string{"projects", "references"} {
		for _, prefix := range entityPrefixes(cfg.DataDir, kind) {
			freed, files, err := artifacts.Prune(art, cfg.DataDir, prefix)
			if err != nil {
				fmt.Printf("  %s 清理中断: %v\n", prefix, err)
			}
			if files > 0 {
				fmt.Printf("  %s 清理 %d 个文件，释放 %.1f MB\n", prefix, files, float64(freed)/(1<<20))
				total += freed
			}
		}
	}
	fmt.Printf("清理完成：共释放 %.1f MB\n", float64(total)/(1<<20))
}
