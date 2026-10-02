# -*- coding: utf-8 -*-
"""StylePack 引擎（手绘叙事·纸面马克笔）——首片 v3 布局系统固化为代码。

帧 = 构图原型(archetype) + 元素配置。提供：
  wrap(frame, sid, body, js)            产物模板（clip 时长=场景时长，字幕带，时间轴注册）
  note/label/beam/arrow_radial/converge/polyline/big  元素助手
  CUES 旁白关键词 → 揭示时刻
"""
import json
import math
import pathlib

import subtitle  # 字幕带共享层（P0-3：三模式 box|line|none，四引擎同源）

INK, PAPER, CORAL = '#2D2D2D', '#FDF6E3', '#F8635F'
BUTTER, MINT, SKY, TURQ, PEACH, PINK = '#FDE68A', '#A8E6CF', '#A8D8F0', '#7ECDC0', '#FFCBA4', '#F7C8D4'

_META_CACHE = {}


def load(proj_dir):
    p = pathlib.Path(proj_dir)
    if str(p) not in _META_CACHE:
        meta = json.load(open(p / 'audio_meta.json', encoding='utf-8'))
        words = {v['id']: v['words'] for v in meta['voices']}
        phrases = {}
        for v in meta['voices']:
            ph = v.get('phrases')
            if not ph:  # 旧产物无短语 → 整段一个短语（v1 行为）
                ph = [[0, len(v['words']) - 1]] if v['words'] else []
            phrases[v['id']] = ph
        # 双语字幕开关：project.json bi_subtitle 关掉时即便有旧翻译也不显示
        bi = False
        try:
            bi = bool(json.load(open(p / 'project.json', encoding='utf-8')).get('bi_subtitle'))
        except Exception:
            pass
        phrases_en = {}
        if bi:
            for v in meta['voices']:
                pe = v.get('phrases_en')
                phrases_en[v['id']] = pe if isinstance(pe, list) else []
        _META_CACHE[str(p)] = {
            'phrases_en': phrases_en,
            'words': words,
            'phrases': phrases,
            'dur': {v['id']: v['duration_s'] for v in meta['voices']},
            'scene': {v['id']: round(v['duration_s'] + 0.35, 3) for v in meta['voices']},
        }
    return _META_CACHE[str(p)]


