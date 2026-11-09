package review

// material_section_test.go —— 段审 kickoff 的素材小节编号（2026-11-03）。
//
// 判卷官要拿原图对照帧判「素材看不看得见」；图号错位会让它对着 A 图判 B 素材，
// 比不注入更糟。本测试锁死：只有真能外发的原图占图号，缺图条目显式说明。

import (
	"strings"
	"testing"

	"webpage-video-agent/server/internal/llm"
)

func TestWriteMaterialSectionNumbering(t *testing.T) {
	mats := []MaterialRef{
		{File: "assets/img/missing.png", Role: "hero", AreaMin: 0.25,
			At: 0.6, Hold: 3.0, Present: "幕布正中"},
		{File: "assets/img/ok.jpg", Title: "GitHub 首页", Role: "inset", AreaMin: 0.06},
	}
	imgs := []llm.ImgRef{
		{}, // 第一条原图读不出来
		{URL: "data:image/jpeg;base64,AAAA", Path: "assets/img/ok.jpg"},
	}
	var b strings.Builder
	writeMaterialSection(&b, mats, imgs, 2) // 前面已外发 2 张（锚点帧 / 样张）
	out := b.String()

	// 第一条没原图 → 显式「原图缺失」；第二条占「前 2 张之后」的号（图3）。
	for _, want := range []string{
		"`assets/img/missing.png`", "role=hero（要求面积 ≥25%）", "幕布正中",
		"原图缺失",
		"`assets/img/ok.jpg`", "GitHub 首页", "原图 = 图3",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("素材小节缺 %q：\n%s", want, out)
		}
	}
	if strings.Contains(out, "图4") {
		t.Fatalf("缺图条目不许占图号（会让图号跳空/错位）：\n%s", out)
	}
}

// 原图全缺时：仍要写清落地要求与「原图缺失」，判卷官不至于把读不到当没出现。
func TestWriteMaterialSectionWithoutImages(t *testing.T) {
	var b strings.Builder
	writeMaterialSection(&b, []MaterialRef{{File: "assets/img/x.jpg", Role: "inset", AreaMin: 0.06}}, nil, 0)
	out := b.String()
	if !strings.Contains(out, "原图缺失") || strings.Contains(out, "原图 = 图") {
		t.Fatalf("无原图时不应有图号指针：\n%s", out)
	}
}
