package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/joho/godotenv"
)

// Config 汇总运行时配置：DataDir / LLMMode / PG / MinIO。
// PG：聊天案卷（配了就连，连不上拒启）；PG+MinIO 同时配：素材库远程镜像。
type Config struct {
	DataDir string
	LLMMode string // manual（会话模式·文件契约，M0 默认）| api（OpenAI 兼容 LLM：GLM 编码套餐/DeepSeek 等）

	LLMKey string

	PGDSN string

	MinIO MinIOConfig // Endpoint 空 = 素材库纯本地
}

// MinIOConfig 素材对象镜像（与 PG_DSN 同开同关：目录行在 PG、对象在 MinIO）。
type MinIOConfig struct {
	Endpoint  string // host:port
	AccessKey string
	SecretKey string
	Bucket    string // 缺省 zhenshu
	Region    string // 缺省 cn-north-1
	Secure    bool
}

func Load() (*Config, error) {
	// .env 可选存在：当前目录逐级向上找（在 server/ 里跑也能读到仓库根的 .env）
	var envDir string
	for _, dir := range []string{".", "..", "../.."} {
		p := filepath.Join(dir, ".env")
		if _, err := os.Stat(p); err == nil {
			_ = godotenv.Load(p)
			envDir = dir
			break
		}
	}

	get := func(key, def string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		return def
	}

	c := &Config{
		DataDir: get("DATA_DIR", "data"),
		LLMMode: get("LLM_MODE", "manual"),
		LLMKey:  get("LLM_API_KEY", os.Getenv("DEEPSEEK_API_KEY")),
		PGDSN:   os.Getenv("PG_DSN"),
		MinIO: MinIOConfig{
			Endpoint:  os.Getenv("MINIO_ENDPOINT"),
			AccessKey: os.Getenv("MINIO_ACCESS_KEY"),
			SecretKey: os.Getenv("MINIO_SECRET_KEY"),
			Bucket:    get("MINIO_BUCKET", "zhenshu"),
			Region:    get("MINIO_REGION", "cn-north-1"),
			Secure:    get("MINIO_SECURE", "0") == "1",
		},
	}

	// 相对 DATA_DIR 相对 .env 所在目录解析，启动目录无关（envDir 为空=未找到 .env，退回 CWD）
	if !filepath.IsAbs(c.DataDir) {
		c.DataDir = filepath.Join(envDir, c.DataDir)
	}

	abs, err := filepath.Abs(c.DataDir)
	if err != nil {
		return nil, fmt.Errorf("resolve data dir: %w", err)
	}
	c.DataDir = abs
	return c, nil
}
