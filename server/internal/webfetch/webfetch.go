// Package webfetch：抓取公开网页并抽正文（readability）。
// 两处共用：API「从链接导入」与 Agent 的 fetch_url 工具。
// 直接 HTTP 抓取，不走智谱 MCP——不占套餐搜索额度；代价是不能执行 JS，
// 重前端页面抽不到正文（调用方按 ErrUnreadable 让用户换个方式）。
package webfetch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-shiori/go-readability"
)

// Article 一篇网页的正文。
type Article struct {
	Title   string
	Content string
	Words   int // rune 数
}

// 错误分类：ErrBadURL=地址不行（API 映射 400），ErrUnreadable=页面读不出正文（映射 422），
// 其余为网络/目标站问题（映射 502）。
var (
	ErrBadURL     = errors.New("地址不允许")
	ErrUnreadable = errors.New("正文提取失败")
)

// Error 带分类的抓取错误：Class 供调用方映射 HTTP 状态码，Msg 是给人看的。
type Error struct {
	Class error
	Msg   string
}

func (e *Error) Error() string { return e.Msg }
func (e *Error) Unwrap() error { return e.Class }

func fail(class error, format string, a ...any) *Error {
	return &Error{Class: class, Msg: fmt.Sprintf(format, a...)}
}

// ssrfGuard 包级变量：Article 调它做地址安检。单独拎出来是给测试注入直通用的——
// httptest 监听 127.0.0.1，天然落在封锁名单里，不解锁就没法离线测抓取。
var ssrfGuard = badHost

// Article 抓取 rawURL 并抽取正文。
func Fetch(ctx context.Context, rawURL string) (*Article, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fail(ErrBadURL, "只支持 http/https 链接")
	}
	if ssrfGuard(u.Hostname()) {
		return nil, fail(ErrBadURL, "不允许的地址")
	}
	client := &http.Client{
		Timeout: 20 * time.Second,
		// 逐跳校验重定向目标：默认客户端会跟满 10 跳且不复查，公网 302 → 127.0.0.1 即绕过
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("重定向过多")
			}
			if ssrfGuard(req.URL.Hostname()) {
				return fmt.Errorf("重定向到不允许的地址")
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, fail(ErrBadURL, "%v", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; ZhenshuBot/1.0; +link-import)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fail(nil, "抓取失败：%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fail(nil, "目标站返回 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fail(nil, "读取失败：%v", err)
	}
	art, err := readability.FromReader(bytes.NewReader(body), u) // bytes 零拷贝，8MB body 免多复制一遍
	if err != nil {
		return nil, fail(ErrUnreadable, "正文提取失败（该页面可能不是文章页）")
	}
	content := strings.TrimSpace(art.TextContent)
	if n := len([]rune(content)); n < 100 {
		return nil, fail(ErrUnreadable, "提取到的正文太短（%d 字，可能不是文章页）", n)
	}
	return &Article{Title: strings.TrimSpace(art.Title), Content: content, Words: len([]rune(content))}, nil
}

// badHost SSRF 防线：字面量检查 + DNS 解析复核。只查字面量拦不住自建域名 A 记录指向
// 内网（云 metadata 169.254.169.254 / host 网络下的宿主回环服务）——必须解析后再验。
func badHost(host string) bool {
	if host == "" || strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return true // 解析失败一律拒绝（防靠 NXDOMAIN 行为差异绕过）
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
			return true
		}
	}
	return false
}
