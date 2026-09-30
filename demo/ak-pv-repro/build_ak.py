# -*- coding: utf-8 -*-
"""明日方舟「怒潮凛冬」PV 复刻 — AK 风格引擎 v2（元素级动效版）。

v1 是"静帧 + 缓推 + 硬切"；v2 把每段拆成图层逐元素做动效（对齐原片的
动态语言）：铅字逐字砸落 + 落位震动、旗帜布料摆动、字母爆散、红色射线
描画、墨团晕开、白闪切、冲击变焦（punch）、光带扫过、雪花/余烬/花瓣
粒子、水墨飞鸟、图章砸落、卡片带撕纸边滑入、标题块侧砸、城市剪影升起、
辉光脉动。全部 GSAP 链式 tween，确定性（无 CSS animation、无 repeat/yoyo、
无随机数——伪随机用下标算术）。

不复用 13 个注册风格，直接产出 llm/comp-segNN.json（{html, elements}），
契约形状与 ai/engines/stylepack.py 的 wrap() 一致（hyperframes check 已验证）。

用法：python demo/ak-pv-repro/build_ak.py <项目目录>
"""
import json
import math
import pathlib
import re
import sys

AI_DIR = pathlib.Path(__file__).resolve().parents[2] / 'ai'
sys.path.insert(0, str(AI_DIR / 'engines'))
import stylepack as sp  # noqa: E402  复用 load()/动效片段生成器

W, H = 1920, 1080
RED = '#C8102E'
INK = '#17140F'
PAPER = '#E9E4D8'
WHITE = '#F2EFE8'
DISP = "'Arial Black','Segoe UI',sans-serif"
CJK = "'Microsoft YaHei','SimHei',sans-serif"
TW = "Consolas,'Courier New',monospace"
META = None  # sp.load(proj) 结果


# ── 时间轴工具（词位 → 秒）──────────────────────────────────

def make_w(sid, words, dur):
    def w(*keys, at=None):
        for k in keys:
            for wd in words:
                if k in wd['text']:
                    return max(0.05, round(wd['start'] - 0.12, 2))
        print(f'  ⚠ {sid} 词未命中: {keys}')
        return max(0.05, round(dur * at, 2)) if at is not None else 1.0
    return w


def chars(text):
    out = []
    for c in text:
        if c == ' ':
            out.append('<span data-c data-layout-allow-overlap style="display:inline-block;width:.34em;">&nbsp;</span>')
        else:
            out.append(f'<span data-c data-layout-allow-overlap style="display:inline-block;">{c}</span>')
    return ''.join(out)


def slam(sel, t, step=.14, dy=56):
    """铅字逐字砸落（自上而下 power3.in，接近活版印刷落位）。"""
    return (f"gsap.set('{sel} [data-c]',{{opacity:0,y:-{dy}}});\n      "
            f"tl.to('{sel} [data-c]',{{opacity:1,y:0,duration:.26,stagger:{step},ease:'power3.in'}},{t:.2f});")


def type_on(sel, t, step=.045):
    """打字机逐字显形（瞬时、等步进）。"""
    return (f"gsap.set('{sel} [data-c]',{{opacity:0}});\n      "
            f"tl.to('{sel} [data-c]',{{opacity:1,duration:.02,stagger:{step},ease:'none'}},{t:.2f});")


def hard_in(sel, t, dur=.07):
    return f"gsap.set('{sel}',{{opacity:0}});\n      tl.to('{sel}',{{opacity:1,duration:{dur},ease:'none'}},{t:.2f});"


def hard_out(sel, t, dur=.07):
    return f"tl.to('{sel}',{{opacity:0,duration:{dur},ease:'none'}},{t:.2f});"


def shake(sel, t, amp=12, n=4):
    """落位震动：x 交替衰减抖动。别与 CAM 同元素混用。"""
    js = []
    for i in range(n):
        d = amp * (1 - i / n) * (1 if i % 2 == 0 else -1)
        js.append(f"tl.to('{sel}',{{x:{d:.0f},duration:.05,ease:'power1.inOut'}},{t + i * .055:.3f});")
    js.append(f"tl.set('{sel}',{{x:0}},{t + n * .055:.3f});")
    return '\n      '.join(js)


def flicker(sel, t, n=6, hi=.85, lo=.2, dur=.7):
    """闪现不稳定感（链式 opacity 振荡，末态 1）。元素需初始 opacity:0。"""
    step = dur / n
    js = []
    for i in range(n):
        v = hi if i % 2 == 0 else lo
        js.append(f"tl.to('{sel}',{{opacity:{v:.2f},duration:{step:.3f},ease:'none'}},{t + i * step:.3f});")
    js.append(f"tl.to('{sel}',{{opacity:1,duration:.1,ease:'none'}},{t + dur:.3f});")
    return '\n      '.join(js)


def wave(sel, t, n=4, amp=4.5, period=1.1):
    """布料摆动（skewY 振荡，transform-origin 需 left center）。"""
    js = []
    for k in range(n + 1):
        v = (amp if k % 2 == 0 else -amp) if k < n else 0
        js.append(f"tl.to('{sel}',{{skewY:{v},duration:{period / n:.2f},ease:'sine.inOut'}},{t + k * period / n:.3f});")
    return '\n      '.join(js)


def punch(sel, t, frm=1.12):
    """冲击变焦：入画瞬间从放大急缩到 1。"""
    return (f"gsap.set('{sel}',{{scale:{frm}}});\n      "
            f"tl.to('{sel}',{{scale:1,duration:.34,ease:'power3.out'}},{t:.2f});")


def glow_pulse(sel, t, dur, n=3, lo=.45, hi=.8):
    """辉光呼吸（链式 opacity 振荡）。"""
    step = dur / (n * 2)
    js = []
    for k in range(n * 2):
        v = hi if k % 2 == 0 else lo
        js.append(f"tl.to('{sel}',{{opacity:{v:.2f},duration:{step * .96:.3f},ease:'sine.inOut'}},{t + k * step:.3f});")
    return '\n      '.join(js)


CAM = {
    'zoom_in':  lambda sel, d: f"tl.fromTo('{sel}',{{scale:1}},{{scale:1.06,duration:{d:.2f},ease:'none'}},0);",
    'zoom_out': lambda sel, d: f"tl.fromTo('{sel}',{{scale:1.08}},{{scale:1,duration:{d:.2f},ease:'none'}},0);",
    'drift':    lambda sel, d: f"tl.fromTo('{sel}',{{scale:1.04,x:0}},{{scale:1.09,x:-28,duration:{d:.2f},ease:'none'}},0);",
    'pan_right': lambda sel, d: f"tl.fromTo('{sel}',{{x:-22}},{{x:22,duration:{d:.2f},ease:'none'}},0);",
}


# ── HTML 工具 ───────────────────────────────────────────────

def el(sid, name, style, inner='', tag='div'):
    eid = f'{sid}-e{name}'
    return (f'<{tag} class="a-el" id="{eid}" data-hf-name="{name}" '
            f'style="{style}">{inner}</{tag}>')


def photo(sid, name, src, x=0, y=0, w=W, h=H, fit='cover', extra=''):
    return el(sid, name,
              f'top:{y}px;left:{x}px;width:{w}px;height:{h}px;overflow:hidden;{extra}',
              f'<img src="{src}" style="width:100%;height:100%;object-fit:{fit};display:block;" />').replace(
        'data-hf-name=', 'data-layout-allow-overflow data-hf-name=', 1)


def bars(sid, h=96):
    return (el(sid, '遮幅上', f'top:0;left:0;width:{W}px;height:{h}px;background:#050505;') + '\n    ' +
            el(sid, '遮幅下', f'top:{H - h}px;left:0;width:{W}px;height:{h}px;background:#050505;'))


def grain(sid, dark_bg=True):
    line = 'rgba(255,255,255,.045)' if dark_bg else 'rgba(30,24,14,.05)'
    return (el(sid, '颗粒', f'top:0;left:0;width:{W}px;height:{H}px;pointer-events:none;'
              f'background:repeating-linear-gradient(0deg,{line} 0 1px,transparent 1px 3px),'
              f'repeating-linear-gradient(90deg,rgba(0,0,0,.035) 0 2px,transparent 2px 4px);') + '\n    ' +
            el(sid, '暗角', f'top:0;left:0;width:{W}px;height:{H}px;pointer-events:none;'
              f'background:radial-gradient(ellipse at center,transparent 58%,rgba(0,0,0,.4) 100%);'))


def bg_black(sid):
    return (f'<div style="position:absolute;inset:0;background:#0B0B0B;'
            f'background-image:radial-gradient(ellipse at 50% 42%,rgba(42,42,42,.32),transparent 62%),'
            f'repeating-linear-gradient(0deg,rgba(255,255,255,.014) 0 1px,transparent 1px 3px);"></div>')


