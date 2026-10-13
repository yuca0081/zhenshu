# -*- coding: utf-8 -*-
"""迷你真段工程壳（样张升级）：给 style/clips/ 下的场景片段各套一个可渲染的单段工程。

与 assemble.py 同源逻辑（字体声明/字幕带/资产/转场），差异：无音频、无章节 overlay、
字幕带 = 一句真实旁白截句全程显示（3s 小样没有 TTS 时间戳，不做词级点亮）。
清单 style/clips/clips.json（Go 侧 styleclips.go 写）：
    [{"file": "clip-0.html", "narration": "…", "duration": 3.0}, …]

用法：python ai/assemble_samples.py <项目目录>
"""
import json
import pathlib
import shutil
import sys

AI_DIR = pathlib.Path(__file__).resolve().parent
from assemble import GSAP_SRC, _esc_html, ensure_font_faces, fontface_css, load_font_assets, load_style_tokens, subtitle_text_color


def load_slots(proj):
    """皮肤槽位：确认前 skin.json 还没落，直接读 style_samples.json 的主皮肤。"""
    try:
        ss = json.loads((proj / 'style' / 'style_samples.json').read_text(encoding='utf-8'))
    except Exception:
        return {}
    skins = ss.get('skins') or []
    if not skins and ss.get('skin'):
        skins = [ss['skin']]
    return (skins[0].get('slots') if skins else {}) or {}


def transition_mode(proj):
    """转场模式：project.json "transition"（LockDesign 从设计系统转场政策映射写入；
    缺省 cut——段间节奏交给场景编排，与 assemble.py 同口径）。"""
    try:
        pj = json.loads((proj / 'project.json').read_text(encoding='utf-8'))
        return str(pj.get('transition') or 'cut').strip().lower()
    except Exception:
        return 'cut'


def caption_block(clip_id, dur, narration, slots):
    """字幕带：样式与正式段逐字同款（.cap-zone/.cap-line + 槽位配色守卫）。"""
    if not narration:
        return '', ''
    txt = subtitle_text_color(slots)
    font = slots.get('font') or "'Segoe UI','Microsoft YaHei',sans-serif"
    body = f'''<div id="cap-{clip_id}" class="clip" data-start="0" data-duration="{dur}" data-track-index="8" data-hf-name="字幕带（样张小样）">
  <style>
    #cap-{clip_id} {{ position: absolute; inset: 0; pointer-events: none; z-index: 55; }}
    #cap-{clip_id} .cap-zone {{ position: absolute; left: 0; right: 0; bottom: 0; height: 17%;
      display: flex; align-items: center; justify-content: center; }}
    #cap-{clip_id} .cap-line {{ background: rgba(10,10,14,.62); border-radius: 14px;
      padding: 12px 40px; color: {txt}; font-family: {font}; font-size: 34px; line-height: 1.4;
      font-weight: 600; text-align: center; text-shadow: 0 1px 8px rgba(0,0,0,.5); max-width: 90%; }}
  </style>
  <div class="cap-zone"><div class="cap-line" data-layout-allow-overlap data-layout-allow-occlusion>{_esc_html(narration)}</div></div>
</div>'''
    js = f"tl.fromTo('#cap-{clip_id}',{{opacity:0}},{{opacity:1,duration:.25,ease:'power1.out'}},0.1);"
    return body, js


def mini_html(w, h, dur, clip_id, frag_src, cap_body, cap_js, xfade, fontface, bg):
    """单段工程壳：与 assemble.py project_html 同构（gsap/hf.js 资产、黑场微叠化、
    scene div 以 data-composition-src 挂片段）。bg = 皮肤底色槽位——片段 HTML 不铺
    满屏底色，露出的就是这层；硬编码浅色会把深色系样张整体衬成浅色。"""
    xf_body = ('      <div id="xfade" style="position:absolute;inset:0;background:#000;'
               'z-index:100;pointer-events:none;opacity:1;"></div>\n') if xfade else ''
    xf_js = ''
    if xfade:
        xf_js = (f"tl.fromTo('#xfade',{{opacity:1}},{{opacity:0,duration:.25,ease:'none'}},0);"
                 f"tl.to('#xfade',{{opacity:1,duration:.25,ease:'none'}},{max(0, dur - 0.25):.3f});")
    return f"""<!doctype html>
<html lang="zh-CN">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width={w}, height={h}" />
    <script src="assets/gsap.min.js"></script>
    <script src="assets/hf.js"></script>
    <style>
      * {{ margin: 0; padding: 0; box-sizing: border-box; }}
      html, body {{ width: {w}px; height: {h}px; overflow: hidden; background: #000; }}
      #root {{ position: relative; width: {w}px; height: {h}px; overflow: hidden; background: {bg}; }}
      .scene {{ position: absolute; inset: 0; width: 100%; height: 100%; }}
      {fontface}
    </style>
  </head>
  <body>
    <div id="root" data-composition-id="main" data-start="0" data-duration="{dur}" data-width="{w}" data-height="{h}">
      <div id="el-{clip_id}" class="scene" data-composition-id="{clip_id}"
        data-composition-src="{frag_src}"
        data-start="0" data-duration="{dur}" data-track-index="0"></div>
{cap_body}
{xf_body}    </div>
    <script>
      window.__timelines = window.__timelines || {{}};
      window.__timelines["main"] = gsap.timeline({{ paused: true }});
      (function () {{ var tl = window.__timelines["main"];
        tl.to({{}}, {{ duration: {dur}, ease: "none" }}, 0);
        {cap_js}
        {xf_js}
      }})();
    </script>
  </body>
</html>
"""


