package stylelib

// 风格媒体后端（货）：原片快照 + 抽帧的存取，docs/style-library-cloud-storage.md §4。
// 两种实现：minioMedia（对象存储 zhenshu/style-media/<id>/，正本在 MinIO、本地目录只是
// Localize 读穿缓存）与 dirMedia（本地目录 data/library/style_media/<id>/，现状行为，
// demo 容器与没配 MinIO 的环境用）。
//
// key/文件名规则统一在这里：payload 里只存文件名，前缀拼装是后端实现细节——
// 「URL 永不入库」的对偶是「key 拼装永不出后端」。

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// MediaStore 风格媒体后端。所有方法以 (styleID, name) 寻址单个文件，以 styleID 寻址整前缀。
type MediaStore interface {
	// Put 覆盖式写入（来源是本地文件；幂等跳过由调用方先 Stat 决定）。
	Put(styleID, name, srcPath string) error
	// Open 读对象本体（HTTP 回源；返回值可直接喂 http.ServeContent）。
	Open(styleID, name string) (rc io.ReadSeekCloser, size int64, modTime time.Time, err error)
	Stat(styleID, name string) bool
	// List 该条目全部文件名（写入侧已过白名单，实现不做二次过滤）。
	List(styleID string) ([]string, error)
	DeletePrefix(styleID string) error
	// Localize 确保该文件在本地盘可得并返回本地绝对路径——简报证据帧要喂多模态，
	// 必须落地成文件。本地后端 = 直取路径；MinIO 后端 = 读穿到缓存目录。
	Localize(styleID, name string) (string, error)
}

