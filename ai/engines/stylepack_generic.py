# -*- coding: utf-8 -*-
"""StylePack 通用引擎（token 驱动）——一个引擎跑注册表里所有风格。

风格定义在 data/library/registry/styles.json（色板/背景画法/字幕条/元素质感/配图调性），
init_engine 按项目 direction 关键字匹配后调 configure(tokens) 注入本模块全局，
接口与 stylepack（手绘）/stylepack_flat（扁平）完全一致：
  常量 INK/BUTTER/MINT/SKY/CORAL/PEACH/PINK/TURQ + FONT/ARROW/BIG/IMG_ROT/FLAT + THEME
  元素助手 note/label/big/beam/disc/chip/panel
  wrap()（参数化背景 + 字幕条，短语级逐词高亮与手绘引擎同一套逻辑）
动画片段直接复用 stylepack（与风格无关）。
"""
import json
import math
import pathlib
import re

from colorutil import pair as _pair, on as _on  # noqa: F401  WCAG 对比度工具（共享）
import subtitle  # 字幕带共享层（P0-3：三模式 box|line|none）
from stylepack import (  # 动画与数据装载与风格无关，直接复用
    load, pop, fade, rise, draw_x, chars_reveal, stagger_pop, stagger_grow, cue,
    slide, wipe, blur_in, exit_fade,
)

REGISTRY = pathlib.Path(__file__).resolve().parent.parent.parent / 'data' / 'library' / 'registry' / 'styles.json'

# ── 运行时风格状态（configure 注入；给个安全缺省 = 深色中性）─────
FONT = "'Segoe UI','Microsoft YaHei',sans-serif"
INK = '#E8ECF7'       # 描边/线条（落在页面背景上的线）
TXT = '#FFFFFF'       # 直接落在背景上的文字
SURFACE = '#FFFFFF'   # 卡片/表格等表面
SURFACE_TXT = '#141A33'
BUTTER, MINT, SKY, CORAL, PEACH, PINK = '#FBBE00', '#00C2A8', '#4D9DE0', '#E30050', '#FF7A59', '#B388EB'
TURQ = MINT
PRIMARY = '#FBBE00'
ARROW = '#4D9DE0'
BIG = PRIMARY
IMG_ROT = 0.0
FLAT = True
BORDER_W = 0
RADIUS = 18
SHADOW = 'none'
NOTE_BORDER = ''
TITLE = {'weight': 800, 'spacing': 2, 'color': None}
CAP = {'mode': 'bar', 'bg': 'rgba(8,12,32,.72)', 'color': '#FFFFFF', 'border': 'none'}
IMG = {'frame': 'polaroid', 'shadow': '0 16px 40px rgba(0,0,0,.35)'}
THEME = {}
TEXT_GLOW = False


