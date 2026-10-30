package llm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 快照/路径是包级状态：每个用例先存旧值、退出还原，互不串台。
func withSettingsEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
	oldSnap, oldPath := settingsSnap.Load(), settingsPath
	settingsPath = filepath.Join(t.TempDir(), "llm-config.json")
	settingsSnap.Store(&Settings{})
	t.Cleanup(func() {
		settingsSnap.Store(oldSnap)
		settingsPath = oldPath
	})
}

func TestFromEnvEnvLayer(t *testing.T) {
	withSettingsEnv(t, map[string]string{
		"LLM_API_KEY":    "k-env",
		"LLM_BASE_URL":   "https://env.example/v4",
		"LLM_MODEL":      "m-env",
		"LLM_PLAN_MODEL": "m-plan",
	})
	p, err := FromEnv(RolePlan)
	if err != nil {
		t.Fatal(err)
	}
	if p.Model != "m-plan" || p.BaseURL != "https://env.example/v4" || p.APIKey != "k-env" || p.Name != "llm" {
		t.Fatalf("env 层解析不符: %+v", p)
	}
	// 同档工位未绑定时跟随档：拿得到同样的 env 值
	ps, err := FromEnv(StationPrepScript)
	if err != nil || ps.Model != "m-plan" {
		t.Fatalf("工位应跟随档: %v %+v", err, ps)
	}
}

func TestStationBindingOverrides(t *testing.T) {
	withSettingsEnv(t, map[string]string{
		"LLM_API_KEY":  "k-env",
		"LLM_BASE_URL": "https://env.example/v4",
		"LLM_MODEL":    "m-env",
	})
	s := &Settings{
		Providers: map[string]ProviderCfg{
			"main":  {Label: "主", BaseURL: "https://main.example/v4", APIKey: "k-main", Models: []string{"m-main"}},
			"flash": {Label: "快", Models: []string{"m-flash"}}, // 只配模型：URL/Key 沿用 .env
		},
		RoleDefault: map[string]Binding{"plan": {Provider: "main"}},
		Stations:    map[string]Binding{"judge": {Provider: "flash", Effort: "low"}},
	}
	if err := SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	// 工位绑定赢过档位兜底；provider 空字段回落 env；effort 覆盖生效
	p, err := FromEnv(StationJudge)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "flash" || p.Model != "m-flash" || p.BaseURL != "https://env.example/v4" || p.APIKey != "k-env" {
		t.Fatalf("工位绑定解析不符: %+v", p)
	}
	if p.EffortOverride != EffortLow {
		t.Fatalf("effort 覆盖未生效: %q", p.EffortOverride)
	}
	// 同档未绑定工位走档位兜底
	p2, err := FromEnv(StationPrepScript)
	if err != nil || p2.Name != "main" || p2.Model != "m-main" {
		t.Fatalf("档位兜底解析不符: %v %+v", err, p2)
	}
	// 未绑定档（visual）回落 env
	p3, err := FromEnv(StationVisionDigest)
	if err != nil || p3.Model != "m-env" {
		t.Fatalf("env 兜底解析不符: %v %+v", err, p3)
	}
}

func TestEffortOnlyStationBinding(t *testing.T) {
	withSettingsEnv(t, map[string]string{"LLM_API_KEY": "k-env", "LLM_MODEL": "m-env"})
	s := &Settings{
		Providers: map[string]ProviderCfg{"main": {Models: []string{"m-main"}, APIKey: "k-main"}},
		RoleDefault: map[string]Binding{
			"plan":   {Provider: "main"},
			"visual": {Provider: "main"},
		},
		Stations: map[string]Binding{"judge": {Effort: "disabled"}}, // 只调思考档不换模型
	}
	if err := SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	p, err := FromEnv(StationJudge)
	if err != nil {
		t.Fatal(err)
	}
	if p.Model != "m-main" || p.EffortOverride != EffortDisabled {
		t.Fatalf("effort-only 绑定不符: %+v", p)
	}
}