def cue(proj_dir, sid, keyword, default=None):
    """旁白里 keyword 首次出现词的 start 时刻；找不到用 default 或段中点。"""
    w = load(proj_dir)['words'][sid]
    for x in w:
        if keyword in x['text']:
            return round(x['start'], 2)
    return default if default is not None else round(w[len(w)//2]['start'], 2)


def css(prefixes, W=1920, H=1080):
    if isinstance(prefixes, str):
        prefixes = [prefixes]
    return '\n'.join(_css_one(f, W, H) for f in prefixes)


def _css_one(frame, W=1920, H=1080):
    cap_top = round(H * 0.83)   # 字幕带顶：高度 17%（16:9→896，9:16→1594）
    cap_max = W - 200
    return f"""
@font-face{{font-family:"KaiTi";src:local("KaiTi"),local("楷体"),local("SimKai");}}
@font-face{{font-family:"STKaiti";src:local("STKaiti"),local("华文楷体");}}
#root{{position:relative;width:{W}px;height:{H}px;overflow:hidden;background:transparent;
  font-family:"KaiTi","STKaiti",serif;color:{INK};}}
.{frame}-clip{{position:absolute;inset:0;}}
.{frame}-paper{{position:absolute;inset:0;background:{PAPER};
  background-image:linear-gradient(rgba(45,45,45,.045) 1px,transparent 1px),
  linear-gradient(90deg,rgba(45,45,45,.045) 1px,transparent 1px);background-size:120px 120px;}}
.{frame}-el{{position:absolute;}}
.{frame}-note{{position:absolute;border:3px solid {INK};border-radius:6px;box-shadow:5px 5px 0 {INK};
  padding:14px 26px;font-size:40px;font-weight:700;white-space:nowrap;}}
{subtitle.css(frame, cap_top, cap_max, mode='line', color=INK)}
.{frame}-label{{position:absolute;font-size:38px;font-weight:700;color:{INK};white-space:nowrap;}}
.{frame}-title{{position:absolute;left:0;right:0;text-align:center;font-weight:700;letter-spacing:4px;}}
"""


def wrap(proj_dir, frame, sid, body_html, body_js, W=1920, H=1080):
    import re
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
    # 字幕：短语级（v2）——一次只显示当前短语，短语内逐词高亮，短语间硬切；
    # 皮肤走共享层（line 无框档），时序逻辑与旧实现逐字符一致。
    words, phrases = sc['words'][sid], sc['phrases'][sid]
    ph_html = subtitle.phrases_html(frame, words, phrases, mode='line',
                                    phrases_en=sc.get('phrases_en', {}).get(sid))
    capjs = subtitle.timeline_js(words, phrases) if ph_html else ''
    capzone = (f'<div id="{frame}-capzone" class="{frame}-clip clip {frame}-capzone" data-start="0" '
               f'data-duration="{scene:.3f}" data-track-index="8" data-hf-name="字幕带：逐词高亮">{ph_html}</div>'
               if ph_html else '')
    return f"""<template>
  <style>{css(prefixes, W, H)}</style>
  <div data-composition-id="{sid}" data-width="{W}" data-height="{H}">
    <div id="root">
      <div id="{frame}-paper" class="{frame}-clip clip {frame}-paper" data-start="0" data-duration="{scene:.3f}" data-track-index="0" data-hf-name="纸面底+淡网格"></div>
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


# ── 常用 reveal 片段 ─────────────────────────────────────────
def pop(sel, t, scale=.3):
    return f"gsap.set('{sel}',{{opacity:0,scale:{scale},transformOrigin:'50% 50%'}});\n      tl.to('{sel}',{{opacity:1,scale:1,duration:.45,ease:'back.out(2)'}},{t:.2f});"


def fade(sel, t, dur=.4):
    return f"gsap.set('{sel}',{{opacity:0}});\n      tl.to('{sel}',{{opacity:1,duration:{dur},ease:'power2.out'}},{t:.2f});"


def rise(sel, t, dy=30):
    return f"gsap.set('{sel}',{{opacity:0,y:{dy},scale:.9}});\n      tl.to('{sel}',{{opacity:1,y:0,scale:1,duration:.4,ease:'back.out(1.6)'}},{t:.2f});"


def draw_x(sel, t, dur=.5):
    return f"gsap.set('{sel}',{{scaleX:0,transformOrigin:'left center'}});\n      tl.to('{sel}',{{scaleX:1,duration:{dur},ease:'power2.out'}},{t:.2f});"


def chars_reveal(sel, t, step=.22, dy=40):
    return (f"gsap.set('{sel} [data-c]',{{opacity:0,y:{dy}}});\n"
            f"      tl.to('{sel} [data-c]',{{opacity:1,y:0,duration:.3,stagger:{step},ease:'power2.out'}},{t:.2f});")


def stagger_pop(sel, t, step=.3, origin='50% 50%'):
    return (f"gsap.set('{sel}',{{opacity:0,scale:.2,transformOrigin:'{origin}'}});\n"
            f"      tl.to('{sel}',{{opacity:1,scale:1,duration:.35,stagger:{step},ease:'back.out(1.8)'}},{t:.2f});")


def stagger_grow(sel, t, step=.28, origin='center bottom'):
    return (f"gsap.set('{sel}',{{opacity:0,scaleY:.1,transformOrigin:'{origin}'}});\n"
            f"      tl.to('{sel}',{{opacity:1,scaleY:1,duration:.38,stagger:{step},ease:'power2.out'}},{t:.2f});")


# ── 动效词表 v2（slide/wipe/blur/退场；全部走 tl，seek 确定性，无 repeat/yoyo）──
def slide(sel, t, dx=90):
    """侧滑入场（dx 正=从右，负=从左）。"""
    return (f"gsap.set('{sel}',{{opacity:0,x:{dx}}});\n      "
            f"tl.to('{sel}',{{opacity:1,x:0,duration:.5,ease:'power3.out'}},{t:.2f});")


def wipe(sel, t, dur=.55):
    """clip-path 左→右揭示（斜切色带/大字板常用）。"""
    return (f"gsap.set('{sel}',{{clipPath:'inset(0% 100% 0% 0%)'}});\n      "
            f"tl.to('{sel}',{{clipPath:'inset(0% 0% 0% 0%)',duration:{dur},ease:'power2.inOut'}},{t:.2f});")


def blur_in(sel, t, dur=.5):
    """失焦到聚焦入场（氛围主视觉）。"""
    return (f"gsap.set('{sel}',{{opacity:0,filter:'blur(14px)'}});\n      "
            f"tl.to('{sel}',{{opacity:1,filter:'blur(0px)',duration:{dur},ease:'power2.out'}},{t:.2f});")


def exit_fade(sel, t, dur=.35):
    """退场：上飘淡出（讲完即退，给后续元素腾画面）。"""
    return f"tl.to('{sel}',{{opacity:0,y:'-=14',duration:{dur},ease:'power1.in'}},{t:.2f});"


# ── 元素 HTML 助手 ──────────────────────────────────────────
def _norm(frame, id=None, prefix=None):
    """防呆：frame 误传项目路径等长串时，从 id（如 'b-l1'）或 prefix 推导真实前缀字母。"""
    if isinstance(frame, str) and 0 < len(frame) <= 3:
        return frame
    if id and isinstance(id, str) and '-' in id:
        return id.split('-')[0]
    if prefix:
        return str(prefix)
    return 'x'


def note(frame, id, x, y, text, bg=BUTTER, rot=-1.5, fs=40):
    f = _norm(frame, id)
    return (f'<div class="{f}-note {f}-el" id="{id}" data-hf-name="便签：{text[:12]}" '
            f'style="top:{y}px;left:{x}px;background:{bg};transform:rotate({rot}deg);font-size:{fs}px;">{text}</div>')


def note_c(frame, id, y, text, bg=BUTTER, rot=1.5, fs=40):
    """水平居中的便签（用 transform 会与 GSAP 冲突，改用 left:50% margin 平移由宽度近似）。"""
    f = _norm(frame, id)
    return (f'<div class="{f}-note {f}-el" id="{id}" data-hf-name="便签：{text[:12]}" '
            f'style="top:{y}px;left:50%;margin-left:-{len(text)*fs//2+30}px;background:{bg};transform:rotate({rot}deg);font-size:{fs}px;">{text}</div>')


def label(frame, id, x, y, text, color=INK, fs=38):
    f = _norm(frame, id)
    return (f'<div class="{f}-label {f}-el" id="{id}" data-hf-name="标注：{text[:12]}" '
            f'style="top:{y}px;left:{x}px;color:{color};font-size:{fs}px;">{text}</div>')


def big(frame, id, x, y, text, fs=120, color='#B45309'):
    f = _norm(frame, id)
    return (f'<div class="{f}-el" id="{id}" data-hf-name="大数字：{text[:10]}" '
            f'style="top:{y}px;left:{x}px;font-size:{fs}px;font-weight:700;color:{color};">{text}</div>')


def beam(frame, id, x, y, w, h=22, bg='#FFFFFF', rot=0, origin='left center'):
    f = _norm(frame, id)
    return (f'<div class="{f}-el" id="{id}" data-hf-name="光束/条" '
            f'style="top:{y}px;left:{x}px;width:{w}px;height:{h}px;background:{bg};'
            f'border:3px solid {INK};border-radius:12px;transform:rotate({rot}deg);transform-origin:{origin};"></div>')


def disc(frame, id, cx, cy, r, bg=MINT):
    f = _norm(frame, id)
    return (f'<div class="{f}-el" id="{id}" data-hf-name="圆盘" '
            f'style="top:{cy-r}px;left:{cx-r}px;width:{2*r}px;height:{2*r}px;background:{bg};'
            f'border:4px solid {INK};border-radius:50%;box-shadow:6px 6px 0 {INK};"></div>')


def radial_arrows(frame, cx, cy, n, r0=200, ln=150, color=SKY, prefix='arr', start_deg=0):
    """以 (cx,cy) 为中心的 n 向放射箭头，返回 (html, selector)。"""
    frame = _norm(frame, prefix=prefix)
    out = []
    for i in range(n):
        ang = start_deg + i * 360 // n
        rad = math.radians(ang)
        x1, y1 = cx + r0 * math.cos(rad), cy + r0 * math.sin(rad)
        x2, y2 = cx + (r0 + ln) * math.cos(rad), cy + (r0 + ln) * math.sin(rad)
        mx, my = (x1 + x2) / 2, (y1 + y2) / 2
        out.append(f'<div class="{frame}-el" data-{prefix}="{i}" data-hf-name="放射箭头{i+1}" '
                   f'style="top:{my-7:.0f}px;left:{mx-ln/2:.0f}px;width:{ln}px;height:14px;'
                   f'background:{color};border:3px solid {INK};border-radius:8px;transform:rotate({ang}deg);"></div>')
    return '\n    '.join(out), f'[data-{prefix}]'


def converge_beams(frame, cx, cy, specs, color=SKY, prefix='cv'):
    """汇聚光束：specs = [(dx, dy, angle, length)] 相对中心。"""
    frame = _norm(frame, prefix=prefix)
    out = []
    for i, (dx, dy, ang, ln) in enumerate(specs):
        out.append(f'<div class="{frame}-el" data-{prefix}="{i}" data-hf-name="汇聚光束{i+1}" '
                   f'style="top:{cy+dy-8}px;left:{cx+dx-ln/2}px;width:{ln}px;height:16px;'
                   f'background:{color};border:3px solid {INK};border-radius:9px;'
                   f'transform:rotate({ang}deg);"></div>')
    return '\n    '.join(out), f'[data-{prefix}]'


def polyline(frame, id, points, w=1920, h=560, top=170, stroke=INK, nodes=None):
    frame = _norm(frame, id)
    dots = ''.join(f'<circle cx="{nx}" cy="{ny}" r="36" fill="{MINT}" stroke="{INK}" stroke-width="6"/>'
                   for nx, ny in (nodes or []))
    return (f'<svg id="{id}" data-hf-name="路径图" viewBox="0 0 {w} {h}" '
            f'style="position:absolute;top:{top}px;left:0;width:{w}px;height:{h}px;">'
            f'<polyline points="{points}" fill="none" stroke="{stroke}" stroke-width="14" '
            f'stroke-linecap="round" stroke-linejoin="round"/>{dots}</svg>')


def title_chars(text):
    return ''.join(f'<span data-c>{c}</span>' for c in text)


# ── 引擎特征（render_spec 风格适配读取；扁平引擎各有自己的值）──
FONT = 'KaiTi,STKaiti,serif'
ARROW = INK
BIG = '#B45309'
IMG_ROT = 1.8   # 照片默认交替 ±IMG_ROT 旋转（拍立得感）
FLAT = False


def _pal(name, default=None):
    m = {'butter': BUTTER, 'mint': MINT, 'sky': SKY, 'coral': CORAL, 'peach': PEACH,
         'pink': PINK, 'turq': TURQ, 'white': '#FFFFFF', 'ink': INK}
    return m.get(name or '', default)


def chip(frame, id, x, y, text, bg=SKY, fs=40):
    """胶囊标签（手绘版 = 无影小圆角标签，与便签区分）。"""
    f = _norm(frame, id)
    c = _pal(bg, SKY)
    return (f'<div class="{f}-el" id="{id}" data-hf-name="标签：{text[:12]}" '
            f'style="top:{y}px;left:{x}px;background:{c};border:3px solid {INK};border-radius:999px;'
            f'padding:{int(fs * 0.28)}px {int(fs * 0.8)}px;font-size:{fs}px;font-weight:700;'
            f'white-space:nowrap;color:{INK};">{text}</div>')


def panel(frame, id, x, y, w, h, title='', text='', bg=None, fs=36):
    """卡片面板（手绘版 = 纸白卡 + 便签色标题条 + 墨线阴影）。"""
    f = _norm(frame, id)
    accent = _pal(bg, BUTTER)
    name = title or (text or '卡片')[:12]
    inner = ''
    if title:
        inner += (f'<div style="background:{accent};color:{INK};font-weight:700;'
                  f'font-size:{fs + 6}px;padding:{int(fs * 0.4)}px {int(fs * 0.8)}px;'
                  f'border-bottom:3px solid {INK};">{title}</div>')
    if text:
        inner += (f'<div style="padding:{int(fs * 0.55)}px {int(fs * 0.8)}px;font-size:{fs}px;'
                  f'font-weight:700;line-height:1.6;color:{INK};white-space:pre-wrap;">{text}</div>')
    return (f'<div class="{f}-el" id="{id}" data-hf-name="卡片：{name}" '
            f'style="top:{y}px;left:{x}px;width:{w}px;height:{h}px;background:#FFFDF6;'
            f'border:4px solid {INK};border-radius:10px;overflow:hidden;'
            f'box-shadow:7px 7px 0 {INK};">{inner}</div>')
