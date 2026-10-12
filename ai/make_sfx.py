# -*- coding: utf-8 -*-
"""转场音效合成器（帧述，2026-09-28）。

程序化合成两枚转场 sfx（whoosh 段边界气流声 / riser 章节切换上扬），
确定性合成（固定种子的白噪声 + 解析包络，无真随机），ffmpeg 编码 mp3，
产物 + 清单落 data/library/media/audio/sfx/。stitch 阶段按段边界延迟混入。

与 make_bgm 同一套哲学：免版权曲库留档麻烦，程序化生成版权自持；
sfx 在成片里只做段间的"空气感"缝合，音量常驻人声之下，合成垫底够用。

用法：python ai/make_sfx.py   （幂等：产物存在则跳过，--force 重生成）
"""
import json
import math
import pathlib
import shutil
import sys
import tempfile

import numpy as np

# normalize / write_wav / ffmpeg_encode 收拢进 av_common（与 make_bgm 同源，
# subprocess/wave 也随迁，这里不再直接用）
from av_common import ffmpeg_encode, normalize, write_wav

SR = 44100
OUT_DIR = pathlib.Path(__file__).resolve().parent.parent / 'data' / 'library' / 'media' / 'audio' / 'sfx'

# 固定种子（MT19937 跨版本稳定）：同机重跑逐字节同产物
RNG = np.random.RandomState(42)


def white_noise(n):
    return RNG.uniform(-1.0, 1.0, n)


def high_pass(x):
    """一阶差分高通：滤掉白噪声的低频轰，留'气流感'。"""
    return np.diff(x, prepend=x[:1])


def edge_fade(x, ms=24):
    k = min(len(x) - 1, int(SR * ms / 1000))
    if k > 0:
        ramp = 0.5 - 0.5 * np.cos(np.pi * np.arange(k) / k)
        x[:k] *= ramp
        x[-k:] *= ramp[::-1]
    return x


def synth_whoosh(dur=0.6):
    """段边界气流声：高通噪声 + 呼吸包络（升-降），左右 3ms 错位出宽度，
    底层补一条 400→120Hz 正弦下滑增加体感。"""
    n = int(dur * SR)
    t = np.arange(n) / SR
    env = np.sin(np.pi * t / dur) ** 1.6          # 中间最响
    breath = 1.0 + 0.28 * np.sin(2 * math.pi * 7.0 * t)  # 7Hz 轻微呼吸调制
    air = high_pass(white_noise(n)) * env * breath
    sweep_f = 400 * (120 / 400) ** (t / dur)      # 指数下滑
    body = 0.30 * np.sin(2 * math.pi * np.cumsum(sweep_f) / SR) * env
    mono = air * 0.8 + body
    d = int(0.003 * SR)                            # 3ms 左右错位 → 立体声宽度
    L = mono.copy()
    R = np.concatenate([np.zeros(d), mono[:-d]])
    L, R = edge_fade(L), edge_fade(R)
    return L, R


def synth_riser(dur=1.2):
    """章节上扬：两个八度的指数上滑（160→640Hz）+ 噪声涌起，末尾 60ms 收干净。"""
    n = int(dur * SR)
    t = np.arange(n) / SR
    f0 = 160 * 2 ** (2.0 * t / dur)                # 160 → 640Hz
    env = (t / dur) ** 1.8                          # 越到后面越响
    sine = np.sin(2 * math.pi * np.cumsum(f0) / SR) * env
    shimmer = 0.22 * np.sin(2 * math.pi * np.cumsum(f0 * 2) / SR) * env
    swell = high_pass(white_noise(n)) * (env ** 1.4) * 0.5
    mono = sine * 0.8 + shimmer + swell
    L = mono.copy()
    R = np.concatenate([np.zeros(int(0.002 * SR)), mono[:-int(0.002 * SR)]])
    L, R = edge_fade(L), edge_fade(R)
    k = int(0.060 * SR)                             # 末尾快速收束（不滴答）
    tail = np.linspace(1, 0, k) ** 0.5
    L[-k:] *= tail
    R[-k:] *= tail
    return L, R


CLIPS = [
    {'name': 'whoosh', 'duration': 0.6, 'synth': synth_whoosh,
     'desc': '段边界气流声（fade 转场缝合）'},
    {'name': 'riser', 'duration': 1.2, 'synth': synth_riser,
     'desc': '章节切换上扬（大转折预告）'},
]


def main() -> int:
    # Go 侧管道捕获不设 PYTHONUTF8，中文 Windows 默认 cp936 严格解码——✓/✗ 不在 GBK 中，
    # 成功路径的 print 反而炸成 rc=1。errors='replace' 保输出永远可写。
    for stream in (sys.stdout, sys.stderr):
        if hasattr(stream, 'reconfigure'):
            stream.reconfigure(errors='replace')
    force = '--force' in sys.argv
    OUT_DIR.mkdir(parents=True, exist_ok=True)
    manifest = []
    for c in CLIPS:
        mp3 = OUT_DIR / f"{c['name']}.mp3"
        if mp3.exists() and not force:
            print(f"{c['name']}: 已存在，跳过（--force 重生成）")
        else:
            print(f"{c['name']}: 合成中…")
            L, R = normalize(*c['synth'](c['duration']), peak=0.5)
            with tempfile.NamedTemporaryFile(suffix='.wav', delete=False) as fh:
                wav_path = pathlib.Path(fh.name)
            try:
                write_wav(wav_path, L, R, SR)
                if not ffmpeg_encode(wav_path, mp3, bitrate='192k'):
                    shutil.move(str(wav_path), mp3.with_suffix('.wav'))  # 无 ffmpeg 留 wav
                    print(f"{c['name']}: ffmpeg 缺失，留 wav ✓")
                    manifest.append({'name': c['name'], 'file': mp3.with_suffix('.wav').name,
                                     'duration': c['duration'], 'desc': c['desc']})
                    continue
            finally:
                wav_path.unlink(missing_ok=True)  # 异常中断也不留临时 wav 尸体
            # 历史 fallback 残留清理：早先无 ffmpeg 时期留过同名 .wav，mp3 成功后删掉
            mp3.with_suffix('.wav').unlink(missing_ok=True)
            print(f"{c['name']}: {mp3.stat().st_size // 1024}KB ✓")
        manifest.append({'name': c['name'], 'file': mp3.name,
                         'duration': c['duration'], 'desc': c['desc']})
    (OUT_DIR / 'sfx.json').write_text(
        json.dumps(manifest, ensure_ascii=False, indent=1), encoding='utf-8')
    print(f"sfx 清单 sfx.json ✓（{len(manifest)} 枚）")
    return 0


if __name__ == '__main__':
    sys.exit(main())
