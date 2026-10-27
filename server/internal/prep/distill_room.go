package prep

// distill_room.go —— 蒸馏毛坯房（一次性 Docker 容器）的代理层
//（docs/style-kit-distillation-plan-2026-10-26.md §2/§5）：监工经 docker exec
// 操控房间；大脑（LLM）在门外，API key 永不进容器。四样通用能力（§4.1：
// 跑命令/写文件/读文件/搜文本）全部落在这层——刻意不造专用工具，容器本身就是墙。
//
// 写文件走 stdin 管道（docker exec -i … cat > path）：长内容经命令行参数传递
// 会被通道静默损伤（云服务器实测教训），文件管道是唯一可靠通道。
// 图片回看（读 PNG 即看画）利用 out/ 是 bind mount 的事实：宿主直接读文件转
// dataURL，走 Loop 的系统附图通道，不占 agent 上下文的图片块。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"webpage-video-agent/server/internal/pipeline"
)

// distillImage 地基镜像名（改 tools/ 脚本 = 升版本号重建镜像，DISTILL_IMAGE 覆盖）。
func distillImage() string {
	if v := os.Getenv("DISTILL_IMAGE"); v != "" {
		return v
	}
	return "distill-base:1"
}

// distillRounds 画师回合预算（首单人工 4 轮迭代；agent 一轮多个工具调用，
// 给足探索+修复余量）。
func distillRounds() int {
	return envIntDefault("DISTILL_ROUNDS", 48)
}

// distillWall 整单墙钟（含渲染/判卷）；到点必须带伤收卷。
func distillWall() time.Duration {
	return time.Duration(envIntDefault("DISTILL_WALL_MINUTES", 50)) * time.Minute
}

func envIntDefault(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// distillRoom 一个正在服役的房间。exec 可注入（测试替身）；cid 空 = 还没起。
type distillRoom struct {
	cid     string
	taskDir string
	image   string
	// exec 跑 docker 子命令（args 不含 "docker" 前缀）；stdin 非空时喂 stdin。
	// 返回 combined output；进程退出码非零也带 output 返回（err 为 *exec.ExitError）。
	exec func(ctx context.Context, args []string, stdin string) (string, error)
}

// realDockerExec 真实 docker CLI 通道。
func realDockerExec(ctx context.Context, args []string, stdin string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	cmd := pipeline.CommandContext(cctx, "docker", args...)
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	err := cmd.Run()
	return out.String(), err
}

// dockerRunArgs 房间启动参数（纯函数：承重墙三规矩+资源限的落地形态，测试钉住）。
// 挂载 in:ro / out:rw，容器内恒为 /task 布局，与 SPEC.md 的路径口径一致。
func dockerRunArgs(image, taskDir string) []string {
	return []string{
		"run", "-d", "--rm",
		"--memory", "2g", "--cpus", "2",
		"--cap-drop=ALL", "--security-opt", "no-new-privileges",
		"--user", "1000:1000",
		"-v", filepath.Join(taskDir, "in") + ":/task/in:ro",
		"-v", filepath.Join(taskDir, "out") + ":/task/out:rw",
		image, "sleep", "infinity",
	}
}

// start 探活 docker 与地基镜像后开房。缺件报人话（服务器缺件处置方针：
// 显式报告，不静默降级）；out 目录放宽到 0777（容器内 uid 1000 要写）。
func (r *distillRoom) start(ctx context.Context) error {
	if _, err := r.exec(ctx, []string{"version", "--format", "ok"}, ""); err != nil {
		return fmt.Errorf("docker 不可用（蒸馏单只在配了 Docker 的宿主跑，本机 Windows 开发环境不参与）: %w", err)
	}
	if _, err := r.exec(ctx, []string{"image", "inspect", r.image}, ""); err != nil {
		return fmt.Errorf("地基镜像 %s 不在：先在宿主 docker build -t %s <仓库>/distill/（改过 distill/tools/ 要升版本号重建）", r.image, r.image)
	}
	out, err := r.exec(ctx, dockerRunArgs(r.image, r.taskDir), "")
	if err != nil {
		return fmt.Errorf("开房失败: %s", pipeline.TailLines(out, 8))
	}
	cid := strings.TrimSpace(out)
	if cid == "" || strings.Contains(cid, "\n") {
		return fmt.Errorf("开房失败：docker run 没回容器 id（%s）", pipeline.FirstLine(out, 120))
	}
	r.cid = cid
	return nil
}

// stop 烧房（用完销毁不复用）。best-effort：失败只记账不阻断收卷。
func (r *distillRoom) stop() {
	if r.cid == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _ = r.exec(ctx, []string{"rm", "-f", r.cid}, "")
	r.cid = ""
}

// run 在房里跑一条 shell 命令（cwd=/task）。命令失败以「输出+退出码」回喂，
// 房间本身故障才返回 error（监工据此终止——连续故障说明环境坏了）。
func (r *distillRoom) run(ctx context.Context, command string, timeout time.Duration) (string, error) {
	if r.cid == "" {
		return "", errors.New("房间没开")
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := r.exec(cctx, []string{"exec", "-w", "/task", r.cid, "bash", "-c", command}, "")
	text := tailRunes(out, 6000)
	if err != nil {
		if code := exitCodeOf(err); code >= 0 {
			return text, fmt.Errorf("命令退出码 %d", code)
		}
		if cctx.Err() != nil {
			return text, fmt.Errorf("命令超时（%s）——大活拆小跑或调大 timeout_secs", timeout)
		}
		return text, fmt.Errorf("房间通道故障: %v", err)
	}
	return text, nil
}

// runStdin 同 run，但经 stdin 管道喂数据（写文件专用通道）。
func (r *distillRoom) runStdin(ctx context.Context, command, stdin string) (string, error) {
	if r.cid == "" {
		return "", errors.New("房间没开")
	}
	out, err := r.exec(ctx, []string{"exec", "-i", "-w", "/task", r.cid, "bash", "-c", command}, stdin)
	return tailRunes(out, 2000), err
}

// roomRelPath 房内相对路径守门：拒绝对路径/回爬/反斜杠/空白与特殊字符
// （后续要嵌进 bash 命令行，字符集收紧比逐处转义可靠）；写只放行 out/ 与 work/
// （in 是只读考卷，/opt/distill-tools 是地基）。
func roomRelPath(p string, write bool) (string, error) {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "/task/")
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return "", fmt.Errorf("路径 %q 要用房间内相对路径（如 out/kit.js）", p)
	}
	if strings.Contains(p, "..") {
		return "", errors.New("路径不许回爬（..）")
	}
	for _, r := range p {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '-' || r == '_' || r == '.' || r == '/') {
			return "", fmt.Errorf("路径 %q 含特殊字符（只许字母/数字/._-/ ）", p)
		}
	}
	root := p
	if i := strings.IndexByte(p, '/'); i >= 0 {
		root = p[:i]
	}
	if root != "out" && root != "work" {
		if write || root != "in" {
			return "", fmt.Errorf("路径 %q 越界：写只许 out/ 与 work/，读另可 in/", p)
		}
	}
	return p, nil
}

