# -*- coding: utf-8 -*-
"""九个场景的编排。签名 fn(img, t, T, sc)：t 场景内秒，T 场景长，sc=timeline 场景对象。"""
import math
from PIL import Image, ImageDraw, ImageFilter

from style import (W, H, INK, DIM, CY, MA, AM, GR, PANEL, mix, with_a, F,
                   clamp01, lerp, seg, ease_io, ease_out, ease_in, pulse,
                   glow_text, text, draw_tracked, stamp, mark, dots_layer,
                   draw_cat, cat_glow, mini_cat, draw_box, draw_atom,
                   _ellipse_pts, label_with_line, pill, arrow, tsize)

CH = lambda sc, i: sc["chunks"][min(i, len(sc["chunks"]) - 1)]  # 第 i 句


def fly(t, t0, dur=0.55):
    """入场进度 0→1"""
    return seg(t, t0, t0 + dur, ease_out)


# ---------------------------------------------------------------- s1 开场
def s1_open(img, t, T, sc):
    d = ImageDraw.Draw(img, "RGBA")
    # 箱子
    bx, by, bw, bh = W / 2, 620, 620, 360
    ap = seg(t, 0.3, 1.4)
    reveal = seg(t, 0.5, 2.4)
    draw_box(img, bx, by, bw * ap, bh * ap, mix((30, 44, 80), CY, 0.28),
             alpha=int(40 * ap), lift=0.0, cutaway=False,
             line_a=int(200 * min(ap, reveal)), lw=3)
    dots_layer(img, t, n=60, seed=11, color=CY, amax=48)
    # 箱内双色呼吸微光（第一句末开始）
    c1 = CH(sc, 1)
    gl = seg(t, c1["t0"] + 1.2, c1["t0"] + 2.2) * pulse(t, 2.4, 0.3, 1)
    if gl > 0:
        for k, col in enumerate((CY, MA)):
            gx = bx - 70 + k * 140
            lay = Image.new("RGBA", (180, 110), (0, 0, 0, 0))
            dl = ImageDraw.Draw(lay)
            dl.ellipse([50, 30, 130, 70], fill=with_a(col, 185))
            lay = lay.filter(ImageFilter.GaussianBlur(18))
            img.alpha_composite(lay, (int(gx - 90), int(by - 25 + 10 * math.sin(t*2 + k))))
    # 主标题（c0 讲完“盒子里有一只猫”后浮现）
    c0 = CH(sc, 0)
    ta = seg(t, c0["t1"] + 0.2, c0["t1"] + 1.4)
    if ta > 0:
        y = 250 - 20 * (1 - ta)
        glow_text(img, (W / 2, y), "薛定谔的猫", F("yb", 124), with_a(INK, int(255 * ta)),
                  radius=18, glow_a=90, anchor="mm")
        draw_tracked(img, (W / 2, y + 118), "SCHRÖDINGER'S CAT", F("cob", 30),
                     with_a(DIM, int(220 * ta)), spacing=14)
        d.line([(W/2 - 330, y + 170), (W/2 - 330 + 660 * ta, y + 170)],
               fill=with_a(mix(CY, MA, 0.5), int(200 * ta)), width=3)
    # 退场：整体暗掉交给转场


