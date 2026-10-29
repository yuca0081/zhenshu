package review

// tools.go —— 审核取证工具（§6.2 两读一交）：get_frame 指定时刻单帧、
// get_clip 连续短片段。取帧结果按（段+时刻）缓存——修复轮/重放不重取；
// 取证调用留痕由 Loop.OnTool 交给调用方落盘。
// 帧压缩到宽 960 的 JPEG dataURL：480 下中文展示字体（黄油体 88px 标题）缩成
// 22px 会糊成错形，判卷官按画面读出「那可话/去黑/指窑」这类不存在的错字——
// seg09 两轮 high 触发签名刹车（2026-10-29 实测）；960 下 44px 可稳读。
// 整图 base64 会让多轮成本滚雪球，故仍走 JPEG q5 压缩不放大原辨析率。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	_ "image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	openai "github.com/sashabaranov/go-openai"

	"webpage-video-agent/server/internal/pipeline"
)

// clipMaxSpan get_clip 跨度上限（§6.2：超限截断报错）。
const clipMaxSpan = 2.5

// clipFallbackFPS 视频直喂不可用时的帧序列密度（§6.2 退路：6–8fps 带时间戳）。
const clipFallbackFPS = 6

// Files 取证目标：段片与缓存根。Service 持有。
type serviceFS struct {
	segVideo func(segID string) string // 段片路径（缺失返回 ""）
	cacheDir string                    // reports/audit（绝对）
	relBase  string                    // cacheDir 的项目内相对路径（"reports/audit"），注入图引用用
}

func hash8(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

// extractFrame 抽指定时刻单帧（ffmpeg，缓存键 seg+t+源文件尺寸/mtime 两位小数）。
// 返回（JPEG dataURL, 项目内相对路径）。路径给「过程」页对话留痕的注入图引用
// （缩略图/点击放大走 /projects/:id/file/ 白名单取件）；越界（负值或超段长 0.05s
// 容差）报错回流，由模型改参数重取。
// 缓存键必须带源文件的 Size+mtime：成片/段片重渲后同刻同名，旧缓存会把上一版
// 画面（甚至上次失败渲染的黑帧）原样端给审核——2026-10-17/18 终审"全片纯黑"
// 两次误判皆因此（真成片有画面，判卷官吃的是隔夜缓存帧）。
func (fs *serviceFS) extractFrame(ctx context.Context, segID string, t float64, dur float64) (string, string, error) {
	if dur > 0 && (t < 0 || t > dur+0.05) {
		return "", "", fmt.Errorf("t=%.2f 越界（%s 段长 %.2fs）——改到 [0, %.2f] 内重取", t, segID, dur, dur)
	}
	if t < 0 {
		t = 0
	}
	src := fs.segVideo(segID)
	if src == "" {
		return "", "", fmt.Errorf("段片 %s 不存在（该段尚未渲染？）", segID)
	}
	fi, err := os.Stat(src)
	if err != nil {
		return "", "", fmt.Errorf("段片 %s 不可读: %w", segID, err)
	}
	key := fmt.Sprintf("%s-t%.2f-%d-%d", segID, round2(t), fi.Size(), fi.ModTime().UnixNano())
	rel := fs.relBase + "/frames/" + key + ".jpg"
	dst := filepath.Join(fs.cacheDir, "frames", key+".jpg")
	if b, err := os.ReadFile(dst); err == nil && len(b) > 0 {
		return jpegDataURL(b), rel, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", "", err
	}
	ff, err := pipeline.FFmpegPath()
	if err != nil {
		return "", "", err
	}
	cmd := exec.CommandContext(ctx, ff, "-y", "-v", "error", "-ss", strconv.FormatFloat(t, 'f', 3, 64),
		"-i", src, "-frames:v", "1", "-vf", "scale=960:-2", "-q:v", "5", dst)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		_ = os.Remove(dst)
		return "", "", fmt.Errorf("抽帧失败（%s t=%.2f）: %s", segID, t, pipeline.TailLines(buf.String(), 3))
	}
	b, err := os.ReadFile(dst)
	if err != nil {
		return "", "", err
	}
	return jpegDataURL(b), rel, nil
}

