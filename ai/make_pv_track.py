# -*- coding: utf-8 -*-
"""PV 整曲作曲工具（音乐驱动 PV · M1 音乐工位的做曲后端）。

与 make_bgm_v2（33–60s 循环垫）的分岔点：这里做的是**整曲**——多段落结构、
能量弧线、from/until 双边声部进出，并从**谱面直出音乐地图 JSON**（节拍网格/
段落/能量），DSP 只当校验（verify_beats.py），不当地图来源——卡点精度天花板
= 谱面拍点精确到毫秒，DSP 只有帧精度。

乐理与渲染底座全部复用 make_bgm_v2（parse_chord/voicing/wobble/toolchain/
FluidSynth 调用），本文件只新写：段落化编曲器 + 谱面地图导出 + 自检环。
自检环 = 音乐工位「地图对 brief 数字比对」的机械版：目标 BPM/时长带/能量弧
断言，不满足非零退出——agent 只调工具不手改数据，工具自己保证账对得上。

两条通道：
  python ai/make_pv_track.py [--out DIR]                # 原型默认曲（demo 用）
  python ai/make_pv_track.py --brief brief.json --out DIR  # 音乐工位工具通道
brief 是白名单词汇的编曲意图（段落名/能量/BPM/和弦全部圈死），本工具负责
brief→编曲参数的确定性映射（段落原型 × 能量缩放），agent 不碰音符层。

退出码：0 成功；1 工具链缺件；2 自检环未过（地图↔brief 对不上）；
3 brief 校验未过（词汇表/结构错误，逐条列出）。
产物：<out>/<name>.mp3 + <out>/music_map.json
"""
import json
import pathlib
import re
import subprocess
import sys
import tempfile
import wave

import numpy as np

from av_common import ffmpeg_encode, write_wav
from make_bgm_v2 import (FLUIDSYNTH, SR, SOUNDFONT, bass_note, expand_prog,
                          parse_chord, toolchain, voicing, wobble)

HERE = pathlib.Path(__file__).resolve().parent

# ── 白名单词汇表（brief 校验的唯一依据，Go 侧 lint 同源镜像）────
KEY_PROGS = {
    'Am': [('Am', 2), ('F', 2), ('C', 2), ('G', 2)],
    'C':  [('C', 2), ('G', 2), ('Am', 2), ('F', 2)],
    'Em': [('Em', 2), ('C', 2), ('G', 2), ('D', 2)],
    'G':  [('G', 2), ('D', 2), ('Em', 2), ('C', 2)],
    'Dm': [('Dm', 2), ('Bb', 2), ('F', 2), ('C', 2)],
    'F':  [('F', 2), ('C', 2), ('Dm', 2), ('Bb', 2)],
}
BPM_BAND = (84, 150)          # PV 常用带：太慢拖、太快糊
BARS_BAND = (16, 64)          # 84bpm×16 小节 ≈ 46s；150bpm×64 ≈ 102s
SEC_BARS_BAND = (2, 16)       # 单段长度带
DUR_BAND_DEFAULT = (45.0, 115.0)   # PV 整曲时长带（用户想法点名时长时 agent 据此选 bars×bpm）
SECTION_NAMES = ('intro', 'verse', 'verse2', 'build', 'chorus', 'chorus2',
                 'break', 'bridge', 'outro')

# ── 原型默认曲（无 --brief 时保持 demo/pv-prototype 原样）─────
BRIEF = {
    'name': 'pv_proto_am120',
    'bpm': 120,                      # 1 拍 = 0.5s，1 小节 = 2s，拍点数学干净
    'bars': 40,                      # 40 小节 × 2s = 80s（时长带 60–90s）
    'dur_band': (60.0, 90.0),
    'prog': [('Am', 2), ('F', 2), ('C', 2), ('G', 2)],   # 四小节一轮 × 10
    'key': 'Am',
}

