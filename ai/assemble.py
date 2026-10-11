# -*- coding: utf-8 -*-
"""组装 hyperframes 工程：index.html + assets/gsap.min.js + package.json + meta.json；
段帧 compositions/frames/*.html 与字幕子组合 compositions/caps/*.html。
用法：python ai/assemble.py <项目目录>
"""
import hashlib
import json
import pathlib
import re
import shutil
import sys

# gsap 资产固定来源：ai/assets（随仓库分发）。
AI_DIR = pathlib.Path(__file__).resolve().parent
GSAP_SRC = AI_DIR / 'assets' / 'gsap.min.js'


def load_overlay(proj):
    """读分镜 overlay 配置：返回 (ovl|None, chapters, style_id)。
    style_id 无论 overlay 是否启用都返回（风格 token 查询：overlay 配色/转场模式）。"""
    try:
        sb = json.load(open(proj / 'storyboards' / 'storyboard.json', encoding='utf-8'))
    except Exception:
        return None, {}, ''
    chapters = {s.get('id'): (s.get('chapter') or '') for s in sb.get('segments', [])}
    ovl = sb.get('overlay')
    if not isinstance(ovl, dict) or not ovl.get('enabled'):
        return None, chapters, sb.get('style_id') or ''
    return ovl, chapters, sb.get('style_id') or ''


def load_style_tokens(style_id):
    """按 style_id 查 styles.json 的 tokens（overlay 取 ink/primary/font；查不到给中性默认）。"""
    if not style_id:
        return {}
    path = AI_DIR.parent / 'data' / 'library' / 'styles.json'
    try:
        for e in json.load(open(path, encoding='utf-8')):
            if str(e.get('id', '')).lower() == str(style_id).lower():
                return e.get('tokens') or {}
    except Exception:
        pass
    return {}


# OS 自带字体（无字体文件可打包）：hyperframes lint 要求引用即声明——
# src: local(...) 的 @font-face 声明本身即满足检查（checker Fix 指引原文）。
# 与 server/internal/pipeline/fontface.go 的 osFontWhitelist 是同一份集合的双端
# 实现（中文字体含中文别名，西文照收——本地缺字体时声明兜底 lint，不影响回退栈），
# constants_drift_test.go 强制两边一致，改这里必须同步那边。
_OS_CJK_FONTS = {
    'microsoft yahei', '微软雅黑', 'simhei', '黑体', 'simsun', '宋体',
    'nsimsun', '新宋体', 'kaiti', '楷体', 'stkaiti', 'stsong', 'stzhongsong',
    'fangsong', '仿宋', 'noto serif sc', 'noto sans sc',
    'consolas', 'segoe ui', 'georgia', 'cambria', 'arial narrow', 'arial',
    'courier new', 'times new roman', 'impact',
}


def fontface_css(*font_stacks):
    """扫描 font-family 栈，为其中 OS 自带中文字体生成 @font-face local 声明。
    只处理已知集合，逐别名各出一条声明（CSS 里用了哪个写法就声明哪个写法）。"""
    found = {}  # lower → 原写法（声明与 CSS 引用逐字一致，防 lint 大小写敏感）
    for stack in font_stacks:
        if not stack:
            continue
        for tok in re.findall(r"'([^']+)'|\"([^\"]+)\"|([A-Za-z\u4e00-\u9fff][\w \u4e00-\u9fff]*?)\s*(?:,|$)",
                              str(stack)):
            name = next((t for t in tok if t), '').strip()
            if name.lower() in _OS_CJK_FONTS:
                found.setdefault(name.lower(), name)
    out = [f"@font-face {{ font-family: '{n}'; src: local('{n}'); }}" for n in found.values()]
    return '\n      '.join(out)


def load_skin_slots(proj):
    """读项目皮肤槽位（style/skin.json；字幕带配色/字体用它，与画面同源）。"""
    try:
        sk = json.load(open(proj / 'style' / 'skin.json', encoding='utf-8'))
        return sk.get('slots') or {}
    except Exception:
        return {}


