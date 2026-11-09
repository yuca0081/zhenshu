package prep

// materials_required.go —— 分镜工位看到的真图素材（2026-11-02 必需素材）。
//
// prep 不能 import produce（produce 已经 import prep，会成环），所以这里就地声明
// assets/materials.json 的**只读切片视图**：字段是 produce.Material 的子集，JSON
// 同源同文件，多余的字段解码时自动忽略。改动那份结构时两边要一起看
// （produce/materials.go Material / api/material_upload.go 写入端）。

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"webpage-video-agent/server/internal/pipeline"
)

// imageMaterial 一条真图素材（分镜只关心「哪张图、什么内容、必须不必」）。
type imageMaterial struct {
	File     string `json:"file"`
	Title    string `json:"title,omitempty"`
	Note     string `json:"note,omitempty"`
	Credit   string `json:"credit,omitempty"`
	Source   string `json:"source,omitempty"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	Required bool   `json:"required,omitempty"`
	Seg      string `json:"seg,omitempty"` // 预分配段（可空；空 = 由分镜自己分配）
}

// loadImageMaterials 读项目素材库（无文件/损坏返回 nil——素材永远可选）。
func loadImageMaterials(p *pipeline.Project) []imageMaterial {
	b, err := os.ReadFile(p.Artifact("assets/materials.json"))
	if err != nil {
		return nil
	}
	var st struct {
		Materials []imageMaterial `json:"materials"`
	}
	if json.Unmarshal(b, &st) != nil {
		return nil
	}
	return st.Materials
}

// imageFiles 素材清单全集（分镜认领的白名单）与其中「必需」的子集。
func imageFiles(mats []imageMaterial) (required, allowed []string) {
	seen := map[string]bool{}
	for _, m := range mats {
		f := strings.TrimSpace(m.File)
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		allowed = append(allowed, f)
		if m.Required {
			required = append(required, f)
		}
	}
	return required, allowed
}

// imageFileSets 项目素材清单的必需集与全集（分镜 validate/submit 校验用）。
func imageFileSets(p *pipeline.Project) (required, allowed []string) {
	return imageFiles(loadImageMaterials(p))
}

// formatImageMaterialsForPeer 素材清单的工具观察文本（给分镜模型看）。
// 必需项排前面并显式标注——它必须被某一镜认领，否则提交会被拒。
func formatImageMaterialsForPeer(mats []imageMaterial) string {
	if len(mats) == 0 {
		return "本项目没有上传真图素材。所有画面都用程序化图形画，不需要 shot.images。"
	}
	var b strings.Builder
	reqN := 0
	for _, m := range mats {
		if m.Required {
			reqN++
		}
	}
	fmt.Fprintf(&b, "本项目共 %d 条真图素材，其中**必需 %d 条**（必需 = 必须出现在成片里，提交时校验）。\n\n", len(mats), reqN)
	write := func(m imageMaterial) {
		mark := "可选"
		if m.Required {
			mark = "必需 ⚠"
		}
		fmt.Fprintf(&b, "- [%s] `%s`", mark, m.File)
		if m.Title != "" {
			b.WriteString(" ｜ " + m.Title)
		}
		if m.Width > 0 && m.Height > 0 {
			fmt.Fprintf(&b, " ｜ 原图 %d×%d", m.Width, m.Height)
		}
		b.WriteString("\n")
		if m.Note != "" {
			b.WriteString("    - 内容/用途：" + m.Note + "\n")
		}
		if m.Seg != "" {
			b.WriteString("    - 预分配段：" + m.Seg + "（建议值，可改）\n")
		}
	}
	for _, m := range mats {
		if m.Required {
			write(m)
		}
	}
	for _, m := range mats {
		if !m.Required {
			write(m)
		}
	}
	b.WriteString(`
怎么用（写进每一段的 shot.images）：
{"file":"assets/img/文件名","role":"hero|inset","at":0.6,"hold":3.0,"present":"摆位/裁切/边框/重点描哪"}
- hero = 主画面（面积 ≥ 25% 画面）；inset = 贴图/画中画（面积 ≥ 6% 画面、短边 ≥ 200px）。没有第三档。
- 一张图只在一段里当主角；要复用就另起一段再认领一次（观众需要时间看清）。
- 认领了图的段，visual_brief.scene 里必须把这张图写进画面（写文件名或「截图/架构图/表格」这类具体指代）——写作阶段读的是那段散文。
- 不需要图的段不要写 shot.images。**但每条「必需」素材都必须被某一镜认领，否则提交被拒。**`)
	return b.String()
}
