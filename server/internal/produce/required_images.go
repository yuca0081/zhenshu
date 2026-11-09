package produce

// required_images.go —— 必需素材落地（2026-11-02「截图没进片」事故的修复）。
//
// 事故形态：素材登记与提示词注入都在，但写作期只有一段「可选加分」的旁挂备注
// （materials.go materialsNote 的「用不用…没有合适的就一张都不用」），模型 10 段
// 里 9 段整段不写 <img>，唯一写了的那张是 300×97 的角标，成片等于没有截图。
//
// 修法三跳（本文件负责后两跳）：
//  1. 上传时勾「必需」→ materials.json 的 required（api/material_upload.go）；
//  2. 分镜阶段必须认领 → contract.ValidateShotImages 的覆盖校验；
//  3. 写作期换成硬约束措辞（requiredMaterialsNote）+ 落地闸门（requiredImageCheck）。
//
// 闸门用「声明尺寸算面积占比」判定，不做像素级渲染核对：静态、便宜、可解释，
// 且正好卡掉事故里那种「缩成角标也算用了」的形态。

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/pipeline"
)

// RequiredMaterial 本段必需素材：登记簿条目 + 分镜给它的呈现约束。
type RequiredMaterial struct {
	Material
	Role    string  `json:"role"`
	At      float64 `json:"at,omitempty"`
	Hold    float64 `json:"hold,omitempty"`
	Present string  `json:"present,omitempty"`
}

// requiredMaterialsForSeg 分镜在本段认领的必需素材（seg.Shot.Images）。
// 素材文件在登记簿里查不到时仍保留条目（file 原样）——闸门要能报出「文件不存在」，
// 而不是静默跳过。
func requiredMaterialsForSeg(p *pipeline.Project, seg *contract.Segment) []RequiredMaterial {
	if seg == nil || seg.Shot == nil || len(seg.Shot.Images) == 0 {
		return nil
	}
	byFile := map[string]Material{}
	if st := loadMaterials(p); st != nil {
		for _, m := range st.Materials {
			byFile[m.File] = m
		}
	}
	var out []RequiredMaterial
	for _, im := range seg.Shot.Images {
		f := strings.TrimSpace(im.File)
		if f == "" {
			continue
		}
		base, ok := byFile[f]
		if !ok {
			base = Material{File: f, Title: f}
		}
		out = append(out, RequiredMaterial{
			Material: base,
			Role:     im.Role,
			At:       im.At,
			Hold:     im.Hold,
			Present:  im.Present,
		})
	}
	return out
}

// optionalMaterials 去掉已被分镜认领的条目：同一张图不再用「可选」语气重复注入。
func optionalMaterials(mats []Material, req []RequiredMaterial) []Material {
	if len(req) == 0 {
		return mats
	}
	claimed := map[string]bool{}
	for _, r := range req {
		claimed[r.File] = true
	}
	var out []Material
	for _, m := range mats {
		if claimed[m.File] {
			continue
		}
		out = append(out, m)
	}
	return out
}

// requiredFileList 登记簿里勾了「必需」的素材路径（按登记顺序）。
func requiredFileList(p *pipeline.Project) []string {
	var out []string
	if st := loadMaterials(p); st != nil {
		for _, m := range st.Materials {
			if m.Required && strings.TrimSpace(m.File) != "" {
				out = append(out, m.File)
			}
		}
	}
	return out
}

// claimedFileSet 分镜里被某一镜认领的素材集合（shot.images）。
func claimedFileSet(p *pipeline.Project) map[string]bool {
	out := map[string]bool{}
	sb, err := p.LoadStoryboard()
	if err != nil || sb == nil {
		return out
	}
	for _, seg := range sb.Segments {
		if seg.Shot == nil {
			continue
		}
		for _, im := range seg.Shot.Images {
			if f := strings.TrimSpace(im.File); f != "" {
				out[f] = true
			}
		}
	}
	return out
}

// unclaimedRequired 勾了「必需」但分镜里没有任何一镜认领的素材。
//
// 认领发生在分镜阶段；用户在分镜确认之后才把某张图改成「必需」时，制作阶段就
// 无从知道该把它放进哪一段。这里不静默放过（那又会出一支「一张截图都没有」的
// 成片），由调用方落 manifest + 推到进度栏，让用户看见。
func unclaimedRequired(p *pipeline.Project) []string {
	req := requiredFileList(p)
	if len(req) == 0 {
		return nil
	}
	claimed := claimedFileSet(p)
	var lost []string
	for _, f := range req {
		if !claimed[f] {
			lost = append(lost, f)
		}
	}
	return lost
}

