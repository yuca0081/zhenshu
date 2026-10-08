package styleref

import (
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// clipArgs 时长档：全片（end<=0）与超长段截到 300s 上限，普通段原样。
func TestClipArgsCapsDuration(t *testing.T) {
	cases := []struct {
		name       string
		start, end float64
		wantSS     string
		wantT      string
	}{
		{"全片", 0, 0, "0.00", "300.00"},
		{"普通段", 55, 120, "55.00", "65.00"},
		{"超长段截断", 10, 1000, "10.00", "300.00"},
	}
	for _, c := range cases {
		args := clipArgs("in.mp4", "out.mp4", c.start, c.end)
		if got := argValue(args, "-ss"); got != c.wantSS {
			t.Fatalf("%s: -ss = %q, want %q", c.name, got, c.wantSS)
		}
		if got := argValue(args, "-t"); got != c.wantT {
			t.Fatalf("%s: -t = %q, want %q", c.name, got, c.wantT)
		}
		joined := strings.Join(args, " ")
		for _, must := range []string{"-an", "+faststart", "scale=-2:480"} { // 无声/快启动/480p 缺一不可
			if !strings.Contains(joined, must) {
				t.Fatalf("%s: 剪辑参数缺 %q（%s）", c.name, must, joined)
			}
		}
	}
}

func argValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// 签名往返：有效签名放行；换文件名/换引用/过期一律拒绝；尾斜杠的 base 被规整。
func TestSignVideoRoundtripAndTamper(t *testing.T) {
	t.Setenv("PUBLIC_BASE_URL", "https://vid.example.com/")
	s := NewStore(t.TempDir(), t.TempDir())
	u, err := s.SignVideoURL("ref20260101-000000", "clip-seg01.mp4", time.Hour)
	if err != nil || u == "" {
		t.Fatalf("签名失败: %q %v", u, err)
	}
	if !strings.HasPrefix(u, "https://vid.example.com/api/llm-video/") {
		t.Fatalf("URL 形状不对（尾斜杠应被去掉）: %s", u)
	}
	ur, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	exp, _ := strconv.ParseInt(ur.Query().Get("exp"), 10, 64)
	sig := ur.Query().Get("sig")
	if !s.VerifyVideo("ref20260101-000000", "clip-seg01.mp4", exp, sig) {
		t.Fatal("有效签名被拒")
	}
	if s.VerifyVideo("ref20260101-000000", "clip-seg02.mp4", exp, sig) {
		t.Fatal("换文件名仍通过（签名未与文件绑定）")
	}
	if s.VerifyVideo("ref99999999-000000", "clip-seg01.mp4", exp, sig) {
		t.Fatal("换引用仍通过（签名未与引用绑定）")
	}
	if s.VerifyVideo("ref20260101-000000", "clip-seg01.mp4", exp-3601, sig) {
		t.Fatal("过期仍通过")
	}
}

// 未配 PUBLIC_BASE_URL = 功能整体关闭：返回空 URL 且不算错误（调用方降级纯帧）。
func TestSignVideoDisabledWithoutBaseURL(t *testing.T) {
	t.Setenv("PUBLIC_BASE_URL", "")
	s := NewStore(t.TempDir(), t.TempDir())
	u, err := s.SignVideoURL("refx", "clip-full.mp4", time.Hour)
	if err != nil || u != "" {
		t.Fatalf("应返回空 URL: %q %v", u, err)
	}
}

// 临时接口文件白名单：只放行引用目录下的 clip-*.mp4，原片/穿越路径/其他文件全拒。
func TestVideoFileWhitelist(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir, dir)
	refID := "ref20260101-000000"
	if err := os.MkdirAll(filepath.Join(dir, "references", refID), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"clip-full.mp4", "video.mp4", "features.json"} {
		if err := os.WriteFile(filepath.Join(dir, "references", refID, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := s.VideoFile(refID, "clip-full.mp4"); !ok {
		t.Fatal("合法剪辑被拒")
	}
	for _, bad := range [][2]string{
		{refID, "video.mp4"},                // 原片不放行
		{refID, "features.json"},            // 非剪辑不放行
		{refID, "../llm_video_secret.key"},  // 穿越
		{"../other", "clip-full.mp4"},       // 换目录
		{"../../etc", "clip-full.mp4"},      // 多级穿越
	} {
		if _, ok := s.VideoFile(bad[0], bad[1]); ok {
			t.Fatalf("应拒绝 %v", bad)
		}
	}
}

// 密钥跨调用稳定（落盘复用），不是每次随机——重启后旧链接仍可验签。
func TestVideoSecretStableAcrossCalls(t *testing.T) {
	s := NewStore(t.TempDir(), t.TempDir())
	a, err := s.videoSecret()
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.videoSecret()
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) || len(a) < 32 {
		t.Fatalf("密钥应稳定且够长: %q vs %q", a, b)
	}
}
