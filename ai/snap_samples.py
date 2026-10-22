# -*- coding: utf-8 -*-
"""风格样张截图：style/style_samples.json 的 HTML 样张 → style/sample-N.png。

spec 生成（produce.genSpecs）把这些 PNG 附给多模态模型——看着真图写画面，
替代纯文字风格注入（glm-5.3-flash 图片输入已实测支持）。

用 Edge 无头截图（Windows 自带，零新依赖；样张是画幅同尺寸的自包含 HTML，
窗口跟 project.json aspect 走——竖版项目的版式必须在竖版画布上验证）。
字体：截图前对样张做目录字体声明纠错+补齐（style/font_assets.json 的真字体
woff2）——渲染端没装的目录字体 local() 必回退雅黑，"所见"就不再是设计确认的
那个样子（2026-10-13 齐马蓝：样张与正片双双回退，样张承诺失真）。字体下载只
在 submit_style 后的 font_tool ensure（6 分钟档）做——prep② 单张截图（--one，
90 秒超时）掐不过冷启动下载（重试×120s+zip 64MB），--one 只吃现成 font_assets，
没有就系统字体兜底（2026-10-21 巡检：链条级，90s 必被掐死还谎称全过）。
缓存：style_samples.json / font_assets.json 任一比已产 PNG 新则重截，否则全跳过。

用法：python ai/snap_samples.py <项目目录>            （批量，带缓存）
      python ai/snap_samples.py --one <项目目录> <html> <out.png>   （单张，prep② 验证管线用）
"""
import json
import pathlib
import subprocess
import sys
import tempfile

from assemble import VEND_RE, ensure_font_faces, load_font_assets


def inline_vend_keep_missing(proj: pathlib.Path, html_path: pathlib.Path) -> None:
    """legacy 静态样张的 vend 兜底（best-effort，就地改写）：有 vend 声明才调
    vendor_tool inline(--keep-missing=未入册库保留声明，画面空转由判卷兜底)。
    prep② 动态链路在 Go 侧严格采购内联，不经过这里。失败打印降级原因不挡截图。"""
    try:
        if not VEND_RE.search(html_path.read_text(encoding='utf-8')):
            return
        r = subprocess.run(
            [sys.executable, str(pathlib.Path(__file__).resolve().parent / 'vendor_tool.py'),
             'inline', '--keep-missing', str(html_path)],
            capture_output=True, text=True, encoding='utf-8', errors='replace', timeout=120)
        if r.returncode != 0:
            print('vend 内联降级:', (r.stdout or r.stderr or '').strip()[:160])
    except Exception as e:
        print('vend 内联降级:', e)
import font_tool

BROWSERS = [
    r'C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe',
    r'C:\Program Files\Microsoft\Edge\Application\msedge.exe',
    r'C:\Program Files\Google\Chrome\Application\chrome.exe',
]
MAX_SAMPLES = 3  # 与 genSpecs 的 styleSampleImages 上限一致


def canvas_size(proj: pathlib.Path):
    """截图窗口=项目画幅（与渲染端 CanvasFor 同口径：9:16→1080×1920，缺省 16:9）。"""
    try:
        aspect = json.loads((proj / 'project.json').read_text(encoding='utf-8')).get('aspect')
    except Exception:
        aspect = None
    return (1080, 1920) if aspect == '9:16' else (1920, 1080)


def find_browser():
    return next((p for p in BROWSERS if pathlib.Path(p).exists()), None)


def shoot(browser, src, out, win_w, win_h):
    # --headless（旧模式）+ 独立 user-data-dir：--headless=new 在默认
    # profile 被 Edge 占用时静默不出图（rc=0 无产物，2026-09-28 实测）
    with tempfile.TemporaryDirectory(prefix='hf-snap-') as ud:
        subprocess.run(
            [browser, '--headless', '--disable-gpu', '--no-first-run',
             f'--user-data-dir={ud}', '--hide-scrollbars',
             f'--screenshot={pathlib.Path(out).resolve()}', f'--window-size={win_w},{win_h}',
             '--virtual-time-budget=3000', pathlib.Path(src).resolve().as_uri()],
            check=True, timeout=30, capture_output=True)


