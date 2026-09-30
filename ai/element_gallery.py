# -*- coding: utf-8 -*-
"""元素样张矩阵：每个注册风格 × 每个元素 kind 各渲一张终帧 PNG（素材中心数据源）。

用法：
  python ai/element_gallery.py build [style_id...]   # 造最小项目（缺省全部风格）
  python ai/element_gallery.py frames [style_id...]  # 渲染并抽帧到 data/library/registry/gallery/
  python ai/element_gallery.py all [style_id...]     # build + frames
风格列 = data/library/registry/styles.json 全部 + hand（手绘默认）+ flat（扁平）。
产物：data/library/registry/gallery/<style_id>/<kind>.png 与 _overview.png（可重入：成片未过期则跳过渲染）。
"""
import json
import pathlib
import shutil
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
REGISTRY = ROOT / 'data' / 'library' / 'registry' / 'elements.json'
STYLES = ROOT / 'data' / 'library' / 'registry' / 'styles.json'
GALLERY = ROOT / 'data' / 'library' / 'registry' / 'gallery'
sys.path.insert(0, str(ROOT / 'ai'))
import seed_styles as _seed  # noqa: E402  复用样张 HTML 生成器
FFBIN = shutil.which('ffmpeg') or 'ffmpeg'  # ffmpeg 可执行（which 解析，不再硬编码本机路径）
FFPROBE = shutil.which('ffprobe') or 'ffprobe'
WORD_DUR = 0.6
NWORDS = 12
# 兜底配图：优先取真实项目下载过的照片，保证 image 元素不落便签占位
IMG_CANDIDATES = sorted((ROOT / 'data' / 'projects').glob('p*/assets/img/img01.*'))

# 非注册引擎列（direction 关键词命中旧手写引擎）
EXTRA_STYLES = [
    {'id': 'hand', 'direction': '手绘叙事（纸面·马克笔）', 'genre': '默认冷启动风格', 'keywords': [], 'photo': ''},
    {'id': 'flat', 'direction': '扁平几何（色块·厚描边）', 'genre': '几何扁平风', 'keywords': [], 'photo': ''},
]

# 26 个 kind 各一段样张 spec（16:9；label 角标注明 kind 名，主元素居中呈现）
def _label(text):
    return {'kind': 'label', 'x': 180, 'y': 150, 'text': text, 'fs': 40, 'reveal': 0}


