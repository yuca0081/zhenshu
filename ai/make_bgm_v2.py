# -*- coding: utf-8 -*-
"""BGM 曲库生成器 v2——MIDI 作曲 + FluidSynth 真音色渲染（2026-10-21）。

v1（make_bgm.py）用正弦波手搓氛围垫；v2 换作曲管线：Python 写乐谱（MIDIUtil）
→ FluidSynth + FluidR3_GM 音色库渲染（真乐器采样：钢琴/电钢/弦乐/贝斯/鼓组）。
曲子是代码（版权自持），音色是 MIT 授权采样（ai/vendor/soundfonts/README.txt 留档）。
产物 mp3 落 data/library/media/audio/，与 v1 共用一份 tracks.json——Go 侧
bgmFor/mixBGM 零改动；新增 bpm/tempo/energy/source 字段留给未来自动选曲规则。

循环无缝性：音符网格在末小节收干净，loop 拼接无爆点；渲染尾部 40ms 淡出防
混响尾截断的咔哒声——成片里 BGM 被闪避压在人声之下，这些都在听感盲区。

用法：python ai/make_bgm_v2.py [--force] [--reel]
  --force 重生成已存在曲目；--reel 串烧全部 v2 曲目出 preview_v2_reel.mp3 试听集
工具链不在位（midiutil/fluidsynth/sf2）→ 报错退出给指引，不静默——Go 侧只读
tracks.json 从不调本脚本，这里失败纯属开发机配置问题，响亮失败才对。
"""
import json
import pathlib
import shutil
import subprocess
import sys
import tempfile
import wave

import numpy as np

from av_common import ffmpeg_encode, write_wav

SR = 44100
HERE = pathlib.Path(__file__).resolve().parent
OUT_DIR = HERE.parent / 'data' / 'library' / 'media' / 'audio'
FLUIDSYNTH = HERE / 'vendor' / 'fluidsynth' / 'bin' / 'fluidsynth.exe'
SOUNDFONT = HERE / 'vendor' / 'soundfonts' / 'FluidR3_GM.sf2'

# ── 乐理 ─────────────────────────────────────────────────────

NOTE_PC = {'C': 0, 'D': 2, 'E': 4, 'F': 5, 'G': 7, 'A': 9, 'B': 11}

QUAL = {
    '':     [0, 4, 7],
    'm':    [0, 3, 7],
    'maj7': [0, 4, 7, 11],
    'm7':   [0, 3, 7, 10],
    '7':    [0, 4, 7, 10],
    'sus4': [0, 5, 7],
    'dim':  [0, 3, 6],
    'm7b5': [0, 3, 6, 10],
}


def parse_chord(sym):
    """'F#m7' → (根音音级 0-11, 音程表)。"""
    i = 2 if len(sym) > 1 and sym[1] in '#b' else 1
    root = NOTE_PC[sym[0]]
    if i == 2:
        root += 1 if sym[1] == '#' else -1
    q = sym[i:]
    if q not in QUAL:
        raise ValueError(f'未知和弦记号 {sym!r}（认识：{sorted(QUAL)}）')
    return root % 12, QUAL[q]


def voicing(sym, low=48):
    """中音区和弦排列：根音落在 [low, low+12)，三/七音向上叠。'Fmaj7' → [53,57,60,64]。"""
    root, iv = parse_chord(sym)
    r = low + root
    return [r + off for off in iv]


def bass_note(sym, octv=2):
    """贝斯根音：octv=2 → [36,47] 区。"""
    root, _ = parse_chord(sym)
    return octv * 12 + root


def wobble(base, bar, beat):
    """确定性力度微动 ±3：每拍完全一致会听出机器人味。"""
    return max(1, min(127, base + (bar * 7 + beat * 3) % 6 - 3))


# ── 声部写入 ─────────────────────────────────────────────────
# 时间单位=拍（4/4 一小节 4 拍）；16 步格子=十六分音符，step=0.25 拍。
# MIDI 通道约定：0=和声垫/柱式、1=贝斯、2=点缀/琶音、9=鼓（GM 规定 10 通道）。