# 段落原型：每个段落名一套声部编排（bars=[起,止) 由 brief 顺序累加；energy
# 1–5 同时驱动声部力度与鼓组力度）。e0=原型的标称能量——brief 给的能量偏离
# 标称时和声/贝斯力度 ±3/级 随动（clamp 20–100），鼓组在 build_midi 里 ±4/级。
# 副歌能量必须显著高于 verse（校验断言），break 回落制造对比——分镜的
# 「切段判据=音乐段落」就吃这张表。
_ARCH = {
    'intro':   dict(e0=1, layers=[
        ('pad',  dict(inst=89, vel=44)),
        ('bass', dict(inst=32, vel=40, grid='x-------', dur=3.6))]),
    'verse':   dict(e0=2, layers=[
        ('pad',  dict(inst=89, vel=48)),
        ('arp',  dict(inst=0, vel=56, rate=0.5)),
        ('bass', dict(inst=32, vel=52, grid='x---x---', dur=1.8)),
        ('drums', dict(kick='x-------', hat='----x---'))]),
    'build':   dict(e0=3, layers=[
        ('pad',  dict(inst=89, vel=52)),
        ('grid', dict(inst=0, vel=58, grid='x---x---x---x---', stac=0.5)),
        ('arp',  dict(inst=0, vel=52, rate=0.25, shift=12)),
        ('bass', dict(inst=33, vel=58, grid='x---x---', dur=1.8)),
        ('drums', dict(kick='x---x---x---x---', snare='----x-------x---',
                       hat='--x---x---x---x-')),
        ('roll', dict())]),          # 末小节军鼓渐强滚奏（进副歌的台阶）
    'chorus':  dict(e0=5, layers=[
        ('pad',  dict(inst=48, vel=56)),
        ('grid', dict(inst=0, vel=66, grid='x--x--x-x--x--x-', stac=0.5,
                      shift=12)),
        ('arp',  dict(inst=9, vel=48, rate=0.5, shift=24)),
        ('bass', dict(inst=33, vel=70, grid='x-x-x-x-x-x-x-x-')),
        ('drums', dict(kick='x---x---x---x---', snare='----x-------x---',
                       hat='--x---x---x---x-', open='------x-------x-',
                       tamb='x-x-x-x-x-x-x-x-'))]),
    'break':   dict(e0=3, layers=[
        ('pad',  dict(inst=89, vel=52)),
        ('arp',  dict(inst=0, vel=48, rate=0.5)),
        ('bass', dict(inst=32, vel=46, grid='x-------', dur=3.6)),
        ('drums', dict(rim='x-------x-------', shaker='--x---x---x---x-'))]),
    'bridge':  dict(e0=3, layers=[
        ('pad',  dict(inst=89, vel=50)),
        ('arp',  dict(inst=0, vel=50, rate=0.5, shift=12)),
        ('bass', dict(inst=33, vel=48, grid='x---x---', dur=1.8)),
        ('drums', dict(rim='x-------x-------', hat='--x---x---x---x-'))]),
    'outro':   dict(e0=2, layers=[
        ('pad',  dict(inst=89, vel=46)),
        ('grid', dict(inst=0, vel=44, grid='x-------', stac=0.9)),
        ('bass', dict(inst=32, vel=46, grid='x-------', dur=3.6))]),
}
_ARCH['verse2'] = _ARCH['verse']
_ARCH['chorus2'] = dict(e0=5, layers=[
    ('pad',  dict(inst=48, vel=56)),
    ('grid', dict(inst=0, vel=68, grid='x--x--x-x--x--x-', stac=0.5, shift=12)),
    ('arp',  dict(inst=9, vel=50, rate=0.25, shift=24)),
    ('bass', dict(inst=33, vel=72, grid='x-x-x-x-x-x-x-x-')),
    ('drums', dict(kick='x---x---x---x---', snare='----x-------x---',
                   hat='--x---x---x---x-', open='------x-------x-',
                   tamb='x-x-x-x-x-x-x-x-', ride='x---x---x---x---'))])

SECTIONS = [
    dict(name='intro',  bars=(0, 4),   energy=1, layers=_ARCH['intro']['layers']),
    dict(name='verse',  bars=(4, 12),  energy=2, layers=_ARCH['verse']['layers']),
    dict(name='build',  bars=(12, 16), energy=3, layers=_ARCH['build']['layers']),
    dict(name='chorus', bars=(16, 24), energy=5, layers=_ARCH['chorus']['layers']),
    dict(name='break',  bars=(24, 28), energy=3, layers=_ARCH['break']['layers']),
    dict(name='chorus2', bars=(28, 36), energy=5, layers=_ARCH['chorus2']['layers']),
    dict(name='outro',  bars=(36, 40), energy=2, layers=_ARCH['outro']['layers']),
]


# ── brief 装载与校验（退出码 3 的来源；Go 侧 prep/music.go 有同源 lint）──

def expected_dur(bpm, bars):
    return bars * 4 * 60.0 / bpm


def default_dur_band(bpm, bars):
    """brief 没点名时长带时按 bars×bpm 数学期望收窄——渲染时长是确定的，带只
    剩防手滑账错的作用。"""
    e = expected_dur(bpm, bars)
    return (e - 1.0, e + 1.0)