// materialsForShot 本段要看的全部素材：登记簿里首次申请段是本段的（老 Met 路径）
// ∪ 分镜在本段认领的（上传素材路径）。按 file 去重保序——视觉副驾细读只跑一次。
func materialsForShot(p *pipeline.Project, seg *contract.Segment) []Material {
	var out []Material
	seen := map[string]bool{}
	add := func(m Material) {
		if m.File == "" || seen[m.File] {
			return
		}
		seen[m.File] = true
		out = append(out, m)
	}
	if seg != nil {
		for _, m := range materialsForSeg(p, seg.ID) {
			add(m)
		}
	}
	for _, r := range requiredMaterialsForSeg(p, seg) {
		add(r.Material)
	}
	return out
}

// requiredMaterialsNote 本段必需素材块：硬约束措辞（对比 materialsNote 的「可选加分」）。
// 这行字是修复的核心——把「用不用随你」换成「必须落地 + 可量化的下限」。
func requiredMaterialsNote(req []RequiredMaterial) string {
	if len(req) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## 本段必需素材（必须出现在画面上且看得清；缺图/缩成角标/被遮罩盖住/只在末帧闪一下 = 返工）\n")
	for _, r := range req {
		line := "- `" + r.File + "`"
		if r.Title != "" && r.Title != r.File {
			line += "｜" + r.Title
		}
		b.WriteString(line + "\n")
		b.WriteString(fmt.Sprintf("  - 占比：%s（面积 ≥ 画面 %.0f%%）\n",
			areaDesc(r.Role), contract.ImageAreaMin(r.Role)*100))
		if r.At > 0 || r.Hold > 0 {
			b.WriteString(fmt.Sprintf("  - 时机：%.1fs 入场，至少停留 %.1fs\n", r.At, r.Hold))
		}
		if s := strings.TrimSpace(r.Present); s != "" {
			b.WriteString("  - 摆法：" + s + "\n")
		}
		if s := strings.TrimSpace(r.Note); s != "" {
			b.WriteString("  - 用途：" + s + "\n")
		}
	}
	b.WriteString(`- 引用语法：<img src="assets/img/文件名" style="...width:Npx;height:Npx;">，**必须显式写 width/height（px）**——渲染侧按声明尺寸核占比
- 可见面积按「没被盖住的部分」算：遮罩/蒙层/半透明色块/角色身体/文字叠层压住的部分不算面积。声明 942×634 却拿 90% 不透明遮罩盖掉 79%、观众只剩一条窄带 = 返工（2026-11-02 实测形态）
- 禁止：opacity:0 且不做入场动画、缩成角标/装饰纹理、裁到看不清内容、当背景垫图淡到看不见
- 禁止用 HTML 文字复刻素材里的关键内容（数字/署名/界面元素）：叠字只能做标注，旁白点名的证据必须从原图里读得出来——拿叠字「代说」证据等于没放素材
- 遮罩只许压次要区域且整片透出（不透明度 ≤30%）；要展示的证据区一块不许盖，角色/道具/字幕不得站在素材上遮住主体
- 素材入场 0.5s 内就应可辨，且至少停留满分镜要求的时长；不许只在末帧闪一下
- 这张图是「证据」不是「装饰」：观众要能在竖屏上认出它是什么，并认出旁白点名的那处证据
`)
	return b.String()
}

// areaDesc 占比档位的人话说明。
func areaDesc(role string) string {
	if strings.ToLower(strings.TrimSpace(role)) == "hero" {
		return "hero 主画面"
	}
	return "inset 贴图/画中画"
}

// ── 落地闸门 ─────────────────────────────────────────────────────

var (
	rootSizeRe  = regexp.MustCompile(`data-width\s*=\s*["'](\d+)["'][^>]*data-height\s*=\s*["'](\d+)["']`)
	rootSizeRe2 = regexp.MustCompile(`data-height\s*=\s*["'](\d+)["'][^>]*data-width\s*=\s*["'](\d+)["']`)
	pxDimRe     = regexp.MustCompile(`(?i)(width|height)\s*:\s*([0-9.]+)px`)
	attrDimRe   = regexp.MustCompile(`(?i)\b(width|height)\s*=\s*["']([0-9.]+)(?:px)?["']`)
)