def configure(tokens):
    """把 styles.json 里某风格的 tokens 注入模块全局（init_engine 调用）。"""
    global FONT, INK, TXT, SURFACE, SURFACE_TXT, BUTTER, MINT, SKY, CORAL, PEACH, PINK
    global PRIMARY, ARROW, BIG, IMG_ROT, FLAT, BORDER_W, RADIUS, SHADOW
    global NOTE_BORDER, TITLE, CAP, IMG, THEME, TEXT_GLOW
    FONT = tokens.get('font', FONT)
    INK = tokens.get('ink', INK)
    TXT = tokens.get('txt', TXT)
    SURFACE = tokens.get('surface', SURFACE)
    SURFACE_TXT = tokens.get('surface_txt', SURFACE_TXT)
    accent = tokens.get('accent') or ['#FBBE00', '#00C2A8', '#4D9DE0', '#E30050', '#FF7A59', '#B388EB']
    # 命名色约定：图表系列色顺序 = MINT,SKY,BUTTER,CORAL,PEACH,PINK（init_engine 按此取）
    MINT, SKY, BUTTER, CORAL, PEACH, PINK = [c.upper() for c in (accent + accent[:6])[:6]]
    global TURQ
    TURQ = MINT
    PRIMARY = tokens.get('primary', MINT)
    ARROW = tokens.get('arrow', PRIMARY)
    BIG = tokens.get('primary', PRIMARY)
    IMG_ROT = float(tokens.get('img_rot', 0))
    FLAT = bool(tokens.get('flat', True))
    BORDER_W = int(tokens.get('border_w', 0))
    RADIUS = int(tokens.get('radius', 18))
    SHADOW = tokens.get('shadow', 'none')
    NOTE_BORDER = (f'{BORDER_W}px solid {INK}' if BORDER_W else 'none')
    t = tokens.get('title') or {}
    TITLE = {'weight': t.get('weight', 800), 'spacing': t.get('spacing', 2), 'color': t.get('color')}
    # 字幕（P0-3）：tokens.cap.mode 历史值 bar/pill→box、plain→line；缺省 line（无框细字）。
    # line 档文字色随底色明暗自适应（白字或深字），box 档保持显式配置的胶囊观感。
    c = tokens.get('cap') or {}
    cap_mode = subtitle.resolve_mode(c.get('mode'), default='line')
    cap_color = c.get('color') or ('#FFFFFF' if cap_mode == 'box'
                                   else _on((tokens.get('bg') or {}).get('from', '#101423')))
    CAP = {'mode': cap_mode, 'bg': c.get('bg', 'rgba(8,12,32,.72)'),
           'color': cap_color, 'border': c.get('border', 'none')}
    im = tokens.get('img') or {}
    IMG = {'frame': im.get('frame', 'polaroid'), 'shadow': im.get('shadow', '0 16px 40px rgba(0,0,0,.35)')}
    # text_glow（M-C）：文字辉光（深空霓虹系审美）；scrim_mode：背景图遮罩模式
    # paper|dark|none（render_spec.render_bg_image 消费，深色风格配 dark 保照片影调）。
    TEXT_GLOW = bool(tokens.get('text_glow', False))
    THEME = {'txt': TXT, 'line': INK, 'surface': SURFACE, 'surface_txt': SURFACE_TXT,
             'img_frame': IMG['frame'], 'img_shadow': IMG['shadow'], 'radius': RADIUS,
             'paper': (tokens.get('bg') or {}).get('from', '#101423'),
             'scrim_mode': tokens.get('scrim_mode', 'paper')}
    _BG.clear()
    _BG.update(tokens.get('bg') or {'type': 'solid', 'from': '#101423'})


def match(direction):
    """direction → styles.json 条目。两轮匹配：先完整方向名子串（最高优先），
    再关键词匹配取最长命中（避免短关键词跨风格撞车，如「健康」）。"""
    if not direction:
        return None
    for st in styles():
        d = st.get('direction', '')
        if d and d in direction:
            return st
    best, best_len = None, 0
    for st in styles():
        for kw in st.get('keywords', []):
            if kw and kw in direction and len(kw) > best_len:
                best, best_len = st, len(kw)
    return best


def styles():
    try:
        return json.load(open(REGISTRY, encoding='utf-8'))
    except Exception:
        return []


def _pal(name, default=None):
    if isinstance(name, str) and name.startswith('#'):
        return name  # render_* 侧 col() 已把名字解析成 hex，原样透传
    m = {'butter': BUTTER, 'mint': MINT, 'sky': SKY, 'coral': CORAL, 'peach': PEACH,
         'pink': PINK, 'turq': MINT, 'white': '#FFFFFF', 'ink': INK,
         'navy': PRIMARY, 'green': PRIMARY, 'primary': PRIMARY}
    return m.get(name or '', default)


