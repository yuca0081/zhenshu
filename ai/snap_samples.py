# -*- coding: utf-8 -*-
"""风格样张截图：style/style_samples.json 的 HTML 样张 → style/sample-N.png。

spec 生成（produce.genSpecs）把这些 PNG 附给多模态模型——看着真图写画面，
替代纯文字风格注入（glm-5.3-flash 图片输入已实测支持）。

用 Edge 无头截图（Windows 自带，零新依赖；样张是 960×540 自包含 HTML）。
缓存：style_samples.json 未比已产 PNG 新则全跳过（spec 重生成不重复截图）。

用法：python ai/snap_samples.py <项目目录>
"""
import json
import pathlib
import subprocess
import sys
import tempfile

BROWSERS = [
    r'C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe',
    r'C:\Program Files\Microsoft\Edge\Application\msedge.exe',
    r'C:\Program Files\Google\Chrome\Application\chrome.exe',
]
MAX_SAMPLES = 3  # 与 genSpecs 的 styleSampleImages 上限一致


def main(proj_dir: str) -> int:
    # Go 侧管道捕获不设 PYTHONUTF8，中文 Windows 默认 cp936 严格解码——✓/✗ 不在 GBK 中，
    # 成功路径的 print 反而炸成 rc=1。errors='replace' 保输出永远可写。
    for stream in (sys.stdout, sys.stderr):
        if hasattr(stream, 'reconfigure'):
            stream.reconfigure(errors='replace')
    proj = pathlib.Path(proj_dir)
    ss_path = proj / 'style' / 'style_samples.json'
    if not ss_path.exists():
        return 0
    try:
        samples = json.loads(ss_path.read_text(encoding='utf-8')).get('samples') or []
    except Exception as e:
        print(f'style_samples.json 不可读，跳过截图: {e}')
        return 0
    if not samples:
        return 0

    outs = [proj / 'style' / f'sample-{i}.png' for i in range(min(len(samples), MAX_SAMPLES))]
    if all(o.exists() and o.stat().st_mtime >= ss_path.stat().st_mtime for o in outs):
        return 0  # 缓存有效
    browser = next((p for p in BROWSERS if pathlib.Path(p).exists()), None)
    if not browser:
        print('未找到 Edge/Chrome，跳过样张截图（spec 生成退回纯文本风格）')
        return 0

    for i, out in enumerate(outs):
        tmp = proj / 'style' / f'.snap-{i}.html'
        tmp.write_text(samples[i].get('html', ''), encoding='utf-8')
        try:
            # --headless（旧模式）+ 独立 user-data-dir：--headless=new 在默认
            # profile 被 Edge 占用时静默不出图（rc=0 无产物，2026-09-28 实测）
            with tempfile.TemporaryDirectory(prefix='hf-snap-') as ud:
                subprocess.run(
                    [browser, '--headless', '--disable-gpu', '--no-first-run',
                     f'--user-data-dir={ud}', '--hide-scrollbars',
                     f'--screenshot={out.resolve()}', '--window-size=960,540',
                     '--virtual-time-budget=3000', tmp.resolve().as_uri()],
                    check=True, timeout=30, capture_output=True)
        except Exception as e:
            print(f'样张 {i} 截图失败: {e}')
            out.unlink(missing_ok=True)
            return 0  # 截不出就退回纯文本，不挡管线
        finally:
            tmp.unlink(missing_ok=True)
        print(f'样张 {i} 截图 → {out.name}')
    # samples 数量缩减后，旧的更高编号 PNG 已不在 outs 里、不会再被 spec 引用，
    # 留着会被旧缓存/旧产物混淆——清掉
    for i in range(len(outs), MAX_SAMPLES):
        (proj / 'style' / f'sample-{i}.png').unlink(missing_ok=True)
    return 0


if __name__ == '__main__':
    if len(sys.argv) < 2:
        print('用法: python ai/snap_samples.py <项目目录>', file=sys.stderr)
        sys.exit(2)
    sys.exit(main(sys.argv[1]))
