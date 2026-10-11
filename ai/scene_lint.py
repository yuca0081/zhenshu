# -*- coding: utf-8 -*-
"""scene_lint：场景文件静态契约门禁（contract.md §1-§3 的 lint 项）。

用法：python ai/scene_lint.py <场景html> [<场景html> ...]
      python ai/scene_lint.py --selftest   （跑内置规则自检）

退出码：0=全过；1=有 ✗ 违规（输出逐条，供回喂修复）。
设计约束：只做零误报的机械判定；模糊问题（审美/密度）归 Tier1/Tier2，不进本文件。
"""
import json
import re
import shutil
import subprocess
import sys
import pathlib
import tempfile

# 段 id（F2/F3 一致性检查用）；从文件名或 data-composition-id 推断
SEG_ID_RE = re.compile(r'seg\d{2,}')

# vendor manifest（vend-stage.md §3;A3 缺库检查的数据源）
VENDOR_MANIFEST = pathlib.Path(__file__).resolve().parent.parent / 'data' / 'library' / 'vendor' / 'manifest.json'

VEND_RE = re.compile(r'<!--\s*vend:\s*([A-Za-z0-9_.-]+)@([^\s>]+?)\s*-->')


_manifest_cache = None  # (mtime_ns, parsed)：一次 lint 会话内几十条 vend 声明共读一次盘


def vendor_has(lib: str, ver: str) -> bool:
    global _manifest_cache
    try:
        mt = VENDOR_MANIFEST.stat().st_mtime_ns
        if _manifest_cache is None or _manifest_cache[0] != mt:
            _manifest_cache = (mt, json.loads(VENDOR_MANIFEST.read_text(encoding='utf-8')))
        return bool(_manifest_cache[1].get('libs', {}).get(lib, {}).get(ver))
    except Exception:
        return False

# T2/T3/T6/A1：禁用 API（字符串出现即违规；注释里写也不行——注释里的逃生口最危险）
BANNED_PATTERNS = [
    (r'Date\.now\s*\(', 'T2', '禁 Date.now（真实时钟）'),
    (r'new\s+Date\s*\(', 'T2', '禁 new Date（真实时钟）'),
    (r'performance\.now\s*\(', 'T2', '禁 performance.now（真实时钟）'),
    (r'setTimeout\s*\(', 'T2', '禁 setTimeout（自管时间）'),
    (r'setInterval\s*\(', 'T2', '禁 setInterval（自走循环）'),
    (r'requestAnimationFrame\s*\(', 'T2', '禁 requestAnimationFrame（浏览器帧时钟）'),
    (r'Math\.random\s*\(', 'T3', '禁 Math.random（未种子化随机；用注入的种子化 rng）'),
    (r'crypto\.getRandomValues', 'T3', '禁 crypto.getRandomValues（未种子化随机）'),
    (r'\bfetch\s*\(', 'A1', '禁 fetch（无网络）'),
    (r'XMLHttpRequest', 'A1', '禁 XMLHttpRequest（无网络）'),
    (r'\bWebSocket\b', 'A1', '禁 WebSocket（无网络）'),
    (r'EventSource', 'A1', '禁 EventSource（无网络）'),
    (r'navigator\.', 'A1', '禁 navigator（环境依赖）'),
    (r'localStorage|sessionStorage|indexedDB', 'A1', '禁存储 API（环境状态）'),
    (r'new\s+Worker\s*\(', 'T6', '禁 Worker（调度时序不确定）'),
    (r'SharedArrayBuffer', 'T6', '禁 SharedArrayBuffer（共享状态）'),
    (r'<video\b', 'T7', '禁 <video> 元素（视频素材走帧序列，由平台层处理）'),
    (r'<audio\b', 'T7', '禁 <audio> 元素（音轨是平台层，assemble 注入）'),
    (r'<iframe\b', 'A1', '禁 <iframe>（外部内容）'),
    (r'<script[^>]+src\s*=\s*["\']https?://', 'A1', '禁外链脚本（CDN）'),
    (r'<script[^>]+src\s*=', 'A3', '禁场景内引脚本（框架库由 wrapper 注入；需要新库用 <!-- vend: lib@ver --> 声明）'),
    (r'<link[^>]+href\s*=\s*["\']https?://', 'A1', '禁外链样式'),
    (r'url\s*\(\s*["\']?https?://', 'A1', '禁外链资源 url()'),
    (r'@import\b', 'A1', '禁 @import（外部样式）'),
    (r'@keyframes\b', 'T5', '禁 @keyframes（合成器时钟，seek 不到）'),
    (r'\btransition\s*:', 'T5', '禁 CSS transition（合成器时钟）'),
    (r'\banimation\s*:', 'T5', '禁 CSS animation（合成器时钟）'),
    (r'data-ph\s*=', 'F7', '禁字幕短语标注（字幕带是平台层，模型不写字幕）'),
    (r'data-w\s*=', 'F7', '禁字幕逐词标注（字幕带是平台层）'),
]


