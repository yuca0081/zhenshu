package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/joho/godotenv"
)

// Config 汇总运行时配置：DataDir / LLMMode / PG。
// PG：聊天案卷（配了就连，连不上拒启）。LLM 密钥由 llm.FromEnv 直读环境，不经这里。
type Config struct {
	DataDir string
	LLMMode string // manual（会话模式·文件契约，M0 默认）| api（OpenAI 兼容 LLM：GLM 编码套餐/DeepSeek 等）

	PGDSN string
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
		PGDSN:   os.Getenv("PG_DSN"),
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
