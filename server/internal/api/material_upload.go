package api

// material_upload.go —— 主题页「真图素材」上传（2026-11-02）。
//
// 事故背景：用户上传的截图此前没有任何入口，只能手工往 assets/materials.json 里塞
// 条目；而写作期对素材只给「可选加分」口径，模型 10 段 9 段整段忽略，成片里一张
// 截图都没有。本文件补的是入口与「必需」开关：
//
//	POST   /api/projects/:id/images          上传（multipart: image/title/note/required）
//	GET    /api/projects/:id/images          清单
//	DELETE /api/projects/:id/images?file=…   删除（连同 assets/img 下的文件）
//
// 落盘规格：最长边压到 1600（渲染用不着更高，原图 4K 截图会把页面撑重），统一存
// JPEG q88。文件名用内容哈希（m-xxxxxxxx.jpg）——ASCII 安全、天然去重、避免中文
// 文件名进 <img src> 后的编码问题；给人看的说明走 materials.json 的 title/note。

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	_ "image/png" // PNG 解码器注册（截图多是 PNG）
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	"webpage-video-agent/server/internal/pipeline"
	"webpage-video-agent/server/internal/produce"
)

const (
	materialImageMaxBytes = 20 << 20 // 单图 20MB
	materialImageMaxSide  = 1600     // 落盘最长边
	materialImageMaxRaw   = 8000     // 解码后原始最长边上限（防解码炸弹）
)

func (s *Server) listImages(c *gin.Context) {
	p, ok := s.project(c)
	if !ok {
		return
	}
	c.JSON(200, gin.H{"images": produce.ListMaterials(p)})
}

func (s *Server) uploadImage(c *gin.Context) {
	p, ok := s.project(c)
	if !ok {
		return
	}
	if s.prepLocked(p.ID) {
		c.JSON(409, gin.H{"error": "准备阶段/对话进行中——素材已在本链路被读取，等这轮结束后再改"})
		return
	}
	fh, err := c.FormFile("image")
	if err != nil {
		c.JSON(400, gin.H{"error": "缺图片文件（字段名 image）"})
		return
	}
	if fh.Size > materialImageMaxBytes {
		c.JSON(400, gin.H{"error": fmt.Sprintf("图片过大（%.1fMB），上限 %dMB", float64(fh.Size)/(1<<20), materialImageMaxBytes>>20)})
		return
	}
	f, err := fh.Open()
	if err != nil {
		c.JSON(500, gin.H{"error": "读不到上传内容: " + err.Error()})
		return
	}
	defer f.Close()

	src, format, err := image.Decode(f)
	if err != nil {
		c.JSON(400, gin.H{"error": "只收 JPEG/PNG 图片（" + err.Error() + "）"})
		return
	}
	sb := src.Bounds()
	if sb.Dx() > materialImageMaxRaw || sb.Dy() > materialImageMaxRaw {
		c.JSON(400, gin.H{"error": fmt.Sprintf("图片尺寸过大（%d×%d），上限 %dpx", sb.Dx(), sb.Dy(), materialImageMaxRaw)})
		return
	}

	// 压到最长边 1600，等比
	out := src
	w, h := sb.Dx(), sb.Dy()
	if long := max(w, h); long > materialImageMaxSide {
		if w >= h {
			nw := materialImageMaxSide
			nh := int(math.Round(float64(h) * float64(nw) / float64(w)))
			out = scaleNRGBA(src, nw, max(nh, 1))
		} else {
			nh := materialImageMaxSide
			nw := int(math.Round(float64(w) * float64(nh) / float64(h)))
			out = scaleNRGBA(src, max(nw, 1), nh)
		}
	}

	// 内容哈希做文件名：同图重复上传天然复用，不堆垃圾
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: 88}); err != nil {
		c.JSON(500, gin.H{"error": "编码失败: " + err.Error()})
		return
	}
	data := buf.Bytes()
	sum := sha256.Sum256(data)
	name := "m-" + hex.EncodeToString(sum[:])[:10] + ".jpg"
	rel := "assets/img/" + name

	dir := p.Artifact("assets/img")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		c.JSON(500, gin.H{"error": "建目录失败: " + err.Error()})
		return
	}
	if err := pipeline.WriteFileAtomic(filepath.Join(dir, name), data, 0o644); err != nil {
		c.JSON(500, gin.H{"error": "落盘失败: " + err.Error()})
		return
	}

	title := strings.TrimSpace(c.PostForm("title"))
	if title == "" {
		title = strings.TrimSpace(fh.Filename)
	}
	mat := produce.Material{
		File:     rel,
		Source:   "upload",
		Title:    pipeline.TruncRunes(title, 60),
		Note:     pipeline.TruncRunes(strings.TrimSpace(c.PostForm("note")), 120),
		Width:    sb.Dx(),
		Height:   sb.Dy(),
		Required: formBool(c.PostForm("required")),
	}
	list, err := produce.AddMaterials(p, []produce.Material{mat})
	if err != nil {
		c.JSON(500, gin.H{"error": "登记失败: " + err.Error()})
		return
	}
	p.Manifest("materials.image_saved", rel+" "+format+" "+mat.Title)
	c.JSON(200, gin.H{"ok": true, "material": mat, "materials": list})
}