# ── 背景画法 ────────────────────────────────────────────────────
def _bg_css(f, W, H):
    """tokens 背景 → (css 规则片段, 额外图层 html)。
    底色走 background-color；渐变与 fx 都并入 background-image 列表（渐变是图片，
    若写进 background 简写会被后面的 background-image 覆盖）。
    列表顺序 = 叠层顺序：第一层在最上——fx（暗角/星点/光晕/网格）在前，不透明底渐变必须垫底。
    fx 词表（M-C 扩充）：glow 光晕 / stars 星点 / particles 双层密星（准粒子场）/
    noise 噪点颗粒（胶片感）/ vignette 暗角 / dots / grid / grid_persp。全部确定性。"""
    bg = _BG
    fx = bg.get('fx') or []
    decls, imgs, sizes = [], [], []
    decls.append(f'background-color:{bg.get("from", "#101423")};')
    if 'vignette' in fx:  # 暗角压最上层（列表第一层在最上）
        imgs.append('radial-gradient(ellipse at 50% 44%,transparent 52%,rgba(0,0,0,.46) 100%)')
        sizes.append('auto')
    if 'glow' in fx:
        for g in bg.get('glow') or []:
            col = g.get('color', PRIMARY)
            imgs.append(f'radial-gradient({g.get("r", 45)}% {g.get("r", 40)}% at {g.get("x", 25)}% {g.get("y", 20)}%,'
                        f'{col}{g.get("alpha", "59")} 0%,transparent 70%)')
            sizes.append('auto')
    if 'stars' in fx:
        imgs += _stars(bg.get('fx_color', '#FFFFFF'))
        sizes += ['auto'] * 34
    if 'particles' in fx:  # 双层密星（不同种子/大小/密度，视觉近似粒子场；静态确定性）
        imgs += _stars(bg.get('fx_color', '#FFFFFF'), seed=2, zone=0.94, r_lo=0.8, r_hi=2.2)
        sizes += ['auto'] * 34
    if 'noise' in fx:  # 噪点颗粒：LCG 生成 24 点/160px tile 低透明度平铺（胶片感）
        tiles, tile_size = _noise_tiles(bg.get('fx_color', '#FFFFFF'))
        imgs.append(tiles)
        sizes.append(tile_size)
    if 'dots' in fx:
        col = bg.get('fx_color', INK)
        imgs.append(f'radial-gradient({col}33 2.5px,transparent 3px)')
        sizes.append('64px 64px')
    if 'grid' in fx or 'paper' in fx:
        col = bg.get('fx_color', INK)
        alpha = '30' if 'grid' in fx else '12'
        imgs.append(f'linear-gradient({col}{alpha} 1px,transparent 1px)')
        imgs.append(f'linear-gradient(90deg,{col}{alpha} 1px,transparent 1px)')
        sizes += ['120px 120px'] * 2
    if bg.get('type', 'linear') != 'solid':
        ang = int(bg.get('angle', 180))
        imgs.append(f'linear-gradient({ang}deg,{bg.get("from", "#101423")} 0%,'
                    f'{bg.get("to", bg.get("from", "#101423"))} 100%)')
        sizes.append('auto')
    decls.append(f'background-size:{",".join(sizes)};' if sizes else '')
    css_paper = ''.join(decls) + ('background-image:' + ','.join(imgs) + ';' if imgs else '')
    extra_html = ''
    if 'grid_persp' in fx:
        col = bg.get('fx_color', INK)
        extra_html = (f'<div id="{f}-gridcv" class="{f}-clip clip {f}-gridcv" data-start="0" data-duration="{{scene}}" '
                      f'data-track-index="0" data-hf-name="透视网格背景"><div class="{f}-grid"></div></div>')
        css_paper += (f'.{f}-gridcv{{position:absolute;inset:0;overflow:hidden;}}'
                      f'.{f}-grid{{position:absolute;left:-45%;right:-45%;top:-135%;bottom:-18%;'
                      f'background-image:linear-gradient({col}40 3px,transparent 3px),'
                      f'linear-gradient(90deg,{col}40 3px,transparent 3px);background-size:130px 130px;'
                      f'transform:perspective(1500px) rotateX(30deg);transform-origin:50% 100%;}}')
    return css_paper, extra_html


