package webfetch

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// allowAll 放开 SSRF 拦截（httptest 监听 127.0.0.1，默认被自家防线拦住），
// 返回还原函数，测试结束恢复防线。
func allowAll(t *testing.T) func() {
	t.Helper()
	old := ssrfGuard
	ssrfGuard = func(string) bool { return false }
	return func() { ssrfGuard = old }
}

func articleHandler(text string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><html><head><title>测试文章标题</title></head>
<body><article><h1>测试文章标题</h1><p>%s</p></article></body></html>`, text)
	}
}

func longText() string {
	return strings.Repeat("这是一段足够长的正文内容，用来通过正文最短长度校验，模拟真实文章段落。", 6)
}

func TestArticleExtracts(t *testing.T) {
	defer allowAll(t)()
	srv := httptest.NewServer(articleHandler(longText()))
	defer srv.Close()

	art, err := Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Article: %v", err)
	}
	if !strings.Contains(art.Title, "测试文章") {
		t.Errorf("标题 = %q，应含「测试文章」", art.Title)
	}
	if art.Words < 100 {
		t.Errorf("正文 %d 字，应 ≥100", art.Words)
	}
	if !strings.Contains(art.Content, "足够长的正文") {
		t.Errorf("正文应含段落文字，得到前 60 字: %.60s", art.Content)
	}
}

func TestArticleBlocksPrivateHost(t *testing.T) {
	// 不放开防线：本机地址必须被拦（默认 ssrfGuard 生效）
	for _, u := range []string{"http://localhost/x", "http://127.0.0.1/x", "http://192.168.1.1/x"} {
		if _, err := Fetch(context.Background(), u); !errors.Is(err, ErrBadURL) {
			t.Errorf("%s 应 ErrBadURL，得到 %v", u, err)
		}
	}
}

func TestArticleRejectsBadURL(t *testing.T) {
	defer allowAll(t)() // 只测地址解析，不发起网络请求
	for _, u := range []string{"ftp://example.com/a", "not a url", "http://", "file:///etc/hosts"} {
		if _, err := Fetch(context.Background(), u); !errors.Is(err, ErrBadURL) {
			t.Errorf("%q 应 ErrBadURL，得到 %v", u, err)
		}
	}
}

func TestArticleTooShort(t *testing.T) {
	defer allowAll(t)()
	srv := httptest.NewServer(articleHandler("太短"))
	defer srv.Close()

	_, err := Fetch(context.Background(), srv.URL)
	if !errors.Is(err, ErrUnreadable) {
		t.Fatalf("应 ErrUnreadable，得到 %v", err)
	}
}

func TestArticleTargetError(t *testing.T) {
	defer allowAll(t)()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := Fetch(context.Background(), srv.URL)
	if err == nil || errors.Is(err, ErrBadURL) || errors.Is(err, ErrUnreadable) {
		t.Fatalf("404 应归为网络/目标站错误，得到 %v", err)
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("错误信息应带状态码，得到 %q", err.Error())
	}
}

// stubDNS 伪造 DNS 解析（测防重绑定不需要真网）。
func stubDNS(t *testing.T, ips []net.IP) {
	t.Helper()
	old := lookupIP
	lookupIP = func(context.Context, string) ([]net.IP, error) { return ips, nil }
	t.Cleanup(func() { lookupIP = old })
}

// badHost 层：域名解析出内网 IP 必须拒绝（自建域名 A 记录指向内网的 SSRF）。
func TestBadHostResolvesInternal(t *testing.T) {
	stubDNS(t, []net.IP{net.ParseIP("1.2.3.4"), net.ParseIP("10.0.0.9")})
	if !badHost("rebind.example.com") {
		t.Fatal("解析结果含内网 IP 应判 bad")
	}
	stubDNS(t, []net.IP{net.ParseIP("1.2.3.4")})
	if badHost("rebind.example.com") {
		t.Fatal("全公网解析应放行（拦截交给拨号层）")
	}
	stubDNS(t, nil)
	lookupIP = func(context.Context, string) ([]net.IP, error) { return nil, errors.New("nx") }
	if !badHost("rebind.example.com") {
		t.Fatal("解析失败应一律拒绝")
	}
}

// guardedDial 层（SSRF 第二层，防 DNS 重绑定）：预检后 A 记录被换成内网 IP 时，
// 拨号时复验必须拒绝建连。
func TestGuardedDialBlocksRebind(t *testing.T) {
	stubDNS(t, []net.IP{net.ParseIP("10.0.0.9")})
	if _, err := guardedDial(context.Background(), "tcp", "rebind.example.com:80"); err == nil {
		t.Fatal("拨号解析到内网 IP 应拒绝建连")
	}
	// 字面量 IP 不走拨号层复验（请求前 badHost 已拦内网字面量）：这里只验证路径不炸
	if _, err := guardedDial(context.Background(), "tcp", "127.0.0.1:1"); err != nil && !strings.Contains(err.Error(), "不允许") {
		t.Logf("字面量回环直连失败属预期（本测只验证不复检路径）: %v", err)
	}
}
