# -*- coding: utf-8 -*-
"""spec → 合成物 HTML 渲染器（M1 确定性渲染层）。

LLM 只产出语义 spec（llm/comp-segNN.spec.json：元素种类 + 坐标 + 文案 + 揭示词位），
本脚本把 spec 经 StylePack 引擎渲染成 llm/comp-segNN.json（{html, elements}），
随后 m0 run compositions 落盘到 compositions/frames/。

用法：python ai/render_spec.py <项目目录>
"""
import json
import math
import os
import pathlib
import re
import sys

AI_DIR = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(AI_DIR / 'engines'))
import stylepack as sp  # noqa: E402  默认引擎（手绘叙事）；main 按项目风格方向分发

import colorutil as cu  # noqa: E402  WCAG 对比度工具

COLORS = {}
CHART_FILLS = []
PREFIX = 's'


def init_engine(proj):
    """按项目风格选引擎，重建色表。

    优先级（docs/asset-library.md §12.2 风格自由化）：
    1. style/skin.json（项目自有皮肤，样张阶段 LLM 产出、用户确认锁定）→ stylepack_skin
    2. direction 含「扁平」→ stylepack_flat；含「手绘」→ stylepack（遗留路径）
    3. data/library/registry/styles.json 注册表关键词命中 → stylepack_generic（token 驱动）
    4. 都没有 → 硬报错退出（废除静默回退手绘：样张与成片两张皮就是这么来的）
    """
    global sp, COLORS, CHART_FILLS
    d = ''
    try:
        with open(proj / 'style' / 'style_samples.json', encoding='utf-8') as fh:
            d = (json.load(fh) or {}).get('direction', '')
    except Exception:
        d = ''
    if (proj / 'style' / 'skin.json').exists():
        import stylepack_skin
        stylepack_skin.configure(proj / 'style' / 'skin.json')
        sp = stylepack_skin
    elif '扁平' in d:
        import stylepack_flat
        sp = stylepack_flat
    elif '手绘' in d:
        import stylepack
        sp = stylepack
    elif d:
        import stylepack_generic
        generic = stylepack_generic.match(d)
        if generic:
            stylepack_generic.configure(generic['tokens'])
            sp = stylepack_generic
        else:
            sys.exit(f'风格皮肤缺失：方向「{d}」未命中注册表，项目也无 style/skin.json。'
                     f'旧项目样张未产出风格文件——请重新生成样张（新版样张同时产 skin），'
                     f'或在注册表 data/library/registry/styles.json 补该方向条目。')
    else:
        import stylepack  # 无方向 = 工具链冒烟/元素样张等无项目风格场景
        sp = stylepack
    COLORS = {'butter': sp.BUTTER, 'mint': sp.MINT, 'sky': sp.SKY, 'coral': sp.CORAL,
              'peach': sp.PEACH, 'pink': sp.PINK, 'turq': sp.TURQ, 'ink': sp.INK,
              'white': '#FFFFFF', 'navy': getattr(sp, 'NAVY', sp.INK),
              'green': getattr(sp, 'GREEN', sp.TURQ)}
    CHART_FILLS = [sp.MINT, sp.SKY, sp.BUTTER, sp.CORAL, sp.PEACH, sp.PINK]


# ── 素材 id 解析（§5 引用统一：spec 只持 lib:<id>，渲染前拷回项目）──────

def _library_root(proj):
    """素材库根：项目在 data/projects/<id> → data/library（ZS_LIBRARY 可覆盖）。"""
    env = os.environ.get('ZS_LIBRARY')
    if env:
        return pathlib.Path(env)
    return proj.parents[1] / 'library'


def resolve_lib_images(proj, spec):
    """spec 里 image 元素的 lib:<id> → 项目本地相对路径（幂等映射表缓存）。

    catalog 只读（写者 = Go assetlib）；文件拷入 assets/img/ 保持渲染本地性。
    id 查不到（库被清/跨机迁移）→ src 置空走便签占位，渲染不死。
    """
    imgs = [e for e in spec.get('elements', []) if e.get('kind') == 'image'
            and str(e.get('src', '')).startswith('lib:')]
    if not imgs:
        return
    img_dir = proj / 'assets' / 'img'
    img_dir.mkdir(parents=True, exist_ok=True)
    map_path = img_dir / '.lib-map.json'
    idmap = {}
    try:
        idmap = json.load(open(map_path, encoding='utf-8'))
    except Exception:
        idmap = {}
    catalog = {}
    try:
        for ent in json.load(open(_library_root(proj) / 'catalog.json', encoding='utf-8')):
            catalog[ent.get('id')] = ent.get('file', '')
    except Exception:
        catalog = {}
    changed = False
    for e in imgs:
        aid = e['src'][4:]
        rel = idmap.get(aid)
        if not rel or not (proj / rel).exists():
            f = catalog.get(aid)
            if not f:
                print(f'  素材 {aid} 不在 catalog，兜底便签')
                e['src'] = ''
                continue
            src = _library_root(proj) / f
            name = aid + pathlib.Path(f).suffix.lower()
            (img_dir / name).write_bytes(src.read_bytes())
            rel = f'assets/img/{name}'
            idmap[aid] = rel
            changed = True
        e['src'] = rel
    if changed:
        json.dump(idmap, open(map_path, 'w', encoding='utf-8'), ensure_ascii=False, indent=1)


def th(key, default):
    """引擎主题值（暗底文字/描边/表面色/图片框等）；手绘与扁平引擎无 THEME → 走缺省。"""
    return (getattr(sp, 'THEME', {}) or {}).get(key, default)


def _on_paper(c):
    """文字/装饰色落在页面纸底上：token 引擎按纸底自动加深/提亮过对比门禁。"""
    paper = th('paper', None)
    if not paper:
        return c
    return cu.ensure_readable(c, paper)


def col(name, default):
    return COLORS.get(name or '', default)


def reveal_t(words, idx):
    """词位 → 揭示时刻（提前 0.12s，留入场时间）。"""
    if not words:
        return 0.05
    idx = max(0, min(int(idx), len(words) - 1))
    return max(0.05, round(words[idx]['start'] - 0.12, 2))


def exit_t(words, idx):
    """词位 → 退场时刻（词尾 +0.05s：讲完这个词才退）。"""
    if not words:
        return 1.0
    idx = max(0, min(int(idx), len(words) - 1))
    return max(0.2, round(words[idx]['end'] + 0.05, 2))


# ── 动效词表 v2（Go 契约 anim 白名单与这里一一对应）──────────────
# anim 入场覆盖只对"单根、无隐藏子元素"的 kind 安全：组合类（表格/清单/时间线/图表）
# 的子元素被 gsap.set 藏起、靠专属 js 揭示，根级覆盖会让子元素永远 opacity:0。
SIMPLE_KINDS = {'title', 'note', 'label', 'big', 'beam', 'disc', 'circle', 'chip',
                'icon', 'image', 'emoji', 'panel', 'zone', 'bracket', 'barrow',
                'stat', 'custom', 'formula'}
ANIM_ENTER = {'pop': 'pop', 'fade': 'fade', 'rise': 'rise', 'slide': 'slide',
              'wipe': 'wipe', 'blur': 'blur_in'}
ANIM_CAMERA = {
    'zoom_in':   lambda sel, dur: f"tl.fromTo('{sel}',{{scale:1}},{{scale:1.05,duration:{dur:.2f},ease:'none'}},0);",
    'zoom_out':  lambda sel, dur: f"tl.fromTo('{sel}',{{scale:1.05}},{{scale:1,duration:{dur:.2f},ease:'none'}},0);",
    'pan_left':  lambda sel, dur: f"tl.fromTo('{sel}',{{x:0}},{{x:-36,duration:{dur:.2f},ease:'none'}},0);",
    'pan_right': lambda sel, dur: f"tl.fromTo('{sel}',{{x:0}},{{x:36,duration:{dur:.2f},ease:'none'}},0);",
    'drift':     lambda sel, dur: f"tl.fromTo('{sel}',{{scale:1,x:0}},{{scale:1.035,x:-22,duration:{dur:.2f},ease:'none'}},0);",
}


def camera_spec_js(sid, cs, dur):
    """关键帧镜头（P1-4）：from → to 全参数 fromTo tween（含缺省补齐），整段时长线性/
    缓动插值——t 的纯函数，seek 确定性。范围再钳一道（Go 校验/Sanitize 同界）。"""
    def key(k):
        if not isinstance(k, dict):
            k = {}
        zoom = min(max(float(k.get('zoom') or 1), 1), 1.6)
        px = min(max(float(k.get('pan_x') or 0), -120), 120)
        py = min(max(float(k.get('pan_y') or 0), -120), 120)
        rot = min(max(float(k.get('rot') or 0), -6), 6)
        # pan 正值 = 画面看向右（内容左移）；rotation 单位度
        return {'scale': zoom, 'x': -px, 'y': -py, 'rotation': rot}
    fr, to = key(cs.get('from')), key(cs.get('to'))
    ease = cs.get('ease') or 'power1.inOut'
    if ease not in ('power1.inOut', 'power2.inOut', 'none'):
        ease = 'power1.inOut'
    def d(k):
        return ('{' + ','.join(f"{n}:{v:.3f}" for n, v in k.items()) + '}')
    return (f"tl.fromTo('#{sid}-cam',{d(fr)},{d(to)},{{duration:{dur:.3f},"
            f"transformOrigin:'50% 50%',ease:'{ease}'}},0);")


