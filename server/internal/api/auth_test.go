package api

// 用户会话与项目隔离的功能测试：走真实 Router()（含 requireUser/projectGuard 全链路），
// 钉住「登录发 cookie、归属过滤、机器 token=admin、开放模式旁路」四个契约。

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"webpage-video-agent/server/internal/agent"
	"webpage-video-agent/server/internal/auth"
	"webpage-video-agent/server/internal/store"
)

func newAuthServer(t *testing.T) (*Server, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	s, _ := newTestServer(t)
	s.SessionSecret = []byte("test-secret")
	s.AuthActive = true
	// projectView 等依赖 Agent.State（只读 DataDir 下的产物文件），零值 Agent 即可
	s.Agent = &agent.Agent{DataDir: s.DataDir}
	return s, s.Router()
}

// mkUser 在 store 里建账号并返回 User（密码明文随返回，便于登录）。
func mkUser(t *testing.T, s *Server, username, password, role string) *store.User {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	u := &store.User{Username: username, Role: role, PasswordHash: hash}
	if err := s.Store.CreateUser(u); err != nil {
		t.Fatal(err)
	}
	return u
}

// login 拿会话 cookie（"Cookie: zs_session=…" 头值）。
func loginCookie(t *testing.T, r *gin.Engine, username, password string) string {
	t.Helper()
	w := doJSON(t, r, "POST", "/api/auth/login", map[string]string{"username": username, "password": password})
	if w.Code != 200 {
		t.Fatalf("登录 %s 失败: %d %s", username, w.Code, w.Body.String())
	}
	ck := ""
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			ck = sessionCookie + "=" + c.Value
		}
	}
	if ck == "" {
		t.Fatalf("登录 %s 未发会话 cookie", username)
	}
	return ck
}