def main(proj_dir: str) -> int:
    # Go 侧管道捕获不设 PYTHONUTF8，中文 Windows 默认 cp936 严格解码——✓/✗ 不在 GBK 中，
    # 成功路径的 print 反而炸成 rc=1。errors='replace' 保输出永远可写。
    for stream in (sys.stdout, sys.stderr):
        if hasattr(stream, 'reconfigure'):
            stream.reconfigure(errors='replace')
    proj = pathlib.Path(proj_dir)
    pj = json.loads((proj / 'project.json').read_text(encoding='utf-8'))
    W, H = (1080, 1920) if pj.get('aspect') == '9:16' else (1920, 1080)
    slots = load_slots(proj)
    xfade = transition_mode(proj) == 'fade'
    entries = json.loads((proj / 'style' / 'clips' / 'clips.json').read_text(encoding='utf-8'))

    fontface = fontface_css(slots.get('font') or '')
    resolved = load_font_assets(proj)
    segdir = proj / '.hf-clip'
    segdir.mkdir(exist_ok=True)
    done = 0
    for e in entries:
        clip_id = pathlib.Path(e['file']).stem
        frag = proj / 'style' / 'clips' / e['file']
        if not frag.exists():
            print(f'跳过 {clip_id}：片段不存在')
            continue
        dur = float(e.get('duration') or 3.0)
        if resolved:
            # 片段字体声明纠错+补齐（幂等）：目录字体 local() 空声明 → 真字体 url，
            # 小样里的标题字体才和确认样张一致
            src = frag.read_text(encoding='utf-8')
            fixed = ensure_font_faces(src, resolved)
            if fixed != src:
                frag.write_text(fixed, encoding='utf-8')
        cap_body, cap_js = caption_block(clip_id, dur, (e.get('narration') or '').strip(), slots)
        out = segdir / f'{clip_id}.html'
        # 壳底色取皮肤 bg 槽位；槽位缺失（样张读不出/老数据）回退米白保旧行为
        out.write_text(mini_html(W, H, dur, clip_id, f'style/clips/{e["file"]}',
                                 cap_body, cap_js, xfade, fontface,
                                 slots.get('bg') or '#FDF6E3'), encoding='utf-8')
        done += 1

    # 渲染器要的工程底座（与 assemble.py 同款；样张阶段先于正式 assemble 跑，得自备）
    (proj / 'assets').mkdir(exist_ok=True)
    if not (proj / 'assets' / 'gsap.min.js').exists():
        shutil.copy(GSAP_SRC, proj / 'assets' / 'gsap.min.js')
    HF_SRC = AI_DIR / 'assets' / 'hf.js'
    if HF_SRC.exists():
        shutil.copyfile(HF_SRC, proj / 'assets' / 'hf.js')
    if not (proj / 'package.json').exists():
        (proj / 'package.json').write_text(json.dumps({
            'name': proj.name.replace(' ', '-'), 'private': True, 'type': 'module',
        }, ensure_ascii=False, indent=2), encoding='utf-8')
    if not (proj / 'meta.json').exists():
        (proj / 'meta.json').write_text(json.dumps(
            {'id': proj.name, 'name': pj.get('name', proj.name)}, ensure_ascii=False), encoding='utf-8')
    # render -c 从 index.html 提取 render shell（2026-10-10 实测：缺它整批小样渲染必炸，
    # 报「requires a project index.html」）。shell 只取 head/资产引用，拿第一条小样工程即可。
    first = next((e for e in entries if (segdir / (pathlib.Path(e['file']).stem + '.html')).exists()), None)
    if first:
        (proj / 'index.html').write_text(
            (segdir / (pathlib.Path(first['file']).stem + '.html')).read_text(encoding='utf-8'),
            encoding='utf-8')
    print(f'迷你真段工程壳 ✓ {done}/{len(entries)} 条（.hf-clip/）')
    return 0


if __name__ == '__main__':
    if len(sys.argv) < 2:
        print('用法: python ai/assemble_samples.py <项目目录>', file=sys.stderr)
        sys.exit(2)
    sys.exit(main(sys.argv[1]))