def render_element(f, sid, e, i, words, W=1920, H=1080):
    """spec 元素 → (html 片段, js 片段)：kind 分发 + 动效词表后处理（anim 入场覆盖 / exit 退场）。"""
    kind = e.get('kind')
    html, js = _render_kind(f, sid, e, i, words, W, H)
    if not html:
        return html, js
    m = re.search(r'id="([^"]+)"', html)
    rid = m.group(1) if m else ''
    # 入场动效覆盖（anim）。chars 只对 title/big 有逐字 span，其余 kind 忽略沿用默认。
    anim = str(e.get('anim') or '').strip()
    if anim and kind in SIMPLE_KINDS and rid:
        sel, t = f'#{rid}', reveal_t(words, e.get('reveal', 0))
        if anim == 'chars' and kind in ('title', 'big'):
            js = sp.chars_reveal(sel, t)
        elif anim == 'none':
            js = ''  # 直出：不藏不揭（custom 垫底板从 0s 可见）
        elif anim in ANIM_ENTER:
            js = getattr(sp, ANIM_ENTER[anim])(sel, t)
    # 退场（exit = 词序号，0/缺省 = 不退场）
    ex = int(e.get('exit', 0) or 0)
    if ex > 0 and rid:
        sel = f'#{rid}'
        if kind == 'arrow':  # 箭头头部是伴随元素，同组退场
            sel += f', [data-{sid}-arr="{i}"]'
        tail = sp.exit_fade(sel, exit_t(words, ex))
        js = f'{js}\n      {tail}' if js else tail
    return html, js


def _render_kind(f, sid, e, i, words, W=1920, H=1080):
    """spec 元素 → (html 片段, js 片段)。id 规则 {sid}-{kind}{i}。"""
    kind = e.get('kind')
    eid = f"{sid}-{kind}{i}"
    t = reveal_t(words, e.get('reveal', 0))
    text = str(e.get('text', '')).strip()
    if kind == 'formula':
        return render_formula(f, sid, e, i, t, W, H)
    if kind == 'scene':
        # 全画幅画布层：不走 reveal/anim/exit 后处理（契约已禁），t 仅作分发占位
        return render_scene(f, sid, e, i, W, H)
    if kind == 'title':
        fs = e.get('fs', 84)
        if W <= 1200 and fs > 72:  # 竖屏窄幅：标题降字号防溢出
            fs = 72
        body = _char_spans(text) if e.get('anim') == 'chars' else text
        html = (f'<div class="{f}-title {f}-el" id="{eid}" data-hf-name="标题：{text[:12]}" '
                f'style="top:{e["y"]}px;font-size:{fs}px;">{body}</div>')
        return html, sp.rise(f'#{eid}', t, dy=26)
    if kind == 'note':
        bg, fs, rot = col(e.get('bg'), sp.BUTTER), e.get('fs', 40), e.get('rot', -1.5)
        html = sp.note(f, eid, e['x'], e['y'], text, bg=bg, rot=rot, fs=fs)
        return html, sp.rise(f'#{eid}', t)
    if kind == 'label':
        fs = e.get('fs', 38)
        color = col(e.get('color'), th('txt', sp.INK))
        html = sp.label(f, eid, e['x'], e['y'], text, color=color, fs=fs)
        return html, sp.fade(f'#{eid}', t)
    if kind == 'big':
        fs = e.get('fs', 110)
        color = _on_paper(col(e.get('color'), getattr(sp, 'BIG', '#B45309')))
        if e.get('anim') == 'chars':  # 逐字强调（倒数/关键数字），不走引擎 big 助手
            html = (f'<div class="{f}-el" id="{eid}" data-hf-name="大字：{text[:8]}" '
                    f'style="top:{e["y"]}px;left:{e["x"]}px;font-size:{fs}px;font-weight:900;'
                    f'color:{color};">{_char_spans(text)}</div>')
            return html, sp.pop(f'#{eid}', t)
        html = sp.big(f, eid, e['x'], e['y'], text, fs=fs, color=color)
        return html, sp.pop(f'#{eid}', t)
    if kind == 'beam':
        bg, rot = col(e.get('bg'), '#FFFFFF'), e.get('rot', 0)
        html = sp.beam(f, eid, e['x'], e['y'], e['w'], h=e.get('h', 22), bg=bg, rot=rot)
        js = sp.draw_x(f'#{eid}', t) if abs(rot) < 0.5 else sp.fade(f'#{eid}', t)
        return html, js
    if kind == 'disc':
        html = sp.disc(f, eid, e['cx'], e['cy'], e['r'], bg=col(e.get('bg'), sp.MINT))
        return html, sp.pop(f'#{eid}', t, scale=.4)
    if kind == 'circle':  # 实心圆点（替代 emoji 的人物/气泡等）
        r, fill = e.get('r', 36), col(e.get('fill'), '#FFFFFF')
        html = (f'<div class="{f}-el" id="{eid}" data-hf-name="圆点" '
                f'style="top:{e["cy"] - r}px;left:{e["cx"] - r}px;width:{2 * r}px;height:{2 * r}px;'
                f'background:{fill};border:3px solid {th("line", sp.INK)};border-radius:50%;"></div>')
        return html, sp.fade(f'#{eid}', t)
    if kind == 'icon':
        return render_icon(f, sid, e, i, t)
    if kind == 'image':
        return render_image(f, sid, e, i, t, W, H)
    if kind == 'panel':
        return render_panel(f, sid, e, i, t)
    if kind == 'chip':
        return render_chip(f, sid, e, i, t)
    if kind == 'zone':
        return render_zone(f, sid, e, i, t)
    if kind == 'timeline':
        return render_timeline(f, sid, e, i, t)
    if kind == 'bracket':
        return render_bracket(f, sid, e, i, t)
    if kind == 'strip':
        return render_strip(f, sid, e, i, t)
    if kind == 'barrow':
        return render_barrow(f, sid, e, i, t)
    if kind == 'table':
        return render_table(f, sid, e, i, t)
    if kind == 'emoji':
        return render_emoji(f, sid, e, i, t)
    if kind == 'chart_bar':
        return render_chart_bar(f, sid, e, i, t)
    if kind == 'chart_line':
        return render_chart_line(f, sid, e, i, t)
    if kind == 'chart_pie':
        return render_chart_pie(f, sid, e, i, t)
    if kind == 'chart_donut':
        return render_chart_donut(f, sid, e, i, t)
    if kind == 'quote':
        return render_quote(f, sid, e, i, t)
    if kind == 'checklist':
        return render_checklist(f, sid, e, i, t)
    if kind == 'stat':
        return render_stat(f, sid, e, i, t)
    if kind == 'arrow':
        x1, y1, x2, y2 = e['x1'], e['y1'], e['x2'], e['y2']
        ln, ang = math.hypot(x2 - x1, y2 - y1), math.degrees(math.atan2(y2 - y1, x2 - x1))
        hw, hw2 = e.get('w', 10) / 2, 34
        c = col(e.get('color'), getattr(sp, 'ARROW', sp.INK))
        name = text[:12] if text else '箭头'
        # 头部 68×68 盒子以 tip(100% 50%) 钉在终点 (x2,y2)，绕 tip 旋转——
        # 任意角度下尖端都精确落在终点；盒子写小一半会整体悬在直线上方（历史 bug）
        line = (f'<div class="{f}-el" id="{eid}" data-hf-name="箭头：{name}" '
                f'style="left:{x1}px;top:{y1 - hw}px;width:{ln:.0f}px;height:{hw * 2}px;background:{c};'
                f'transform:rotate({ang:.1f}deg);transform-origin:left center;border-radius:6px;"></div>')
        head = (f'<div data-{sid}-arr="{i}" style="position:absolute;left:{x2 - 2 * hw2}px;top:{y2 - hw2}px;'
                f'width:{2 * hw2}px;height:{2 * hw2}px;background:{c};transform:rotate({ang:.1f}deg);'
                f'transform-origin:100% 50%;clip-path:polygon(0 12%, 100% 50%, 0 88%);"></div>')
        return f'{line}\n    {head}', sp.fade(f'#{eid}, [data-{sid}-arr="{i}"]', t)
    if kind == 'custom':
        return render_custom(f, sid, e, i, t, W, H)
    raise ValueError(f'未知元素 kind: {kind}')


