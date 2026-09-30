# -*- coding: utf-8 -*-
"""StylePack 皮肤引擎（§12.2 风格自由化）：一个引擎跑所有 LLM 自由创作的风格。

皮肤 = 项目 style/skin.json（必填槽位 bg/txt/accent/font + 可选扩展 + 自由 CSS），
样张阶段 LLM 产出、用户确认时锁定。引擎只做「读插座 → 上标准件」：
四个必填槽位经 HSV 推导出全套派生色（图表 6 色/表面色/字幕条/强调件），
自由 CSS 原样追加到每段 <style> 尾部（挂稳定类 .skin-root/.skin-paper +
CSS 变量 --bg/--txt/--accent/--mute），纹理与特效底全由它定义——
引擎不预制任何"风格方向"，风格的上限在皮肤文件里。

接口与 stylepack/stylepack_flat/stylepack_generic 完全一致（duck-typed）。
动画片段直接复用 stylepack（与风格无关）。
"""
import colorsys
import json
import pathlib
import re

from colorutil import mix, on as _on, pair as _pair, ratio as _ratio, ensure_readable
import subtitle  # 字幕带共享层（P0-3：三模式 box|line|none）
from stylepack import (  # 动画与数据装载与风格无关，直接复用
    load, cue, pop, fade, rise, draw_x, chars_reveal, stagger_pop, stagger_grow,
    slide, wipe, blur_in, exit_fade,
)

# ── 运行时皮肤状态（configure 注入；缺省 = 中性深色，工具链冒烟用）────
SKIN_NAME = 'skin'
FONT = "'Microsoft YaHei','SimHei',sans-serif"
BG = '#101423'
TXT = '#F2F4FA'
MUTE = '#A6ADC8'
ACCENT = '#FBBE00'
PRIMARY = ACCENT
ARROW = ACCENT
BIG = ACCENT
INK = TXT                     # 落在背景上的线条/描边
SURFACE = '#1A2138'
SURFACE_TXT = '#F2F4FA'
BUTTER = MINT = SKY = CORAL = PEACH = PINK = ACCENT   # configure 里派生
TURQ = MINT
IMG_ROT = 0.0
FLAT = True
BORDER_W = 0
RADIUS = 14
SHADOW = '0 14px 36px rgba(0,0,0,.35)'
NOTE_BORDER = 'none'
TITLE = {'weight': 800, 'spacing': 2, 'color': None}
CAP = {'mode': 'line', 'bg': 'rgba(8,10,18,.74)', 'color': '#FFFFFF', 'border': 'none'}
IMG = {'frame': 'plain', 'shadow': '0 16px 40px rgba(0,0,0,.4)'}
THEME = {}
SKIN_CSS = ''


# ── 色彩推导 ────────────────────────────────────────────────────

def _hex_rgb(c):
    c = c.lstrip('#')
    return tuple(int(c[i:i + 2], 16) for i in (0, 2, 4))


def _rgb_hex(r, g, b):
    return '#%02X%02X%02X' % (max(0, min(255, round(r))), max(0, min(255, round(g))), max(0, min(255, round(b))))


def _shift(c, dh=0.0, ds=0.0, dl=0.0):
    """hex → HSV 微调 → hex（色相单位 1/360 圈）。"""
    r, g, b = (v / 255 for v in _hex_rgb(c))
    h, l, s = colorsys.rgb_to_hls(r, g, b)
    h = (h + dh) % 1.0
    s = max(0.0, min(1.0, s + ds))
    l = max(0.06, min(0.94, l + dl))
    return _rgb_hex(*(_v * 255 for _v in colorsys.hls_to_rgb(h, l, s)))


def _chart_palette(accent, bg, txt):
    """强调色 → 6 个图表系列色：同族延伸为主（气质统一），补色点缀为辅；
    每个都校准对底色可见（≥1.6:1），保证图表形状读得出。"""
    base = [accent, _shift(accent, dh=0.045, dl=0.10), _shift(accent, dh=-0.05, dl=-0.06),
            _shift(accent, ds=-0.18, dl=0.16), _shift(accent, dh=0.5, ds=-0.25, dl=0.05),
            mix(accent, txt, 0.45)]
    out = []
    for c in base:
        for _ in range(6):
            if _ratio(c, bg) >= 1.6:
                break
            c = _shift(c, dl=0.10 if _ratio(bg, '#FFFFFF') < 0.3 else -0.10)
        out.append(c.upper())
    return out


