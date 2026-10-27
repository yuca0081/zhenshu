package prep

// distill_test.go —— 蒸馏监工的纯逻辑面单测（docker/LLM 通道不进测试：
// 本机 Windows 无 Docker 是常态，房内行为靠云服务器首单集成验证）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"webpage-video-agent/server/internal/stylelib"
)

// 房间启动参数钉住承重墙三规矩+资源限（方案 §5.3）——这是安全边界，改坏必须惊动测试。
func TestDockerRunArgs(t *testing.T) {
	taskDir := t.TempDir()
	args := dockerRunArgs("distill-base:1", taskDir)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--memory 2g", "--cpus 2", "--cap-drop=ALL", "--security-opt no-new-privileges",
		"--user 1000:1000",
		filepath.Join(taskDir, "in") + ":/task/in:ro",
		filepath.Join(taskDir, "out") + ":/task/out:rw",
		"distill-base:1", "sleep infinity",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("启动参数缺 %q：%s", want, joined)
		}
	}
}

func TestRoomRelPath(t *testing.T) {
	for _, p := range []string{"out/kit.js", "work/draft.mjs", "/task/out/kit.json"} {
		if _, err := roomRelPath(p, true); err != nil {
			t.Errorf("写路径 %q 应放行: %v", p, err)
		}
	}
	if _, err := roomRelPath("in/truth-1.png", false); err != nil {
		t.Errorf("读 in/ 应放行: %v", err)
	}
	if _, err := roomRelPath("in/x.md", true); err == nil {
		t.Error("写 in/（只读考卷）应拒绝")
	}
	for _, p := range []string{"out/../etc/passwd", "/etc/passwd", "..", "out/my file.js", "out/a$b.js", "out/x\\y.js"} {
		if _, err := roomRelPath(p, false); err == nil {
			t.Errorf("路径 %q 应拒绝", p)
		}
	}
	// out/ 下的嵌套子目录放行：房里允许自组织，出料侧只认清单平名（collectKitFiles 把关）
	if _, err := roomRelPath("out/sub/dir/kit.js", false); err != nil {
		t.Errorf("嵌套子目录应放行: %v", err)
	}
}

// manifest lint：精确版本/许可证/lockfile 账单/装了没报——毕业检查①②③的机械面。
func TestLoadDistillManifest(t *testing.T) {
	cheat := strings.Repeat("笔 mark/色 palette/纹理 texture/明度 light 四类语义 API；", 30)
	notes := "路线：底漆→笔触→做旧。弱点：暗部易糊。"

	cases := []struct {
		name    string
		deps    string // kit.json deps 原文
		pkg     string // package.json 原文；空=不写
		wantErr string // 期望的问题关键词；空=应无问题
	}{
		{"零依赖合法", `[]`, "", ""},
		{"精确版本+许可证合法", `[{"name":"p5","version":"1.9.0","license":"LGPL-2.1"}]`,
			`{"dependencies":{"p5":"1.9.0"}}`, ""},
		{"版本带前缀违法", `[{"name":"p5","version":"^1.9.0","license":"MIT"}]`, "", "不精确"},
		{"@latest 违法", `[{"name":"p5","version":"latest","license":"MIT"}]`, "", "不精确"},
		{"缺许可证", `[{"name":"p5","version":"1.9.0"}]`, "", "license"},
		{"声明依赖缺 lockfile", `[{"name":"p5","version":"1.9.0","license":"MIT"}]`, "", "package-lock"},
		{"装了没报", `[{"name":"p5","version":"1.9.0","license":"MIT"}]`,
			`{"dependencies":{"p5":"1.9.0","other":"2.0.0"}}`, "没声明"},
		{"package.json 版本漂浮", `[{"name":"p5","version":"1.9.0","license":"MIT"}]`,
			`{"dependencies":{"p5":"^1.9.0"}}`, "钉死"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			kit := map[string]any{
				"framework": "p5.brush 1.9", "cheat_sheet": cheat, "notes": notes,
				"files": []string{"scenes.js"},
			}
			if tc.deps != "" {
				kit["deps"] = json.RawMessage(tc.deps)
			}
			if tc.pkg != "" {
				writeOut(t, dir, "package.json", tc.pkg)
				writeOut(t, dir, "package-lock.json", "{}")
			}
			b, _ := json.Marshal(kit)
			writeOut(t, dir, "kit.json", string(b))
			writeOut(t, dir, "kit.js", "// kit")
			writeOut(t, dir, "scenes.js", "// scenes")
			_, errs := loadDistillManifest(dir)
			joined := strings.Join(errs, "；")
			if tc.wantErr == "" && len(errs) > 0 {
				t.Fatalf("应无问题，收到：%s", joined)
			}
			if tc.wantErr != "" && !strings.Contains(joined, tc.wantErr) {
				t.Fatalf("问题应含 %q，收到：%s", tc.wantErr, joined)
			}
		})
	}

	// 独立小项：cheat_sheet 太短 / notes 流水账
	t.Run("cheat_sheet 太短", func(t *testing.T) {
		dir := t.TempDir()
		writeOut(t, dir, "kit.json", `{"framework":"x","cheat_sheet":"太短","notes":"n"}`)
		writeOut(t, dir, "kit.js", "//")
		_, errs := loadDistillManifest(dir)
		if !strings.Contains(strings.Join(errs, "；"), "cheat_sheet") {
			t.Fatalf("应报 cheat_sheet 问题：%v", errs)
		}
	})
}