def bg_paper(sid):
    return (f'<div style="position:absolute;inset:0;background:{PAPER};'
            f'background-image:linear-gradient(105deg,rgba(60,50,30,.07),transparent 42%),'
            f'linear-gradient(275deg,rgba(255,255,255,.55),transparent 32%),'
            f'repeating-linear-gradient(0deg,rgba(70,60,40,.03) 0 1px,transparent 1px 4px);"></div>')


def bg_dark(sid, a='#0C1214', b='#14232B'):
    return (f'<div style="position:absolute;inset:0;background:linear-gradient(118deg,{a},{b} 62%,{a});'
            f'background-image:linear-gradient(64deg,rgba(53,208,200,.05) 0 2px,transparent 2px 260px),'
            f'linear-gradient(64deg,rgba(255,255,255,.03) 0 1px,transparent 1px 190px),'
            f'linear-gradient(118deg,{a},{b} 62%,{a});"></div>')


def typew(sid, name, x, y, text, fs=28, color='#9A9A8E', ls=7):
    return el(sid, name, f'top:{y}px;left:{x}px;font:{fs}px/1.5 {TW};color:{color};'
              f'letter-spacing:{ls}px;white-space:nowrap;', chars(text))


def bigt(sid, name, x, y, text, fs=120, color=INK, fam=DISP, ls=0, skew=0, wpx=None, center=False):
    sk = f'transform:skewX({skew}deg);' if skew else ''
    wpx_s = f'width:{wpx}px;' if wpx else ''
    ta = 'text-align:center;' if center else ''
    return el(sid, name, f'top:{y}px;left:{x}px;{wpx_s}{ta}font-family:{fam};font-weight:900;'
              f'font-size:{fs}px;line-height:1.05;color:{color};letter-spacing:{ls}px;white-space:nowrap;{sk}', text)


def chars_big(sid, name, x, y, text, fs=120, color=INK, fam=DISP, ls=0, skew=0, wpx=None, center=False):
    sk = f'transform:skewX({skew}deg);' if skew else ''
    wpx_s = f'width:{wpx}px;' if wpx else ''
    ta = 'text-align:center;' if center else ''
    return el(sid, name, f'top:{y}px;left:{x}px;{wpx_s}{ta}font-family:{fam};font-weight:900;'
              f'font-size:{fs}px;line-height:1.05;color:{color};letter-spacing:{ls}px;white-space:nowrap;{sk}',
              chars(text))


def stamp(sid, name, x, y, text, wpx=230, hpx=80, fs=50, color=RED, rot=-5,
          txt='#FFFFFF', filled=True):
    bgc = f'background:{color};' if filled else f'border:5px solid {color};'
    tc = txt if filled else color
    return el(sid, name, f'top:{y}px;left:{x}px;width:{wpx}px;height:{hpx}px;{bgc}'
              f'display:flex;align-items:center;justify-content:center;transform:rotate({rot}deg);'
              f'font-family:{CJK};font-weight:900;font-size:{fs}px;letter-spacing:6px;color:{tc};', text)


def thud(sel, t, rot=0):
    """图章砸落：放大急缩 + 微旋定版。"""
    return (f"gsap.set('{sel}',{{scale:1.9,opacity:0,rotation:{rot - 8}}});\n      "
            f"tl.to('{sel}',{{opacity:1,scale:1,rotation:{rot},duration:.26,ease:'power3.in'}},{t:.2f});")


def red_bar(sid, name, x, y, wpx, hpx=14, color=RED, skew=8):
    return el(sid, name, f'top:{y}px;left:{x}px;width:{wpx}px;height:{hpx}px;background:{color};'
              f'transform:skewX({skew}deg);')


def rays(sid, name, cx, cy, n=12, ln=1500, lw=9, color=RED, start=-84, step=15):
    parts = []
    for i in range(n):
        ang = start + i * step
        parts.append(f'<div data-{name}="{i}" style="position:absolute;top:{cy - lw / 2}px;left:{cx}px;'
                     f'width:{ln}px;height:{lw}px;background:{color};opacity:.9;'
                     f'transform-origin:0 50%;transform:rotate({ang}deg);"></div>')
    return el(sid, name, f'top:0;left:0;width:{W}px;height:{H}px;pointer-events:none;', ''.join(parts))


def splat(sid, name, x, y, w, h, color, rot=0, opacity=.92):
    """墨团/色块晕斑（不规则圆角）。"""
    return el(sid, name, f'top:{y}px;left:{x}px;width:{w}px;height:{h}px;background:{color};opacity:{opacity};'
              f'border-radius:46% 54% 58% 42%/52% 44% 56% 48%;transform:rotate({rot}deg);')


def skyline(sid, name, y, h, color='#22201C', seed=1, opacity=.9):
    """城市剪影（确定性伪随机楼块，底部对齐）。"""
    parts = []
    x, i = 0, 0
    while x < W:
        bw = 64 + ((i * 127 + seed * 31) % 150)
        bh = h * (.3 + ((i * 53 + seed * 17) % 65) / 100)
        parts.append(f'<div style="position:absolute;bottom:0;left:{x}px;width:{bw}px;height:{bh:.0f}px;background:{color};"></div>')
        x += bw + 6 + (i * 29) % 30
        i += 1
    return el(sid, name, f'top:{y}px;left:0;width:{W}px;height:{h}px;overflow:hidden;opacity:{opacity};', ''.join(parts))


# ── 动效道具（返回 html, js；粒子类选择器一律 scoped 到容器 id）──

def strobe(sid, name, t, peak=.75, dur=.24):
    """白闪切。"""
    eid = f'{sid}-e{name}'
    html = el(sid, name, f'top:0;left:0;width:{W}px;height:{H}px;pointer-events:none;'
              f'background:#F5F2EA;opacity:0;')
    js = (f"tl.to('#{eid}',{{opacity:{peak},duration:{dur * .4:.3f},ease:'none'}},{t:.3f});\n      "
          f"tl.to('#{eid}',{{opacity:0,duration:{dur * .6:.3f},ease:'none'}},{t + dur * .4 + .02:.3f});")
    return html, js


def sweep(sid, name, t, dur=1.1, y=0, h=H, alpha=.32):
    """斜向光带扫过。"""
    eid = f'{sid}-e{name}'
    html = el(sid, name, f'top:{y}px;left:-520px;width:480px;height:{h}px;pointer-events:none;opacity:0;'
              f'background:linear-gradient(100deg,transparent,rgba(255,255,255,{alpha}) 45%,'
              f'rgba(255,255,255,{alpha * .3:.2f}) 62%,transparent);transform:skewX(-14deg);')
    js = (f"tl.to('#{eid}',{{opacity:1,duration:{dur * .22:.2f},ease:'none'}},{t:.3f});\n      "
          f"tl.fromTo('#{eid}',{{x:0}},{{x:2960,duration:{dur:.2f},ease:'power1.inOut'}},{t:.3f});\n      "
          f"tl.to('#{eid}',{{opacity:0,duration:{dur * .3:.2f},ease:'none'}},{t + dur * .68:.3f});")
    return html, js


def particles(sid, name, n, t0, dur, mode='fall', color='rgba(238,236,228,.9)', area=None):
    """粒子：fall=雪/屑下落 rise=余烬上升 petal=花瓣摆落。伪随机靠下标算术。"""
    eid = f'{sid}-e{name}'
    x0, y0, x1, y1 = area or (0, 0, W, H)
    span_x = x1 - x0
    parts, js = [], []
    for i in range(n):
        px = x0 + (i * 733 + 97) % span_x
        s = (3 + (i * 5) % 5) if mode != 'petal' else (9 + (i * 7) % 9)
        top = y0 if mode != 'rise' else y1 - 40
        style = f'position:absolute;top:{top}px;left:{px}px;width:{s}px;height:{s}px;background:{color};opacity:0;'
        if mode == 'petal':
            style += f'border-radius:62% 38% 55% 45%/50% 62% 38% 55%;transform:rotate({(i * 53) % 360}deg);'
        else:
            style += 'border-radius:50%;'
        parts.append(f'<div data-p="{i}" style="{style}"></div>')
        sel = f'#{eid} [data-p="{i}"]'
        delay = t0 + ((i * .41) % max(dur * .55, .5))
        travel = y1 - y0 + 60
        d = dur * (.45 + (i % 4) * .16)
        if mode == 'rise':
            js.append(f"tl.fromTo('{sel}',{{y:0,opacity:0}},{{opacity:.85,duration:.5}},{delay:.2f});\n      "
                      f"tl.to('{sel}',{{y:-{travel:.0f},x:{18 + (i % 3) * 16},duration:{d:.1f},ease:'none'}},{delay:.2f});\n      "
                      f"tl.to('{sel}',{{opacity:0,duration:.4}},{delay + d - .4:.2f});")
        else:
            sway = (26 + (i % 3) * 20) * (1 if i % 2 else -1)
            js.append(f"tl.fromTo('{sel}',{{y:0,opacity:0}},{{opacity:.9,duration:.5}},{delay:.2f});\n      "
                      f"tl.to('{sel}',{{y:{travel:.0f},duration:{d:.1f},ease:'none'}},{delay:.2f});\n      "
                      f"tl.to('{sel}',{{x:{sway},duration:{d:.1f},ease:'sine.inOut'}},{delay:.2f});\n      "
                      f"tl.to('{sel}',{{opacity:0,duration:.4}},{delay + d - .4:.2f});")
    html = el(sid, name, f'top:0;left:0;width:{W}px;height:{H}px;pointer-events:none;', ''.join(parts))
    return html, '\n      '.join(js)


