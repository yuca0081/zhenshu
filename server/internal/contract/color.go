// Package contract 颜色工具：与 ai/engines/colorutil.py 同一套 WCAG 公式
// （skin 槽位对比度门禁在 Go 侧复算，公式两边一致才有效）。
package contract

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ParseHexColor "#RRGGBB"（容忍 #RGB 缩写）→ r,g,b ∈ [0,255]。
func ParseHexColor(s string) (r, g, b int, err error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "#") {
		return 0, 0, 0, fmt.Errorf("缺 # 前缀")
	}
	h := s[1:]
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	if len(h) != 6 {
		return 0, 0, 0, fmt.Errorf("长度非 3/6")
	}
	v, err := strconv.ParseUint(strings.ToLower(h), 16, 32)
	if err != nil {
		return 0, 0, 0, err
	}
	return int(v >> 16 & 0xFF), int(v >> 8 & 0xFF), int(v & 0xFF), nil
}

// ContrastRatio WCAG 相对亮度对比度（1–21）。与 colorutil.py ratio() 同公式。
func ContrastRatio(fg, bg string) float64 {
	lum := func(hex string) float64 {
		r, g, b, err := ParseHexColor(hex)
		if err != nil {
			return 0
		}
		ch := func(c int) float64 {
			f := float64(c) / 255
			if f <= 0.03928 {
				return f / 12.92
			}
			return math.Pow((f+0.055)/1.055, 2.4)
		}
		return 0.2126*ch(r) + 0.7152*ch(g) + 0.0722*ch(b)
	}
	l1, l2 := lum(fg), lum(bg)
	if l2 > l1 {
		l1, l2 = l2, l1
	}
	return (l1 + 0.05) / (l2 + 0.05)
}
