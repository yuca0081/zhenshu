package produce

// scenehistory.go —— 写画面会话历史（2026-10-20 降本 D2「续写」，docs/cost-optimization-plan §3.2）：
//
// 修复轮不再失忆重开：首轮对话（user 大提示 + assistant 成稿 JSON）落盘
// llm/comp-<seg>.history.json，修复调用以完整消息数组出站——模型带着自己
// 首轮的全上下文改自己的稿（旧路径每轮重发全套注入包 + prevHTML 全文，
// 等于付着「历史」的钱拿不到「记忆」的收益）。全新会话路径保留为兜底。
//
// 护栏（方案 §3.2）：①指纹失配（分镜/设计改稿）即弃用；②续写帽
// maxSceneContinuations——GLM 无前缀缓存，历史每轮照付全量 prompt，帽后改走
// 全新会话重置；③历史只存文本：首轮的样张/素材图不进历史，续写请求零图片
// 载荷，修复轮不再重传图（风格已由 assistant 成稿体现）。同签名打转由
// segCheck 的签名刹车（回退上一版过检画面）兜底，不在此重复。

import (
	"encoding/json"
	"os"
	"strings"

	openai "github.com/sashabaranov/go-openai"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/pipeline"
)

// scenePayload 场景产物的历史/留痕形态：只含 html 与元素注册表——image_requests
// 是首轮一次性申请单，不进历史（续写轮模型不该再交申请单）。
type scenePayload struct {
	HTML     string                  `json:"html"`
	Elements []contract.NamedElement `json:"elements"`
}

// scenePayloadJSON 产物序列化（历史消息与留痕共用）。
func scenePayloadJSON(html string, elements []contract.NamedElement) string {
	b, _ := json.Marshal(scenePayload{HTML: html, Elements: elements})
	return string(b)
}

// maxSceneContinuations 单段会话续写帽：每追加一对修复消息，下轮 prompt 就多背
// 一段——帽后收益转负（无前缀缓存端点），改走全新会话并重置历史。
const maxSceneContinuations = 3

type sceneHistory struct {
	FP       string                         `json:"fp"`       // 场景指纹（sceneSidecarFingerprint 同源）：分镜/设计改稿即失效
	Messages []openai.ChatCompletionMessage `json:"messages"` // user(首轮大提示) + assistant(成稿JSON) + [user(修复指令)+assistant]×N
	Cont     int                            `json:"cont"`     // 已续写次数
}

func sceneHistoryPath(p *pipeline.Project, segID string) string {
	return p.Artifact("llm/comp-" + segID + ".history.json")
}

// loadSceneHistory 读可用的会话历史；缺失/指纹失配/超帽/消息不成对返回 nil——
// 调用方退回全新会话路径（行为同旧版）。
func loadSceneHistory(p *pipeline.Project, segID, fp string) *sceneHistory {
	b, err := os.ReadFile(sceneHistoryPath(p, segID))
	if err != nil {
		return nil
	}
	var h sceneHistory
	if json.Unmarshal(b, &h) != nil || h.FP != fp {
		return nil
	}
	if h.Cont >= maxSceneContinuations || len(h.Messages) < 2 {
		return nil
	}
	// 末条必须是 assistant（成稿）——修复指令追加在它后面才构成合法对话
	if h.Messages[len(h.Messages)-1].Role != openai.ChatMessageRoleAssistant {
		return nil
	}
	return &h
}

// saveSceneHistory 原子落盘。写失败静默：历史是省钱优化不是正确性依赖，丢了
// 下一轮自动退回全新会话——宁重复不陈旧。
func saveSceneHistory(p *pipeline.Project, segID string, h *sceneHistory) {
	b, err := json.Marshal(h)
	if err != nil {
		return
	}
	_ = pipeline.WriteFileAtomic(sceneHistoryPath(p, segID), b, 0o644)
}

// removeSceneHistory 首写/全片重生成前清旧历史（宁无历史不错历史）。
func removeSceneHistory(p *pipeline.Project, segID string) {
	_ = os.Remove(sceneHistoryPath(p, segID))
}

// sceneContRepairMsg 修复轮追加的用户消息——轻量提示词（§八·1）：不重发契约/
// 设计系统/金样例/词序表（那些活在首轮消息里），只带指令本身与输出契约
// （patch 改动单优先，整份文件兜底）。
func sceneContRepairMsg(instruction, feedback string) string {
	var b strings.Builder
	b.WriteString("## 修复指令（在原对话续写：你自己的上一条回复就是当前场景文件）\n")
	if instruction != "" {
		b.WriteString("用户改画要求（最高优先级，逐字落实）：\n" + instruction + "\n")
	}
	if feedback != "" {
		b.WriteString("检查回喂（逐条修正）：\n" + feedback + "\n")
	}
	b.WriteString(`
只修指出的问题、只做要求里的改动，其余画面一切保持原样。首轮附带的样张/素材图本轮不重发，风格以你首轮成稿为准。

输出二选一：
a) 改动单（改动少时首选，省 token）：
{"note":"改动思路一句话","patches":[
  {"target":"#dom-id","text":"新文字"},
  {"target":"#dom-id","style":{"color":"#fff"}},
  {"target":"#dom-id","html":"<div id=\"dom-id\" …>替换后的完整外层</div>"}]}
	   - target 用元素 DOM id；一条只做一件事（text/style/html 互斥）；打不中整单作废
	   - html 替换必须保持同一 id（GSAP 补间按 id 找元素）
	   - 时间轴/GSAP 补间/大结构改动塞不进改动单——不要硬凑，用 b。
	     升级规则：上一轮交的是改动单、检查仍报同一问题＝问题在改动单够不着的
	     时序/结构里，本轮禁止再交改动单，必须用 b 整份重写（入场动画窗口里的
	     重叠/越界几乎都属于这类：动的是补间，不是静态摆放）。
b) 整份文件（结构/时序大改时）：
{"note":"…","html":"<template>…整份场景文件…</template>","elements":[{"id":"元素DOM id","name":"人话名"}]}`)
	return b.String()
}
