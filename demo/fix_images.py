# -*- coding: utf-8 -*-
"""替换自述片带水印的配图：从 Wikimedia Commons 下载干净图到暂存目录。
用法：python demo/fix_images.py   （产物在 demo/selfvideo-frames/newimg/，人工目检后再覆盖）"""
import json, pathlib, sys, urllib.parse, urllib.request

ROOT = pathlib.Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / 'ai'))
from fetch_images import search_commons, download  # 复用管线的搜索与下载

# 目标文件名 → (Commons 查询, 期望mime, 排除的文件标题)
SLOTS = {
    'img01.jpg': ('clapperboard film set', 'image/jpeg'),
    'img02.jpg': ('chinese calligraphy manuscript', 'image/jpeg'),
    'img03.jpg': ('anime sketch pencil drawing', 'image/jpeg'),
    'img04.jpg': ('manga drawing monochrome', 'image/jpeg'),
    'img05.jpg': ('storyboard sketch', 'image/jpeg'),
    'img07.png': ('lego bricks colorful', 'image/png'),
    'img08.jpg': ('mountain lake landscape', 'image/jpeg'),
    'img09.jpg': ('video editing computer screen', 'image/jpeg'),
    'img11.jpg': ('old book pages', 'image/jpeg'),
    'img12.png': ('bookshelf library books', 'image/png'),
    'img13.jpg': ('film reel cinema', 'image/jpeg'),
}

def main():
    out = ROOT / 'demo' / 'selfvideo-frames' / 'newimg'
    out.mkdir(parents=True, exist_ok=True)
    used = set()
    report = []
    for name, (query, want_mime) in SLOTS.items():
        got = None
        for url, title in search_commons(query, n=10):
            if title in used:
                continue
            # 下载后按字节判真实类型，jpg 槽位优先 jpeg
            data = download(url)
            if not data:
                continue
            real = 'image/jpeg' if data.startswith(b'\xff\xd8\xff') else (
                   'image/png' if data.startswith(b'\x89PNG') else None)
            if real is None:
                continue
            if real != want_mime and name.endswith('.jpg') and real != 'image/jpeg':
                continue  # jpg 槽位不收 png（扩展名要贴合）
            got = (url, title, data, real)
            break
        if got:
            url, title, data, real = got
            used.add(title)
            (out / name).write_bytes(data)
            ext_ok = '(ext贴合)' if name.endswith(real.split('/')[-1].replace('jpeg','jpg')) else '(字节型别贴合,扩展名沿用)'
            report.append(f'{name} ← {title} {len(data)//1024}KB {ext_ok}')
            print(f'{name} ✓ {title}')
        else:
            report.append(f'{name} ✗ 未找到可用图')
            print(f'{name} ✗ {query}')
    (out / '_report.txt').write_text('\n'.join(report), encoding='utf-8')
    # 出处留档（credits.txt 追加）
    with open(ROOT / 'data/projects/p20260925-155753/credits.txt', 'a', encoding='utf-8') as f:
        f.write('\n# 2026-09-25 水印替换（Wikimedia Commons）\n')
        for line in report:
            if '←' in line:
                name, title = line.split(' ← ')
                f.write(f'替换{name}\t{title}\tWikimedia Commons\n')

if __name__ == '__main__':
    main()
