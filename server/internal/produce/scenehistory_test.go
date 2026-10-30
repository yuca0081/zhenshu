package produce

import (
	"os"
	"strings"
	"testing"

	openai "github.com/sashabaranov/go-openai"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/pipeline"
)

// 会话历史（2026-10-20 降本 D2 续写）：指纹匹配才可用；超帽/末条非 assistant/
// 消息不成对一律视为不可用——调用方退回全新会话，宁重复不陈旧。
func TestSceneHistoryLoadGuards(t *testing.T) {
	dir := t.TempDir()
	p := pipeline.NewProject(dir, "p1")
	if err := os.MkdirAll(p.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	saveSceneHistory(p, "seg01", &sceneHistory{
		FP: "fp1",
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: "首轮提示"},
			{Role: openai.ChatMessageRoleAssistant, Content: `{"html":"<template/>"}`},
		},
		Cont: 0,
	})

	if h := loadSceneHistory(p, "seg01", "fp1"); h == nil {
		t.Fatal("指纹匹配应可用")
	}
	if h := loadSceneHistory(p, "seg01", "fp2"); h != nil {
		t.Fatal("指纹失配应弃用（分镜/设计改稿）")
	}
	if h := loadSceneHistory(p, "seg02", "fp1"); h != nil {
		t.Fatal("缺失应返回 nil")
	}

	// 超帽：Cont 到顶不可续
	saveSceneHistory(p, "seg03", &sceneHistory{FP: "fp1", Cont: maxSceneContinuations,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: "u"},
			{Role: openai.ChatMessageRoleAssistant, Content: "a"},
		}})
	if h := loadSceneHistory(p, "seg03", "fp1"); h != nil {
		t.Fatal("续写超帽应弃用（改走全新会话重置历史）")
	}

	// 末条非 assistant（历史损坏/写坏）不可用
	saveSceneHistory(p, "seg04", &sceneHistory{FP: "fp1",
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: "u"},
			{Role: openai.ChatMessageRoleUser, Content: "u2"},
		}})
	if h := loadSceneHistory(p, "seg04", "fp1"); h != nil {
		t.Fatal("末条非 assistant 应弃用")
	}
}

// 续写修复指令：用户改画要求与检查回喂都要在；轻量提示词不许重发契约/金样例。
func TestSceneContRepairMsg(t *testing.T) {
	got := sceneContRepairMsg("把标题改成 X", "对比度 ✗ …")
	for _, want := range []string{"把标题改成 X", "对比度 ✗", "原对话续写", "整份场景文件"} {
		if !strings.Contains(got, want) {
			t.Fatalf("续写指令缺 %q", want)
		}
	}
	if strings.Contains(got, "gsap.timeline") || strings.Contains(got, "金样例") {
		t.Fatal("续写指令应是轻量的——契约/金样例活在首轮历史里，不许重发")
	}
}

// 机器验收退回的失败落盘（2026-10-30）：对话带退回原因存档可回读（自动重跑
// 带原因续修的入口条件）；无成稿对话不存；pending 链 Cont 累计到帽后拒载。
func TestSceneHistoryPendingRoundtrip(t *testing.T) {
	dir := t.TempDir()
	p := pipeline.NewProject(dir, "p1")
	if err := os.MkdirAll(p.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	msgs := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: "首轮提示"},
		{Role: openai.ChatMessageRoleAssistant, Content: `{"html":"<template/>","elements":[{"id":"a","name":"甲"}]}`},
	}
	savePendingSceneHistory(p, "seg01", "fp1", msgs, nil, "契约检查三轮未过：\n- 未注册时间轴")

	h := loadSceneHistory(p, "seg01", "fp1")
	if h == nil {
		t.Fatal("pending 历史指纹匹配且末条为 assistant 应可载")
	}
	if h.Pending == "" || !strings.Contains(h.Pending, "未注册时间轴") {
		t.Fatalf("退回原因应随历史回读，got %q", h.Pending)
	}
	if h.Cont != 1 {
		t.Fatalf("首条 pending 链 Cont 应为 1，got %d", h.Cont)
	}

	// 无成稿对话（模型调用本身失败）不落盘——没有可续的对话
	savePendingSceneHistory(p, "seg02", "fp1", nil, nil, "模型调用失败")
	if loadSceneHistory(p, "seg02", "fp1") != nil {
		t.Fatal("无对话不该落 pending 历史")
	}

	// pending 链 Cont 随上一轮递增；存到帽值后拒载（重跑自然重置为全新会话）
	savePendingSceneHistory(p, "seg03", "fp1", msgs, &sceneHistory{Cont: maxSceneContinuations - 2}, "又没过")
	if h := loadSceneHistory(p, "seg03", "fp1"); h == nil || h.Cont != maxSceneContinuations-1 {
		t.Fatalf("帽内应可载且 Cont=%d", maxSceneContinuations-1)
	}
	savePendingSceneHistory(p, "seg03", "fp1", msgs, &sceneHistory{Cont: maxSceneContinuations - 1}, "还是没过")
	if loadSceneHistory(p, "seg03", "fp1") != nil {
		t.Fatal("pending 链存到帽值应拒载（护栏：防历史无限膨胀/被自己带偏）")
	}
}

// 产物序列化：只含 html/elements，image_requests 申请单不进历史。
func TestScenePayloadJSONShape(t *testing.T) {
	got := scenePayloadJSON("<template/>", []contract.NamedElement{{ID: "a", Name: "甲"}})
	if strings.Contains(got, "image_requests") {
		t.Fatalf("申请单不该进历史: %s", got)
	}
	for _, want := range []string{`"html"`, `"elements"`, `"a"`, "甲"} {
		if !strings.Contains(got, want) {
			t.Fatalf("缺 %q: %s", want, got)
		}
	}
}
