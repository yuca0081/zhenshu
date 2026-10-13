# -*- coding: utf-8 -*-
"""视觉风格与绘图工具：深空蓝底 + 青/紫对偶配色 + 辉光 + 颗粒。"""
import math, os
import numpy as np
from PIL import Image, ImageDraw, ImageFilter, ImageFont

W, H = 1920, 1080
FDIR = r"C:\Windows\Fonts"

# ---------- 配色 ----------
BG0 = (5, 8, 16)
BG1 = (12, 19, 40)
INK = (233, 238, 248)
DIM = (137, 149, 175)
CY = (64, 226, 255)     # 青 = 活/正面
MA = (192, 104, 255)    # 紫 = 死/对偶
AM = (255, 181, 92)     # 琥珀 = 强调/数字
GR = (128, 242, 141)    # 毒液绿
PANEL = (14, 22, 42)

def mix(a, b, t):
    t = max(0.0, min(1.0, t))
    return tuple(int(round(a[i] + (b[i] - a[i]) * t)) for i in range(3))

def with_a(c, a):
    return (c[0], c[1], c[2], int(a))

# ---------- 缓动 ----------
def clamp01(p): return 0.0 if p < 0 else (1.0 if p > 1 else p)
def lerp(a, b, t): return a + (b - a) * t
def ease_io(p):
    p = clamp01(p)
    return 4 * p ** 3 if p < .5 else 1 - ((-2 * p + 2) ** 3) / 2
def ease_out(p):
    p = clamp01(p)
    return 1 - (1 - p) ** 3
def ease_in(p):
    p = clamp01(p)
    return p ** 3
def seg(t, a, b, ease=ease_io):
    return ease((t - a) / (b - a)) if b > a else (1.0 if t >= b else 0.0)
def pulse(t, period=2.0, lo=0.0, hi=1.0):
    return lo + (hi - lo) * (0.5 + 0.5 * math.sin(2 * math.pi * t / period))

# ---------- 字体 ----------
_fonts = {}
def F(kind, size):
    key = (kind, size)
    if key not in _fonts:
        path = dict(ya=f"{FDIR}\\msyh.ttc", yb=f"{FDIR}\\msyhbd.ttc",
                    kai=f"{FDIR}\\STKAITI.TTF", co=f"{FDIR}\\consola.ttf",
                    cob=f"{FDIR}\\consolab.ttf")[kind]
        _fonts[key] = ImageFont.truetype(path, size)
    return _fonts[key]

def tsize(font, s):
    b = font.getbbox(s)
    return b[2] - b[0], b[3] - b[1]

