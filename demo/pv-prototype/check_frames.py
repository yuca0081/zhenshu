# -*- coding: utf-8 -*-
"""镜头帧非空机械检查（音乐驱动 PV · 原型闸）——终审机械侧。

原型闸首轮教训：单点采样会踩中"入场首帧=空底"的合法瞬态（分镜承诺如此），
亮像素 >200 的阈值对中亮度配色全漏报；而真伤（重触发动效把透明度归零，
小节边界整帧纯色）是 σ=0 抓到的，和刻意极简镜头的 σ≈10 差一个数量级。
所以本检查：每镜头 3 个采样点（入场后/中段/收尾前，避开边界），
σ<3 判空=硬伤，σ<15 记 LOW 只报不拦（极简设计不是伤），亮占比仅参考。

用法：py -3 demo/pv-prototype/check_frames.py
"""
import json
import pathlib
import subprocess
import sys

import numpy as np

HERE = pathlib.Path(__file__).resolve().parent


def frame_gray(video, t):
    r = subprocess.run(
        ['ffmpeg', '-v', 'error', '-ss', str(t), '-i', str(video),
         '-frames:v', '1', '-f', 'rawvideo', '-pix_fmt', 'gray', '-'],
        capture_output=True)
    return np.frombuffer(r.stdout, dtype=np.uint8)


def main() -> int:
    for stream in (sys.stdout, sys.stderr):
        if hasattr(stream, 'reconfigure'):
            stream.reconfigure(errors='replace')
    spec = json.loads((HERE / 'shots.json').read_text(encoding='utf-8'))
    video = HERE / 'pv_final.mp4'
    bad = []
    for s in spec['shots']:
        d = s['t1'] - s['t0']
        base = s['t0'] - spec['meta']['segment']['start']
        for label, off in (('in', 0.5), ('mid', d / 2), ('out', d - 0.5)):
            g = frame_gray(video, base + off)
            std = float(g.std()) if len(g) == 1080 * 1920 else -1.0
            state = 'EMPTY' if std < 3 else ('LOW' if std < 15 else 'OK')
            if state == 'EMPTY':
                bad.append(f"{s['id']}@{label}(+{off}s) σ={std:.1f}")
            print(f"{s['id']} {label:3s} σ={std:6.1f} "
                  f"亮占比={(g > 150).mean() * 100:5.1f}% {state}")
    if bad:
        print('非空检查 FAIL：' + '; '.join(bad))
        return 1
    print(f"非空机械检查 PASS（{len(spec['shots'])} 镜头 × 3 采样点全有结构）")
    return 0


if __name__ == '__main__':
    sys.exit(main())