def _stars(color='#FFFFFF', seed=1, zone=0.82, n=34, r_lo=1.2, r_hi=3.4):
    """确定性星点：LCG 伪随机 → radial-gradient 列表（无运行时随机，渲染可复现）。
    seed/zone/n/半径可调——particles 复用同函数出第二层。"""
    out, s = [], seed
    for _i in range(n):
        s = (s * 48271) % 2147483647
        x = (s % 997) / 10.0
        s = (s * 48271) % 2147483647
        y = (s % 929) / 10.0 * zone       # 集中在上部（避开字幕带）
        s = (s * 48271) % 2147483647
        r = r_lo + (s % round((r_hi - r_lo) * 10)) / 10.0
        out.append(f'radial-gradient({r:.1f}px {r:.1f}px at {x:.1f}% {y:.1f}%,{color} 50%,transparent 55%)')
    return out


def _noise_tiles(color='#FFFFFF'):
    """确定性噪点 tile：LCG 24 点 / 160px 方砖，alpha 3–8%（平铺后 = 胶片颗粒）。
    返回 (单条 background-image 表达式, tile 尺寸)。"""
    parts, s = [], 7
    for _i in range(24):
        s = (s * 48271) % 2147483647
        x = s % 160
        s = (s * 48271) % 2147483647
        y = s % 160
        s = (s * 48271) % 2147483647
        a = 3 + s % 6
        parts.append(f'radial-gradient(1px 1px at {x}px {y}px,{color}{a:02X} 50%,transparent 55%)')
    return ','.join(parts), '160px 160px'


_BG = {'type': 'linear', 'from': '#101423', 'to': '#101423', 'fx': []}


def _font_faces():
    """从字体栈生成 @font-face local 声明（hyperframes check 要求；跳过泛型族）。"""
    out = []
    for m in re.finditer(r"'([^']+)'|\"([^\"]+)\"", FONT):
        name = m.group(1) or m.group(2)
        out.append(f'@font-face{{font-family:"{name}";src:local("{name}");}}')
    return '\n'.join(out)


def css(f, W=1920, H=1080):
    cap_top = round(H * 0.83)
    cap_max = W - 200
    paper_css, grid_extra = _bg_css(f, W, H)
    tcol = TITLE['color'] or TXT
    cap = subtitle.css(f, cap_top, cap_max, mode=CAP['mode'], color=CAP['color'],
                       box_bg=CAP['bg'], box_border=CAP['border'])
    glow_css = ''
    if TEXT_GLOW:  # 文字辉光（tokens.text_glow）：主色双层 text-shadow，深空霓虹质感
        g = PRIMARY
    glow_css = ''
    if TEXT_GLOW:  # 文字辉光（tokens.text_glow）：主色双层 text-shadow，深空霓虹质感
        g = PRIMARY
        glow_css = ('.{f}-title{{text-shadow:0 0 16px {g}66,0 0 44px {g}30;}}' + chr(10) +
                    '.{f}-big{{text-shadow:0 0 14px {g}66,0 0 36px {g}30;}}' + chr(10) +
                    '.{f}-label{{text-shadow:0 0 10px {g}45;}}').format(f=f, g=g)
    return f"""
{_font_faces()}
#root{{position:relative;width:{W}px;height:{H}px;overflow:hidden;background:transparent;
  font-family:{FONT};color:{TXT};}}
.{f}-clip{{position:absolute;inset:0;}}
.{f}-paper{{position:absolute;inset:0;{paper_css}}}
.{f}-el{{position:absolute;}}
.{f}-note{{position:absolute;border:{NOTE_BORDER};border-radius:{RADIUS}px;
  padding:14px 28px;font-size:40px;font-weight:700;white-space:nowrap;
  box-shadow:{SHADOW};}}
{cap}
.{f}-label{{position:absolute;font-size:38px;font-weight:700;color:{TXT};white-space:nowrap;}}
.{f}-title{{position:absolute;left:0;right:0;text-align:center;font-weight:{TITLE["weight"]};
  letter-spacing:{TITLE["spacing"]}px;color:{tcol};}}
{glow_css}
{grid_extra}"""