# ---------------------------------------------------------------- s2 来历
def s2_origin(img, t, T, sc):
    d = ImageDraw.Draw(img, "RGBA")
    c0, c1, c2, c3 = (CH(sc, i) for i in range(4))
    ya = fly(t, c0["t0"] + 0.1, 0.8)
    if ya > 0:
        x = W / 2 - 40 - 60 * (1 - ya)
        glow_text(img, (x, 300), "1935", F("cob", 210), with_a(AM, int(255 * ya)),
                  radius=20, glow_a=110, anchor="mm")
        draw_tracked(img, (W / 2, 425), "维也纳 · 埃尔温·薛定谔", F("yb", 40),
                     with_a(INK, int(240 * ya)), spacing=6)
    # 方程卡（“量子力学如日中天”）
    ea = seg(t, c1["t0"] + 1.2, c1["t0"] + 2.2)
    if ea > 0:
        d.rounded_rectangle([W/2 - 360, 520, W/2 + 360, 700], radius=18,
                            fill=with_a(PANEL, int(200 * ea)),
                            outline=with_a(mix(CY, INK, 0.4), int(120 * ea)), width=2)
        glow_text(img, (W / 2, 610), "H ψ = E ψ", F("cob", 64),
                  with_a(INK, int(255 * ea)), radius=10, glow_a=70, anchor="mm")
        text(img, (W / 2, 662), "薛定谔方程 · 微观世界的运行规则", F("ya", 26),
             with_a(DIM, int(220 * ea)), anchor="mm")
    # “说不圆”：裂缝（走公式下方，不穿字）
    ca = seg(t, c1["t0"] + 5.2, c1["t0"] + 6.0)
    if ca > 0:
        pts = []
        x0, x1 = W/2 - 300, W/2 + 300 * ca
        for i in range(25):
            u = i / 24
            pts.append((x0 + (x1 - x0) * u, 668 - 22 * u + 10 * math.sin(u * 18) * u))
        d.line(pts, fill=with_a(AM, int(230 * min(1, ca * 2))), width=4)
    # 年代轴（c2 起）
    axa = seg(t, c2["t0"] - 0.5, c2["t0"] + 1.0)
    if axa > 0:
        y = 860
        d.line([(W/2 - 520, y), (W/2 - 520 + 1040 * axa, y)],
               fill=with_a(DIM, 180), width=2)
        for k, (xx, yr, lab) in enumerate((
                (W/2 - 380, "1900", "普朗克 · 量子萌芽"),
                (W/2 - 40, "1925", "量子力学建立"),
                (W/2 + 360, "1935", "猫！"))):
            ka = clamp01(axa * 3 - k)
            if ka <= 0: continue
            col = AM if k == 2 else CY
            r = 8 if k < 2 else 11
            d.ellipse([xx - r, y - r, xx + r, y + r], fill=with_a(col, int(255 * ka)))
            glow_text(img, (xx, y - 52), yr, F("cob", 40), with_a(col, int(255 * ka)),
                      radius=8, glow_a=60, anchor="mm")
            text(img, (xx, y + 40), lab, F("ya", 27), with_a(DIM, int(230 * ka)), anchor="mm")
    # c3 “放大成一只肉眼可见的猫”：原子→猫 形变
    ma_ = seg(t, c3["t0"] + 2.0, c3["t0"] + 3.6)
    if ma_ > 0:
        cx, cy = W/2 + 470, 800
        if ma_ < 0.5:
            draw_atom(img, cx, cy, 26 + 20 * math.sin(ma_ * math.pi), t, CY, alpha=255)
        else:
            sc_ = seg(ma_, 0.5, 1.0, ease_out)
            cat_glow(img, cx, cy + 60, 120 * sc_ + 20, CY, alpha=80)
            draw_cat(img, cx, cy + 62, 120 * sc_ + 20, CY, alpha=int(255 * sc_))