// ValidMediaName 媒体文件名白名单：原片 video.mp4 / 封面 cover.png / 抽帧 frame-*.png。
// 抽帧名带小数点时间戳（extract_video_features.py 产物，如 frame-0.8.png），
// 字符集放宽到 [a-z0-9._-]，但斜杠与「..」段坚决拒收（防路径穿越——本地拼路径、
// MinIO 拼 key 都吃这个名单）。既是写入过滤也是读取防穿越，api 复用。
func ValidMediaName(name string) bool {
	if name == "video.mp4" || name == "cover.png" {
		return true
	}
	if !strings.HasPrefix(name, "frame-") || !strings.HasSuffix(name, ".png") {
		return false
	}
	if name == "frame-.png" {
		return false // 帧名主体为空
	}
	body := name[len("frame-"):]
	if strings.ContainsAny(name, `/\`) || strings.Contains(body, "..") {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

func isSlugName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// ── 本地目录实现（现状行为）────────────────────────────────

// dirMedia 本地目录后端：root = data/library，媒体在 root/style_media/<id>/。
type dirMedia struct{ root string }

func newDirMedia(root string) *dirMedia { return &dirMedia{root: root} }

func (m *dirMedia) dir(id string) string { return filepath.Join(m.root, "style_media", id) }

// Put 覆盖式落盘：硬链接优先（同卷零拷贝），跨卷/不支持降级整拷。cover 先删旧——
// 硬链接占位会挡 os.Link。
func (m *dirMedia) Put(styleID, name, srcPath string) error {
	dstDir := m.dir(styleID)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}
	dst := filepath.Join(dstDir, name)
	_ = os.Remove(dst)
	if err := os.Link(srcPath, dst); err == nil {
		return nil
	}
	return copyFile(srcPath, dst)
}

func (m *dirMedia) Open(styleID, name string) (io.ReadSeekCloser, int64, time.Time, error) {
	f, err := os.Open(filepath.Join(m.dir(styleID), name))
	if err != nil {
		return nil, 0, time.Time{}, err
	}
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		f.Close()
		return nil, 0, time.Time{}, fmt.Errorf("stat %s: %w", name, err)
	}
	return f, st.Size(), st.ModTime(), nil
}

func (m *dirMedia) Stat(styleID, name string) bool {
	st, err := os.Stat(filepath.Join(m.dir(styleID), name))
	return err == nil && !st.IsDir()
}

func (m *dirMedia) List(styleID string) ([]string, error) {
	entries, err := os.ReadDir(m.dir(styleID))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

func (m *dirMedia) DeletePrefix(styleID string) error {
	return os.RemoveAll(m.dir(styleID))
}

func (m *dirMedia) Localize(styleID, name string) (string, error) {
	p := filepath.Join(m.dir(styleID), name)
	if st, err := os.Stat(p); err != nil || st.IsDir() {
		return "", fmt.Errorf("媒体 %s 不存在", name)
	}
	return p, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

// ── MinIO 实现（正本在对象存储，本地目录是 Localize 缓存）──

// mediaKeyPrefix 桶内前缀（0005 迁移与 docs/style-library-cloud-storage.md §4 同源）。
const mediaKeyPrefix = "style-media/"

// minioMedia MinIO 后端。cache = data/library：Localize 把对象读穿到
// cache/style_media/<id>/，与本地后端同址——排查时一个地方看全部媒体。
type minioMedia struct {
	cli    *minio.Client
	bucket string
	cache  string
}

// NewMinioMedia MinIO 客户端构造 + 存活探测（fail-fast 同 store.Open 语义：
// 配了连不上直接报错——媒体正本在 MinIO，静默降级会让人以为库还在）。
// 桶不存在则建（幂等）并开 versioning（误删后悔药；SetBucketVersioning 幂等可重跑）。
func NewMinioMedia(endpoint, accessKey, secretKey, bucket string, secure bool, cacheDir string) (*minioMedia, error) {
	cli, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: secure,
	})
	if err != nil {
		return nil, fmt.Errorf("MinIO 客户端构造失败: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	exists, err := cli.BucketExists(ctx, bucket)
	if err != nil {
		return nil, fmt.Errorf("MinIO 不可达（%v）：修好对象存储再启动——风格库媒体正本在 MinIO", err)
	}
	if !exists {
		if err := cli.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
			return nil, fmt.Errorf("建 MinIO 桶 %s 失败: %w", bucket, err)
		}
		log.Printf("[stylelib] MinIO 桶 %s 不存在，已创建", bucket)
	}
	if err := cli.SetBucketVersioning(ctx, bucket, minio.BucketVersioningConfiguration{Status: "Enabled"}); err != nil {
		return nil, fmt.Errorf("桶 %s 开 versioning 失败（误删后悔药必须上）: %w", bucket, err)
	}
	log.Printf("[stylelib] MinIO 已连接 bucket=%s", bucket)
	return &minioMedia{cli: cli, bucket: bucket, cache: cacheDir}, nil
}

func (m *minioMedia) key(styleID, name string) string {
	return mediaKeyPrefix + styleID + "/" + name
}

func (m *minioMedia) prefix(styleID string) string {
	return mediaKeyPrefix + styleID + "/"
}

// contentType 按扩展名给对象类型（白名单内只有 png/mp4，兜底 octet-stream）。
func contentType(name string) string {
	if strings.HasSuffix(name, ".png") {
		return "image/png"
	}
	if strings.HasSuffix(name, ".mp4") {
		return "video/mp4"
	}
	return "application/octet-stream"
}

func (m *minioMedia) Put(styleID, name, srcPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute) // 原片快照最大 200MB
	defer cancel()
	_, err := m.cli.FPutObject(ctx, m.bucket, m.key(styleID, name), srcPath,
		minio.PutObjectOptions{ContentType: contentType(name)})
	return err
}

// Open 拿 minio.Object（自带 Seek）喂 ServeContent。超时只罩建立阶段——对象流
// 生命周期覆盖整个 HTTP 响应，绑短 ctx 会被中途掐断。
func (m *minioMedia) Open(styleID, name string) (io.ReadSeekCloser, int64, time.Time, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	info, err := m.cli.StatObject(ctx, m.bucket, m.key(styleID, name), minio.StatObjectOptions{})
	if err != nil {
		return nil, 0, time.Time{}, err
	}
	obj, err := m.cli.GetObject(context.Background(), m.bucket, m.key(styleID, name), minio.GetObjectOptions{})
	if err != nil {
		return nil, 0, time.Time{}, err
	}
	return obj, info.Size, info.LastModified, nil
}

func (m *minioMedia) Stat(styleID, name string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := m.cli.StatObject(ctx, m.bucket, m.key(styleID, name), minio.StatObjectOptions{})
	return err == nil
}

func (m *minioMedia) List(styleID string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var out []string
	for obj := range m.cli.ListObjects(ctx, m.bucket, minio.ListObjectsOptions{
		Prefix: m.prefix(styleID), Recursive: true,
	}) {
		if obj.Err != nil {
			return nil, obj.Err
		}
		if name := strings.TrimPrefix(obj.Key, m.prefix(styleID)); name != "" {
			out = append(out, name)
		}
	}
	return out, nil
}

// DeletePrefix 整前缀删除。RemoveObjects 批量提交，部分失败只报 error——调用方
// 不回滚账（账已先行删除，孤儿货无害）。
func (m *minioMedia) DeletePrefix(styleID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	objects := make(chan minio.ObjectInfo)
	errCh := m.cli.RemoveObjects(ctx, m.bucket, objects, minio.RemoveObjectsOptions{})
	go func() {
		defer close(objects)
		for obj := range m.cli.ListObjects(ctx, m.bucket, minio.ListObjectsOptions{
			Prefix: m.prefix(styleID), Recursive: true,
		}) {
			if obj.Err == nil {
				objects <- obj
			}
		}
	}()
	var firstErr error
	for e := range errCh {
		if e.Err != nil && firstErr == nil {
			firstErr = e.Err
		}
	}
	return firstErr
}

// Localize 读穿缓存：本地有直接回；没有则拉对象落地（与本地后端同址，排查一处看全）。
func (m *minioMedia) Localize(styleID, name string) (string, error) {
	local := filepath.Join(m.cache, "style_media", styleID, name)
	if st, err := os.Stat(local); err == nil && !st.IsDir() {
		return local, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		return "", err
	}
	if err := m.cli.FGetObject(ctx, m.bucket, m.key(styleID, name), local, minio.GetObjectOptions{}); err != nil {
		return "", err
	}
	return local, nil
}
