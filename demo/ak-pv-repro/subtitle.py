# -*- coding: utf-8 -*-
"""字幕带共享层（P0-3）：四引擎（楷体/flat/generic/skin）同一套短语 HTML、
时间轴 JS 与三模式样式。

mode：
- box  胶囊/底条（旧观感，风格显式指定才用）
- line 无框细字（默认档——字幕是文案不是 UI），投影随文字色明暗自适应
- none 不出字幕（capzone 一并省略）

时间轴逻辑（短语硬切 + 词级渐亮）与旧实现逐字符一致——只换皮肤不动时序，
tts_align 词级时间戳的消费方式零变化。
"""
import colorutil

MODES = ('box', 'line', 'none')


def resolve_mode(mode, default='line'):
    """归一化各引擎的历史叫法：bar/pill→box、plain→line；未知值走默认。"""
    m = (mode or '').strip().lower()
    return {'box': 'box', 'bar': 'box', 'pill': 'box',
            'line': 'line', 'plain': 'line', 'none': 'none'}.get(m, default)


def phrases_html(frame, words, phrases, mode='line', phrases_en=None):
    """短语胶囊 DOM；none 模式返回 ''（wrap 侧连 capzone 一起省略）。
    phrases_en（双语字幕，M-C）：与 phrases 同序的英文短语 → 每条胶囊挂英文次行。"""
    if mode == 'none':
        return ''
    out = []
    for pi, (i0, i1) in enumerate(phrases):
        en = ''
        if phrases_en and pi < len(phrases_en) and phrases_en[pi]:
            en = f'<div class="{frame}-cap-en">{_esc(phrases_en[pi])}</div>'
        out.append(
            f'<div class="{frame}-capph" data-ph="{pi}"><div class="{frame}-cap">'
            + ''.join(f'<span data-w="{i}">{words[i]["text"]}</span>' for i in range(i0, i1 + 1))
            + f'{en}</div></div>')
    return ''.join(out)


def _esc(s):
    return (s.replace('&', '&amp;').replace('<', '&lt;').replace('>', '&gt;'))


def timeline_js(words, phrases):
    """短语显隐 + 词级渐亮（与旧四引擎实现逐字符一致）。"""
    ph_js = ''
    for pi, (i0, _i1) in enumerate(phrases):
        t_in = max(0.0, words[i0]['start'] - 0.05)
        if pi > 0:
            ph_js += f'tl.set(\'[data-ph="{pi-1}"]\',{{opacity:0}},{t_in:.2f});\n      '
        ph_js += f'tl.set(\'[data-ph="{pi}"]\',{{opacity:1}},{t_in:.2f});\n      '
    return ph_js + ''.join(
        f'tl.to(\'[data-w="{i}"]\',{{opacity:1,duration:.1,ease:"none"}},{w["start"]:.2f});'
        for i, w in enumerate(words))


def css(f, cap_top, cap_max, mode='line', color='#1B2233',
        box_bg='rgba(8,12,32,.72)', box_border='none', box_radius=16, fs=33):
    """capzone/capph 公共规则 + 模式化的 .cap 规则（none 只留公共规则，空挂无副作用）。"""
    common = (f'.{f}-capzone{{position:absolute;left:0;right:0;top:{cap_top}px;bottom:0;}}\n'
              f'.{f}-capph{{position:absolute;inset:0;display:flex;align-items:center;'
              f'justify-content:center;opacity:0;}}')
    if mode == 'none':
        return common
    # 英文次行（双语字幕）：衬线斜体小字，主行下方（box 胶囊内 / line 行下）
    cap_en = (f".{f}-cap-en{{font-size:.58em;font-style:italic;opacity:.72;"
              f"font-family:Georgia,'Times New Roman',serif;letter-spacing:.04em;"
              f"margin-top:.14em;line-height:1.25;}}")
    if mode == 'box':
        cap = (f'.{f}-cap{{max-width:{cap_max}px;background:{box_bg};color:{color};'
               f'border:{box_border};border-radius:{box_radius}px;padding:12px 44px;font-size:{fs}px;'
               f'line-height:1.4;text-align:center;font-weight:600;}}')
    else:  # line：无框细字；浅字配暗影、深字配亮影，任意底色可读
        sh = ('0 2px 14px rgba(0,0,0,.5),0 0 2px rgba(0,0,0,.35)'
              if colorutil.lum(color) > 0.45 else
              '0 2px 10px rgba(255,255,255,.32),0 0 2px rgba(255,255,255,.22)')
        cap = (f'.{f}-cap{{max-width:{cap_max}px;color:{color};padding:6px 28px;font-size:{fs}px;'
               f'line-height:1.5;text-align:center;font-weight:500;letter-spacing:.02em;'
               f'text-shadow:{sh};}}')
    return common + '\n' + cap + f'\n.{f}-cap span{{opacity:.28;margin:0 3px;}}\n' + cap_en
