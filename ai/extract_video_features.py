# -*- coding: utf-8 -*-
"""参考视频特征抽取（视频风格解析第一步，确定性部分，不碰 LLM）。

用法：python ai/extract_video_features.py <video_path> <outdir> [--start S] [--end E]
产物 <outdir>/features.json：
  { video, duration_s, span_s, n_shots(镜头数=硬切点+1), n_cuts, cuts,
    frames: [{path, t}], palette: [{hex, share, lum}] }
另产 <outdir>/sheet-00.png：全部关键帧拼网格（每格带 #序号 与时间戳角标），
供解析智能体的结构判断阶段一张图看懂全片。

流程：ffmpeg 场景切分取镜头时间点（可限定 --start/--end 时间段）→ 按时长自适应
抽 ≤36 个关键帧 640px PNG → k-means 取色（8 簇：hex + 覆盖率 + 亮度，角色分配
交给 Go 侧解析智能体）。
"""
import argparse
import json
import math
import os
import pathlib
import re
import shutil
import subprocess
import sys

import numpy as np
from PIL import Image, ImageDraw

N_CLUSTERS = 8
SHEET_COLS, SHEET_ROWS = 6, 5


def _ff(name: str) -> str:
    # which 解析：Windows 上自动命中 ffprobe.exe/ffmpeg.exe，PATH 优先；
    # FFMPEG_BIN 环境变量可指向便携版 bin 目录（与 Go 侧 fontface/pipeline 同名同义）
    p = shutil.which(name)
    if p:
        return p
    bindir = os.environ.get('FFMPEG_BIN', '')
    if bindir:
        cand = pathlib.Path(bindir) / name
        if cand.exists():
            return str(cand)
    ext = '.exe' if sys.platform == 'win32' else ''
    return name + ext


def probe_duration(video: pathlib.Path) -> float:
    out = subprocess.run([_ff('ffprobe'), '-v', 'error', '-show_entries',
                          'format=duration', '-of', 'csv=p=0', str(video)],
                         capture_output=True, text=True, encoding='utf-8', errors='replace',
                         check=True)
    try:
        return float(out.stdout.strip())
    except ValueError:
        # 损坏/非媒体文件 ffprobe 可能空串输出——裸 float() 只会甩 ValueError
        # traceback，给一行能定位的原因
        raise SystemExit(f'✗ ffprobe 读不到时长({video}): '
                         f'{(out.stderr or out.stdout).strip()[:200] or "无输出"}')


def max_frames(duration: float) -> int:
    """自适应帧数：12 帧≈35s 内看全，更长视频按 ~3s/帧 加密，上限 60（网格 6×5×2 张）。"""
    return min(60, max(12, round(duration / 3)))