def configure(skin_path):
    """skin.json → 模块全局（render_spec.init_engine 调用）。槽位是唯一事实源，
    派生色全部可复现（HSV 确定性，无随机）。"""
    global SKIN_NAME, FONT, BG, TXT, MUTE, ACCENT, PRIMARY, ARROW, BIG, INK
    global SURFACE, SURFACE_TXT, BUTTER, MINT, SKY, CORAL, PEACH, PINK, TURQ
    global IMG_ROT, FLAT, BORDER_W, RADIUS, SHADOW, NOTE_BORDER, TITLE, CAP, IMG
    global THEME, SKIN_CSS

    skin = json.load(open(skin_path, encoding='utf-8'))
    s = skin.get('slots') or {}
    SKIN_NAME = skin.get('name') or 'skin'
    BG = (s.get('bg') or BG).upper()
    TXT = (s.get('txt') or TXT).upper()
    ACCENT = (s.get('accent') or ACCENT).upper()
    FONT = s.get('font') or FONT
    MUTE = (s.get('mute') or mix(TXT, BG, 0.38)).upper()
    dark_bg = _ratio(BG, '#FFFFFF') < _ratio(BG, '#000000')
    SURFACE = (s.get('surface') or mix(BG, TXT if dark_bg else '#000000', 0.07)).upper()
    SURFACE_TXT = (s.get('panel_txt') or ensure_readable(TXT, SURFACE, 4.0)).upper()
    RADIUS = int(s.get('radius') or 14)
    BORDER_W = int(s.get('border_w') or 0)
    IMG_ROT = float(s.get('img_rot') or 0)
    MINT, SKY, BUTTER, CORAL, PEACH, PINK = _chart_palette(ACCENT, BG, TXT)
    TURQ = MINT
    PRIMARY = ACCENT
    BIG = ACCENT
    ARROW = ensure_readable(ACCENT, BG, 2.4)
    INK = TXT
    SHADOW = ('0 14px 36px rgba(0,0,0,.45)' if dark_bg else '0 12px 30px rgba(20,16,8,.22)')
    NOTE_BORDER = f'{BORDER_W}px solid {INK}' if BORDER_W else 'none'
    serifish = any(k in FONT.lower() for k in ('song', 'serif', 'kaiti', 'fangsong', 'georgia'))
    TITLE = {'weight': 800, 'spacing': 1 if serifish else 2, 'color': None}
    # 字幕（P0-3）：默认 line 无框细字（色走 txt，浅底自动深字由投影自适应）；
    # 底色向黑收的胶囊配方保留给显式 box 档（皮肤 extra.subtitle_mode 可指定）。
    bar_bg_src = BG if dark_bg else mix(BG, '#000000', 0.72)
    br, bg_, bb = _hex_rgb(mix(bar_bg_src, '#000000', 0.3))
    CAP = {'mode': subtitle.resolve_mode((skin.get('extra') or {}).get('subtitle_mode')),
           'bg': f'rgba({br},{bg_},{bb},.76)', 'color': TXT,
           'border': (f'{max(BORDER_W, 1)}px solid {mix(TXT, BG, 0.7)}' if BORDER_W else 'none')}
    IMG = {'frame': 'clean', 'shadow': SHADOW}  # clean：细描边圆角，线/圆角/表面全走槽位派生
    THEME = {'txt': TXT, 'line': INK, 'surface': SURFACE, 'surface_txt': SURFACE_TXT,
             'img_frame': IMG['frame'], 'img_shadow': IMG['shadow'], 'radius': RADIUS,
             'paper': BG, 'mute': MUTE}
    SKIN_CSS = (skin.get('css') or '').strip()


def _pal(name, default=None):
    if isinstance(name, str) and name.startswith('#'):
        return name
    m = {'butter': BUTTER, 'mint': MINT, 'sky': SKY, 'coral': CORAL, 'peach': PEACH,
         'pink': PINK, 'turq': MINT, 'white': '#FFFFFF', 'ink': INK,
         'navy': PRIMARY, 'green': PRIMARY, 'primary': PRIMARY}
    return m.get(name or '', default)


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
    tcol = TITLE['color'] or TXT
    base = f"""
{_font_faces()}
#root{{position:relative;width:{W}px;height:{H}px;overflow:hidden;background:transparent;
  font-family:{FONT};color:{TXT};}}
.skin-root{{--bg:{BG};--txt:{TXT};--accent:{ACCENT};--mute:{MUTE};--surface:{SURFACE};--font:{FONT};}}
.{f}-clip{{position:absolute;inset:0;}}
.{f}-paper{{position:absolute;inset:0;background-color:{BG};}}
.{f}-el{{position:absolute;}}
.{f}-note{{position:absolute;border:{NOTE_BORDER};border-radius:{RADIUS}px;
  padding:14px 28px;font-size:40px;font-weight:700;white-space:nowrap;
  box-shadow:{SHADOW};}}
{subtitle.css(f, cap_top, cap_max, mode=CAP['mode'], color=CAP['color'],
             box_bg=CAP['bg'], box_border=CAP['border'], box_radius=max(6, RADIUS - 4))}
.{f}-label{{position:absolute;font-size:38px;font-weight:700;color:{TXT};white-space:nowrap;}}
.{f}-title{{position:absolute;left:0;right:0;text-align:center;font-weight:{TITLE["weight"]};
  letter-spacing:{TITLE["spacing"]}px;color:{tcol};}}"""
    if not SKIN_CSS:
        return base
    return base + '\n/* ── 皮肤自由 CSS（§12.2：纹理/特效/装饰，全段共用）── */\n' + SKIN_CSS + '\n'