// mkOwnedProject 落一个带归属的磁盘项目（project.json 为归属事实源）。
func mkOwnedProject(t *testing.T, dataDir, id, userID string) {
	t.Helper()
	dir := filepath.Join(dataDir, "projects", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(map[string]any{"id": id, "name": id, "status": "created", "user_id": userID})
	if err := os.WriteFile(filepath.Join(dir, "project.json"), meta, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAuthStatusActiveVsOpen(t *testing.T) {
	s, r := newAuthServer(t)
	w := doJSON(t, r, "GET", "/api/auth/status", nil)
	var out struct {
		AuthRequired bool            `json:"auth_required"`
		User         *map[string]any `json:"user"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.AuthRequired || out.User != nil {
		t.Fatalf("鉴权模式应 auth_required=true 且无用户: %s", w.Body.String())
	}

	// 开放模式：不需要登录，且返回本机全权用户
	s.AuthActive = false
	w = doJSON(t, s.Router(), "GET", "/api/auth/status", nil)
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.AuthRequired || out.User == nil {
		t.Fatalf("开放模式应 auth_required=false 且带本机用户: %s", w.Body.String())
	}
}

func TestLoginWrongPasswordRejected(t *testing.T) {
	s, r := newAuthServer(t)
	mkUser(t, s, "alice", "pass123", "user")

	w := doJSON(t, r, "POST", "/api/auth/login", map[string]string{"username": "alice", "password": "nope"})
	if w.Code != 401 {
		t.Fatalf("错误密码应 401: %d", w.Code)
	}
	// 未知用户与错误密码同口径 401（不暴露账号存在性）
	w = doJSON(t, r, "POST", "/api/auth/login", map[string]string{"username": "nobody", "password": "nope"})
	if w.Code != 401 {
		t.Fatalf("未知用户应同口径 401: %d", w.Code)
	}
}

func TestProjectIsolation(t *testing.T) {
	s, r := newAuthServer(t)
	admin := mkUser(t, s, "admin", "adminpw", "admin")
	alice := mkUser(t, s, "alice", "alicepw", "user")

	mkOwnedProject(t, s.DataDir, "p-alice", alice.ID) // alice 自己的
	mkOwnedProject(t, s.DataDir, "p-admin", admin.ID) // admin 的
	mkOwnedProject(t, s.DataDir, "p-legacy", "")      // 旧项目：无归属 = 系统项目

	aliceCK := loginCookie(t, r, "alice", "alicepw")
	adminCK := loginCookie(t, r, "admin", "adminpw")
	get := func(ck, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		if ck != "" {
			req.Header.Set("Cookie", ck)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	// alice：只见自己的
	w := get(aliceCK, "/api/projects")
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("列表不是 JSON 数组: %s", w.Body.String())
	}
	if len(rows) != 1 || rows[0]["id"] != "p-alice" {
		t.Fatalf("alice 应只见 p-alice: %s", w.Body.String())
	}
	// 别人的与无主的：404（不暴露存在性）
	for _, id := range []string{"p-admin", "p-legacy", "p-none"} {
		if w := get(aliceCK, "/api/projects/"+id); w.Code != 404 {
			t.Fatalf("alice 访问 %s 应 404，实际 %d", id, w.Code)
		}
	}
	// admin：全可见
	w = get(adminCK, "/api/projects")
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("admin 应见全部 3 个项目: %s", w.Body.String())
	}
	if w := get(adminCK, "/api/projects/p-legacy"); w.Code != 200 {
		t.Fatalf("admin 访问无主项目应 200: %d", w.Code)
	}
	// 未登录：401
	if w := get("", "/api/projects"); w.Code != 401 {
		t.Fatalf("未登录应 401: %d", w.Code)
	}
}

func TestCreateProjectStampsOwner(t *testing.T) {
	s, r := newAuthServer(t)
	alice := mkUser(t, s, "alice", "alicepw", "user")
	aliceCK := loginCookie(t, r, "alice", "alicepw")

	req := httptest.NewRequest("POST", "/api/projects", strings.NewReader(`{"name":"甲","topic":"测试主题"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", aliceCK)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("建项目失败: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(s.DataDir, "projects", created.ID, "project.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	if err := json.Unmarshal(b, &meta); err != nil {
		t.Fatal(err)
	}
	if meta["user_id"] != alice.ID {
		t.Fatalf("project.json 应落 alice 的 uid: %v", meta["user_id"])
	}
	if _, err := s.Store.UserByName("alice"); err != nil { // DB 行可建即（store 侧归属走 CreateProject 列）
		t.Fatal(err)
	}
}

func TestMachineTokenIsAdmin(t *testing.T) {
	s, _ := newAuthServer(t)
	s.APIToken = "mach3ne-secret"
	r := s.Router()
	mkUser(t, s, "admin", "adminpw", "admin")
	mkOwnedProject(t, s.DataDir, "p-legacy", "") // 无主项目仅管理员可见——机器 token 同权

	// 无凭据 401；机器 token 放行且等同 admin（可见无主项目）
	if w := doJSON(t, r, "GET", "/api/projects", nil); w.Code != 401 {
		t.Fatalf("无凭据应 401: %d", w.Code)
	}
	req := httptest.NewRequest("GET", "/api/projects", nil)
	req.Header.Set("X-API-Token", "mach3ne-secret")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("%s", w.Body.String())
	}
	if len(rows) != 1 || rows[0]["id"] != "p-legacy" {
		t.Fatalf("机器 token 应以 admin 身份看到无主项目: %s", w.Body.String())
	}
}

func TestUserAdminEndpoints(t *testing.T) {
	s, r := newAuthServer(t)
	mkUser(t, s, "admin", "adminpw", "admin")
	mkUser(t, s, "alice", "alicepw", "user")
	adminCK := loginCookie(t, r, "admin", "adminpw")
	aliceCK := loginCookie(t, r, "alice", "alicepw")

	reqWith := func(ck, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if ck != "" {
			req.Header.Set("Cookie", ck)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	// 普通用户 403；管理员可建可列
	if w := reqWith(aliceCK, "GET", "/api/auth/users", ""); w.Code != 403 {
		t.Fatalf("alice 列用户应 403: %d", w.Code)
	}
	w := reqWith(adminCK, "POST", "/api/auth/users", `{"username":"bob","password":"bobpw123","role":"user"}`)
	if w.Code != 200 {
		t.Fatalf("建账号失败: %d %s", w.Code, w.Body.String())
	}
	if w := reqWith(adminCK, "POST", "/api/auth/users", `{"username":"bob","password":"bobpw123"}`); w.Code != 409 {
		t.Fatalf("重名应 409: %d", w.Code)
	}
	if w := reqWith(adminCK, "GET", "/api/auth/users", ""); w.Code != 200 {
		t.Fatalf("列用户失败: %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "PasswordHash") || strings.Contains(w.Body.String(), "password_hash") {
		t.Fatalf("用户列表不得泄漏哈希: %s", w.Body.String())
	}
	// 新账号能登录（管理员预建→用户可用的闭环）
	if ck := loginCookie(t, r, "bob", "bobpw123"); ck == "" {
		t.Fatal("新建账号应能登录")
	}
}