KIND_SPECS = {
    'title':      {'kind': 'title', 'y': 280, 'text': '风格样张册', 'reveal': 2},
    'note':       {'kind': 'note', 'x': 760, 'y': 380, 'text': '翻正反射', 'bg': 'butter', 'rot': 2, 'reveal': 2},
    'panel':      {'kind': 'panel', 'x': 560, 'y': 240, 'w': 800, 'h': 420, 'title': '面板标题',
                   'text': '厚描边 + 彩色标题栏，承载说明、对比与清单', 'bg': 'mint', 'reveal': 2},
    'chip':       {'kind': 'chip', 'x': 800, 'y': 420, 'text': '胶囊标签', 'bg': 'butter', 'reveal': 2},
    'zone':       {'kind': 'zone', 'x': 460, 'y': 220, 'w': 1000, 'h': 480, 'bg': 'mint', 'reveal': 1},
    'timeline':   {'kind': 'timeline', 'x': 700, 'y': 200, 'nodes': ['初始化服务', '启动系统', '执行命令', '输出结果'],
                   'gap': 140, 'reveal': 2},
    'bracket':    {'kind': 'timeline', 'x': 700, 'y': 180, 'nodes': ['对齐', '渲染', '拼片'], 'gap': 130, 'reveal': 1},
    'strip':      {'kind': 'strip', 'x': 520, 'y': 400, 'n': 6, 'text': '512 维', 'reveal': 2},
    'barrow':     {'kind': 'barrow', 'x': 680, 'y': 380, 'w': 520, 'h': 110, 'color': 'coral', 'reveal': 2},
    'table':      {'kind': 'table', 'x': 420, 'y': 180, 'w': 1080,
                   'rows': [['阶段', '耗时', '产物'], ['对齐', '12s', '词表'], ['渲染', '40s', '帧'], ['拼片', '3s', 'mp4']],
                   'cell_h': 100, 'reveal': 2},
    'quote':      {'kind': 'quote', 'x': 340, 'y': 240, 'w': 1240, 'text': '每一种风格都是一套可复用的视觉语言',
                   'name': '风格注册表', 'reveal': 2},
    'checklist':  {'kind': 'checklist', 'x': 560, 'y': 220, 'nodes': ['暗底自动反白', '图表系列色', '相框质感'],
                   'gap': 150, 'reveal': 2},
    'stat':       {'kind': 'stat', 'x': 640, 'y': 280, 'w': 640, 'text': '27 种', 'title': '元素积木', 'reveal': 2},
    'label':      {'kind': 'label', 'x': 700, 'y': 400, 'text': '纯文字标注，不带底色', 'fs': 56, 'reveal': 2},
    'big':        {'kind': 'big', 'x': 700, 'y': 340, 'text': '3 秒', 'fs': 170, 'reveal': 2},
    'image':      {'kind': 'image', 'x': 660, 'y': 240, 'w': 600, 'h': 400, 'query': '星空',
                   'src': 'assets/img/sample.jpg', 'rot': 0, 'reveal': 2},
    'icon':       {'kind': 'icon', 'x': 830, 'y': 260, 'size': 280, 'name': 'rocket', 'reveal': 2},
    'emoji':      {'kind': 'emoji', 'x': 840, 'y': 280, 'text': '🚀', 'fs': 200, 'reveal': 2},
    'chart_bar':  {'kind': 'chart_bar', 'x': 520, 'y': 220, 'w': 880, 'h': 460, 'values': [30, 55, 40, 70],
                   'labels': ['甲', '乙', '丙', '丁'], 'reveal': 2},
    'chart_line': {'kind': 'chart_line', 'x': 520, 'y': 220, 'w': 880, 'h': 460, 'values': [20, 45, 35, 60, 50],
                   'reveal': 2},
    'chart_pie':  {'kind': 'chart_pie', 'x': 740, 'y': 200, 'w': 440, 'h': 440, 'values': [45, 30, 25],
                   'labels': ['甲', '乙', '丙'], 'reveal': 2},
    'chart_donut': {'kind': 'chart_donut', 'x': 660, 'y': 200, 'w': 600, 'h': 460, 'values': [45, 30, 25],
                    'labels': ['甲', '乙', '丙'], 'title': '占比', 'reveal': 2},
    'disc':       {'kind': 'disc', 'cx': 960, 'cy': 440, 'r': 150, 'bg': 'mint', 'reveal': 2},
    'circle':     {'kind': 'circle', 'cx': 960, 'cy': 440, 'r': 44, 'fill': 'sky', 'reveal': 2},
    'beam':       {'kind': 'beam', 'x': 460, 'y': 420, 'w': 1000, 'h': 28, 'bg': 'sky', 'reveal': 2},
    'arrow':      {'kind': 'arrow', 'x1': 660, 'y1': 440, 'x2': 1260, 'y2': 440, 'reveal': 2},
    'custom':     {'kind': 'custom', 'x': 470, 'y': 190, 'w': 980, 'h': 520, 'text': '斜切大字板',
                   'anim': 'wipe', 'reveal': 2,
                   'html': ('<div class="band"></div><div class="dot"></div>'
                            '<div class="k">元素库<b>V2</b></div>'
                            '<div class="sub">FREE-FORM LAYER</div>'),
                   'css': ('.band{position:absolute;left:-60px;top:130px;width:1100px;height:240px;'
                           'background:linear-gradient(105deg,#E30050 0%,#B388EB 100%);'
                           'transform:skewY(-7deg);}'
                           '.dot{position:absolute;right:-30px;top:-40px;width:340px;height:260px;'
                           'background:radial-gradient(circle at 4px 4px,rgba(255,255,255,.9) 3px,'
                           'transparent 4px) 0 0/26px 26px;}'
                           '.k{position:absolute;left:52px;top:104px;font-size:132px;line-height:1;'
                           'font-weight:900;font-style:italic;color:#FFFFFF;letter-spacing:2px;}'
                           '.k b{color:#141A33;}'
                           '.sub{position:absolute;left:58px;top:266px;font-size:34px;font-weight:700;'
                           'letter-spacing:12px;color:rgba(255,255,255,.95);}'),
                   },
}
# 多元素组合的 kind：补齐陪衬元素让画面成立（kind 语义才完整）
COMPANIONS = {
    'zone': [{'kind': 'note', 'x': 560, 'y': 320, 'text': '分组 A', 'bg': 'butter', 'reveal': 3},
             {'kind': 'note', 'x': 560, 'y': 480, 'text': '分组 B', 'bg': 'sky', 'reveal': 5}],
    'bracket': [{'kind': 'bracket', 'x': 1020, 'y': 180, 'h': 390, 'text': '流水线', 'color': 'green', 'reveal': 5}],
    'arrow': [{'kind': 'circle', 'cx': 600, 'cy': 440, 'r': 36, 'fill': 'white', 'reveal': 1},
              {'kind': 'circle', 'cx': 1320, 'cy': 440, 'r': 36, 'fill': 'mint', 'reveal': 4}],
}
OVERVIEW_SPEC = {
    'note': '风格总览：标题+金句+面板+双指标卡+标签',
    'elements': [
        {'kind': 'title', 'y': 100, 'text': '风格总览', 'reveal': 0},
        {'kind': 'quote', 'x': 170, 'y': 250, 'w': 940, 'text': '每一种风格都是一套可复用的视觉语言',
         'name': '风格注册表', 'reveal': 2},
        {'kind': 'panel', 'x': 1180, 'y': 250, 'w': 560, 'h': 232, 'title': '元素质感', 'bg': 'mint',
         'text': '表面、描边与阴影随风格整套切换', 'reveal': 4},
        {'kind': 'stat', 'x': 170, 'y': 560, 'w': 420, 'text': '13 套', 'title': '注册风格包', 'reveal': 6},
        {'kind': 'stat', 'x': 640, 'y': 560, 'w': 420, 'text': '27 种', 'title': '元素积木', 'reveal': 8},
        {'kind': 'chip', 'x': 1150, 'y': 580, 'text': '逐词高亮', 'bg': 'butter', 'reveal': 9},
    ],
}
NARR = ['素材', '中心', '元素', '样张', '在', '当前', '风格', '下', '的', '呈现', '效果', '示意']


