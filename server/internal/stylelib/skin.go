// 库内条目 → 项目皮肤的机械派生：视频解析/老项目起草的条目没有 Skin 字段，
// 而 v2 制作闸（contract.ValidateStyleSamplesBase）硬性要求皮肤。tokens 是配色
// 事实源，槽位从它确定性地推导——同一 tokens 永远派生同一皮肤，无第二事实源。
package stylelib

import (
	"strings"

	"webpage-video-agent/server/internal/contract"
)

// SkinFromTokens 从条目 tokens 推导皮肤。tokens 缺形状/颜色不合法时逐级兜底，
// 最终一定返回过得了 ValidateSkin 的皮肤（对比度守门，字体保证含中文）。
func SkinFromTokens(name string, tokens map[string]any) *contract.Skin {
	if strings.TrimSpace(name) == "" {
		name = "默认皮肤"
	}
	bg := hexOf(tokens, "bg") // bg 可能是 hex 串或 {from,to} 渐变对象
	if bg == "" {
		bg = "#2D2C36" // 深底兜底：深底亮字的画面更常见，字色再往亮里挑
	}
	txt := pickTxt(tokens, bg)
	accent := pickAccent(tokens, txt)
	font := fontOf(tokens)
	sk := &contract.Skin{
		Name: name,
		Slots: contract.SkinSlots{
			BG: bg, TXT: txt, Accent: accent, Font: font,
			Mute:    hexOf(tokens, "ink"),
			Surface: strOf(tokens, "surface"),
			PanelTxt: func() string {
				if v := hexOf(tokens, "surface_txt"); v != "" {
					return v
				}
				return txt // 面板底(surface)与画布底(bg)同族，字色随主字最稳
			}(),
			Radius:  intOf(tokens, "radius", 0, 40),
			BorderW: intOf(tokens, "border_w", 0, 6),
		},
	}
	return sk
}

// pickTxt 文字色：txt → ink → cap.color → surface_txt 里挑第一个对比度达标的 hex；
// 全不行按底色亮度取黑白（守 3:1 门的确定性兜底）。
func pickTxt(tokens map[string]any, bg string) string {
	for _, key := range []string{"txt", "ink", "cap.color", "surface_txt"} {
		if v := hexOf(tokens, key); v != "" && contract.ContrastRatio(v, bg) >= 3.0 {
			return v
		}
	}
	if light(bg) {
		return "#111111"
	}
	return "#F5F5F5"
}

// pickAccent 强调色：primary → accent（串或数组首项）→ arrow，跳过与文字同色的；
// 全缺就退文字色（槽位必填，颜色语义由场景措辞兜住）。
func pickAccent(tokens map[string]any, txt string) string {
	for _, key := range []string{"primary", "accent", "arrow"} {
		for _, v := range hexList(tokens, key) {
			if v != "" && !strings.EqualFold(v, txt) {
				return v
			}
		}
	}
	return txt
}

// hexOf 取一个合法 hex：key 支持一层点号嵌套（"cap.color" → tokens["cap"].(map)["color"]）；
// key 是渐变对象时取 from（退 to）。
func hexOf(tokens map[string]any, keys ...string) string {
	for _, key := range keys {
		v := tokens[key]
		if dot := strings.IndexByte(key, '.'); dot >= 0 {
			sub, ok := tokens[key[:dot]].(map[string]any)
			if !ok {
				continue
			}
			v = sub[key[dot+1:]]
		}
		switch t := v.(type) {
		case string:
			if isHex(t) {
				return strings.ToUpper(t)
			}
		case map[string]any:
			for _, sub := range []string{"from", "to"} {
				if s, ok := t[sub].(string); ok && isHex(s) {
					return strings.ToUpper(s)
				}
			}
		}
	}
	return ""
}

// hexList 取一组合法 hex（accent 可能是单串或数组）。
func hexList(tokens map[string]any, key string) []string {
	var out []string
	switch v := tokens[key].(type) {
	case string:
		if isHex(v) {
			out = append(out, strings.ToUpper(v))
		}
	case []any:
		for _, c := range v {
			if s, ok := c.(string); ok && isHex(s) {
				out = append(out, strings.ToUpper(s))
			}
		}
	}
	return out
}

func isHex(s string) bool {
	_, _, _, err := contract.ParseHexColor(s)
	return err == nil
}

func strOf(tokens map[string]any, key string) string {
	s, _ := tokens[key].(string)
	return strings.TrimSpace(s)
}

// fontOf tokens.font 必须含中文字体（ValidateSkin 硬门），否则退系统栈。
func fontOf(tokens map[string]any) string {
	if f := strOf(tokens, "font"); f != "" && contract.HasCJKFont(f) {
		return f
	}
	return "'Microsoft YaHei','SimHei',sans-serif"
}

// intOf JSON 数字一律 float64，取整并夹进 [lo,hi]。
func intOf(tokens map[string]any, key string, lo, hi int) int {
	v, ok := tokens[key].(float64)
	if !ok {
		return 0
	}
	n := int(v)
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

// light 底色亮度（人眼感知近似）：决定兜底字色往黑还是往白走。
func light(hex string) bool {
	r, g, b, err := contract.ParseHexColor(hex)
	if err != nil {
		return false
	}
	return 0.299*float64(r)+0.587*float64(g)+0.114*float64(b) > 140
}
