// Package auth：用户密码哈希与签名会话（无状态 cookie，不建会话表）。
//
// 固定账号模型：账号由管理员预建（启动引导 + /api/auth/users 管理端点），无自助注册。
// 会话选 HMAC 签名 cookie 而非服务端会话表：memStore 降级/多实例都不需要额外状态，
// EventSource（SSE）同源自动带 cookie，顺带消灭旧 ?token= 明文进 URL 的问题。
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"webpage-video-agent/server/internal/store"
)

// HashPassword bcrypt（自带盐，DefaultCost≈几十毫秒，登录频率下无压力）。
func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

// CheckPassword hash 为空/格式坏一律 false（恒不匹配），不 panic。
func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// Claims 会话载荷。角色/用户名随 cookie 走：改角色重登录后生效（固定账号场景够用），
// 换取每请求零 DB 查询。UID 是 store.User.ID（UUID）。
type Claims struct {
	UID      string `json:"uid"`
	Username string `json:"username"`
	Role     string `json:"role"` // admin | user
	Exp      int64  `json:"exp"`
}

var ErrBadSession = errors.New("会话无效或已过期")

// SignSession → base64url(payload) + "." + hex(HMAC-SHA256(payload))。
func SignSession(secret []byte, cl Claims) string {
	b, _ := json.Marshal(cl)
	payload := base64.RawURLEncoding.EncodeToString(b)
	return payload + "." + signPayload(secret, payload)
}

// VerifySession 验签 + 查有效期。hmac.Equal 恒定时间比较，防时序侧信道。
func VerifySession(secret []byte, val string, now time.Time) (Claims, error) {
	var cl Claims
	parts := strings.Split(val, ".")
	if len(parts) != 2 {
		return cl, ErrBadSession
	}
	want := signPayload(secret, parts[0])
	if !hmac.Equal([]byte(want), []byte(parts[1])) {
		return cl, ErrBadSession
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(b, &cl) != nil {
		return cl, ErrBadSession
	}
	if now.Unix() >= cl.Exp {
		return cl, ErrBadSession
	}
	return cl, nil
}

// signPayload HMAC-SHA256 十六进制摘要。
func signPayload(secret []byte, payload string) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(payload))
	return hex.EncodeToString(m.Sum(nil))
}

// DeriveSecret 会话签名密钥：SESSION_SECRET > API_TOKEN 派生 > 随机（开放模式）。
// 从 API_TOKEN 派生不降低强度——知 token 者本就是管理员；代价是换 token 全员下线（可接受）。
// 两者都未配置 = 本机开放模式：仍返回随机密钥（避免 nil secret），active=false。
func DeriveSecret(sessionSecret, apiToken string) (secret []byte, active bool) {
	if sessionSecret != "" {
		return []byte(sessionSecret), true
	}
	if apiToken != "" {
		h := sha256.Sum256([]byte("zhenshu-session:" + apiToken))
		return h[:], true
	}
	s := make([]byte, 32)
	if _, err := rand.Read(s); err != nil {
		panic(fmt.Sprintf("会话密钥生成失败: %v", err)) // crypto/rand 失败 = 系统级异常
	}
	return s, false
}

// usernameRe 用户名白名单：点号不能进用户名（cookie 载荷分隔符语义需要）。
var usernameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{2,32}$`)

func ValidUsername(s string) bool { return usernameRe.MatchString(s) }

// EnsureBootstrapAdmin 用户表为空时建初始管理员（仅鉴权启用时调用，开放模式跳过）。
// 口令未配置 = 用户名默认 admin / 口令默认 admin，大声警告——公网部署必须设
// INITIAL_ADMIN_PASSWORD。返回是否新建。
func EnsureBootstrapAdmin(st store.Store, username, password string) (bool, error) {
	users, err := st.ListUsers()
	if err != nil {
		return false, fmt.Errorf("查用户表失败: %w", err)
	}
	if len(users) > 0 {
		return false, nil
	}
	if username == "" {
		username = "admin"
	}
	if !ValidUsername(username) {
		return false, fmt.Errorf("INITIAL_ADMIN_USER %q 不合法（2-32 位字母/数字/_/-）", username)
	}
	defaulted := false
	if password == "" {
		password = "admin"
		defaulted = true
	}
	hash, err := HashPassword(password)
	if err != nil {
		return false, err
	}
	u := &store.User{Username: username, Role: "admin", PasswordHash: hash}
	if err := st.CreateUser(u); err != nil {
		return false, fmt.Errorf("建初始管理员失败: %w", err)
	}
	if defaulted {
		log.Printf("[auth] ⚠ 已创建初始管理员 %s，口令是默认值 admin——公网部署务必立即改密，或在 .env 设 INITIAL_ADMIN_PASSWORD", username)
	} else {
		log.Printf("[auth] 已创建初始管理员 %s", username)
	}
	return true, nil
}