def fontasset_css(proj, *font_stacks):
    """目录字体解析产物（style/font_assets.json——ai/font_tool.py 下载+按本文稿
    子集好的真字体 woff2 data URL）：栈里引用到就补 url() 声明。质检要声明，
    渲染要真字体，这里两个一起给；没有产物文件就静默空串（font_tool 失败时
    家族已被剥除，不会走到这）。"""
    try:
        fa = json.loads((proj / 'style' / 'font_assets.json').read_text(encoding='utf-8'))
    except Exception:
        return ''
    resolved = fa.get('resolved') or {}
    low = str(' '.join(s for s in font_stacks if s) or '').lower()
    out = []
    for fam, url in resolved.items():
        if url and fam and fam.lower() in low:
            out.append("@font-face { font-family: '%s'; src: url('%s') format('woff2'); }" % (fam, url))
    return '\n    '.join(out)


def _hex_rgb(c):
    m = re.match(r'#([0-9a-fA-F]{6})$', str(c or '').strip())
    if not m:
        return None
    v = m.group(1)
    return tuple(int(v[i:i + 2], 16) for i in (0, 2, 4))


def _rel_luma(rgb):
    def ch(u):
        u = u / 255
        return u / 12.92 if u <= 0.04045 else ((u + 0.055) / 1.055) ** 2.4
    r, g, b = rgb
    return 0.2126 * ch(r) + 0.7152 * ch(g) + 0.0722 * ch(b)


def _contrast(a, b):
    la, lb = _rel_luma(a), _rel_luma(b)
    hi, lo = max(la, lb), min(la, lb)
    return (hi + 0.05) / (lo + 0.05)


def subtitle_text_color(slots):
    """字幕字色守卫：字幕条固定深底（rgba(10,10,14,.62) 压在皮肤底色上），而皮肤
    txt 槽位是为浅底面板设计的——深底直接用会暗对暗（实测暖砂皮肤 #321F16 只有
    1.8:1）。对有效条底色算对比度，不达 4.5:1 回落浅色。"""
    bg = _hex_rgb(slots.get('bg') or '') or (255, 255, 255)
    bar = tuple(round(0.38 * bg[i] + 0.62 * v) for i, v in enumerate((10, 10, 14)))
    txt = _hex_rgb(slots.get('txt') or '')
    if txt is not None and _contrast(txt, bar) >= 4.5:
        return slots['txt']
    return '#F2F2EA'


