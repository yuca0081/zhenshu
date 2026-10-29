package api

// 模型设置端点（docs/llm-settings-2026-10-29.md）：GET/PUT /api/llm-settings +
// POST /api/llm-settings/test。管理员收口——URL/Key 属运维面，普通账号不可见。
// Key 只进不出：GET 永远掩码（has_key + 尾 4 位），PUT 空 key = 保持已存，
// clear_api_key=true 显式清除。PUT 原子落盘并换内存快照，下一次作业热生效。

import (
	"context"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"webpage-video-agent/server/internal/llm"
)

type llmProviderView struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
	HasKey  bool   `json:"has_key"`
	KeyTail string `json:"key_tail"`
}

func keyTail(k string) (bool, string) {
	if k == "" {
		return false, ""
	}
	if len(k) > 4 {
		k = k[len(k)-4:]
	}
	return true, k
}

func (s *Server) getLLMSettings(c *gin.Context) {
	snap := llm.SettingsSnapshot()
	if snap == nil { // 未初始化（子命令/测试路径）——按空设置展示
		empty := &llm.Settings{}
		llm.NormalizeSettings(empty)
		snap = empty
	}
	provs := make([]llmProviderView, 0, len(snap.Providers))
	for id, p := range snap.Providers {
		has, tail := keyTail(p.APIKey)
		provs = append(provs, llmProviderView{ID: id, Label: p.Label, BaseURL: p.BaseURL, Model: p.Model, HasKey: has, KeyTail: tail})
	}
	sort.Slice(provs, func(i, j int) bool { return provs[i].ID < provs[j].ID })
	eff := gin.H{}
	for _, r := range []llm.Role{llm.RoleDialogue, llm.RolePlan, llm.RoleVisual} {
		eff[string(r)] = llm.ResolveEffective(r)
	}
	for _, st := range llm.StationRegistry {
		eff[st.ID] = llm.ResolveEffective(llm.Role(st.ID))
	}
	_, model, keySet := llm.EnvFallbacks()
	c.JSON(200, gin.H{
		"providers":    provs,
		"role_default": snap.RoleDefault,
		"stations":     snap.Stations,
		"station_meta": llm.StationRegistry,
		"effective":    eff,
		"env": gin.H{
			"key_set": keySet,
			"base_url": func() string {
				if v := os.Getenv("LLM_BASE_URL"); v != "" {
					return v
				}
				return os.Getenv("DEEPSEEK_BASE_URL")
			}(),
			"model":  model,
			"effort": os.Getenv("LLM_EFFORT"),
		},
	})
}

type llmProviderIn struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	BaseURL     string `json:"base_url"`
	Model       string `json:"model"`
	APIKey      string `json:"api_key"`       // 空 = 保持已存
	ClearAPIKey bool   `json:"clear_api_key"` // true = 清除已存
}

type llmSettingsIn struct {
	Providers   []llmProviderIn        `json:"providers"`
	RoleDefault map[string]llm.Binding `json:"role_default"`
	Stations    map[string]llm.Binding `json:"stations"`
}

func (s *Server) saveLLMSettings(c *gin.Context) {
	var in llmSettingsIn
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(400, gin.H{"error": "请求体不是合法 JSON: " + err.Error()})
		return
	}
	old := llm.SettingsSnapshot()
	out := &llm.Settings{Providers: map[string]llm.ProviderCfg{}}
	if in.RoleDefault != nil {
		out.RoleDefault = in.RoleDefault
	}
	if in.Stations != nil {
		out.Stations = in.Stations
	}
	for _, p := range in.Providers {
		id := strings.ToLower(strings.TrimSpace(p.ID))
		if _, dup := out.Providers[id]; dup {
			c.JSON(400, gin.H{"error": "供应商 id 重复: " + id})
			return
		}
		key := strings.TrimSpace(p.APIKey)
		if key == "" && !p.ClearAPIKey && old != nil {
			key = old.Providers[id].APIKey // 未填且未显式清除 = 沿用已存
		} else if p.ClearAPIKey {
			key = ""
		}
		out.Providers[id] = llm.ProviderCfg{
			Label:   strings.TrimSpace(p.Label),
			BaseURL: strings.TrimSpace(p.BaseURL),
			APIKey:  key,
			Model:   strings.TrimSpace(p.Model),
		}
	}
	if err := llm.SaveSettings(out); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

type llmTestIn struct {
	Provider string `json:"provider"`
	BaseURL  string `json:"base_url"`
	APIKey   string `json:"api_key"`
	Model    string `json:"model"`
}

// testLLMProvider 连通性探针：显式值 > 供应商台账（按 id 取已存 Key）> .env 兜底。
// 支持保存前先测（表单值直传），也支持保存后按 id 复验。
func (s *Server) testLLMProvider(c *gin.Context) {
	var in llmTestIn
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(400, gin.H{"error": "请求体不是合法 JSON: " + err.Error()})
		return
	}
	baseURL, model := strings.TrimSpace(in.BaseURL), strings.TrimSpace(in.Model)
	key := strings.TrimSpace(in.APIKey)
	if pid := strings.TrimSpace(in.Provider); pid != "" {
		snap := llm.SettingsSnapshot()
		if snap == nil {
			snap = &llm.Settings{}
		}
		pc, ok := snap.Providers[pid]
		if !ok {
			c.JSON(404, gin.H{"error": "供应商不存在: " + pid})
			return
		}
		if baseURL == "" {
			baseURL = pc.BaseURL
		}
		if key == "" {
			key = pc.APIKey
		}
		if model == "" {
			model = pc.Model
		}
	}
	fbBase, fbModel, _ := llm.EnvFallbacks()
	if baseURL == "" {
		baseURL = fbBase
	}
	if model == "" {
		model = fbModel
	}
	if key == "" {
		key = llm.EnvKey()
	}
	if key == "" {
		c.JSON(400, gin.H{"error": "没有可用 Key：表单/台账/.env 三处都空，先在「大模型」页填 Key"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Second)
	defer cancel()
	latency, reply, err := llm.Probe(ctx, baseURL, key, model)
	if err != nil {
		c.JSON(200, gin.H{"ok": false, "latency_ms": latency.Milliseconds(), "error": err.Error()})
		return
	}
	if strings.TrimSpace(reply) == "" {
		reply = "（空回复——端点连通，token 花在思考上）"
	}
	c.JSON(200, gin.H{"ok": true, "latency_ms": latency.Milliseconds(), "reply": reply})
}
