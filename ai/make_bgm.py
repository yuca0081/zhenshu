# -*- coding: utf-8 -*-
"""内置 BGM 曲库生成器（帧述，2026-09-26）。

程序化合成三首免版权氛围垫乐（和弦 pad + 低音 + 轻pluck），ffmpeg 编码 mp3，
产物 + 曲库清单落 data/library/media/audio/。全部确定性（无随机），循环无缝（包络
按循环周期构造，收尾窗口回卷）。

为什么不放现成音乐：免版权曲库授权留档麻烦；程序化生成版权自持（自用 1.0），
音色做"氛围垫"够用——成片里它被闪避压到人声之下 -15dB 左右，只负责氛围。

用法：python ai/make_bgm.py   （幂等：产物存在则跳过，--force 重生成）
"""
import json
import math
import pathlib
import shutil
import struct
import subprocess
import sys
import tempfile
import wave

import numpy as np

SR = 44100
OUT_DIR = pathlib.Path(__file__).resolve().parent.parent / 'data' / 'library' / 'media' / 'audio'

# ── 乐理 ─────────────────────────────────────────────────────

NOTE_SEMI = {'C': 0, 'D': 2, 'E': 4, 'F': 5, 'G': 7, 'A': 9, 'B': 11}


def freq(note):
    """'C3'/'F#4' → 频率（A4=440，十二平均律）。"""
    name, octv = note[:-1], int(note[-1])
    semi = NOTE_SEMI[name[0]] + (1 if '#' in name else 0)
    midi = (octv + 1) * 12 + semi
    return 440.0 * 2 ** ((midi - 69) / 12)


# ── 合成 ─────────────────────────────────────────────────────

def chord_env(n, slot, cf):
    """第 k 个和弦的包络（样本域）：在区间两端 cf 样本的互补升余弦，
    相邻和弦包络和恒为 1 → 无缝循环。返回 (N, slots) 矩阵。"""
    t = np.arange(n)
    env = np.zeros((len(slot), n))
    for k in range(len(slot) - 1):
        s, e = slot[k], slot[k + 1]
        rise = np.clip((t - (s - cf)) / cf, 0, 1)
        fall = np.clip(((e + cf) - t) / cf, 0, 1)
        env[k] = np.minimum(1.0, np.minimum(rise, fall))
    # 平滑 + 循环回卷：首和弦的起音窗包住末和弦的收音窗（时间回卷取模）
    env = 0.5 - 0.5 * np.cos(np.pi * env)
    return env


def synth_pad(notes, slot_s, cf_s, bright=1.0, sub_amp=0.5, detune=0.004):
    """和弦 pad：每音两层失谐正弦（L/R 各偏一点 → 立体声宽）+ 3 次泛音 +
    低八度 sub。返回 (L, R)。包络按循环周期构造 → 首尾无缝。"""
    total = sum(slot_s)
    n = int(round(total * SR))
    slot = [int(round(s * SR)) for s in np.cumsum([0] + list(slot_s))]
    env = chord_env(n, slot, int(cf_s * SR))
    L = np.zeros(n)
    R = np.zeros(n)
    for k, chord in enumerate(notes):
        seg = env[k]
        for ni, note in enumerate(chord):
            f0 = freq(note)
            amp = 0.9 / len(chord) * (1.0 if ni else 1.15)  # 根音略抬
            for mult, ha in ((1.0, 1.0), (2.0, 0.22 * bright), (3.0, 0.07 * bright)):
                ph = 2 * math.pi * ni * 0.13  # 固定相位差防完全同相叠加
                L += amp * ha * seg * np.sin(2 * np.pi * f0 * mult * (1 - detune) * np.arange(n) / SR + ph)
                R += amp * ha * seg * np.sin(2 * np.pi * f0 * mult * (1 + detune) * np.arange(n) / SR + ph)
            # 低八度 sub（只根音）
            if ni == 0:
                fs = f0 / 2
                L += sub_amp * seg * np.sin(2 * np.pi * fs * np.arange(n) / SR)
                R += sub_amp * seg * np.sin(2 * np.pi * fs * np.arange(n) / SR + 0.5)
    return L, R