def extract_elements(html):
    els = []
    for m in re.finditer(r'<\w+\b[^>]*?id="([^"]+)"[^>]*?data-hf-name="([^"]+)"[^>]*>', html):
        els.append({'id': m.group(1), 'name': m.group(2)})
    return els


def _char_spans(text):
    """逐字 span（chars_reveal 的着力点；空格占位防 inline-block 折叠）。"""
    return ''.join(f'<span data-c style="display:inline-block;">{"&nbsp;" if c == " " else c}</span>'
                   for c in text)


# ── image / emoji / 图表（手绘风渲染层）─────────────────────

ICON_DIR = AI_DIR / 'assets' / 'icons'


def _rgba(hex6, a):
    """hex → rgba() 字符串（scrim 渐变用）；非 6 位 hex 回退中性灰。"""
    h = str(hex6).lstrip('#')
    if len(h) != 6:
        return f'rgba(80,80,80,{a})'
    return f'rgba({int(h[0:2], 16)},{int(h[2:4], 16)},{int(h[4:6], 16)},{a})'


def render_bg_image(f, eid, e, t, W, H):
    """全幅背景图（role=bg）：照片满画幅 + 纸色渐变 scrim（越靠字幕带越浓）+
    可选毛玻璃。scrim 用风格纸色打底——深色风格压成暗调、纸面风格提成淡调，
    任何风格下前景 ink 色文字都可读。src 空 = 无图：纸色渐变 + query 淡水印兜底。"""
    q = str(e.get('query', '背景')).strip()[:10]
    src = e.get('src')
    dim = e.get('dim')
    dim = 0.55 if dim is None else max(0.0, min(1.0, float(dim)))
    blur = max(0.0, min(12.0, float(e.get('blur') or 0)))
    paper = th('paper', None) or '#FDF6E3'
    # scrim 模式（M-C token 化）：paper=风格纸色渐变（默认，浅色系）；dark=黑色压暗
    # （深色系保照片影调，不再洗平）；none=无 scrim（前景元素自己保对比）。
    mode = str(th('scrim_mode', 'paper') or 'paper').strip().lower()
    if mode == 'none' and src:
        scrim = ''
    else:
        base = '#000000' if mode == 'dark' else paper
        a_top, a_bot = round(0.28 + 0.38 * dim, 2), round(0.48 + 0.46 * dim, 2)
        scrim = (f'background:linear-gradient(180deg,{_rgba(base, a_top)} 0%,'
                 f'{_rgba(base, a_bot)} 100%);')
    if not src:
        html = (f'<div class="{f}-el" id="{eid}" data-hf-name="背景：{q}" '
                f'style="top:0;left:0;width:{W}px;height:{H}px;{scrim}'
                f'display:flex;align-items:flex-end;padding:36px 60px;'
                f'font-size:30px;font-weight:700;color:{_on_paper(sp.INK)};opacity:.55;">{q}</div>')
        return html, sp.fade(f'#{eid}', t, dur=.6)
    filt = f'filter:blur({blur:.0f}px) saturate(1.05);' if blur else ''
    zoom = 1 + blur / 300 if blur else 1  # blur 边缘露底用放大补偿
    html = (f'<div class="{f}-el" id="{eid}" data-hf-name="背景：{q}" '
            f'style="top:0;left:0;width:{W}px;height:{H}px;overflow:hidden;">'
            f'<img src="{src}" style="position:absolute;inset:0;width:100%;height:100%;'
            f'object-fit:cover;transform:scale({zoom:.3f});{filt}" />'
            f'<div style="position:absolute;inset:0;{scrim}"></div></div>')
    return html, sp.fade(f'#{eid}', t, dur=.6)


def render_image(f, sid, e, i, t, W=1920, H=1080):
    """真实照片：制作期已下载到本地（assets/img/）。相框质感随风格 THEME：
    polaroid（白框手绘感）/ clean（细描边圆角）/ glass（半透明玻璃）。
    role=bg = 全幅背景层（满画幅 + 纸色 scrim，无相框）。
    src 空 = 搜图无源 → 便签兜底（管线不死）。"""
    eid = f"{sid}-img{i}"
    if e.get('role') == 'bg':
        return render_bg_image(f, eid, e, t, W, H)
    q = str(e.get('query', '配图')).strip()[:10]
    w, h = e.get('w', 520), e.get('h', 360)
    src = e.get('src')
    if not src:
        note_html = sp.note(f, eid, e['x'], e['y'], q, bg=sp.SKY, rot=-1.5, fs=38)
        return note_html, sp.rise(f'#{eid}', t)
    rot = e.get('rot')
    if rot is None:
        if getattr(sp, 'THEME', None):  # token 引擎：风格自带照片倾斜角
            rot = getattr(sp, 'IMG_ROT', 0) * (1 if i % 2 else -1)
        else:
            rot = 0 if getattr(sp, 'FLAT', False) else (-1.8 if i % 2 else 1.8)
    frame = th('img_frame', 'polaroid')
    if frame == 'glass':
        box = (f'background:rgba(255,255,255,.10);border:1px solid {th("line", sp.INK)};'
               f'border-radius:{th("radius", 18)}px;padding:0;'
               f'box-shadow:{th("img_shadow", "0 20px 50px rgba(0,0,0,.4)")};backdrop-filter:blur(6px);')
    elif frame == 'clean':
        box = (f'background:{th("surface", "#FFF")};border:1px solid {th("line", sp.INK)};'
               f'border-radius:{th("radius", 12)}px;padding:0;'
               f'box-shadow:{th("img_shadow", "0 12px 28px rgba(0,0,0,.16)")};')
    else:  # polaroid：拍立得白框（手绘/纸面传统）
        box = (f'background:#FFF;border:3px solid {sp.INK};padding:12px 12px 18px;'
               f'box-shadow:{th("img_shadow", f"7px 7px 0 {sp.INK}")};')
    html = (f'<div class="{f}-el" id="{eid}" data-hf-name="图片：{q}" '
            f'style="top:{e["y"]}px;left:{e["x"]}px;width:{w}px;height:{h}px;{box}'
            f'transform:rotate({rot}deg);">'
            f'<img src="{src}" style="width:100%;height:100%;object-fit:cover;display:block;'
            f'border-radius:{th("radius", 8) if frame != "polaroid" else 0}px;" /></div>')
    return html, sp.pop(f'#{eid}', t, scale=.85)


def render_panel(f, sid, e, i, t):
    """卡片面板：白底 + 强调色描边/标题栏（手绘/扁平引擎各有质感）。"""
    eid = f"{sid}-panel{i}"
    html = sp.panel(f, eid, e['x'], e['y'], e.get('w', 560), e.get('h', 320),
                    title=str(e.get('title', '')).strip(), text=str(e.get('text', '')).strip(),
                    bg=col(e.get('bg'), None), fs=e.get('fs', 36))
    return html, sp.rise(f'#{eid}', t)


def render_chip(f, sid, e, i, t):
    """胶囊标签：短词强调（扁平=纯色块白粗字，手绘=圆角标签）。"""
    eid = f"{sid}-chip{i}"
    html = sp.chip(f, eid, e['x'], e['y'], str(e.get('text', '')).strip(),
                   bg=col(e.get('bg'), None), fs=e.get('fs', 40))
    return html, sp.pop(f'#{eid}', t, scale=.7)


# ── 图解结构积木（zone/timeline/bracket/strip/barrow/table）──────

def render_zone(f, sid, e, i, t):
    """淡色高亮区：垫底圈住一组元素（DOM 顺序即 z 序，spec 里放最先）。"""
    eid = f"{sid}-zone{i}"
    bg = col(e.get('bg'), sp.MINT)
    dashed = f'border:3px dashed {bg};' if e.get('dashed') else ''
    html = (f'<div class="{f}-el" id="{eid}" data-hf-name="高亮区" '
            f'style="top:{e["y"]}px;left:{e["x"]}px;width:{e["w"]}px;height:{e["h"]}px;'
            f'background:{bg}26;border-radius:26px;{dashed}"></div>')
    return html, sp.fade(f'#{eid}', t, dur=.5)


