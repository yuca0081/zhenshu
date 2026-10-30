package prep

// materials.go —— 项目材料库（2026-10-30 主题页配套）：用户在主题页上传的
// 参考资料（.txt/.md 纯文本），落 input/materials/。①文稿工位的 read_source
// 是「材料唯一入口」，这里只是它的一个来源段——材料与原始稿（article.txt）
// 同等对待：改写时不可改事实、不可增删观点。
// 只收纯文本是有意的 v1 边界：docx/pdf 要解析器，图片要多模态读稿，等真实
// 使用逼出来再加；后端不猜格式（JSON 传上来的就是文本）。

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"webpage-video-agent/server/internal/pipeline"
)

// MaterialsDir 项目内材料目录（相对项目根）。
const MaterialsDir = "input/materials"

// 材料单份上限（rune）：50k 字 ≈ 一本小册子，超了说明传错了东西；
// 总注入上限防御性给到 200k——read_source 一次性回喂，别把上下文撑爆。
const (
	materialMaxRunes = 50_000
	materialTotalCap = 200_000
)

// MaterialFile 材料清单行（API 回传用）。
type MaterialFile struct {
	Name  string `json:"name"`
	Chars int    `json:"chars"`
}

// sanitizeMaterialName 文件名白名单清洗：去路径分量与控制符，保中文/空格；
// 空名与危险名（. .. 隐藏文件）拒绝——落盘名就是用户看到的清单名，不能让
// "…/…/article.txt" 这种输入写到材料目录外。
func sanitizeMaterialName(name string) (string, error) {
	n := strings.TrimSpace(name)
	if n == "" || n == "." || n == ".." || strings.HasPrefix(n, ".") {
		return "", fmt.Errorf("材料名不合法")
	}
	if strings.ContainsAny(n, `/\`) || strings.Contains(n, "..") {
		return "", fmt.Errorf("材料名不能含路径分隔符")
	}
	for _, r := range n {
		if r < 0x20 {
			return "", fmt.Errorf("材料名含控制字符")
		}
	}
	if len([]rune(n)) > 80 {
		n = string([]rune(n)[:80])
	}
	return n, nil
}

// ListMaterials 材料清单（按名称排序，稳定展示）。
func ListMaterials(p *pipeline.Project) []MaterialFile {
	entries, err := os.ReadDir(p.Artifact(MaterialsDir))
	if err != nil {
		return []MaterialFile{}
	}
	out := []MaterialFile{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(p.Artifact(MaterialsDir + "/" + e.Name()))
		if err != nil {
			continue
		}
		out = append(out, MaterialFile{Name: e.Name(), Chars: len([]rune(string(b)))})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// SaveMaterial 落一份材料（同名覆盖=重复上传即更新）。返回清单。
func SaveMaterial(p *pipeline.Project, name, content string) ([]MaterialFile, error) {
	n, err := sanitizeMaterialName(name)
	if err != nil {
		return nil, err
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, fmt.Errorf("材料内容为空")
	}
	if r := []rune(content); len(r) > materialMaxRunes {
		return nil, fmt.Errorf("材料过长（%d 字 > 单份上限 %d 字）——拆成多份上传", len(r), materialMaxRunes)
	}
	if err := os.MkdirAll(p.Artifact(MaterialsDir), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(p.Artifact(MaterialsDir+"/"+n), []byte(content), 0o644); err != nil {
		return nil, err
	}
	return ListMaterials(p), nil
}

// DeleteMaterial 删一份材料（按清单名）。返回清单。
func DeleteMaterial(p *pipeline.Project, name string) ([]MaterialFile, error) {
	n, err := sanitizeMaterialName(name)
	if err != nil {
		return nil, err
	}
	if err := os.Remove(p.Artifact(MaterialsDir + "/" + n)); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return ListMaterials(p), nil
}

// MaterialsText read_source 用的材料段：逐份带名拼接（超总帽截断并声明）。
// 没有材料返回空串——调用方按「无材料」走原路径。
func MaterialsText(p *pipeline.Project) string {
	mats := ListMaterials(p)
	if len(mats) == 0 {
		return ""
	}
	var b strings.Builder
	total := 0
	truncated := false
	for _, m := range mats {
		raw, err := os.ReadFile(p.Artifact(MaterialsDir + "/" + m.Name))
		if err != nil {
			continue
		}
		content := strings.TrimSpace(string(raw))
		if total+len([]rune(content)) > materialTotalCap {
			truncated = true
			break
		}
		fmt.Fprintf(&b, "\n〔用户上传材料：%s〕\n%s\n", m.Name, content)
		total += len([]rune(content))
	}
	if truncated {
		b.WriteString("\n（材料过多，超出部分未注入——请以已给材料为准）\n")
	}
	return strings.TrimSpace(b.String())
}