# ---------- 辉光文本 ----------
_glowcache = {}
def glow_text(img, xy, s, font, fill, radius=12, glow_a=150, anchor="la", blur=None):
    """先贴模糊辉光层，再画清晰字。xy 为 anchor 点。"""
    key = (id(font), s)
    if key not in _glowcache:
        mask = Image.new("L", (int(font.getlength(s)) + 12,
                               font.getbbox(s)[3] + 16), 0)
        dm = ImageDraw.Draw(mask)
        dm.text((4, 4), s, font=font, fill=255, anchor="la")
        _glowcache[key] = (mask, mask.filter(ImageFilter.GaussianBlur(radius)))
    _, mblur = _glowcache[key]
    lay = Image.new("RGBA", mblur.size, with_a(fill, 0))
    lay.paste(with_a(fill, 255), (0, 0),
              mblur.point(lambda v: v * glow_a // 255))
    # 根据 anchor 计算粘贴位
    ax, ay = xy
    if anchor[0] == "m": ax -= mblur.size[0] // 2 - 4
    elif anchor[0] == "r": ax -= mblur.size[0] - 8
    if anchor[1] == "m": ay -= mblur.size[1] // 2 - 4
    elif anchor[1] == "s": ay -= mblur.size[1] - 8
    img.alpha_composite(lay, (int(ax) - 4, int(ay) - 4))
    d = ImageDraw.Draw(img)
    d.text(xy, s, font=font, fill=with_a(fill, 255), anchor=anchor)

def text(img, xy, s, font, fill, anchor="la", aa=True):
    ImageDraw.Draw(img).text(xy, s, font=font, fill=fill, anchor=anchor)

# ---------- 基础背景 ----------
_base = None
def get_base():
    global _base
    if _base is not None:
        return _base
    y = np.linspace(0, 1, H)[:, None]
    grad = np.empty((H, W, 3), np.float32)
    for k in range(3):
        grad[:, :, k] = BG0[k] + (BG1[k] - BG0[k]) * y
    # 中心微光
    yy, xx = np.mgrid[0:H, 0:W].astype(np.float32)
    r2 = ((xx - W / 2) / (W * 0.62)) ** 2 + ((yy - H * 0.42) / (H * 0.62)) ** 2
    glow = np.exp(-r2 * 1.6) * 14
    grad += glow[:, :, None] * np.array([0.35, 0.6, 1.0], np.float32)[None, None, :]
    # 暗角
    corner = ((xx / W - 0.5) ** 2 + (yy / H - 0.5) ** 2) * 2.2
    grad *= (1 - 0.42 * np.clip(corner - 0.25, 0, 1))[:, :, None]
    im = Image.fromarray(np.clip(grad, 0, 255).astype(np.uint8))
    # 网格
    d = ImageDraw.Draw(im, "RGBA")
    for gx in range(0, W + 1, 120):
        d.line([(gx, 0), (gx, H)], fill=with_a((110, 150, 200), 7), width=1)
    for gy in range(0, H + 1, 120):
        d.line([(0, gy), (W, gy)], fill=with_a((110, 150, 200), 7), width=1)
    _base = im
    return _base

_grains = None
def get_grains():
    global _grains
    if _grains is None:
        rng = np.random.default_rng(7)
        _grains = [rng.integers(-3, 4, (H, W, 1), dtype=np.int16).repeat(3, 2)
                   for _ in range(4)]
    return _grains

# ---------- 通用图形 ----------
def pill(d, box, r, fill, outline=None, ow=2):
    d.rounded_rectangle(box, radius=r, fill=fill, outline=outline, width=ow)

def arrow(d, p0, p1, color, w=4, head=14):
    d.line([p0, p1], fill=color, width=w)
    ang = math.atan2(p1[1] - p0[1], p1[0] - p0[0])
    for s in (2.6, 3.6):
        d.line([p1, (p1[0] - head * math.cos(ang - s / 6),
                     p1[1] - head * math.sin(ang - s / 6))], fill=color, width=w)

def dots_layer(img, t, n=70, seed=3, color=CY, amax=60, drift=8.0):
    """漂浮尘埃粒子，确定性伪随机。"""
    rng = np.random.default_rng(seed)
    xs0 = rng.uniform(0, W, n); ys0 = rng.uniform(0, H, n)
    spd = rng.uniform(4, 16, n); ph = rng.uniform(0, 6.28, n)
    sz = rng.uniform(1.2, 3.0, n); al = rng.uniform(0.25, 1.0, n)
    d = ImageDraw.Draw(img, "RGBA")
    for i in range(n):
        x = (xs0[i] + t * spd[i] * 0.4) % W
        y = (ys0[i] + 9 * math.sin(t * 0.35 + ph[i]) - t * drift) % H
        a = amax * al[i] * (0.55 + 0.45 * math.sin(t * 0.8 + ph[i] * 2))
        d.ellipse([x - sz[i], y - sz[i], x + sz[i], y + sz[i]],
                  fill=with_a(color, max(8, a)))

# ---------- 猫（风格化剪影） ----------
CAT_SIT = [  # 单位坐标：y 向上，高≈1，面朝左
    (0.18, 0.00), (0.15, 0.24), (0.19, 0.38), (0.22, 0.52), (0.21, 0.63),
    (0.225, 0.72), (0.235, 0.80), (0.26, 0.90), (0.32, 0.80), (0.35, 0.78),
    (0.42, 0.88), (0.44, 0.79), (0.46, 0.70), (0.48, 0.58), (0.55, 0.55),
    (0.66, 0.58), (0.73, 0.50), (0.76, 0.34), (0.74, 0.12), (0.66, 0.00),
]

def cat_glow(img, cx, ground_y, h, color, alpha=70, pose="sit", flip=False):
    """猫剪影的辉光垫底：只对包围盒局部模糊。"""
    lay = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    draw_cat(lay, cx, ground_y, h, color, 255, pose, eyes=False, flip=flip)
    box = lay.getbbox()
    if not box:
        return
    pad = 26
    box = (max(0, box[0] - pad), max(0, box[1] - pad),
           min(W, box[2] + pad), min(H, box[3] + pad))
    crop = lay.crop(box).filter(ImageFilter.GaussianBlur(12))
    a = crop.getchannel("A").point(lambda v: v * alpha // 255)
    crop.putalpha(a)
    img.alpha_composite(crop, (box[0], box[1]))

def draw_cat(img, cx, ground_y, h, color, alpha=255, pose="sit", eyes=True,
             eye_color=None, flip=False, tail_wag=0.0):
    """坐姿用多边形；卧姿用几何组合。alpha 复合到 RGBA img 上。"""
    lay = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    d = ImageDraw.Draw(lay)
    s = h  # 高度缩放
    def P(pt):
        x = pt[0] * s * (-1 if flip else 1) + cx
        y = ground_y - pt[1] * s
        return (x, y)
    if pose == "sit":
        poly = [P(p) for p in CAT_SIT]
        d.polygon(poly, fill=with_a(color, alpha))
        # 尾巴：后部甩出的弧线
        t0 = P((0.74, 0.16))
        t1 = P((0.92, 0.30 + 0.10 * math.sin(tail_wag)))
        t2 = P((0.88, 0.52 + 0.08 * math.sin(tail_wag + 1)))
        steps = 24
        for i in range(steps):
            u0, u1 = i / steps, (i + 1) / steps
            q = lambda u: ((1-u)**2*t0[0] + 2*(1-u)*u*t1[0] + u**2*t2[0],
                           (1-u)**2*t0[1] + 2*(1-u)*u*t1[1] + u**2*t2[1])
            wdt = int(10 * (1 - u0 * 0.65))
            d.line([q(u0), q(u1)], fill=with_a(color, alpha), width=max(2, wdt))
        if eyes:
            ec = eye_color or INK
            for ex, ey in ((0.27, 0.67), (0.365, 0.65)):
                px, py = P((ex, ey))
                d.ellipse([px - h*0.018, py - h*0.008, px + h*0.018, py + h*0.008],
                          fill=with_a(ec, min(255, alpha + 40)))
    else:  # lie 卧姿（闭眼）：头明显凸出于身体左端，双耳分离
        d.ellipse([cx + 0.22*s, ground_y - 0.32*s, cx + 0.98*s, ground_y],
                  fill=with_a(color, alpha))
        d.line([(cx + 0.98*s, ground_y - 0.10*s), (cx + 1.28*s, ground_y - 0.05*s)],
               fill=with_a(color, alpha), width=max(2, int(0.055*s)))
        hcx, hcy, r = cx + 0.16*s, ground_y - 0.16*s, 0.13*s
        d.ellipse([hcx - r, hcy - r, hcx + r, hcy + r], fill=with_a(color, alpha))
        for ex_ in (0.085, 0.235):  # 双耳（分离）
            bx = cx + ex_ * s
            by = ground_y - 0.25 * s
            d.polygon([(bx - 0.048*s, by), (bx + 0.048*s, by), (bx + 0.012*s, by - 0.16*s)],
                      fill=with_a(color, alpha))
        if eyes:  # 闭眼线
            d.line([(hcx - 0.045*s, hcy), (hcx + 0.03*s, hcy + 0.01*s)],
                   fill=with_a((10, 14, 24), alpha), width=max(2, int(0.010*s)))
    img.alpha_composite(lay)

def mini_cat(img, cx, cy, s, color, alpha=255, dead=False):
    """树梢/卡片用的小猫头。dead=True 时用 × 眼。"""
    d = ImageDraw.Draw(img, "RGBA")
    d.ellipse([cx - s, cy - s, cx + s, cy + s], fill=with_a(color, alpha))
    d.polygon([(cx - s, cy - s*0.5), (cx - s*0.4, cy - s*0.5), (cx - s*0.75, cy - s*1.5)],
              fill=with_a(color, alpha))
    d.polygon([(cx + s*0.4, cy - s*0.5), (cx + s, cy - s*0.5), (cx + s*0.75, cy - s*1.5)],
              fill=with_a(color, alpha))
    if dead:
        k = s * 0.28
        for exo in (-s*0.35, s*0.35):
            d.line([(cx+exo-k, cy-k), (cx+exo+k, cy+k)], fill=(10, 14, 24), width=2)
            d.line([(cx+exo-k, cy+k), (cx+exo+k, cy-k)], fill=(10, 14, 24), width=2)
    else:
        d.ellipse([cx-s*0.45, cy-s*0.12, cx-s*0.25, cy+s*0.12], fill=(10, 14, 24))
        d.ellipse([cx+s*0.25, cy-s*0.12, cx+s*0.45, cy+s*0.12], fill=(10, 14, 24))

# ---------- 箱子 ----------
def draw_box(img, cx, cy, w, h, color, alpha=110, lift=0.0, cutaway=False,
             line_a=255, lw=3):
    """cy 为箱体中心。lift∈[0,1] 盖子掀起比例。cutaway=True 前面半透。"""
    x0, x1 = cx - w / 2, cx + w / 2
    y0, y1 = cy + h / 2, cy - h / 2
    dx, dy = w * 0.16, -h * 0.14
    lay = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    d = ImageDraw.Draw(lay)
    # 侧面
    d.polygon([(x1, y0), (x1 + dx, y0 + dy), (x1 + dx, y1 + dy), (x1, y1)],
              fill=with_a(color, int(alpha * 0.55)))
    # 盖（绕后缘上掀）
    lp = ease_io(lift)
    bx0, bx1 = x0 + dx, x1 + dx
    by = y1 + dy
    fy = y1 - lp * h * 0.55
    fx = lp * w * 0.10
    d.polygon([(bx0, by), (bx1, by), (x1 + fx, fy + dy*lp), (x0 + fx, fy + dy*lp)],
              fill=with_a(color, int(alpha * 0.8)),
              outline=with_a(color, line_a), width=lw)
    # 前面
    fa = int(alpha * (0.35 if cutaway else 1.0))
    d.polygon([(x0, y0), (x1, y0), (x1, y1), (x0, y1)],
              fill=with_a(color, fa), outline=with_a(color, line_a), width=lw)
    d.line([(x0, y1), (bx0, by)], fill=with_a(color, int(line_a*0.7)), width=lw)
    d.line([(x1, y1), (x1 + dx, y1 + dy)], fill=with_a(color, int(line_a*0.7)), width=lw)
    img.alpha_composite(lay)
    return (x0, x1, y0, y1, dx, dy)

# ---------- 原子 ----------
def draw_atom(img, cx, cy, r, t, color, alpha=255, decay_flash=0.0, electrons=3):
    lay = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    d = ImageDraw.Draw(lay)
    for k in range(3):
        ang = math.radians(60 * k + t * 8)
        d.line(_ellipse_pts(cx, cy, r, r * 0.36, ang, 60),
               fill=with_a(color, int(alpha * 0.35)), width=2)
    for e in range(electrons):
        k = e % 3
        ang = math.radians(60 * k + t * 8)
        th = t * 2.2 + e * 2.1
        ex = cx + (r * math.cos(th)) * math.cos(ang) - (r * 0.36 * math.sin(th)) * math.sin(ang)
        ey = cy + (r * math.cos(th)) * math.sin(ang) + (r * 0.36 * math.sin(th)) * math.cos(ang)
        d.ellipse([ex - 5, ey - 5, ex + 5, ey + 5], fill=with_a(INK, alpha))
    core = mix(color, (255, 255, 255), 0.35 + 0.3 * decay_flash)
    d.ellipse([cx - r*0.2, cy - r*0.2, cx + r*0.2, cy + r*0.2],
              fill=with_a(core, alpha))
    img.alpha_composite(lay)
    # 核心辉光（局部）
    gs = int(r * 1.4)
    gl = Image.new("RGBA", (gs * 2, gs * 2), (0, 0, 0, 0))
    dg = ImageDraw.Draw(gl)
    dg.ellipse([gs - r*0.24, gs - r*0.24, gs + r*0.24, gs + r*0.24],
               fill=with_a(color, 190))
    gl = gl.filter(ImageFilter.GaussianBlur(r * 0.35 + 6))
    img.alpha_composite(gl, (int(cx - gs), int(cy - gs)))

def _ellipse_pts(cx, cy, rx, ry, rot, n):
    pts = []
    for i in range(n + 1):
        th = 2 * math.pi * i / n
        x = rx * math.cos(th); y = ry * math.sin(th)
        pts.append((cx + x * math.cos(rot) - y * math.sin(rot),
                    cy + x * math.sin(rot) + y * math.cos(rot)))
    return pts

# ---------- 字幕 ----------
def wrap_caption(s, font, maxw):
    lines, cur = [], ""
    for ch in s:
        if font.getlength(cur + ch) > maxw and cur:
            lines.append(cur); cur = ch
        else:
            cur += ch
    if cur: lines.append(cur)
    lines = lines[:3]
    # 避免孤字尾行：把上一行末尾字下移
    while len(lines) > 1 and len(lines[-1]) < 8 and len(lines[-2]) > 10:
        lines[-1] = lines[-2][-1] + lines[-1]
        lines[-2] = lines[-2][:-1]
    return lines

def draw_captions(img, caps):
    """caps: [(text,...)] 本帧应显示的字幕列表（一般1条）。"""
    d = ImageDraw.Draw(img, "RGBA")
    ybase = H - 92
    for s in caps:
        lines = wrap_caption(s, F("yb", 44), 1150)
        lh = 64
        total = len(lines) * lh
        y0 = ybase - total + 20
        for i, ln in enumerate(lines):
            cy = y0 + i * lh + lh // 2
            shadow = Image.new("RGBA", (W, 120), (0, 0, 0, 0))
            ds = ImageDraw.Draw(shadow)
            ds.text((W // 2, 60), ln, font=F("yb", 44), fill=(0, 0, 0, 210), anchor="mm")
            shadow = shadow.filter(ImageFilter.GaussianBlur(6))
            img.alpha_composite(shadow, (0, cy - 60 + 3))
            d.text((W // 2, cy), ln, font=F("yb", 44), fill=with_a(INK, 250), anchor="mm")

def draw_kicker(img, kicker, p):
    """左上角场景序号标签，p∈[0,1] 淡入。"""
    if p <= 0: return
    a = int(220 * p)
    d = ImageDraw.Draw(img, "RGBA")
    d.rectangle([70, 62, 78, 96], fill=with_a(AM, a))
    text(img, (94, 64), kicker, F("yb", 28), with_a(DIM, a))
    d.line([(94, 104), (300, 104)], fill=with_a(DIM, int(90 * p)), width=1)

def draw_tracked(img, xy, s, font, fill, spacing=0, anchor="mm"):
    """带字距的文本。anchor: mm / ma"""
    widths = [font.getlength(ch) for ch in s]
    total = sum(widths) + spacing * (len(s) - 1)
    x, y = xy
    if anchor[0] == "m": x -= total / 2
    d = ImageDraw.Draw(img, "RGBA")
    for ch, w in zip(s, widths):
        d.text((x, y), ch, font=font, fill=fill, anchor="l" + anchor[1])
        x += w + spacing

def stamp(img, xy, s, font, color, angle=8, alpha=200, border=True):
    """旋转图章（如「别追问」）：画到小层再旋转贴回。"""
    w = int(font.getlength(s)) + 36
    h = font.getbbox(s)[3] + 40
    lay = Image.new("RGBA", (w + 40, h + 40), (0, 0, 0, 0))
    d = ImageDraw.Draw(lay)
    d.rounded_rectangle([20, 20, w + 20, h + 20], radius=10,
                        outline=with_a(color, alpha), width=5)
    d.text((20 + w / 2, 20 + h / 2), s, font=font,
           fill=with_a(color, alpha), anchor="mm")
    lay = lay.rotate(angle, expand=True, resample=Image.BICUBIC)
    img.alpha_composite(lay, (int(xy[0] - lay.size[0] / 2), int(xy[1] - lay.size[1] / 2)))

def mark(img, cx, cy, s, kind, color, width=6):
    """手绘对勾/叉，避免字体缺字。"""
    d = ImageDraw.Draw(img, "RGBA")
    c = with_a(color, 255)
    if kind == "ok":
        d.line([(cx - s*0.5, cy + s*0.05), (cx - s*0.12, cy + s*0.4)], fill=c, width=width)
        d.line([(cx - s*0.12, cy + s*0.4), (cx + s*0.55, cy - s*0.4)], fill=c, width=width)
    else:
        for dx in (-1, 1):
            d.line([(cx - s*0.45, cy - s*0.45*dx), (cx + s*0.45, cy + s*0.45*dx)],
                   fill=c, width=width)

def label_with_line(img, anchor_pt, box_pt, s, color=DIM, tcol=None, fsize=30,
                    align_right=False, alpha=255, lw=3):
    """元素 -> 引线 -> 文字标注。box_pt 为文字锚点。"""
    d = ImageDraw.Draw(img, "RGBA")
    d.line([anchor_pt, box_pt], fill=with_a(color, int(alpha * 0.7)), width=lw)
    f = F("yb", fsize)
    tc = with_a(tcol or INK, alpha)
    if align_right:
        d.text((box_pt[0] - 8, box_pt[1]), s + " ", font=f, fill=tc, anchor="rm")
    else:
        d.text((box_pt[0] + 8, box_pt[1]), s, font=f, fill=tc, anchor="lm")