func TestSaveLoadRoundtrip(t *testing.T) {
	withSettingsEnv(t, map[string]string{"LLM_API_KEY": "k-env"})
	s := &Settings{
		Providers:   map[string]ProviderCfg{"main": {Models: []string{"m-main"}, APIKey: "k-main"}},
		RoleDefault: map[string]Binding{"plan": {Provider: "main"}},
		Stations:    map[string]Binding{"judge": {Effort: "low"}},
	}
	if err := SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	settingsSnap.Store(&Settings{}) // 清快照逼出一次真读盘
	if err := LoadSettings(); err != nil {
		t.Fatal(err)
	}
	got := SettingsSnapshot()
	if got.Providers["main"].Models[0] != "m-main" || got.RoleDefault["plan"].Provider != "main" || got.Stations["judge"].Effort != "low" {
		t.Fatalf("回读不符: %+v", got)
	}
	if b, err := os.ReadFile(SettingsPath()); err != nil || len(b) == 0 {
		t.Fatalf("文件未落盘: %v", err)
	}
}

func TestValidateRejects(t *testing.T) {
	withSettingsEnv(t, map[string]string{})
	cases := []struct {
		name string
		s    *Settings
		want string
	}{
		{"悬空供应商引用", &Settings{
			Providers:   map[string]ProviderCfg{"a": {}},
			RoleDefault: map[string]Binding{"plan": {Provider: "nope"}},
		}, "不存在"},
		{"坏 effort", &Settings{
			Providers: map[string]ProviderCfg{"a": {}},
			Stations:  map[string]Binding{"judge": {Provider: "a", Effort: "turbo"}},
		}, "思考档"},
		{"坏 URL scheme", &Settings{
			Providers: map[string]ProviderCfg{"a": {BaseURL: "ftp://x"}},
		}, "http"},
		{"坏工位键", &Settings{
			Providers: map[string]ProviderCfg{"a": {}},
			Stations:  map[string]Binding{"mars_rover": {Provider: "a"}},
		}, "工位"},
		{"坏档位键", &Settings{
			Providers:   map[string]ProviderCfg{"a": {}},
			RoleDefault: map[string]Binding{"painter": {Provider: "a"}},
		}, "档位"},
		{"坏供应商 id", &Settings{
			Providers: map[string]ProviderCfg{"A B": {}},
		}, "id"},
		{"绑定模型不在列表", &Settings{
			Providers:   map[string]ProviderCfg{"a": {Models: []string{"m-a"}}},
			RoleDefault: map[string]Binding{"plan": {Provider: "a", Model: "m-nope"}},
		}, "不在供应商"},
		{"绑定模型没选供应商", &Settings{
			Providers:   map[string]ProviderCfg{"a": {Models: []string{"m-a"}}},
			RoleDefault: map[string]Binding{"plan": {Model: "m-a"}},
		}, "没选供应商"},
		{"供应商没配模型列表就绑模型", &Settings{
			Providers:   map[string]ProviderCfg{"a": {}},
			RoleDefault: map[string]Binding{"plan": {Provider: "a", Model: "m-x"}},
		}, "模型列表"},
	}
	for _, tc := range cases {
		err := SaveSettings(tc.s)
		if err == nil {
			t.Fatalf("%s: 应被拒", tc.name)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: 报错不含 %q: %v", tc.name, tc.want, err)
		}
	}
}

func TestNormalizeDropsDanglingKeepsEffort(t *testing.T) {
	s := &Settings{
		Providers: map[string]ProviderCfg{"a": {Models: []string{"m"}}},
		Stations:  map[string]Binding{
			"judge":    {Provider: "gone", Effort: "low"}, // 悬空引用：掐供应商保 effort
			"bgm_pick": {}, // 全空绑定：整条删
		},
	}
	normalizeSettings(s)
	if b, ok := s.Stations["judge"]; !ok || b.Provider != "" || b.Effort != "low" {
		t.Fatalf("悬空绑定应保留 effort: %+v", s.Stations)
	}
	if _, ok := s.Stations["bgm_pick"]; ok {
		t.Fatalf("全空绑定应被删: %+v", s.Stations)
	}
}