func TestCollectKitFiles(t *testing.T) {
	t.Run("清单去重+kit.js 兜底", func(t *testing.T) {
		dir := t.TempDir()
		for _, f := range []string{"kit.json", "kit.js", "scenes.js"} {
			writeOut(t, dir, f, "// "+f)
		}
		files, err := collectKitFiles(dir, &distillManifest{Files: []string{"scenes.js", "kit.json"}})
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]bool{"kit.json": true, "scenes.js": true, "kit.js": true}
		if len(files) != len(want) {
			t.Fatalf("应收 %v，得到 %v", want, files)
		}
		for _, f := range files {
			if !want[f] {
				t.Fatalf("多余文件 %s", f)
			}
		}
	})
	t.Run("非法名拒绝", func(t *testing.T) {
		_, err := collectKitFiles(t.TempDir(), &distillManifest{Files: []string{"a/b.js"}})
		if err == nil {
			t.Fatal("子目录名应拒绝")
		}
	})
	t.Run("缺文件拒绝", func(t *testing.T) {
		dir := t.TempDir()
		writeOut(t, dir, "kit.json", "{}")
		writeOut(t, dir, "kit.js", "//")
		_, err := collectKitFiles(dir, &distillManifest{Files: []string{"ghost.js"}})
		if err == nil {
			t.Fatal("缺文件应拒绝")
		}
	})
	t.Run("有依赖带 package 账单", func(t *testing.T) {
		dir := t.TempDir()
		for _, f := range []string{"kit.json", "kit.js", "package.json", "package-lock.json"} {
			writeOut(t, dir, f, "{}")
		}
		files, err := collectKitFiles(dir, &distillManifest{
			Deps: []stylelib.StyleKitDep{{Name: "p5", Version: "1.9.0", License: "MIT"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		found := map[string]bool{}
		for _, f := range files {
			found[f] = true
		}
		if !found["package-lock.json"] {
			t.Fatalf("lockfile 应进清单：%v", files)
		}
	})
}

func TestDistillVerdictPassed(t *testing.T) {
	mk := func(totals []float64, fatal string, neutral float64) *distillVerdict {
		v := &distillVerdict{}
		for i, total := range totals {
			v.Frames = append(v.Frames, distillFrameScore{
				Frame: string(rune('A' + i)), Total: total, Fatal: fatal,
			})
		}
		v.Neutral.Score = neutral
		return v
	}
	if !(mk([]float64{34, 31.5, 31}, "", 82)).passed() {
		t.Fatal("首单口径（34/31.5/31 + 换题82）应过线")
	}
	if (mk([]float64{34, 29.5, 31}, "", 82)).passed() {
		t.Fatal("单帧 29.5 < 30 应不过")
	}
	if (mk([]float64{34, 31.5, 31}, "远看不神似", 82)).passed() {
		t.Fatal("致命穿帮应不过")
	}
	if (mk([]float64{34, 31.5, 31}, "", 68)).passed() {
		t.Fatal("换题 68 < 70 应不过")
	}
	line := mk([]float64{34, 31.5}, "", 82).reproduceLine()
	if line != "A 34.0/40 · B 31.5/40" {
		t.Fatalf("还原分一行口径：%q", line)
	}
}

func TestPickSpread(t *testing.T) {
	if got := pickSpread([]string{"a", "b"}, 3); len(got) != 2 {
		t.Fatalf("n 超长应收全量：%v", got)
	}
	got := pickSpread([]string{"a", "b", "c", "d", "e"}, 3)
	if len(got) != 3 || got[0] != "a" || got[2] != "e" {
		t.Fatalf("首/中/尾：%v", got)
	}
}

func writeOut(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