def load_brief(path):
    """读 + 校验 brief JSON。返回 (brief, sections, errs)：errs 非空即拒绝。"""
    try:
        raw = json.loads(pathlib.Path(path).read_text(encoding='utf-8'))
    except Exception as e:                                   # noqa: BLE001
        return None, None, [f'brief 文件读不了/不是 JSON：{e}']
    errs = []
    name = str(raw.get('name') or 'pv_track')
    if not re.fullmatch(r'[\w\-]{2,40}', name):
        errs.append(f'name {name!r} 只许 2–40 个字母/数字/下划线/连字符（它要当 mp3 文件名）')
    bpm = raw.get('bpm')
    if not isinstance(bpm, int) or not (BPM_BAND[0] <= bpm <= BPM_BAND[1]):
        errs.append(f'bpm 必须是 {BPM_BAND[0]}–{BPM_BAND[1]} 的整数，收到 {bpm!r}')
    bars = raw.get('bars')
    if not isinstance(bars, int) or not (BARS_BAND[0] <= bars <= BARS_BAND[1]):
        errs.append(f'bars 必须是 {BARS_BAND[0]}–{BARS_BAND[1]} 的整数，收到 {bars!r}')
    if errs:
        return None, None, errs

    key = str(raw.get('key') or 'Am')
    if 'prog' in raw and raw['prog']:
        prog = []
        for c in raw['prog']:
            try:
                sym, n = str(c['sym']), int(c['bars'])
            except Exception:                                # noqa: BLE001
                errs.append(f'prog 条目格式应为 {{"sym":"Am","bars":2}}，收到 {c!r}')
                continue
            try:
                parse_chord(sym)
            except ValueError as e:
                errs.append(f'和弦记号 {e}')
            if not 1 <= n <= 8:
                errs.append(f'和弦 {sym} 每轮占 {n} 小节，超出 1–8')
            prog.append((sym, n))
    elif key in KEY_PROGS:
        prog = list(KEY_PROGS[key])
    else:
        errs.append(f'key {key!r} 没有内置和弦进行（可选：{"、".join(KEY_PROGS)}），'
                    f'要么换 key 要么自己给 prog')
    if errs:
        return None, None, errs
    cycle = sum(n for _, n in prog)
    if cycle > bars:
        errs.append(f'和弦进行一轮 {cycle} 小节 > 全曲 {bars} 小节')
        return None, None, errs

    band = raw.get('dur_band')
    if band and (not isinstance(band, list) or len(band) != 2
                 or not all(isinstance(v, (int, float)) and v > 0 for v in band)
                 or band[0] >= band[1]):
        errs.append(f'dur_band 应为 [低, 高] 秒且低<高，收到 {band!r}')
    dur_band = tuple(float(v) for v in band) if band else DUR_BAND_DEFAULT
    exp = expected_dur(bpm, bars)
    if not (dur_band[0] <= exp <= dur_band[1]):
        errs.append(f'按 bars×bpm 算出的时长 {exp:.1f}s 落在 dur_band '
                    f'[{dur_band[0]:.0f},{dur_band[1]:.0f}] 之外——调 bars 或 bpm')

    secs = raw.get('sections')
    if not isinstance(secs, list) or not 2 <= len(secs) <= 10:
        errs.append(f'sections 要 2–10 段，收到 {len(secs) if isinstance(secs, list) else type(secs).__name__}')
        return None, None, errs
    total = 0
    energies = {}
    for s in secs:
        nm = s.get('name')
        if nm not in SECTION_NAMES:
            errs.append(f'段落名 {nm!r} 不在词汇表（可选：{"、".join(SECTION_NAMES)}）')
            continue
        b = s.get('bars')
        e = s.get('energy')
        if not isinstance(b, int) or not (SEC_BARS_BAND[0] <= b <= SEC_BARS_BAND[1]):
            errs.append(f'段落 {nm} 的 bars 要 {SEC_BARS_BAND[0]}–{SEC_BARS_BAND[1]}，收到 {b!r}')
        if not isinstance(e, int) or not 1 <= e <= 5:
            errs.append(f'段落 {nm} 的 energy 要 1–5 整数，收到 {e!r}')
            continue
        energies[nm] = max(energies.get(nm, 0), e)
        total += b if isinstance(b, int) else 0
    if total != bars:
        errs.append(f'各段 bars 合计 {total} ≠ 全曲 {bars}——段落要恰好铺满整曲')
    chorus = [v for k, v in energies.items() if k.startswith('chorus')]
    verse = [v for k, v in energies.items() if k.startswith('verse')]
    if not chorus:
        errs.append('缺副歌：至少要有一段 chorus/chorus2（PV 的能量锚点）')
    elif max(chorus) < 4:
        errs.append(f'副歌能量 {max(chorus)} < 4——能量弧立不起来')
    if chorus and verse and max(chorus) - max(verse) < 2:
        errs.append(f'副歌-主歌能量差 {max(chorus) - max(verse)} < 2，段落对比不足')
    if errs:
        return None, None, errs

    brief = dict(name=name, key=key, bpm=bpm, bars=bars, dur_band=dur_band,
                 prog=prog)
    return brief, sections_from_brief(brief, secs), []


