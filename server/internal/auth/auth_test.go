package auth

import (
	"strings"
	"testing"
	"time"

	"webpage-video-agent/server/internal/store"
)

func TestPasswordHashRoundtrip(t *testing.T) {
	h, err := HashPassword("s3cret-密码")
	if err != nil {
		t.Fatal(err)
	}
	if h == "s3cret-密码" || !strings.HasPrefix(h, "$2") {
		t.Fatalf("应输出 bcrypt 哈希，实际 %q", h)
	}
	if !CheckPassword(h, "s3cret-密码") {
		t.Fatal("正确密码应通过")
	}
	if CheckPassword(h, "wrong") {
		t.Fatal("错误密码不应通过")
	}
	if CheckPassword("", "x") { // 空 hash 恒不匹配，不 panic
		t.Fatal("空 hash 不应通过")
	}
}

func TestSessionSignVerify(t *testing.T) {
	secret := []byte("test-secret")
	cl := Claims{UID: "u-1", Username: "alice", Role: "user", Exp: time.Now().Add(time.Hour).Unix()}
	val := SignSession(secret, cl)

	got, err := VerifySession(secret, val, time.Now())
	if err != nil || got.UID != cl.UID || got.Username != cl.Username || got.Role != cl.Role {
		t.Fatalf("roundtrip 失败: %v %+v", err, got)
	}

	// 篡改载荷 / 错误密钥 / 过期，一律拒绝
	if _, err := VerifySession([]byte("other-key"), val, time.Now()); err == nil {
		t.Fatal("错误密钥应拒绝")
	}
	tampered := strings.SplitN(val, ".", 2)[0] + ".deadbeef"
	if _, err := VerifySession(secret, tampered, time.Now()); err == nil {
		t.Fatal("篡改签名应拒绝")
	}
	expired := Claims{UID: "u-1", Username: "alice", Role: "user", Exp: time.Now().Add(-time.Minute).Unix()}
	if _, err := VerifySession(secret, SignSession(secret, expired), time.Now()); err == nil {
		t.Fatal("过期会话应拒绝")
	}
}

func TestDeriveSecret(t *testing.T) {
	// SESSION_SECRET 优先且原样使用
	s1, active := DeriveSecret("abc", "tok")
	if !active || string(s1) != "abc" {
		t.Fatalf("SESSION_SECRET 应优先: %v %v", active, s1)
	}
	// API_TOKEN 派生：确定性（重启会话不失效）且激活
	a1, active := DeriveSecret("", "tok")
	a2, _ := DeriveSecret("", "tok")
	if !active || len(a1) == 0 || string(a1) != string(a2) {
		t.Fatal("API_TOKEN 派生应确定且激活")
	}
	// 都未配置 = 开放模式：随机密钥（非 nil 非确定）
	r1, active := DeriveSecret("", "")
	r2, _ := DeriveSecret("", "")
	if active {
		t.Fatal("都未配置应是非激活（开放模式）")
	}
	if string(r1) == string(r2) {
		t.Fatal("随机密钥两次不应相同")
	}
}

func TestEnsureBootstrapAdmin(t *testing.T) {
	st, _ := store.Open("")
	created, err := EnsureBootstrapAdmin(st, "root", "pass123")
	if err != nil || !created {
		t.Fatalf("空表应建管理员: %v %v", created, err)
	}
	u, err := st.UserByName("root")
	if err != nil || u.Role != "admin" || !CheckPassword(u.PasswordHash, "pass123") {
		t.Fatalf("管理员账号新建不符: %v %+v", err, u)
	}
	// 已有用户：跳过（幂等）
	created, err = EnsureBootstrapAdmin(st, "other", "x12345")
	if err != nil || created {
		t.Fatalf("非空表应跳过: %v %v", created, err)
	}
	// 非法用户名拒绝（空表场景——非空表时直接跳过，env 无意义）
	fresh, _ := store.Open("")
	if _, err := EnsureBootstrapAdmin(fresh, "bad name!", "x12345"); err == nil {
		t.Fatal("非法用户名应报错")
	}
}
