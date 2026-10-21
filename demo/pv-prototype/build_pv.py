# -*- coding: utf-8 -*-
"""拼接挂原曲（音乐驱动 PV · 原型闸）——mixBGM 简化版的最小实现。

文稿驱动 mixBGM 的三件事这里只留两件：音轨管道（ffmpeg 同源）、首尾淡出；
去掉的两件：闪避（人声没了，音乐是主角不是垫）、循环补长（曲长=片长，
发令枪保证对齐——分镜总长必须等于走链片段长，多一秒少一秒都是账错）。

流程：镜头 mp4 按序 concat（统一重编码防时间戳毛刺）→ 挂原曲对应片段
（-ss 定位、尾部 300ms 淡出）→ pv_final.mp4 + 账目核对打印。

用法：py -3 demo/pv-prototype/build_pv.py
"""
import json
import pathlib
import subprocess
import sys

HERE = pathlib.Path(__file__).resolve().parent


def run(cmd):
    r = subprocess.run(cmd, capture_output=True, text=True,
                       encoding='utf-8', errors='replace')
    if r.returncode != 0:
        raise RuntimeError(' '.join(cmd[:4]) + '… FAIL: ' + (r.stderr or '')[-400:])
    return r


def dur(p):
    r = run(['ffprobe', '-v', 'error', '-show_entries', 'format=duration',
             '-of', 'default=nw=1:nk=1', str(p)])
    return float(r.stdout.strip())


def main() -> int:
    for stream in (sys.stdout, sys.stderr):
        if hasattr(stream, 'reconfigure'):
            stream.reconfigure(errors='replace')
    spec = json.loads((HERE / 'shots.json').read_text(encoding='utf-8'))
    shots = spec['shots']
    seg = spec['meta']['segment']
    seg_dur = seg['end'] - seg['start']

    # 账目铁律：镜头总长 == 走链片段长（发令枪对齐的前提）
    total = sum(s['t1'] - s['t0'] for s in shots)
    if abs(total - seg_dur) > 1e-6:
        print(f'账目错：镜头总长 {total}s ≠ 片段长 {seg_dur}s')
        return 1

    # 1) concat：统一重编码（分别编码的 h264 直接 -c copy 时间戳不可靠）
    lst = HERE / 'concat.txt'
    lst.write_text('\n'.join(
        f"file '{(HERE / 'renders' / (s['id'] + '.mp4')).as_posix()}'"
        for s in shots), encoding='utf-8')
    silent = HERE / 'pv_silent.mp4'
    run(['ffmpeg', '-y', '-v', 'error', '-f', 'concat', '-safe', '0',
         '-i', str(lst), '-c:v', 'libx264', '-crf', '18', '-preset', 'medium',
         '-pix_fmt', 'yuv420p', str(silent)])
    vd = dur(silent)
    print(f"拼接 {len(shots)} 镜头 → pv_silent.mp4（{vd:.2f}s）✓")

    # 2) 挂原曲：定位片段 + 尾部淡出；-shortest 兜住音视频毫秒差
    mp = json.loads((HERE / 'music_map.json').read_text(encoding='utf-8'))
    track = HERE / f"{mp['name']}.mp3"
    final = HERE / 'pv_final.mp4'
    fade_st = seg_dur - 0.3
    run(['ffmpeg', '-y', '-v', 'error',
         '-i', str(silent),
         '-ss', str(seg['start']), '-t', str(seg_dur + 0.05), '-i', str(track),
         '-map', '0:v', '-map', '1:a',
         '-c:v', 'copy', '-c:a', 'aac', '-b:a', '192k',
         '-af', f'afade=t=out:st={fade_st:.3f}:d=0.3',
         '-shortest', str(final)])
    fd = dur(final)
    dev = abs(fd - seg_dur) * 1000
    print(f"挂原曲（{seg['start']}–{seg['end']}s，尾淡出 300ms）→ "
          f"pv_final.mp4（{fd:.2f}s，账面 {seg_dur}s，偏差 {dev:.0f}ms）✓")
    if dev > 120:
        print('终片时长偏差超 120ms——发令枪对齐存疑')
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