def birds(sid, name, n, t0, dur, y0=210, spread=240, dir=1, scale=1.0, color='#33302A'):
    """水墨飞鸟群（SVG 双弧翅，横穿 + 扑翼）。"""
    eid = f'{sid}-e{name}'
    parts, js = [], []
    for i in range(n):
        by = y0 + ((i * 149) % spread) - spread / 2
        s = scale * (.65 + (i % 4) * .17)
        x_start = -180 if dir > 0 else W + 180
        parts.append(
            f'<div data-b="{i}" style="position:absolute;top:{by:.0f}px;left:{x_start}px;'
            f'width:{110 * s:.0f}px;height:{64 * s:.0f}px;opacity:0;">'
            f'<svg viewBox="0 0 100 60" width="100%" height="100%">'
            f'<path d="M4,46 Q27,6 50,34 Q73,6 96,46" fill="none" stroke="{color}" '
            f'stroke-width="8" stroke-linecap="round"/></svg></div>')
        sel = f'#{eid} [data-b="{i}"]'
        delay = t0 + (i * .5) % (dur * .4)
        d = dur * (.7 + (i % 3) * .12)
        xoff = (W + 380) * dir
        fl = '\n      '.join(
            f"tl.to('{sel}',{{scaleY:{.5 if k % 2 == 0 else 1},duration:.24,ease:'sine.inOut'}},{delay + .2 + k * .34:.2f});"
            for k in range(7))
        js.append(f"tl.to('{sel}',{{opacity:.92,duration:.4}},{delay:.2f});\n      "
                  f"tl.fromTo('{sel}',{{x:0}},{{x:{xoff},duration:{d:.1f},ease:'none'}},{delay:.2f});\n      "
                  f"{fl}\n      tl.to('{sel}',{{opacity:0,duration:.4}},{delay + d - .3:.2f});")
    html = el(sid, name, f'top:0;left:0;width:{W}px;height:{H}px;pointer-events:none;', ''.join(parts))
    return html, '\n      '.join(js)


def burst(sid, name, cx, cy, n, t, color='#F5F2EA', dist=430, dur=.55):
    """碎点从中心爆散。"""
    eid = f'{sid}-e{name}'
    parts, js = [], []
    for i in range(n):
        ang = i * (360 / n) + (i * 13) % 11
        rad = math.radians(ang)
        dd = dist * (.55 + (i % 3) * .28)
        dx, dy = math.cos(rad) * dd, math.sin(rad) * dd * .85
        s = 6 + (i * 3) % 8
        shape = 'border-radius:50%;' if i % 2 else 'transform:rotate(20deg);'
        parts.append(f'<div data-s="{i}" style="position:absolute;top:{cy - s / 2:.0f}px;left:{cx - s / 2:.0f}px;'
                     f'width:{s}px;height:{s}px;background:{color};opacity:0;{shape}"></div>')
        js.append(f"tl.fromTo('#{eid} [data-s=\"{i}\"]',{{x:0,y:0,opacity:1,scale:1}},"
                  f"{{x:{dx:.0f},y:{dy:.0f},opacity:0,scale:.3,duration:{dur},ease:'power2.out'}},{t:.2f});")
    html = el(sid, name, f'top:0;left:0;width:{W}px;height:{H}px;pointer-events:none;', ''.join(parts))
    return html, '\n      '.join(js)


def scraps(sid, name, n, t, dur=1.4, color='#EFEAE0'):
    """纸屑横穿（切换时的碎纸感）。"""
    eid = f'{sid}-e{name}'
    parts, js = [], []
    for i in range(n):
        py = (i * 211 + 60) % 940 + 30
        w = 26 + (i * 17) % 40
        h = 18 + (i * 11) % 22
        rot = (i * 71) % 360
        parts.append(f'<div data-r="{i}" style="position:absolute;top:{py}px;left:{W + 60}px;width:{w}px;height:{h}px;'
                     f'background:{color};opacity:0;transform:rotate({rot}deg);box-shadow:2px 2px 0 rgba(0,0,0,.18);"></div>')
        sel = f'#{eid} [data-r="{i}"]'
        delay = t + (i * .09) % .5
        d = dur * (.7 + (i % 3) * .15)
        js.append(f"tl.to('{sel}',{{opacity:1,duration:.12,ease:'none'}},{delay:.2f});\n      "
                  f"tl.fromTo('{sel}',{{x:0}},{{x:-{W + 400},y:{(i % 5 - 2) * 40},rotation:{rot + 180},"
                  f"duration:{d:.1f},ease:'power1.in'}},{delay:.2f});\n      "
                  f"tl.to('{sel}',{{opacity:0,duration:.2}},{delay + d - .2:.2f});")
    html = el(sid, name, f'top:0;left:0;width:{W}px;height:{H}px;pointer-events:none;', ''.join(parts))
    return html, '\n      '.join(js)


# ── 字幕带（短语切换 + 逐词提亮）────────────────────────────

def build_caps(words, phrases, tone):
    ph_html = ''.join(
        f'<div class="a-capph" data-ph="{pi}"><div class="a-cap a-cap-{tone}">'
        + ''.join(f'<span data-w="{i}">{words[i]["text"]}</span>' for i in range(i0, i1 + 1))
        + '</div></div>'
        for pi, (i0, i1) in enumerate(phrases))
    js = ''
    for pi, (i0, _i1) in enumerate(phrases):
        t_in = max(0.0, words[i0]['start'] - 0.05)
        if pi > 0:
            js += f'tl.set(\'[data-ph="{pi-1}"]\',{{opacity:0}},{t_in:.2f});\n      '
        js += f'tl.set(\'[data-ph="{pi}"]\',{{opacity:1}},{t_in:.2f});\n      '
    js += ''.join(f'tl.to(\'[data-w="{i}"]\',{{opacity:1,duration:.1,ease:"none"}},{wd["start"]:.2f});'
                  for i, wd in enumerate(words))
    return ph_html, js


# ── wrap：整页模板（与 stylepack.wrap 同形状）───────────────

def wrap(sid, bg_html, body_html, body_js, cap_tone='dark', bg_name='底'):
    sc = META
    scene = sc['scene'][sid]
    words, phrases = sc['words'][sid], sc['phrases'][sid]
    cap_html, cap_js = build_caps(words, phrases, cap_tone)
    css = f"""
@font-face{{font-family:"Microsoft YaHei";src:local("Microsoft YaHei"),local("微软雅黑");}}
@font-face{{font-family:"SimHei";src:local("SimHei"),local("黑体");}}
@font-face{{font-family:"Arial Black";src:local("Arial Black");}}
@font-face{{font-family:"Segoe UI";src:local("Segoe UI");}}
@font-face{{font-family:"Consolas";src:local("Consolas");}}
@font-face{{font-family:"Courier New";src:local("Courier New");}}
@font-face{{font-family:"Georgia";src:local("Georgia");}}
#root{{position:relative;width:{W}px;height:{H}px;overflow:hidden;background:transparent;
  font-family:{CJK};color:{INK};}}
.a-clip{{position:absolute;inset:0;}}
.a-el{{position:absolute;}}
.a-capzone{{position:absolute;left:0;right:0;top:82.5%;bottom:0;}}
.a-capph{{position:absolute;inset:0;display:flex;align-items:flex-start;justify-content:center;
  padding-top:28px;opacity:0;}}
.a-cap{{max-width:1620px;text-align:center;font-size:34px;font-weight:600;line-height:1.5;
  letter-spacing:2px;}}
.a-cap span{{opacity:.4;margin:0 2px;}}
.a-cap-dark{{color:{WHITE};text-shadow:0 1px 4px rgba(0,0,0,.92),0 0 18px rgba(0,0,0,.6);}}
.a-cap-light{{color:#26221C;background:rgba(238,233,222,.9);border:1px solid rgba(38,34,28,.22);
  padding:6px 36px;box-shadow:3px 3px 0 rgba(38,34,28,.14);}}
"""
    return f"""<template>
  <style>{css}</style>
  <div data-composition-id="{sid}" data-width="{W}" data-height="{H}">
    <div id="root">
      <div id="{sid}-bg" class="a-clip clip" data-start="0" data-duration="{scene:.3f}" data-track-index="0" data-hf-name="{bg_name}">{bg_html}</div>
      {body_html}
      <div id="{sid}-capzone" class="a-clip clip a-capzone" data-start="0" data-duration="{scene:.3f}" data-track-index="8" data-hf-name="字幕带">{cap_html}</div>
    </div>
  </div>
  <script>
    (function() {{
      var tl = gsap.timeline({{ paused: true }});
      {body_js}
      {cap_js}
      var pad = {{ v: 0 }};
      tl.to(pad, {{ v: 1, duration: 0.02 }}, {scene - 0.05:.3f});
      window.__timelines["{sid}"] = tl;
    }})();
  </script>
</template>"""


