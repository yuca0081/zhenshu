package api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCookieSecureProxyAware 会话 cookie 的 Secure 判定：默认只认直连 TLS 与反代
// X-Forwarded-Proto=https（Docker + TLS 反代终止的标准部署下 Go 侧看不到 TLS，
// 恒 false 会让 30 天长效 cookie 走明文链路）；COOKIE_SECURE 强制时无条件置位。
func TestCookieSecureProxyAware(t *testing.T) {
	s, r := newAuthServer(t)
	mkUser(t, s, "alice", "alicepw", "user")

	login := func(headers map[string]string) bool {
		req := httptest.NewRequest("POST", "/api/auth/login",
			strings.NewReader(`{"username":"alice","password":"alicepw"}`))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("login failed: %d %s", w.Code, w.Body.String())
		}
		for _, c := range w.Result().Cookies() {
			if c.Name == sessionCookie {
				return c.Secure
			}
		}
		t.Fatal("no session cookie issued")
		return false
	}

	// 明文直连且无转发头：Secure 关（本机开发模式不受影响）
	if login(nil) {
		t.Fatal("plain http login should not set Secure")
	}
	// 反代告知 https：Secure 开
	if !login(map[string]string{"X-Forwarded-Proto": "https"}) {
		t.Fatal("X-Forwarded-Proto: https should set Secure")
	}
	// COOKIE_SECURE 强制：无转发头也置位
	s.CookieSecure = true
	if !login(nil) {
		t.Fatal("forced CookieSecure should set Secure without forwarded proto")
	}
}