def render_timeline(f, sid, e, i, t):
    """垂直时间线：粗竖线 + 彩色圆点 + 粗体文字（步骤/流程）。"""
    eid = f"{sid}-tl{i}"
    x, y = e['x'], e['y']
    gap = e.get('gap', 110)
    nodes = e.get('nodes') or ['步骤']
    c = col(e.get('color'), getattr(sp, 'GREEN', sp.MINT))
    fs = e.get('fs', 40)
    r = 22
    h = (len(nodes) - 1) * gap + 2 * r
    width = 60 + max((len(str(n)) for n in nodes), default=4) * fs * 1.1
    line = (f'<div data-{eid}-line style="position:absolute;top:0;left:{r + 8 - 5}px;width:10px;'
            f'height:{h}px;background:{c};border-radius:5px;"></div>')
    parts = [line]
    for k, txt in enumerate(nodes):
        cy = r + k * gap
        border = '' if getattr(sp, 'FLAT', False) else f'border:4px solid {sp.INK};'
        parts.append(f'<div data-{eid}-dot="{k}" style="position:absolute;top:{cy - r}px;'
                     f'left:{8}px;width:{2 * r}px;height:{2 * r}px;border-radius:50%;'
                     f'background:{c};{border}"></div>')
        parts.append(f'<div data-{eid}-lbl="{k}" style="position:absolute;top:{cy - fs * 0.72}px;'
                     f'left:{2 * r + 34}px;font-size:{fs}px;font-weight:900;'
                     f'color:{th("txt", sp.INK)};white-space:nowrap;">{txt}</div>')
    html = (f'<div class="{f}-el" id="{eid}" data-hf-name="时间线" '
            f'style="top:{y - r - 8}px;left:{x - r - 8}px;width:{width:.0f}px;height:{h + 16:.0f}px;">'
            + ''.join(parts) + '</div>')
    js = (f"gsap.set('#{eid} [data-{eid}-dot],#{eid} [data-{eid}-lbl]',{{opacity:0,x:-16}});\n      "
          f"tl.to('#{eid} [data-{eid}-dot],#{eid} [data-{eid}-lbl]',{{opacity:1,x:0,duration:.35,stagger:.3,ease:'power2.out'}},{t:.2f});")
    return html, js


def render_bracket(f, sid, e, i, t):
    """大括号 + 竖排标注（圈住一组步骤，如「多次重复执行」）。"""
    eid = f"{sid}-brk{i}"
    x, y, h = e['x'], e['y'], e.get('h', 300)
    c = col(e.get('color'), getattr(sp, 'GREEN', sp.MINT))
    txt = str(e.get('text', '')).strip()
    fs = e.get('fs', 44)
    brace_char = '}'
    brace = (f'<div style="position:absolute;top:{-h * 0.08:.0f}px;left:0;height:{h}px;'
             f'line-height:{h}px;font-size:{h * 1.05:.0f}px;font-weight:900;color:{c};">{brace_char}</div>')
    lbl = ''
    if txt:
        lbl = (f'<div style="position:absolute;top:0;left:{h * 0.5:.0f}px;height:{h}px;'
               f'writing-mode:vertical-rl;text-align:center;font-size:{fs}px;font-weight:900;'
               f'color:{c};letter-spacing:8px;">{txt}</div>')
    w = h * 0.5 + fs * 1.3 + 20
    html = (f'<div class="{f}-el" id="{eid}" data-hf-name="括注：{txt[:8]}" '
            f'style="top:{y}px;left:{x}px;width:{w:.0f}px;height:{h}px;">{brace}{lbl}</div>')
    return html, sp.fade(f'#{eid}', t, dur=.5)


def render_strip(f, sid, e, i, t):
    """小方块序列 + 省略号 + 说明（向量/维度示意，如「512维」）。"""
    eid = f"{sid}-strip{i}"
    x, y = e['x'], e['y']
    n = int(e.get('n', 5))
    size = e.get('size', 44)
    gap = 14
    c = col(e.get('color'), sp.SKY)
    txt = str(e.get('text', '')).strip()
    w = n * (size + gap) + 40
    if txt:
        w += len(txt) * 36 + 50
    parts = []
    for k in range(n):
        parts.append(f'<div data-{eid}-sq="{k}" style="position:absolute;top:0;'
                     f'left:{k * (size + gap)}px;width:{size}px;height:{size}px;'
                     f'border-radius:10px;background:{c};border:3px solid {th("line", sp.INK)};"></div>')
    cx = n * (size + gap)
    if e.get('ellipsis', True):
        parts.append(f'<div style="position:absolute;top:{-size * 0.28:.0f}px;left:{cx}px;'
                     f'font-size:{size}px;font-weight:900;color:{th("txt", sp.INK)};">…</div>')
        cx += size * 0.8
    if txt:
        parts.append(f'<div style="position:absolute;top:{size * 0.12:.0f}px;left:{cx + 14}px;'
                     f'font-size:{e.get("fs", 36)}px;font-weight:900;color:{th("txt", sp.INK)};">{txt}</div>')
    html = (f'<div class="{f}-el" id="{eid}" data-hf-name="方块序列：{txt or n}" '
            f'style="top:{y}px;left:{x}px;width:{w:.0f}px;height:{size}px;">' + ''.join(parts) + '</div>')
    js = (f"gsap.set('#{eid} [data-{eid}-sq]',{{opacity:0,scale:.3,transformOrigin:'50% 50%'}});\n      "
          f"tl.to('#{eid} [data-{eid}-sq]',{{opacity:1,scale:1,duration:.3,stagger:.14,ease:'back.out(1.8)'}},{t:.2f});")
    return html, js


def render_barrow(f, sid, e, i, t):
    """粗块箭头（实心大箭头，流程指向）。"""
    eid = f"{sid}-barrow{i}"
    w_ = e.get('w', 260)
    h_ = e.get('h', 90)
    rot = e.get('rot', 0)
    c = col(e.get('color'), getattr(sp, 'ARROW', sp.INK))
    html = (f'<div class="{f}-el" id="{eid}" data-hf-name="块箭头" '
            f'style="top:{e["y"]}px;left:{e["x"]}px;width:{w_}px;height:{h_}px;background:{c};'
            f'clip-path:polygon(0 32%,60% 32%,60% 0,100% 50%,60% 100%,60% 68%,0 68%);'
            f'transform:rotate({rot}deg);"></div>')
    js = sp.draw_x(f'#{eid}', t, dur=.5) if abs(rot) < 10 else sp.pop(f'#{eid}', t, scale=.6)
    return html, js


def render_table(f, sid, e, i, t):
    """格子表格（数字/短文本行列，白底描边圆角格）。"""
    eid = f"{sid}-tbl{i}"
    rows = e.get('rows') or [['?']]
    w_ = e.get('w', 520)
    cell_h = e.get('cell_h', 76)
    fs = e.get('fs', 30)
    nrow = len(rows)
    ncol = max(len(r) for r in rows)
    cw = (w_ - (ncol - 1) * 10) / ncol
    parts = []
    for ri, row in enumerate(rows):
        for ci in range(ncol):
            val = row[ci] if ci < len(row) else ''
            parts.append(f'<div data-{eid}-c="{ri}-{ci}" style="position:absolute;'
                         f'top:{ri * (cell_h + 10)}px;left:{ci * (cw + 10):.0f}px;'
                         f'width:{cw:.0f}px;height:{cell_h}px;background:{th("surface", "#FFFFFF")};'
                         f'border:3px solid {th("line", sp.INK)};border-radius:10px;display:flex;'
                         f'align-items:center;justify-content:center;font-size:{fs}px;'
                         f'font-weight:700;color:{th("surface_txt", sp.INK)};">{val}</div>')
    h_ = nrow * cell_h + (nrow - 1) * 10
    html = (f'<div class="{f}-el" id="{eid}" data-hf-name="表格" '
            f'style="top:{e["y"]}px;left:{e["x"]}px;width:{w_}px;height:{h_}px;">' + ''.join(parts) + '</div>')
    js = sp.stagger_pop(f'#{eid} [data-{eid}-c]', t, step=.12)
    return html, js


def render_emoji(f, sid, e, i, t):
    """大号彩色 emoji（本地系统字体，确定性渲染）。"""
    eid = f"{sid}-emoji{i}"
    fs = e.get('fs', 140)
    ch = (e.get('text') or '✨')[:4]
    html = (f'<div class="{f}-el" id="{eid}" data-hf-name="表情：{ch}" '
            f'style="top:{e["y"]}px;left:{e["x"]}px;font-size:{fs}px;line-height:1.1;'
            f"font-family:'Segoe UI Emoji','Apple Color Emoji','Noto Color Emoji',sans-serif;\">{ch}</div>")
    return html, sp.pop(f'#{eid}', t, scale=.5)


