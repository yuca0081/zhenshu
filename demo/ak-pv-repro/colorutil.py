# -*- coding: utf-8 -*-
"""WCAG 对比度工具（引擎与渲染器共用）：保证色块文字/图形 ≥ 门禁对比度。"""
import re


def _norm_hex(c):
    c = (c or '#888888').strip().lstrip('#')
    if len(c) == 3:
        c = ''.join(ch * 2 for ch in c)
    return c


def lum(c):
    """WCAG 相对亮度（sRGB 线性化）。"""
    c = _norm_hex(c)
    try:
        r, g, b = (int(c[i:i + 2], 16) / 255 for i in (0, 2, 4))
    except ValueError:
        return 0.2
    lin = [(x / 12.92 if x <= 0.03928 else ((x + 0.055) / 1.055) ** 2.4) for x in (r, g, b)]
    return 0.2126 * lin[0] + 0.7152 * lin[1] + 0.0722 * lin[2]


def ratio(c1, c2):
    l1, l2 = lum(c1), lum(c2)
    hi, lo = max(l1, l2), min(l1, l2)
    return (hi + 0.05) / (lo + 0.05)


def mix(c, other, k):
    """颜色向 other 混合 k（0–1）。"""
    c, o = _norm_hex(c), _norm_hex(other)
    try:
        chs = [round(int(c[i:i + 2], 16) * (1 - k) + int(o[i:i + 2], 16) * k) for i in (0, 2, 4)]
    except ValueError:
        return '#' + c
    return '#%02X%02X%02X' % tuple(chs)


def pair(bg):
    """→ (bg', txt)：优先白字，不足 3.5:1 压暗 bg；仍不足则提亮 bg 配深字。
    alpha/rgba 输入不处理，原样返回配白字。"""
    if not isinstance(bg, str) or not bg.startswith('#') or len(_norm_hex(bg)) != 6:
        return bg, '#FFFFFF'
    if ratio(bg, '#FFFFFF') >= 3.5:
        return bg, '#FFFFFF'
    dark = mix(bg, '#000000', 0.25)
    for _ in range(5):
        if ratio(dark, '#FFFFFF') >= 3.5:
            return dark, '#FFFFFF'
        dark = mix(dark, '#000000', 0.25)
    light = mix(bg, '#FFFFFF', 0.25)
    for _ in range(5):
        if ratio(light, '#1B2233') >= 3.5:
            return light, '#1B2233'
        light = mix(light, '#FFFFFF', 0.25)
    return bg, '#FFFFFF'


def on(c):
    """不动底色，选对比更高的文字色。"""
    return '#FFFFFF' if ratio(c, '#FFFFFF') >= ratio(c, '#1B2233') else '#1B2233'


def ensure_readable(c, surface, min_ratio=3.5):
    """c 作为文字/图形色落在 surface 上：不足 min_ratio 先向黑加深、再向白提亮。"""
    if not isinstance(c, str) or not c.startswith('#') or len(_norm_hex(c)) != 6:
        return c
    if ratio(c, surface) >= min_ratio:
        return c
    d = c
    for _ in range(6):
        d = mix(d, '#000000', 0.22)
        if ratio(d, surface) >= min_ratio:
            return d
    l = c
    for _ in range(6):
        l = mix(l, '#FFFFFF', 0.22)
        if ratio(l, surface) >= min_ratio:
            return l
    return c