// tailRunes 保尾部（命令输出的错误摘要通常在末尾）。
func tailRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…（前面截断）…" + s[len(s)-n:]
}

// roomTools 画师的四样通用能力（§4.1 刻意极简；无专用工具）。
func (r *distillRoom) tools() []Tool {
	return []Tool{
		{
			Def: toolSchema("run_command", "在房里跑一条 shell 命令（bash，工作目录 /task）。装依赖记得 npm install --ignore-scripts；渲染走 /opt/distill-tools/render.sh。", map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command":      map[string]any{"type": "string", "description": "shell 命令"},
					"timeout_secs": map[string]any{"type": "integer", "description": "时限秒数，默认 120，上限 600（渲染/npm install 可调大）"},
				},
				"required": []string{"command"},
			}),
			Run: func(ctx context.Context, args string) Observation {
				var in struct {
					Command     string `json:"command"`
					TimeoutSecs int    `json:"timeout_secs"`
				}
				if err := decodeArgs(args, &in); err != nil {
					return Observation{Text: "参数错误：" + err.Error()}
				}
				if strings.TrimSpace(in.Command) == "" {
					return Observation{Text: "command 不能空"}
				}
				if in.TimeoutSecs <= 0 {
					in.TimeoutSecs = 120
				}
				if in.TimeoutSecs > 600 {
					in.TimeoutSecs = 600
				}
				out, err := r.run(ctx, in.Command, time.Duration(in.TimeoutSecs)*time.Second)
				if err != nil {
					return Observation{Text: out + "\n" + err.Error()}
				}
				if strings.TrimSpace(out) == "" {
					return Observation{Text: "（无输出，退出码 0）"}
				}
				return Observation{Text: out}
			},
		},
		{
			Def: toolSchema("write_file", "写文件（经 stdin 管道，长内容无损）。只许 out/（交付）与 work/（草稿）下。", map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":    map[string]any{"type": "string", "description": "房间内相对路径，如 out/kit.js"},
					"content": map[string]any{"type": "string", "description": "全文内容（整体覆盖写）"},
				},
				"required": []string{"path", "content"},
			}),
			Run: func(ctx context.Context, args string) Observation {
				var in struct {
					Path    string `json:"path"`
					Content string `json:"content"`
				}
				if err := decodeArgs(args, &in); err != nil {
					return Observation{Text: "参数错误：" + err.Error()}
				}
				p, err := roomRelPath(in.Path, true)
				if err != nil {
					return Observation{Text: err.Error()}
				}
				if len(in.Content) > 512*1024 {
					return Observation{Text: "单文件超 512KB——拆分或瘦身"}
				}
				out, err := r.runStdin(ctx, "mkdir -p \"$(dirname \""+p+"\")\" && cat > \""+p+"\" && wc -c < \""+p+"\"", in.Content)
				if err != nil {
					return Observation{Text: "写失败：" + err.Error()}
				}
				return Observation{Text: "已写 " + p + "（" + strings.TrimSpace(out) + " 字节）"}
			},
		},
		{
			Def: toolSchema("read_file", "读文件。文本回喂尾部（≤12KB）；PNG/JPG 直接以附图回看——看真迹、看自己的渲染结果都用它（in/ 或 out/ 下的图才能附图）。", map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "房间内路径：out/…、work/…、in/…（考卷）或 /opt/distill-tools/…（规则书）"},
				},
				"required": []string{"path"},
			}),
			Run: func(ctx context.Context, args string) Observation {
				var in struct {
					Path string `json:"path"`
				}
				if err := decodeArgs(args, &in); err != nil {
					return Observation{Text: "参数错误：" + err.Error()}
				}
				p := strings.TrimSpace(in.Path)
				lower := strings.ToLower(p)
				if strings.HasSuffix(lower, ".png") || strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") {
					return r.viewImage(p)
				}
				var target string
				if strings.HasPrefix(p, "/opt/distill-tools/") && !strings.Contains(p, "..") {
					target = p
				} else {
					rel, err := roomRelPath(p, false)
					if err != nil {
						return Observation{Text: err.Error()}
					}
					target = rel
				}
				out, err := r.run(ctx, "head -c 12000 \""+target+"\"", 30*time.Second)
				if err != nil {
					return Observation{Text: out + "\n读取失败：" + err.Error()}
				}
				return Observation{Text: out}
			},
		},
		{
			Def: toolSchema("search_text", "房内全文搜索（grep -rn）。找「Math.random」「@latest」这类违禁品，或翻 node_modules 源码找 API。", map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern": map[string]any{"type": "string", "description": "正则（grep -E 语法）"},
					"path":    map[string]any{"type": "string", "description": "搜索根，默认 .（整个 /task）"},
				},
				"required": []string{"pattern"},
			}),
			Run: func(ctx context.Context, args string) Observation {
				var in struct {
					Pattern string `json:"pattern"`
					Path    string `json:"path"`
				}
				if err := decodeArgs(args, &in); err != nil {
					return Observation{Text: "参数错误：" + err.Error()}
				}
				if strings.TrimSpace(in.Pattern) == "" {
					return Observation{Text: "pattern 不能空"}
				}
				root := "."
				if in.Path != "" {
					rel, err := roomRelPath(in.Path, false)
					if err != nil {
						return Observation{Text: err.Error()}
					}
					root = rel
				}
				// 正则经 stdin 传给 grep 变量（模式里的引号/$ 不经命令行，无损）
				out, err := r.runStdin(ctx, "P=\"$(cat)\"; grep -rnE --binary-files=without-match --exclude-dir=node_modules -- \"$P\" \""+
					root+"\" | tail -n 120", in.Pattern)
				if err != nil {
					// grep 无命中退出码 1，是正常空结果不是故障
					if strings.Contains(err.Error(), "退出码 1") {
						return Observation{Text: "无命中"}
					}
					return Observation{Text: out + "\n" + err.Error()}
				}
				if strings.TrimSpace(out) == "" {
					return Observation{Text: "无命中"}
				}
				return Observation{Text: out}
			},
		},
	}
}

// viewImage 宿主侧读图转 dataURL（out/、in/ 是 bind mount，宿主直读；work/ 是
// 容器层，宿主看不见——提示画师把要看的结果渲到 out/）。
func (r *distillRoom) viewImage(p string) Observation {
	rel, err := roomRelPath(p, false)
	if err != nil {
		return Observation{Text: err.Error()}
	}
	i := strings.IndexByte(rel, '/')
	if i < 0 {
		return Observation{Text: "路径要指到文件，如 out/repro-1.png"}
	}
	root := rel[:i]
	if root == "work" {
		return Observation{Text: "work/ 下的图宿主看不见：把要看的结果渲到 out/ 再 read_file"}
	}
	host := filepath.Join(r.taskDir, filepath.FromSlash(rel))
	url, err := pipeline.ShrinkImageDataURL(host, 1100, 82)
	if err != nil {
		return Observation{Text: "读图失败：" + err.Error()}
	}
	return Observation{Text: fmt.Sprintf("附图：%s（路径记忆用，内容看图）", rel), Images: []string{url}}
}