def render_icon(f, sid, e, i, t):
    """本地 lucide 线稿图标：inline 进合成物（无网络/无外链，lint 安全）。"""
    size = e.get('size', 120)
    rot = e.get('rot', 0)
    eid = f"{sid}-icon{i}"
    name = str(e.get('name', '')).strip()
    path = ICON_DIR / f"{name}.svg"
    if not path.exists():  # 兜底：未知图标 → 便签盒子 + 名字（不阻塞管线）
        note_html = sp.note(f, eid, e['x'], e['y'], name[:10] or '?', bg=sp.BUTTER, rot=-1.5, fs=36)
        return note_html, sp.rise(f'#{eid}', t)
    svg = path.read_text(encoding='utf-8')
    svg = re.sub(r'<!--.*?-->', '', svg, flags=re.S)          # 许可注释不入帧
    svg = re.sub(r'\sclass="[^"]*"', '', svg)                  # 去 lucide class
    svg = re.sub(r'<svg\b', '<svg width="100%" height="100%"', svg, count=1)
    stroke = e.get('color') or th('txt', sp.INK)  # 图标是视觉锚点：用正文色而非半透明描边色
    sw = 2 if size >= 140 else 1.5                             # 24 viewBox：放大后描边视觉补偿
    svg = re.sub(r'stroke-width="[\d.]+"', f'stroke-width="{sw}"', svg, count=1)
    rot_css = f'transform:rotate({rot}deg);' if abs(rot) > 0.01 else ''
    html = (f'<div class="{f}-el" id="{eid}" data-hf-name="图标：{name}" '
            f'style="top:{e["y"]}px;left:{e["x"]}px;width:{size}px;height:{size}px;'
            f'color:{stroke};{rot_css}">{svg}</div>')
    return html, sp.pop(f'#{eid}', t, scale=.55)


def render_quote(f, sid, e, i, t):
    """金句/引用：行内大引号 + 重磅大字 + 署名（风格主色装饰）。"""
    eid = f"{sid}-quote{i}"
    x, y = e['x'], e['y']
    w = e.get('w', 1240)
    fs = e.get('fs', 64)
    text = str(e.get('text', '')).strip()
    name = str(e.get('name', '')).strip()
    c = _on_paper(col(e.get('color'), getattr(sp, 'PRIMARY', getattr(sp, 'BIG', sp.INK))))
    body = (f'<div data-{eid}-txt style="font-size:{fs}px;'
            f'font-weight:800;line-height:1.5;color:{th("txt", sp.INK)};white-space:pre-wrap;">'
            f'<span data-{eid}-mark style="font-size:{fs * 1.45:.0f}px;line-height:0;'
            f'font-weight:900;color:{c};vertical-align:-0.12em;margin-right:.08em;">“</span>{text}</div>')
    who = ''
    if name:
        who = (f'<div data-{eid}-who style="margin-top:{fs * 0.4:.0f}px;text-align:right;'
               f'font-size:{max(26, fs * 0.42):.0f}px;font-weight:700;color:{c};">—— {name}</div>')
    html = (f'<div class="{f}-el" id="{eid}" data-hf-name="金句：{text[:10]}" '
            f'style="top:{y}px;left:{x}px;width:{w}px;">{body}{who}</div>')
    js = (f"gsap.set('#{eid} [data-{eid}-mark]',{{opacity:0,scale:2.4,transformOrigin:'0% 60%',display:'inline-block'}});\n      "
          f"tl.to('#{eid} [data-{eid}-mark]',{{opacity:1,scale:1,duration:.5,ease:'power3.out'}},{t:.2f});\n      "
          + sp.fade(f'#{eid} [data-{eid}-who]', t + .7))
    return html, js


def render_checklist(f, sid, e, i, t):
    """要点清单：对勾圆徽 + 粗体条目，逐条落位（卖点/论据/清单）。"""
    eid = f"{sid}-cl{i}"
    x, y = e['x'], e['y']
    gap = e.get('gap', 96)
    nodes = e.get('nodes') or ['要点']
    c = col(e.get('color'), getattr(sp, 'PRIMARY', sp.MINT))
    fs = e.get('fs', 40)
    r = 26
    width = 70 + max((len(str(n)) for n in nodes), default=4) * fs * 1.1
    parts = []
    for k, txt in enumerate(nodes):
        cy = r + k * gap
        cc, tc = cu.pair(c)
        parts.append(f'<div data-{eid}-chk="{k}" style="position:absolute;top:{cy - r}px;left:0;'
                     f'width:{2 * r}px;height:{2 * r}px;border-radius:50%;background:{cc};'
                     f'display:flex;align-items:center;justify-content:center;'
                     f'font-size:{r + 4}px;font-weight:900;color:{tc};">✓</div>')
        parts.append(f'<div data-{eid}-lbl="{k}" style="position:absolute;top:{cy - fs * 0.68}px;'
                     f'left:{2 * r + 28}px;font-size:{fs}px;font-weight:800;'
                     f'color:{th("txt", sp.INK)};white-space:nowrap;">{txt}</div>')
    h = (len(nodes) - 1) * gap + 2 * r
    html = (f'<div class="{f}-el" id="{eid}" data-hf-name="清单：{nodes[0][:8]}" '
            f'style="top:{y}px;left:{x}px;width:{width:.0f}px;height:{h}px;">' + ''.join(parts) + '</div>')
    js = (f"gsap.set('#{eid} [data-{eid}-chk]',{{opacity:0,scale:.2,transformOrigin:'50% 50%'}});\n      "
          f"tl.to('#{eid} [data-{eid}-chk]',{{opacity:1,scale:1,duration:.35,stagger:.3,ease:'back.out(2)'}},{t:.2f});\n      "
          f"gsap.set('#{eid} [data-{eid}-lbl]',{{opacity:0,x:-18}});\n      "
          f"tl.to('#{eid} [data-{eid}-lbl]',{{opacity:1,x:0,duration:.35,stagger:.3,ease:'power2.out'}},{t + .1:.2f});")
    return html, js


def render_stat(f, sid, e, i, t):
    """指标卡：大数字 + 标签（KPI/参数，发布会/财经气质）。"""
    eid = f"{sid}-stat{i}"
    x, y = e['x'], e['y']
    w = e.get('w', 420)
    fs = e.get('fs', 96)
    text = str(e.get('text', '')).strip()
    title = str(e.get('title', '')).strip()
    lab_fs = max(28, int(fs * 0.32))
    accent = col(e.get('bg'), getattr(sp, 'PRIMARY', getattr(sp, 'BIG', sp.INK)))
    num_c = col(e.get('color'), accent)
    surface = th('surface', '#FFFFFF')
    if not (isinstance(surface, str) and surface.startswith('#') and len(surface) == 7):
        surface = th('paper', None)  # 玻璃等 rgba 表面：按纸底（合成后的近似）保障
    if isinstance(surface, str) and surface.startswith('#') and len(surface) == 7:
        num_c = cu.ensure_readable(num_c, surface)  # 彩字自动加深/提亮过门禁
    if getattr(sp, 'THEME', None):
        border = f'border:1px solid {th("line", sp.INK)};'
    elif getattr(sp, 'FLAT', False):
        border = f'border:4px solid {accent};'
    else:
        border = f'border:3px solid {sp.INK};'
    inner = (f'<div data-{eid}-num style="font-size:{fs}px;font-weight:900;line-height:1.12;'
             f'color:{num_c};letter-spacing:1px;">{text}</div>')
    if title:
        inner += (f'<div data-{eid}-lab style="margin-top:{int(fs * 0.06)}px;font-size:{lab_fs}px;'
                  f'font-weight:700;color:{th("surface_txt", sp.INK)};opacity:.85;">{title}</div>')
    html = (f'<div class="{f}-el" id="{eid}" data-hf-name="指标：{text[:10]}" '
            f'style="top:{y}px;left:{x}px;width:{w}px;padding:{int(fs * 0.22)}px {int(fs * 0.3)}px;'
            f'background:{th("surface", "#FFFFFF")};{border}'
            f'border-radius:{th("radius", 16)}px;box-shadow:{th("img_shadow", "0 12px 30px rgba(0,0,0,.14)")};">'
            f'{inner}</div>')
    return html, sp.pop(f'#{eid}', t, scale=.86)


