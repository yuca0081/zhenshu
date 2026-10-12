# -*- coding: utf-8 -*-
"""make_bgm / make_sfx 共用的音频底座：归一化、wav 落盘、ffmpeg 编码。
两边只差 peak 与码率，全部走参数——改动只改这一份。"""
import shutil
import subprocess
import wave

import numpy as np


def normalize(L, R, peak):
    """双声道归一到指定峰值。"""
    m = max(np.abs(L).max(), np.abs(R).max()) or 1.0
    return L / m * peak, R / m * peak


def write_wav(path, L, R, sr):
    """16bit 立体声 PCM 落盘。"""
    pcm = (np.clip(np.stack([L, R], axis=1), -1, 1) * 32767).astype('<i2')
    with wave.open(str(path), 'wb') as w:
        w.setnchannels(2)
        w.setsampwidth(2)
        w.setframerate(sr)
        w.writeframes(pcm.tobytes())


def ffmpeg_encode(wav_path, mp3_path, bitrate):
    """wav → mp3（libmp3lame）。ffmpeg 缺失或编码失败回 False。"""
    ff = shutil.which('ffmpeg')
    if not ff:
        return False
    # 显式 utf-8 解码：中文 Windows 默认 cp936 严格解码，ffmpeg 报错回显 UTF-8
    # 内容时会抛 UnicodeDecodeError，把可降级的失败炸成崩溃
    r = subprocess.run([ff, '-y', '-v', 'error', '-i', str(wav_path),
                        '-codec:a', 'libmp3lame', '-b:a', bitrate, str(mp3_path)],
                       capture_output=True, text=True, encoding='utf-8', errors='replace')
    return r.returncode == 0