def scene_times(video: pathlib.Path, duration: float, start: float, end: float, cap: int) -> tuple[list[float], list[float]]:
    """场景切分：showinfo 的 pts_time 即镜头起点。失败/无切换回退均匀采样。
    只看 [start, end] 时间段（合集拆段后逐段抽帧用）。
    返回 (抽帧时间点, 全量硬切点)——硬切点是拆段智能体的边界候选信号。"""
    try:
        # 旧版用 -vsync vfr，ffmpeg 7+ 已移除该选项（Unrecognized option →
        # 场景检测静默退化均匀采样）；select 过滤后默认 passthrough 已是
        # 所需行为，直接不传，全版本兼容。
        # -ss/-t 限定解码窗口：段模式下本函数被调 K 次，不限窗=K 次全片解码；
        # 输入 seeking 会把时间轴归零（实测 pts_time 变为相对 seek 点），解析出的
        # 切点统一加回 start 折算成绝对时刻（全片模式 start=0 不受影响）
        out = subprocess.run([_ff('ffmpeg'), '-ss', f'{start:.3f}', '-i', str(video),
                              '-t', f'{max(end - start, 0):.3f}',
                              '-vf', "select='gt(scene,0.25)',showinfo",
                              '-f', 'null', '-'],
                             capture_output=True, text=True,
                             encoding='utf-8', errors='replace', timeout=600)
        cuts = sorted({start + float(m.group(1))
                       for m in re.finditer(r'pts_time:([0-9.]+)', out.stderr)})
    except Exception as e:
        # 降级前留告警：裸吞会让「全片无硬切」与「检测炸了」在下游不可分辨
        print(f'警告: 场景检测失败，回退均匀采样: {str(e)[:160]}')
        cuts = []
    cuts = [t for t in cuts if start + 0.3 < t < end - 0.3][:400]
    ts = list(cuts)
    span = end - start
    if span >= duration - 1 and len(ts) < cap:
        # 全片模式：硬切点稀疏时（叠化过渡检测不到）按 ~4s/帧 均匀补足，保证覆盖密度
        n_even = cap - len(ts)
        even = [start + span * (i + 0.5) / n_even for i in range(n_even)]
        ts = sorted(set(round(t, 2) for t in ts + even))
    if len(ts) < 3:  # 长镜头/动画渐变类：均匀兜底
        n = min(cap, max(3, int(span // 4)))
        ts = [start + span * i / (n + 1) for i in range(n)]
    if len(ts) > cap:  # 镜头太多 → 均匀抽（硬切点列表不裁，另作信号）
        step = len(ts) / cap
        ts = [ts[int(i * step)] for i in range(cap)]
    return [start] + ts, cuts


def grab_frame(video: pathlib.Path, t: float, out_png: pathlib.Path):
    subprocess.run([_ff('ffmpeg'), '-y', '-v', 'error', '-ss', f'{t:.2f}',
                    '-i', str(video), '-frames:v', '1', '-vf', 'scale=640:-2', str(out_png)],
                   check=True, timeout=120)


def _nearest(data, cent):
    """每个像素到各质心的最近簇号（距离平方，无须开根）。"""
    d = ((data[:, None, :] - cent[None, :, :]) ** 2).sum(-1)
    return d.argmin(1)


def _kmeans(data, k, n_init=4, iters=30, seed=42):
    """轻量确定性 k-means：固定种子、n_init 轮取 SSE 最优（对齐原 sklearn 版语义）。
    只为这一处聚类拖 sklearn+scipy 进镜像不值当，numpy 十几行够用。"""
    rng = np.random.default_rng(seed)
    best = None  # (sse, cent, lab)
    for _ in range(n_init):
        cent = data[rng.choice(len(data), size=k, replace=False)].copy()
        for _ in range(iters):
            lab = _nearest(data, cent)
            new = cent.copy()
            for j in range(k):
                m = lab == j
                if m.any():
                    new[j] = data[m].mean(0)
                else:  # 空簇：吸收离自己质心最远的点（拉走最刺眼的离群簇）
                    dmin = ((data - cent[j]) ** 2).sum(-1)
                    new[j] = data[dmin.argmax()]
            done = np.allclose(new, cent, atol=0.01)
            cent = new
            if done:
                break
        lab = _nearest(data, cent)
        sse = float(((data - cent[lab]) ** 2).sum())
        if best is None or sse < best[0]:
            best = (sse, cent, lab)
    return best[1], best[2]


def palette(frames: list[pathlib.Path]) -> list[dict]:
    """k-means 色板：帧降采样像素聚类，按覆盖率排序。"""
    px = []
    for f in frames:
        im = Image.open(f).convert('RGB').resize((80, 45))
        px.append(np.asarray(im).reshape(-1, 3))
    data = np.concatenate(px).astype(np.float32)
    cent, lab = _kmeans(data, N_CLUSTERS)
    out = []
    for j, c in enumerate(cent):
        share = float((lab == j).mean())
        r, g, b = (int(round(v)) for v in c)
        lum = round((0.2126 * r + 0.7152 * g + 0.0722 * b) / 255, 3)
        out.append({'hex': f'#{r:02X}{g:02X}{b:02X}', 'share': round(share, 3), 'lum': lum})
    return sorted(out, key=lambda c: -c['share'])


def make_sheets(frames: list[dict], outdir: pathlib.Path) -> list[str]:
    """关键帧拼网格：每张 ≤30 格（6 列×5 行），每格 320×180 + 「#序号 t=秒」角标。
    返回文件名列表（sheet-00.png, sheet-01.png, …）。"""
    cell_w, cell_h, bar = 320, 180, 22
    per = SHEET_ROWS * SHEET_COLS
    names = []
    for page in range(0, len(frames), per):
        chunk = frames[page:page + per]
        rows = math.ceil(len(chunk) / SHEET_COLS)
        sheet = Image.new('RGB', (SHEET_COLS * cell_w, rows * (cell_h + bar)), (16, 16, 20))
        dr = ImageDraw.Draw(sheet)
        for i, f in enumerate(chunk):
            im = Image.open(f['path']).convert('RGB').resize((cell_w, cell_h))
            cx, cy = (i % SHEET_COLS) * cell_w, (i // SHEET_COLS) * (cell_h + bar)
            sheet.paste(im, (cx, cy + bar))
            dr.rectangle([cx, cy, cx + cell_w, cy + bar], fill=(16, 16, 20))
            dr.text((cx + 6, cy + 4), f"#{f['idx']}  t={f['t']:.1f}s", fill=(240, 240, 240))
        name = f'sheet-{page // per:02d}.png'
        sheet.save(outdir / name)
        names.append(name)
    return names


def main() -> int:
    # Go 侧管道捕获不设 PYTHONUTF8，中文 Windows 默认 cp936 严格解码——✓/✗ 不在 GBK 中，
    # 成功路径的 print 反而炸成 rc=1。errors='replace' 保输出永远可写。
    for stream in (sys.stdout, sys.stderr):
        if hasattr(stream, 'reconfigure'):
            stream.reconfigure(errors='replace')
    ap = argparse.ArgumentParser()
    ap.add_argument('video')
    ap.add_argument('outdir')
    ap.add_argument('--start', type=float, default=0.0, help='限定时间段起点（段模式）')
    ap.add_argument('--end', type=float, default=None, help='限定时间段终点（段模式）')
    ap.add_argument('--cap', type=int, default=0, help='强制抽帧上限（0=自动：全片自适应/段模式 4）')
    a = ap.parse_args()
    video, outdir = pathlib.Path(a.video), pathlib.Path(a.outdir)
    outdir.mkdir(parents=True, exist_ok=True)
    if not video.exists():
        print(f'视频不存在: {video}')
        return 1

    duration = probe_duration(video)
    end = duration if a.end is None else min(a.end, duration)
    cap = a.cap if a.cap > 0 else (max_frames(duration) if end - a.start >= duration - 1 else 4)
    times, cuts = scene_times(video, duration, a.start, end, cap)
    frames = []
    for i, t in enumerate(times):
        png = outdir / f'frame-{i:02d}.png'
        try:
            grab_frame(video, t, png)
        except Exception as e:
            # 半途失败：把已抽的帧一并清掉，不留残缺帧集给下游拼网格/取色误用
            for f in frames:
                pathlib.Path(f['path']).unlink(missing_ok=True)
            png.unlink(missing_ok=True)
            raise SystemExit(f'✗ 抽帧失败 t={t:.2f}s（{video}）: {e}')
        frames.append({'path': str(png.resolve()), 't': round(t, 2), 'idx': i})

    sheets = make_sheets(frames, outdir)

    feats = {
        'video': str(video.resolve()),
        'duration_s': round(duration, 1),
        'span_s': [round(a.start, 2), round(end, 2)],
        # n_shots = 段内镜头数（硬切点+1）：Go 侧 styleref 把它当「N 个镜头」注入
        # 提示词，旧值 len(times) 是抽帧点数（受 cap 采样上限约束），不是镜头数。
        # 场景检测失败的降级路径 cuts=[] → 记 1（=未检出硬切）
        'n_shots': len(cuts) + 1,
        # 原始硬切信号单列一份，与抽帧点/镜头数解耦（消费方按需取）
        'n_cuts': len(cuts),
        'cuts': [round(t, 2) for t in cuts],
        'sheets': sheets,
        'frames': frames,
        'palette': palette([pathlib.Path(f['path']) for f in frames]),
    }
    (outdir / 'features.json').write_text(
        json.dumps(feats, ensure_ascii=False, indent=1), encoding='utf-8')
    print(f'features: {len(frames)} 帧 / {len(sheets)} 张网格 / 硬切 {len(cuts)} 点 / '
          f'{a.start:.0f}-{end:.0f}s / 色板 {len(feats["palette"])} 簇')
    return 0


if __name__ == '__main__':
    sys.exit(main())