def render_chart_donut(f, sid, e, i, t):
    """环形图：扇环逐段描画 + 右侧图例；中心可放标题/合计。"""
    eid = f"{sid}-donut{i}"
    x, y = e['x'], e['y']
    w, h = e.get('w', 460), e.get('h', 360)
    values = e.get('values') or [1, 1]
    labels = e.get('labels') or [''] * len(values)
    total = sum(values) or 1.0
    title = str(e.get('title', '')).strip()
    dia = min(h, w - (200 if labels else 0))          # 右侧留图例列
    dia = max(dia, 160)
    sw = max(34, int(dia * 0.16))                     # 环厚
    r = (dia - sw) / 2
    cx = cy = dia / 2
    C = 2 * math.pi * r
    parts = []
    acc = 0.0
    for j, v in enumerate(values):
        frac = v / total
        fill = CHART_FILLS[j % len(CHART_FILLS)]
        rot = -90 + 360 * acc
        parts.append(f'<circle data-seg="{j}" cx="{cx:.0f}" cy="{cy:.0f}" r="{r:.0f}" fill="none" '
                     f'stroke="{fill}" stroke-width="{sw}"'
                     f' transform="rotate({rot:.1f} {cx:.0f} {cy:.0f})" '
                     f'stroke-dasharray="0 {C:.1f}"/>')
        acc += frac
    if title:
        parts.append(f'<text x="{cx:.0f}" y="{cy + 12:.0f}" text-anchor="middle" '
                     f'font-size="{dia * 0.13:.0f}" font-weight="800" fill="{th("txt", sp.INK)}" '
                     f'font-family="{sp.FONT}">{title}</text>')
    legend = []
    lx = dia + 36
    for j, v in enumerate(values):
        fill = CHART_FILLS[j % len(CHART_FILLS)]
        legend.append(f'<div data-lg="{j}" style="position:absolute;top:{j * 64 + 8}px;left:{lx}px;'
                      f'display:flex;align-items:center;font-size:30px;font-weight:700;'
                      f'color:{th("txt", sp.INK)};white-space:nowrap;">'
                      f'<span style="width:26px;height:26px;border-radius:8px;background:{fill};'
                      f'display:inline-block;margin-right:14px;"></span>'
                      f'{labels[j] or "项" + str(j + 1)}&nbsp;&nbsp;{_fmt_num(v / total * 100)}%</div>')
    html = (f'<div class="{f}-el" id="{eid}" data-hf-name="环形图" '
            f'style="top:{y}px;left:{x}px;width:{w}px;height:{h}px;">'
            f'<svg width="{dia}" height="{dia}">{"".join(parts)}</svg>' + ''.join(legend) + '</div>')
    # 逐段顺序描画（GSAP attr tween 只吃字面值，最终 dasharray 在 Python 侧算好）
    seg_js = ''
    acc_t = t
    for j, v in enumerate(values):
        final = f'{v / total * C:.1f} {C:.1f}'
        seg_js += (f"tl.to('#{eid} [data-seg=\"{j}\"]',{{attr:{{'stroke-dasharray':'{final}'}},"
                   f"duration:.55,ease:'power2.inOut'}},{acc_t:.2f});\n      ")
        acc_t += 0.28
    js = seg_js + sp.stagger_pop(f'#{eid} [data-lg]', t + .3, step=.2)
    return html, js


def _fmt_num(v):
    return f"{v:.4g}"


def render_chart_bar(f, sid, e, i, t):
    """手绘柱状图：SVG + 微旋转抖动（确定性），柱 stagger 生长。"""
    eid = f"{sid}-cbar{i}"
    x, y, w, h = e['x'], e['y'], e.get('w', 560), e.get('h', 360)
    values = e.get('values') or [1, 1]
    labels = e.get('labels') or [''] * len(values)
    n = len(values)
    vmax = max(values) or 1.0
    bw = min(96, (w - 60) / n - 26)
    gap = (w - 40 - n * bw) / max(n - 1, 1) if n > 1 else 0
    pad_b, label_fs, pad_t = 56, 26, 48   # pad_t：数值标签头部空间（check 门禁测出的越界）
    area_h = h - pad_b - pad_t
    parts = []
    for j, v in enumerate(values):
        bx = 20 + j * (bw + gap)
        bh = max(14, area_h * (v / vmax))
        jitter = ((j % 3) - 1) * 0.8                          # 手绘感抖动收窄，防柱角越顶
        fill = CHART_FILLS[j % len(CHART_FILLS)]
        parts.append(
            f'<g data-bar="{j}" style="transform:rotate({jitter:.1f}deg);transform-origin:{bx + bw/2:.0f}px {h:.0f}px;">'
            f'<rect x="{bx:.0f}" y="{pad_t + area_h - bh:.0f}" width="{bw:.0f}" height="{bh:.0f}" rx="6" '
            f'fill="{fill}" stroke="{th("line", sp.INK)}" stroke-width="4"/></g>')
        if labels[j]:
            parts.append(f'<text x="{bx + bw/2:.0f}" y="{h - 18}" text-anchor="middle" '
                         f'font-size="{label_fs}" fill="{th("txt", sp.INK)}" font-family="{sp.FONT}">{labels[j]}</text>')
        vy = max(pad_t + area_h - bh - 10, label_fs + 4)      # 数值标签夹在 SVG 内
        parts.append(f'<text x="{bx + bw/2:.0f}" y="{vy:.0f}" text-anchor="middle" '
                     f'font-size="{label_fs + 2}" font-weight="700" fill="{th("txt", sp.INK)}" '
                     f'font-family="{sp.FONT}">{_fmt_num(v)}</text>')
    html = (f'<div class="{f}-el" id="{eid}" data-hf-name="柱状图" '
            f'style="top:{y}px;left:{x}px;width:{w}px;height:{h}px;">'
            f'<svg viewBox="0 0 {w} {h}" width="{w}" height="{h}">{"".join(parts)}</svg></div>')
    return html, sp.stagger_grow(f'#{eid} [data-bar]', t, step=.22, origin='center bottom')


def render_chart_line(f, sid, e, i, t):
    """手绘折线图：描边生长（dashoffset）+ 逐点 pop。"""
    eid = f"{sid}-cline{i}"
    x, y, w, h = e['x'], e['y'], e.get('w', 560), e.get('h', 360)
    values = e.get('values') or [1, 2, 1]
    n = len(values)
    vmax, vmin = max(values), min(values)
    span = (vmax - vmin) or 1.0
    pad_b, pad_t = 56, 26
    area_h = h - pad_b - pad_t
    pts = []
    for j, v in enumerate(values):
        px = 28 + (w - 56) * (j / max(n - 1, 1))
        py = pad_t + area_h * (1 - (v - vmin) / span)
        pts.append((px, py))
    poly = ' '.join(f'{px:.0f},{py:.0f}' for px, py in pts)
    dots = ''.join(
        f'<circle data-pt="{j}" cx="{px:.0f}" cy="{py:.0f}" r="13" fill="{sp.CORAL}" stroke="{th("line", sp.INK)}" stroke-width="4"/>'
        for j, (px, py) in enumerate(pts))
    labels = e.get('labels') or []
    lbl = ''.join(
        f'<text x="{px:.0f}" y="{h - 18}" text-anchor="middle" font-size="26" fill="{th("txt", sp.INK)}" '
        f'font-family="{sp.FONT}">{labels[j]}</text>'
        for j, (px, py) in enumerate(pts) if j < len(labels) and labels[j])
    length = (w - 56) * 1.15 + area_h  # 折线长度近似（dash 动画用，宁多勿少）
    html = (f'<div class="{f}-el" id="{eid}" data-hf-name="折线图" '
            f'style="top:{y}px;left:{x}px;width:{w}px;height:{h}px;">'
            f'<svg viewBox="0 0 {w} {h}" width="{w}" height="{h}">'
            f'<polyline data-line points="{poly}" fill="none" stroke="{th("line", sp.INK)}" stroke-width="6" '
            f'stroke-linecap="round" stroke-linejoin="round" stroke-dasharray="{length:.0f}" '
            f'stroke-dashoffset="{length:.0f}"/>{dots}{lbl}</svg></div>')
    js = (f"tl.to('#{eid} [data-line]',{{strokeDashoffset:0,duration:1.1,ease:'power2.out'}},{t:.2f});\n      "
          + sp.stagger_pop(f'#{eid} [data-pt]', t + .5, step=.18))
    return html, js


def render_chart_pie(f, sid, e, i, t):
    """手绘饼图：扇形逐个 pop；标签沿扇形外侧放置。"""
    eid = f"{sid}-cpie{i}"
    x, y, w, h = e['x'], e['y'], e.get('w', 360), e.get('h', 360)
    values = e.get('values') or [1, 1]
    labels = e.get('labels') or [''] * len(values)
    total = sum(values) or 1.0
    r = min(w, h) / 2 - 8
    cx, cy = w / 2, h / 2
    parts, angle = [], -90.0
    for j, v in enumerate(values):
        sweep = 360 * v / total
        a0, a1 = angle, angle + sweep
        large = 1 if sweep > 180 else 0
        x0, y0 = cx + r * math.cos(math.radians(a0)), cy + r * math.sin(math.radians(a0))
        x1, y1 = cx + r * math.cos(math.radians(a1)), cy + r * math.sin(math.radians(a1))
        fill = CHART_FILLS[j % len(CHART_FILLS)]
        parts.append(f'<path data-wedge="{j}" d="M{cx:.0f},{cy:.0f} L{x0:.0f},{y0:.0f} '
                     f'A{r:.0f},{r:.0f} 0 {large} 1 {x1:.0f},{y1:.0f} Z" '
                     f'fill="{fill}" stroke="{th("line", sp.INK)}" stroke-width="4" stroke-linejoin="round"/>')
        mid = math.radians(a0 + sweep / 2)
        lx, ly = cx + (r + 44) * math.cos(mid), cy + (r + 44) * math.sin(mid)
        anchor = 'middle' if abs(math.cos(mid)) < .35 else ('start' if math.cos(mid) > 0 else 'end')
        parts.append(f'<text data-wlbl="{j}" x="{lx:.0f}" y="{ly:.0f}" text-anchor="{anchor}" '
                     f'dominant-baseline="middle" font-size="27" fill="{th("txt", sp.INK)}" '
                     f'font-family="{sp.FONT}">{labels[j]} {_fmt_num(v)}</text>')
        angle = a1
    html = (f'<div class="{f}-el" id="{eid}" data-hf-name="饼图" '
            f'style="top:{y}px;left:{x}px;width:{w}px;height:{h}px;">'
            f'<svg viewBox="0 0 {w} {h}" width="{w}" height="{h}">{"".join(parts)}</svg></div>')
    js = sp.stagger_pop(f'#{eid} [data-wedge]', t, step=.25) + '\n      ' + \
        sp.fade(f'#{eid} [data-wlbl]', t + .5)
    return html, js


