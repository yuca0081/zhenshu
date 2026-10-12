package styleref

// 视频直读（解析增强，2026-10-08）：解析时把参考视频压成 480p 无声剪辑，签发
// 短时效签名 URL 供视觉模型拉取直看——动效/运镜/转场/节奏是静止帧看不出的时间
// 维度信息（motion 字段此前只能靠帧"推断"）。构图/配色仍走帧与测得色板（确定性高）。
//
// 三道防线：
//  1. PUBLIC_BASE_URL 未配 → 功能整体关闭（videoURLFor 返回 ""，解析退回纯抽帧）；
//  2. 剪辑/签名任一步失败 → 调用方拿 "" 降级纯帧（视频是增强项，不挡解析）；
//  3. 临时接口只认 clip-*.mp4 白名单 + HMAC 签名 + 有效期——不暴露原片与其他文件。

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"webpage-video-agent/server/internal/pipeline"
)

// llmClipCapS 全片剪辑时长上限：动效语言取前 5 分钟足够代表，控制模型侧拉取与 token 成本。
const llmClipCapS = 300.0

// llmClipTTL 签名有效期：覆盖一次解析全程（多段合集含逐段 LLM 调用，可能跑几十分钟）+ 重试余量。
const llmClipTTL = 6 * time.Hour

var (
	refIDRe    = regexp.MustCompile(`^ref[A-Za-z0-9_-]*$`)
	clipNameRe = regexp.MustCompile(`^clip-[A-Za-z0-9_-]+\.mp4$`)
)

// clipArgs ffmpeg 剪辑参数（纯函数，测试钉行为）：480p 无声 + faststart（moov 前置，
// 模型侧边拉边播）。段级裁 [start,end]；end<=0 表示全片；超过上限截断为前 llmClipCapS 秒。
func clipArgs(video, out string, start, end float64) []string {
	dur := end - start
	if end <= 0 || dur > llmClipCapS {
		dur = llmClipCapS
	}
	if dur <= 0 { // 异常入参兜底：至少给一段
		dur = llmClipCapS
	}
	return []string{"-y", "-ss", fmt.Sprintf("%.2f", start), "-i", video,
		"-t", fmt.Sprintf("%.2f", dur),
		"-vf", "scale=-2:480", "-c:v", "libx264", "-preset", "veryfast", "-crf", "26",
		"-an", "-movflags", "+faststart", out}
}

// ensureClip 剪辑落盘（缓存复用：同引用重试不重压）。临时文件原子改名，防中途
// 死进程留下半截坏文件被 Stat 命中当缓存。
func (s *Store) ensureClip(ctx context.Context, video, refDir, tag string, start, end float64) (string, error) {
	name := "clip-" + tag + ".mp4"
	out := filepath.Join(refDir, name)
	if _, err := os.Stat(out); err == nil {
		return name, nil
	}
	ff, err := pipeline.FFmpegPath()
	if err != nil {
		return "", err
	}
	tmp := out + ".tmp"
	// pipeline.CommandContext：取消时整棵进程树击杀，ffmpeg 孙进程不残留
	if out2, err := pipeline.CommandContext(ctx, ff, clipArgs(video, tmp, start, end)...).CombinedOutput(); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("剪辑失败: %w\n%s", err, tail(string(out2), 4))
	}
	if err := os.Rename(tmp, out); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return name, nil
}

// videoURLFor 解析侧一站式：剪辑 → 签名 URL。任何一步不成（含未配 PUBLIC_BASE_URL）
// 返回 ""——调用方据此降级纯帧。
func (s *Store) videoURLFor(ctx context.Context, refID, video string, start, end float64, tag string) string {
	if strings.TrimSpace(os.Getenv("PUBLIC_BASE_URL")) == "" {
		return ""
	}
	name, err := s.ensureClip(ctx, video, s.dir(refID), tag, start, end)
	if err != nil {
		return ""
	}
	u, err := s.SignVideoURL(refID, name, llmClipTTL)
	if err != nil {
		return ""
	}
	return u
}

// SignVideoURL 公网直读 URL。PUBLIC_BASE_URL 未配返回 ""（功能关闭，不是错误）。
func (s *Store) SignVideoURL(refID, clipName string, ttl time.Duration) (string, error) {
	base := strings.TrimSpace(os.Getenv("PUBLIC_BASE_URL"))
	if base == "" {
		return "", nil
	}
	secret, err := s.videoSecret()
	if err != nil {
		return "", err
	}
	exp := time.Now().Add(ttl).Unix()
	return fmt.Sprintf("%s/api/llm-video/%s/%s?exp=%d&sig=%s",
		strings.TrimSuffix(base, "/"), url.PathEscape(refID), url.PathEscape(clipName), exp,
		videoSig(secret, refID, clipName, exp)), nil
}

// videoSig HMAC-SHA256(refID|name|exp)：链接与具体文件绑定，改一个字符即失效。
func videoSig(secret []byte, refID, name string, exp int64) string {
	h := hmac.New(sha256.New, secret)
	fmt.Fprintf(h, "%s|%s|%d", refID, name, exp)
	return hex.EncodeToString(h.Sum(nil))
}

// VerifyVideoSig 验签（api 层调用）：签名一致且未过期。ConstantTimeCompare 防时序侧信道。
func VerifyVideoSig(secret []byte, refID, name string, exp int64, sig string) bool {
	if exp < time.Now().Unix() {
		return false
	}
	return hmac.Equal([]byte(videoSig(secret, refID, name, exp)), []byte(sig))
}

// VerifyVideo 临时接口验签入口：密钥读不出一律拒绝。
func (s *Store) VerifyVideo(id, name string, exp int64, sig string) bool {
	secret, err := s.videoSecret()
	if err != nil {
		return false
	}
	return VerifyVideoSig(secret, id, name, exp, sig)
}

// VideoFile 临时接口取文件路径：id/name 白名单校验（防目录穿越）+ 存在性检查。
// 只放行引用目录下的 clip-*.mp4 剪辑——原片 video.* 永不出这个口子。
func (s *Store) VideoFile(id, name string) (string, bool) {
	if !refIDRe.MatchString(id) || !clipNameRe.MatchString(name) {
		return "", false
	}
	p := filepath.Join(s.dir(id), name)
	if _, err := os.Stat(p); err != nil {
		return "", false
	}
	return p, true
}

// videoSecret 签名密钥（data/llm_video_secret.key，首次生成落盘）。落盘而非内存随机：
// 重启后旧链接仍可验签，跑到一半的解析不因进程重启集体失效。mu 串行化首次生成，
// 防两个解析并发各写各的把对方签好的链接作废。
func (s *Store) videoSecret() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.DataDir, "llm_video_secret.key")
	if b, err := os.ReadFile(path); err == nil {
		if v := strings.TrimSpace(string(b)); len(v) >= 32 {
			return []byte(v), nil
		}
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	v := hex.EncodeToString(raw)
	if err := os.WriteFile(path, []byte(v), 0o600); err != nil {
		return nil, err
	}
	return []byte(v), nil
}