# ── 每段构图（v2：逐元素动效）───────────────────────────────

def seg01(w, dur):
    js = []
    t0 = w('乌', at=.02)
    t1 = w('风雪')
    t2 = w('齿轮')
    t3 = w('审判')
    body = typew('s01', 'a', 150, 150, 'URSUS · EMPIRE ARCHIVE — 015', 30, '#8F8F84')
    js.append(type_on('#s01-ea', t0, step=.04))
    caret = el('s01', 'b', 'top:148px;left:1010px;width:16px;height:40px;background:#B03A40;opacity:0;')
    body += '\n    ' + caret
    js.append(flicker('#s01-eb', t0, n=6, hi=.9, lo=.1, dur=1.1) + '\n      ' +
              f"tl.to('#s01-eb',{{opacity:0,duration:.3}},{t0 + 1.4:.2f});")
    body += '\n    ' + splat('s01', 'c', 560, 300, 800, 420, '#331114', rot=4, opacity=.0)
    js.append(f"tl.fromTo('#s01-ec',{{scale:.3,opacity:0}},{{opacity:.55,duration:.8,ease:'power2.out'}},{t1:.2f});")
    body += '\n    ' + chars_big('s01', 'd', 0, 400, '乌萨斯', 200, WHITE, fam=CJK, ls=26, wpx=W, center=True)
    js.append(slam('#s01-ed', t1, step=.16))
    js.append(shake('#s01-ed', t1 + .62, amp=10, n=3))
    sub = chars_big('s01', 'e', 0, 660, '帝 国 的 心 脏', 62, '#B9B4A6', fam=CJK, wpx=W, center=True)
    body += '\n    ' + sub
    js.append(f"gsap.set('#s01-ee [data-c]',{{opacity:0,x:function(i){{return (i % 2 ? 52 : -52);}}}});\n      "
              f"tl.to('#s01-ee [data-c]',{{opacity:1,x:0,duration:.7,stagger:.06,ease:'power2.out'}},{t1 + .7:.2f});")
    body += '\n    ' + red_bar('s01', 'f', 760, 790, 400, 12)
    js.append(sp.wipe('#s01-ef', t2, dur=.45))
    body += '\n    ' + typew('s01', 'g', 1440, 950, '帧述 · 复刻演示', 26, '#6E6E64')
    js.append(sp.fade('#s01-eg', t3, dur=.5))
    return bg_black('s01'), body + '\n    ' + grain('s01'), '\n      '.join(js), 'dark', '黑底'


def seg02(w, dur):
    js = []
    t0 = w('跪下', at=.05)
    cut = w('战斗')
    card = (f'<div style="position:absolute;top:120px;left:210px;width:1500px;height:840px;'
            f'background:#ECE8DE;transform:rotate(-1.2deg);box-shadow:0 26px 60px rgba(0,0,0,.55);'
            f'overflow:hidden;">'
            f'<img src="assets/img/ak_statue.jpg" style="position:absolute;top:0;left:0;width:520px;height:100%;object-fit:cover;display:block;" />'
            f'<div style="position:absolute;top:0;left:520px;right:0;bottom:0;'
            f'background:linear-gradient(112deg,transparent 0 46%,rgba(120,116,104,.16) 46% 52%,transparent 52%);"></div>'
            f'<div data-bm1 style="position:absolute;top:190px;left:640px;width:720px;height:20px;background:#B9B4A6;transform:rotate(-16deg);opacity:0;"></div>'
            f'<div data-bm2 style="position:absolute;top:700px;left:690px;width:620px;height:14px;background:#B9B4A6;transform:rotate(-16deg);opacity:0;"></div>'
            f'<div style="position:absolute;top:310px;left:600px;font-family:{DISP};font-weight:900;'
            f'font-size:108px;line-height:1.02;color:{INK};transform:skewX(-8deg);white-space:nowrap;letter-spacing:4px;">{chars("НА КОЛЕНИМ!")}</div>'
            f'<div data-card-bar style="position:absolute;top:560px;left:606px;width:640px;height:16px;'
            f'background:{RED};transform:skewX(-8deg);"></div>'
            f'</div>')
    body = f'<div class="a-el" id="s02-card" data-hf-name="海报卡：跪下" style="top:0;left:0;width:{W}px;height:{H}px;">{card}</div>'
    js.append(f"gsap.set('#s02-card',{{y:-170,rotation:-3.6,opacity:0}});\n      "
              f"tl.to('#s02-card',{{y:0,rotation:-1.2,opacity:1,duration:.5,ease:'back.out(1.3)'}},.2);")
    js.append(slam('#s02-card [data-c]', t0, step=.09))
    js.append(shake('#s02-card', t0 + 1.0, amp=9, n=3))
    js.append(sp.draw_x('#s02-card [data-card-bar]', t0 + .5, dur=.35))
    js.append(sp.fade('#s02-card [data-bm1]', .6, dur=.4) + '\n      ' +
              sp.fade('#s02-card [data-bm2]', .78, dur=.4))
    sh, sj = scraps('s02', 'x', 7, t0 + .05)
    body += '\n    ' + sh
    js.append(sj)
    # beat B：В БОЙ 硬切 + 白闪 + 冲击震动
    body += '\n    ' + photo('s02', 'b', 'assets/img/ak_vboy.jpg')
    st, stj = strobe('s02', 'y', cut)
    body += '\n    ' + st
    js.append(stj)
    js.append(hard_in('#s02-eb', cut) + '\n      ' + hard_out('#s02-card', cut))
    js.append(punch('#s02-eb img', cut + .05))
    js.append(shake('#s02-eb', cut + .1, amp=14, n=4))
    js.append(f"tl.to('#s02-eb img',{{scale:1.05,duration:{max(.5, dur - cut - .55):.2f},ease:'none'}},{cut + .45:.2f});")
    body += '\n    ' + red_bar('s02', 'z', 0, 696, 1920, 8, skew=0)
    js.append(sp.draw_x('#s02-ez', cut + .3, dur=.4))
    return bg_black('s02'), body + '\n    ' + grain('s02'), '\n      '.join(js), 'dark', '黑底'


def seg03(w, dur):
    js = []
    t0 = w('为了', at=.04)
    t1 = w('旗帜')
    t2 = w('团结')
    body = bigt('s03', 'a', 0, 120, 'ЗА ЗАВТРА', 168, INK, wpx=W, center=True)
    js.append(slam('#s03-ea', t0, step=.12))
    js.append(shake('#s03-ea', t0 + .75, amp=10, n=3))
    body += '\n    ' + photo('s03', 'b', 'assets/img/ak_flag.jpg', 395, 360, 1130, 480, 'cover')
    js.append(sp.rise('#s03-eb', t0 + .45))
    js.append(wave('#s03-eb', t0 + .9, n=4, amp=3.5, period=1.2))
    body += '\n    ' + skyline('s03', 'f', 880, 200, color='#57534A', seed=3, opacity=.45)
    js.append(f"tl.fromTo('#s03-ef',{{y:210}},{{y:0,duration:.6,ease:'power3.out'}},{t0 + .6:.2f});")
    # beat B：星爆锤子
    body += '\n    ' + photo('s03', 'c', 'assets/img/ak_star.jpg')
    st, stj = strobe('s03', 'g', t1)
    body += '\n    ' + st
    js.append(stj)
    js.append(hard_in('#s03-ec', t1) + '\n      ' + hard_out('#s03-ea', t1) + '\n      ' +
              hard_out('#s03-eb', t1) + '\n      ' + hard_out('#s03-ef', t1))
    js.append(punch('#s03-ec img', t1 + .05))
    js.append(shake('#s03-ec', t1 + .1, amp=13, n=4))
    js.append(f"tl.to('#s03-ec img',{{scale:1.06,duration:{max(.4, t2 - t1 - .5):.2f},ease:'none'}},{t1 + .45:.2f});")
    body += '\n    ' + red_bar('s03', 'h', 0, 320, 1920, 8, skew=0)
    js.append(sp.draw_x('#s03-eh', t1 + .2, dur=.35))
    # beat C：红色 ЕДИНЬ НЕПОБЕДИМЫ
    redc = (f'<div style="position:absolute;inset:0;background:{RED};'
            f'background-image:radial-gradient(ellipse at 50% 30%,rgba(255,255,255,.1),transparent 55%);"></div>'
            f'<div style="position:absolute;top:250px;left:0;width:{W}px;text-align:center;'
            f'font-family:{DISP};font-weight:900;font-size:150px;line-height:1.04;color:#F5F2EA;'
            f'text-shadow:6px 6px 0 rgba(0,0,0,.28);white-space:nowrap;">{chars("ЕДИНЬ —")}</div>'
            f'<div style="position:absolute;top:430px;left:0;width:{W}px;text-align:center;'
            f'font-family:{DISP};font-weight:900;font-size:150px;line-height:1.04;color:#141210;'
            f'white-space:nowrap;">{chars("НЕПОБЕДИМЫ!")}</div>'
            f'<div data-c3-stamp style="position:absolute;top:680px;left:810px;width:300px;height:64px;'
            f'background:#141210;color:#F5F2EA;display:flex;align-items:center;justify-content:center;'
            f'transform:rotate(-2deg);font-family:{CJK};font-weight:900;font-size:36px;letter-spacing:10px;">团结一心</div>')
    body += '\n    ' + f'<div class="a-el" id="s03-red" data-hf-name="红卡：战无不胜" style="top:0;left:0;width:{W}px;height:{H}px;overflow:hidden;">{redc}</div>'
    js.append(sp.wipe('#s03-red', t2, dur=.4))
    js.append(slam('#s03-red [data-c]', t2 + .22, step=.08))
    js.append(shake('#s03-red', t2 + .85, amp=12, n=4))
    js.append(f"gsap.set('#s03-red [data-c3-stamp]',{{scale:1.8,opacity:0}});\n      "
              f"tl.to('#s03-red [data-c3-stamp]',{{opacity:1,scale:1,duration:.24,ease:'power3.in'}},{t2 + 1.05:.2f});")
    bh, bj = burst('s03', 'i', 960, 430, 14, t2 + .3, color='#F5F2EA')
    body += '\n    ' + bh
    js.append(bj)
    return bg_paper('s03'), body + '\n    ' + grain('s03', dark_bg=False), '\n      '.join(js), 'dark', '纸面'


