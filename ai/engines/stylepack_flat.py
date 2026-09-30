# -*- coding: utf-8 -*-
"""StylePack 引擎·扁平图解（厚描边·色块）——仿扁平科普图解片头风格。

与 stylepack（手绘叙事）同一套引擎接口，render_spec 按项目风格方向分发：
  常量 INK/NAVY/GREEN/色板 + FONT/ARROW/BIG/IMG_ROT/FLAT
  元素助手 note/label/big/beam/disc/chip/panel
  wrap()（雾绿底 + 透视网格 + 深色字幕条）
  动画片段直接复用 stylepack（rise/pop/fade/stagger 与风格无关）。
视觉规范：直角构图不旋转、厚描边（4–6px 深藏青）、纯色块白粗字、
  卡片 = 白底 + 强调色描边/标题栏 + 平移浅影；字幕 = 深色圆角条白字。
"""
import re

import subtitle
from stylepack import (  # 动画与数据装载与风格无关，直接复用
    load, pop, fade, rise, draw_x, chars_reveal, stagger_pop, stagger_grow,
    slide, wipe, blur_in, exit_fade,
)

# ── 色板（仿参考片：深藏青 + 图解绿 + 雾绿底）─────────────────
INK = '#1F3B54'      # 深藏青：描边/正文
NAVY = '#1F4066'     # 藏青色块
GREEN = '#3E9073'    # 图解绿：标题栏/胶囊
GREEN_DK = '#1E6B4E' # 深绿：标注文字
MIST = '#ECEFE9'     # 背景
GRID = '#D9DED5'     # 网格线
BUTTER = '#E8B84B'
MINT = '#52A88C'
SKY = '#6FA8C9'
CORAL = '#E2694F'
PEACH = '#F0A07C'
PINK = '#D98CA6'
TURQ = '#4AA9A0'
WHITE = '#FFFFFF'

# 引擎特征（render_spec 按这些 attr 做风格适配）
FLAT = True
FONT = "'Microsoft YaHei','PingFang SC',sans-serif"
ARROW = GREEN_DK          # 箭头默认深绿（仿参考片标注箭头）
BIG = NAVY                # 大数字默认色
IMG_ROT = 0               # 照片不旋转（扁平构图）
DARK_BG = {MINT, SKY, CORAL, NAVY, GREEN, GREEN_DK, TURQ, INK}  # 白字底


def _txt_color(bg):
    return '#FFFFFF' if bg in DARK_BG else INK


