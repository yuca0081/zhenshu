# -*- coding: utf-8 -*-
"""全片音频：配音摆位 + 程序化配乐/音效 + 混音。
输出 build/mix.wav（48k stereo s16），再经 ffmpeg loudnorm -> aac。
"""
import json, os, subprocess, wave
import numpy as np
from scipy import signal as sg

BUILD = os.path.join(os.path.dirname(__file__), "..", "build")
SR = 48000

tl = json.load(open(os.path.join(BUILD, "timeline.json"), encoding="utf-8"))
TOTAL = tl["total"]
N = int((TOTAL + 0.5) * SR)
rng = np.random.default_rng(42)


def load_voice_chunk(mp3):
    """mp3 -> 48k mono float32"""
    wav = mp3.replace(".mp3", "_48.wav")
    if not os.path.exists(wav):
        subprocess.run(["ffmpeg", "-y", "-loglevel", "error", "-i", mp3,
                        "-ar", str(SR), "-ac", "1", "-sample_fmt", "s16", wav],
                       check=True)
    with wave.open(wav, "rb") as w:
        data = np.frombuffer(w.readframes(w.getnframes()), dtype=np.int16)
    return data.astype(np.float32) / 32768.0


def build_voice():
    v = np.zeros(N, np.float32)
    segs = json.load(open(os.path.join(BUILD, "segments.json"), encoding="utf-8"))
    for sc, rec in zip(tl["scenes"], segs):
        assert sc["id"] == rec["id"]
        for ch, crec in zip(sc["chunks"], rec["chunks"]):
            mp3 = os.path.join(BUILD, crec["mp3"])
            x = load_voice_chunk(mp3)
            i0 = int((sc["start"] + ch["t0"]) * SR)
            i1 = min(i0 + len(x), N)
            if i1 > i0:
                v[i0:i1] += x[:i1 - i0]
    return v


# ---------------- 配乐/音效 ----------------
def note_wave(freq, dur, harmonics=((1, 1.0), (2, 0.35), (3, 0.12)), detune=0.0015):
    t = np.arange(int(dur * SR)) / SR
    out = np.zeros_like(t, np.float32)
    for k, a in harmonics:
        out += a * np.sin(2 * np.pi * freq * k * (1 + detune) * t).astype(np.float32)
        out += a * np.sin(2 * np.pi * freq * k * (1 - detune) * t).astype(np.float32)
    return out * 0.5


CHORDS = [  # (根音, [音高频率])  Am9 / Fmaj7 / Cmaj7 / G6
    (55.0, [110.0, 164.81, 246.94, 261.63, 329.63]),
    (43.65, [87.31, 174.61, 220.0, 261.63, 329.63]),
    (65.41, [130.81, 164.81, 196.0, 261.63, 329.63]),
    (49.0, [98.0, 146.83, 196.0, 246.94, 293.66]),
]


def build_music():
    L = np.zeros(N, np.float32); R = np.zeros(N, np.float32)
    chord_len, xfade = 8.0, 3.0
    n_ch = int(np.ceil(TOTAL / chord_len)) + 1
    env_full = np.ones(int(chord_len * SR), np.float32)
    a = int(2.5 * SR); r = int(3.0 * SR)
    env_full[:a] = np.linspace(0, 1, a) ** 2
    env_full[-r:] *= np.linspace(1, 0, r) ** 1.5
    for ci in range(n_ch):
        i0 = int(ci * chord_len * SR)
        if i0 >= N:
            break
        root, notes = CHORDS[ci % 4]
        wL = np.zeros(int(chord_len * SR), np.float32)
        wR = np.zeros_like(wL)
        for f in notes:
            wL += note_wave(f, chord_len, detune=+0.0016) / len(notes)
            wR += note_wave(f, chord_len, detune=-0.0016) / len(notes)
        sub = np.sin(2 * np.pi * root * np.arange(len(wL)) / SR).astype(np.float32)
        i1 = min(i0 + len(wL), N)
        seg = slice(i0, i1)
        L[seg] += (wL * 0.075 * env_full)[:i1 - i0]
        R[seg] += (wR * 0.075 * env_full)[:i1 - i0]
        L[seg] += (sub * 0.028 * env_full)[:i1 - i0]
        R[seg] += (sub * 0.028 * env_full)[:i1 - i0]
    # 开头淡入 / 结尾淡出（配合画面）
    fi = int(2.5 * SR); fo = int(4.0 * SR)
    ramp_i = np.linspace(0, 1, fi, dtype=np.float32)
    ramp_o = np.linspace(1, 0, fo, dtype=np.float32)
    L[:fi] *= ramp_i; R[:fi] *= ramp_i
    L[-fo:] *= ramp_o; R[-fo:] *= ramp_o
    return L, R