# ---------------------------------------------------------------- s3 装置
def s3_apparatus(img, t, T, sc):
    d = ImageDraw.Draw(img, "RGBA")
    c0, c1, c2, c3 = (CH(sc, i) for i in range(4))
    # 大箱子（剖面）
    bx, by, bw, bh = W / 2 + 20, 610, 1020, 540
    ba = fly(t, c0["t0"] + 0.1, 0.7)
    if ba <= 0: return
    draw_box(img, bx, by, bw, bh, mix((30, 44, 80), CY, 0.22),
             alpha=int(60 * ba), cutaway=True, line_a=int(170 * ba), lw=3)
    text(img, (bx, by - bh/2 - 44), "密封铁箱 · 一小时观察窗", F("ya", 28),
         with_a(DIM, int(235 * ba)), anchor="mm")
    # 猫（左下）
    cat_glow(img, bx - 300, by + 185, 230, CY, alpha=60)
    draw_cat(img, bx - 300, by + 187, 230, mix(CY, (20, 40, 70), 0.35),
             alpha=int(255 * ba), tail_wag=t)
    label_with_line(img, (bx - 330, by + 60), (bx - 470, by + 40), "猫",
                    alpha=int(235 * ba), fsize=32)
    # 四部件依句飞入
    t0 = c1["t0"] + 0.4  # “再加四样东西”
    items = [
        ("atom",  t0 + 0.4,  (bx - 60, by - 40), "放射性原子"),
        ("geiger", t0 + 2.6, (bx + 210, by - 60), "盖革计数器"),
        ("hammer", t0 + 4.8, (bx + 330, by + 30), "锤子"),
        ("vial",   t0 + 6.8, (bx + 330, by + 165), "毒气瓶"),
    ]
    chain = seg(t, c2["t0"] + 1.0, c2["t0"] + 7.5)  # 因果链动画窗口
    for name, tt, (ix, iy), lab in items:
        p = fly(t, tt)
        if p <= 0: continue
        dy = -80 * (1 - p)
        ix, iy = ix, iy + dy
        a = int(255 * p)
        if name == "atom":
            flash = pulse(t, 0.9, 0, 1) * seg(t, c2["t0"] + 0.8, c2["t0"] + 1.4) + \
                    (0.5 + 0.5 * math.sin(t * 30)) * seg(t, c2["t0"] + 0.6, c2["t0"] + 1.2)
            draw_atom(img, ix, iy, 52 * p, t, mix(CY, AM, 0.2), alpha=a,
                      decay_flash=clamp01(flash))
        elif name == "geiger":
            s = p
            d.rounded_rectangle([ix - 85*s, iy - 46*s, ix + 85*s, iy + 46*s], radius=10,
                                fill=with_a(PANEL, a), outline=with_a(CY, a), width=2)
            d.ellipse([ix - 66*s, iy - 26*s, ix - 14*s, iy + 26*s],
                      outline=with_a(CY, a), width=3)
            # 指针：因果链时刻弹跳
            base = -0.6
            trig = seg(t, c2["t0"] + 1.5, c2["t0"] + 1.55, ease_out) - \
                   seg(t, c2["t0"] + 2.3, c2["t0"] + 2.8, ease_io)
            ang = base + 1.9 * clamp01(trig) * math.exp(-max(0, t - (c2["t0"] + 1.55)) * 2.5)
            nx, ny = ix + 50 + 26 * math.cos(ang), iy + 6 + 26 * math.sin(ang)
            d.line([(ix + 50, iy + 6), (nx, ny)], fill=with_a(AM, a), width=4)
        elif name == "hammer":
            ang0 = -0.9  # 抬起
            fall = seg(t, c2["t0"] + 2.7, c2["t0"] + 3.15, ease_in)
            ang = ang0 * (1 - fall) + 0.55 * fall
            lay = Image.new("RGBA", (200, 200), (0, 0, 0, 0))
            dl = ImageDraw.Draw(lay)
            cxp, cyp = 100, 100
            dl.line([(cxp, cyp), (cxp + 70 * math.cos(ang + 1.2), cyp + 70 * math.sin(ang + 1.2))],
                    fill=with_a((180, 170, 150), a), width=9)
            hx = cxp + 70 * math.cos(ang + 1.2); hy = cyp + 70 * math.sin(ang + 1.2)
            dl.rounded_rectangle([hx - 22, hy - 14, hx + 22, hy + 14], radius=5,
                                 fill=with_a((200, 190, 170), a))
            lay = lay.rotate(0)
            img.alpha_composite(lay, (int(ix - 100), int(iy - 100)))
        elif name == "vial":
            s = p
            broken = seg(t, c2["t0"] + 3.5, c2["t0"] + 3.9)
            if broken <= 0:
                d.rounded_rectangle([ix - 22*s, iy - 40*s, ix + 22*s, iy + 40*s], radius=10,
                                    fill=with_a((60, 80, 60), a), outline=with_a(GR, a), width=2)
                d.polygon([(ix - 10*s, iy - 40*s), (ix + 10*s, iy - 40*s),
                           (ix + 6*s, iy - 52*s), (ix - 6*s, iy - 52*s)], fill=with_a(GR, a))
                d.ellipse([ix - 15*s, iy + 8*s, ix + 15*s, iy + 38*s],
                          fill=with_a(GR, int(220 * s)))
            else:  # 碎裂 + 气体
                for k in range(10):
                    u = (k * 2.399 + 1) % 6.28
                    r = 30 + 60 * broken
                    d.line([(ix, iy), (ix + r * math.cos(u), iy + r * math.sin(u) * 0.6)],
                           fill=with_a(GR, int(140 * (1 - broken))), width=2)
                lay = Image.new("RGBA", (260, 200), (0, 0, 0, 0))
                dl = ImageDraw.Draw(lay)
                dl.ellipse([80, 60, 180, 140], fill=with_a(GR, 90))
                lay = lay.filter(ImageFilter.GaussianBlur(22))
                img.alpha_composite(lay, (int(ix - 130), int(iy - 100)))
        # 标注
        lx, ly = {"atom": (ix - 120, iy - 90), "geiger": (ix + 40, iy - 70),
                  "hammer": (ix + 60, iy - 60), "vial": (ix + 60, iy + 20)}[name]
        label_with_line(img, (ix, iy), (lx, ly), lab, alpha=a,
                        fsize=29, align_right=name in ("hammer", "vial", "geiger"))
    # 因果链箭头（c2 中段）
    for (p0, p1, tt) in (
            ((bx - 10, by - 40), (bx + 120, by - 55), c2["t0"] + 1.4),
            ((bx + 130, by - 40), (bx + 310, by + 20), c2["t0"] + 2.5),
            ((bx + 330, by + 60), (bx + 330, by + 130), c2["t0"] + 3.3),
            ((bx + 120, by + 140), (bx - 180, by + 170), c2["t0"] + 4.1)):
        pa = fly(t, tt, 0.4)
        if pa > 0:
            mx, my = lerp(p0[0], p1[0], pa), lerp(p0[1], p1[1], pa)
            arrow(d, p0, (mx, my), with_a(AM, int(200 * pa)), w=4)
    if t > c2["t0"] + 4.5:  # 毒瓶→猫 箭头含义标注
        text(img, (bx - 30, by + 128), "毒气", F("yb", 26),
             with_a(mix(GR, INK, 0.4), 220), anchor="mm")
    # 结局双色（c3：原子不衰变，猫就活着）——箱内顶部条
    alt = seg(t, c3["t0"] + 0.2, c3["t0"] + 1.2)
    if alt > 0:
        d.rounded_rectangle([bx - 190, by - 275, bx + 190, by - 175], radius=14,
                            fill=with_a((10, 16, 30), int(200 * alt)),
                            outline=with_a(DIM, int(140 * alt)), width=2)
        mini_cat(img, bx - 150, by - 245, 20, MA, alpha=int(255 * alt), dead=True)
        text(img, (bx - 112, by - 245), "衰变 → 猫死", F("yb", 29),
             with_a(MA, int(235 * alt)), anchor="lm")
        mini_cat(img, bx - 150, by - 202, 20, CY, alpha=int(255 * alt))
        text(img, (bx - 112, by - 202), "不衰变 → 猫活", F("yb", 29),
             with_a(CY, int(235 * alt)), anchor="lm")


