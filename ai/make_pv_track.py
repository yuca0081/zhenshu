# -*- coding: utf-8 -*-
"""PV 整曲作曲原型（音乐驱动 PV · 第 0 步原型闸）——M1「曲库整曲改造」的种子。

与 make_bgm_v2（33–60s 循环垫）的分岔点：这里做的是**整曲**——多段落结构、
能量弧线、from/until 双边声部进出，并从**谱面直出音乐地图 JSON**（节拍网格/
段落/能量），DSP 只当校验（verify_beats.py），不当地图来源——卡点精度天花板
= 谱面拍点精确到毫秒，DSP 只有帧精度。

乐理与渲染底座全部复用 make_bgm_v2（parse_chord/voicing/wobble/toolchain/
FluidSynth 调用），本文件只新写：段落化编曲器 + 谱面地图导出 + 自检环。
自检环 = 音乐工位「地图对 brief 数字比对」的机械版：目标 BPM/时长带/能量弧
断言，不满足非零退出——agent 只调工具不手改数据，工具自己保证账对得上。

用法：python ai/make_pv_track.py [--out demo/pv-prototype]
产物：<out>/pv_proto_am120.mp3 + <out>/music_map.json
"""
import json
import pathlib
import subprocess
import sys
import tempfile
import wave

import numpy as np

from av_common import ffmpeg_encode, write_wav
from make_bgm_v2 import (FLUIDSYNTH, SR, SOUNDFONT, bass_note, expand_prog,
                          toolchain, voicing, wobble)

HERE = pathlib.Path(__file__).resolve().parent

# ── brief（创作意图，自检环的比对基准）───────────────────────
BRIEF = {
    'name': 'pv_proto_am120',
    'bpm': 120,                      # 1 拍 = 0.5s，1 小节 = 2s，拍点数学干净
    'bars': 40,                      # 40 小节 × 2s = 80s（时长带 60–90s）
    'dur_band': (60.0, 90.0),
    'prog': [('Am', 2), ('F', 2), ('C', 2), ('G', 2)],   # 四小节一轮 × 10
    'key': 'Am',
}

# 段落编排：bars=[起,止) 小节号；energy 1–5 同时驱动声部密度与力度。
# 副歌能量必须显著高于 verse（自检断言），break 回落制造对比——分镜的
# 「切段判据=音乐段落」就吃这张表。
SECTIONS = [
    dict(name='intro',  bars=(0, 4),   energy=1,
         layers=[('pad',  dict(inst=89, vel=44)),
                 ('bass', dict(inst=32, vel=40, grid='x-------', dur=3.6))]),
    dict(name='verse',  bars=(4, 12),  energy=2,
         layers=[('pad',  dict(inst=89, vel=48)),
                 ('arp',  dict(inst=0, vel=56, rate=0.5)),
                 ('bass', dict(inst=32, vel=52, grid='x---x---', dur=1.8)),
                 ('drums', dict(kick='x-------', hat='----x---'))]),
    dict(name='build',  bars=(12, 16), energy=3,
         layers=[('pad',  dict(inst=89, vel=52)),
                 ('grid', dict(inst=0, vel=58, grid='x---x---x---x---', stac=0.5)),
                 ('arp',  dict(inst=0, vel=52, rate=0.25, shift=12)),
                 ('bass', dict(inst=33, vel=58, grid='x---x---', dur=1.8)),
                 ('drums', dict(kick='x---x---x---x---', snare='----x-------x---',
                                hat='--x---x---x---x-')),
                 ('roll', dict())]),          # 末小节军鼓渐强滚奏（进副歌的台阶）
    dict(name='chorus', bars=(16, 24), energy=5,
         layers=[('pad',  dict(inst=48, vel=56)),
                 ('grid', dict(inst=0, vel=66, grid='x--x--x-x--x--x-', stac=0.5,
                               shift=12)),
                 ('arp',  dict(inst=9, vel=48, rate=0.5, shift=24)),
                 ('bass', dict(inst=33, vel=70, grid='x-x-x-x-x-x-x-x-')),
                 ('drums', dict(kick='x---x---x---x---', snare='----x-------x---',
                                hat='--x---x---x---x-', open='------x-------x-',
                                tamb='x-x-x-x-x-x-x-x-'))]),
    dict(name='break',  bars=(24, 28), energy=3,
         layers=[('pad',  dict(inst=89, vel=52)),
                 ('arp',  dict(inst=0, vel=48, rate=0.5)),
                 ('bass', dict(inst=32, vel=46, grid='x-------', dur=3.6)),
                 ('drums', dict(rim='x-------x-------', shaker='--x---x---x---x-'))]),
    dict(name='chorus2', bars=(28, 36), energy=5,
         layers=[('pad',  dict(inst=48, vel=56)),
                 ('grid', dict(inst=0, vel=68, grid='x--x--x-x--x--x-', stac=0.5,
                               shift=12)),
                 ('arp',  dict(inst=9, vel=50, rate=0.25, shift=24)),
                 ('bass', dict(inst=33, vel=72, grid='x-x-x-x-x-x-x-x-')),
                 ('drums', dict(kick='x---x---x---x---', snare='----x-------x---',
                                hat='--x---x---x---x-', open='------x-------x-',
                                tamb='x-x-x-x-x-x-x-x-', ride='x---x---x---x---'))]),
    dict(name='outro',  bars=(36, 40), energy=2,
         layers=[('pad',  dict(inst=89, vel=46)),
                 ('grid', dict(inst=0, vel=44, grid='x-------', stac=0.9)),
                 ('bass', dict(inst=32, vel=46, grid='x-------', dur=3.6))]),
]

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
    print(f"music_map.json ✓（{len(secs)} 段，{len(beats)} 拍格，"
          f"副歌 {secs[3]['start']}s–{secs[3]['end']}s）")
    return out


# ── 自检环（音乐工位 agent 的机械版：地图对 brief 数字比对）───

def self_check(brief, sections, mp):
    errs = []
    if mp['bpm'] != brief['bpm']:
        errs.append(f"BPM {mp['bpm']} ≠ brief {brief['bpm']}")
    lo, hi = brief['dur_band']
    if not (lo <= mp['score_duration'] <= hi):
        errs.append(f"时长 {mp['score_duration']:.0f}s 不在带 [{lo:.0f},{hi:.0f}]")
    chorus = [s for s in mp['sections'] if s['name'].startswith('chorus')]
    verse = [s for s in mp['sections'] if s['name'] == 'verse']
    if not chorus or max(s['energy'] for s in chorus) < 5:
        errs.append('副歌能量未到 5——能量弧立不起来')
    if chorus and verse and max(s['energy'] for s in chorus) - verse[0]['energy'] < 3:
        errs.append('副歌-主歌能量差 <3，段落对比不足')
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
    out_arg = '--out'
    out_dir = HERE.parent / 'demo' / 'pv-prototype'
    if out_arg in sys.argv:
        out_dir = pathlib.Path(sys.argv[sys.argv.index(out_arg) + 1])
    out_dir.mkdir(parents=True, exist_ok=True)

    mf = build_midi(BRIEF, SECTIONS)
    mp3, dur = render(BRIEF, mf, out_dir)
    mp = export_map(BRIEF, SECTIONS, dur, out_dir / 'music_map.json')

    errs = self_check(BRIEF, SECTIONS, mp)
    if errs:
        print('自检环 FAIL（brief ↔ 地图对不上）：')
        for e in errs:
            print('  - ' + e)
        return 2
    print('自检环 PASS：BPM/时长带/副歌能量弧/渲染时长 全对上')
    return 0


if __name__ == '__main__':
    sys.exit(main())