func (s *Server) deleteImage(c *gin.Context) {
	p, ok := s.project(c)
	if !ok {
		return
	}
	if s.prepLocked(p.ID) {
		c.JSON(409, gin.H{"error": "准备阶段/对话进行中——素材已在本链路被读取，等这轮结束后再改"})
		return
	}
	file := strings.TrimSpace(c.Query("file"))
	if !strings.HasPrefix(file, "assets/img/") || strings.Contains(file, "..") {
		c.JSON(400, gin.H{"error": "file 必须是 assets/img/ 下的相对路径"})
		return
	}
	// 从登记簿摘除并重写；文件本身删掉（同内容重新上传会再生成同样名字）
	all := produce.ListMaterials(p)
	kept := make([]produce.Material, 0, len(all))
	found := false
	for _, m := range all {
		if m.File == file {
			found = true
			continue
		}
		kept = append(kept, m)
	}
	if !found {
		c.JSON(404, gin.H{"error": "素材不存在: " + file})
		return
	}
	if err := produce.ReplaceMaterials(p, kept); err != nil {
		c.JSON(500, gin.H{"error": "登记簿更新失败: " + err.Error()})
		return
	}
	_ = os.Remove(p.Artifact(filepath.FromSlash(file)))
	p.Manifest("materials.image_deleted", file)
	c.JSON(200, gin.H{"ok": true, "materials": kept})
}

// updateImage 改素材元信息（主要是「必需」开关）：不重传图，只改登记簿。
func (s *Server) updateImage(c *gin.Context) {
	p, ok := s.project(c)
	if !ok {
		return
	}
	if s.prepLocked(p.ID) {
		c.JSON(409, gin.H{"error": "准备阶段/对话进行中——素材已在本链路被读取，等这轮结束后再改"})
		return
	}
	var in struct {
		File     string  `json:"file"`
		Required *bool   `json:"required"`
		Title    *string `json:"title"`
		Note     *string `json:"note"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(400, gin.H{"error": "参数不合法: " + err.Error()})
		return
	}
	file := strings.TrimSpace(in.File)
	if file == "" {
		c.JSON(400, gin.H{"error": "缺 file"})
		return
	}
	all := produce.ListMaterials(p)
	found := false
	for i := range all {
		if all[i].File != file {
			continue
		}
		found = true
		if in.Required != nil {
			all[i].Required = *in.Required
		}
		if in.Title != nil {
			all[i].Title = pipeline.TruncRunes(strings.TrimSpace(*in.Title), 60)
		}
		if in.Note != nil {
			all[i].Note = pipeline.TruncRunes(strings.TrimSpace(*in.Note), 120)
		}
	}
	if !found {
		c.JSON(404, gin.H{"error": "素材不存在: " + file})
		return
	}
	if err := produce.ReplaceMaterials(p, all); err != nil {
		c.JSON(500, gin.H{"error": "登记簿更新失败: " + err.Error()})
		return
	}
	p.Manifest("materials.image_updated", file)
	c.JSON(200, gin.H{"ok": true, "materials": all})
}

// formBool 表单布尔：1/true/on/yes 都算真。
func formBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
}

// scaleNRGBA 等比缩放（双线性）。没引 x/image：项目依赖里没有，也不想为一个上传
// 入口引新模块；截图多为大面积纯色 + 细字，双线性足够，缩完再编 JPEG 看不出差。
func scaleNRGBA(src image.Image, w, h int) *image.NRGBA {
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	// 先落到 NRGBA 再按像素索引：比逐点 At() 的类型断言快一个数量级
	flat := image.NewNRGBA(image.Rect(0, 0, sw, sh))
	draw.Draw(flat, flat.Bounds(), src, sb.Min, draw.Src)
	nrgba := image.NewNRGBA(image.Rect(0, 0, w, h))
	sx := float64(sw) / float64(w)
	sy := float64(sh) / float64(h)
	for y := 0; y < h; y++ {
		fy := (float64(y)+0.5)*sy - 0.5
		y0 := int(math.Floor(fy))
		wy := fy - float64(y0)
		if y0 < 0 {
			y0, wy = 0, 0
		}
		y1 := y0 + 1
		if y1 >= sh {
			y1 = sh - 1
		}
		for x := 0; x < w; x++ {
			fx := (float64(x)+0.5)*sx - 0.5
			x0 := int(math.Floor(fx))
			wx := fx - float64(x0)
			if x0 < 0 {
				x0, wx = 0, 0
			}
			x1 := x0 + 1
			if x1 >= sw {
				x1 = sw - 1
			}
			o00 := flat.PixOffset(x0, y0)
			o10 := flat.PixOffset(x1, y0)
			o01 := flat.PixOffset(x0, y1)
			o11 := flat.PixOffset(x1, y1)
			for k := 0; k < 4; k++ {
				v00 := float64(flat.Pix[o00+k])
				v10 := float64(flat.Pix[o10+k])
				v01 := float64(flat.Pix[o01+k])
				v11 := float64(flat.Pix[o11+k])
				top := v00 + (v10-v00)*wx
				bot := v01 + (v11-v01)*wx
				nrgba.Pix[nrgba.PixOffset(x, y)+k] = uint8(top + (bot-top)*wy + 0.5)
			}
		}
	}
	return nrgba
}