def expand_prog(prog, bars):
    """[('Fmaj7',2),...] → 长度 bars 的 小节→和弦 表。进行=一轮循环，自动平铺；
    一轮超过曲长才算配置错误（循环曲没有"铺不满"一说）。"""
    cycle = []
    for sym, n in prog:
        cycle += [sym] * n
    if not cycle:
        raise ValueError('空和弦进行')
    if len(cycle) > bars:
        raise ValueError(f'和弦进行一轮 {len(cycle)} 小节，超过 bars={bars}')
    reps = -(-bars // len(cycle))
    return (cycle * reps)[:bars]


def add_pad(mf, ch, table, vel, from_bar=0):
    """整小节和弦垫：每小节一个全音符和弦 + 低八度根音垫底。"""
    for bar, sym in enumerate(table):
        if bar < from_bar:
            continue
        mf.addNote(0, ch, voicing(sym)[0] - 12, bar * 4, 4, vel)
        for n in voicing(sym):
            mf.addNote(0, ch, n, bar * 4, 4, wobble(vel, bar, 0))


def add_grid(mf, ch, table, grid, vel, stac=0.5, shift=0, from_bar=0):
    """柱式节奏型：grid 16 步（十六分）循环，x=弹 o=重音。"""
    bars = len(table)
    step = 0.25
    for bar in range(bars):
        if bar < from_bar:
            continue
        tones = voicing(table[bar])
        for s, c in enumerate(grid):
            if c not in 'xo':
                continue
            v = vel + 12 if c == 'o' else vel
            mf.addNote(0, ch, tones[s % len(tones)] + shift,
                       bar * 4 + s * step, step * stac, wobble(v, bar, s))


def add_arp(mf, ch, table, vel, rate=0.5, pat=(0, 1, 2, 1, 3, 1, 2, 1),
            shift=0, from_bar=0):
    """琶音：rate=每音拍数（0.5=八分），pat=和弦音索引循环。"""
    bars = len(table)
    k = 0
    for bar in range(bars):
        if bar < from_bar:
            continue
        tones = voicing(table[bar])
        t = bar * 4
        while t < (bar + 1) * 4 - 1e-9:
            n = tones[pat[k % len(pat)] % len(tones)] + shift
            mf.addNote(0, ch, n, t, rate * 0.9, wobble(vel, bar, k))
            t += rate
            k += 1


def add_bass(mf, ch, table, inst, grid, vel, style='root', dur=1.8):
    """贝斯：grid 16 步循环；style=root 只根音，root5 三五度交替。"""
    bars = len(table)
    step = 0.25
    for bar in range(bars):
        sym = table[bar]
        root, _ = parse_chord(sym)
        fifth = (root + 7) % 12
        k5 = 0
        for s, c in enumerate(grid):
            if c != 'x':
                continue
            if style == 'root5':
                pc = root if k5 % 2 == 0 else fifth
                k5 += 1
            else:
                pc = root
            mf.addNote(0, ch, bass_note(sym) + (pc - parse_chord(sym)[0]),
                       bar * 4 + s * step, dur, wobble(vel, bar, s))


def add_drums(mf, ch, grids, from_bar=0):
    """鼓组：{'kick':16步格,'snare':..,'hat':..,'open':..,'shaker':..} + 各自力度。"""
    gm = {'kick': 36, 'snare': 38, 'rim': 37, 'hat': 42, 'open': 46,
          'ride': 51, 'tamb': 54, 'shaker': 70}
    step = 0.25
    bars = len(next(iter(grids.values()))) if grids else 0
    for name, grid in grids.items():
        if not grid:
            continue
        note = gm[name]
        base = {'kick': 78, 'snare': 62, 'rim': 48, 'hat': 38, 'open': 50,
                'ride': 40, 'tamb': 40, 'shaker': 30}[name]
        for bar in range(bars):
            if bar < from_bar:
                continue
            for s, c in enumerate(grid):
                if c not in 'xo':
                    continue
                v = base + (12 if c == 'o' else 0)
                mf.addNote(0, ch, note, bar * 4 + s * step, 0.25,
                           wobble(v, bar, s))


# ── 曲目定义 ─────────────────────────────────────────────────
# 时长 = bars × 4 拍 × 60/bpm。后半段用 from_bar 让声部递进，避免 16 小节一跟到底。

TRACKS = [
    {
        'name': 'lofi_night',
        'mood': '松弛 lo-fi 夜色（电钢+软鼓刷）——深夜叙事/城市夜景/慢节奏话题',
        'bpm': 68, 'bars': 16, 'tempo': 'slow', 'energy': 2,
        'prog': [('Fmaj7', 2), ('Em7', 2), ('Dm7', 2), ('Cmaj7', 2)],
        'layers': [
            ('pad',   dict(inst=89, vel=40)),
            ('grid',  dict(inst=4, vel=58, grid='--x--x----x--x--', stac=0.45)),
            ('bass',  dict(inst=33, vel=64, grid='x-------x-------', style='root5')),
            ('drums', dict(kick='x------x--x-----', rim='....x.......x...',
                           hat='--x---x---x---x-', hat_from=8)),
        ],
    },
    {
        'name': 'piano_story',
        'mood': '钢琴叙事（分解和弦+弦乐底）——人物故事/传记/温情/回忆',
        'bpm': 84, 'bars': 16, 'tempo': 'slow', 'energy': 2,
        'prog': [('C', 2), ('G', 2), ('Am', 2), ('F', 2), ('C', 2), ('G', 2),
                 ('F', 2), ('C', 2)],
        'layers': [
            ('pad',  dict(inst=48, vel=36, from_bar=4)),
            ('arp',  dict(inst=0, vel=54, rate=0.5, pat=(0, 1, 2, 1, 3, 1, 2, 1))),
            ('bass', dict(inst=32, vel=52, grid='x-------', style='root', dur=3.5)),
        ],
    },
    {
        'name': 'strings_rise',
        'mood': '弦乐推进（组弦+定音底鼓）——历史/战争/宏大叙事/渐强段落',
        'bpm': 88, 'bars': 16, 'tempo': 'mid', 'energy': 4,
        'prog': [('Am', 2), ('F', 2), ('C', 2), ('G', 2)],
        'layers': [
            ('pad',  dict(inst=48, vel=56)),
            ('grid', dict(inst=48, vel=38, grid='x---x---x---x---', stac=0.85,
                          shift=12, from_bar=8)),
            ('bass', dict(inst=32, vel=62, grid='x-------x-------', style='root5')),
            ('drums', dict(kick='x---------------',
                           hat='x-------x-------')),
        ],
    },
    {
        'name': 'morning_walk',
        'mood': '明快行进（马林巴+轻鼓）——旅行/日常/轻松话题/户外',
        'bpm': 104, 'bars': 16, 'tempo': 'mid', 'energy': 3,
        'prog': [('G', 2), ('D', 2), ('Em', 2), ('C', 2)],
        'layers': [
            ('arp',  dict(inst=12, vel=56, rate=0.25,
                          pat=(0, 2, 1, 2, 3, 2, 1, 2, 0, 2, 1, 2, 3, 2, 1, 2))),
            ('grid', dict(inst=0, vel=46, grid='--x---x---x---x-', stac=0.4,
                          shift=12)),
            ('bass', dict(inst=33, vel=62, grid='x-------x-------', style='root5')),
            ('drums', dict(kick='x---x-------x---', snare='....x.......x...',
                           hat='x-x-x-x-x-x-x-x-')),
        ],
    },
    {
        'name': 'sunny_pop',
        'mood': '欢快流行（钢琴+铃铛+四踩）——美食/萌宠/开箱/生活好物',
        'bpm': 116, 'bars': 16, 'tempo': 'fast', 'energy': 4,
        'prog': [('C', 1), ('G', 1), ('Am', 1), ('F', 1)],
        'layers': [
            ('grid', dict(inst=0, vel=56, grid='x--x--x-x--x--x-', stac=0.5)),
            ('arp',  dict(inst=9, vel=42, rate=0.5, pat=(0, 2, 3, 2), shift=12,
                          from_bar=8)),
            ('bass', dict(inst=33, vel=66, grid='x-----x-x-----x-', style='root5')),
            ('drums', dict(kick='x-------x-x-----', snare='....x.......x...',
                           hat='x-x-x-x-x-x-x-x-', tamb='--x---x---x---x-')),
        ],
    },
    {
        'name': 'soft_mystery',
        'mood': '悬浮悬念（暖垫+颤音琴点）——谜题/科技/暗色/未解之谜',
        'bpm': 66, 'bars': 16, 'tempo': 'slow', 'energy': 1,
        'prog': [('Dm7', 2), ('Bbmaj7', 2), ('Gm7', 2), ('Asus4', 2)],
        'layers': [
            ('pad',  dict(inst=89, vel=44)),
            ('grid', dict(inst=11, vel=42, grid='--------x-------', stac=0.9)),
            ('bass', dict(inst=32, vel=50, grid='x---------------', style='root',
                          dur=3.6)),
            ('drums', dict(kick='x-------x-------', shaker='x---x---x---x---',
                           shaker_from=8)),
        ],
    },
]

# (和弦, 小节数) 展开后的总小节必须等于 bars；省写时以最后和弦铺满——
# 这里全部显式铺满，展开器负责把账算清楚。


# ── 渲染 ─────────────────────────────────────────────────────

def toolchain():
    """工具链定位与体检：缺件给出可执行的补救指引。"""
    problems = []
    try:
        import midiutil  # noqa: F401
    except ImportError:
        problems.append('缺 midiutil：python -m pip install midiutil')
    if not FLUIDSYNTH.exists():
        problems.append(f'缺 FluidSynth：期望 {FLUIDSYNTH}（github releases '
                        'FluidSynth/fluidsynth v2.6.1 win10-x64 zip 解压即用）')
    if not SOUNDFONT.exists():
        problems.append(f'缺音色库：期望 {SOUNDFONT}（Debian fluid-soundfont 3.1 '
                        'orig 包提取，来源与授权见 ai/vendor/soundfonts/README.txt）')
    return problems


def render_track(tr, force=False):
    """单轨：MIDI → FluidSynth wav → 峰值归一 + 尾部淡出 → mp3。返回 (mp3, 秒)。"""
    import midiutil
    mp3 = OUT_DIR / f"{tr['name']}.mp3"
    if mp3.exists() and not force:
        print(f"{tr['name']}: 已存在，跳过（--force 重生成）")
        return mp3, None

    bars = tr['bars']
    table = expand_prog(tr['prog'], bars)
    mf = midiutil.MIDIFile(1)
    mf.addTempo(0, 0, tr['bpm'])
    # 每个声部独占一条通道：GM 音色按通道设置，共用通道会被后设的 ProgramChange
    # 整条改掉（pad 被点乐器顶掉那种事故）。1=贝斯、9=鼓固定，其余从池里发。
    free_ch = iter([0, 2, 3, 4, 5, 6])
    for kind, kw in tr['layers']:
        from_bar = kw.get('from_bar', 0) or 0
        if kind == 'pad':
            ch = next(free_ch)
            mf.addProgramChange(0, ch, 0, kw['inst'])
            add_pad(mf, ch, table, kw['vel'], from_bar)
        elif kind == 'grid':
            ch = next(free_ch)
            mf.addProgramChange(0, ch, 0, kw['inst'])
            add_grid(mf, ch, table, kw['grid'], kw['vel'],
                     stac=kw.get('stac', 0.5), shift=kw.get('shift', 0),
                     from_bar=from_bar)
        elif kind == 'arp':
            ch = next(free_ch)
            mf.addProgramChange(0, ch, 0, kw['inst'])
            add_arp(mf, ch, table, kw['vel'], rate=kw.get('rate', 0.5),
                    pat=kw.get('pat', (0, 1, 2, 1, 3, 1, 2, 1)),
                    shift=kw.get('shift', 0), from_bar=from_bar)
        elif kind == 'bass':
            mf.addProgramChange(0, 1, 0, kw['inst'])
            add_bass(mf, 1, table, kw['inst'], kw['grid'], kw['vel'],
                     style=kw.get('style', 'root'), dur=kw.get('dur', 1.8))
        elif kind == 'drums':
            names = ('kick', 'snare', 'rim', 'hat', 'open', 'ride', 'tamb',
                     'shaker')
            grids = {k: kw[k] for k in names if k in kw}
            deferred = {}
            for k in list(grids):  # 带 _from 后缀的声部延后进场（hat_from=8）
                fk = kw.get(f'{k}_from')
                if fk:
                    deferred[k] = grids.pop(k), fk
            add_drums(mf, 9, grids, from_bar=0)
            for k, (g, fk) in deferred.items():
                add_drums(mf, 9, {k: g}, from_bar=fk)
        else:
            raise ValueError(f'未知声部类型 {kind!r}')

    expect = bars * 4 * 60.0 / tr['bpm']
    with tempfile.NamedTemporaryFile(suffix='.mid', delete=False) as fh:
        mid_path = pathlib.Path(fh.name)
    with tempfile.NamedTemporaryFile(suffix='.wav', delete=False) as fh:
        wav_path = pathlib.Path(fh.name)
    try:
        with open(mid_path, 'wb') as fh:
            mf.writeFile(fh)
        r = subprocess.run(
            [str(FLUIDSYNTH), '-ni', '-F', str(wav_path), '-r', str(SR),
             '-g', '0.7', str(SOUNDFONT), str(mid_path)],
            capture_output=True, text=True, encoding='utf-8', errors='replace')
        if r.returncode != 0 or not wav_path.exists():
            print(f"{tr['name']}: FluidSynth FAIL: {(r.stderr or '')[-200:]}")
            return mp3, None

        with wave.open(str(wav_path), 'rb') as w:
            nch, sw, sr, frames = (w.getnchannels(), w.getsampwidth(),
                                   w.getframerate(), w.getnframes())
            raw = w.readframes(frames)
        if nch != 2 or sw != 2 or sr != SR:
            print(f"{tr['name']}: 非预期 wav 格式 ch={nch} sw={sw} sr={sr}，跳过归一")
            shutil.move(str(wav_path), str(mp3.with_suffix('.tmp.wav')))
            ok = ffmpeg_encode(mp3.with_suffix('.tmp.wav'), mp3, bitrate='160k')
            mp3.with_suffix('.tmp.wav').unlink(missing_ok=True)
            return mp3, frames / sr if ok else None

        x = np.frombuffer(raw, dtype='<i2').astype(float) / 32767.0
        x = x.reshape(-1, 2)
        peak = np.abs(x).max() or 1.0
        x = x / peak * 0.55
        # 裁掉 fluidsynth 多渲的混响死气（比 MIDI 结尾多出数秒，循环播放会变成
        # 周期性"断气"）：留曲长 +1.5s 混响尾，尾部 150ms 淡出防拼接咔哒
        keep = int((expect + 1.5) * SR)
        if len(x) > keep:
            x = x[:keep]
        fade = min(int(SR * 0.15), len(x))
        x[-fade:] *= np.linspace(1, 0, fade)[:, None]
        write_wav(wav_path, x[:, 0], x[:, 1], SR)
        ok = ffmpeg_encode(wav_path, mp3, bitrate='160k')
        if not ok:
            return mp3, None
        print(f"{tr['name']}: {mp3.stat().st_size // 1024}KB，"
              f"{len(x) / SR:.1f}s（期望 {expect:.1f}s）✓")
        return mp3, len(x) / SR
    finally:
        mid_path.unlink(missing_ok=True)
        wav_path.unlink(missing_ok=True)


def update_manifest(tr, dur):
    """tracks.json 合并更新：v1 条目原样保留，v2 条目带 tempo/energy/source。"""
    path = OUT_DIR / 'tracks.json'
    items = []
    if path.exists():
        items = json.loads(path.read_text(encoding='utf-8'))
    items = [it for it in items if it.get('name') != tr['name']]
    items.append({'name': tr['name'], 'file': f"{tr['name']}.mp3",
                  'duration': round(dur, 1), 'mood': tr['mood'],
                  'bpm': tr['bpm'], 'tempo': tr['tempo'], 'energy': tr['energy'],
                  'source': 'midi-fluidsynth/FluidR3_GM'})
    path.write_text(json.dumps(items, ensure_ascii=False, indent=1),
                    encoding='utf-8')


def load_audio(mp3):
    """mp3 → (n,2) float 数组（-1..1），供串烧拼接。"""
    ff = shutil.which('ffmpeg')
    with tempfile.NamedTemporaryFile(suffix='.wav', delete=False) as fh:
        tmp = pathlib.Path(fh.name)
    try:
        r = subprocess.run([ff, '-y', '-v', 'error', '-i', str(mp3),
                            '-ar', str(SR), '-ac', '2', str(tmp)],
                           capture_output=True, text=True, encoding='utf-8',
                           errors='replace')
        if r.returncode != 0:
            return None
        with wave.open(str(tmp), 'rb') as w:
            raw = w.readframes(w.getnframes())
        return np.frombuffer(raw, dtype='<i2').astype(float).reshape(-1, 2) / 32767.0
    finally:
        tmp.unlink(missing_ok=True)


def make_reel(mp3s):
    """试听串烧：全部 v2 曲目间隔 1 秒拼一条，人耳验收用。"""
    gap = np.zeros((SR, 2))
    parts = []
    for p in mp3s:
        x = load_audio(p)
        if x is None:
            print(f'串烧跳过 {p.name}（解码失败）')
            continue
        parts += [x, gap]
    if not parts:
        print('串烧：没有可拼的曲目')
        return
    reel = np.vstack(parts[:-1])
    out = OUT_DIR / 'preview_v2_reel.mp3'
    with tempfile.NamedTemporaryFile(suffix='.wav', delete=False) as fh:
        wav_path = pathlib.Path(fh.name)
    write_wav(wav_path, reel[:, 0], reel[:, 1], SR)
    ok = ffmpeg_encode(wav_path, out, bitrate='160k')
    wav_path.unlink(missing_ok=True)
    if ok:
        print(f"试听串烧 preview_v2_reel.mp3 ✓（{len(reel) / SR:.0f}s）")


def main() -> int:
    for stream in (sys.stdout, sys.stderr):
        if hasattr(stream, 'reconfigure'):
            stream.reconfigure(errors='replace')  # cp936 下 ✓/中文不炸
    problems = toolchain()
    if problems:
        print('工具链缺件：')
        for p in problems:
            print('  - ' + p)
        return 1
    force = '--force' in sys.argv
    OUT_DIR.mkdir(parents=True, exist_ok=True)
    rendered = []
    for tr in TRACKS:
        mp3, dur = render_track(tr, force)
        if dur is not None:
            update_manifest(tr, dur)
            rendered.append((tr, mp3, dur))
        elif mp3.exists():
            rendered.append((tr, mp3, None))  # 已存在的也进串烧
    n_ok = sum(1 for _, _, d in rendered if d is not None)
    print(f"曲库清单 tracks.json ✓（本次渲染 {n_ok}，v2 共 {len(TRACKS)} 首）")
    if '--reel' in sys.argv and rendered:
        make_reel([mp3 for _, mp3, _ in rendered])
    return 0


if __name__ == '__main__':
    sys.exit(main())
