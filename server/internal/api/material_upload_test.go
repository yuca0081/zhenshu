package api

// material_upload_test.go —— 真图素材上传接口（2026-11-02）：
// 上传 → 压规格落 assets/img → 登记进 materials.json 且带「必需」标记。

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func testImage(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2400, 1200))
	for y := 0; y < 1200; y++ {
		for x := 0; x < 2400; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 90, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// 上传走完：文件进 assets/img（长边压到 1600）、materials.json 记 required=true。
func TestUploadImage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dataDir := t.TempDir()
	projDir := filepath.Join(dataDir, "projects", "t1")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	s := &Server{DataDir: dataDir}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, _ := mw.CreateFormFile("image", "github 截图.png")
	part.Write(testImage(t))
	mw.WriteField("title", "GitHub 仓库实拍")
	mw.WriteField("note", "开场实证")
	mw.WriteField("required", "1")
	mw.Close()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/api/projects/t1/images", &body)
	c.Request.Header.Set("Content-Type", mw.FormDataContentType())
	c.Params = gin.Params{{Key: "id", Value: "t1"}}
	s.uploadImage(c)

	if w.Code != 200 {
		t.Fatalf("上传应成功: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Material struct {
			File     string `json:"file"`
			Required bool   `json:"required"`
			Title    string `json:"title"`
			Width    int    `json:"width"`
		} `json:"material"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Material.Required {
		t.Fatal("required 应落库为 true")
	}
	if resp.Material.Title != "GitHub 仓库实拍" {
		t.Fatalf("标题: %q", resp.Material.Title)
	}
	if !strings.HasPrefix(resp.Material.File, "assets/img/m-") || !strings.HasSuffix(resp.Material.File, ".jpg") {
		t.Fatalf("文件名应是内容哈希: %q", resp.Material.File)
	}
	// 原图 2400×1200 → 最长边压到 1600（等比：1600×800），原始尺寸记在元信息里
	if resp.Material.Width != 2400 {
		t.Fatalf("元信息应记原图宽度: %d", resp.Material.Width)
	}
	disk := filepath.Join(projDir, filepath.FromSlash(resp.Material.File))
	cfg, err := os.Stat(disk)
	if err != nil || cfg.Size() == 0 {
		t.Fatalf("图片应落盘: %v", err)
	}
	f, _ := os.Open(disk)
	defer f.Close()
	im, _, err := image.DecodeConfig(f)
	if err != nil {
		t.Fatal(err)
	}
	if im.Width != 1600 || im.Height != 800 {
		t.Fatalf("落盘应压到 1600×800，得到 %d×%d", im.Width, im.Height)
	}

	// 登记簿可读回
	mb, _ := os.ReadFile(filepath.Join(projDir, "assets", "materials.json"))
	var st struct {
		Materials []struct {
			File     string `json:"file"`
			Required bool   `json:"required"`
		} `json:"materials"`
	}
	if err := json.Unmarshal(mb, &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Materials) != 1 || !st.Materials[0].Required {
		t.Fatalf("materials.json 应有一条 required=true: %+v", st.Materials)
	}
}

// 非图片一律拒绝（只收 JPEG/PNG）。
func TestUploadImageRejectsNonImage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "projects", "t2"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := &Server{DataDir: dataDir}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, _ := mw.CreateFormFile("image", "notes.txt")
	part.Write([]byte("这不是图片"))
	mw.Close()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/api/projects/t2/images", &body)
	c.Request.Header.Set("Content-Type", mw.FormDataContentType())
	c.Params = gin.Params{{Key: "id", Value: "t2"}}
	s.uploadImage(c)
	if w.Code != 400 {
		t.Fatalf("非图片应 400，得到 %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dataDir, "projects", "t2", "assets")); err == nil {
		t.Fatal("被拒的上传不应建 assets 目录")
	}
}

// 「必需」开关可改：PUT 后 materials.json 跟着变。
func TestUpdateImageRequired(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dataDir := t.TempDir()
	projDir := filepath.Join(dataDir, "projects", "t3")
	if err := os.MkdirAll(filepath.Join(projDir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	seed := `{"materials":[{"file":"assets/img/a.jpg","source":"upload","title":"A","required":true}]}`
	if err := os.WriteFile(filepath.Join(projDir, "assets", "materials.json"), []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Server{DataDir: dataDir}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("PUT", "/api/projects/t3/images",
		strings.NewReader(`{"file":"assets/img/a.jpg","required":false}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: "t3"}}
	s.updateImage(c)
	if w.Code != 200 {
		t.Fatalf("更新应成功: %d %s", w.Code, w.Body.String())
	}
	mb, _ := os.ReadFile(filepath.Join(projDir, "assets", "materials.json"))
	if strings.Contains(string(mb), `"required": true`) {
		t.Fatalf("required 应被改成 false:\n%s", mb)
	}
}
