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

// lookupIP DNS 解析（包级变量供测试注入：伪造「域名解析到内网 IP」的重绑定场景）。
var lookupIP = func(ctx context.Context, host string) ([]net.IP, error) {
	return net.LookupIP(host)
}

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
		// 拨号层复验（防 DNS 重绑定，见 guardedDial）；Proxy/HTTP2 与默认 Transport 同口径
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           guardedDial,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
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

// badHost SSRF 防线（第一层，请求前）：字面量检查 + DNS 解析复核。只查字面量拦不住
// 自建域名 A 记录指向内网（云 metadata 169.254.169.254 / host 网络下的宿主回环服务）
// ——必须解析后再验。第二层在拨号时（guardedDial）：两次解析之间 A 记录可被换成
// 内网 IP（DNS 重绑定），拨号层以「真拿来建连的 IP」为准再过一遍黑名单。
func badHost(host string) bool {
	if host == "" || strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ipBanned(ip)
	}
	ips, err := lookupIP(context.Background(), host)
	if err != nil {
		return true // 解析失败一律拒绝（防靠 NXDOMAIN 行为差异绕过）
	}
	for _, ip := range ips {
		if ipBanned(ip) {
			return true
		}
	}
	return false
}

// ipBanned 单个 IP 是否落在封锁名单：回环/内网/链路本地/未指定/组播。
func ipBanned(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

// guardedDial Transport 拨号钩子（SSRF 第二层，防 DNS 重绑定）：addr 是主机名时先
// 解析出 IP、逐个过黑名单，任一命中即拒绝；通过后只对验证过的 IP 建连——拨号与
// 建连用的是同一次解析结果，重绑定窗口被关死。字面量 IP 不经这里再解析（badHost
// 已把内网字面量拦在请求前）。TLS 的 SNI/证书校验不受影响：Transport 按 URL host
// 设 ServerName，与底层连到哪个 IP 无关。
func guardedDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if net.ParseIP(host) == nil {
		ips, err := lookupIP(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("拨号解析 %s 失败: %w", host, err)
		}
		chosen := ""
		for _, ip := range ips {
			if !ipBanned(ip) {
				chosen = ip.String()
				break
			}
		}
		if chosen == "" {
			return nil, fmt.Errorf("%s 解析到不允许的地址，拒绝建连", host)
		}
		addr = net.JoinHostPort(chosen, port)
	}
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}