def sections_from_brief(brief, secs):
    """brief.sections → 编曲段落表：段落原型 × 能量缩放（确定性映射，agent
    不碰音符层）。和声/贝斯/点缀力度随 (energy−e0)×3 随动，鼓组由 build_midi
    按 energy 出 ±4/级。"""
    out = []
    cur = 0
    for s in secs:
        arch = _ARCH[s['name']]
        layers = []
        for kind, kw in arch['layers']:
            kw = dict(kw)
            if 'vel' in kw:
                kw['vel'] = max(20, min(100, kw['vel'] + (s['energy'] - arch['e0']) * 3))
            layers.append((kind, kw))
        out.append(dict(name=s['name'], bars=(cur, cur + s['bars']),
                        energy=s['energy'], layers=layers))
        cur += s['bars']
    return out


# ── 段落化写入器（make_bgm_v2 的 add_* 是全曲铺满+from_bar 单边，这里要双边）──

def put_pad(mf, ch, table, off, vel):
    for bar, sym in enumerate(table):
        mf.addNote(0, ch, voicing(sym)[0] - 12, (off + bar) * 4, 4, vel)
        for n in voicing(sym):
            mf.addNote(0, ch, n, (off + bar) * 4, 4, wobble(vel, off + bar, 0))


def put_grid(mf, ch, table, off, grid, vel, stac=0.5, shift=0):
    for bar, sym in enumerate(table):
        tones = voicing(sym)
        for s, c in enumerate(grid):
            if c not in 'xo':
                continue
            v = vel + 12 if c == 'o' else vel
            mf.addNote(0, ch, tones[s % len(tones)] + shift,
                       (off + bar) * 4 + s * 0.25, 0.25 * stac,
                       wobble(v, off + bar, s))


def put_arp(mf, ch, table, off, vel, rate=0.5, pat=(0, 1, 2, 1, 3, 1, 2, 1),
            shift=0):
    k = 0
    for bar, sym in enumerate(table):
        tones = voicing(sym)
        t = (off + bar) * 4
        while t < (off + bar + 1) * 4 - 1e-9:
            mf.addNote(0, ch, tones[pat[k % len(pat)] % len(tones)] + shift,
                       t, rate * 0.9, wobble(vel, off + bar, k))
            t += rate
            k += 1


def put_bass(mf, table, off, grid, vel, dur=1.8):
    for bar, sym in enumerate(table):
        for s, c in enumerate(grid):
            if c == 'x':
                mf.addNote(0, 1, bass_note(sym), (off + bar) * 4 + s * 0.25,
                           dur, wobble(vel, off + bar, s))


GM_DRUM = {'kick': 36, 'snare': 38, 'rim': 37, 'hat': 42, 'open': 46,
           'ride': 51, 'tamb': 54, 'shaker': 70}
DRUM_VEL = {'kick': 80, 'snare': 64, 'rim': 50, 'hat': 40, 'open': 52,
            'ride': 42, 'tamb': 40, 'shaker': 32}


def put_drums(mf, grids, off, bars, vel_bias=0):
    for name, grid in grids.items():
        note = GM_DRUM[name]
        base = DRUM_VEL[name] + vel_bias
        for bar in range(bars):
            for s, c in enumerate(grid):
                if c in 'xo':
                    v = base + (12 if c == 'o' else 0)
                    mf.addNote(0, 9, note, (off + bar) * 4 + s * 0.25, 0.25,
                               wobble(v, off + bar, s))


def put_roll(mf, off, last_bar):
    """末小节 16 分军鼓渐强：vel 44→112 线性爬坡——进副歌前的台阶。"""
    bar = off + last_bar
    for s in range(16):
        v = int(44 + (112 - 44) * s / 15)
        mf.addNote(0, 9, 38, bar * 4 + s * 0.25, 0.25, v)