# ── wrap（与手绘/扁平同一套字幕短语逻辑，背景/字幕条来自 tokens）──
def wrap(proj_dir, frame, sid, body_html, body_js, W=1920, H=1080):
    prefixes = sorted(set(re.findall(r'class="([a-z]{1,2})-', body_html)) | {frame})
    sc = load(proj_dir)
    scene = sc['scene'][sid]
    n = [0]

    def add_id(m):
        tag = m.group(0)
        if 'id="' in tag:
            return tag
        n[0] += 1
        return tag.replace('class="', f'id="{frame}-c{n[0]}" class="', 1)
    body_html = re.sub(r'<div\b[^>]*class="[^"]*clip[^"]*"[^>]*>', add_id, body_html)
    words, phrases = sc['words'][sid], sc['phrases'][sid]
    ph_html = subtitle.phrases_html(frame, words, phrases, mode=CAP['mode'],
                                    phrases_en=sc.get('phrases_en', {}).get(sid))
    capjs = subtitle.timeline_js(words, phrases) if ph_html else ''
    capzone = (f'<div id="{frame}-capzone" class="{frame}-clip clip {frame}-capzone" data-start="0" '
               f'data-duration="{scene:.3f}" data-track-index="8" data-hf-name="字幕带：逐词高亮">{ph_html}</div>'
               if ph_html else '')
    paper_name = '背景'
    style = '\n'.join(css(p, W, H) for p in prefixes)
    grid_extra = f'<div id="{frame}-gridcv" class="{frame}-clip clip {frame}-gridcv" data-start="0" data-duration="{scene:.3f}" data-track-index="0" data-hf-name="透视网格背景"><div class="{frame}-grid"></div></div>'
    if 'grid_persp' not in (_BG.get('fx') or []):
        grid_extra = ''
    return f"""<template>
  <style>{style}</style>
  <div data-composition-id="{sid}" data-width="{W}" data-height="{H}">
    <div id="root">
      <div id="{frame}-paper" class="{frame}-clip clip {frame}-paper" data-start="0" data-duration="{scene:.3f}" data-track-index="0" data-hf-name="{paper_name}"></div>
      {grid_extra}
      {body_html}
      {capzone}
    </div>
  </div>
  <script>
    (function() {{
      var tl = gsap.timeline({{ paused: true }});
      {body_js}
      {capjs}
      var pad = {{ v: 0 }};
      tl.to(pad, {{ v: 1, duration: 0.02 }}, {scene - 0.05:.3f});
      window.__timelines["{sid}"] = tl;
    }})();
  </script>
</template>"""


# ── 元素 HTML 助手（签名与 stylepack 对齐）──────────────────────

def _norm(frame, id=None, prefix=None):
    if isinstance(frame, str) and 0 < len(frame) <= 3:
        return frame
    if id and isinstance(id, str) and '-' in id:
        return id.split('-')[0]
    if prefix:
        return str(prefix)
    return 'x'


def note(frame, id, x, y, text, bg=BUTTER, rot=0, fs=40):
    f = _norm(frame, id)
    c, tc = _pair(_pal(bg, BUTTER))
    return (f'<div class="{f}-note {f}-el" id="{id}" data-hf-name="便签：{text[:12]}" '
            f'style="top:{y}px;left:{x}px;background:{c};color:{tc};font-size:{fs}px;'
            f'transform:rotate({rot}deg);">{text}</div>')


