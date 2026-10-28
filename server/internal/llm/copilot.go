// copilot.go —— 视觉副驾（2026-10-28 大杯换脑）：主脑 glm-5.3 纯文本无多模态，
// 一切「看图」活改由 flash 档（RoleVisual）单发细读成文字画像回喂文本通道。
// 判卷链（样张判卷/frameQA/段审/终审）本来就是整段 flash 直判，不经这里。
//
// 两个设计约束：
//   - 同图同问 = 同请求体字节，磁盘缓存天然跨调用点/跨段复用（重启重跑零成本）；
//     因此细读问法（question）必须是调用方定义的稳定常量，禁止拼时间戳/序号。
//   - 失败一律降级返回空串（调用方给缺省话术）——眼睛瞎了不挡出片：判卷链仍
//     对照真图，写的一端缺画像只是退回文字档案口径，闭环没断。
package llm

import (
	"context"
	"strings"

	openai "github.com/sashabaranov/go-openai"
)

// digestSystem 细读员系统提示词（全站共用一份固定前缀，利前缀缓存）。
const digestSystem = `你是视觉细读员。把给定帧图的视觉语法读成文字画像，供一个看不见图的画师使用。要求：配色给近似色值（底色/强调色/文字色）、质感（颗粒/描边/纹理/渐变）、画面密度与层次结构、字体气质（粗细/衬线/像素感/字距）；每图一段，开头带「图N：」。只写风格语法与构图事实，不复述题材物件、不评价好坏。只输出 JSON。`

// VisionDigest 视觉细读：images（data URL）+ question（这批图是什么、细读哪些
// 维度——必须是稳定常量，见文件头约束）→ 一段紧凑中文画像。失败返回空串，
// Usage 照实返回（调用方记账：花出去的探听费不算零）。
func VisionDigest(ctx context.Context, images []string, question string) (string, openai.Usage) {
	if len(images) == 0 {
		return "", openai.Usage{}
	}
	prov, err := FromEnv(RoleVisual)
	if err != nil {
		return "", openai.Usage{}
	}
	var out struct {
		Digest string `json:"digest"`
	}
	user := question + "\n输出：{\"digest\":\"多行文字画像，每图一段\"}"
	usage, err := prov.GenerateJSONVisionNoThink(ctx, digestSystem, user, images, 0.2, &out)
	if err != nil {
		return "", usage
	}
	return strings.TrimSpace(out.Digest), usage
}
