package produce

// audit_materials.go —— 段审/终审要对照的「真图素材」装配（2026-11-03）。
//
// 事故：素材链路测试（p20261102-204948）里截图声明 942×634（28.8%，过闸门），
// 但 79% 面积被 90% 不透明的米色遮罩盖住，观众只看见一条 6% 的窄带，且旁白点名的
// 「5.1k stars / DeepSeek Harness Team」是 HTML 叠字假证据；真截图的 Star 徽章在
// 遮罩底下。落地闸门只核声明尺寸，这一层看不见就得靠判卷官——本文件把素材原图与
// 落地要求喂给段审/终审，让「素材到底看不看得见」成为可判的题。

import (
	"path/filepath"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/pipeline"
	"webpage-video-agent/server/internal/review"
)

// auditMaterialImages 素材原图注入尺寸：判「这张图有没有被盖住、关键内容在不在」，
// 720 宽足够看清布局与文字块位置；再大只是白付载荷。
const (
	auditMaterialImgW = 720
	auditMaterialImgQ = 80
)

// segAuditMaterials 段审材料：本段认领的必需素材 + 原图 data URL。
// 返回的两个切片逐条对应（同序同长）：原图读不出来时 URL 留空，由段审侧过滤
// 并在 kickoff 里省略「原图 = 图N」指针——不许出现指针指向别的图的错位。
func segAuditMaterials(p *pipeline.Project, seg *contract.Segment) ([]review.MaterialRef, []llm.ImgRef) {
	if p == nil || seg == nil {
		return nil, nil
	}
	req := requiredMaterialsForSeg(p, seg)
	if len(req) == 0 {
		return nil, nil
	}
	mats := make([]review.MaterialRef, 0, len(req))
	imgs := make([]llm.ImgRef, 0, len(req))
	for _, m := range req {
		mats = append(mats, review.MaterialRef{
			File: m.File, Title: m.Title, Role: m.Role,
			At: m.At, Hold: m.Hold, Present: m.Present,
			AreaMin: contract.ImageAreaMin(m.Role), SegID: seg.ID,
		})
		imgs = append(imgs, llm.ImgRef{
			URL:   materialDataURL(p, m.File),
			Path:  m.File,
			Label: "素材原图 " + filepath.Base(m.File),
		})
	}
	return mats, imgs
}

// filmAuditMaterials 终审清单：全片被分镜认领的真图（含认领段与落地要求），不注入
// 原图（终审帧多，再加图太贵）——判「素材有没有出现、认不认得出」用帧就够。
func filmAuditMaterials(p *pipeline.Project, sb *contract.Storyboard) []review.MaterialRef {
	if p == nil || sb == nil {
		return nil
	}
	var out []review.MaterialRef
	for i := range sb.Segments {
		seg := &sb.Segments[i]
		for _, m := range requiredMaterialsForSeg(p, seg) {
			out = append(out, review.MaterialRef{
				File: m.File, Title: m.Title, Role: m.Role,
				At: m.At, Hold: m.Hold, Present: m.Present,
				AreaMin: contract.ImageAreaMin(m.Role), SegID: seg.ID,
			})
		}
	}
	return out
}

// materialDataURL 项目内素材图 → 多模态 data URL（720 宽 JPEG）；读不出返回空串。
func materialDataURL(p *pipeline.Project, rel string) string {
	if p == nil || rel == "" {
		return ""
	}
	u, err := pipeline.ShrinkImageDataURL(p.Artifact(rel), auditMaterialImgW, auditMaterialImgQ)
	if err != nil || !llm.ValidImageData(u) {
		return ""
	}
	return u
}
