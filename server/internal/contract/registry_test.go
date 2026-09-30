package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 漂移门禁：specKinds（校验白名单）与 data/library/registry/elements.json（元素库单一事实源）
// 必须一致。加 kind 而忘登记（或反之）在这里被拒——两处同步曾经全靠人肉。
func TestRegistryMatchesSpecKinds(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "data", "library", "registry", "elements.json"))
	if err != nil {
		t.Fatalf("元素注册表不可读: %v", err)
	}
	var els []struct {
		Kind     string `json:"kind"`
		Name     string `json:"name"`
		Category string `json:"category"`
		Scene    string `json:"scene"`
		Menu     string `json:"menu"`
	}
	if err := json.Unmarshal(b, &els); err != nil {
		t.Fatalf("elements.json 损坏: %v", err)
	}
	if len(els) != len(specKinds) {
		t.Errorf("注册表 %d 条 vs specKinds %d 个，数量不一致", len(els), len(specKinds))
	}
	seen := map[string]bool{}
	for _, e := range els {
		if !specKinds[e.Kind] {
			t.Errorf("注册表 kind %q 不在 specKinds 白名单（校验会拒收）", e.Kind)
		}
		if seen[e.Kind] {
			t.Errorf("注册表 kind %q 重复登记", e.Kind)
		}
		seen[e.Kind] = true
		if e.Name == "" || e.Category == "" || e.Scene == "" || e.Menu == "" {
			t.Errorf("kind %q 元数据不全（name/category/scene/menu 必填）", e.Kind)
		}
	}
	for k := range specKinds {
		if !seen[k] {
			t.Errorf("specKinds kind %q 未登记进 elements.json（菜单/素材中心不可见）", k)
		}
	}
}