func TestResolveEffectiveLayerLabels(t *testing.T) {
	withSettingsEnv(t, map[string]string{"LLM_API_KEY": "k-env", "LLM_MODEL": "m-env"})
	s := &Settings{
		Providers:   map[string]ProviderCfg{"main": {Models: []string{"m-main"}, APIKey: "k-main"}},
		RoleDefault: map[string]Binding{"plan": {Provider: "main"}},
		Stations:    map[string]Binding{"prep_script": {Provider: "main"}},
	}
	if err := SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if e := ResolveEffective(StationPrepScript); e.Source != "station" || e.Provider != "main" {
		t.Fatalf("工位层标注不符: %+v", e)
	}
	if e := ResolveEffective(StationPrepStoryboard); e.Source != "role" {
		t.Fatalf("档位层标注不符: %+v", e)
	}
	if e := ResolveEffective(StationVisionDigest); e.Source != "env" {
		t.Fatalf("env 层标注不符: %+v", e)
	}
}

// v3 台账：一条数据 = 一个端点 + 模型名列表；绑定从列表选具体模型。
func TestModelListPickChain(t *testing.T) {
	withSettingsEnv(t, map[string]string{"LLM_API_KEY": "k-env", "LLM_MODEL": "m-env"})
	s := &Settings{
		Providers: map[string]ProviderCfg{
			"multi": {Models: []string{"m-a", "m-b"}, APIKey: "k"},
			"empty": {BaseURL: "https://e.example/v4"}, // 不配模型列表 = 沿用 .env 模型名
		},
		RoleDefault: map[string]Binding{
			"plan":   {Provider: "multi", Model: "m-b"},         // 显式选第二个
			"visual": {Provider: "multi"},                       // 不选 = 列表第一个
			"dialogue": {Provider: "empty"},                     // 列表空 = .env 模型名
		},
	}
	if err := SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if p, err := FromEnv(StationPrepScript); err != nil || p.Model != "m-b" {
		t.Fatalf("绑定模型应赢过列表顺序: %v %+v", err, p)
	}
	if p, err := FromEnv(StationFrameQA); err != nil || p.Model != "m-a" {
		t.Fatalf("未选模型应取列表第一个: %v %+v", err, p)
	}
	if p, err := FromEnv(StationSupervisor); err != nil || p.Model != "m-env" || p.BaseURL != "https://e.example/v4" {
		t.Fatalf("列表空应回落 .env 模型名（URL 仍取台账）: %v %+v", err, p)
	}
}

// v2 兼容读取：旧 JSON 的单 model 字段迁移进 Models（旧库/旧缓存文件零手工迁移）。
func TestV2ModelFieldMigrates(t *testing.T) {
	withSettingsEnv(t, map[string]string{})
	v2 := `{"version":2,"providers":{"old":{"label":"旧","base_url":"https://old.example/v4","api_key":"k-old","model":"m-old"}},"role_default":{"plan":{"provider":"old"}},"stations":{}}`
	if err := os.WriteFile(settingsPath, []byte(v2), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadSettings(); err != nil {
		t.Fatal(err)
	}
	got := SettingsSnapshot()
	p := got.Providers["old"]
	if len(p.Models) != 1 || p.Models[0] != "m-old" || p.Model != "" {
		t.Fatalf("v2 单模型应迁移进列表且旧字段清空: %+v", p)
	}
	if r := got.RoleDefault["plan"]; r.Provider != "old" || r.Model != "" {
		t.Fatalf("旧绑定应保留 provider、model 为空: %+v", r)
	}
}

// 绑定的模型名防悬空：normalize 掐掉列表里没有的模型名（保 provider/effort）。
func TestNormalizePrunesDanglingBoundModel(t *testing.T) {
	s := &Settings{
		Providers:   map[string]ProviderCfg{"a": {Models: []string{"m-a"}}},
		RoleDefault: map[string]Binding{"plan": {Provider: "a", Model: "m-gone", Effort: "low"}},
	}
	normalizeSettings(s)
	b := s.RoleDefault["plan"]
	if b.Provider != "a" || b.Model != "" || b.Effort != "low" {
		t.Fatalf("悬空模型应被掐、provider/effort 保留: %+v", b)
	}
}
