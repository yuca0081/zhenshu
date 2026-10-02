# -*- coding: utf-8 -*-
# 把 ASCII 字符网格变成 SVG 像素画:一个字符 = 一个像素方块
# '.' 表示透明,其他字符查调色板取颜色
import os

OUT = os.path.dirname(os.path.abspath(__file__))

SPRITES = {
    "heart": {
        "title": "爱心",
        "palette": {"D": "#8f1d3c", "r": "#ef476f", "h": "#ffd6de"},
        "grid": [
            ".DD.....DD.",
            "DrrD...DrrD",
            "DhrrD.DrrrD",
            "DhrrrDrrrrD",
            "DrrrrrrrrrD",
            ".DrrrrrrrD.",
            "..DrrrrrD..",
            "...DrrrD...",
            "....DrD....",
            ".....D.....",
        ],
    },
    "invader": {
        "title": "太空入侵者",
        "palette": {"X": "#4ade80"},
        "grid": [
            "..X.....X..",
            "...X...X...",
            "..XXXXXXX..",
            ".XX.XXX.XX.",
            "XXXXXXXXXXX",
            "X.XXXXXXX.X",
            "X.X.....X.X",
            "...XX.XX...",
        ],
    },
    "smiley": {
        "title": "笑脸",
        "palette": {"Y": "#ffd93b", "D": "#5c4300"},
        "grid": [
            ".YYYYYY.",
            "YYYYYYYY",
            "YYDYYDYY",
            "YYYYYYYY",
            "YDYYYYDY",
            "YYDDDDYY",
            "YYYYYYYY",
            ".YYYYYY.",
        ],
    },
}


def merge_runs(grid):
    """同一行里连续的同色像素合并成一个横条 rect,省体积。"""
    runs = []
    for y, row in enumerate(grid):
        x = 0
        while x < len(row):
            c = row[x]
            if c != ".":
                x0 = x
                while x < len(row) and row[x] == c:
                    x += 1
                runs.append((x0, y, x - x0, c))
            else:
                x += 1
    return runs


def to_svg(sprite, scale=20):
    grid = sprite["grid"]
    pal = sprite["palette"]
    h, w = len(grid), len(grid[0])
    lines = [
        f'<svg xmlns="http://www.w3.org/2000/svg" width="{w*scale}" height="{h*scale}" '
        f'viewBox="0 0 {w} {h}" shape-rendering="crispEdges">'
    ]
    runs = merge_runs(grid)
    for x, y, wd, c in runs:
        lines.append(f'  <rect x="{x}" y="{y}" width="{wd}" height="1" fill="{pal[c]}"/>')
    lines.append("</svg>")
    return "\n".join(lines), runs


def selfcheck(grid, runs):
    """把 rect 反推回网格,确认和原图一字不差。"""
    h, w = len(grid), len(grid[0])
    rebuilt = [["."] * w for _ in range(h)]
    for x, y, wd, c in runs:
        for i in range(wd):
            rebuilt[y][x + i] = c
    return ["".join(r) for r in rebuilt] == list(grid)


cards = []
for name, sprite in SPRITES.items():
    svg_text, runs = to_svg(sprite)
    assert selfcheck(sprite["grid"], runs), name
    with open(os.path.join(OUT, name + ".svg"), "w", encoding="utf-8") as f:
        f.write(svg_text + "\n")
    grid = sprite["grid"]
    h, w = len(grid), len(grid[0])
    sized = svg_text.replace("<svg ", f'<svg style="width:{w*20}px;height:{h*20}px" ', 1)
    cards.append(
        f'<div class="card"><div class="checker">{sized}</div>'
        f'<div class="label">{sprite["title"]}</div>'
        f'<div class="meta">{w}×{h} 像素 · {len(runs)} 个矩形</div></div>'
    )

html = f"""<!doctype html>
<html lang="zh">
<head>
<meta charset="utf-8">
<title>SVG 像素画 Demo</title>
<style>
  body {{ background:#17171f; color:#e8e8f0; font-family:system-ui,sans-serif;
         display:flex; flex-direction:column; align-items:center; padding:48px 16px; margin:0; }}
  h1 {{ font-weight:600; font-size:22px; }}
  p.sub {{ color:#9a9ab0; margin-top:-6px; font-size:14px; }}
  .row {{ display:flex; gap:36px; flex-wrap:wrap; justify-content:center; margin-top:36px; }}
  .card {{ background:#20202c; border:1px solid #2e2e3e; border-radius:12px; padding:26px 26px 16px; text-align:center; }}
  .checker {{ background:repeating-conic-gradient(#262633 0% 25%, #1c1c26 0% 50%) 0 0 / 22px 22px;
             border-radius:8px; line-height:0; display:inline-block; padding:12px; }}
  .label {{ margin-top:12px; color:#c8c8da; font-size:14px; }}
  .meta {{ color:#78788e; font-size:12px; margin-top:4px; }}
</style>
</head>
<body>
<h1>SVG 像素画 Demo</h1>
<p class="sub">一个像素 = 一个 &lt;rect&gt; 方块 · 无限放大不糊</p>
<div class="row">
{chr(10).join(cards)}
</div>
</body>
</html>
"""

with open(os.path.join(OUT, "index.html"), "w", encoding="utf-8") as f:
    f.write(html)

print("OK:", ", ".join(SPRITES))
