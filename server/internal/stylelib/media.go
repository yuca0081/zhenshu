package stylelib

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// 媒体快照：视频解析风格入库时把原片 + 抽帧收进风格自己的目录
//（data/library/style_media/<id>/），参考视频库删除后详情抽屉仍可回看。
// 硬链接优先（同卷零拷贝），跨卷/不支持时降级整拷；已存在的文件跳过（幂等）。

// MediaDir 风格媒体目录（无则不代表没有媒体——老条目走 references 回退）。
func (l *Library) MediaDir(id string) string {
	return filepath.Join(l.dir, "style_media", id)
}

// SnapshotMedia 把 sourceDir（参考视频目录：video.mp4 + frame-*.png）快照进风格媒体目录。
// 部分成功不回滚：媒体是冗余不是事实源，缺哪张下次重新确认/手动补即可。
func (l *Library) SnapshotMedia(styleID, sourceDir string) error {
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		return fmt.Errorf("读参考视频目录: %w", err)
	}
	dstDir := l.MediaDir(styleID)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}
	var firstErr error
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		keep := name == "video.mp4" || name == "cover.png" ||
			(strings.HasPrefix(name, "frame-") && strings.HasSuffix(name, ".png"))
		if !keep {
			continue
		}
		dst := filepath.Join(dstDir, name)
		if _, err := os.Stat(dst); err == nil {
			continue // 已快照过
		}
		src := filepath.Join(sourceDir, name)
		if err := os.Link(src, dst); err == nil {
			continue
		}
		if err := copyFile(src, dst); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

// BackfillMedia 启动回填：video: 来源的老条目若媒体目录为空且参考视频目录还在，
// 补一次快照（入库是一次性的，老条目靠这里补齐）。失败逐条跳过不挡启动。
func (l *Library) BackfillMedia(referencesDir string) {
	styles, err := l.List()
	if err != nil {
		return
	}
	for _, st := range styles {
		if !st.Published || !strings.HasPrefix(st.OriginProject, "video:") {
			continue
		}
		dir := l.MediaDir(st.ID)
		if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
			continue // 已有快照
		}
		refDir := filepath.Join(referencesDir, strings.TrimPrefix(st.OriginProject, "video:"))
		if _, err := os.Stat(filepath.Join(refDir, "video.mp4")); err != nil {
			continue // 原片不在（已删/异机），无从补
		}
		_ = l.SnapshotMedia(st.ID, refDir)
	}
}
