# -*- coding: utf-8 -*-
"""scene_lint：场景文件静态契约门禁（contract.md §1-§3 的 lint 项）。

用法：python ai/scene_lint.py <场景html> [<场景html> ...]
      python ai/scene_lint.py --selftest   （跑内置规则自检）

退出码：0=全过；1=有 ✗ 违规（输出逐条，供回喂修复）。
设计约束：只做零误报的机械判定；模糊问题（审美/密度）归 Tier1/Tier2，不进本文件。
"""
import re
import sys
import pathlib

# 段 id（F2/F3 一致性检查用）；从文件名或 data-composition-id 推断
SEG_ID_RE = re.compile(r'seg\d{2,}')

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

    # F4/F5：clip 轨道层与语义标注计数（v1 惯例：clip 标全屏轨道层 ≥1；
    # 内部元素由模型自己的时间轴编排，语义标注才是每个可见元素的硬要求）
    n_clip = len(re.findall(r'class="[^"]*\bclip\b[^"]*"', html))
    if n_clip < 1:
        errs.append(('F4', '缺 class="clip" 轨道层（背景等全屏层需要 clip+data-start/duration/track-index）'))
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

    # A2：@font-face 存在性提示（弱规则：只在用了 font-family 且无任何声明时提示）
    ff_used = set(t.lower() for t in re.findall(r"font-family\s*:\s*['\"]([^'\"]+)['\"]", html))
    if ff_used and '@font-face' not in html:
        errs.append(('A2', '用了 font-family 但无任何 @font-face 声明（引用即声明，src:local()）'))

    # A4：data-URI 大图（素材应入库 assets/img/，不入 HTML）
    for mm in re.finditer(r'url\s*\(\s*["\']?(data:image/[^)"\']+)', html, re.I):
        if len(mm.group(1)) > 65536:
            errs.append(('A4', 'data-URI 图片超过 64KB（素材应入素材库，HTML 引 assets/img/ 路径）'))
            break

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
