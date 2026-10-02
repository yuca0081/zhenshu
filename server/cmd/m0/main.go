// m0 — M0 管线 CLI（无 UI）。
//
// 用法：
//
//	m0 new <article.txt> [项目名]     建项目（拷入文稿，生成目录骨架）
//	m0 run <项目ID> [阶段]            顺序跑到指定阶段（默认尽力跑）
//	m0 status <项目ID>                查看产物与 manifest 尾部
//
// 会话模式（LLM_MODE=manual，默认）：run 停在等待 LLM 产物处，
// 请求已写到 llm/*.request.md；产物写到对应 llm/*.json 后重跑即续。
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"webpage-video-agent/server/internal/config"
	"webpage-video-agent/server/internal/pipeline"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cfg, err := config.Load()
	if err != nil {
		fatal(err)
	}
	pipeline.EnsureFFmpeg()
	pipeline.SetRootDir(repoRoot(cfg))
	pipeline.SetDataDir(cfg.DataDir)
	switch os.Args[1] {
	case "new":
		if len(os.Args) < 3 {
			fatal(fmt.Errorf("用法: m0 new <article.txt> [项目名]"))
		}
		cmdNew(cfg, os.Args[2], pick(os.Args, 3))
	case "run":
		if len(os.Args) < 3 {
			fatal(fmt.Errorf("用法: m0 run <项目ID> [阶段]"))
		}
		cmdRun(cfg, os.Args[2], pick(os.Args, 3))
	case "status":
		if len(os.Args) < 3 {
			fatal(fmt.Errorf("用法: m0 status <项目ID>"))
		}
		cmdStatus(cfg, os.Args[2])
	default:
		usage()
		os.Exit(2)
	}
}

func pick(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}

// repoRoot 仓库根推断（DATA_DIR 的上级，ai/ 在其中；与 server/main.go 同法）。
func repoRoot(cfg *config.Config) string {
	root, _ := filepath.Abs(filepath.Join(cfg.DataDir, ".."))
	if _, err := os.Stat(filepath.Join(root, "ai")); err != nil {
		return "" // 推断失败：BGM 曲库功能自动失效，管线其余不受影响
	}
	return root
}

func usage() {
	fmt.Println("m0 — 帧述 M0 管线 CLI\n\n  m0 new <article.txt> [项目名]\n  m0 run <项目ID> [阶段]\n  m0 status <项目ID>")
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "错误:", err)
	os.Exit(1)
}

func cmdNew(cfg *config.Config, articlePath, name string) {
	raw, err := os.ReadFile(articlePath)
	if err != nil {
		fatal(fmt.Errorf("读文稿: %w", err))
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		fatal(fmt.Errorf("文稿为空"))
	}
	id := "p" + time.Now().Format("20060102-150405")
	if name == "" {
		name = strings.TrimSpace(strings.SplitN(strings.TrimSpace(string(raw)), "\n", 2)[0])
		if len([]rune(name)) > 24 {
			name = string([]rune(name)[:24])
		}
	}
	p := pipeline.NewProject(cfg.DataDir, id)
	for _, d := range []string{
		"input", "llm", "manuscripts", "storyboards", "style",
		"audio", "compositions/frames", "compositions/registry", "renders",
	} {
		if err := os.MkdirAll(filepath.Join(p.Dir, d), 0o755); err != nil {
			fatal(err)
		}
	}
	if err := os.WriteFile(p.Artifact("input/article.txt"), raw, 0o644); err != nil {
		fatal(err)
	}
	meta, _ := json.MarshalIndent(map[string]any{
		"id": id, "name": name, "status": "created",
		"created_at": time.Now().Format(time.RFC3339),
		"llm_mode":   cfg.LLMMode,
	}, "", "  ")
	if err := os.WriteFile(p.Artifact("project.json"), meta, 0o644); err != nil {
		fatal(err)
	}
	p.Manifest("project.created", name)
	fmt.Printf("✓ 项目已建: %s（%s）\n  %s\n", id, name, p.Dir)
	fmt.Printf("下一步: m0 run %s\n", id)
}

func cmdRun(cfg *config.Config, id, until string) {
	p := pipeline.NewProject(cfg.DataDir, id)
	if _, err := os.Stat(p.Dir); err != nil {
		fatal(fmt.Errorf("项目不存在: %s", p.Dir))
	}
	fmt.Printf("项目 %s · LLM 模式: %s\n", id, cfg.LLMMode)
	err := pipeline.Run(context.Background(), p, until)
	if err == nil {
		return
	}
	// 等待 LLM / 待实现 属正常停点，退出码 0 方便脚本串联；其余失败必须非零退出，
	// 否则脚本串联会把半途而废的管线当成功（错误文本 Run 已打印）
	var await *pipeline.ErrAwaitLLM
	if errors.As(err, &await) || errors.Is(err, pipeline.ErrTodo) {
		os.Exit(0)
	}
	os.Exit(1)
}

func cmdStatus(cfg *config.Config, id string) {
	p := pipeline.NewProject(cfg.DataDir, id)
	if _, err := os.Stat(p.Dir); err != nil {
		fatal(fmt.Errorf("项目不存在: %s", p.Dir))
	}
	fmt.Printf("项目 %s\n  目录: %s\n\n产物:\n", id, p.Dir)
	for _, rel := range []string{
		"manuscripts/manuscript.json", "storyboards/storyboard.json",
		"style/style_samples.json", "audio/", "compositions/frames/",
		"renders/main.mp4",
	} {
		marker := "✗"
		if st, err := os.Stat(p.Artifact(rel)); err == nil {
			marker = "✓"
			if st.IsDir() {
				entries, _ := os.ReadDir(p.Artifact(rel))
				if len(entries) == 0 {
					marker = "○"
				}
			}
		}
		fmt.Printf("  %s %s\n", marker, rel)
	}
	manifest := p.Artifact("manifest.jsonl")
	if _, err := os.Stat(manifest); err == nil {
		fmt.Println("\nmanifest 尾部:")
		lines := readTail(manifest, 8)
		for _, l := range lines {
			var ev struct {
				TS     string `json:"ts"`
				Event  string `json:"event"`
				Detail string `json:"detail"`
			}
			if json.Unmarshal([]byte(l), &ev) == nil {
				fmt.Printf("  %s  %-18s %s\n", ev.TS[11:19], ev.Event, truncate(ev.Detail, 60))
			}
		}
	}
}

func readTail(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