def add_pluck(L, R, chord_notes, slot_s, bpm, amp=0.14):
    """明快曲目叠加的软 pluck 琶音：八分音符循和弦音上行，指数衰减，
    尾音越循环边界回卷（无缝）。"""
    total = sum(slot_s)
    n = int(round(total * SR))
    step = 60.0 / bpm / 2  # 八分音符
    ring = int(1.6 * SR)
    seq = [note for bar in chord_notes for note in bar]  # 平铺循环
    k = 0
    t = 0.0
    while t < total - 0.01:
        note = seq[k % len(seq)]
        f0 = freq(note) * 2  # 高八度
        i0 = int(round(t * SR))
        m = np.arange(ring)
        decay = np.exp(-m / (SR * 0.28))
        tone = (np.sin(2 * np.pi * f0 * m / SR) + 0.35 * np.sin(2 * np.pi * f0 * 2 * m / SR)) * decay
        pan = 0.5 + 0.22 * math.sin(k * 1.7)  # 确定性左右摆动
        idx = (i0 + m) % n  # 回卷 → 循环无缝
        np.add.at(L, idx, amp * (1 - pan) * 2 * tone)
        np.add.at(R, idx, amp * pan * 2 * tone)
        k += 1
        t += step


def normalize(L, R, peak=0.55):
    m = max(np.abs(L).max(), np.abs(R).max()) or 1.0
    return L / m * peak, R / m * peak


def write_wav(path, L, R):
    data = np.stack([L, R], axis=1)
    pcm = (np.clip(data, -1, 1) * 32767).astype('<i2')
    with wave.open(str(path), 'wb') as w:
        w.setnchannels(2)
        w.setsampwidth(2)
        w.setframerate(SR)
        w.writeframes(pcm.tobytes())


def ffmpeg_encode(wav_path, mp3_path):
    ff = shutil.which('ffmpeg')
    if not ff:
        return False
    r = subprocess.run([ff, '-y', '-v', 'error', '-i', str(wav_path),
                        '-codec:a', 'libmp3lame', '-b:a', '160k', str(mp3_path)],
                       capture_output=True, text=True)
    return r.returncode == 0


# ── 曲目定义 ─────────────────────────────────────────────────

