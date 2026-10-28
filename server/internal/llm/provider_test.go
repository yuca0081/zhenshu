package llm

import "testing"

// 按档分模型：LLM_<ROLE>_MODEL 只覆盖单档，其余档回落 LLM_MODEL——
// 2026-10-28 写画面（RoleVisual）切 glm-5.3 全量版、其余档留 flash 的配置入口。
func TestFromEnvRoleModelOverride(t *testing.T) {
	t.Setenv("LLM_API_KEY", "test-key")
	t.Setenv("LLM_MODEL", "base-model")
	t.Setenv("LLM_VISUAL_MODEL", "glm-5.3")

	vis, err := FromEnv(RoleVisual)
	if err != nil {
		t.Fatalf("FromEnv(RoleVisual): %v", err)
	}
	if vis.Model != "glm-5.3" {
		t.Fatalf("RoleVisual 应命中 LLM_VISUAL_MODEL=glm-5.3，得到 %q", vis.Model)
	}
	for role, want := range map[Role]string{RolePlan: "base-model", RoleDialogue: "base-model"} {
		p, err := FromEnv(role)
		if err != nil {
			t.Fatalf("FromEnv(%s): %v", role, err)
		}
		if p.Model != want {
			t.Fatalf("%s 未设覆盖应回落 LLM_MODEL=%q，得到 %q", role, want, p.Model)
		}
	}
}

// 未设任何覆盖时三档同模型（M0 单模型口径不回归）。
func TestFromEnvSingleModelFallback(t *testing.T) {
	t.Setenv("LLM_API_KEY", "test-key")
	t.Setenv("LLM_VISUAL_MODEL", "")
	t.Setenv("LLM_MODEL", "")
	for _, role := range []Role{RolePlan, RoleDialogue, RoleVisual} {
		p, err := FromEnv(role)
		if err != nil {
			t.Fatalf("FromEnv(%s): %v", role, err)
		}
		if p.Model != "glm-5.3-flash" {
			t.Fatalf("%s 默认应为 glm-5.3-flash，得到 %q", role, p.Model)
		}
	}
}
