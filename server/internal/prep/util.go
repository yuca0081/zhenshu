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