# ── wrap（字幕短语逻辑与三个旧引擎同一套；背景/挂点来自皮肤）──────
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
    # 字幕：短语逻辑走共享层（P0-3），模式由皮肤 extra.subtitle_mode 决定（默认 line）
    words, phrases = sc['words'][sid], sc['phrases'][sid]
    ph_html = subtitle.phrases_html(frame, words, phrases, mode=CAP['mode'],
                                    phrases_en=sc.get('phrases_en', {}).get(sid))
    capjs = subtitle.timeline_js(words, phrases) if ph_html else ''
    capzone = (f'<div id="{frame}-capzone" class="{frame}-clip clip {frame}-capzone" data-start="0" '
               f'data-duration="{scene:.3f}" data-track-index="8" data-hf-name="字幕带：逐词高亮">{ph_html}</div>'
               if ph_html else '')
    style = '\n'.join(css(p, W, H) for p in prefixes)
    return f"""<template>
  <style>{style}</style>
  <div data-composition-id="{sid}" data-width="{W}" data-height="{H}">
    <div id="root" class="skin-root">
      <div id="{frame}-paper" class="{frame}-clip clip {frame}-paper skin-paper" data-start="0" data-duration="{scene:.3f}" data-track-index="0" data-hf-name="皮肤背景"></div>
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


def _fill_txt(c):
    """色块上的文字色：优先 txt 槽位（气质统一），对比不足退 bg 槽位，再退自动黑白。"""
    if _ratio(TXT, c) >= 3.5:
        return TXT
    if _ratio(BG, c) >= 3.5:
        return BG
    return _on(c)


def note(frame, id, x, y, text, bg=None, rot=0, fs=40):
    f = _norm(frame, id)
    c = _pal(bg, SURFACE)
    tc = _fill_txt(c)
    return (f'<div class="{f}-note {f}-el" id="{id}" data-hf-name="便签：{text[:12]}" '
            f'style="top:{y}px;left:{x}px;background:{c};color:{tc};font-size:{fs}px;'
            f'transform:rotate({rot}deg);">{text}</div>')


def chip(frame, id, x, y, text, bg=None, fs=40):
    f = _norm(frame, id)
    c = _pal(bg, SURFACE)
    tc = _fill_txt(c)
    return (f'<div class="{f}-el" id="{id}" data-hf-name="标签：{text[:12]}" '
            f'style="top:{y}px;left:{x}px;background:{c};color:{tc};'
            f'border:{NOTE_BORDER};border-radius:{min(RADIUS + 8, 999)}px;'
            f'padding:{int(fs * 0.3)}px {int(fs * 0.85)}px;'
            f'font-size:{fs}px;font-weight:800;white-space:nowrap;box-shadow:{SHADOW};">{text}</div>')


def panel(frame, id, x, y, w, h, title='', text='', bg=None, fs=36):
    f = _norm(frame, id)
    accent = _pal(bg, ACCENT)
    accent_txt = _fill_txt(accent)
    name = title or (text or '卡片')[:12]
    inner = ''
    if title:
        inner += (f'<div style="background:{accent};color:{accent_txt};font-weight:800;'
                  f'font-size:{fs + 6}px;padding:{int(fs * 0.42)}px {int(fs * 0.8)}px;'
                  f'border-radius:{RADIUS}px {RADIUS}px 0 0;">{title}</div>')
    if text:
        inner += (f'<div style="padding:{int(fs * 0.55)}px {int(fs * 0.8)}px;font-size:{fs}px;'
                  f'font-weight:600;line-height:1.55;color:{SURFACE_TXT};white-space:pre-wrap;">{text}</div>')
    border = f'border:{BORDER_W}px solid {INK};' if BORDER_W else f'border:1px solid {mix(TXT, BG, 0.72)};'
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


def disc(frame, id, cx, cy, r, bg=None):
    f = _norm(frame, id)
    c = _pal(bg, MINT)
    border = f'border:{max(BORDER_W, 3)}px solid {INK};' if BORDER_W else ''
    return (f'<div class="{f}-el" id="{id}" data-hf-name="圆盘" '
            f'style="top:{cy - r}px;left:{cx - r}px;width:{2 * r}px;height:{2 * r}px;background:{c};'
            f'{border}border-radius:50%;box-shadow:{SHADOW};"></div>')