def seg04(w, dur):
    js = []
    cut = w('怒潮')
    body = photo('s04', 'a', 'assets/img/ak_logo.jpg')
    js.append(sp.blur_in('#s04-ea', .4, dur=.7))
    js.append(CAM['drift']('#s04-ea img', cut))
    sw, swj = sweep('s04', 'b', .9, dur=1.0)
    body += '\n    ' + sw
    js.append(swj)
    body += '\n    ' + photo('s04', 'c', 'assets/img/ak_map.jpg')
    st, stj = strobe('s04', 'd', cut)
    body += '\n    ' + st
    js.append(stj)
    js.append(hard_in('#s04-ec', cut) + '\n      ' + hard_out('#s04-ea', cut))
    js.append(punch('#s04-ec img', cut + .05))
    js.append(f"tl.to('#s04-ec img',{{scale:1.06,duration:{max(.4, dur - cut - .5):.2f},ease:'none'}},{cut + .45:.2f});")
    sh, sj = scraps('s04', 'e', 6, cut + .05)
    body += '\n    ' + sh
    js.append(sj)
    return bg_black('s04'), body + '\n    ' + grain('s04'), '\n      '.join(js), 'dark', '黑底'


def seg05(w, dur):
    js = []
    t1 = w('雾里', at=.18)
    t2 = w('雪原', at=.4)
    body = (photo('s05', 'a', 'assets/img/ak_city_gray.jpg', 0, 96, W, 888) + '\n    ' +
            photo('s05', 'b', 'assets/img/ak_city_fog.jpg', 0, 96, W, 888) + '\n    ' +
            photo('s05', 'c', 'assets/img/ak_snow.jpg', 0, 96, W, 888))
    js.append(sp.fade('#s05-ea', .3, dur=.5))
    js.append(punch('#s05-ea img', .3, frm=1.06))
    _ken_d = max(.5, t1 - .8)
    js.append(f"tl.to('#s05-ea img',{{scale:1.06,duration:{_ken_d:.2f},ease:'none'}},{t1 - _ken_d:.2f});")
    sw, swj = sweep('s05', 'd', 1.0, dur=1.0, y=96, h=888, alpha=.26)
    body += '\n    ' + sw
    js.append(swj)
    js.append(hard_in('#s05-eb', t1) + '\n      ' + hard_out('#s05-ea', t1))
    js.append(punch('#s05-eb img', t1 + .05))
    js.append(f"tl.to('#s05-eb img',{{scale:1.05,duration:{max(.4, t2 - t1):.2f},ease:'none'}},{t1 + .4:.2f});")
    js.append(hard_in('#s05-ec', t2) + '\n      ' + hard_out('#s05-eb', t2))
    js.append(punch('#s05-ec img', t2 + .05))
    js.append(shake('#s05-ec', t2 + .08, amp=8, n=3))
    js.append(f"tl.to('#s05-ec img',{{scale:1.06,duration:{max(.6, dur - t2 - .4):.2f},ease:'none'}},{t2 + .4:.2f});")
    # 雾团漂移（两团大雾反向缓移）
    fog1 = el('s05', 'p', 'top:220px;left:-300px;width:1100px;height:340px;pointer-events:none;opacity:.5;'
              'background:radial-gradient(ellipse at center,rgba(236,234,228,.85),transparent 70%);filter:blur(24px);')
    fog2 = el('s05', 'q', 'top:520px;left:900px;width:1200px;height:300px;pointer-events:none;opacity:.4;'
              'background:radial-gradient(ellipse at center,rgba(236,234,228,.8),transparent 70%);filter:blur(28px);')
    body += '\n    ' + fog1 + '\n    ' + fog2
    js.append(f"tl.fromTo('#s05-ep',{{x:0}},{{x:520,duration:{dur:.2f},ease:'none'}},0);\n      "
              f"tl.fromTo('#s05-eq',{{x:0}},{{x:-560,duration:{dur:.2f},ease:'none'}},0);")
    sn, snj = particles('s05', 'r', 22, .4, dur, mode='fall', color='rgba(240,240,236,.85)', area=(0, 0, W, 900))
    body += '\n    ' + sn
    js.append(snj)
    body += '\n    ' + bars('s05', 96)
    return bg_black('s05'), body + '\n    ' + grain('s05'), '\n      '.join(js), 'dark', '黑底'


def seg06(w, dur):
    js = []
    t0 = w('听证会', at=.06)
    t1 = w('审判')
    t2 = w('被告')
    lines = ['ЗАКРЫТОЕ ЗАСЕДАНИЕ', 'СТУДЕНЧЕСКОГО САМОУПРАВЛЕНИЯ', 'ПРОТОКОЛ № 015']
    inner = (f'<div data-r1 style="position:absolute;top:0;left:0;width:100%;text-align:center;'
             f'font-family:{TW};font-size:46px;letter-spacing:14px;color:#EDEAE0;">{chars(lines[0])}</div>'
             f'<div data-r2 style="position:absolute;top:86px;left:0;width:100%;text-align:center;'
             f'font-family:{TW};font-size:38px;letter-spacing:8px;color:#C9C6BB;">{chars(lines[1])}</div>'
             f'<div data-r3 style="position:absolute;top:168px;left:0;width:100%;text-align:center;'
             f'font-family:{TW};font-size:38px;letter-spacing:8px;color:#C9C6BB;">{chars(lines[2])}</div>'
             f'<div data-r4 style="position:absolute;top:250px;left:560px;width:280px;height:8px;background:{RED};"></div>')
    body = el('s06', 'a', 'top:330px;left:460px;width:1000px;height:300px;', inner)
    js.append(type_on('#s06-ea [data-r1]', t0, step=.05))
    js.append(type_on('#s06-ea [data-r2]', t0 + .9, step=.04))
    js.append(type_on('#s06-ea [data-r3]', t0 + 1.7, step=.04))
    js.append(sp.draw_x('#s06-ea [data-r4]', t0 + 2.3, dur=.4))
    body += '\n    ' + photo('s06', 'b', 'assets/img/ak_hearing.jpg')
    st1, st1j = strobe('s06', 'c', t1)
    body += '\n    ' + st1
    js.append(st1j)
    js.append(hard_in('#s06-eb', t1) + '\n      ' + hard_out('#s06-ea', t1))
    js.append(punch('#s06-eb img', t1 + .05))
    js.append(f"tl.to('#s06-eb img',{{scale:1.05,duration:{max(.5, t2 - t1):.2f},ease:'none'}},{t1 + .4:.2f});")
    body += '\n    ' + photo('s06', 'd', 'assets/img/ak_eagle.jpg')
    st2, st2j = strobe('s06', 'e', t2)
    body += '\n    ' + st2
    js.append(st2j)
    js.append(hard_in('#s06-ed', t2) + '\n      ' + hard_out('#s06-eb', t2))
    js.append(punch('#s06-ed img', t2 + .05))
    js.append(f"tl.to('#s06-ed img',{{scale:1.06,duration:{max(.5, dur - t2 - .4):.2f},ease:'none'}},{t2 + .4:.2f});")
    sw, swj = sweep('s06', 'f', t2 + .5, dur=1.0, y=88, h=904, alpha=.22)
    body += '\n    ' + sw
    js.append(swj)
    body += ('\n    ' + el('s06', 'g', 'top:690px;left:0;width:1920px;height:300px;pointer-events:none;'
             'background:linear-gradient(transparent,rgba(0,0,0,.72));'))
    body += '\n    ' + bars('s06', 88)
    return bg_black('s06'), body + '\n    ' + grain('s06'), '\n      '.join(js), 'dark', '黑底'