def css(f, W=1920, H=1080):
    cap_top = round(H * 0.83)
    cap_max = W - 200
    return f"""
@font-face{{font-family:"Microsoft YaHei";src:local("Microsoft YaHei");}}
@font-face{{font-family:"PingFang SC";src:local("PingFang SC");}}
#root{{position:relative;width:{W}px;height:{H}px;overflow:hidden;background:transparent;
  font-family:{FONT};color:{INK};}}
.{f}-clip{{position:absolute;inset:0;}}
.{f}-paper{{position:absolute;inset:0;background:{MIST};}}
.{f}-gridcv{{position:absolute;inset:0;overflow:hidden;}}
.{f}-grid{{position:absolute;left:-45%;right:-45%;top:-135%;bottom:-18%;
  background-image:linear-gradient({GRID} 3px,transparent 3px),
  linear-gradient(90deg,{GRID} 3px,transparent 3px);background-size:130px 130px;
  transform:perspective(1500px) rotateX(30deg);transform-origin:50% 100%;}}
.{f}-el{{position:absolute;}}
.{f}-note{{position:absolute;border:4px solid {INK};border-radius:14px;
  padding:12px 30px;font-size:40px;font-weight:700;white-space:nowrap;}}
{subtitle.css(f, cap_top, cap_max, mode='line', color=INK)}
.{f}-label{{position:absolute;font-size:38px;font-weight:900;color:{GREEN_DK};white-space:nowrap;}}
.{f}-title{{position:absolute;left:0;right:0;text-align:center;font-weight:900;letter-spacing:2px;color:{INK};}}
"""


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
    # 字幕：与手绘引擎同一套短语级逐词高亮逻辑（共享层 line 档）
    words, phrases = sc['words'][sid], sc['phrases'][sid]
    ph_html = subtitle.phrases_html(frame, words, phrases, mode='line',
                                    phrases_en=sc.get('phrases_en', {}).get(sid))
    capjs = subtitle.timeline_js(words, phrases) if ph_html else ''
    capzone = (f'<div id="{frame}-capzone" class="{frame}-clip clip {frame}-capzone" data-start="0" '
               f'data-duration="{scene:.3f}" data-track-index="8" data-hf-name="字幕条：逐词高亮">{ph_html}</div>'
               if ph_html else '')
    style = '\n'.join(css(p, W, H) for p in prefixes)
    return f"""<template>
  <style>{style}</style>
  <div data-composition-id="{sid}" data-width="{W}" data-height="{H}">
    <div id="root">
      <div id="{frame}-paper" class="{frame}-clip clip {frame}-paper" data-start="0" data-duration="{scene:.3f}" data-track-index="0" data-hf-name="雾绿底"></div>
      <div id="{frame}-gridcv" class="{frame}-clip clip {frame}-gridcv" data-start="0" data-duration="{scene:.3f}" data-track-index="0" data-hf-name="透视网格背景"><div class="{frame}-grid"></div></div>
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


# ── 元素 HTML 助手（签名与 stylepack 对齐；rot 参数收下但不用——扁平直角构图）──

def _norm(frame, id=None, prefix=None):
    if isinstance(frame, str) and 0 < len(frame) <= 3:
        return frame
    if id and isinstance(id, str) and '-' in id:
        return id.split('-')[0]
    if prefix:
        return str(prefix)
    return 'x'


def _pal(name, default=None):
    m = {'butter': BUTTER, 'mint': MINT, 'sky': SKY, 'coral': CORAL, 'peach': PEACH,
         'pink': PINK, 'turq': TURQ, 'white': WHITE, 'ink': INK,
         'navy': NAVY, 'green': GREEN}
    return m.get(name or '', default)


def note(frame, id, x, y, text, bg=BUTTER, rot=0, fs=40):
    f = _norm(frame, id)
    c = _pal(bg, BUTTER)
    return (f'<div class="{f}-note {f}-el" id="{id}" data-hf-name="便签：{text[:12]}" '
            f'style="top:{y}px;left:{x}px;background:{c};color:{_txt_color(c)};font-size:{fs}px;">{text}</div>')


def chip(frame, id, x, y, text, bg=GREEN, fs=40):
    """胶囊标签：纯色块白粗字、无描边（仿参考片「操作系统」）。"""
    f = _norm(frame, id)
    c = _pal(bg, GREEN)
    return (f'<div class="{f}-el" id="{id}" data-hf-name="标签：{text[:12]}" '
            f'style="top:{y}px;left:{x}px;background:{c};color:{_txt_color(c)};'
            f'border-radius:18px;padding:{int(fs * 0.3)}px {int(fs * 0.85)}px;'
            f'font-size:{fs}px;font-weight:900;white-space:nowrap;">{text}</div>')


def panel(frame, id, x, y, w, h, title='', text='', bg=None, fs=36):
    """厚描边卡片：白底 + 强调色描边/标题栏 + 平移浅影。"""
    f = _norm(frame, id)
    accent = _pal(bg, NAVY)
    name = title or (text or '卡片')[:12]
    inner = ''
    if title:
        inner += (f'<div style="background:{accent};color:#FFFFFF;font-weight:900;'
                  f'font-size:{fs + 8}px;padding:{int(fs * 0.42)}px {int(fs * 0.8)}px;">{title}</div>')
    if text:
        inner += (f'<div style="padding:{int(fs * 0.55)}px {int(fs * 0.8)}px;font-size:{fs}px;'
                  f'font-weight:700;line-height:1.55;color:{INK};white-space:pre-wrap;">{text}</div>')
    return (f'<div class="{f}-el" id="{id}" data-hf-name="卡片：{name}" '
            f'style="top:{y}px;left:{x}px;width:{w}px;height:{h}px;background:#FFFFFF;'
            f'border:5px solid {accent};border-radius:22px;overflow:hidden;'
            f'box-shadow:10px 10px 0 rgba(31,59,84,.10);">{inner}</div>')


def label(frame, id, x, y, text, color=None, fs=38):
    f = _norm(frame, id)
    c = color or GREEN_DK
    return (f'<div class="{f}-label {f}-el" id="{id}" data-hf-name="标注：{text[:12]}" '
            f'style="top:{y}px;left:{x}px;color:{c};font-size:{fs}px;">{text}</div>')


def big(frame, id, x, y, text, fs=120, color=None):
    f = _norm(frame, id)
    c = color or NAVY
    return (f'<div class="{f}-el" id="{id}" data-hf-name="大数字：{text[:10]}" '
            f'style="top:{y}px;left:{x}px;font-size:{fs}px;font-weight:900;color:{c};">{text}</div>')


def beam(frame, id, x, y, w, h=22, bg=None, rot=0, origin='left center'):
    f = _norm(frame, id)
    c = _pal(bg, GREEN)
    return (f'<div class="{f}-el" id="{id}" data-hf-name="色条" '
            f'style="top:{y}px;left:{x}px;width:{w}px;height:{h}px;background:{c};'
            f'border:3px solid {INK};border-radius:10px;transform:rotate({rot}deg);transform-origin:{origin};"></div>')


def disc(frame, id, cx, cy, r, bg=MINT):
    f = _norm(frame, id)
    c = _pal(bg, MINT)
    return (f'<div class="{f}-el" id="{id}" data-hf-name="圆盘" '
            f'style="top:{cy - r}px;left:{cx - r}px;width:{2 * r}px;height:{2 * r}px;background:{c};'
            f'border:4px solid {INK};border-radius:50%;box-shadow:8px 8px 0 rgba(31,59,84,.12);"></div>')
