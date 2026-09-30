# -*- coding: utf-8 -*-
"""组装 hyperframes 工程：index.html + assets/gsap.min.js + package.json + meta.json。
用法：python ai/assemble.py <项目目录>
"""
import json
import pathlib
import re
import shutil
import subprocess
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
    path = AI_DIR.parent / 'data' / 'library' / 'registry' / 'styles.json'
    try:
        for e in json.load(open(path, encoding='utf-8')):
            if str(e.get('id', '')).lower() == str(style_id).lower():
                return e.get('tokens') or {}
    except Exception:
        pass
    return {}


# OS 自带中文字体（无字体文件可打包）：hyperframes lint 要求引用即声明——
# src: local(...) 的 @font-face 声明本身即满足检查（checker Fix 指引原文）。
_OS_CJK_FONTS = {
    'simsun', '宋体', 'nsimsun', '新宋体', 'simhei', '黑体', 'kaiti', '楷体',
    'stkaiti', 'stsong', 'stzhongsong', 'fangsong', '仿宋', 'microsoft yahei', '微软雅黑',
    'noto serif sc', 'noto sans sc',
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


def subtitle_blocks(scenes, meta_voices, slots, base=0.0):
    """平台字幕带（v2：字幕=TTS 时间戳机械装配，模型永不写 cue）。
    每段一个父级 clip（z=55，字幕带占底部 ~17%），短语硬切 + 词级渐亮，
    时间轴挂主 tl（绝对时刻 = 段起点 + 词相对时刻；单段工程传 base 使段内归零）。
    返回 (html 片段, 主 tl 的 JS 语句)，与 overlay_block 同一注入位。"""
    per = {v['id']: v for v in meta_voices}
    txt = slots.get('txt') or '#F2F2EA'
    font = slots.get('font') or "'Segoe UI','Microsoft YaHei',sans-serif"
    html_parts, js_parts = [], []
    for sid, start, dur, adur in scenes:
        v = per.get(sid)
        if not v or not v.get('phrases'):
            continue
        words, phrases = v['words'], v['phrases']
        en = v.get('phrases_en') or []
        t0 = base + start
        ph_html = []
        for pi, rng in enumerate(phrases):
            i0, i1 = int(rng[0]), int(rng[1])
            enline = ''
            if pi < len(en) and en[pi]:
                enline = f'<div class="cap-en" data-layout-allow-overlap data-layout-allow-occlusion>{_esc_html(en[pi])}</div>'
            spans = ''.join(f'<span class="cap-w" data-layout-allow-overlap data-layout-allow-occlusion data-sw="{i}">{_esc_html(words[i]["text"])}</span>'
                            for i in range(i0, i1 + 1))
            ph_html.append(f'<div class="cap-ph" data-layout-allow-overlap data-layout-allow-occlusion data-sph="{pi}"><div class="cap-line">{spans}{enline}</div></div>')
        html_parts.append(
            f'<div id="cap-{sid}" class="clip" data-start="{start}" data-duration="{dur}" '
            f'data-track-index="8" data-hf-name="字幕带">\n'
            f'  <style>\n'
            f'    #cap-{sid} {{ position: absolute; inset: 0; pointer-events: none; z-index: 55; }}\n'
            f'    #cap-{sid} .cap-zone {{ position: absolute; left: 0; right: 0; bottom: 0; height: 17%; '
            f'display: flex; align-items: center; justify-content: center; }}\n'
            f'    #cap-{sid} .cap-ph {{ position: absolute; inset: 0; display: flex; align-items: center; '
            f'justify-content: center; opacity: 0; max-width: 90%; margin: 0 auto; }}\n'
            f'    #cap-{sid} .cap-line {{ background: rgba(10,10,14,.62); border-radius: 14px; '
            f'padding: 12px 40px; color: {txt}; font-family: {font}; font-size: 34px; line-height: 1.4; '
            f'font-weight: 600; text-align: center; text-shadow: 0 1px 8px rgba(0,0,0,.5); }}\n'
            f'    #cap-{sid} .cap-w {{ opacity: .3; margin: 0 2px; }}\n'
            f'    #cap-{sid} .cap-en {{ font-size: .56em; font-style: italic; opacity: .72; '
            f'font-family: Georgia, "Times New Roman", serif; letter-spacing: .04em; margin-top: .12em; }}\n'
            f'  </style>\n'
            f'  <div class="cap-zone"><div class="cap-line"><div class="cap-body">'
            + ''.join(ph_html) + '</div></div></div>\n'
            f'</div>')
        js = [f"gsap.set('#cap-{sid} .cap-ph',{{opacity:0}});"]
        for pi, rng in enumerate(phrases):
            i0, i1 = int(rng[0]), int(rng[1])
            a, b = words[i0]['start'], words[min(i1 + 1, len(words) - 1)]['start'] if i1 + 1 < len(words) else words[i1]['end']
            js.append(f"tl.set('#cap-{sid} [data-sph=\"{pi}\"]',{{opacity:1}},{t0 + a:.3f});")
            if b > a + 0.05:
                js.append(f"tl.set('#cap-{sid} [data-sph=\"{pi}\"]',{{opacity:0}},{t0 + b:.3f});")
            for i in range(i0, i1 + 1):
                js.append(f"tl.to('#cap-{sid} [data-sw=\"{i}\"]',{{opacity:1,duration:.1,ease:'none'}},{t0 + words[i]['start']:.3f});")
        js_parts.append('\n        '.join(js))
    return '\n'.join(html_parts), '\n        '.join(js_parts)


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
    # lint 合规：为 token/角标字体栈中的 OS 自带字体生成 @font-face local 声明
    slots = load_skin_slots(proj)
    fontface = fontface_css(tokens.get('font') or '',
                            "Georgia, 'Times New Roman', 'SimSun', serif",
                            slots.get('font') or '')
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
            v0 = cc['from'] + (cc['to'] - cc['from']) * (t0 / total if total else 0)
            v1 = cc['from'] + (cc['to'] - cc['from']) * (min(t0 + dur, total) / total if total else 0)
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
      // v1 段间硬切（转场为独立验证项）
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
        [(sid, starts[sid], scene[sid], durs[sid]) for sid in ids], meta['voices'], slots)
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
        (segdir / f'{sid}.html').write_text(
            project_html(W, H, scene[sid],
                         scenes_html([(sid, 0, scene[sid], durs[sid])]),
                         *overlay_block(chapters.get(sid, ''), scene[sid], starts[sid]),
                         *subtitle_blocks([(sid, 0, scene[sid], durs[sid])], meta['voices'], slots),
                         xfade=xfade),
            encoding='utf-8')

    (proj / 'assets').mkdir(exist_ok=True)
    if not (proj / 'assets' / 'gsap.min.js').exists():
        shutil.copy(GSAP_SRC, proj / 'assets' / 'gsap.min.js')
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
