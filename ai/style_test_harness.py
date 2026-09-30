# -*- coding: utf-8 -*-
"""多风格验证管线：为 styles.json 每个注册风格造最小测试项目并出片抽帧。

用法：
  python ai/style_test_harness.py build <style_id...>   # 造项目（缺省全部）
  python ai/style_test_harness.py frames <style_id...>  # render_spec→m0→assemble→check→render→抽帧
产物：data/projects/p-style-<id>/renders/frames/segNN_<t>s.png
"""
import json
import pathlib
import shutil
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
REGISTRY = ROOT / 'data' / 'library' / 'registry' / 'styles.json'
sys.path.insert(0, str(ROOT / 'ai'))
import seed_styles as _seed  # noqa: E402  复用样张生成器
FF = shutil.which('ffmpeg') or 'ffmpeg'  # ffmpeg 可执行（which 解析，不再硬编码本机路径）
FFBIN = str(pathlib.Path(FF).parent) if pathlib.Path(FF).is_absolute() else ''
WORD_DUR = 0.6

NARR1 = ['每一种', '风格', '都是', '一套', '可以', '直接', '复用', '的', '视觉', '语言', '系统']
NARR2 = ['对勾清单', '环形图', '图标', '照片', '标注', '和', '大数字', '都', '有', '主题', '配色']

SPEC1 = {
    'note': '风格样张第一段：标题+金句+双指标卡+标签+卡片',
    'elements': [
        {'kind': 'title', 'y': 100, 'text': '风格样张', 'reveal': 0},
        {'kind': 'quote', 'x': 170, 'y': 250, 'w': 940, 'text': '每一种风格都是一套可复用的视觉语言', 'name': '风格注册表', 'reveal': 2},
        {'kind': 'panel', 'x': 1180, 'y': 250, 'w': 560, 'h': 232, 'title': '元素质感', 'text': '表面、描边与阴影随风格整套切换', 'bg': 'mint', 'reveal': 4},
        {'kind': 'stat', 'x': 170, 'y': 560, 'w': 420, 'text': '13套', 'title': '注册风格包', 'reveal': 6},
        {'kind': 'stat', 'x': 640, 'y': 560, 'w': 420, 'text': '26种', 'title': '元素积木', 'color': 'sky', 'reveal': 8},
        {'kind': 'chip', 'x': 1150, 'y': 580, 'text': '逐词高亮', 'bg': 'butter', 'reveal': 9},
    ],
}
SPEC2 = {
    'note': '风格样张第二段：清单+环形图+图标+图片兜底+标注+大数字',
    'elements': [
        {'kind': 'checklist', 'x': 170, 'y': 170, 'nodes': ['暗底自动反白', '图表系列色', '照片相框质感'], 'gap': 150, 'fs': 44, 'reveal': 0},
        {'kind': 'chart_donut', 'x': 1120, 'y': 140, 'w': 640, 'h': 380, 'values': [45, 30, 25], 'labels': ['甲', '乙', '丙'], 'title': '占比', 'reveal': 2},
        {'kind': 'icon', 'x': 250, 'y': 620, 'size': 140, 'name': 'rocket', 'reveal': 4},
        {'kind': 'image', 'x': 470, 'y': 600, 'w': 420, 'h': 240, 'query': '星空', 'reveal': 6},
        {'kind': 'label', 'x': 980, 'y': 640, 'text': '揭示随旁白', 'reveal': 8},
        {'kind': 'big', 'x': 1440, 'y': 600, 'text': '100%', 'fs': 100, 'reveal': 9},
    ],
}


def words(texts):
    return [{'text': t, 'start': round(i * WORD_DUR, 2), 'end': round((i + 1) * WORD_DUR, 2)}
            for i, t in enumerate(texts)]