# ── custom：自由设计元素（LLM 直出容器内 HTML/CSS/GSAP 的表达力逃生舱）───────

# 与 Go 契约 bannedSnippet 同规则；渲染侧再拦一道（Sanitize 降级路径也带病不出帧）。
_BAN_PATTERNS = [
    r'<script', r'javascript:', r'<iframe', r'<object', r'<embed', r'<link',
    r'\son\w+\s*=', r'position\s*:\s*fixed', r'url\(\s*[\'"]?\s*https?:',
    r'src\s*=\s*[\'"]?\s*https?:', r'animation\s*:', r'transition\s*:',
]
_BAN_JS = [r'repeat\s*:', r'yoyo\s*:', r'\bwindow\b', r'\bdocument\b', r'\bfetch\s*\(',
           r'\beval\s*\(', r'\bFunction\s*\(', r'\bDate\b', r'\bMath\.random\b',
           r'setTimeout', r'setInterval', r'requestAnimationFrame', r'\blocalStorage\b',
           r'ID\s*\.']  # ID 是选择器字符串非元素：ID.querySelector 会炸整段 timeline


def _banned(s, patterns=_BAN_PATTERNS):
    low = s.lower()
    for pat in patterns:
        if re.search(pat, low):
            return pat
    return None


def scope_css(css, eid):
    """CSS 自动限定在容器内：规则选择器前缀 #eid；@keyframes 改名防跨元素/跨段撞名；
    @media 递归。坏规则浏览器直接丢弃（最坏无效果，不会炸渲染）。"""
    css = re.sub(r'/\*.*?\*/', '', css, flags=re.S)
    kf_names = re.findall(r'@keyframes\s+([\w-]+)', css)

    def rename(body):
        for name in kf_names:
            body = re.sub(r'(animation(?:-name)?\s*:[^;}]*)\b' + re.escape(name) + r'\b',
                          r'\g<1>kf-' + eid + '-' + name, body)
        return body

    out, i, n = [], 0, len(css)
    while i < n:
        b = css.find('{', i)
        if b < 0:
            break
        sel = css[i:b].strip()
        depth, j = 1, b + 1
        while j < n and depth:
            if css[j] == '{':
                depth += 1
            elif css[j] == '}':
                depth -= 1
            j += 1
        body = css[b + 1:j - 1] if j <= n else css[b + 1:]
        if sel.startswith('@keyframes'):
            name = sel.split()[-1] if sel.split() else 'k'
            out.append(f'@keyframes kf-{eid}-{name}{{{rename(body)}}}')
        elif sel.startswith('@media'):
            out.append(f'{sel}{{{scope_css(body, eid)}}}')
        elif sel:
            parts = ', '.join(f'#{eid} {p.strip()}' for p in sel.split(',') if p.strip())
            out.append(f'{parts}{{{rename(body)}}}')
        i = j
    return '\n'.join(out)


def _js_ok(code):
    """GSAP 片段过 node --check（语法坏了会炸整段 timeline 注册）。无 node 环境跳过。"""
    import shutil
    import subprocess
    import tempfile
    node = shutil.which('node')
    if not node:
        return True
    wrapper = ('(function(){var tl=gsap.timeline({paused:true});var T=0;var ID="#x";'
               + code + '\n})();')
    fh = tempfile.NamedTemporaryFile('w', suffix='.js', delete=False, encoding='utf-8')
    try:
        fh.write(wrapper)
        fh.close()
        r = subprocess.run([node, '--check', fh.name], capture_output=True, text=True, timeout=15)
        return r.returncode == 0
    except Exception:
        return True
    finally:
        pathlib.Path(fh.name).unlink(missing_ok=True)


def render_custom(f, sid, e, i, t, W=1920, H=1080):
    """自由设计元素：容器 x/y/w/h（内容裁剪在内），LLM 直出 html（必填）/css（自动
    scope）/js（可选 GSAP 片段：T=揭示秒、ID=容器选择器、tl=时间线，入场自己负责）。
    role=bg = 全幅背景特效层（容器扩到满画幅，坐标忽略）。
    默认无 js 时整容器淡入；js 坏/含禁用模式 → 语法降级或跳过，不炸整段。"""
    eid = f"{sid}-cx{i}"
    full = e.get('role') == 'bg'
    x, y = (0, 0) if full else (e['x'], e['y'])
    w, h = (W, H) if full else (e.get('w', 560), e.get('h', 320))
    html_body = str(e.get('html', '') or '')
    css_raw = str(e.get('css', '') or '')
    js_raw = str(e.get('js', '') or '')
    label = str(e.get('text', '') or '').strip() or '自定义'
    if not html_body.strip():
        raise ValueError(f'custom#{i} 缺 html')
    bad = _banned(html_body + css_raw)
    if not bad and js_raw:
        bad = _banned(js_raw, _BAN_JS)
    if bad:
        raise ValueError(f'custom#{i} 含禁用模式 {bad}')
    style = ''
    if css_raw.strip():
        style = f'<style>{scope_css(css_raw, eid)}</style>'
    js = ''
    if js_raw.strip():
        code = f'var T={t:.2f};var ID="#{eid}";\n{js_raw}'
        if _js_ok(code):
            js = code.replace('\n', '\n      ')
        else:
            print(f'{sid}: custom#{i} js 语法检查未过，降级静态淡入')
    html = (f'<div class="{f}-el" id="{eid}" data-hf-name="自定义：{label[:12]}" '
            f'style="top:{y}px;left:{x}px;width:{w}px;'
            f'height:{h}px;overflow:hidden;">{style}{html_body}</div>')
    return html, (js if js else sp.fade(f'#{eid}', t))


# ── scene：确定性逐帧画布（P1-1；原型闸 demo/scene-prototype 已验证机制）──────

# SCENE_CODE_MAX 与 contract.SceneCodeMax 同值（constants_drift_test 门禁兜住）。
SCENE_CODE_MAX = 8000
# 与 Go sceneCodeBan 同规则：渲染侧再拦一道（Sanitize 降级路径也不带病出帧）。
_BAN_SCENE = [r'\bMath\.random\b', r'\bDate\b', r'requestAnimationFrame', r'setTimeout',
              r'setInterval', r'\bfetch\s*\(', r'XMLHttpRequest', r'WebSocket',
              r'\beval\s*\(', r'\bFunction\s*\(', r'\bwindow\b', r'\bdocument\b',
              r'\blocation\b', r'\bnavigator\b', r'\blocalStorage\b', r'\bpostMessage\b',
              r'\bWorker\b', r'\bimport\b', r'https?:']

# 段时长表（main 按段填；scene tween 的 duration 来源）。
_SCENE_DUR = {}


def _scene_js_ok(code):
    """scene 函数体过 node --check（包装成 __draw 调用环境）。无 node 环境跳过。"""
    import shutil
    import subprocess
    import tempfile
    node = shutil.which('node')
    if not node:
        return True
    wrapper = ('(function(t,ctx,W,H,rng){' + code + '\n})(0,null,0,0,function(){return 0;});')
    fh = tempfile.NamedTemporaryFile('w', suffix='.js', delete=False, encoding='utf-8')
    try:
        fh.write(wrapper)
        fh.close()
        r = subprocess.run([node, '--check', fh.name], capture_output=True, text=True, timeout=15)
        return r.returncode == 0
    except Exception:
        return True
    finally:
        pathlib.Path(fh.name).unlink(missing_ok=True)


def _strip_draw_wrapper(code):
    """LLM 高频错误：把整段 function __draw(t,ctx,W,H,rng){...} 声明写进本该是
    函数体的 code。任意位置的声明都剥壳取体内（平衡花括号）：声明前的语句
    （预计算）保留在前——每帧重算，确定性不受影响。"""
    m = re.search(r'(?:function\s+)?__draw\s*\([^)]*\)\s*\{', code)
    if not m:
        return code
    brace = code.find("{", m.end() - 1)
    if brace < 0:
        return code
    depth, j = 1, brace + 1
    while j < len(code) and depth:
        if code[j] == '{':
            depth += 1
        elif code[j] == '}':
            depth -= 1
        j += 1
    inner = code[brace + 1:j - 1]
    head, tail = code[:m.start()], code[j:].strip()
    return (head + inner + (chr(10) + tail if tail else "")).strip() or code