# ── 编曲：段落表 → MIDI ─────────────────────────────────────

def build_midi(brief, sections):
    import midiutil
    table = expand_prog(brief['prog'], brief['bars'])
    mf = midiutil.MIDIFile(1)
    mf.addTempo(0, 0, brief['bpm'])
    free_ch = iter([0, 2, 3, 4, 5, 6])
    ch_of = {}                       # 乐器号→通道：跨段同乐器必须同通道
    for sec in sections:
        b0, b1 = sec['bars']
        sub = table[b0:b1]
        e = sec['energy']
        vel_bias = (e - 3) * 4       # 能量进力度：±8 封顶
        for kind, kw in sec['layers']:
            if kind in ('pad', 'grid', 'arp'):
                if kw['inst'] not in ch_of:
                    ch_of[kw['inst']] = next(free_ch)
                    mf.addProgramChange(0, ch_of[kw['inst']], 0, kw['inst'])
                ch = ch_of[kw['inst']]
                if kind == 'pad':
                    put_pad(mf, ch, sub, b0, kw['vel'])
                elif kind == 'grid':
                    put_grid(mf, ch, sub, b0, kw['grid'], kw['vel'],
                             stac=kw.get('stac', 0.5), shift=kw.get('shift', 0))
                else:
                    put_arp(mf, ch, sub, b0, kw['vel'], rate=kw.get('rate', 0.5),
                            shift=kw.get('shift', 0))
            elif kind == 'bass':
                # 贝斯固定通道 1 但跨段换音色：ProgramChange 必须落在段落起点，
                # 写 t=0 会被最后一段的音色整轨覆盖（32/33 混装事故）
                mf.addProgramChange(0, 1, b0 * 4, kw['inst'])
                put_bass(mf, sub, b0, kw['grid'], kw['vel'],
                         dur=kw.get('dur', 1.8))
            elif kind == 'drums':
                put_drums(mf, kw, b0, b1 - b0, vel_bias)
            elif kind == 'roll':
                put_roll(mf, b0, b1 - b0 - 1)
            else:
                raise ValueError(f'未知声部类型 {kind!r}')
    return mf


def render(brief, mf, out_dir):
    """MIDI → FluidSynth → 归一 → 精确裁到曲长（PV 挂轨不许带混响尾巴）→ mp3。"""
    expect = brief['bars'] * 4 * 60.0 / brief['bpm']
    with tempfile.NamedTemporaryFile(suffix='.mid', delete=False) as fh:
        mid = pathlib.Path(fh.name)
    with tempfile.NamedTemporaryFile(suffix='.wav', delete=False) as fh:
        wav = pathlib.Path(fh.name)
    mp3 = out_dir / f"{brief['name']}.mp3"
    try:
        with open(mid, 'wb') as fh:
            mf.writeFile(fh)
        r = subprocess.run(
            [str(FLUIDSYNTH), '-ni', '-F', str(wav), '-r', str(SR),
             '-g', '0.7', str(SOUNDFONT), str(mid)],
            capture_output=True, text=True, encoding='utf-8', errors='replace')
        if r.returncode != 0 or not wav.exists():
            raise RuntimeError('FluidSynth FAIL: ' + (r.stderr or '')[-300:])
        with wave.open(str(wav), 'rb') as w:
            raw = w.readframes(w.getnframes())
            nch, sw, sr = (w.getnchannels(), w.getsampwidth(), w.getframerate())
        if nch != 2 or sw != 2 or sr != SR:
            raise RuntimeError(f'非预期 wav 格式 ch={nch} sw={sw} sr={sr}')
        x = np.frombuffer(raw, dtype='<i2').astype(float) / 32767.0
        x = x.reshape(-1, 2)
        peak = np.abs(x).max() or 1.0
        x = x / peak * 0.6
        keep = int(expect * SR)           # 精确曲长：视频与音轨同长是发令枪前提
        x = x[:keep]
        fade = int(SR * 0.4)              # 尾部 400ms 淡出（outro 本身已收）
        x[-fade:] *= np.linspace(1, 0, fade)[:, None]
        write_wav(wav, x[:, 0], x[:, 1], SR)
        if not ffmpeg_encode(wav, mp3, bitrate='192k'):
            raise RuntimeError('mp3 编码失败')
        print(f"{mp3.name}: {mp3.stat().st_size // 1024}KB，"
              f"{len(x) / SR:.2f}s（谱面期望 {expect:.2f}s）✓")
        return mp3, len(x) / SR
    finally:
        mid.unlink(missing_ok=True)
        wav.unlink(missing_ok=True)