// extractClip 连续短片段：视频直喂优先，退路 6fps 带时间戳帧序列。跨度超上限
// 截断并在返回文本里说明。缓存键 seg+t1+t2。返回（观察文本, 附图）。
func (fs *serviceFS) extractClip(ctx context.Context, segID string, t1, t2, dur float64) (string, []string, error) {
	if t1 > t2 {
		t1, t2 = t2, t1
	}
	if t1 < 0 {
		t1 = 0
	}
	if dur > 0 && t2 > dur {
		t2 = dur
	}
	note := ""
	if t2-t1 > clipMaxSpan {
		t2 = t1 + clipMaxSpan
		note = fmt.Sprintf("（跨度超 %.1fs 上限，已截断到 t=%.2f——要看更晚的画面再取一次）", clipMaxSpan, t2)
	}
	// 缓存键带源文件 Size+mtime：段片重渲后同刻同名，不失效会把旧画面端给审核
	//（与 extractFrame 同病同修，2026-10-18）。
	src := fs.segVideo(segID)
	if src == "" {
		return "", nil, fmt.Errorf("段片 %s 不存在（该段尚未渲染？）", segID)
	}
	sfi, err := os.Stat(src)
	if err != nil {
		return "", nil, fmt.Errorf("段片 %s 不可读: %w", segID, err)
	}
	key := fmt.Sprintf("%s-c%.2f-%.2f-%d-%d", segID, round2(t1), round2(t2), sfi.Size(), sfi.ModTime().UnixNano())
	mp4 := filepath.Join(fs.cacheDir, "clips", key+".mp4")
	_ = os.MkdirAll(filepath.Dir(mp4), 0o755)
	if _, err := os.Stat(mp4); err != nil {
		ff, err := pipeline.FFmpegPath()
		if err != nil {
			return "", nil, err
		}
		tmp := mp4 + ".tmp.mp4"
		cmd := exec.CommandContext(ctx, ff, "-y", "-v", "error",
			"-ss", strconv.FormatFloat(t1, 'f', 3, 64), "-t", strconv.FormatFloat(t2-t1, 'f', 3, 64),
			"-i", src, "-an", "-c:v", "libx264", "-preset", "veryfast", "-crf", "26",
			"-vf", "scale=640:-2", "-movflags", "+faststart", tmp)
		var buf bytes.Buffer
		cmd.Stdout, cmd.Stderr = &buf, &buf
		if err := cmd.Run(); err != nil {
			_ = os.Remove(tmp)
			return "", nil, fmt.Errorf("取片段失败（%s %.2f-%.2f）: %s", segID, t1, t2, pipeline.TailLines(buf.String(), 3))
		}
		if err := os.Rename(tmp, mp4); err != nil {
			return "", nil, err
		}
	}
	summary := fmt.Sprintf("片段 %s t=%.2f–%.2f（%.2fs）已取回%s。", segID, t1, t2, t2-t1, note)
	if videoSupported(ctx, mp4) {
		if b, err := os.ReadFile(mp4); err == nil {
			url := "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(b)
			return summary + " 已按视频附上，逐帧看边界动画。", []string{url}, nil
		}
	}
	// 退路：带时间戳帧序列。帧率按跨度自适应（8帧封顶）：ReAct 每轮全量重发
	// 历史，取证一次塞 9-15 张图曾把后期单请求顶到 30-40 张图——端点逐张视觉
	// 编码，这是一次审核 5-10 分钟的主因。短窗口仍保持 6fps（设计退路口径）。
	fps := clipFallbackFPS
	if span := t2 - t1; span > 0 && span/float64(fps) > 8.0/clipFallbackFPS {
		fps = int(span*clipFallbackFPS/8.0 + 0.5)
		if fps < 3 {
			fps = 3
		}
	}
	var imgs []string
	var stamps []string
	for t := t1; t <= t2+1e-6; t += 1.0 / float64(fps) {
		u, _, err := fs.extractFrame(ctx, segID, t, dur)
		if err != nil {
			return summary + " 帧序列取回失败: " + err.Error(), nil, nil
		}
		imgs = append(imgs, u)
		stamps = append(stamps, fmt.Sprintf("图%d = t=%.2fs", len(imgs), t))
	}
	return summary + " 视频通道不可用，已退回帧序列：" + strings.Join(stamps, "、") + "。", imgs, nil
}

// ── 视频直喂能力探测（每进程一次；首个真片段产物作探针，端点拒收即永久走帧序列退路）──

var (
	vidMu      sync.Mutex
	vidSupport bool
	vidProbed  bool
)

// videoSupported 探测端点是否吃 mp4 dataURL。探针通道由 auditProvider 注入
// （videoProbeProv，同锁保护——包级全局无锁读写是数据竞争，2026-10-19 中-20）。
// 探测全程持锁：并发首调用只探一次（原先锁外探测会双探针白烧一次）；
// 探针请求失败（非法图片/不支持视频）按不可用缓存，本进程后续片段直接走帧序列。
func videoSupported(ctx context.Context, mp4 string) bool {
	vidMu.Lock()
	defer vidMu.Unlock()
	if vidProbed {
		return vidSupport
	}
	prov := videoProbeProv
	if prov == nil {
		return false // 探针通道未注入：按不可用处理，不缓存（注入后仍可探）
	}
	ok := false
	if b, err := os.ReadFile(mp4); err == nil && len(b) < 4<<20 {
		url := "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(b)
		msgs := []openai.ChatCompletionMessage{{
			Role: openai.ChatMessageRoleUser,
			MultiContent: []openai.ChatMessagePart{
				{Type: openai.ChatMessagePartTypeText, Text: "这是一个视频探针，能读到画面就只回复两个字：可读"},
				{Type: openai.ChatMessagePartTypeImageURL, ImageURL: &openai.ChatMessageImageURL{URL: url}},
			},
		}}
		if _, _, err := prov.ChatTemp(ctx, msgs, nil, 0.1); err == nil {
			ok = true
		}
	}
	vidSupport, vidProbed = ok, true
	return ok
}

// videoProbeProv 探针用的模型通道（agent.go 构造工具时注入；nil = 视为不可用）。
var videoProbeProv ChatClient

func jpegDataURL(b []byte) string {
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(b)
}