def kinds_in_order():
    return [e['kind'] for e in json.load(open(REGISTRY, encoding='utf-8'))]


def styles_map():
    m = {s['id']: s for s in json.load(open(STYLES, encoding='utf-8'))}
    for s in EXTRA_STYLES:
        m[s['id']] = s
    return m


def words():
    return [{'text': t, 'start': round(i * WORD_DUR, 2), 'end': round((i + 1) * WORD_DUR, 2)}
            for i, t in enumerate(NARR)]


def build(style_ids):
    styles = styles_map()
    els = {e['kind']: e for e in json.load(open(REGISTRY, encoding='utf-8'))}
    ids = style_ids or list(styles)
    for sid in ids:
        st = styles.get(sid)
        if not st:
            print(f'未注册风格: {sid}')
            return 1
        p = ROOT / 'data' / 'projects' / f'_gal-{sid}'  # _ 前缀：不出现在项目列表（非用户项目）
        for d in ('style', 'storyboards', 'llm', 'audio', 'input', 'assets/img', 'compositions/frames'):
            (p / d).mkdir(parents=True, exist_ok=True)
        (p / 'input' / 'article.txt').write_text(''.join(NARR) + '\n', encoding='utf-8')
        json.dump({'id': p.name, 'name': f'元素样张 {sid}', 'aspect': '16:9',
                   'created_at': '2026-09-25T12:00:00+08:00', 'status': 'producing', 'stylepack_id': ''},
                  open(p / 'project.json', 'w', encoding='utf-8'), ensure_ascii=False, indent=1)
        samples_html = _seed.sample_html(st) if sid not in ('hand', 'flat') else '<div></div>'
        json.dump({'direction': st['direction'], 'desc': 'element gallery',
                   'samples': [{'tag': '样张', 'desc': 'gallery', 'html': samples_html}], 'confirmed': True},
                  open(p / 'style' / 'style_samples.json', 'w', encoding='utf-8'), ensure_ascii=False)
        if IMG_CANDIDATES and not (p / 'assets' / 'img' / 'sample.jpg').exists():
            shutil.copy(IMG_CANDIDATES[0], p / 'assets' / 'img' / 'sample.jpg')
        w = words()
        dur = round(len(w) * WORD_DUR, 2)
        order = ['__overview'] + kinds_in_order()
        segs, voices = [], []
        for k, kind in enumerate(order, 1):
            sid2 = f'seg{k:02d}'
            spec = {'note': f'{kind} 样张', 'elements': []}
            if kind == 'custom':
                spec['camera'] = 'zoom_in'  # 段级镜头缓推随 custom 样张一并演示
            if kind == '__overview':
                spec = OVERVIEW_SPEC
            elif kind == 'label':
                spec['elements'] = [_label('label · 文字标注'), KIND_SPECS[kind]]
            else:
                spec['elements'] = ([_label(f"{kind} · {els[kind]['name']}"), KIND_SPECS[kind]]
                                    + COMPANIONS.get(kind, []))
            voices.append({'id': sid2, 'path': f'audio/{sid2}.wav', 'duration_s': dur, 'words': w,
                           'phrases': [[0, 5], [6, len(w) - 1]]})
            segs.append({'id': sid2, 'idx': k, 'key': f'样张{kind}', 'narration': ''.join(NARR),
                         'visual_brief': f'{kind} 元素样张', 'duration_hint': dur})
            json.dump(spec, open(p / 'llm' / f'comp-{sid2}.spec.json', 'w', encoding='utf-8'),
                      ensure_ascii=False, indent=1)
        json.dump({'tts_provider': 'gallery', 'voice_id': 'none', 'asr_aligner': 'none', 'voices': voices},
                  open(p / 'audio_meta.json', 'w', encoding='utf-8'), ensure_ascii=False, indent=1)
        json.dump({'timing_basis': 'narration', 'plan': 'gallery', 'segments': segs},
                  open(p / 'storyboards' / 'storyboard.json', 'w', encoding='utf-8'), ensure_ascii=False, indent=1)
        for v in voices:  # 静音音轨（hyperframes render 要求音频存在）
            out = p / v['path']
            if not out.exists():
                subprocess.run([FFBIN, '-y', '-v', 'error', '-f', 'lavfi', '-i', 'anullsrc=r=24000:cl=mono',
                                '-t', str(v['duration_s'] + 0.4), str(out)], check=True)
        print(f'build ✓ {p.name}（{len(segs)} 段）')
    return 0


