// remote.go — 素材库远程镜像（PG 目录 + MinIO 对象，docs/asset-library.md §9 预留位的落地）。
//
// 定位是「镜像」不是「搬迁」：磁盘 catalog/文件仍是渲染事实源（Python 侧只读本地），
// PG assets 表 + MinIO 对象做异地冗余与跨机引导——容器重建/换机时 SyncRemote 把
// 远端拉回本地。远端不可达不挡业务：入库只 warn，下次启动对账补齐。
package assetlib

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// RemoteConfig 与 Config 同源（PG_DSN 复用案卷库连接）。
type RemoteConfig struct {
	DSN       string
	Endpoint  string // host:port，空 = 不启用镜像
	AccessKey string
	SecretKey string
	Bucket    string // 缺省 zhenshu
	Region    string // 缺省 cn-north-1（MinIO 建桶时定的）
	Secure    bool
	Table     string // PG 表名，缺省 assets；集成测试用 assets_test 隔离
}

// objectPrefix 库内对象统一前缀（同一 bucket 可能将来放别的资产域）。
const objectPrefix = "library/"

type Remote struct {
	db     *gorm.DB
	mc     *minio.Client
	bucket string
	table  string // PG 表名（默认 assets）
}

// OpenRemote 连 PG + MinIO 并幂等建表/确认桶。任一失败返回错误（调用方降级为纯本地）。
func OpenRemote(cfg RemoteConfig) (*Remote, error) {
	if cfg.DSN == "" || cfg.Endpoint == "" {
		return nil, errors.New("PG_DSN 与 MINIO_ENDPOINT 需同时配置")
	}
	if cfg.Bucket == "" {
		cfg.Bucket = "zhenshu"
	}
	if cfg.Region == "" {
		cfg.Region = "cn-north-1"
	}
	if cfg.Table == "" {
		cfg.Table = "assets"
	}
	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  cfg.DSN + " connect_timeout=3",
		PreferSimpleProtocol: true,
	}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, fmt.Errorf("assets PG: %w", err)
	}
	if err := db.Exec(fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		id          TEXT PRIMARY KEY,
		type        TEXT NOT NULL,
		file        TEXT NOT NULL,
		tags        JSONB NOT NULL DEFAULT '[]',
		description TEXT NOT NULL DEFAULT '',
		origin      TEXT NOT NULL DEFAULT '',
		meta        JSONB NOT NULL DEFAULT '{}',
		created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
	)`, cfg.Table)).Error; err != nil {
		return nil, fmt.Errorf("assets 建表: %w", err)
	}
	mc, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds: credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.Secure, Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("minio client: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ok, err := mc.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("minio 探测桶: %w", err)
	}
	if !ok {
		if err := mc.MakeBucket(ctx, cfg.Bucket, minio.MakeBucketOptions{Region: cfg.Region}); err != nil {
			return nil, fmt.Errorf("minio 建桶 %s: %w", cfg.Bucket, err)
		}
		log.Printf("[assetlib] MinIO 桶 %s 不存在，已创建", cfg.Bucket)
	}
	sqlDB, _ := db.DB()
	if sqlDB != nil {
		sqlDB.SetMaxOpenConns(2) // 镜像流量轻，别跟案卷库抢连接
		sqlDB.SetConnMaxLifetime(time.Hour)
	}
	return &Remote{db: db, mc: mc, bucket: cfg.Bucket, table: cfg.Table}, nil
}

// PushEntry 条目 upsert 进 PG（id 冲突 = 本地覆盖远端：磁盘是事实源）。
func (r *Remote) PushEntry(e Entry) error {
	tags, _ := json.Marshal(e.Tags)
	meta, _ := json.Marshal(e.Meta)
	return r.db.Exec(fmt.Sprintf(`INSERT INTO %s (id, type, file, tags, description, origin, meta)
		VALUES (?, ?, ?, ?::jsonb, ?, ?, ?::jsonb)
		ON CONFLICT (id) DO UPDATE SET
		  type = EXCLUDED.type, file = EXCLUDED.file, tags = EXCLUDED.tags,
		  description = EXCLUDED.description, origin = EXCLUDED.origin, meta = EXCLUDED.meta`, r.table),
		e.ID, e.Type, e.File, string(tags), e.Desc, e.Origin, string(meta)).Error
}

// DeleteEntry 撤销镜像行（本地删除流将来接；条目级，不动对象）。
func (r *Remote) DeleteEntry(id string) error {
	return r.db.Exec(fmt.Sprintf(`DELETE FROM %s WHERE id = ?`, r.table), id).Error
}

// ListEntries 全量镜像条目（SyncRemote 对账用）。
func (r *Remote) ListEntries() ([]Entry, error) {
	var rows []struct {
		ID, Type, File, Tags, Description, Origin, Meta string
	}
	if err := r.db.Raw(fmt.Sprintf(`SELECT id, type, file, tags::text AS tags, description, origin, meta::text AS meta FROM %s`, r.table)).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(rows))
	for _, row := range rows {
		e := Entry{ID: row.ID, Type: row.Type, File: row.File, Desc: row.Description, Origin: row.Origin}
		_ = json.Unmarshal([]byte(row.Tags), &e.Tags)
		e.Meta = map[string]any{}
		_ = json.Unmarshal([]byte(row.Meta), &e.Meta)
		out = append(out, e)
	}
	return out, nil
}

// PutObject 库文件上 MinIO（键 = library/<file>）。
func (r *Remote) PutObject(ctx context.Context, e Entry, src io.Reader, size int64) error {
	_, err := r.mc.PutObject(ctx, r.bucket, objectKey(e.File), src, size, minio.PutObjectOptions{ContentType: contentType(e.File)})
	return err
}

// FetchObject 对象拉回本地路径（跨机引导/懒取）。
func (r *Remote) FetchObject(ctx context.Context, e Entry, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return r.mc.FGetObject(ctx, r.bucket, objectKey(e.File), dst, minio.GetObjectOptions{})
}

// RemoveObject 测试/清理用。
func (r *Remote) RemoveObject(ctx context.Context, e Entry) error {
	return r.mc.RemoveObject(ctx, r.bucket, objectKey(e.File), minio.RemoveObjectOptions{})
}

// HasObject 对象存在性（HEAD；对账补传被中断的推送用）。
func (r *Remote) HasObject(ctx context.Context, e Entry) bool {
	_, err := r.mc.StatObject(ctx, r.bucket, objectKey(e.File), minio.StatObjectOptions{})
	return err == nil
}

func objectKey(file string) string { return objectPrefix + filepath.ToSlash(file) }

func contentType(file string) string {
	switch strings.ToLower(filepath.Ext(file)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	case ".svg":
		return "image/svg+xml"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".mp4":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	}
	return "application/octet-stream"
}
