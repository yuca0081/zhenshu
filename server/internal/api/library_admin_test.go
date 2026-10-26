package api

import (
	"net/http/httptest"
	"testing"
)

// TestGlobalAssetWritesRequireAdmin 全局资产（风格库/参考库）写操作的管理员双守卫：
// 普通用户可读（GET /api/library 照旧）但删库/发布/上传参考必须是 admin——多用户
// 部署下一个普通账号不该能清空全用户共享的库（项目归属守卫只覆盖 projects/:id）。
func TestGlobalAssetWritesRequireAdmin(t *testing.T) {
	s, r := newAuthServer(t)
	mkUser(t, s, "admin", "adminpw", "admin")
	mkUser(t, s, "alice", "alicepw", "user")
	adminCK := loginCookie(t, r, "admin", "adminpw")
	aliceCK := loginCookie(t, r, "alice", "alicepw")

	withCookie := func(ck, method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Cookie", ck)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	// 读路径：普通用户照旧
	if w := withCookie(aliceCK, "GET", "/api/library"); w.Code != 200 {
		t.Fatalf("plain user should read library, got %d %s", w.Code, w.Body.String())
	}
	// 写路径：普通用户一律 403
	for _, tc := range [][2]string{
		{"DELETE", "/api/library/xyz"},
		{"POST", "/api/library/xyz/publish"},
		{"POST", "/api/references"},
		{"POST", "/api/references/xyz/analyze"},
		{"POST", "/api/references/xyz/confirm"},
		{"PUT", "/api/references/xyz/draft"},
	} {
		if w := withCookie(aliceCK, tc[0], tc[1]); w.Code != 403 {
			t.Fatalf("plain user %s %s: want 403, got %d %s", tc[0], tc[1], w.Code, w.Body.String())
		}
	}
	// 管理员放行到 handler 本体（条目不存在 → 非 403 的业务错误码即可）
	if w := withCookie(adminCK, "DELETE", "/api/library/xyz"); w.Code == 403 {
		t.Fatalf("admin delete should pass the gate, got 403: %s", w.Body.String())
	}
}