# ---------------------------------------------------------------- s4 叠加态
def s4_superposition(img, t, T, sc):
    d = ImageDraw.Draw(img, "RGBA")
    c0, c1, c2, c3, c4 = (CH(sc, i) for i in range(5))
    aa = fly(t, c0["t0"] + 0.3, 0.8)
    # 左：原子双色呼吸
    if aa > 0:
        col = mix(CY, MA, 0.5 + 0.5 * math.sin(t * 1.4))
        draw_atom(img, 600, 470, 150 * aa, t * 0.7, col, alpha=int(255 * aa))
        text(img, (600, 700), "放射性原子", F("yb", 32), with_a(DIM, int(240 * aa)), anchor="mm")
        glow_text(img, (600, 322), "衰变 + 未衰变", F("yb", 46),
                  with_a(col, int(255 * aa)), radius=10, glow_a=80, anchor="mm")
    # 右：双猫叠加（卧姿在右前，不再被坐猫完全遮住）
    ca = seg(t, c1["t0"] + 3.0, c1["t0"] + 4.2)
    if ca > 0:
        ph = t * 2.0
        a_cy = int(255 * (0.35 + 0.45 * (0.5 + 0.5 * math.sin(ph))) * ca)
        a_ma = int(255 * (0.40 + 0.45 * (0.5 - 0.5 * math.sin(ph))) * ca)
        jitter = 5 * math.sin(t * 9)
        cat_glow(img, 1280 + jitter, 805, 320, CY, alpha=55)
        draw_cat(img, 1280 + jitter, 807, 320, CY, alpha=a_cy, tail_wag=t)
        cat_glow(img, 1560 - jitter, 806, 175, MA, alpha=50)
        draw_cat(img, 1560 - jitter, 808, 175, MA, alpha=a_ma, pose="lie")
    # 中间干涉波
    if ca > 0.3:
        pts = []
        for i in range(120):
            u = i / 119
            x = 790 + 260 * u
            env = math.sin(u * math.pi)
            y = 540 + 70 * env * math.sin(u * 12 + t * 4) * (0.5 + 0.5 * math.sin(t))
            pts.append((x, y))
        d.line(pts, fill=with_a(mix(CY, MA, 0.5), 150), width=3)
    # “各占一半”
    half = seg(t, c2["t0"] - 0.2, c2["t0"] + 0.8)
    if half > 0:
        w = 300
        x0 = 450
        d.rounded_rectangle([x0, 770, x0 + w, 800], radius=15,
                            outline=with_a(DIM, int(160 * half)), width=2)
        d.rectangle([x0 + 2, 772, x0 + w/2 - 2, 798], fill=with_a(CY, int(200 * half)))
        d.rectangle([x0 + w/2 + 2, 772, x0 + w - 2, 798], fill=with_a(MA, int(200 * half)))
        glow_text(img, (x0 + w/2, 838), "50 / 50", F("cob", 40),
                  with_a(AM, int(255 * half)), radius=8, glow_a=60, anchor="mm")
    # 拴在一起（c3）
    tie = seg(t, c3["t0"] + 0.2, c3["t0"] + 1.4)
    if tie > 0:
        arrow(d, (700, 560), (700 + 560 * tie, 700), with_a(INK, int(170 * tie)), w=3)
    # 结论式（c4）——放左侧，避开底部字幕与右侧双猫
    ea = seg(t, c4["t0"] + 4.6, c4["t0"] + 5.8)
    if ea > 0:
        d.rounded_rectangle([240, 812, 1080, 894], radius=14,
                            fill=with_a(PANEL, int(205 * ea)),
                            outline=with_a(mix(CY, MA, .5), int(150 * ea)), width=2)
        f_eq = F("yb", 42)
        segs_ = [("ψ", INK), (" = ½( ", DIM), ("活", CY), (" + ", DIM), ("死", MA), (" )", DIM)]
        total = sum(f_eq.getlength(s) for s, _ in segs_)
        x = 660 - total / 2
        for s, col in segs_:
            d.text((x, 853), s, font=f_eq, fill=with_a(col, int(255 * ea)), anchor="lm")
            x += f_eq.getlength(s)