TRACKS = [
    {
        'name': 'calm',
        'mood': '温暖平静的和弦垫——通用叙事/口播，默认推荐',
        'chords': [['C3', 'E3', 'G3', 'B3'], ['A2', 'E3', 'G3', 'C4'],
                   ['F2', 'A3', 'C4', 'E4'], ['G2', 'B3', 'D4', 'E4']],
        'slot': 12.0, 'bright': 1.0, 'pluck': None,
    },
    {
        'name': 'bright',
        'mood': '明快轻亮带琶音——产品介绍/轻松话题/新事物',
        'chords': [['D3', 'F#3', 'A3', 'C#4'], ['B2', 'D3', 'F#3', 'A3'],
                   ['G2', 'B3', 'D4', 'F#4'], ['A2', 'C#4', 'E4', 'A4']],
        'slot': 8.0, 'bright': 1.35, 'pluck': 88,
    },
    {
        'name': 'deep',
        'mood': '低沉深邃的暗色垫——科技/悬疑/硬核知识',
        'chords': [['A2', 'E3', 'B3', 'C4'], ['F2', 'C3', 'E3', 'A3'],
                   ['D2', 'A2', 'F3', 'C4'], ['E2', 'B2', 'G#3', 'B3']],
        'slot': 12.0, 'bright': 0.55, 'pluck': None,
    },
    {
        # M-C 扩充（2026-09-28）：按情绪补三首——史诗推进/神秘悬浮/温暖人文
        'name': 'epic',
        'mood': '小调史诗推进（低音沉重和弦进行）——历史/战争/宏大叙事/星球宇宙',
        'chords': [['A2', 'E3', 'A3', 'C4'], ['F2', 'C3', 'F3', 'A3'],
                   ['C3', 'G3', 'C4', 'E4'], ['G2', 'D3', 'G3', 'B3']],
        'slot': 16.0, 'bright': 0.7, 'pluck': 60,
    },
    {
        'name': 'mysterious',
        'mood': '神秘悬浮暗垫（减和弦色彩）——谜题/未解之谜/心理/暗黑题材',
        'chords': [['D2', 'A2', 'C3', 'F#3'], ['B1', 'F#2', 'B2', 'E3'],
                   ['G2', 'D3', 'F3', 'B3'], ['A2', 'E3', 'G3', 'C4']],
        'slot': 14.0, 'bright': 0.45, 'pluck': None,
    },
    {
        'name': 'warm',
        'mood': '温暖人文大调慢琶音——人物故事/传记/教育/公益',
        'chords': [['C3', 'G3', 'E4', 'B4'], ['G2', 'D3', 'B3', 'G4'],
                   ['A2', 'E3', 'C4', 'G4'], ['F2', 'C3', 'A3', 'F4']],
        'slot': 10.0, 'bright': 0.95, 'pluck': 66,
    },
    {
        # M-C 第三批（2026-09-28）：情绪再补三首——悲情/欢快/怀旧
        'name': 'melancholy',
        'mood': '悲情小调慢垫（Am 空和声）——离别/灾难/悲伤叙事/悼念',
        'chords': [['A2', 'E3', 'A3', 'C4'], ['E2', 'B2', 'G3', 'B3'],
                   ['F2', 'C3', 'A3', 'C4'], ['G2', 'D3', 'B3', 'D4']],
        'slot': 12.0, 'bright': 0.6, 'pluck': None,
    },
    {
        'name': 'upbeat',
        'mood': '欢快大调快琶音——美食/生活/旅行/萌宠/开箱',
        'chords': [['G2', 'D3', 'B3', 'D4'], ['D3', 'F#3', 'A3', 'D4'],
                   ['E3', 'G3', 'B3', 'E4'], ['C3', 'E3', 'G3', 'C4']],
        'slot': 6.0, 'bright': 1.4, 'pluck': 108,
    },
    {
        'name': 'nostalgic',
        'mood': '怀旧暖色七和弦——历史回顾/老照片/人物回忆/时代变迁',
        'chords': [['C3', 'E3', 'G3', 'B3'], ['A2', 'E3', 'G3', 'C4'],
                   ['F2', 'A3', 'C4', 'E4'], ['G2', 'B3', 'D4', 'F4']],
        'slot': 11.0, 'bright': 0.75, 'pluck': 56,
    },
]


def main() -> int:
    force = '--force' in sys.argv
    OUT_DIR.mkdir(parents=True, exist_ok=True)
    manifest = []
    for tr in TRACKS:
        mp3 = OUT_DIR / f"{tr['name']}.mp3"
        if mp3.exists() and not force:
            print(f"{tr['name']}: 已存在，跳过（--force 重生成）")
        else:
            print(f"{tr['name']}: 合成中…")
            slots = [tr['slot']] * len(tr['chords'])
            L, R = synth_pad(tr['chords'], slots, cf_s=3.0, bright=tr['bright'])
            if tr['pluck']:
                add_pluck(L, R, tr['chords'], slots, tr['pluck'])
            L, R = normalize(L, R)
            with tempfile.NamedTemporaryFile(suffix='.wav', delete=False) as fh:
                wav_path = pathlib.Path(fh.name)
            write_wav(wav_path, L, R)
            ok = ffmpeg_encode(wav_path, mp3)
            if not ok:  # 无 ffmpeg：改留 wav（Chromium 照播）
                wav_asset = mp3.with_suffix('.wav')
                shutil.move(str(wav_path), wav_asset)
                manifest.append({'name': tr['name'], 'file': wav_asset.name,
                                 'duration': tr['slot'] * len(tr['chords']), 'mood': tr['mood']})
                print(f"{tr['name']}: ffmpeg 缺失，留 wav ✓")
                continue
            wav_path.unlink(missing_ok=True)
            print(f"{tr['name']}: {mp3.stat().st_size // 1024}KB ✓")
        manifest.append({'name': tr['name'], 'file': mp3.name,
                         'duration': tr['slot'] * len(tr['chords']), 'mood': tr['mood']})
    (OUT_DIR / 'tracks.json').write_text(
        json.dumps(manifest, ensure_ascii=False, indent=1), encoding='utf-8')
    print(f"曲库清单 tracks.json ✓（{len(manifest)} 首）")
    return 0


if __name__ == '__main__':
    sys.exit(main())