// requiredImageCheck 段级闸门：分镜在本段认领的必需素材必须真的落在 HTML 里，
// 且声明面积达标。返回空 = 过。
//
// 只查「声明尺寸」不查像素：便宜、确定、可在修复轮里逐条回喂；渲染期的实际
// 呈现由抽帧复核兜底（reports/audit/frames）。
func requiredImageCheck(p *pipeline.Project, html, segID string) []string {
	sb, err := p.LoadStoryboard()
	if err != nil || sb == nil {
		return nil
	}
	var seg *contract.Segment
	for i := range sb.Segments {
		if sb.Segments[i].ID == segID {
			seg = &sb.Segments[i]
			break
		}
	}
	if seg == nil || seg.Shot == nil || len(seg.Shot.Images) == 0 {
		return nil
	}
	rootW, rootH := rootSize(html)
	var errs []string
	for _, im := range seg.Shot.Images {
		file := strings.TrimSpace(im.File)
		if file == "" {
			continue
		}
		tag := findImgTag(html, file)
		if tag == "" {
			errs = append(errs, fmt.Sprintf("必需素材没有出现在画面里：%s（分镜已认领 role=%s）——必须写 <img src=%q> 并显式给 width/height", file, im.Role, file))
			continue
		}
		w, h := tagDims(tag)
		if w <= 0 || h <= 0 {
			errs = append(errs, fmt.Sprintf("必需素材 %s 的 <img> 没有显式 width/height（px），渲染侧无法核占比，也无法保证不被压扁", file))
			continue
		}
		if rootW > 0 && rootH > 0 {
			ratio := (w * h) / float64(rootW*rootH)
			// 面积下限之外再给一条并列判据：占满画面宽度也算落地。竖屏下宽扁素材
			// （550×178 的官方架构图、3:1 的表格截图）面积占比受画布宽度封顶，
			// 单看面积数学上永远够不到 hero 线（1266px > 1080px），只留面积一条
			// 会逼模型反复返工。
			wide := w >= float64(rootW)*0.8
			if min := contract.ImageAreaMin(im.Role); ratio < min && !(wide && im.Role == "hero") {
				errs = append(errs,
					fmt.Sprintf("必需素材 %s 只占画面 %.1f%%（%dx%d / %dx%d），%s 要求 ≥ %.0f%% 面积（或占满画面宽度 80%%）——这就是「素材在片里但观众看不见」的形态，放大或改用合适档位",
						file, ratio*100, int(w), int(h), rootW, rootH, areaDesc(im.Role), min*100))
			}
		}
	}
	return errs
}

// rootSize 舞台画幅（data-width/data-height，属性顺序不固定）。
func rootSize(html string) (int, int) {
	if m := rootSizeRe.FindStringSubmatch(html); m != nil {
		w, _ := strconv.Atoi(m[1])
		h, _ := strconv.Atoi(m[2])
		return w, h
	}
	if m := rootSizeRe2.FindStringSubmatch(html); m != nil {
		h, _ := strconv.Atoi(m[1])
		w, _ := strconv.Atoi(m[2])
		return w, h
	}
	return 0, 0
}

// findImgTag 取引用该文件的 <img ...> 标签原文（没有返回空）。
func findImgTag(html, file string) string {
	re := regexp.MustCompile(`(?is)<img\b[^>]*src\s*=\s*["']` + regexp.QuoteMeta(file) + `["'][^>]*>`)
	return re.FindString(html)
}

// tagDims 标签声明的宽高：先 style 里的 width/height:NNpx，退回 width/height 属性。
// style 后写者胜（与 CSS 层叠一致：内联样式里后声明的覆盖先声明的）。
func tagDims(tag string) (float64, float64) {
	var w, h float64
	if style := styleAttr(tag); style != "" {
		for _, m := range pxDimRe.FindAllStringSubmatch(style, -1) {
			v, _ := strconv.ParseFloat(m[2], 64)
			if strings.EqualFold(m[1], "width") {
				w = v
			} else {
				h = v
			}
		}
	}
	if w == 0 || h == 0 {
		for _, m := range attrDimRe.FindAllStringSubmatch(tag, -1) {
			v, _ := strconv.ParseFloat(m[2], 64)
			if strings.EqualFold(m[1], "width") && w == 0 {
				w = v
			}
			if strings.EqualFold(m[1], "height") && h == 0 {
				h = v
			}
		}
	}
	return w, h
}

// styleAttr 取 style="..." 的内容（引号单双都认）。
func styleAttr(tag string) string {
	re := regexp.MustCompile(`(?is)\bstyle\s*=\s*("([^"]*)"|'([^']*)')`)
	m := re.FindStringSubmatch(tag)
	if m == nil {
		return ""
	}
	if m[2] != "" {
		return m[2]
	}
	return m[3]
}