def lint_text(html: str, seg_hint: str = '') -> list:
    """返回违规清单 [(规则, 消息)]；空 = 过。"""
    errs = []
    # F1：<template> 包裹（松校验：首行附近出现即可，doctype/html 头是违规）
    stripped = html.strip()
    if not stripped.startswith('<template'):
        errs.append(('F1', '文件必须是 <template> 片段开头（无 DOCTYPE/html/head/body）'))
    for tag in ('<!doctype', '<html', '<head', '<body'):
        if tag in stripped[:400].lower():
            errs.append(('F1', f'片段内出现 {tag}（场景文件不是完整文档）'))
            break

    # F2/F3：composition id 与 timeline 注册
    m = re.search(r'data-composition-id="([^"]+)"', html)
    if not m:
        errs.append(('F2', '缺 data-composition-id（根元素必须有）'))
        seg = seg_hint
    else:
        seg = m.group(1)
        if seg_hint and seg != seg_hint:
            errs.append(('F2', f'data-composition-id="{seg}" 与段号 {seg_hint} 不一致'))
    if not re.search(r'gsap\.timeline\s*\(\s*\{\s*paused\s*:\s*true', html):
        errs.append(('F3', '必须是 gsap.timeline({paused:true})（平台时钟，seek 驱动）'))
    if seg and f'window.__timelines["{seg}"]' not in html:
        errs.append(('F3', f'未注册 window.__timelines["{seg}"]（渲染器入口）'))

    # F5：语义标注计数（硬要求）。F4（clip 轨道层）2026-09-30 降级为指导：
    # bakeoff 实测模型用 #root CSS 画底色是正当惯例，硬门禁造成修复死锁。
    n_name = len(re.findall(r'data-hf-name=', html))
    if n_name < 3:
        errs.append(('F5', f'data-hf-name 语义标注仅 {n_name} 处（<3；元素级指认依赖它）'))

    # F6：视口单位
    vw = re.findall(r'\d+v(?:w|h)\b', re.sub(r'<script.*?</script>', '', html, flags=re.S))
    if vw:
        errs.append(('F6', f'禁 vw/vh 视口单位（画幅固定像素）：出现 {vw[:5]}'))

    # T4：canvas 存在 → 必须是 __draw_ 纯函数模式
    n_canvas = len(re.findall(r'<canvas\b', html))
    if n_canvas:
        if not re.search(r'window\s*\[\s*"__draw_[^"]+"\s*\]\s*=\s*function\s*\(\s*t\b', html) \
           and not re.search(r'window\.__draw_\w+\s*=\s*function\s*\(\s*t\b', html):
            errs.append(('T4', '有 <canvas> 但没有 window["__draw_…"]=function(t,…) 参数化绘制（draw 必须是 t 的纯函数）'))

    # T9：CSS transform 的元素被 GSAP 补间引用（GSAP 覆盖整个 transform，
    # CSS 里的 translateX(-50%) 居中等会被静默丢弃——bakeoff 实测模型高发）
    css_ids = set()
    for mm in re.finditer(r'#([A-Za-z][\w-]*)\s*\{[^}]*transform\s*:', html):
        css_ids.add(mm.group(1))
    if css_ids:
        script = html[html.find('<script'):] if '<script' in html else ''
        for eid in css_ids:
            if re.search(r"['\"]#" + re.escape(eid) + r"['\"]", script):
                errs.append(('T9', f'#{eid} 的 CSS transform 与 GSAP 补间混用（GSAP 会覆盖 CSS transform）；居中用 GSAP 的 xPercent/yPercent，CSS 里删掉 transform'))

    # A2：@font-face 存在性提示（弱规则：只在用了 font-family 且无任何声明时提示）
    ff_used = set(t.lower() for t in re.findall(r"font-family\s*:\s*['\"]([^'\"]+)['\"]", html))
    if ff_used and '@font-face' not in html:
        errs.append(('A2', '用了 font-family 但无任何 @font-face 声明（引用即声明，src:local()）'))

    # A4：data-URI 大图（素材应入库 assets/img/，不入 HTML）
    for mm in re.finditer(r'url\s*\(\s*["\']?(data:image/[^)"\']+)', html, re.I):
        if len(mm.group(1)) > 65536:
            errs.append(('A4', 'data-URI 图片超过 64KB（素材应入素材库，HTML 引 assets/img/ 路径）'))
            break

    # A3：vend 声明（vend-stage.md §3）——模型只有申请权：
    # 已登记库放行；未登记库报缺库（消息即采购申请单，由平台/人跑 vendor_tool 采购）
    for mm in VEND_RE.finditer(html):
        lib, ver = mm.group(1), mm.group(2)
        if not vendor_has(lib, ver):
            errs.append(('A3', f'缺库:{lib}@{ver} 未入 vendor manifest——'
                         f'运行 python ai/vendor_tool.py ensure {lib}@{ver} --file <tarball内路径> 采购'))

    # J1：内联脚本 JS 语法校验（node --check；无 node 时跳过——渲染环境必有）。
    # 每块单独 check：浏览器里多个 <script> 各自是独立作用域，两个块各声明同名
    # const 完全合法，拼一个文件检查就假语法错——零误报设计不容这种误杀。
    scripts = re.findall(r'<script[^>]*>(.*?)</script>', html, flags=re.S)
    if scripts and shutil.which('node'):
        for si, sc in enumerate(scripts):
            with tempfile.NamedTemporaryFile('w', suffix='.js', delete=False, encoding='utf-8') as tf:
                tf.write(sc)
                tmpjs = tf.name
            try:
                r = subprocess.run(['node', '--check', tmpjs], capture_output=True, text=True)
                if r.returncode != 0:
                    msg = (r.stderr or '').strip().splitlines()
                    tag = f'（第 {si + 1} 个 <script> 块）' if len(scripts) > 1 else ''
                    errs.append(('J1', '内联脚本 JS 语法错误' + tag + '：' + (msg[-1] if msg else '未知')[:160]))
            finally:
                pathlib.Path(tmpjs).unlink(missing_ok=True)

    # 禁用 API 全量扫描
    for pat, rule, msg in BANNED_PATTERNS:
        for mm in re.finditer(pat, html, re.I):
            line = html.count('\n', 0, mm.start()) + 1
            errs.append((rule, f'第 {line} 行：{msg}'))
            break  # 每条规则报首个即可（修复回喂不用刷屏）

    return errs


