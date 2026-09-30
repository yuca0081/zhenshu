# -*- coding: utf-8 -*-
"""参考视频特征抽取（视频风格解析第一步，确定性部分，不碰 LLM）。

用法：python ai/extract_video_features.py <video_path> <outdir>
产物 <outdir>/features.json：
  { video, duration_s, n_shots, frames: [{path, t}], palette: [{hex, share, lum}] }

流程：ffmpeg 场景切分取镜头时间点 → 均匀选 ≤12 个关键帧抽 640px PNG
→ k-means 全局取色（8 簇：hex + 覆盖率 + 亮度，角色分配交给 Go 侧 LLM）。
"""
import json
import os
import pathlib
import re
import shutil
import subprocess
import sys

import numpy as np
from PIL import Image
from sklearn.cluster import KMeans

MAX_FRAMES = 12
N_CLUSTERS = 8


def _ff(name: str) -> str:
    # which 解析：Windows 上自动命中 ffprobe.exe/ffmpeg.exe，PATH 优先；
    # FFBIN 环境变量可指向便携版 bin 目录（不再硬编码他人机器的绝对路径）
    p = shutil.which(name)
    if p:
        return p
    bindir = os.environ.get('FFBIN', '')
    if bindir:
        cand = pathlib.Path(bindir) / name
        if cand.exists():
            return str(cand)
    ext = '.exe' if sys.platform == 'win32' else ''
    return name + ext


def probe_duration(video: pathlib.Path) -> float:
    out = subprocess.run([_ff('ffprobe'), '-v', 'error', '-show_entries',
                          'format=duration', '-of', 'csv=p=0', str(video)],
                         capture_output=True, text=True, check=True)
    return float(out.stdout.strip())


def scene_times(video: pathlib.Path, duration: float) -> list[float]:
    """场景切分：showinfo 的 pts_time 即镜头起点。失败/无切换回退均匀采样。"""
    try:
        # 旧版用 -vsync vfr，ffmpeg 7+ 已移除该选项（Unrecognized option →
        # 场景检测静默退化均匀采样）；select 过滤后默认 passthrough 已是
        # 所需行为，直接不传，全版本兼容
        out = subprocess.run([_ff('ffmpeg'), '-i', str(video),
                              '-vf', "select='gt(scene,0.25)',showinfo",
                              '-f', 'null', '-'], capture_output=True, text=True, timeout=600)
        ts = sorted({float(m.group(1)) for m in re.finditer(r'pts_time:([0-9.]+)', out.stderr)})
    except Exception:
        ts = []
    ts = [t for t in ts if 0.5 < t < duration - 0.5]
    if len(ts) < 3:  # 长镜头/动画渐变类：均匀兜底
        n = min(MAX_FRAMES, max(4, int(duration // 4)))
        ts = [duration * i / n for i in range(1, n)]
    if len(ts) > MAX_FRAMES:  # 镜头太多 → 均匀抽
        step = len(ts) / MAX_FRAMES
        ts = [ts[int(i * step)] for i in range(MAX_FRAMES)]
    return [0.0] + ts


def grab_frame(video: pathlib.Path, t: float, out_png: pathlib.Path):
    subprocess.run([_ff('ffmpeg'), '-y', '-v', 'error', '-ss', f'{t:.2f}',
                    '-i', str(video), '-frames:v', '1', '-vf', 'scale=640:-2', str(out_png)],
                   check=True, timeout=120)


def palette(frames: list[pathlib.Path]) -> list[dict]:
    """全局 k-means 色板：全帧降采样像素聚类，按覆盖率排序。"""
    px = []
    for f in frames:
        im = Image.open(f).convert('RGB').resize((80, 45))
        px.append(np.asarray(im).reshape(-1, 3))
    data = np.concatenate(px).astype(float)
    km = KMeans(n_clusters=N_CLUSTERS, n_init=4, random_state=42).fit(data)
    out = []
    for i, c in enumerate(km.cluster_centers_):
        share = float((km.labels_ == i).mean())
        r, g, b = (int(round(v)) for v in c)
        lum = round((0.2126 * r + 0.7152 * g + 0.0722 * b) / 255, 3)
        out.append({'hex': f'#{r:02X}{g:02X}{b:02X}', 'share': round(share, 3), 'lum': lum})
    return sorted(out, key=lambda c: -c['share'])


def main(video_arg: str, outdir_arg: str) -> int:
    video = pathlib.Path(video_arg)
    outdir = pathlib.Path(outdir_arg)
    outdir.mkdir(parents=True, exist_ok=True)
    if not video.exists():
        print(f'视频不存在: {video}')
        return 1

    duration = probe_duration(video)
    times = scene_times(video, duration)
    frames = []
    for i, t in enumerate(times):
        png = outdir / f'frame-{i:02d}.png'
        grab_frame(video, t, png)
        frames.append({'path': str(png.resolve()), 't': round(t, 2)})

    feats = {
        'video': str(video.resolve()),
        'duration_s': round(duration, 1),
        'n_shots': len(times),
        'frames': frames,
        'palette': palette([pathlib.Path(f['path']) for f in frames]),
    }
    (outdir / 'features.json').write_text(
        json.dumps(feats, ensure_ascii=False, indent=1), encoding='utf-8')
    print(f'features: {len(frames)} 帧 / {duration:.0f}s / 色板 {len(feats["palette"])} 簇')
    return 0


if __name__ == '__main__':
    sys.exit(main(sys.argv[1], sys.argv[2]))