def seg07(w, dur):
    js = []
    t0 = w('列奥', at=.06)
    t1 = w('编号')
    t2 = w('铅字')
    corners = ''.join(
        f'<div data-cn="{k}" style="position:absolute;{pos};width:64px;height:64px;border-{edge}:4px solid {INK};opacity:0;"></div>'
        for k, (pos, edge) in enumerate([('top:90px;left:110px', 'top'), ('top:90px;right:110px', 'top'),
                                         ('bottom:130px;left:110px', 'bottom'), ('bottom:130px;right:110px', 'bottom')]))
    body = el('s07', 'a', 'top:0;left:0;width:1920px;height:1080px;pointer-events:none;', corners)
    js.append(f"tl.to('#s07-ea [data-cn]',{{opacity:.65,duration:.4,stagger:.12}},{.3:.2f});")
    body += '\n    ' + chars_big('s07', 'b', 0, 330, 'ЛЕОНИД', 224, '#171412', ls=10, wpx=W, center=True)
    js.append(slam('#s07-eb', t0, step=.17, dy=64))
    js.append(shake('#s07-eb', t0 + 1.0, amp=12, n=3))
    body += '\n    ' + bigt('s07', 'c', 0, 640, '( 0 1 5 )', 58, '#4A4438', fam=TW, ls=16, wpx=W, center=True)
    js.append(type_on('#s07-ec', t1, step=.06))
    body += '\n    ' + red_bar('s07', 'd', 650, 780, 620, 12)
    js.append(sp.wipe('#s07-ed', t2, dur=.45))
    body += '\n    ' + stamp('s07', 'e', 1500, 170, '015', 190, 150, 76, rot=-7, filled=False)
    js.append(thud('#s07-ee', t1 + .25, rot=-7))
    return bg_paper('s07'), body + '\n    ' + grain('s07', dark_bg=False), '\n      '.join(js), 'light', '纸面'


def seg08(w, dur):
    js = []
    t0 = w('马特', at=.05)
    t1 = w('炸开')
    t2 = w('素描')
    body = chars_big('s08', 'a', 0, 360, 'МАТВЕЙ', 186, '#171412', ls=8, wpx=W, center=True)
    js.append(slam('#s08-ea', t0, step=.15, dy=60))
    # 爆炸：每个字母向外飞散 + 红射线 + 墨团
    ex = []
    for i in range(6):
        dx = (i - 2.5) * 250 + ((i % 2) * 70 - 35)
        dy = -170 - (i % 3) * 120
        rot = (i * 47) % 80 - 40
        ex.append(f"tl.to('#s08-ea span:nth-child({i + 1})',{{x:{dx},y:{dy},rotation:{rot},opacity:0,"
                  f"duration:.55,ease:'power2.out'}},{t1:.2f});")
    js.append('\n      '.join(ex))
    ry = rays('s08', 'b', 960, 470, n=12, ln=1400, lw=8, color=RED, start=-78, step=14)
    body += '\n    ' + ry
    js.append(f"gsap.set('#s08-eb',{{scale:.06,transformOrigin:'960px 470px',opacity:0}});\n      "
              f"tl.to('#s08-eb',{{scale:1,opacity:1,duration:.4,ease:'power3.out'}},{t1:.2f});\n      "
              f"tl.to('#s08-eb',{{rotation:8,duration:.8,ease:'none'}},{t1 + .1:.2f});\n      "
              f"tl.to('#s08-eb',{{opacity:0,duration:.3}},{t2 - .1:.2f});")
    sp1 = splat('s08', 'c', 700, 260, 420, 300, '#26221C', rot=-14, opacity=.0)
    sp2 = splat('s08', 'd', 1080, 430, 360, 260, RED, rot=10, opacity=.0)
    body += '\n    ' + sp1 + '\n    ' + sp2
    js.append(f"tl.fromTo('#s08-ec',{{scale:.2,opacity:0}},{{scale:1,opacity:.9,duration:.4,ease:'power3.out'}},{t1 + .05:.2f});\n      "
              f"tl.fromTo('#s08-ed',{{scale:.2,opacity:0}},{{scale:1,opacity:.85,duration:.4,ease:'power3.out'}},{t1 + .12:.2f});")
    bh, bj = burst('s08', 'e', 960, 470, 16, t1 + .02, color='#26221C', dist=520)
    body += '\n    ' + bh
    js.append(bj)
    # beat C：素描少女（纸片横飞 + 揭示）
    body += '\n    ' + photo('s08', 'f', 'assets/img/ak_sketchgirl.jpg')
    st, stj = strobe('s08', 'g', t2)
    body += '\n    ' + st
    js.append(stj)
    js.append(sp.wipe('#s08-ef', t2, dur=.5))
    js.append(sp.rise('#s08-ef', t2 + .05, dy=40))
    js.append(CAM['drift']('#s08-ef img', max(.5, dur - t2 - .5)))
    sh, sj = scraps('s08', 'h', 6, t2 + .05)
    body += '\n    ' + sh
    js.append(sj)
    return bg_paper('s08'), body + '\n    ' + grain('s08', dark_bg=False), '\n      '.join(js), 'light', '纸面'


def seg09(w, dur):
    js = []
    t1 = w('旗帜')
    body = photo('s09', 'a', 'assets/img/ak_people.jpg')
    js.append(sp.fade('#s09-ea', .3, dur=.5))
    js.append(CAM['drift']('#s09-ea img', t1))
    sw, swj = sweep('s09', 'b', .9, dur=1.0, alpha=.2)
    body += '\n    ' + sw
    js.append(swj)
    body += '\n    ' + red_bar('s09', 'c', 0, 250, 1920, 6, color='#B9B4A6', skew=0)
    js.append(sp.draw_x('#s09-ec', .7, dur=.6))
    body += '\n    ' + photo('s09', 'd', 'assets/img/ak_redcloak.jpg')
    st, stj = strobe('s09', 'e', t1)
    body += '\n    ' + st
    js.append(stj)
    js.append(hard_in('#s09-ed', t1) + '\n      ' + hard_out('#s09-ea', t1) + '\n      ' + hard_out('#s09-ec', t1))
    js.append(punch('#s09-ed img', t1 + .05))
    js.append(shake('#s09-ed', t1 + .1, amp=12, n=3))
    js.append(f"tl.to('#s09-ed img',{{scale:1.06,duration:{max(.6, dur - t1 - .5):.2f},ease:'none'}},{t1 + .45:.2f});")
    sp1 = splat('s09', 'f', 90, 120, 300, 220, RED, rot=-12, opacity=.0)
    body += '\n    ' + sp1
    js.append(f"tl.fromTo('#s09-ef',{{scale:.2,opacity:0}},{{scale:1,opacity:.8,duration:.35,ease:'power3.out'}},{t1 + .2:.2f});")
    return bg_black('s09'), body + '\n    ' + grain('s09'), '\n      '.join(js), 'light', '黑底'


def seg10(w, dur):
    js = []
    body = photo('s10', 'a', 'assets/img/ak_burncity.jpg')
    js.append(sp.fade('#s10-ea', .3, dur=.6))
    js.append(CAM['zoom_in']('#s10-ea img', dur))
    body += '\n    ' + el('s10', 'b', 'top:0;left:0;width:1920px;height:1080px;pointer-events:none;'
             'background:radial-gradient(ellipse at 62% 42%,rgba(255,150,40,.35),transparent 55%);opacity:.5;')
    js.append(glow_pulse('#s10-eb', .8, dur - 1.0, n=3, lo=.4, hi=.75))
    em, emj = particles('s10', 'c', 16, .8, dur - 1.2, mode='rise', color='rgba(255,176,88,.9)', area=(300, 380, 1500, 860))
    body += '\n    ' + em
    js.append(emj)
    body += '\n    ' + bars('s10', 88)
    return bg_black('s10'), body + '\n    ' + grain('s10'), '\n      '.join(js), 'dark', '黑底'


