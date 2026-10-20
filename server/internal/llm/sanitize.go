// sanitize.go —— 出网载荷里的图片校验（2026-10-20 成本诊断 P0-3）。
//
// 诊断期实锤：`HTTP 400: messages[N].content[M].image_url格式错误` 反复拉起审核
// 重跑，一次审核 5–10 分钟的账全打水漂。病根是坏图（空串/截断 data URL/非 data
// URL 的本地路径）混进注入清单，端点整包拒收。修法 = 发送前逐张验身，坏的拦在
// 门口并留痕（长度+指纹前 8 位），好的照发——一条坏图不再有资格报废一整审。
package llm

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"log"
	"strings"

	openai "github.com/sashabaranov/go-openai"
)

// maxImagePayload 单张图载荷上限（base64 字符数）。渲染截图 480 宽 JPEG ≈ 100–300KB，
// 全屏 PNG ≈ 1–3MB；20MB 起就是装配事故（串错文件/循环拼接），发出去必 400。
const maxImagePayload = 20 << 20

// ValidImageData 判断一个 data URL 是否可能被端点接受：
// data:image/ 前缀 + ;base64, + 非空且解码得动的载荷（抽头解码，代价 O(1)）。
func ValidImageData(u string) bool {
	if u == "" || len(u) > maxImagePayload+256 {
		return false
	}
	rest, ok := strings.CutPrefix(u, "data:image/")
	if !ok {
		return false
	}
	i := strings.Index(rest, ";base64,")
	if i < 0 {
		return false
	}
	payload := rest[i+len(";base64,"):]
	if len(payload) < 4 {
		return false
	}
	head := payload
	if len(head) > 512 {
		head = head[:512]
	}
	head = head[:len(head)-len(head)%4] // StdEncoding 要 4 的倍数
	if len(head) == 0 {
		return false
	}
	b, err := base64.StdEncoding.DecodeString(head)
	return err == nil && len(b) > 0
}

// sanitizeImageURLs 过滤图片清单：坏的拦下并打日志（含长度与指纹，好对照源头）。
func sanitizeImageURLs(urls []string) []string {
	out := make([]string, 0, len(urls))
	for _, u := range urls {
		if ValidImageData(u) {
			out = append(out, u)
			continue
		}
		logImageDrop(u, "generate 附图清单")
	}
	return out
}

// sanitizeChatMsg 消息级过滤：MultiContent 里的非法 image_url 块换成文字占位。
// 不动调用方的 slice（组包前拷贝），全图被拦时留一行文字让消息结构仍合法。
func sanitizeChatMsg(m openai.ChatCompletionMessage) openai.ChatCompletionMessage {
	if m.MultiContent == nil {
		return m
	}
	hasBad := false
	for _, p := range m.MultiContent {
		if p.Type == openai.ChatMessagePartTypeImageURL && (p.ImageURL == nil || !ValidImageData(p.ImageURL.URL)) {
			hasBad = true
			break
		}
	}
	if !hasBad {
		return m
	}
	parts := make([]openai.ChatMessagePart, 0, len(m.MultiContent))
	for _, p := range m.MultiContent {
		if p.Type == openai.ChatMessagePartTypeImageURL && (p.ImageURL == nil || !ValidImageData(p.ImageURL.URL)) {
			if p.ImageURL != nil {
				logImageDrop(p.ImageURL.URL, "对话消息")
			}
			continue
		}
		parts = append(parts, p)
	}
	if len(parts) == 0 {
		parts = append(parts, openai.ChatMessagePart{
			Type: openai.ChatMessagePartTypeText, Text: "（系统：本条附图载荷全部非法，已被发送前校验拦截。）",
		})
	}
	m.MultiContent = parts
	return m
}

func logImageDrop(u, where string) {
	head := u
	if len(head) > 24 {
		head = head[:24]
	}
	sum := sha256.Sum256([]byte(u))
	log.Printf("[llm] 拦截非法图片载荷（%s）: len=%d head=%q sha=%s", where, len(u), head, hex.EncodeToString(sum[:4]))
}
