package produce

import (
	"os"
	"path/filepath"
	"testing"

	"webpage-video-agent/server/internal/pipeline"
)

// 同签名刹车（P0-4）的签名归一化：重掷一轮只换变量名/数字 = 同签名；
// 错误类别不同 = 异签名。
func TestCheckSigRenameInvariant(t *testing.T) {
	a := checkSig("✗ seg07: Cannot access 'Ph' before initialization")
	b := checkSig("✗ seg07: Cannot access 'Qj' before initialization")
	if a != b {
		t.Fatalf("换变量名应同签名: %s vs %s", a, b)
	}
	c := checkSig("✗ 内容越界 compositions/frames/seg07.html y=1700")
	d := checkSig("✗ 内容越界 compositions/frames/seg11.html y=1650")
	if c != d {
		t.Fatalf("仅数字不同应同签名: %s vs %s", c, d)
	}
	e := checkSig("✗ 对比度不足")
	f := checkSig("✗ 元素越界")
	if e == f {
		t.Fatalf("不同错误类别不该同签名: %s", e)
	}
}

// 过检画面备份/恢复：stash 落盘、覆盖后 restore 拿回原内容（assemble 链路失败
// 不影响 comp 文件已还原的事实——文件层语义在此锁定）。
func TestStashRestoreLastGoodScene(t *testing.T) {
	dir := t.TempDir()
	p := pipeline.NewProject(dir, "p1")
	if err := os.MkdirAll(filepath.Join(p.Dir, "llm"), 0o755); err != nil {
		t.Fatal(err)
	}
	good := []byte(`<template data-composition-id="seg01">good</template>`)
	if err := os.WriteFile(p.Artifact("llm/comp-seg01.json"), good, 0o644); err != nil {
		t.Fatal(err)
	}
	stashLastGoodScene(p, "seg01")

	bad := []byte(`<template data-composition-id="seg01">bad</template>`)
	if err := os.WriteFile(p.Artifact("llm/comp-seg01.json"), bad, 0o644); err != nil {
		t.Fatal(err)
	}
	// 文件级还原（不经 restoreLastGoodScene——那条链尾部的 assemble 依赖 python 环境）
	b, err := os.ReadFile(lastGoodScenePath(p, "seg01"))
	if err != nil {
		t.Fatalf("备份缺失: %v", err)
	}
	if err := pipeline.WriteFileAtomic(p.Artifact("llm/comp-seg01.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p.Artifact("llm/comp-seg01.json"))
	if string(got) != string(good) {
		t.Fatalf("还原内容不符: %s", got)
	}
	// 无备份段：文件不存在，恢复路径能判 false 而不炸
	if _, err := os.Stat(lastGoodScenePath(p, "seg99")); !os.IsNotExist(err) {
		t.Fatalf("seg99 不该有备份: %v", err)
	}
	_ = filepath.Join // 保持 filepath 引用（不同平台测试共用）
}

func TestIsLoneRuntimeCrash(t *testing.T) {
	lone := "✗ check_runtime_failure: Cannot access 'Ph' before initialization"
	if !isLoneRuntimeCrash(lone) {
		t.Fatal("单条幽灵 TDZ 应命中")
	}
	mixed := "✗ contrast: #x 1.2:1\n✗ check_runtime_failure: Cannot access 'Ph' before initialization"
	if isLoneRuntimeCrash(mixed) {
		t.Fatal("伴随版面诊断不应命中（那是要真修的）")
	}
	other := "✗ check_runtime_failure: ReferenceError: foo is not defined"
	if isLoneRuntimeCrash(other) {
		t.Fatal("其他运行时错误不属幽灵 TDZ 指纹")
	}
	empty := ""
	if isLoneRuntimeCrash(empty) {
		t.Fatal("空诊断不应命中")
	}
}
