package main

// gc.go —— 磁盘清理器：完结/空闲超过保留窗口的项目与参考，删本地「可弃+已上传」
// 的文件给磁盘腾地方；契约小文件（JSON/txt/指纹/manifest）永远留本地——它们是
// 列表/鉴权/重放的读取面。安全阀三道：MinIO 未接线不跑（正本不在远端，删本地即
// 丢数据）、制作/准备进行中不跑、Keeper 大文件删前逐一验证远端正本。
// docs/storage-cloud-migration.md。

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"webpage-video-agent/server/internal/artifacts"
)

const gcSweepInterval = time.Hour

// startStorageGC 启动后台清理循环（启动 5 分钟后首扫——让对账/自动续跑先落位，
// 此后每小时）。delay < 0 = 永不清理。
func startStorageGC(art artifacts.Store, dataDir string, delay time.Duration, busy func(id string) bool) {
	if delay < 0 {
		log.Println("[gc] DISK_GC_DELAY<0：本地清理关闭，产物只上云不删本地")
		return
	}
	if !art.Available() {
		log.Println("[gc] 未接 MinIO：本地清理关闭（正本不在远端，删本地即丢数据）")
		return
	}
	go func() {
		time.Sleep(5 * time.Minute)
		for {
			sweepStorage(art, dataDir, delay, busy)
			time.Sleep(gcSweepInterval)
		}
	}()
}

// sweepStorage 单轮清扫：项目看 manifest/project.json 的 mtime 判空闲，
// 参考看 ref.json；空闲超窗才动手。
func sweepStorage(art artifacts.Store, dataDir string, delay time.Duration, busy func(id string) bool) {
	cutoff := time.Now().Add(-delay)
	var freedTotal int64

	if entries, err := os.ReadDir(filepath.Join(dataDir, "projects")); err == nil {
		for _, e := range entries {
			id := e.Name()
			if !e.IsDir() || strings.HasPrefix(id, "_") {
				continue
			}
			if busy != nil && busy(id) {
				continue
			}
			dir := filepath.Join(dataDir, "projects", id)
			if lastActivity(dir, "manifest.jsonl", "project.json").After(cutoff) {
				continue
			}
			if freed, files, err := artifacts.Prune(art, dataDir, artifacts.LocalPrefix(id)); err == nil && files > 0 {
				freedTotal += freed
				log.Printf("[gc] 项目 %s 清理 %d 个文件，释放 %.1f MB", id, files, float64(freed)/(1<<20))
			}
		}
	}
	if entries, err := os.ReadDir(filepath.Join(dataDir, "references")); err == nil {
		for _, e := range entries {
			id := e.Name()
			if !e.IsDir() || !strings.HasPrefix(id, "ref") {
				continue
			}
			dir := filepath.Join(dataDir, "references", id)
			if lastActivity(dir, "ref.json").After(cutoff) {
				continue
			}
			if freed, files, err := artifacts.Prune(art, dataDir, artifacts.ReferenceKey(id, "")); err == nil && files > 0 {
				freedTotal += freed
				log.Printf("[gc] 参考 %s 清理 %d 个文件，释放 %.1f MB", id, files, float64(freed)/(1<<20))
			}
		}
	}
	if freedTotal > 0 {
		log.Printf("[gc] 本轮共释放 %.1f MB", float64(freedTotal)/(1<<20))
	}
}

// lastActivity 目录最近活动时间：names 里最新的 mtime，全缺退目录自身 mtime
// （目录 mtime 只反映条目增删，文件内容追加不上浮，所以 manifest 这类高频
// 追加文件必须单独 Stat）。
func lastActivity(dir string, names ...string) time.Time {
	best := modTime(dir)
	for _, n := range names {
		if fi, err := os.Stat(filepath.Join(dir, n)); err == nil && fi.ModTime().After(best) {
			best = fi.ModTime()
		}
	}
	return best
}

func modTime(p string) time.Time {
	if fi, err := os.Stat(p); err == nil {
		return fi.ModTime()
	}
	return time.Time{} // zero = 很老，可清
}
