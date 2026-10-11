package produce

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/pipeline"
)

// designNoteFor 段级风格注入（v2 笼子二入口，plan-v2 §4）：style_samples.json 含设计系统块
// （fonts/motion/device 任一）→ DesignDoc 机械渲染——皮肤（含双皮肤切换规则）/字体三角色/
// 动效语言/贯穿装置/版式轮换/负面清单一次给全，本段版式指派按段号内联；v3 起带强参考
// 特色落点表（style_brief.json 存在时）。否则退 v1 兜底（方向+皮肤槽位——旧项目）。
func designNoteFor(p *pipeline.Project, segIdx int) string {
	var ss contract.StyleSamples
	if b, err := os.ReadFile(p.Artifact("style/style_samples.json")); err == nil && json.Unmarshal(b, &ss) == nil && hasDesignSystem(&ss) {
		return "\n" + pipeline.DesignDocWithBrief(&ss, segIdx, pipeline.LoadStyleBrief(p))
	}
	return styleNoteFor(p)
}

// hasDesignSystem 是否 v2 设计系统（区别于 v1 纯皮肤文件——升级换代的判别器）。
func hasDesignSystem(ss *contract.StyleSamples) bool {
	return ss.Fonts != nil || ss.Motion != nil || ss.Device != nil
}

// styleNoteFor 项目风格 → 场景生成提示里的风格说明（v1 兜底：方向 + 皮肤槽位）。
// v3 库条目无 tokens/版式，库内查询兜底（projectStyle/ByDirection）已随 schema 退役：
// 老项目按 style_id 查不到时静默只剩方向（画面靠项目自己的设计系统文档）。
func styleNoteFor(p *pipeline.Project) string {
	direction := projectDirection(p)
	if direction == "" {
		return ""
	}
	note := "\n## 本片风格\n- 方向：" + direction
	note += "\n- 风格≠题材：以上是构图语法（怎么画），画面主体永远跟本段画面提示走，\n  禁止把主体降级成小卡片迁就版式；风格来源视频的题材物件与品牌字样禁止入画"
	note += skinNote(p)
	return note
}

// styleKeyframeImages 简报证据帧（v3：原片真迹）→ data URL（≤6 张）。
// 证据来自项目 style/style_brief.json 的 evidence（绝对路径，style_media 快照），
// 每张先经 ffmpeg 压成 1280 宽 JPEG 缓存进项目 style/（key-<条目id>-<帧名>.jpg，
// 与段渲染抽帧同规格），压图/读图失败单张跳过——真迹是加分项，缺失不挡出片。
func styleKeyframeImages(ctx context.Context, p *pipeline.Project) []string {
	brief := pipeline.LoadStyleBrief(p)
	if brief == nil || len(brief.Evidence) == 0 {
		return nil
	}
	ff, err := pipeline.FFmpegPath()
	if err != nil {
		return nil
	}
	var urls []string
	const maxKeyframes = 6 // 与素材真图上限同口径：真迹是对照物不是正文，多塞白烧 token
	for i, src := range brief.Evidence {
		if i >= maxKeyframes {
			break
		}
		dst := p.Artifact("style/key-" + brief.EntryID + "-" + filepath.Base(src) + ".jpg")
		if _, err := os.Stat(dst); err != nil {
			var buf bytes.Buffer
			cmd := exec.CommandContext(ctx, ff, "-y", "-i", src, "-vf", "scale=1280:-2", "-q:v", "5", dst)
			cmd.Stdout, cmd.Stderr = &buf, &buf
			if err := cmd.Run(); err != nil {
				continue
			}
		}
		b, err := os.ReadFile(dst)
		if err != nil {
			continue
		}
		urls = append(urls, "data:image/jpeg;base64,"+base64.StdEncoding.EncodeToString(b))
	}
	return urls
}

// keyframeNote 场景生成提示里的真迹说明（真迹紧随样张截图；素材真图在真迹之后）。
// 真迹只准学气质（配色/光影/质感/密度/节奏）——物件是原片题材，照着画就是题材中毒。
func keyframeNote(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("\n- 附图样张之后紧随的 %d 张是本风格来源视频的原片真迹（关键帧）：只学气质——配色、光影、质感、画面密度、构图节奏；真迹里画的物件（建筑/人物/器物等）是原视频的题材，与本片无关，禁止模仿着画；本段画什么永远由画面提示说了算\n", n)
}
