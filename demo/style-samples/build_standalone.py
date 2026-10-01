# -*- coding: utf-8 -*-
"""把契约模板片段(segNN.html)包装成 hyperframes 可独立渲染的完整文档。

管线内这步由 produce 装配(index.html 渲染壳);demo 里等价复刻:
  <template> 的 innerHTML + 本地 gsap(独立渲染时本脚本就是 wrapper,契约 A3)。

用法: python build_standalone.py   (产物在 standalone/,已 gitignore 亦可)
"""
import pathlib
import re

ROOT = pathlib.Path(__file__).resolve().parent

SHELL = """<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8" />
<meta name="viewport" content="width=1920, height=1080" />
<script src="assets/gsap.min.js"></script>
<style>
  html, body { margin: 0; padding: 0; width: 1920px; height: 1080px; overflow: hidden; }
</style>
</head>
<body>
<script>window.__timelines = window.__timelines || {};</script>
%s
</body>
</html>
"""


def main():
    gsap_src = ROOT.parent / 'ink-prototype' / 'assets' / 'gsap.min.js'
    (ROOT / 'assets').mkdir(exist_ok=True)
    (ROOT / 'assets' / 'gsap.min.js').write_bytes(gsap_src.read_bytes())
    (ROOT / 'standalone').mkdir(exist_ok=True)
    for src in sorted(ROOT.glob('seg*.html')):
        html = src.read_text(encoding='utf-8')
        m = re.search(r'<template>([\s\S]*)</template>', html)
        if not m:
            print(f'skip {src.name}: 无 <template>')
            continue
        out = ROOT / 'standalone' / src.name
        out.write_text(SHELL % m.group(1), encoding='utf-8')
        print(f'built {out.relative_to(ROOT)}')


if __name__ == '__main__':
    main()
