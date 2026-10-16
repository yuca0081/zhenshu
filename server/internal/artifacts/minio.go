package artifacts

// MinIO 产物后端：正本在对象存储，本地目录只是工作区+缓存。与 stylelib/media.go 的
// minioMedia 同源同纪律，两处差异是刻意的：
//   - 独立桶（MINIO_DATA_BUCKET，默认 zhenshu-data）：产物会反复覆写（段片重渲、终片
//     重混），不开 versioning——桶级 versioning 会把每个历史版本整份留在同一块磁盘上，
//     与「省空间」的初衷背道而驰；风格库桶（zhenshu）的 versioning 后悔药不适用这里。
//   - 不设 PG 前置：风格库要求 PG 已接是因为它的账本挂靠 PG；产物层没有账（前缀即
//     清单），只依赖 MinIO 自身存活。

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

type minioStore struct {
	cli    *minio.Client
	bucket string
	root   string // DATA_DIR：Localize 的落地根
}

// NewMinio 构造 MinIO 后端。存活探测 fail-fast（同 store.Open 语义：正本在远端，
// 连不上还静默降级本地会让人以为数据都在）；桶不存在则建（幂等）。
func NewMinio(endpoint, accessKey, secretKey, bucket string, secure bool, dataDir string) (*minioStore, error) {
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
		return nil, fmt.Errorf("MinIO 不可达（%v）：修好对象存储再启动——产物正本在 MinIO", err)
	}
	if !exists {
		if err := cli.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
			return nil, fmt.Errorf("建 MinIO 桶 %s 失败: %w", bucket, err)
		}
		log.Printf("[artifacts] MinIO 桶 %s 不存在，已创建", bucket)
	}
	log.Printf("[artifacts] MinIO 已连接 bucket=%s（产物正本后端）", bucket)
	return &minioStore{cli: cli, bucket: bucket, root: dataDir}, nil
}

func (s *minioStore) Available() bool { return true }

func (s *minioStore) LocalPath(key string) string {
	return filepath.Join(s.root, filepath.FromSlash(key))
}

func contentType(name string) string {
	switch {
	case strings.HasSuffix(name, ".mp4"):
		return "video/mp4"
	case strings.HasSuffix(name, ".wav"):
		return "audio/wav"
	case strings.HasSuffix(name, ".mp3"):
		return "audio/mpeg"
	case strings.HasSuffix(name, ".png"):
		return "image/png"
	case strings.HasSuffix(name, ".jpg"), strings.HasSuffix(name, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(name, ".webp"):
		return "image/webp"
	case strings.HasSuffix(name, ".gif"):
		return "image/gif"
	case strings.HasSuffix(name, ".json"):
		return "application/json"
	case strings.HasSuffix(name, ".html"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(name, ".txt"), strings.HasSuffix(name, ".md"):
		return "text/plain; charset=utf-8"
	case strings.HasSuffix(name, ".ttf"):
		return "font/ttf"
	case strings.HasSuffix(name, ".otf"):
		return "font/otf"
	default:
		return "application/octet-stream"
	}
}

// Put 覆盖式上传。超时只罩单次传输（母版/终片数百 MB 量级，给足 30min）。
func (s *minioStore) Put(key, srcPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	_, err := s.cli.FPutObject(ctx, s.bucket, key, srcPath,
		minio.PutObjectOptions{ContentType: contentType(key)})
	return err
}

// Open 拿 minio.Object（自带 Seek）喂 ServeContent。超时只罩建立阶段——对象流
// 生命周期覆盖整个 HTTP 响应，绑短 ctx 会被中途掐断（stylelib 同款注释同款坑）。
func (s *minioStore) Open(key string) (io.ReadSeekCloser, int64, time.Time, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	info, err := s.cli.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return nil, 0, time.Time{}, err
	}
	obj, err := s.cli.GetObject(context.Background(), s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, 0, time.Time{}, err
	}
	return obj, info.Size, info.LastModified, nil
}

func (s *minioStore) Stat(key string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := s.cli.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	return err == nil
}

func (s *minioStore) Delete(key string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.cli.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
}

func (s *minioStore) List(prefix string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var out []string
	for obj := range s.cli.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
		Prefix: prefix, Recursive: true,
	}) {
		if obj.Err != nil {
			return nil, obj.Err
		}
		out = append(out, obj.Key)
	}
	return out, nil
}

func (s *minioStore) DeletePrefix(prefix string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	objects := make(chan minio.ObjectInfo)
	errCh := s.cli.RemoveObjects(ctx, s.bucket, objects, minio.RemoveObjectsOptions{})
	go func() {
		defer close(objects)
		for obj := range s.cli.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
			Prefix: prefix, Recursive: true,
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

// Localize 读穿缓存：本地有直接回；缺则拉对象按原布局落地（本地排查仍是一处看全）。
func (s *minioStore) Localize(key string) (string, error) {
	local := s.LocalPath(key)
	if st, err := os.Stat(local); err == nil && !st.IsDir() {
		return local, nil
	}
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		return "", err
	}
	if err := s.Fetch(key, local); err != nil {
		return "", err
	}
	return local, nil
}

func (s *minioStore) Fetch(key, dstPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	return s.cli.FGetObject(ctx, s.bucket, key, dstPath, minio.GetObjectOptions{})
}