def chip(frame, id, x, y, text, bg=None, fs=40):
    f = _norm(frame, id)
    c, tc = _pair(_pal(bg, MINT))
    return (f'<div class="{f}-el" id="{id}" data-hf-name="标签：{text[:12]}" '
            f'style="top:{y}px;left:{x}px;background:{c};color:{tc};'
            f'border:{NOTE_BORDER};border-radius:{min(RADIUS + 8, 999)}px;'
            f'padding:{int(fs * 0.3)}px {int(fs * 0.85)}px;'
            f'font-size:{fs}px;font-weight:800;white-space:nowrap;box-shadow:{SHADOW};">{text}</div>')


def panel(frame, id, x, y, w, h, title='', text='', bg=None, fs=36):
    f = _norm(frame, id)
    accent, accent_txt = _pair(_pal(bg, PRIMARY))
    name = title or (text or '卡片')[:12]
    inner = ''
    if title:
        inner += (f'<div style="background:{accent};color:{accent_txt};font-weight:800;'
                  f'font-size:{fs + 6}px;padding:{int(fs * 0.42)}px {int(fs * 0.8)}px;'
                  f'border-radius:{RADIUS}px {RADIUS}px 0 0;">{title}</div>')
    if text:
        inner += (f'<div style="padding:{int(fs * 0.55)}px {int(fs * 0.8)}px;font-size:{fs}px;'
                  f'font-weight:600;line-height:1.55;color:{SURFACE_TXT};white-space:pre-wrap;">{text}</div>')
    border = f'border:{BORDER_W}px solid {INK};' if BORDER_W else f'border:1px solid {INK};'
    return (f'<div class="{f}-el" id="{id}" data-hf-name="卡片：{name}" '
            f'style="top:{y}px;left:{x}px;width:{w}px;height:{h}px;background:{SURFACE};'
            f'{border}border-radius:{RADIUS}px;overflow:hidden;box-shadow:{SHADOW};">{inner}</div>')


def label(frame, id, x, y, text, color=None, fs=38):
    f = _norm(frame, id)
    c = color or TXT
    return (f'<div class="{f}-label {f}-el" id="{id}" data-hf-name="标注：{text[:12]}" '
            f'style="top:{y}px;left:{x}px;color:{c};font-size:{fs}px;">{text}</div>')


def big(frame, id, x, y, text, fs=120, color=None):
    f = _norm(frame, id)
    c = color or PRIMARY
    return (f'<div class="{f}-el" id="{id}" data-hf-name="大数字：{text[:10]}" '
            f'style="top:{y}px;left:{x}px;font-size:{fs}px;font-weight:800;color:{c};">{text}</div>')


def beam(frame, id, x, y, w, h=22, bg=None, rot=0, origin='left center'):
    f = _norm(frame, id)
    c = _pal(bg, PRIMARY)
    border = f'border:{BORDER_W}px solid {INK};' if BORDER_W else ''
    return (f'<div class="{f}-el" id="{id}" data-hf-name="色条" '
            f'style="top:{y}px;left:{x}px;width:{w}px;height:{h}px;background:{c};'
            f'{border}border-radius:{min(RADIUS, h // 2 + 2)}px;'
            f'transform:rotate({rot}deg);transform-origin:{origin};"></div>')


def disc(frame, id, cx, cy, r, bg=MINT):
    f = _norm(frame, id)
    c = _pal(bg, MINT)
    border = f'border:{max(BORDER_W, 3)}px solid {INK};' if BORDER_W else ''
    return (f'<div class="{f}-el" id="{id}" data-hf-name="圆盘" '
            f'style="top:{cy - r}px;left:{cx - r}px;width:{2 * r}px;height:{2 * r}px;background:{c};'
            f'{border}border-radius:50%;box-shadow:{SHADOW};"></div>')


_BG = {'type': 'linear', 'from': '#101423', 'to': '#101423', 'fx': []}
