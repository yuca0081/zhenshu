# -*- coding: utf-8 -*-
"""节拍/起始点分析 → beatmap.json（确定性：同输入同输出）。

谱通量 onset 检测 + 自相关 tempo 估计 + 相位网格对齐。
用法：py -3 demo/agi-mv-repro/analyze_audio.py
"""
import json
import pathlib
import wave

import numpy as np

HERE = pathlib.Path(__file__).resolve().parent
SR = 22050
HOP = 512
NFFT = 1024


def load_wav(path):
    with wave.open(str(path), 'rb') as w:
        assert w.getframerate() == SR and w.getnchannels() == 1
        data = np.frombuffer(w.readframes(w.getnframes()), dtype=np.int16)
    return data.astype(np.float64) / 32768.0


def spectral_flux(x):
    n = (len(x) - NFFT) // HOP
    win = np.hanning(NFFT)
    prev = None
    flux = np.zeros(n)
    frames = np.lib.stride_tricks.as_strided(
        x, shape=(n, NFFT), strides=(x.strides[0] * HOP, x.strides[0])).copy()
    spec = np.abs(np.fft.rfft(frames * win, axis=1))
    diff = np.diff(spec, axis=0)
    flux[1:] = diff.clip(min=0).sum(axis=1)
    return flux


def smooth(v, k):
    ker = np.hanning(k)
    ker /= ker.sum()
    return np.convolve(v, ker, mode='same')


def pick_onsets(flux, thresh_loose=1.05, min_gap_ms=90):
    med = np.median(flux)
    mad = np.median(np.abs(flux - med)) * 1.4826
    thr = med + 1.6 * mad
    min_gap = int(SR / HOP * min_gap_ms / 1000)
    onsets = []
    last = -min_gap
    for i in range(2, len(flux) - 2):
        if flux[i] >= thr and flux[i] == max(flux[i - 2:i + 3]) and i - last >= min_gap:
            onsets.append(i)
            last = i
    return onsets, thr


def estimate_bpm(flux, onsets):
    fps = SR / HOP
    env = smooth(flux - np.mean(flux), 9)
    env = env.clip(min=0)
    ac = np.correlate(env, env, mode='full')[len(env):]
    # 找 60-190 BPM 对应的滞后峰
    lo, hi = int(fps * 60 / 190), int(fps * 60 / 60)
    lag = lo + int(np.argmax(ac[lo:hi]))
    bpm = 60.0 * fps / lag
    # 精化：滞后附近抛物线插值
    if 1 <= lag - 1 and lag + 1 < len(ac):
        a, b, c = ac[lag - 1], ac[lag], ac[lag + 1]
        d = 0.5 * (a - c) / (a - 2 * b + c + 1e-12)
        bpm = 60.0 * fps / (lag + d)
    # 倍/半速消歧：autocorrelation 常锁错倍频，用 onset 贴合度裁决
    cands = sorted({round(bpm / 2, 2), round(bpm, 2), round(bpm * 2, 2)})
    scored = [(onset_score(c, flux, onsets), c) for c in cands if 60 <= c <= 190]
    scored.sort(reverse=True)
    return scored[0][1]


def onset_score(bpm, flux, onsets=None):
    """节拍网格与 onset 的贴合度（归一命中量）——用于倍速消歧。"""
    fps = SR / HOP
    period = 60.0 / bpm
    ons = [i / fps for i in (onsets if onsets is not None else [])]
    if not ons:
        return 0.0
    best = -1
    for k in range(24):
        phase = period * k / 24
        s = 0.0
        for o in ons:
            m = (o - phase) % period
            d = min(m, period - m)
            if d < 0.06:
                s += 1 - d / 0.06
        best = max(best, s)
    return best / len(ons)


def beat_grid(bpm, flux, onsets):
    """以 onset 加权对齐节拍相位：扫 8 个相位偏移取 onset 命中总分。"""
    period = 60.0 / bpm
    fps = SR / HOP
    ons = [i / fps for i in onsets]
    best, best_score = 0.0, -1
    for k in range(24):
        phase = period * k / 24
        score = 0.0
        for o in ons:
            m = (o - phase) % period
            d = min(m, period - m)
            if d < 0.06:
                score += 1 - d / 0.06
        if score > best_score:
            best_score, best = score, phase
    beats = []
    t = best
    while t < len(flux) * HOP / SR:
        beats.append(round(t, 3))
        t += period
    return beats, period, best


def main():
    x = load_wav(HERE / 'song.wav')
    dur = len(x) / SR
    flux = spectral_flux(x)
    onsets, thr = pick_onsets(flux)
    bpm = estimate_bpm(flux, onsets)
    beats, period, phase = beat_grid(bpm, flux, onsets)
    fps = SR / HOP
    out = {
        'duration': round(dur, 3),
        'bpm': round(bpm, 2),
        'beat_period': round(period, 4),
        'phase': round(phase, 4),
        'onsets': [round(i / fps, 3) for i in onsets],
        'beats': beats,
    }
    (HERE / 'beatmap.json').write_text(
        json.dumps(out, ensure_ascii=False), encoding='utf-8')
    print(f"时长 {dur:.1f}s  BPM≈{bpm:.1f}  onset {len(onsets)} 个  beat {len(beats)} 格")


if __name__ == '__main__':
    main()