def subtitle_blocks(scenes, meta_voices, slots, proj, w=1080, h=1920):
    """平台字幕带（v2：字幕=TTS 时间戳机械装配，模型永不写 cue）。
    每段字幕落成子组合文件 compositions/caps/cap-{sid}.html（样式+词级点亮 JS，
    局部时间轴用段内相对时刻），index/单段工程只留挂载元素——lint 三连同源根治
    （主文件过大 / 时间轴轨道密度 / timeline 元素禁嵌套 div，挂载与子组合全部豁免），
    段级重渲缓存经挂载上的 data-cap-v 指纹感知字幕内容变化。
    返回 (挂载 html 片段, '')，与 overlay_block 同一注入位。"""
    per = {v['id']: v for v in meta_voices}
    txt = subtitle_text_color(slots)
    font = slots.get('font') or "'Segoe UI','Microsoft YaHei',sans-serif"
    fa_css = fontasset_css(proj, font)
    capdir = proj / 'compositions' / 'caps'
    capdir.mkdir(parents=True, exist_ok=True)
    html_parts = []
    for sid, start, dur, adur in scenes:
        v = per.get(sid)
        if not v or not v.get('phrases'):
            continue
        words, phrases = v['words'], v['phrases']
        en = v.get('phrases_en') or []
        cap_id = f'cap-{sid}'
        ph_html = []
        for pi, rng in enumerate(phrases):
            i0, i1 = int(rng[0]), int(rng[1])
            enline = ''
            if pi < len(en) and en[pi]:
                enline = f'<div class="cap-en" data-layout-allow-overlap data-layout-allow-occlusion>{_esc_html(en[pi])}</div>'
            spans = ''.join(f'<span class="cap-w" data-layout-allow-overlap data-layout-allow-occlusion data-sw="{i}">{_esc_html(words[i]["text"])}</span>'
                            for i in range(i0, i1 + 1))
            ph_html.append(f'<div class="cap-ph" data-layout-allow-overlap data-layout-allow-occlusion data-sph="{pi}"><div class="cap-line">{spans}{enline}</div></div>')
        # 局部时间轴：0 = 段首（挂载点 data-start 对齐），词级时刻本就是段内相对值
        js = [f"gsap.set('#{cap_id} .cap-ph',{{opacity:0}});"]
        for pi, rng in enumerate(phrases):
            i0, i1 = int(rng[0]), int(rng[1])
            a = words[i0]['start']
            b = words[i1 + 1]['start'] if i1 + 1 < len(words) else words[i1]['end']
            js.append(f"tl.set('#{cap_id} [data-sph=\"{pi}\"]',{{opacity:1}},{a:.3f});")
            if b > a + 0.05:
                js.append(f"tl.set('#{cap_id} [data-sph=\"{pi}\"]',{{opacity:0}},{b:.3f});")
            for i in range(i0, i1 + 1):
                js.append(f"tl.to('#{cap_id} [data-sw=\"{i}\"]',{{opacity:1,duration:.1,ease:'none'}},{words[i]['start']:.3f});")
        js.append(f"tl.to({{}},{{duration:{dur:.3f}}},0);")  # 占位撑满段长，越尾寻址不塌
        body = '\n'.join(js)
        cap_html = f"""<template>
  <style>
    {fontface_css(font)}
    {fa_css}
    #{cap_id} {{ position: absolute; inset: 0; pointer-events: none; z-index: 55; }}
    #{cap_id} .cap-zone {{ position: absolute; left: 0; right: 0; bottom: 0; height: 17%; display: flex; align-items: center; justify-content: center; }}
    #{cap_id} .cap-ph {{ position: absolute; inset: 0; display: flex; align-items: center; justify-content: center; opacity: 0; max-width: 90%; margin: 0 auto; }}
    #{cap_id} .cap-line {{ background: rgba(10,10,14,.62); border-radius: 14px; padding: 12px 40px; color: {txt}; font-family: {font}; font-size: 34px; line-height: 1.4; font-weight: 600; text-align: center; text-shadow: 0 1px 8px rgba(0,0,0,.5); }}
    #{cap_id} .cap-w {{ opacity: .3; margin: 0 2px; }}
    #{cap_id} .cap-en {{ font-size: .56em; font-style: italic; opacity: .72; font-family: Georgia, "Times New Roman", serif; letter-spacing: .04em; margin-top: .12em; }}
  </style>
  <div data-composition-id="{cap_id}" data-width="{w}" data-height="{h}">
    <div id="{cap_id}">
      <div class="cap-zone"><div class="cap-body">{''.join(ph_html)}</div></div>
    </div>
  </div>
  <script>
    (function(){{
      var tl=gsap.timeline({{paused:true}});
      {body}
      window.__timelines["{cap_id}"]=tl;
    }})();
  </script>
</template>"""
        fp = hashlib.md5(cap_html.encode('utf-8')).hexdigest()[:8]
        out = capdir / f'{cap_id}.html'
        if not out.exists() or out.read_text(encoding='utf-8') != cap_html:
            out.write_text(cap_html, encoding='utf-8')
        html_parts.append(
            f'<div id="{cap_id}" class="scene clip" data-composition-id="{cap_id}" '
            f'data-composition-src="compositions/caps/{cap_id}.html" '
            f'data-start="{start}" data-duration="{dur}" data-track-index="8" '
            f'data-hf-name="字幕带" data-cap-v="{fp}" style="z-index:55;"></div>')
    return '\n'.join(html_parts), ''


def _esc_html(s):
    return (str(s).replace('&', '&amp;').replace('<', '&lt;').replace('>', '&gt;'))