def render_scene(f, sid, e, i, W=1920, H=1080):
    """程序化画布：全画幅 canvas，LLM 只写 __draw(t,ctx,W,H,rng) 函数体（无状态纯函数，
    同 t 同像素）。驱动 = 占位 tween onUpdate 取 tl.time() 调 __draw——原型闸已验证
    seek 必触发、两渲像素 diff=0。垫底（DOM 序在背景层之后、内容元素之前），
    豁免安全区/重叠/元素数（Go 契约同口径）。入场统一 0.3s 淡入（不可配，防滥用）。"""
    eid = f'{sid}-scene'
    code = str(e.get('code') or '')
    if not code.strip():
        raise ValueError(f'scene#{i} 缺 code')
    code = _strip_draw_wrapper(code)
    if len(code) > SCENE_CODE_MAX:
        raise ValueError(f'scene#{i} code 超长（>{SCENE_CODE_MAX} 字）')
    bad = _banned(code, _BAN_SCENE)
    if bad:
        raise ValueError(f'scene#{i} 含禁用模式 {bad}（确定性硬约束）')
    if not _scene_js_ok(code):
        raise ValueError(f'scene#{i} code 语法未过 node --check')
    seed = int(e.get('seed') or 0) or 42
    dur = _SCENE_DUR.get(sid) or 6.0
    html = (f'<canvas id="{eid}" width="{W}" height="{H}" data-hf-name="程序化画布" '
            f'style="position:absolute;left:0;top:0;width:{W}px;height:{H}px;"></canvas>')
    # 驱动壳是本层可信模板（document 可用），code 是 LLM 产物已过黑名单+语法闸；
    # 用 replace 拼接，不走 f-string——code 自带花括号会被格式化洗坏。
    shell = (
        'var __c=document.getElementById("@EID@"),__x=__c.getContext("2d");\n'
        'function __rng32(a){return function(){a|=0;a=a+0x6D2B79F5|0;var t=Math.imul(a^a>>>15,1|a);'
        't=t+Math.imul(t^t>>>7,61|t)^t;return((t^t>>>14)>>>0)/4294967296;};}\n'
        'window["@DRAW@"]=function(t,ctx,W,H,rng){\n@CODE@\n};\n'
        'gsap.set("#@EID@",{opacity:0});\n'
        'tl.to("#@EID@",{opacity:1,duration:.3,ease:"none"},0);\n'
        'tl.to({},{duration:@DUR@,ease:"none",onUpdate:function(){'
        'var t=tl.time();__x.clearRect(0,0,@W@,@H@);'
        'window["@DRAW@"](t,__x,@W@,@H@,__rng32(@SEED@));}},0);'
    )
    shell = (shell.replace('@EID@', eid).replace('@DRAW@', f'__draw_{eid}')
            .replace('@CODE@', code).replace('@DUR@', f'{dur:.3f}')
            .replace('@W@', str(W)).replace('@H@', str(H)).replace('@SEED@', str(seed)))
    return html, shell


# ── formula：KaTeX 离线公式（P1-2）────────────────────────────────

_KATEX_DIR = AI_DIR / 'assets' / 'katex'


def katex_ready():
    """KaTeX 离线资源就绪（ai/assets/katex；assemble 负责拷进项目 assets/）。"""
    return (_KATEX_DIR / 'katex.min.js').exists() and (_KATEX_DIR / 'katex.min.css').exists()


def render_formula(f, sid, e, i, t, W=1920, H=1080):
    """数学公式：KaTeX 同步排版（无异步无随机，seek 确定性）。katex.render 由
    主页面 <script> 供给（assemble 注入，本地资源无外链）。"""
    if not katex_ready():
        raise ValueError('formula 需要 KaTeX 资源（ai/assets/katex 缺失）')
    eid = f'{sid}-formula{i}'
    tex = str(e.get('tex') or '')
    if not tex.strip():
        raise ValueError(f'formula#{i} 缺 tex')
    fs = e.get('fs', 48)
    color = _on_paper(col(e.get('color'), th('txt', sp.INK)))
    w = e.get('w', 0)
    wstyle = f'max-width:{w}px;' if w else ''
    html = (f'<div class="{f}-el" id="{eid}" data-hf-name="公式" '
            f'data-layout-allow-overlap data-layout-allow-occlusion '
            f'style="top:{e["y"]}px;left:{e["x"]}px;font-size:{fs}px;color:{color};{wstyle}"></div>')
    # KaTeX 同步渲染（throwOnError:false 坏公式落红字，check 阶段可见可修）
    js = ('katex.render(' + json.dumps(tex, ensure_ascii=False) + ','
          f'document.getElementById("{eid}"),{{throwOnError:false,displayMode:false}});\n      '
          + sp.fade(f'#{eid}', t))
    return html, js


def main(proj_dir: str) -> int:
    proj = pathlib.Path(proj_dir)
    sb = json.load(open(proj / 'storyboards' / 'storyboard.json', encoding='utf-8'))
    init_engine(proj)  # 按项目风格方向选引擎（手绘/扁平）
    meta = sp.load(proj)
    # 段时长表（scene tween duration 来源）
    _SCENE_DUR.clear()
    _SCENE_DUR.update(meta.get('scene') or {})
    # 画幅随项目（project.json aspect，缺省 16:9 与既有项目一致）
    W, H = (1080, 1920) if _aspect(proj) == '9:16' else (1920, 1080)
    for seg in sb['segments']:
        sid = seg['id']
        spec_path = proj / 'llm' / f'comp-{sid}.spec.json'
        if not spec_path.exists():
            print(f'{sid}: 缺 spec，跳过')
            return 1
        spec = json.load(open(spec_path, encoding='utf-8'))
        resolve_lib_images(proj, spec)  # lib:<素材id> → 本地相对路径（幂等）
        words = meta['words'][sid]
        els = spec.get('elements', [])
        # 层序：role=bg 背景（0）→ scene 画布（1）→ 内容元素（2）。DOM 序即 z 序，
        # LLM 给的相对顺序在各层内部保持不变。
        els = sorted(els, key=lambda e: 0 if e.get('role') == 'bg' else (1 if e.get('kind') == 'scene' else 2))
        htmls, jss = [], []
        for i, e in enumerate(els):
            if not e.get('kind'):
                print(f"{sid}: 元素{i + 1} 缺 kind，跳过")
                continue
            try:
                h, j = render_element(PREFIX, sid, e, i + 1, words, W, H)
            except ValueError as ex:  # 未知 kind / scene·formula 违规：跳过不炸整段
                print(f'{sid}: {ex}，跳过')
                continue
            htmls.append(h)
            jss.append(j)
        body_html = '\n    '.join(htmls)
        body_js = '\n      '.join(jss)
        # 镜头：camera_spec 关键帧（P1-4）优先于 5 词表；都只动内容层（字幕带不动）。
        cs = spec.get('camera_spec') or None
        cam = str((spec.get('camera') or '')).strip()
        if cs and isinstance(cs, dict) and (cs.get('from') or cs.get('to')):
            body_html = f'<div id="{sid}-cam" data-layout-allow-overflow style="position:absolute;inset:0;">{body_html}</div>'
            body_js = f'{body_js}\n      {camera_spec_js(sid, cs, meta["scene"][sid])}'
        elif cam in ANIM_CAMERA:  # 段级镜头缓推：内容包一层 cam（字幕带在 cam 外，保持不动）
            # cam 容器缩放缓推是设计行为，溢出 root 属预期（checker 豁免标记）
            body_html = f'<div id="{sid}-cam" data-layout-allow-overflow style="position:absolute;inset:0;">{body_html}</div>'
            body_js = f'{body_js}\n      {ANIM_CAMERA[cam](f"#{sid}-cam", meta["scene"][sid])}'
        full = sp.wrap(proj, PREFIX, sid, body_html, body_js, W, H)
        payload = {'html': full, 'elements': extract_elements(full)}
        out = proj / 'llm' / f'comp-{sid}.json'
        json.dump(payload, open(out, 'w', encoding='utf-8'), ensure_ascii=False, indent=1)
        print(f'{sid}: {len(htmls)} 元素 ✓')
    return 0


def _aspect(proj) -> str:
    try:
        return json.load(open(proj / 'project.json', encoding='utf-8')).get('aspect', '16:9')
    except Exception:
        return '16:9'


if __name__ == '__main__':
    sys.exit(main(sys.argv[1]))
