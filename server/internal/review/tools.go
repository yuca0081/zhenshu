package review

// tools.go —— 审核取证工具（§6.2 两读一交）：get_frame 指定时刻单帧、
// get_clip 连续短片段。取帧结果按（段+时刻）缓存——修复轮/重放不重取；
// 取证调用留痕由 Loop.OnTool 交给调用方落盘。
// 帧压缩到宽 480 的 JPEG dataURL（与准备工位缩图同参：检查分辨率足够，
// 整图 base64 会让多轮成本滚雪球）。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"image"
	"image/jpeg"
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
	cacheDir string                    // reports/audit
}

func hash8(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

// extractFrame 抽指定时刻单帧（ffmpeg，缓存键 seg+t 两位小数）。返回 JPEG dataURL。
// 越界（负值或超段长 0.05s 容差）报错回流，由模型改参数重取。
func (fs *serviceFS) extractFrame(ctx context.Context, segID string, t float64, dur float64) (string, error) {
	if dur > 0 && (t < 0 || t > dur+0.05) {
		return "", fmt.Errorf("t=%.2f 越界（%s 段长 %.2fs）——改到 [0, %.2f] 内重取", t, segID, dur, dur)
	}
	if t < 0 {
		t = 0
	}
	key := fmt.Sprintf("%s-t%.2f", segID, round2(t))
	dst := filepath.Join(fs.cacheDir, "frames", key+".jpg")
	if b, err := os.ReadFile(dst); err == nil && len(b) > 0 {
		return jpegDataURL(b), nil
	}
	src := fs.segVideo(segID)
	if src == "" {
		return "", fmt.Errorf("段片 %s 不存在（该段尚未渲染？）", segID)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	ff, err := pipeline.FFmpegPath()
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, ff, "-y", "-v", "error", "-ss", strconv.FormatFloat(t, 'f', 3, 64),
		"-i", src, "-frames:v", "1", "-vf", "scale=480:-2", "-q:v", "5", dst)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		_ = os.Remove(dst)
		return "", fmt.Errorf("抽帧失败（%s t=%.2f）: %s", segID, t, pipeline.TailLines(buf.String(), 3))
	}
	b, err := os.ReadFile(dst)
	if err != nil {
		return "", err
	}
	return jpegDataURL(b), nil
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
	key := fmt.Sprintf("%s-c%.2f-%.2f", segID, round2(t1), round2(t2))
	mp4 := filepath.Join(fs.cacheDir, "clips", key+".mp4")
	_ = os.MkdirAll(filepath.Dir(mp4), 0o755)
	if _, err := os.Stat(mp4); err != nil {
		src := fs.segVideo(segID)
		if src == "" {
			return "", nil, fmt.Errorf("段片 %s 不存在（该段尚未渲染？）", segID)
		}
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
		u, err := fs.extractFrame(ctx, segID, t, dur)
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

// videoSupported 探测端点是否吃 mp4 dataURL。prov 是当前会话的模型通道；
// 探针请求失败（非法图片/不支持视频）按不可用缓存，本进程后续片段直接走帧序列。
func videoSupported(ctx context.Context, mp4 string) bool {
	vidMu.Lock()
	if vidProbed {
		ok := vidSupport
		vidMu.Unlock()
		return ok
	}
	vidMu.Unlock()
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
		if _, _, err := videoProbeProv.ChatTemp(ctx, msgs, nil, 0.1); err == nil {
			ok = true
		}
	}
	vidMu.Lock()
	vidSupport, vidProbed = ok, true
	vidMu.Unlock()
	return ok
}

// videoProbeProv 探针用的模型通道（agent.go 构造工具时注入；nil = 视为不可用）。
var videoProbeProv ChatClient

func jpegDataURL(b []byte) string {
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(b)
}

// shrinkImage 任意 PNG/JPG → 宽 480 JPEG dataURL（开场注入的锚点帧用；
// 与 prep.shrinkFrame 同参，独立实现避免包依赖）。
func shrinkImage(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	src, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	tw, th := 480, 480*h/w
	if tw > w {
		tw, th = w, h
	}
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	for y := 0; y < th; y++ {
		sy := bounds.Min.Y + y*h/th
		for x := 0; x < tw; x++ {
			dst.Set(x, y, src.At(bounds.Min.X+x*w/tw, sy))
		}
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 70}); err != nil {
		return "", err
	}
	return jpegDataURL(out.Bytes()), nil
}
