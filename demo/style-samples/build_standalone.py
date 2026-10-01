# -*- coding: utf-8 -*-
"""把契约模板片段(segNN.html)包装成 hyperframes 可独立渲染的完整文档。

管线内这步由 produce 装配(index.html 渲染壳);demo 里等价复刻:
  <template> 的 innerHTML + 本地 gsap + vend 注入(契约 A3/wrapper,vend-stage.md §3):
  场景内 <!-- vend: lib@ver --> 声明 → 本脚本查 manifest → 拷 vendor 文件进 assets/vendor/
  并在壳里注入 <script src=...>(场景文件本身永远不引 script src,由平台侧代劳)。

用法: python build_standalone.py
"""
import json
import pathlib
import re
import shutil

ROOT = pathlib.Path(__file__).resolve().parent
REPO = ROOT.parent.parent
VEND_RE = re.compile(r'<!--\s*vend:\s*([A-Za-z0-9_.-]+)@([^\s>]+?)\s*-->')

SHELL = """<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8" />
<meta name="viewport" content="width=1920, height=1080" />
<script src="assets/gsap.min.js"></script>
{vend_tags}
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


def vend_tags_for(html: str) -> str:
    """解析 vend 声明 → 拷贝 vendor 文件 → 生成壳内 script 标签(平台侧注入)。"""
    manifest_path = REPO / 'data' / 'library' / 'vendor' / 'manifest.json'
    manifest = json.loads(manifest_path.read_text(encoding='utf-8')) if manifest_path.exists() else {'libs': {}}
    (ROOT / 'assets' / 'vendor').mkdir(parents=True, exist_ok=True)
    tags = []
    for m in VEND_RE.finditer(html):
        lib, ver = m.group(1), m.group(2)
        ent = manifest.get('libs', {}).get(lib, {}).get(ver)
        if not ent:
            raise SystemExit(f'✗ vend 声明未入册:{lib}@{ver}(先跑 python ai/vendor_tool.py ensure {lib}@{ver})')
        src = REPO / 'data' / 'library' / 'vendor' / lib / ver / ent['file']
        dst = ROOT / 'assets' / 'vendor' / f'{lib}-{ver}.js'
        shutil.copyfile(src, dst)
        tags.append(f'<script src="assets/vendor/{lib}-{ver}.js"></script>')
        print(f'  vend 注入 {lib}@{ver} ← {src.relative_to(REPO)}')
    return '\n'.join(tags)


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
        out.write_text(SHELL.replace('{vend_tags}', vend_tags_for(html)) % m.group(1), encoding='utf-8')
        print(f'built {out.relative_to(ROOT)}')


if __name__ == '__main__':
    main()