# ---------------------------------------------------------------- s5 波函数
def s5_wavefunction(img, t, T, sc):
    d = ImageDraw.Draw(img, "RGBA")
    c0, c1, c2, c3, c4 = (CH(sc, i) for i in range(5))
    collapse = c2["t0"] + 1.05  # 与音效事件对齐
    col_ap = seg(t, c0["t0"] - 0.3, c0["t0"] + 1.2)
    # 波
    if col_ap > 0:
        mid = 500
        cp = seg(t, collapse, collapse + 0.7, ease_in)
        breathe = 0.85 + 0.15 * math.sin(t * 1.6)
        amp0 = 150 * breathe * col_ap
        amp = amp0 * (1 - cp)
        k = 2 * math.pi / 300 * (1 + cp * 3.5)
        funnel = 1 - 0.75 * cp  # 包络向中心收拢
        pts_cy, pts_ma = [], []
        for i in range(241):
            u = i / 240
            x = 150 + (W - 300) * u
            env = math.exp(-((u - 0.5) ** 2) / (0.22 * funnel))
            y = mid + amp * env * math.sin(k * (x - 150) - t * 3)
            (pts_cy if y <= mid else pts_ma).append((x, y))
        d.line(pts_cy, fill=with_a(CY, 220), width=4)
        d.line(pts_ma, fill=with_a(MA, 220), width=4)
        glow_text(img, (110, mid), "ψ", F("cob", 90), with_a(INK, int(255 * col_ap)),
                  radius=12, glow_a=80, anchor="mm")
        text(img, (W / 2, 240), "波函数：所有可能性的叠加", F("yb", 40),
             with_a(DIM, int(240 * col_ap)), anchor="mm")
    # 眼睛（开箱前）
    eye = seg(t, c2["t0"], c2["t0"] + 0.5)
    if eye > 0 and t < collapse:
        ex, ey = W / 2, 660
        a = int(230 * eye)
        d.ellipse([ex - 60, ey - 34, ex + 60, ey + 34], outline=with_a(INK, a), width=4)
        d.ellipse([ex - 16, ey - 16, ex + 16, ey + 16], fill=with_a(INK, a))
        for lx in (-95, 95):
            d.line([(ex + lx, ey), (ex + lx * 0.55, ey)], fill=with_a(INK, a), width=3)
    # 坍缩闪光 + 直线
    if t >= collapse + 0.7:
        flash = 1 - seg(t, collapse + 0.7, collapse + 0.85)
        if flash > 0:
            d.rectangle([0, 0, W, H], fill=with_a((235, 240, 255), int(140 * flash)))
        la = seg(t, collapse + 0.75, collapse + 1.3)
        d.line([(150, 500, ), (W - 150, 500)], fill=with_a(INK, int(255 * la)), width=5)
        glow_text(img, (W / 2, 620), "坍缩 COLLAPSE", F("yb", 52),
                  with_a(AM, int(255 * la)), radius=10, glow_a=80, anchor="mm")
        # 幸存的猫
        keep = seg(t, collapse + 1.5, collapse + 2.3)
        if keep > 0:
            cat_glow(img, W / 2, 880, 240, CY, alpha=90)
            draw_cat(img, W / 2, 882, 240, CY, alpha=int(255 * keep), tail_wag=t)
            text(img, (W / 2 + 360, 790), "只有一个结果", F("yb", 34),
                 with_a(DIM, int(235 * keep)), anchor="mm")
    # 迷你开盖箱子（右上）
    draw_box(img, W - 220, 200, 220, 150, mix((30, 44, 80), CY, 0.25), alpha=60,
             lift=seg(t, c2["t0"], c2["t0"] + 0.8), line_a=180, lw=3)
    dots_layer(img, t, n=40, seed=5, color=CY, amax=36)


