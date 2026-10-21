# -*- coding: utf-8 -*-
"""卡点机械闸（音乐驱动 PV · 原型闸）——M3「卡点机械闸」的最小可行版。

能机器判的绝不花 LLM 的钱。两个不变量：
  1. 切点闸：每个镜头边界（t0/t1）必须钉在节拍格上（默认容忍 50ms——
     30fps 帧粒度 33ms 在内）；
  2. 时长闸：每个渲染出的镜头 mp4 实测时长 = 分镜账面时长（±100ms），
     且账面时长 = 小节数 × 每小节秒数。

外加：正交性自测——喂一个故意歪的切点（+137ms），闸必须叫。闸自己
不会叫，等于没有闸（回放门禁掷硬币的教训）。

用法：py -3 demo/pv-prototype/check_cuts.py [--no-video 跳过mp4实测]
产物：gate_report.json + 控制台 PASS/FAIL
"""
import json
import pathlib
import subprocess
import sys

HERE = pathlib.Path(__file__).resolve().parent
TOL_CUT_MS = 50       # 切点偏拍容忍
TOL_DUR_MS = 100      # mp4 实测时长 vs 账面


def ffprobe_dur(p):
    r = subprocess.run(
        ['ffprobe', '-v', 'error', '-show_entries', 'format=duration',
         '-of', 'default=nw=1:nk=1', str(p)],
        capture_output=True, text=True, encoding='utf-8', errors='replace')
    return float(r.stdout.strip()) if r.returncode == 0 else None


def cut_deviations(cuts, mp):
    """切点 → 节拍格（含半拍格）偏差表。分镜边界钉整拍/半拍都算卡上。"""
    period = mp['beat_period']
    half = period / 2
    out = []
    for t in cuts:
        d = (t - mp['phase']) % half
        dev = min(d, half - d) * 1000
        out.append((t, dev))
    return out


def main() -> int:
    for stream in (sys.stdout, sys.stderr):
        if hasattr(stream, 'reconfigure'):
            stream.reconfigure(errors='replace')
    spec = json.loads((HERE / 'shots.json').read_text(encoding='utf-8'))
    mp = json.loads((HERE / 'music_map.json').read_text(encoding='utf-8'))
    bar_s = spec['meta']['bar']

    # 正交性自测：歪切点必须被抓
    bad_t = spec['shots'][0]['t0'] + 0.137
    bad_dev = cut_deviations([bad_t], mp)[0][1]
    self_test = bad_dev > TOL_CUT_MS
    if not self_test:
        print(f'自测 FAIL：歪切点 +137ms 偏差仅 {bad_dev:.1f}ms 未被抓——闸失灵')
        return 3

    findings, rows = [], []
    cuts = [spec['shots'][0]['t0']] + [s['t1'] for s in spec['shots']]
    for t, dev in cut_deviations(cuts, mp):
        rows.append(dict(kind='cut', at=t, dev_ms=round(dev, 1)))
        if dev > TOL_CUT_MS:
            findings.append(f'切点 {t}s 偏离节拍 {dev:.0f}ms（容忍 {TOL_CUT_MS}）')

    for s in spec['shots']:
        want = (s['bar1'] - s['bar0']) * bar_s
        if abs((s['t1'] - s['t0']) - want) > 1e-6:
            findings.append(f"{s['id']} 账面时长≠小节账")
        if '--no-video' not in sys.argv:
            vp = HERE / 'renders' / f"{s['id']}.mp4"
            if not vp.exists():
                findings.append(f"{s['id']} 渲染产物缺失：{vp.name}")
                continue
            d = ffprobe_dur(vp)
            dev = abs(d - (s['t1'] - s['t0'])) * 1000
            rows.append(dict(kind='dur', id=s['id'], measured=round(d, 3),
                             want=s['t1'] - s['t0'], dev_ms=round(dev, 1)))
            if dev > TOL_DUR_MS:
                findings.append(f"{s['id']} 实测 {d:.3f}s ≠ 账面 "
                                f"{s['t1']-s['t0']:.3f}s（±{TOL_DUR_MS}ms）")

    ok = not findings
    report = dict(verdict='PASS' if ok else 'FAIL', findings=findings,
                  self_test_bad_cut_ms=round(bad_dev, 1), checks=rows,
                  tolerance=dict(cut_ms=TOL_CUT_MS, dur_ms=TOL_DUR_MS))
    (HERE / 'gate_report.json').write_text(
        json.dumps(report, ensure_ascii=False, indent=1), encoding='utf-8')
    cut_devs = [r['dev_ms'] for r in rows if r['kind'] == 'cut']
    print(f"切点 {len(cut_devs)} 个：max 偏差 {max(cut_devs):.1f}ms"
          f"（容忍 {TOL_CUT_MS}ms）")
    if '--no-video' not in sys.argv:
        dur_devs = [r['dev_ms'] for r in rows if r['kind'] == 'dur']
        if dur_devs:
            print(f"镜头时长 {len(dur_devs)} 个：max 偏差 {max(dur_devs):.1f}ms"
                  f"（容忍 {TOL_DUR_MS}ms）")
    for f in findings:
        print('  - ' + f)
    print('卡点机械闸 ' + report['verdict'])
    return 0 if ok else 1


if __name__ == '__main__':
    sys.exit(main())