def add_bell(L, R, t, freq, amp=0.05, dur=2.6, pan=0.0):
    i0 = int(t * SR)
    if i0 >= N: return
    tt = np.arange(min(int(dur * SR), N - i0)) / SR
    b = (np.sin(2 * np.pi * freq * tt)
         + 0.45 * np.sin(2 * np.pi * freq * 2.01 * tt)
         + 0.22 * np.sin(2 * np.pi * freq * 2.94 * tt))
    b *= np.exp(-tt * 2.2) * amp
    L[i0:i0 + len(b)] += b * (1 - pan) / 2 + b * 0.5
    R[i0:i0 + len(b)] += b * (1 + pan) / 2 + b * 0.5


def add_geiger(L, R, t0, t1, lam0=0.8, lam1=2.4):
    t = t0
    while t < t1:
        lam = lam0 + (lam1 - lam0) * (t - t0) / max(0.1, (t1 - t0))
        t += rng.exponential(1.0 / lam)
        if t >= t1: break
        i0 = int(t * SR)
        dur = int(0.05 * SR)
        if i0 + dur >= N: break
        noise = rng.standard_normal(dur).astype(np.float32)
        env = np.exp(-np.arange(dur) / (0.006 * SR))
        click = sg.lfilter(*sg.butter(2, [1200 / (SR / 2), 7500 / (SR / 2)], "bandpass"),
                           noise * env) * 0.16
        pan = rng.uniform(-0.6, 0.6)
        L[i0:i0 + dur] += click * (1 - pan)
        R[i0:i0 + dur] += click * (1 + pan)


def add_boom(L, R, t):
    i0 = int(t * SR); dur = int(1.6 * SR)
    if i0 + dur >= N: return
    tt = np.arange(dur) / SR
    f = 110 * np.exp(-tt * 1.9) + 36
    phase = 2 * np.pi * np.cumsum(f) / SR
    b = np.sin(phase).astype(np.float32) * np.exp(-tt * 2.1) * 0.34
    thump = sg.lfilter(*sg.butter(2, 220 / (SR / 2), "low"),
                       rng.standard_normal(dur).astype(np.float32) * np.exp(-tt * 16)) * 0.22
    b = b + thump
    L[i0:i0 + dur] += b; R[i0:i0 + dur] += b


def add_whoosh(L, R, t):
    dur = int(0.9 * SR); i0 = int(t * SR)
    if i0 < 0 or i0 + dur >= N: return
    noise = rng.standard_normal(dur).astype(np.float32)
    noise = sg.lfilter(*sg.butter(2, 900 / (SR / 2), "low"), noise)
    env = np.hanning(dur) ** 1.6
    w = noise * env * 0.055
    L[i0:i0 + dur] += w; R[i0:i0 + dur] += w


def add_arp(L, R, t):
    for k, f in enumerate((880.0, 1046.5, 1318.5, 1760.0)):
        add_bell(L, R, t + k * 0.07, f, amp=0.032, dur=1.2, pan=0.5 - k * 0.25)


def main():
    ev = tl["events"]
    v = build_voice()
    L, R = build_music()
    add_bell(L, R, ev["bell_title"], 660.0, amp=0.055)
    for k, bt in enumerate(tl["bells"]):
        add_bell(L, R, bt, (523.25, 587.33, 659.25, 783.99, 880.0)[k % 5],
                 amp=0.042, pan=0.35 if k % 2 else -0.35)
    for wt in tl["whooshes"]:
        add_whoosh(L, R, wt)
    add_geiger(L, R, ev["geiger_start"], ev["geiger_end"])
    add_boom(L, R, ev["boom"])
    add_arp(L, R, ev["split_arp"])

    # 混音：配音为主
    vp = np.max(np.abs(v)) or 1.0
    v = v * (0.72 / vp)
    L = L + v; R = R + v
    mix = np.stack([L, R], axis=1)
    peak = np.max(np.abs(mix)) or 1.0
    if peak > 0.97:
        mix *= 0.97 / peak
    pcm = (np.clip(mix, -1, 1) * 32767).astype(np.int16)
    out = os.path.join(BUILD, "mix.wav")
    with wave.open(out, "wb") as w:
        w.setnchannels(2); w.setsampwidth(2); w.setframerate(SR)
        w.writeframes(pcm.tobytes())
    print("mix.wav", pcm.shape[0] / SR, "s, peak", round(float(peak), 3))


if __name__ == "__main__":
    main()