# ---------------------------------------------------------------- s6 诠释之争
def s6_interpretations(img, t, T, sc):
    d = ImageDraw.Draw(img, "RGBA")
    c0, c1, c2, c3, c4, c5, c6 = (CH(sc, i) for i in range(7))
    ta = fly(t, c0["t0"] + 0.2, 0.7)
    if ta <= 0: return
    # 中缝
    d.line([(W/2, 180), (W/2, 900)], fill=with_a(DIM, int(110 * ta)), width=2)
    pill(d, [W/2 - 44, 500, W/2 + 44, 560], 30, with_a(PANEL, int(230 * ta)),
         outline=with_a(DIM, int(160 * ta)), ow=2)
    text(img, (W/2, 530), "VS", F("cob", 36), with_a(INK, int(240 * ta)), anchor="mm")
    # 左：哥本哈根
    la = seg(t, c2["t0"] - 0.6, c2["t0"] + 0.6)
    if la > 0:
        text(img, (W/4, 250), "哥本哈根诠释", F("yb", 46), with_a(INK, int(255 * la)), anchor="mm")
        text(img, (W/4, 310), "1920s · 玻尔 · 海森堡", F("ya", 26),
             with_a(DIM, int(220 * la)), anchor="mm")
        # 迷你波循环坍缩
        mid = 470
        cp = (t % 3.0) / 3.0
        pts = []
        for i in range(90):
            u = i / 89
            x = W/4 - 260 + 520 * u
            env = math.exp(-((u - .5) ** 2) / 0.2) * (1 - seg(cp, 0.72, 0.95, ease_in))
            y = mid + 55 * env * math.sin(u * 16 + t * 3)
            pts.append((x, y))
        d.line(pts, fill=with_a(CY, int(200 * la)), width=3)
        d.rounded_rectangle([W/4 - 280, 570, W/4 + 280, 660], radius=12,
                            fill=with_a(PANEL, int(200 * la)),
                            outline=with_a(CY, int(120 * la)), width=2)
        text(img, (W/4, 615), "观测 → 波函数坍缩", F("yb", 34),
             with_a(INK, int(245 * la)), anchor="mm")
        text(img, (W/4, 700), "「这就是规则，别再问了」", F("ya", 28),
             with_a(DIM, int(230 * la)), anchor="mm")
        if la > 0.9:
            stamp(img, (W/4 + 170, 545), "别追问", F("kai", 40), AM, angle=-10,
                  alpha=int(190 * pulse(t, 2.0, 0.82, 1)))
    # 右：多世界
    ra = seg(t, c3["t0"] + 0.2, c3["t0"] + 1.2)
    if ra > 0:
        text(img, (W * 0.75, 250), "多世界诠释", F("yb", 46), with_a(INK, int(255 * ra)), anchor="mm")
        text(img, (W * 0.75, 310), "1957 · 休·埃弗雷特", F("ya", 26),
             with_a(DIM, int(220 * ra)), anchor="mm")
        # 分裂树：层级随 c4 推进
        root = (W/2 + 160, 480)
        lvl = [seg(t, c4["t0"] + 0.3, c4["t0"] + 0.9),
               seg(t, c4["t0"] + 1.2, c4["t0"] + 1.8),
               seg(t, c4["t0"] + 2.1, c4["t0"] + 2.7)]
        spans = [130, 210, 280]
        tips = [root]
        for li in range(3):
            new_tips = []
            for (px, py) in tips:
                for sgn in (-1, 1):
                    if lvl[li] <= 0: continue
                    qx = px + sgn * spans[li] * 0.5 + 40
                    qy = py + 95
                    mx, my = lerp(px, qx, 0.5), py + 40
                    steps = 26
                    prog = int(steps * lvl[li])
                    pts = []
                    for i in range(max(2, prog)):
                        u = i / (steps - 1)
                        xq = (1-u)**2*px + 2*(1-u)*u*mx + u**2*qx
                        yq = (1-u)**2*py + 2*(1-u)*u*my + u**2*qy
                        pts.append((xq, yq))
                    d.line(pts, fill=with_a(mix(CY, MA, 0.35), 200), width=4)
                    new_tips.append((qx, qy))
            tips = [tp for tp in new_tips] if any(lvl) else tips
        # 树梢小猫：活/死交替
        leaf = seg(t, c4["t0"] + 2.4, c4["t0"] + 3.0)
        if leaf > 0 and len(tips) >= 8:
            for k, (px, py) in enumerate(tips[:8]):
                dead = k % 2 == 1
                mini_cat(img, px, py + 34, 22, MA if dead else CY,
                         alpha=int(235 * leaf), dead=dead)
        text(img, (W * 0.75, 838), "两个世界，同样真实", F("yb", 34),
             with_a(mix(CY, MA, 0.3), int(240 * seg(t, c5["t0"] - 0.2, c5["t0"] + 0.8))),
             anchor="mm")
    dots_layer(img, t, n=36, seed=9, color=CY, amax=30)