def _splash_card(sid, w, dur, t0, title_l1, title_l2, cjk, stars_n, char_src,
                 accent, panel_x, label):
    """角色立绘卡通用构图：纸底 + 墨团 + 城市剪影 + 右侧立绘板（撕纸边）+
    标题块侧砸 + 大字逐字砸落 + 星级连弹 + 章砸落。"""
    parts, js = [], []
    sky = skyline(sid, 'a', 780, 300, color='#2A2721', seed=7, opacity=.8)
    parts.append(sky)
    js.append(f"tl.fromTo('#{sid}-ea',{{y:320}},{{y:0,duration:.7,ease:'power3.out'}},{t0:.2f});")
    parts.append(splat(sid, 'b', 110, 230, 700, 360, '#1E1B17', rot=-6, opacity=.0))
    js.append(f"tl.fromTo('#{sid}-eb',{{scale:.25,rotation:-26,opacity:0}},"
              f"{{scale:1,rotation:-6,opacity:.95,duration:.5,ease:'power3.out'}},{t0:.2f});")
    parts.append(splat(sid, 'c', 520, 120, 340, 200, accent, rot=14, opacity=.0))
    js.append(f"tl.fromTo('#{sid}-ec',{{scale:.2,opacity:0}},{{scale:1,opacity:.8,duration:.4,ease:'power3.out'}},{t0 + .15:.2f});")
    panel = (f'<div data-panel style="position:absolute;top:0;left:0;width:1920px;height:1080px;">'
             f'<div style="position:absolute;top:110px;left:{panel_x}px;width:{1920 - panel_x}px;height:900px;overflow:hidden;">'
             f'<img src="{char_src}" style="width:100%;height:100%;object-fit:cover;display:block;" /></div>'
             f'<div style="position:absolute;top:110px;left:{panel_x - 46}px;width:46px;height:900px;background:{PAPER};'
             f'clip-path:polygon(0 0,100% 0,42% 4%,88% 9%,48% 15%,84% 22%,44% 30%,80% 38%,40% 47%,88% 55%,'
             f'50% 63%,86% 71%,46% 80%,82% 88%,52% 95%,100% 100%,0 100%);"></div></div>')
    parts.append(f'<div class="a-el" id="{sid}-panel" data-hf-name="{label}立绘板" style="top:0;left:0;width:1920px;height:1080px;">{panel}</div>')
    js.append(f"gsap.set('#{sid}-panel',{{x:620}});\n      "
              f"tl.to('#{sid}-panel',{{x:0,duration:.6,ease:'back.out(1.15)'}},{t0 + .1:.2f});")
    titles = (f'<div data-t1 style="position:absolute;top:280px;left:150px;background:#1B1815;color:#F2EFE8;'
              f'font-family:{DISP};font-weight:900;font-size:74px;line-height:1;padding:12px 26px;'
              f'transform:skewX(-6deg);white-space:nowrap;">{title_l1}</div>'
              f'<div data-t2 style="position:absolute;top:378px;left:150px;background:{RED};color:#F5F2EA;'
              f'font-family:{DISP};font-weight:900;font-size:74px;line-height:1;padding:12px 26px;'
              f'transform:skewX(-6deg);white-space:nowrap;">{title_l2}</div>')
    parts.append(f'<div class="a-el" id="{sid}-tt" data-hf-name="{label}标题块" style="top:0;left:0;width:{W}px;height:{H}px;">{titles}</div>')
    js.append(f"gsap.set('#{sid}-tt [data-t1]',{{x:-460,opacity:0}});\n      "
              f"tl.to('#{sid}-tt [data-t1]',{{x:0,opacity:1,duration:.4,ease:'power3.out'}},{t0 + .3:.2f});\n      "
              f"gsap.set('#{sid}-tt [data-t2]',{{x:-540,opacity:0}});\n      "
              f"tl.to('#{sid}-tt [data-t2]',{{x:0,opacity:1,duration:.4,ease:'power3.out'}},{t0 + .45:.2f});")
    parts.append(chars_big(sid, 'u', 150, 520, cjk, 118, INK, fam=CJK, ls=8))
    js.append(slam(f'#{sid}-eu', t0 + .6, step=.1))
    js.append(shake(f'#{sid}-eu', t0 + 1.1, amp=10, n=3))
    stars = ''.join(f'<span data-st="{i}" style="display:inline-block;font-size:50px;color:{RED};'
                    f'margin-right:10px;opacity:0;">★</span>' for i in range(stars_n))
    parts.append(el(sid, 'v', 'top:690px;left:154px;', f'<div style="font-family:{DISP};">{stars}</div>'))
    js.append(f"tl.fromTo('#{sid}-ev [data-st]',{{opacity:0,scale:1.6}},{{opacity:1,scale:1,duration:.18,"
              f"stagger:.09,ease:'power2.out'}},{t0 + 1.2:.2f});")
    parts.append(stamp(sid, 'w', 140, 140, '( New )', 230, 88, 44, rot=-8))
    js.append(thud(f'#{sid}-ew', t0 + .9, rot=-8))
    sw, swj = sweep(sid, 'x', t0 + 1.5, dur=1.0, alpha=.2)
    parts.append(sw)
    js.append(swj)
    body = f'<div id="{sid}-cam" style="position:absolute;inset:0;">' + '\n    '.join(parts) + '</div>'
    js.insert(0, CAM['zoom_in'](f'#{sid}-cam', dur))
    return body, '\n      '.join(js)


def seg11(w, dur):
    t0 = w('怒潮', at=.06)
    body, js = _splash_card('s11', w, dur, t0, 'ZIMA THE', 'RAGING TIDE', '怒潮凛冬', 6,
                            'assets/img/ak_zima_char.jpg', '#B03A40', 1040, '怒潮凛冬')
    return bg_paper('s11'), body + '\n    ' + grain('s11', dark_bg=False), js, 'light', '纸面'


def seg12(w, dur):
    t0 = w('集结', at=.08)
    body, js = _splash_card('s12', w, dur, t0, 'УКУСОК', 'UKUSOK', '乌啾', 5,
                            'assets/img/ak_ukusok_char.jpg', '#2E7F7B', 1060, '乌啾')
    bd, bj = birds('s12', 'z', 3, t0 + 1.5, max(2.5, dur - t0 - 2.0), y0=200, spread=180, dir=1, scale=.8, color='#26221C')
    body += '\n    ' + bd
    js += '\n      ' + bj
    return bg_paper('s12'), body + '\n    ' + grain('s12', dark_bg=False), js, 'light', '纸面'


def seg13(w, dur):
    js = []
    t1 = w('繁花')
    body = photo('s13', 'a', 'assets/img/ak_prelude.jpg')
    js.append(sp.wipe('#s13-ea', .35, dur=.6))
    js.append(punch('#s13-ea img', .4))
    ry = rays('s13', 'b', 1450, 300, n=14, ln=900, lw=10, color='rgba(255,214,140,.4)', start=-90, step=13)
    body += '\n    ' + ry
    _ray_d = max(.8, t1 - .6)
    js.append(f"gsap.set('#s13-eb',{{opacity:0,rotation:0,transformOrigin:'1450px 300px'}});\n      "
              f"tl.to('#s13-eb',{{opacity:1,rotation:22,duration:{_ray_d:.2f},ease:'none'}},.5);")
    sw, swj = sweep('s13', 'c', .8, dur=1.1, alpha=.26)
    body += '\n    ' + sw
    js.append(swj)
    body += '\n    ' + photo('s13', 'd', 'assets/img/ak_bloom.jpg')
    st, stj = strobe('s13', 'e', t1)
    body += '\n    ' + st
    js.append(stj)
    js.append(hard_in('#s13-ed', t1) + '\n      ' + hard_out('#s13-ea', t1) + '\n      ' + hard_out('#s13-eb', t1))
    js.append(punch('#s13-ed img', t1 + .05))
    js.append(f"tl.to('#s13-ed img',{{scale:1.05,duration:{max(.6, dur - t1 - .5):.2f},ease:'none'}},{t1 + .45:.2f});")
    pt, ptj = particles('s13', 'f', 12, t1 + .2, dur - t1, mode='petal', color='rgba(244,166,205,.95)', area=(100, 0, 1800, 900))
    body += '\n    ' + pt
    js.append(ptj)
    return bg_black('s13'), body + '\n    ' + grain('s13'), '\n      '.join(js), 'light', '黑底'


def seg14(w, dur):
    js = []
    t0 = w('新增', at=.05)
    t1 = w('解封')
    body = photo('s14', 'a', 'assets/img/ak_files.jpg')
    js.append(sp.fade('#s14-ea', .3, dur=.5))
    js.append(CAM['zoom_in']('#s14-ea img', dur))
    body += '\n    ' + typew('s14', 'b', 150, 150, 'OPERATOR FILES — UNSEALED', 30, '#A7B0B5')
    js.append(type_on('#s14-eb', t0, step=.04))
    body += '\n    ' + stamp('s14', 'c', 1450, 160, '档案解封', 360, 96, 54, rot=-5)
    js.append(thud('#s14-ec', t1, rot=-5))
    js.append(shake('#s14-ec', t1 + .3, amp=8, n=2))
    hexes = ''.join(
        f'<div data-hx="{i}" style="position:absolute;top:{(i % 2) * 66}px;left:{i * 58}px;width:44px;height:50px;'
        f'background:rgba(200,16,46,.75);clip-path:polygon(50% 0,100% 25%,100% 75%,50% 100%,0 75%,0 25%);opacity:0;"></div>'
        for i in range(4))
    body += '\n    ' + el('s14', 'd', 'top:120px;left:1290px;width:300px;height:130px;', hexes)
    js.append(f"tl.fromTo('#s14-ed [data-hx]',{{opacity:0,scale:.3}},{{opacity:1,scale:1,duration:.22,"
              f"stagger:.1,ease:'back.out(2)'}},{t1 + .25:.2f});")
    st, stj = strobe('s14', 'e', .25, peak=.5, dur=.18)
    body += '\n    ' + st
    js.append(stj)
    return bg_dark('s14'), body + '\n    ' + grain('s14'), '\n      '.join(js), 'dark', '深色底'


