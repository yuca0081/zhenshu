# -*- coding: utf-8 -*-
"""DSP 交叉验证：谱面直出的音乐地图 vs 实听分析（音乐驱动 PV · 原型闸）。

架构立场：地图来源=谱面（毫秒级精确），DSP 只当**校验器**。首轮实测教训
（已入缺口清单）：裸自相关 BPM 有 ±2–3% 误差＋八度混淆（本曲真值 120，
估出 122.9——整数帧滞后处两座等高峰，真峰卡在帧缝里被插值带歪），固定网格
逐拍比对会累积出 1.7s 假漂移。所以本验证器**锚定谱面先验**：

  主判据 = onset 对谱面 16 分网格的贴合度（mean/p95 毫秒）——直接回答
  「听到的拍点是否落在谱面网格上」，不经过 BPM 估计这个有损环节。
  参考项 = 自相关 BPM 估计（容忍 ±3，只报不拦）＋首 onset 相位。

用法：py -3 demo/pv-prototype/verify_beats.py
产物：verify_report.json + 控制台 PASS/FAIL
"""
import json
import pathlib
import subprocess
import sys
import tempfile
import wave

import numpy as np

HERE = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent / 'agi-mv-repro'))
from analyze_audio import (HOP, SR, estimate_bpm,  # noqa: E402
                           pick_onsets, spectral_flux)

TOL_MEAN_MS = 25     # onset→16 分格 平均偏差（mp3 帧粒度 26ms 在内）
TOL_P95_MS = 60      # p95：弱拍/装饰音 onset 抖动
TOL_BPM_REF = 3.0    # BPM 估计参考项容忍（不作为主判据）


def load_mono_22050(mp3):
    with tempfile.NamedTemporaryFile(suffix='.wav', delete=False) as fh:
        tmp = pathlib.Path(fh.name)
    try:
        r = subprocess.run(
            ['ffmpeg', '-y', '-v', 'error', '-i', str(mp3),
             '-ar', str(SR), '-ac', '1', str(tmp)],
            capture_output=True, text=True, encoding='utf-8', errors='replace')
        if r.returncode != 0:
            raise RuntimeError('解码失败: ' + (r.stderr or '')[-200:])
        with wave.open(str(tmp), 'rb') as w:
            raw = w.readframes(w.getnframes())
        return np.frombuffer(raw, dtype='<i2').astype(np.float64) / 32768.0
    finally:
        tmp.unlink(missing_ok=True)


def main() -> int:
    mp = json.loads((HERE / 'music_map.json').read_text(encoding='utf-8'))
    x = load_mono_22050(HERE / f"{mp['name']}.mp3")

    flux = spectral_flux(x)
    onsets, _ = pick_onsets(flux)
    ons = np.array([i * HOP / SR for i in onsets])  # HOP 跟 analyze_audio 同源，上游改窗距这里不再静默错标
    bpm_ref = estimate_bpm(flux, onsets)

    # 主判据：onset → 谱面 16 分网格（谱面先验锚定，无 BPM 估计环节）
    step = mp['beat_period'] / 4
    d = ons % step
    dev_ms = np.minimum(d, step - d) * 1000
    # 首拍相位：最早 onset 相对谱面网格的带符号偏差（折到 ±半拍内；
    # mp3 编码器延迟 ~1 帧在容忍内）
    p = ons[0] % mp['beat_period']
    if p > mp['beat_period'] / 2:
        p -= mp['beat_period']
    first_dev = p * 1000

    report = {
        'score_map': {'bpm': mp['bpm'], 'beats': len(mp['beats']),
                      'phase_ms': mp['phase'] * 1000},
        'dsp': {'bpm_estimated': round(bpm_ref, 2), 'onsets': len(onsets)},
        'onset_to_16th_grid_ms': {
            'mean': round(float(dev_ms.mean()), 1),
            'p95': round(float(np.percentile(dev_ms, 95)), 1),
            'max': round(float(dev_ms.max()), 1)},
        'first_onset_phase_ms': round(first_dev, 1),
        'tolerance': {'mean_ms': TOL_MEAN_MS, 'p95_ms': TOL_P95_MS,
                      'bpm_ref': TOL_BPM_REF},
        'note': 'BPM 估计为参考项（自相关 ±2-3% 固有误差），主判据=网格贴合',
    }
    ok = (report['onset_to_16th_grid_ms']['mean'] <= TOL_MEAN_MS
          and report['onset_to_16th_grid_ms']['p95'] <= TOL_P95_MS)
    report['verdict'] = 'PASS' if ok else 'FAIL'
    (HERE / 'verify_report.json').write_text(
        json.dumps(report, ensure_ascii=False, indent=1), encoding='utf-8')

    print(f"谱面 {mp['bpm']} BPM（{len(mp['beats'])} 格）  "
          f"DSP 估计 {bpm_ref:.2f} BPM（参考项，容忍 ±{TOL_BPM_REF}）")
    print(f"onset {len(onsets)} 个 → 16 分格偏差："
          f"mean {report['onset_to_16th_grid_ms']['mean']}ms / "
          f"p95 {report['onset_to_16th_grid_ms']['p95']}ms / "
          f"max {report['onset_to_16th_grid_ms']['max']}ms")
    print(f"首 onset 相位 {first_dev:.0f}ms（mp3 编码延迟 ~26ms 量级在容忍内）")
    print('DSP 交叉验证 ' + report['verdict'])
    return 0 if ok else 1


if __name__ == '__main__':
    sys.exit(main())
