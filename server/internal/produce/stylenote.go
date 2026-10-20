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
	"strings"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/pipeline"
)

// designNoteFor 段级风格注入（v2 笼子二入口，plan-v2 §4）：style_samples.json 含设计系统块
// （fonts/motion/device 任一）→ DesignDoc 机械渲染——皮肤（含双皮肤切换规则）/字体三角色/
// 动效语言/贯穿装置/版式轮换/负面清单一次给全，本段版式指派按段号内联；v3 起带强参考
// 特色落点表（style_brief.json 存在时）。否则退 v1 兜底（方向+皮肤槽位——旧项目）。
func designNoteFor(p *pipeline.Project, segIdx int) string {
	shared, seg := designNotesFor(p, segIdx)
	return shared + seg
}

// designNotesFor 设计系统注入拆两半（七刀#4 稳定前缀布局，docs/cost-optimization-plan
// §6 表#4）：shared=全段逐字一致的主体（进写画面提示的共享前缀），seg=本段版式指派
// （段变量，垫尾）——段信息插在共享文本中间曾把 GLM 前缀缓存斩断在 ≈2944 token。
// v1 兜底（方向+皮肤槽位，旧项目）整块共享，seg 为空。
func designNotesFor(p *pipeline.Project, segIdx int) (shared, seg string) {
	var ss contract.StyleSamples
	if b, err := os.ReadFile(p.Artifact("style/style_samples.json")); err == nil && json.Unmarshal(b, &ss) == nil && hasDesignSystem(&ss) {
		return "\n" + pipeline.DesignDocShared(&ss, pipeline.LoadStyleBrief(p)),
			pipeline.DesignSegAssign(&ss, segIdx)
	}
	return styleNoteFor(p), ""
}

// sampleStyleNote 样张源码注入（文字样式的权威规范）：场景模型此前只能"看图猜"样张
// 的文字用法——标题/副标题各用什么色、字距多大、分隔线细框怎么画，全在样张 HTML 里，
// 图像给不了这个粒度，正片文字样式于是漂移（副标题被升成金色标题、装饰线丢失，
// 2026-10-13 齐马蓝实录）。把确认样张的 HTML 源码直接注入场景提示：样式照抄，
// 内容按画面提示重写。上限防护：每张截 2400 字符、总量 6000（样式集中在头部
// <style>，截尾不伤关键段）。
func sampleStyleNote(p *pipeline.Project) string {
	var ss contract.StyleSamples
	if b, err := os.ReadFile(p.Artifact("style/style_samples.json")); err != nil ||
		json.Unmarshal(b, &ss) != nil {
		return ""
	}
	var parts []string
	total := 0
	for i, s := range ss.Samples {
		if i >= 3 || total >= 6000 {
			break
		}
		h := strings.TrimSpace(s.HTML)
		if h == "" {
			continue
		}
		if len(h) > 2400 {
			h = h[:2400]
		}
		parts = append(parts, fmt.Sprintf("样张 %d（%s）：\n```html\n%s\n```", i, s.Tag, h))
		total += len(h)
	}
	if len(parts) == 0 {
		return ""
	}
	return "\n## 样张源码（文字样式的权威规范——样式照它，内容按本段画面提示）\n" +
		"- 字体族/字重/字距/颜色/阴影柔光/装饰（分隔线、细框、角标）以样张写法为准；\n" +
		"  标题层、副标题层、弱化注脚各用什么颜色照抄样张，别把强调色挪去当标题色\n" +
		"- 只搬样式不搬内容：样张里的具体文案与物件禁止出现，画面内容跟本段画面提示走\n" +
		"- 版式构图按本段画幅与本段指定版式重排（样张是 960×540 横版，画幅不同时自己换算）\n" +
		strings.Join(parts, "\n") + "\n"
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

// styleKeyframeImages 简报证据帧（v3：原片真迹）→ 多模态注入图（≤6 张）。
// 证据来自项目 style/style_brief.json 的 evidence（绝对路径，style_media 快照），
// 每张先经 ffmpeg 压成 1280 宽 JPEG 缓存进项目 style/（key-<条目id>-<帧名>.jpg，
// 与段渲染抽帧同规格），压图/读图失败单张跳过——真迹是加分项，缺失不挡出片。
func styleKeyframeImages(ctx context.Context, p *pipeline.Project) []visImg {
	brief := pipeline.LoadStyleBrief(p)
	if brief == nil || len(brief.Evidence) == 0 {
		return nil
	}
	ff, err := pipeline.FFmpegPath()
	if err != nil {
		return nil
	}
	var vis []visImg
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
		vis = append(vis, visImg{
			URL:   "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(b),
			Path:  projRel(p, dst),
			Label: "风格真迹（原片帧）",
		})
	}
	return vis
}

// keyframeNote 场景生成提示里的真迹说明（真迹紧随样张截图；素材真图在真迹之后）。
// 真迹只准学气质（配色/光影/质感/密度/节奏）——物件是原片题材，照着画就是题材中毒。
func keyframeNote(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("\n- 附图样张之后紧随的 %d 张是本风格来源视频的原片真迹（关键帧）：只学气质——配色、光影、质感、画面密度、构图节奏；真迹里画的物件（建筑/人物/器物等）是原视频的题材，与本片无关，禁止模仿着画；本段画什么永远由画面提示说了算\n", n)
}