def sh(cmd, cwd=ROOT, timeout=3600):
    import os
    print('>', ' '.join(str(c) for c in cmd[:4]), '…', flush=True)
    resolved = []
    for c in cmd:
        s = str(c)
        if s in ('npx', 'python', sys.executable) and not pathlib.Path(s).exists():
            s = shutil.which(s) or s
        resolved.append(s)
    # FFBIN 是绝对路径时把其目录并进子进程 PATH（which 命中的裸名本就在 PATH 里）
    ffdir = str(pathlib.Path(FFBIN).parent) if pathlib.Path(FFBIN).is_absolute() else ''
    return subprocess.run(resolved, cwd=str(cwd), capture_output=True, text=True,
                          env={**os.environ, 'PATH': (ffdir + os.pathsep if ffdir else '') + os.environ['PATH']},
                          timeout=timeout)


def _inputs_mtime(p):
    """样张 freshness 基准：specs + storyboard + audio_meta + 渲染代码/注册表。
    只比 specs 会漏两类过期：registry 加 kind 后 storyboard 段数变了、渲染器代码变了——
    旧 mp4 比 specs 新就被复用，按新 kind 数切旧段数成片 → 整组样张错位。"""
    files = (list(p.glob('llm/comp-*.spec.json'))
             + [p / 'storyboards' / 'storyboard.json', p / 'audio_meta.json',
                ROOT / 'ai' / 'render_spec.py', ROOT / 'ai' / 'element_gallery.py',
                ROOT / 'data' / 'library' / 'registry' / 'elements.json'])
    files += list((ROOT / 'ai' / 'engines').glob('*.py'))
    ts = [f.stat().st_mtime for f in files if f.exists()]
    return max(ts) if ts else 0.0