# ---------------------------------------------------------------- s7 澄清
def s7_clarify(img, t, T, sc):
    d = ImageDraw.Draw(img, "RGBA")
    c0, c1, c2, c3, c4 = (CH(sc, i) for i in range(5))
    ma = seg(t, c0["t0"] - 0.3, c0["t0"] + 1.0)
    if ma <= 0: return
    # 误解卡
    d.rounded_rectangle([W/2 - 560, 200, W/2 + 560, 330], radius=16,
                        fill=with_a((40, 26, 46), int(200 * ma)),
                        outline=with_a(MA, int(140 * ma)), width=2)
    mis_text = "误解：薛定谔支持「猫真的又生又死」"
    text(img, (W/2, 250), mis_text, F("yb", 42), with_a(INK, int(240 * ma)), anchor="mm")
    text(img, (W/2, 300), "MISREADING", F("cob", 24), with_a(MA, int(200 * ma)), anchor="mm")
    # 划掉
    strike = seg(t, c1["t0"] + 0.3, c1["t0"] + 0.9)
    if strike > 0:
        x0 = W/2 - tsize(F("yb", 42), mis_text)[0] / 2 - 10
        x1 = x0 + (tsize(F("yb", 42), mis_text)[0] + 20) * strike
        d.line([(x0, 250), (x1, 250)], fill=with_a(AM, 240), width=7)
        mark(img, W/2 + 620, 250, 46, "no", AM, width=7)
    # 本意卡（楷体）
    ta = seg(t, c1["t0"] + 1.1, c1["t0"] + 2.1)
    if ta > 0:
        d.rounded_rectangle([W/2 - 620, 420, W/2 + 620, 700], radius=18,
                            fill=with_a(PANEL, int(215 * ta)),
                            outline=with_a(mix(CY, INK, 0.5), int(140 * ta)), width=2)
        glow_text(img, (W/2, 510), "他的本意，是反对这种图景", F("kai", 58),
                  with_a(INK, int(250 * ta)), radius=10, glow_a=60, anchor="mm")
        text(img, (W/2, 600), "把微观的模糊，放大成一只肉眼可见的猫，", F("kai", 40),
             with_a(mix(DIM, INK, 0.45), int(245 * ta)), anchor="mm")
        text(img, (W/2, 655), "荒谬感便无处遁形。", F("kai", 40),
             with_a(mix(DIM, INK, 0.45), int(245 * ta)), anchor="mm")
        mark(img, W/2 - 570, 460, 40, "ok", CY, width=6)
    # 拼图（缺一块）
    pa = seg(t, c3["t0"] + 0.4, c3["t0"] + 1.4)
    if pa > 0:
        gx, gy, cell = W/2 - 150, 810, 74
        for i in range(5):
            x = gx + i * cell
            if i < 4:
                d.rounded_rectangle([x, gy, x + cell - 10, gy + cell - 10], radius=8,
                                    fill=with_a(mix(PANEL, CY, 0.12), int(230 * pa)))
            else:
                blink = 0.5 + 0.5 * math.sin(t * 5)
                d.rounded_rectangle([x, gy, x + cell - 10, gy + cell - 10], radius=8,
                                    outline=with_a(AM, int(230 * pa * blink)), width=4)
                text(img, (x + cell/2 - 5, gy + cell/2 - 5), "?", F("cob", 44),
                     with_a(AM, int(255 * pa * blink)), anchor="mm")
        text(img, (W/2 + 260, gy + 32), "量子理论还缺一块拼图——至今没找到", F("yb", 32),
             with_a(DIM, int(240 * seg(t, c4["t0"] - 0.3, c4["t0"] + 0.7))), anchor="lm")


