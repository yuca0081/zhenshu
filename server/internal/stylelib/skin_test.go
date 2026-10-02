package stylelib

import (
	"encoding/json"
	"testing"

	"webpage-video-agent/server/internal/contract"
)

// 派生皮肤必须过制作闸的皮肤校验——这是它存在的唯一目的，钉死。
func TestSkinFromTokensValid(t *testing.T) {
	cases := []struct {
		name   string
		tokens map[string]any
	}{
		{"等轴2.5D实库形状（bg渐变对象+accent数组）", map[string]any{
			"bg":  map[string]any{"type": "linear", "from": "#2D2C36", "to": "#545360", "angle": 135.0},
			"txt": "#DFDAEC", "ink": "#DFDAEC", "primary": "#CCC4E3",
			"accent":  []any{"#B0AECB", "#8F8DB0", "#707084"},
			"font":    "'Segoe UI','Microsoft YaHei','SimHei',sans-serif",
			"surface": "#40404C", "surface_txt": "#DFDAEC",
			"radius": 10.0, "border_w": 0.0,
			"cap": map[string]any{"mode": "bar", "bg": "rgba(45,44,54,.85)", "color": "#DFDAEC"},
		}},
		{"bg是hex串+accent单串", map[string]any{
			"bg": "#F5F1E8", "txt": "#2B2B2B", "primary": "#C0392B",
			"font": "'KaiTi','Microsoft YaHei',serif",
		}},
		{"txt对比度不足→黑白兜底", map[string]any{
			"bg": "#777777", "txt": "#8A8A8A", "primary": "#8A8A8A",
			"font": "'SimHei',sans-serif",
		}},
		{"tokens全缺→中性兜底", nil},
		{"颜色全是rgba非hex", map[string]any{
			"bg": "rgba(45,44,54,.9)", "txt": "rgba(223,218,236,1)",
			"font": "'Microsoft YaHei',sans-serif",
		}},
		{"accent与txt同色→跳过取下一个", map[string]any{
			"bg": "#111111", "txt": "#EEEEEE", "primary": "#EEEEEE",
			"accent": []any{"EEEEEE", "#FF5533"}, "font": "'SimHei',sans-serif",
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sk := SkinFromTokens("等轴2.5D", c.tokens)
			if err := contract.ValidateSkin(sk); err != nil {
				t.Fatalf("派生皮肤未过 ValidateSkin: %v", err)
			}
		})
	}
}

func TestSkinFromTokensValues(t *testing.T) {
	tokens := map[string]any{
		"bg":          map[string]any{"from": "#2d2c36", "to": "#545360"},
		"txt":         "#dfdaec",
		"accent":      []any{"#b0aecb"},
		"font":        "'Segoe UI','Microsoft YaHei',sans-serif",
		"surface":     "#40404C",
		"surface_txt": "#DDDDDD",
		"radius":      10.0,
		"border_w":    99.0, // 越界夹回 6
	}
	sk := SkinFromTokens("等轴2.5D（像素城市）", tokens)
	if sk.Name != "等轴2.5D（像素城市）" {
		t.Errorf("Name = %q", sk.Name)
	}
	if sk.Slots.BG != "#2D2C36" { // 渐变取 from，且统一大写
		t.Errorf("BG = %q，想从渐变 from 取", sk.Slots.BG)
	}
	if sk.Slots.TXT != "#DFDAEC" {
		t.Errorf("TXT = %q", sk.Slots.TXT)
	}
	if sk.Slots.Accent != "#B0AECB" {
		t.Errorf("Accent = %q", sk.Slots.Accent)
	}
	if sk.Slots.Radius != 10 || sk.Slots.BorderW != 6 {
		t.Errorf("Radius/BorderW = %d/%d，想 10/6", sk.Slots.Radius, sk.Slots.BorderW)
	}
	if sk.Slots.PanelTxt != "#DDDDDD" {
		t.Errorf("PanelTxt = %q", sk.Slots.PanelTxt)
	}
	// tokens 全缺：字体栈必须兜底含中文字体
	bare := SkinFromTokens("", nil)
	if !contract.HasCJKFont(bare.Slots.Font) {
		t.Errorf("兜底字体栈缺中文: %q", bare.Slots.Font)
	}
	if bare.Name != "默认皮肤" {
		t.Errorf("空名兜底 = %q", bare.Name)
	}
}

// 库内条目没存 Skin 时，StyleSamplesJSON 必须带派生皮肤——「什么是二向箔」
// 事故（缺皮肤卡死制作）的回归钉。
func TestStyleSamplesJSONDerivesSkin(t *testing.T) {
	st := Style{
		ID: "vid-x-s01", Name: "测试风", Direction: "测试风（深底）",
		Tokens: map[string]any{
			"bg":  map[string]any{"from": "#1A1A24", "to": "#2A2A3A"},
			"txt": "#EDEDF5", "primary": "#CCC4E3",
			"font": "'Microsoft YaHei',sans-serif",
		},
		Samples: []Sample{{Tag: "样张 A", HTML: "<!doctype html><html></html>"}},
	}
	var ss contract.StyleSamples
	if err := json.Unmarshal(st.StyleSamplesJSON(), &ss); err != nil {
		t.Fatalf("StyleSamplesJSON 解不开: %v", err)
	}
	skins := ss.EffectiveSkins()
	if len(skins) != 1 {
		t.Fatalf("无 Skin 条目应派生 1 套皮肤，得到 %d", len(skins))
	}
	if err := contract.ValidateSkin(&skins[0]); err != nil {
		t.Fatalf("派生皮肤不合格: %v", err)
	}
	if skins[0].Slots.BG != "#1A1A24" {
		t.Errorf("BG = %q", skins[0].Slots.BG)
	}
	// 条目自带 Skin 时不派生，原样携带
	st.Skin = &contract.Skin{Name: "手定", Slots: contract.SkinSlots{BG: "#FFFFFF", TXT: "#111111", Accent: "#FF0000", Font: "'SimHei',sans-serif"}}
	ss2 := contract.StyleSamples{}
	if err := json.Unmarshal(st.StyleSamplesJSON(), &ss2); err != nil {
		t.Fatalf("解不开: %v", err)
	}
	if got := ss2.EffectiveSkins(); len(got) != 1 || got[0].Name != "手定" {
		t.Errorf("自带 Skin 被派生覆盖: %+v", got)
	}
}