def main(proj_dir: str) -> int:
    proj = pathlib.Path(proj_dir)
    meta = json.load(open(proj / 'audio_meta.json', encoding='utf-8'))
    pj = json.load(open(proj / 'project.json', encoding='utf-8'))
    name = pj.get('name', proj.name)
    # 画幅随项目（缺省 16:9 与既有项目一致）
    W, H = (1080, 1920) if pj.get('aspect') == '9:16' else (1920, 1080)

    durs = {v['id']: v['duration_s'] for v in meta['voices']}
    scene = {k: round(v + 0.35, 3) for k, v in durs.items()}
    ids = sorted(scene.keys())
    total = round(sum(scene.values()), 3)
    starts, acc = {}, 0.0
    for sid in ids:
        starts[sid] = round(acc, 3)
        acc += scene[sid]

    # 跨段统一角标（P0-2）：配置在分镜层，注入每段页面（单段重渲各自带全）。
    ovl, chapters, style_id = load_overlay(proj)
    tokens = load_style_tokens(style_id)
    # lint 合规：为 token/角标字体栈中的 OS 自带字体生成 @font-face local 声明；
    # 目录字体（font_assets.json 解析产物）补真字体 url 声明
    slots = load_skin_slots(proj)
    fontface = fontface_css(tokens.get('font') or '',
                            "Georgia, 'Times New Roman', 'SimSun', serif",
                            slots.get('font') or '')
    fontface += '\n' + fontasset_css(proj, tokens.get('font') or '', slots.get('font') or '')
    # 章节刻度位置 = 章节切换段的全局起点（相邻同 chapter 合并）
    bounds = [0.0]
    for i, sid in enumerate(ids):
        if i > 0 and chapters.get(sid, '') != chapters.get(ids[i - 1], ''):
            bounds.append(starts[sid])

    def overlay_block(chapter, dur, t0):
        """三锚点 HUD（左上章节/右上计数器/底部进度线）。t0 = 本文件 0 秒对应的
        全片时刻（index=0；单段=该段全局起点）。计数器与进度线取 [t0,t0+dur]
        时间片的起止值——t 的线性函数，seek 确定性不受影响。"""
        if not ovl:
            return '', ''
        s = H / 1080.0
        ink = tokens.get('ink') or '#EAF0FF'
        accent = tokens.get('primary') or '#D4AF37'
        font = tokens.get('font') or "'Segoe UI','Microsoft YaHei',sans-serif"
        zh, en = chapter, ''
        if '|' in chapter:
            parts = [x.strip() for x in chapter.split('|', 1)]
            zh, en = parts[0], parts[1] if len(parts) > 1 else ''
        ch_html = ''
        if zh or en:
            ch_html = (f'<div class="ovl-ch">{zh or ""}'
                       + (f'<span class="ovl-ch-en">{en}</span>' if en else '') + '</div>')
        cnt_html, cnt_js = '', ''
        if cc := ovl.get('chapter_counter'):
            dec = cc.get('decimals', 2) or 0
            lo, hi = cc.get('from', 0), cc.get('to', 0)  # 配置缺键兜 0，别 KeyError 炸组装
            v0 = lo + (hi - lo) * (t0 / total if total else 0)
            v1 = lo + (hi - lo) * (min(t0 + dur, total) / total if total else 0)
            cnt_html = (f'<div class="ovl-cnt" id="ovl-cnt">{cc.get("prefix","")}'
                        f'{v0:.{dec}f}{cc.get("unit","")}</div>')
            cnt_js = (
                'var ovlC=document.getElementById("ovl-cnt");if(ovlC){var cv={v:'
                f'{v0!r}' + '};tl.to(cv,{v:' + f'{v1!r}' + ',duration:' + f'{dur!r}'
                + ',ease:"none",onUpdate:function(){ovlC.textContent='
                + json.dumps(cc.get('prefix', ''), ensure_ascii=False)
                + '+cv.v.toFixed(' + str(dec) + ')+'
                + json.dumps(cc.get('unit', ''), ensure_ascii=False) + ';}},0);}')
        p0 = t0 / total * 100 if total else 0
        p1 = min(t0 + dur, total) / total * 100 if total else 0
        ticks = ''.join(f'<div class="ovl-tick" style="left:{(b - t0) / dur * 100:.2f}%"></div>'
                        for b in bounds if t0 < b < t0 + dur)
        html = f'''<div id="ovl" class="clip" data-start="0" data-duration="{dur:.3f}" data-track-index="9" data-hf-name="全局overlay">
  <style>
    #ovl {{ position: absolute; inset: 0; pointer-events: none; z-index: 60; font-family: {font}; }}
    #ovl .ovl-ch {{ position: absolute; left: 2.8%; top: 3.6%; max-width: 46%; color: {ink};
      font-family: Georgia, 'Times New Roman', 'SimSun', serif; font-size: {28 * s:.0f}px;
      letter-spacing: .08em; opacity: .94; text-shadow: 0 1px 10px rgba(0,0,0,.45); }}
    #ovl .ovl-ch-en {{ display: block; font-size: {15 * s:.0f}px; font-style: italic;
      letter-spacing: .22em; text-transform: uppercase; opacity: .7; margin-top: {6 * s:.0f}px; }}
    #ovl .ovl-cnt {{ position: absolute; right: 2.8%; top: 3.8%; color: {accent};
      font-size: {23 * s:.0f}px; font-variant-numeric: tabular-nums; letter-spacing: .04em;
      opacity: .95; text-shadow: 0 1px 8px rgba(0,0,0,.45); }}
    #ovl .ovl-bar {{ position: absolute; left: 0; right: 0; bottom: 0; height: {max(3, round(4 * s))}px;
      background: rgba(255,255,255,.14); }}
    #ovl .ovl-fill {{ position: absolute; left: 0; top: 0; bottom: 0; width: {p0:.2f}%;
      background: {accent}; opacity: .85; }}
    #ovl .ovl-tick {{ position: absolute; top: -{max(2, round(3 * s))}px; width: {max(1, round(2 * s))}px;
      height: {max(5, round(9 * s))}px; background: rgba(255,255,255,.5); }}
  </style>
  {ch_html}{cnt_html}
  <div class="ovl-bar"><div class="ovl-fill" id="ovl-fill"></div>{ticks}</div>
</div>'''
        js = cnt_js + (f"tl.fromTo('#ovl-fill',{{width:'{p0:.2f}%'}},"
                       f"{{width:'{p1:.2f}%',duration:{dur!r},ease:'none'}},0);")
        return html, js

    def scenes_html(scenes, track_from=0):
        out = []
        for i, (sid, start, dur, adur) in enumerate(scenes):
            out.append(
                f'      <div id="el-{sid}" class="scene" data-composition-id="{sid}"\n'
                f'        data-composition-src="compositions/frames/{sid}.html"\n'
                f'        data-start="{start}" data-duration="{dur}" data-track-index="{track_from + i}"></div>\n'
                f'      <audio id="el-{sid}-voice" src="audio/{sid}.wav"\n'
                f'        data-start="{start}" data-duration="{adur}" data-track-index="10" data-volume="1"></audio>')
        return chr(10).join(out)

    def project_html(w, h, total_dur, body, ovl_body='', ovl_js='', cap_body='', cap_js='', xfade=True):
        # KaTeX 本地供给（formula 元素用；外链被禁，必须离线打包）
        katex_head = ''
        if (AI_DIR / 'assets' / 'katex' / 'katex.min.js').exists():
            katex_head = ('    <link rel="stylesheet" href="assets/katex/katex.min.css" />\n'
                          '    <script src="assets/katex/katex.min.js"></script>\n')
        # 转场 v1：段首/段尾黑场微叠化（0.25s，吃 SegTail 余量；cut = 硬切老行为）
        xf_body = ('      <div id="xfade" style="position:absolute;inset:0;background:#000;'
                   'z-index:100;pointer-events:none;opacity:1;"></div>\n') if xfade else ''
        xf_js = ''
        if xfade:
            xf_js = (f"tl.fromTo('#xfade',{{opacity:1}},{{opacity:0,duration:.25,ease:'none'}},0);"
                     f"tl.to('#xfade',{{opacity:1,duration:.25,ease:'none'}},{max(0, total_dur - 0.25):.3f});")
        return f"""<!doctype html>
<html lang="zh-CN">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width={w}, height={h}" />
    <script src="assets/gsap.min.js"></script>
    <script src="assets/hf.js"></script>
{katex_head}    <style>
      * {{ margin: 0; padding: 0; box-sizing: border-box; }}
      html, body {{ width: {w}px; height: {h}px; overflow: hidden; background: #000; }}
      #root {{ position: relative; width: {w}px; height: {h}px; overflow: hidden; background: #FDF6E3; }}
      .scene {{ position: absolute; inset: 0; width: 100%; height: 100%; }}
      {fontface}
    </style>
  </head>
  <body>
    <div id="root" data-composition-id="main" data-start="0" data-duration="{total_dur}" data-width="{w}" data-height="{h}">
{body}
{ovl_body}
{cap_body}
{xf_body}    </div>
    <script>
      window.__timelines = window.__timelines || {{}};
      window.__timelines["main"] = gsap.timeline({{ paused: true }});
      // 段间转场：黑场叠化（xf_body）+ Go 侧 xfade 交叉叠化（timeline.json）
      (function () {{ var tl = window.__timelines["main"];
        tl.to({{}}, {{ duration: {total_dur} }}, 0);
        {ovl_js}
        {cap_js}
        {xf_js}
      }})();
    </script>
  </body>
</html>
"""

    first_chapter = next((c for c in map(chapters.get, ids) if c), '')
    # 转场模式：styles.json tokens.transition（cut|fade|xfade，缺省 fade）。
    # fade=v1 段首尾页内黑场微叠化；xfade=v1b 真交叉叠化在 Go concat 侧做
    # （xfadeConcat），页内不能再叠黑场（双重变暗），故页内层只在 fade 时注入。
    trans = str(tokens.get('transition') or 'fade').strip().lower()
    xfade = trans == 'fade'
    cap_body, cap_js = subtitle_blocks(
        [(sid, starts[sid], scene[sid], durs[sid]) for sid in ids], meta['voices'], slots, proj, W, H)
    (proj / 'index.html').write_text(
        project_html(W, H, total,
                     scenes_html([(sid, starts[sid], scene[sid], durs[sid]) for sid in ids]),
                     *overlay_block(first_chapter, total, 0.0), cap_body, cap_js, xfade=xfade),
        encoding='utf-8')

    # 单段工程（段级局部重渲）：.hf-seg/segNN.html = 只含该段的 index，
    # hyperframes render -c 出带音轨段片，段间 concat 出成片（见 pipeline stageRender）。
    segdir = proj / '.hf-seg'
    segdir.mkdir(exist_ok=True)
    for f in segdir.glob('*.html'):
        if f.stem not in scene:
            f.unlink()
    for sid in ids:
        seg_html = project_html(W, H, scene[sid],
                                scenes_html([(sid, 0, scene[sid], durs[sid])]),
                                *overlay_block(chapters.get(sid, ''), scene[sid], starts[sid]),
                                *subtitle_blocks([(sid, 0, scene[sid], durs[sid])], meta['voices'], slots, proj, W, H),
                                xfade=xfade)
        out = segdir / f'{sid}.html'
        # 内容没变就保住 mtime：段级重渲缓存按"段片旧于段工程"判旧（pipeline segStale），
        # 修复轮回滚会把全部段源原样重写一遍，无脑覆盖 mtime 会把缓存打穿成全片重渲
        if out.exists() and out.read_text(encoding='utf-8') == seg_html:
            continue
        out.write_text(seg_html, encoding='utf-8')

    # 字幕子组合清仓：段没了/段没声了，旧 cap 文件不留（挂载 404 = 渲染必炸）
    capdir = proj / 'compositions' / 'caps'
    if capdir.exists():
        voices = {v['id']: v for v in meta['voices']}
        want = {f'cap-{sid}' for sid in ids if voices.get(sid, {}).get('phrases')}
        for f in capdir.glob('cap-*.html'):
            if f.stem not in want:
                f.unlink()

    (proj / 'assets').mkdir(exist_ok=True)
    if not (proj / 'assets' / 'gsap.min.js').exists():
        shutil.copy(GSAP_SRC, proj / 'assets' / 'gsap.min.js')
    # hf.js 守卫层随仓库升级：无条件覆盖（gsap 是三方固定版本，策略不同）
    HF_SRC = AI_DIR / 'assets' / 'hf.js'
    if HF_SRC.exists():
        shutil.copyfile(HF_SRC, proj / 'assets' / 'hf.js')
    katex_src = AI_DIR / 'assets' / 'katex'
    if katex_src.exists() and not (proj / 'assets' / 'katex').exists():
        shutil.copytree(katex_src, proj / 'assets' / 'katex')
    if not (proj / 'package.json').exists():
        (proj / 'package.json').write_text(json.dumps({
            'name': proj.name.replace(' ', '-'), 'private': True, 'type': 'module',
        }, ensure_ascii=False, indent=2), encoding='utf-8')
    if not (proj / 'meta.json').exists():
        (proj / 'meta.json').write_text(json.dumps(
            {'id': proj.name, 'name': name}, ensure_ascii=False), encoding='utf-8')
    print(f'index.html ✓ 总时长 {total}s，{len(ids)} 段（含 .hf-seg 单段工程）')
    return 0


if __name__ == '__main__':
    sys.exit(main(sys.argv[1]))