def seg15(w, dur):
    js = []
    t0 = w('赛季', at=.05)
    t1 = w('保全')
    t2 = w('防线')
    body = chars_big('s15', 'a', 150, 210, 'NEW SEASON', 48, WHITE, ls=12)
    js.append(f"gsap.set('#s15-ea [data-c]',{{opacity:0,x:function(i){{return (i % 2 ? 60 : -60);}}}});\n      "
              f"tl.to('#s15-ea [data-c]',{{opacity:1,x:0,duration:.7,stagger:.05,ease:'power2.out'}},{t0:.2f});")
    band = (f'<div data-band style="position:absolute;top:410px;left:150px;width:1420px;height:176px;'
            f'background:#35D0C8;transform:skewX(-6deg);display:flex;align-items:center;'
            f'padding-left:80px;box-shadow:0 18px 40px rgba(0,0,0,.45);">'
            f'<div style="font-family:{CJK};font-weight:900;font-size:88px;color:#062220;'
            f'letter-spacing:6px;">保全派驻 · 新派驻周期</div></div>')
    body += '\n    ' + el('s15', 'b', 'top:0;left:0;width:1920px;height:1080px;', band)
    js.append(f"gsap.set('#s15-eb [data-band]',{{x:-1720}});\n      "
              f"tl.to('#s15-eb [data-band]',{{x:0,duration:.55,ease:'power4.out'}},{t1:.2f});")
    body += '\n    ' + typew('s15', 'c', 160, 660, 'STATIONARY SECURITY SERVICE', 30, '#BFE8E4', ls=9)
    js.append(type_on('#s15-ec', t1 + .5, step=.035))
    hexes = ''.join(
        f'<div data-hx="{i}" style="position:absolute;top:0;left:{i * 96}px;width:64px;height:72px;'
        f'background:rgba(53,208,200,.16);clip-path:polygon(50% 0,100% 25%,100% 75%,50% 100%,0 75%,0 25%);opacity:0;"></div>'
        for i in range(6))
    body += '\n    ' + el('s15', 'd', 'top:790px;left:160px;width:700px;height:72px;', hexes)
    js.append(f"tl.fromTo('#s15-ed [data-hx]',{{opacity:0,scale:.3}},{{opacity:1,scale:1,duration:.3,"
              f"stagger:.12,ease:'back.out(2)'}},{t2:.2f});")
    streaks = (f'<div data-sk1 style="position:absolute;top:-100px;left:0;width:6px;height:1400px;'
               f'background:linear-gradient(180deg,transparent,rgba(53,208,200,.35),transparent);'
               f'transform:rotate(24deg);"></div>'
               f'<div data-sk2 style="position:absolute;top:-100px;left:600px;width:3px;height:1400px;'
               f'background:linear-gradient(180deg,transparent,rgba(255,255,255,.22),transparent);'
               f'transform:rotate(24deg);"></div>')
    body += '\n    ' + el('s15', 'e', 'top:0;left:0;width:1920px;height:1080px;pointer-events:none;overflow:hidden;', streaks)
    js.append(f"tl.fromTo('#s15-ee [data-sk1]',{{x:-200}},{{x:2200,duration:{dur:.2f},ease:'none'}},0);\n      "
              f"tl.fromTo('#s15-ee [data-sk2]',{{x:400}},{{x:2600,duration:{dur * 1.2:.2f},ease:'none'}},0);")
    return bg_dark('s15'), body + '\n    ' + grain('s15'), '\n      '.join(js), 'dark', '深色底'


def seg16(w, dur):
    js = []
    t0 = w('六套', at=.08)
    t1 = w('洗牌')
    body = photo('s16', 'a', 'assets/img/ak_modules.jpg')
    js.append(sp.fade('#s16-ea', .3, dur=.5))
    js.append(CAM['zoom_in']('#s16-ea img', t1))
    body += '\n    ' + typew('s16', 'b', 150, 150, 'MODULE SYSTEM ×6', 30, '#C9CFD2')
    js.append(type_on('#s16-eb', t0, step=.045))
    body += '\n    ' + stamp('s16', 'c', 1380, 160, '六套新模组', 430, 92, 50, rot=-4)
    js.append(thud('#s16-ec', t0, rot=-4))
    js.append(shake('#s16-ec', t0 + .3, amp=8, n=2))
    body += '\n    ' + photo('s16', 'd', 'assets/img/ak_gameplay.jpg')
    st, stj = strobe('s16', 'e', t1)
    body += '\n    ' + st
    js.append(stj)
    js.append(hard_in('#s16-ed', t1) + '\n      ' + hard_out('#s16-ea', t1) + '\n      ' +
              hard_out('#s16-ec', t1))
    js.append(punch('#s16-ed img', t1 + .05))
    js.append(CAM['pan_right']('#s16-ed img', dur - t1))
    body += '\n    ' + red_bar('s16', 'f', 0, 900, 1920, 8, skew=0)
    js.append(sp.draw_x('#s16-ef', t1 + .25, dur=.4))
    return bg_dark('s16'), body + '\n    ' + grain('s16'), '\n      '.join(js), 'dark', '深色底'


def seg17(w, dur):
    js = []
    t0 = w('跨越', at=.05)
    t2 = w('敬请期待')
    body = photo('s17', 'a', 'assets/img/ak_ending.jpg', 0, 0, W, H, 'cover')
    js.append(sp.blur_in('#s17-ea', .5, dur=.8))
    js.append(CAM['drift']('#s17-ea img', dur))
    b1, bj1 = birds('s17', 'b', 4, t0 + .8, max(2.2, dur * .45), y0=250, spread=170, dir=1, scale=.85, color='#3A362E')
    body += '\n    ' + b1
    js.append(bj1)
    b2, bj2 = birds('s17', 'c', 3, t0 + 2.2, max(2.2, dur * .45), y0=150, spread=130, dir=-1, scale=.65, color='#4A463C')
    body += '\n    ' + b2
    js.append(bj2)
    body += '\n    ' + el('s17', 'd', 'top:800px;left:150px;background:rgba(238,233,222,.92);'
             'border:1px solid rgba(38,34,28,.25);padding:8px 24px;font:28px/1.5 Consolas,\'Courier New\',monospace;'
             'color:#3E3A32;letter-spacing:4px;white-space:nowrap;', chars('BEYOND THE DAWN — THANKFUL SPOTLIGHT'))
    js.append(type_on('#s17-ed', t0, step=.04))
    body += '\n    ' + stamp('s17', 'e', 1540, 130, '帧述 · 复刻', 300, 80, 42, rot=4)
    js.append(thud('#s17-ee', t2, rot=4))
    js.append(shake('#s17-ee', t2 + .3, amp=6, n=2))
    body += '\n    ' + red_bar('s17', 'f', 0, 880, 1920, 6, skew=0)
    js.append(sp.draw_x('#s17-ef', t0 + .6, dur=.7))
    return bg_black('s17'), body + '\n    ' + grain('s17', dark_bg=True), '\n      '.join(js), 'dark', '黑底'


BUILDERS = {'seg01': seg01, 'seg02': seg02, 'seg03': seg03, 'seg04': seg04,
            'seg05': seg05, 'seg06': seg06, 'seg07': seg07, 'seg08': seg08,
            'seg09': seg09, 'seg10': seg10, 'seg11': seg11, 'seg12': seg12,
            'seg13': seg13, 'seg14': seg14, 'seg15': seg15, 'seg16': seg16,
            'seg17': seg17}


def extract_elements(html):
    els = []
    for m in re.finditer(r'<\w+\b[^>]*?id="([^"]+)"[^>]*?data-hf-name="([^"]+)"[^>]*>', html):
        els.append({'id': m.group(1), 'name': m.group(2)})
    return els


def main(proj_dir: str) -> int:
    global META
    proj = pathlib.Path(proj_dir)
    sb = json.load(open(proj / 'storyboards' / 'storyboard.json', encoding='utf-8'))
    META = sp.load(proj)
    (proj / 'llm').mkdir(exist_ok=True)
    for seg in sb['segments']:
        sid = seg['id']
        b = BUILDERS.get(sid)
        if not b:
            print(f'{sid}: 无 builder，跳过')
            return 1
        words = META['words'][sid]
        dur = META['dur'][sid]
        w = make_w(sid, words, dur)
        bg_html, body_html, body_js, tone, bg_name = b(w, dur)
        full = wrap(sid, bg_html, body_html, body_js, tone, bg_name)
        payload = {'html': full, 'elements': extract_elements(full)}
        out = proj / 'llm' / f'comp-{sid}.json'
        json.dump(payload, open(out, 'w', encoding='utf-8'), ensure_ascii=False, indent=1)
        print(f'{sid}: {len(payload["elements"])} 元素 ✓ (旁白 {dur:.1f}s)')
    return 0


if __name__ == '__main__':
    sys.exit(main(sys.argv[1]))
