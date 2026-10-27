package stylelib

// kitmedia_test.go —— 画材包相关的库侧回环：媒体白名单扩展（kit/ 子树 +
// repro/neutral 样图）、AttachKit 回账、待蒸馏标记生命周期、重解析不烧 kit。

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"webpage-video-agent/server/internal/contract"
)

func TestValidMediaNameKit(t *testing.T) {
	for _, name := range []string{
		"frame-0.8.png", "repro-1.png", "neutral.png", "cover.png", "video.mp4",
		"kit/kit.js", "kit/kit.json", "kit/package-lock.json", "kit/scenes.js",
		"kit/cheat-sheet.md", "kit/spec-v1.lock",
	} {
		if !ValidMediaName(name) {
			t.Errorf("%q 应放行", name)
		}
	}
	for _, name := range []string{
		"kit/../kit.js", "kit/a/b.js", "kit/kit.exe", "kit/kit.JS", "kit/", "kit/.png",
		"repro-.png", "neutral-.png", "frame-.png", "a/b.png", "repro-1.jpg",
		"Frame-1.png", "kit/kit .js", "kit/$h.js",
	} {
		if ValidMediaName(name) {
			t.Errorf("%q 应拒绝", name)
		}
	}
}

func TestAttachKitRoundTrip(t *testing.T) {
	l := New(t.TempDir())
	main := mustVocab(t, l, "像素舞台", "格子笔触")
	st, err := l.IngestVideoRef("ref-kit", VideoStyleIn{
		Name: "像素舞台", Subject: "像素游戏战斗",
		Tags: contract.StyleTags{Main: main},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !st.DistillPending {
		t.Fatal("解析入库应打待蒸馏标记")
	}

	// kit 文件落位 + 回账
	src := filepath.Join(t.TempDir(), "kit.js")
	if err := os.WriteFile(src, []byte("// kit"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := l.PutMedia(st.ID, "kit/kit.js", src); err != nil {
		t.Fatal(err)
	}
	if err := l.PutMedia(st.ID, "repro-1.png", src); err != nil { // 名字过审即可，内容测试无所谓
		t.Fatal(err)
	}
	if err := l.AttachKit(st.ID, &StyleKit{Framework: "纯 Canvas2D", Files: []string{"kit.js"}}); err != nil {
		t.Fatal(err)
	}
	got, ok := l.Get(st.ID)
	if !ok || got.Kit == nil || got.Kit.Framework != "纯 Canvas2D" {
		t.Fatalf("AttachKit 后账不对：%+v", got)
	}
	if got.DistillPending {
		t.Fatal("kit 落库应摘掉待蒸馏标记")
	}
	// 媒体目录形态：kit/ 子树落位且帧清单不受污染
	if names, _ := l.MediaNames(st.ID); len(names) != 0 { // 没快照过 frame-*
		t.Fatalf("帧清单不该混进 kit 产物：%v", names)
	}
	if rc, _, _, ok := l.OpenMedia(st.ID, "kit/kit.js"); !ok {
		t.Fatal("kit/kit.js 应可回源")
	} else {
		rc.Close()
	}

	// 重解析（覆盖式重写）不烧 kit，也不再挂待蒸馏
	st2, err := l.IngestVideoRef("ref-kit", VideoStyleIn{
		Name: "像素舞台 v2", Tags: contract.StyleTags{Main: main},
	})
	if err != nil {
		t.Fatal(err)
	}
	if st2.Kit == nil {
		t.Fatal("重解析不该抹掉 Kit")
	}
	if st2.DistillPending {
		t.Fatal("已有 kit 的条目重解析不该再挂待蒸馏")
	}

	// 不存在的条目 / 非法媒体名
	if err := l.AttachKit("nope", &StyleKit{}); !errors.Is(err, ErrNoEntry) {
		t.Fatalf("应报 ErrNoEntry：%v", err)
	}
	if err := l.PutMedia(st.ID, "kit/../x.js", src); err == nil {
		t.Fatal("kit 路径穿越应拒绝")
	}
}