def ensure_fonts(proj: pathlib.Path):
    """目录字体准备（幂等+缓存；批量模式用）：有申请书且资产未备好就就地补齐，
    失败降级不挡出图。单张模式（--one）不调这里——90 秒超时掐不过冷启动下载。"""
    if (proj / 'style' / 'font_assets.request.json').exists():
        try:
            font_tool.ensure(str(proj))
        except Exception as e:
            print(f'font_tool ensure 失败（样张退回系统字体）: {e}')


def snap_one(proj: pathlib.Path, html_path: str, out_path: str) -> int:
    """单张模式：任意样张 html → out.png（prep② 渲染验证管线逐轮调用）。
    只吃现成 font_assets 补字体声明，不做下载（见模块头注释）。"""
    browser = find_browser()
    if not browser:
        print('未找到 Edge/Chrome，单张截图跳过')
        return 1
    win_w, win_h = canvas_size(proj)
    resolved = load_font_assets(proj)
    html = pathlib.Path(html_path).read_text(encoding='utf-8')
    if resolved:
        html = ensure_font_faces(html, resolved)
    tmp = proj / 'style' / '.snap-one.html'
    tmp.write_text(html, encoding='utf-8')
    inline_vend_keep_missing(proj, tmp)
    out = pathlib.Path(out_path)
    had_out = out.exists()
    try:
        shoot(browser, tmp, out_path, win_w, win_h)
    except Exception as e:
        print(f'单张截图失败: {e}')
        # 只清本次尝试的残迹，别把上一轮截好的 PNG 删了——Go 侧账面还指着它
        if not had_out:
            out.unlink(missing_ok=True)
        return 1
    finally:
        tmp.unlink(missing_ok=True)
    print(f'样张单张截图 → {out_path}')
    return 0


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
    # 缓存键 = 样张 JSON 与字体资产二者的新鲜度：font_tool 重跑（字体产物更新）
    # 而样张未变时，旧 PNG 仍是回退渲染，也要重截
    deps = [ss_path, proj / 'style' / 'font_assets.json']
    newest = max((p.stat().st_mtime for p in deps if p.exists()), default=0)
    if all(o.exists() and o.stat().st_mtime >= newest for o in outs):
        return 0  # 缓存有效
    browser = find_browser()
    if not browser:
        print('未找到 Edge/Chrome，跳过样张截图（spec 生成退回纯文本风格）')
        return 0
    ensure_fonts(proj)
    win_w, win_h = canvas_size(proj)

    resolved = load_font_assets(proj)
    for i, out in enumerate(outs):
        html = samples[i].get('html', '')
        if resolved:
            html = ensure_font_faces(html, resolved)
        tmp = proj / 'style' / f'.snap-{i}.html'
        tmp.write_text(html, encoding='utf-8')
        inline_vend_keep_missing(proj, tmp)
        try:
            shoot(browser, tmp, out, win_w, win_h)
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
    # Go 侧管道捕获不设 PYTHONUTF8，中文 Windows 默认 cp936 严格解码——✓/✗ 不在 GBK 中，
    # 成功路径的 print 反而炸成 rc=1。errors='replace' 保输出永远可写。
    for stream in (sys.stdout, sys.stderr):
        if hasattr(stream, 'reconfigure'):
            stream.reconfigure(errors='replace')
    args = sys.argv[1:]
    if args and args[0] == '--one':
        if len(args) < 4:
            print('用法: python ai/snap_samples.py --one <项目目录> <html> <out.png>', file=sys.stderr)
            sys.exit(2)
        # 不 mkdir：传错目录要炸出响（静默建目录会把笔误藏成「成功截图到空项目」）
        sys.exit(snap_one(pathlib.Path(args[1]), args[2], args[3]))
    if len(args) < 1:
        print('用法: python ai/snap_samples.py <项目目录>              （批量，带缓存）\n'
              '      python ai/snap_samples.py --one <项目目录> <html> <out.png>   （单张）',
              file=sys.stderr)
        sys.exit(2)
    sys.exit(main(args[0]))