# ---------------------------------------------------------------- s8 今天
def s8_today(img, t, T, sc):
    d = ImageDraw.Draw(img, "RGBA")
    c0, c1, c2, c3 = (CH(sc, i) for i in range(4))
    aa = fly(t, c0["t0"] + 0.3, 0.8)
    if aa <= 0: return
    # 左：量子芯片
    gx0, gy0, cell, gap = 320, 320, 58, 12
    for r_ in range(5):
        for c_ in range(8):
            u = (r_ * 8 + c_) / 40
            ph = math.sin(t * 2.2 + (r_ + c_) * 0.7)
            col = mix(CY, MA, 0.5 + 0.5 * ph)
            blink = 1 if ph > 0.92 else 0
            x = gx0 + c_ * (cell + gap); y = gy0 + r_ * (cell + gap)
            d.rounded_rectangle([x, y, x + cell, y + cell], radius=6,
                                fill=with_a(col, int((40 + 60 * (0.5 + 0.5 * ph)) * aa)),
                                outline=with_a(col, int((90 + 120 * blink) * aa)), width=2)
    text(img, (gx0 + 4 * (cell + gap) - 6, gy0 - 50), "量子处理器 · 量子比特阵列", F("yb", 32),
         with_a(DIM, int(240 * aa)), anchor="mm")
    # “同时是0又是1” 时整片闪
    flash = seg(t, c2["t0"] + 1.2, c2["t0"] + 2.0) * (1 - seg(t, c2["t0"] + 2.0, c2["t0"] + 2.8))
    if flash > 0:
        d.rectangle([gx0 - 20, gy0 - 20, gx0 + 8 * (cell + gap), gy0 + 5 * (cell + gap)],
                    fill=with_a(mix(CY, MA, 0.5), int(40 * flash)))
    # 右：布洛赫球
    cx, cy, r = 1330, 500, 190
    d.ellipse([cx - r, cy - r, cx + r, cy + r], outline=with_a(DIM, int(200 * aa)), width=3)
    eq = _ellipse_pts(cx, cy, r, r * 0.32, math.radians(18), 64)
    for i in range(0, 64, 2):
        d.line([eq[i], eq[min(i + 1, 64)]], fill=with_a(DIM, int(120 * aa)), width=2)
    d.line([(cx, cy - r - 40), (cx, cy + r + 40)], fill=with_a(DIM, int(90 * aa)), width=2)
    glow_text(img, (cx, cy - r - 62), "|0⟩", F("yb", 40), with_a(CY, int(255 * aa)),
              radius=8, glow_a=70, anchor="mm")
    glow_text(img, (cx, cy + r + 66), "|1⟩", F("yb", 40), with_a(MA, int(255 * aa)),
              radius=8, glow_a=70, anchor="mm")
    # 进动箭头
    th = math.pi * (0.5 + 0.62 * math.sin(t * 0.5))  # 上极↔下极
    az = t * 0.9
    px = cx + r * 0.78 * math.sin(th) * math.cos(az)
    py = cy - r * 0.78 * math.cos(th)
    col = mix(CY, MA, (1 - math.cos(th)) / 2)
    arrow(d, (cx, cy), (px, py), with_a(col, int(240 * aa)), w=5, head=16)
    d.ellipse([cx - 6, cy - 6, cx + 6, cy + 6], fill=with_a(INK, int(220 * aa)))
    # 底部应用条
    for k, (lab, tt) in enumerate((("量子计算", c1["t0"] + 3.0),
                                   ("量子加密", c3["t0"] + 0.3),
                                   ("量子通信", c3["t0"] + 1.8))):
        p = fly(t, tt, 0.5)
        if p <= 0: continue
        x = 480 + k * 480
        d.rounded_rectangle([x - 170, 860, x + 170, 946], radius=14,
                            fill=with_a(PANEL, int(210 * p)),
                            outline=with_a(CY, int(130 * p)), width=2)
        text(img, (x, 903), lab, F("yb", 38), with_a(INK, int(250 * p)), anchor="mm")


# ---------------------------------------------------------------- s9 尾声
def s9_outro(img, t, T, sc):
    d = ImageDraw.Draw(img, "RGBA")
    c0, c1, c2 = (CH(sc, i) for i in range(3))
    ap = seg(t, 0.3, 1.8)
    bx, by, bw, bh = W / 2, 610, 560, 330
    draw_box(img, bx, by, bw, bh, mix((30, 44, 80), CY, 0.25),
             alpha=int(46 * ap), line_a=int(190 * ap), lw=3)
    dots_layer(img, t, n=70, seed=11, color=CY, amax=52)
    # 问号双色呼吸
    qa = seg(t, c0["t0"] + 1.0, c0["t0"] + 2.0)
    if qa > 0:
        col = mix(CY, MA, 0.5 + 0.5 * math.sin(t * 1.8))
        glow_text(img, (bx, 360), "?", F("cob", 150), with_a(col, int(255 * qa)),
                  radius=16, glow_a=110, anchor="mm")
    # 结语（c2）
    fa = seg(t, c2["t0"] - 0.2, c2["t0"] + 1.2)
    if fa > 0:
        text(img, (W/2, 860), "在你看清之前，一切皆有可能。", F("kai", 46),
             with_a(INK, int(250 * fa)), anchor="mm")
    # 片尾字幕（tail 内）
    cred = seg(t, c2["t1"] + 0.8, c2["t1"] + 1.8)
    if cred > 0:
        d.rectangle([0, 0, W, H], fill=with_a((4, 6, 12), int(235 * cred)))
        text(img, (W/2, 480), "解说 · 画面 · 配乐 —— AI Agent 独立完成", F("yb", 44),
             with_a(INK, int(245 * cred)), anchor="mm")
        text(img, (W/2, 556), "Python 逐帧绘制 × ffmpeg 合成 · 2026", F("ya", 30),
             with_a(DIM, int(235 * cred)), anchor="mm")
        draw_tracked(img, (W/2, 660), "SCHRÖDINGER'S CAT", F("cob", 26),
                     with_a(DIM, int(200 * cred)), spacing=12)


SCENES = dict(s1=s1_open, s2=s2_origin, s3=s3_apparatus, s4=s4_superposition,
              s5=s5_wavefunction, s6=s6_interpretations, s7=s7_clarify,
              s8=s8_today, s9=s9_outro)