def lint_file(path: str) -> int:
    p = pathlib.Path(path)
    if not p.exists():
        print(f'✗ 文件不存在：{path}')
        return 1
    html = p.read_text(encoding='utf-8', errors='replace')
    hint = ''
    m = SEG_ID_RE.search(p.stem)
    if m:
        hint = m.group(0)
    errs = lint_text(html, hint)
    if not errs:
        print(f'✓ {p.name} 契约 lint 通过')
        return 0
    print(f'✗ {p.name} 契约 lint 未过（{len(errs)} 项）：')
    for rule, msg in errs:
        print(f'  ✗ [{rule}] {msg}')
    return 1


def selftest() -> int:
    """规则自检：最小合法样本过；各禁用项逐个命中。"""
    ok = '<template><style>@font-face{font-family:"X";src:local("X");}</style>' \
         '<div data-composition-id="seg01" data-width="1920" data-height="1080"><div id="root">' \
         '<div class="clip" data-start="0" data-duration="1" data-track-index="0" data-hf-name="a"></div>' \
         '<div class="clip" data-start="0" data-duration="1" data-track-index="1" data-hf-name="b"></div>' \
         '<div class="clip" data-start="0" data-duration="1" data-track-index="2" data-hf-name="c"></div>' \
         '</div></div><script>(function(){var tl=gsap.timeline({paused:true});' \
         'window.__timelines["seg01"]=tl;})();</script></template>'
    assert lint_text(ok, 'seg01') == [], lint_text(ok, 'seg01')
    probes = [
        ('Math.random()', 'T3'), ('Date.now()', 'T2'), ('setTimeout(', 'T2'),
        ('requestAnimationFrame(', 'T2'), ('<video ', 'T7'), ('data-ph="0"', 'F7'),
        ('transition: all', 'T5'), ('@keyframes x', 'T5'), ('fetch(', 'A1'),
        ('new Worker(', 'T6'), ('<canvas id="c"></canvas>', 'T4'),
        ('<script src="assets/x.js"></script>', 'A3'),
        ('<!-- vend: nonexistent-lib@1.0.0 -->', 'A3'),
    ]
    for probe, rule in probes:
        errs = lint_text(ok + probe, 'seg01')
        assert any(r == rule for r, _ in errs), f'{probe} 未命中 {rule}: {errs}'
    print('✓ scene_lint selftest 通过')
    return 0


if __name__ == '__main__':
    args = sys.argv[1:]
    if not args:
        print(__doc__)
        sys.exit(1)
    if args[0] == '--selftest':
        sys.exit(selftest())
    sys.exit(max(lint_file(a) for a in args))