# ── 谱面地图导出（分镜工位唯一认的物理事实）──────────────────

def export_map(brief, sections, dur, path):
    beat = 60.0 / brief['bpm']
    beats = [round(i * beat, 4) for i in range(brief['bars'] * 4 + 1)]
    downbeats = [round(b * 4 * beat, 4) for b in range(brief['bars'] + 1)]
    secs = []
    for sec in sections:
        b0, b1 = sec['bars']
        secs.append(dict(name=sec['name'], start_bar=b0, end_bar=b1,
                         start=round(b0 * 4 * beat, 4), end=round(b1 * 4 * beat, 4),
                         energy=sec['energy']))
    out = dict(name=brief['name'], key=brief['key'], bpm=brief['bpm'],
               beat_period=round(beat, 4), bar_seconds=round(4 * beat, 4),
               beats_per_bar=4, phase=0.0, duration=round(dur, 3),
               score_duration=round(brief['bars'] * 4 * beat, 3),
               beats=beats, downbeats=downbeats, sections=secs,
               energy_curve=[[s['start'], s['energy']] for s in secs],
               source='score-direct/midi-fluidsynth')
    path.write_text(json.dumps(out, ensure_ascii=False, indent=1),
                    encoding='utf-8')
    top = max(secs, key=lambda s: s['energy'])
    print(f"music_map.json ✓（{len(secs)} 段，{len(beats)} 拍格，"
          f"高潮「{top['name']}」{top['start']}s–{top['end']}s）")
    return out


# ── 自检环（音乐工位 agent 的机械版：地图对 brief 数字比对）───

def self_check(brief, sections, mp):
    errs = []
    if mp['bpm'] != brief['bpm']:
        errs.append(f"BPM {mp['bpm']} ≠ brief {brief['bpm']}")
    lo, hi = brief['dur_band']
    if not (lo <= mp['score_duration'] <= hi):
        errs.append(f"时长 {mp['score_duration']:.0f}s 不在带 [{lo:.0f},{hi:.0f}]")
    energies = [s['energy'] for s in mp['sections']]
    if not energies or max(energies) < 4:
        errs.append('最高段落能量 <4——能量弧立不起来')
    if energies and max(energies) - min(energies) < 2:
        errs.append('段落能量无对比（max−min <2）')
    if mp['score_duration'] != mp['duration']:
        errs.append(f"渲染时长 {mp['duration']} ≠ 谱面 {mp['score_duration']}")
    return errs


def main() -> int:
    for stream in (sys.stdout, sys.stderr):
        if hasattr(stream, 'reconfigure'):
            stream.reconfigure(errors='replace')
    problems = toolchain()
    if problems:
        print('工具链缺件：')
        for p in problems:
            print('  - ' + p)
        return 1
    out_dir = HERE.parent / 'demo' / 'pv-prototype'
    brief_path = None
    argv = sys.argv[1:]
    for i, a in enumerate(argv):
        if a == '--out' and i + 1 < len(argv):
            out_dir = pathlib.Path(argv[i + 1])
        elif a == '--brief' and i + 1 < len(argv):
            brief_path = argv[i + 1]
    out_dir.mkdir(parents=True, exist_ok=True)

    if brief_path:
        brief, sections, errs = load_brief(brief_path)
        if errs:
            print('brief 校验未过（白名单词汇/结构错误，逐条修）：')
            for e in errs:
                print('  - ' + e)
            return 3
    else:
        brief, sections = BRIEF, SECTIONS

    mf = build_midi(brief, sections)
    mp3, dur = render(brief, mf, out_dir)
    if brief_path:
        # 音乐工位通道：产物名固定 track.mp3（Go 侧 canonical 工件名），曲名只
        # 进地图不进文件名——创作名每次重做都会变，工件名变来变去下游没法认。
        fixed = out_dir / 'track.mp3'
        mp3.rename(fixed)
        mp3 = fixed
    mp = export_map(brief, sections, dur, out_dir / 'music_map.json')

    errs = self_check(brief, sections, mp)
    if errs:
        print('自检环 FAIL（brief ↔ 地图对不上）：')
        for e in errs:
            print('  - ' + e)
        return 2
    print('自检环 PASS：BPM/时长带/能量弧/渲染时长 全对上')
    return 0


if __name__ == '__main__':
    sys.exit(main())