def frames(style_ids):
    styles = styles_map()
    ids = style_ids or list(styles)
    order = ['__overview'] + kinds_in_order()
    fails = []
    for sid in ids:
        p = ROOT / 'data' / 'projects' / f'_gal-{sid}'
        if not p.exists():
            print(f'{sid}: 未 build，跳过')
            continue
        sb = json.load(open(p / 'storyboards' / 'storyboard.json', encoding='utf-8'))
        if len(sb['segments']) != len(order):  # registry 加了 kind → 工程自愈重建
            print(f'{sid}: 样张工程 {len(sb["segments"])} 段 ≠ {len(order)} kind，重建')
            if build([sid]) != 0:
                fails.append((sid, 'build'))
                continue
        out_dir = GALLERY / sid
        out_dir.mkdir(parents=True, exist_ok=True)
        want = {k: out_dir / f'{"_overview" if k == "__overview" else k}.png' for k in order}
        fresh = _inputs_mtime(p)
        have_all = all(f.exists() and f.stat().st_mtime > fresh for f in want.values())
        mp4 = p / 'renders' / 'main.mp4'
        if have_all and mp4.exists() and mp4.stat().st_mtime > fresh:
            print(f'{sid}: 样张未过期，跳过')
            continue
        r = sh([sys.executable, 'ai/render_spec.py', p])
        if r.returncode != 0:
            fails.append((sid, 'render_spec')); print(f'{sid}: render_spec FAIL'); continue
        for f in (p / 'compositions' / 'frames').glob('*.html'):
            f.unlink()
        if sh([ROOT / 'bin' / 'm0.exe', 'run', p.name, 'compositions']).returncode != 0:
            fails.append((sid, 'm0')); print(f'{sid}: m0 FAIL'); continue
        if sh([sys.executable, 'ai/assemble.py', p]).returncode != 0:
            fails.append((sid, 'assemble')); print(f'{sid}: assemble FAIL'); continue
        ok = p / '.hyperframes-ok'
        if ok.exists():
            ok.unlink()
        r = sh(['npx', '--offline', '--yes', 'hyperframes@0.8.55', 'check'], cwd=p)
        if r.returncode != 0 or 'Check passed' not in (r.stdout or ''):
            fails.append((sid, 'check')); print(f'{sid}: check FAIL\n{(r.stdout or "")[-800:]}'); continue
        if not mp4.exists() or mp4.stat().st_mtime < fresh:
            if mp4.exists():
                mp4.unlink()
            r = sh(['npx', '--offline', '--yes', 'hyperframes@0.8.55', 'render', '-o', 'renders/main.mp4'],
                   cwd=p, timeout=7200)
            if 'rendered in' not in (r.stdout or ''):
                fails.append((sid, 'render')); print(f'{sid}: render FAIL\n{(r.stdout or "")[-500:]}'); continue
        # 段边界不猜：ffprobe 总时长 / 段数（各段等长由构造保证），取每段 75% 处
        # （全部 reveal 已落定、又未到下一段；此前按 dur+0.4 估边界整体偏移过一段）
        pr = subprocess.run([FFPROBE, '-v', 'error', '-show_entries', 'format=duration',
                             '-of', 'default=noprint_wrappers=1:nokey=1', str(mp4)],
                            capture_output=True, text=True, check=True)
        seg_len = float(pr.stdout.strip()) / len(order)
        for i, k in enumerate(order):
            t = round((i + 0.75) * seg_len, 2)
            subprocess.run([FFBIN, '-y', '-v', 'error', '-ss', str(t), '-i', str(mp4),
                            '-frames:v', '1', str(want[k])], check=True)
        print(f'{sid}: {len(order)} 张 ✓', flush=True)
    if fails:
        print('失败清单:', fails)
        return 1
    return 0


if __name__ == '__main__':
    cmd = sys.argv[1] if len(sys.argv) > 1 else 'all'
    ids = sys.argv[2:]
    if cmd == 'build':
        sys.exit(build(ids))
    if cmd == 'frames':
        sys.exit(frames(ids))
    rc = build(ids)
    sys.exit(rc or frames(ids))