def build(style_ids):
    styles = {s['id']: s for s in json.load(open(REGISTRY, encoding='utf-8'))}
    ids = style_ids or list(styles)
    for sid in ids:
        st = styles.get(sid)
        if not st:
            print(f'未注册风格: {sid}')
            return 1
        p = ROOT / 'data' / 'projects' / f'p-style-{sid}'
        (p / 'style').mkdir(parents=True, exist_ok=True)
        (p / 'storyboards').mkdir(exist_ok=True)
        (p / 'llm').mkdir(exist_ok=True)
        (p / 'audio').mkdir(exist_ok=True)
        (p / 'input').mkdir(exist_ok=True)
        (p / 'input' / 'article.txt').write_text(
            ''.join(NARR1) + '\n\n' + ''.join(NARR2) + '\n', encoding='utf-8')
        json.dump({'id': p.name, 'name': f'风格验证 {sid}', 'aspect': '16:9',
                   'created_at': '2026-09-24T12:00:00+08:00', 'status': 'producing',
                   'stylepack_id': ''}, open(p / 'project.json', 'w', encoding='utf-8'),
                  ensure_ascii=False, indent=1)
        json.dump({'direction': st['direction'], 'desc': 'style harness',
                   'samples': [{'tag': '样张 A · 风格预览', 'desc': 'harness',
                                'html': _seed.sample_html(st)}],
                   'confirmed': True},
                  open(p / 'style' / 'style_samples.json', 'w', encoding='utf-8'), ensure_ascii=False)
        segs, voices = [], []
        for k, (narr, spec) in enumerate([(NARR1, SPEC1), (NARR2, SPEC2)], 1):
            sid2 = f'seg{k:02d}'
            w = words(narr)
            dur = round(len(w) * WORD_DUR, 2)
            voices.append({'id': sid2, 'path': f'audio/{sid2}.wav', 'duration_s': dur, 'words': w,
                           'phrases': [[0, 5], [6, len(w) - 1]]})
            segs.append({'id': sid2, 'idx': k, 'key': f'风格段{k}', 'narration': ''.join(narr),
                         'visual_brief': st['direction'], 'duration_hint': dur})
            json.dump(spec, open(p / 'llm' / f'comp-{sid2}.spec.json', 'w', encoding='utf-8'),
                      ensure_ascii=False, indent=1)
        json.dump({'tts_provider': 'harness', 'voice_id': 'none', 'asr_aligner': 'none',
                   'voices': voices}, open(p / 'audio_meta.json', 'w', encoding='utf-8'),
                  ensure_ascii=False, indent=1)
        json.dump({'timing_basis': 'narration', 'plan': 'harness', 'segments': segs},
                  open(p / 'storyboards' / 'storyboard.json', 'w', encoding='utf-8'),
                  ensure_ascii=False, indent=1)
        for v in voices:  # 静音音轨（hyperframes render 需要 audio 文件存在）
            out = p / v['path']
            if not out.exists():
                subprocess.run([FF, '-y', '-v', 'error',
                                '-f', 'lavfi', '-i', f'anullsrc=r=24000:cl=mono',
                                '-t', str(v['duration_s'] + 0.4), str(out)], check=True)
        print(f'build ✓ {p.name}')
    return 0


def sh(cmd, cwd=ROOT, timeout=600):
    import os
    import shutil
    print('>', ' '.join(str(c) for c in cmd), flush=True)
    resolved = []
    for c in cmd:
        s = str(c)
        if s in ('npx', 'python', sys.executable) and not pathlib.Path(s).exists():
            s = shutil.which(s) or s
        resolved.append(s)
    r = subprocess.run(resolved, cwd=str(cwd), capture_output=True, text=True,
                       env={**os.environ, 'PATH': (FFBIN + os.pathsep if FFBIN else '') + os.environ['PATH']},
                       timeout=timeout)
    if r.returncode != 0:
        print((r.stdout or '')[-1500:], (r.stderr or '')[-600:], flush=True)
    return r


def frames(style_ids):
    styles = {s['id']: s for s in json.load(open(REGISTRY, encoding='utf-8'))}
    ids = style_ids or list(styles)
    for sid in ids:
        p = ROOT / 'data' / 'projects' / f'p-style-{sid}'
        pid = p.name
        r = sh([sys.executable, 'ai/render_spec.py', p])
        if r.returncode != 0:
            print(f'{pid}: render_spec FAIL')
            continue
        for f in (p / 'compositions' / 'frames').glob('*.html'):
            f.unlink()
        if sh([ROOT / 'bin' / 'm0.exe', 'run', pid, 'compositions']).returncode != 0:
            print(f'{pid}: m0 FAIL')
            continue
        if sh([sys.executable, 'ai/assemble.py', p]).returncode != 0:
            print(f'{pid}: assemble FAIL')
            continue
        ok = p / '.hyperframes-ok'
        if ok.exists():
            ok.unlink()
        r = sh(['npx', '--offline', '--yes', 'hyperframes@0.8.55', 'check'], cwd=p)
        if r.returncode != 0 or 'Check passed' not in (r.stdout or ''):
            print(f'{pid}: check FAIL\n{(r.stdout or "")[-1200:]}')
            continue
        if not (p / 'renders' / 'main.mp4').exists():
            r = sh(['npx', '--offline', '--yes', 'hyperframes@0.8.55', 'render', '-o', 'renders/main.mp4'],
                   cwd=p, timeout=1800)
            if 'rendered in' not in (r.stdout or ''):
                print(f'{pid}: render FAIL\n{(r.stdout or "")[-600:]}')
                continue
        fdir = p / 'renders' / 'frames'
        fdir.mkdir(parents=True, exist_ok=True)
        for name, t in [('seg01_6.5', '6.5'), ('seg02_13.45', '13.45')]:
            out = fdir / f'{name}s.png'
            subprocess.run([FF, '-y', '-v', 'error', '-ss', t,
                            '-i', str(p / 'renders' / 'main.mp4'), '-frames:v', '1', str(out)], check=True)
        print(f'{pid}: frames ✓')
    return 0


if __name__ == '__main__':
    cmd = sys.argv[1] if len(sys.argv) > 1 else 'build'
    ids = sys.argv[2:]
    sys.exit(build(ids) if cmd == 'build' else frames(ids))
