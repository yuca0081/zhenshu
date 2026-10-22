package prep

// util.go —— 工位共享件：Deps（宿主接线）、工具 schema 构造、样张截图与缩图
// （②的多模态自检走同一条路：ai/snap_samples.py 渲染 → 缩图 → dataURL 回喂）。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // 判卷帧/海报（ffmpeg 产物）
	_ "image/png"  // 海报与样张截图
	"os"
	"path/filepath"

	openai "github.com/sashabaranov/go-openai"

	"webpage-video-agent/server/internal/artifacts"
	"webpage-video-agent/server/internal/pipeline"
	"webpage-video-agent/server/internal/stylelib"
)

// contentHash8 内容指纹（sha256 前 8 字节 hex）：样张渲染缓存等场景用。
func contentHash8(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

// Deps 宿主接线：仓库根（ai/ 脚本定位）、风格库、SSE 播报、产物正本后端。
type Deps struct {
	RootDir   string
	Lib       *stylelib.Library
	Emit      func(projectID, typ, stage, detail string)
	Artifacts artifacts.Store // 准备工件上传/回源（nil/本地 = 不上云，docs/storage-cloud-migration.md）
}

func (d Deps) emit(projectID, typ, stage, detail string) {
	if d.Emit != nil {
		d.Emit(projectID, typ, stage, detail)
	}
}

// toolSchema 工具定义构造（parameters 直接进 function calling schema——
// 结构化字段格式放这里，按 §5.3 不进提示词）。
func toolSchema(name, desc string, params map[string]any) openai.Tool {
	if params == nil {
		params = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{
		Name: name, Description: desc, Parameters: params,
	}}
}

// snapSamples 跑 ai/snap_samples.py：把 style_samples.json 里的样张 HTML
// 截成 style/sample-N.png（与 produce 同一脚本同一语义，缓存在脚本里）。
func snapSamples(ctx context.Context, d Deps, p *pipeline.Project) error {
	if d.RootDir == "" {
		return errors.New("RootDir 未接线（无法截图）")
	}
	cmd := pipeline.CommandContext(ctx, pipeline.PythonExe(),
		filepath.Join(d.RootDir, "ai", "snap_samples.py"), p.Dir)
	cmd.Dir = d.RootDir
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("snap_samples 退出: %w\n%s", err, pipeline.TailLines(buf.String(), 8))
	}
	return nil
}

// shrinkFrame 样张截图 → 宽 480 的 JPEG dataURL。整图 base64 动辄半兆，
// 每回合消息重发会把多轮成本滚起来；480×270 的检查分辨率足够看构图配色。
func shrinkFrame(path string) (string, error) {
	return pipeline.ShrinkImageDataURL(path, 480, 70)
}

// shrinkRef 风格真迹 → 指定宽度的 JPEG dataURL（判卷对照物，比样张自检图
// 留多一档分辨率：质感/颗粒/字形的判读全靠它）。
func shrinkRef(path string, targetW int) (string, error) {
	return pipeline.ShrinkImageDataURL(path, targetW, 72)
}

// aHash 感知哈希（8×8 灰度均值哈希，64 位）：构图近重复检测用——字体/小文案的
// 改动动不了低频结构，同构图返工的距离是个位数，不同构图几十起步。
func aHash(path string) (uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	src, _, err := image.Decode(f)
	if err != nil {
		return 0, err
	}
	b := src.Bounds()
	const n = 8
	var grid [n][n]uint8
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			// 格子中心采样：(2x+1)/(2n)——旧式 (x+1)/(2n) 只扫到左上 1/4 区域
			r, g, bl, _ := src.At(b.Min.X+(2*x+1)*b.Dx()/(2*n), b.Min.Y+(2*y+1)*b.Dy()/(2*n)).RGBA()
			r8, g8, b8 := r>>8, g>>8, bl>>8
			// ITU-R 601 亮度，/1000 归一到 0-255（uint32 中间量，防溢出/截断）
			grid[y][x] = uint8((r8*299 + g8*587 + b8*114) / 1000)
		}
	}
	var sum uint64
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			sum += uint64(grid[y][x])
		}
	}
	mean := sum / (n * n)
	var h uint64
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if uint64(grid[y][x]) > mean {
				h |= 1 << uint(y*n+x)
			}
		}
	}
	return h, nil
}

// hashDistance 两个感知哈希的汉明距离（0-64，越小越像）。
func hashDistance(a, b uint64) int {
	d := a ^ b
	n := 0
	for d != 0 {
		d &= d - 1
		n++
	}
	return n
}
