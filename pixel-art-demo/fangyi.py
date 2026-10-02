# -*- coding: utf-8 -*-
# 明日方舟:终末地 庄方宜 Q版像素立绘(待机姿态,仿参考图风格)
# 画法:用小工具函数在画布上逐块拼装,再渲染成 SVG/PNG
import os, zlib, struct

OUT = os.path.dirname(os.path.abspath(__file__))

PAL = {
    'O': '#1f1b26',  # 深色描边
    'K': '#2e2b38',  # 头发黑
    'k': '#4d4960',  # 头发高光
    'G': '#5fca96',  # 玉绿主色(挑染/衣摆/剑)
    'g': '#378f68',  # 深玉绿
    'W': '#f4f6f1',  # 衣服白
    'w': '#ccd8cc',  # 衣服白阴影
    'S': '#f6d7c3',  # 皮肤
    's': '#e3b79f',  # 皮肤阴影
    'E': '#3fca87',  # 瞳色绿
    'B': '#3b3040',  # 深色(靴/腰带/剑柄)
    'Y': '#ecc76d',  # 金色饰件
    'H': '#d8f2e2',  # 角(浅玉色)
    'A': '#b9f4d6',  # 剑身亮部/电光
    'a': '#7ddcae',  # 剑身中间色
    'd': '#2f8f66',  # 剑刃暗边/裤影
    'F': '#c96f74',  # 嘴唇
}

BG = (32, 36, 44)  # 预览图背景色


class Grid:
    def __init__(self, w, h):
        self.w, self.h = w, h
        self.g = [['.'] * w for _ in range(h)]

    def px(self, x, y, c):
        if 0 <= x < self.w and 0 <= y < self.h:
            self.g[y][x] = c

    def hline(self, x0, x1, y, c):
        for x in range(x0, x1 + 1):
            self.px(x, y, c)

    def vline(self, x, y0, y1, c):
        for y in range(y0, y1 + 1):
            self.px(x, y, c)

    def rect(self, x, y, w, h, c):
        for j in range(h):
            self.hline(x, x + w - 1, y + j, c)

    def paint(self, x, y, art):
        for j, row in enumerate(art):
            for i, ch in enumerate(row):
                if ch != '.':
                    self.px(x + i, y + j, ch)

    def rows(self):
        return [''.join(r) for r in self.g]


g = Grid(46, 46)
CX = 28  # 角色中轴

# ---------- 后发(披到腰后的黑发) ----------
hair = {6: (24, 32), 7: (23, 33), 8: (22, 34), 9: (21, 35), 10: (21, 35),
        11: (22, 34), 12: (22, 34), 13: (22, 34), 14: (22, 34), 15: (22, 34),
        16: (22, 34), 17: (23, 33), 18: (23, 33), 19: (23, 33), 20: (24, 32),
        21: (24, 32), 22: (24, 32), 23: (25, 31), 24: (25, 31), 25: (26, 30),
        26: (26, 30), 27: (27, 29)}
for y, (a, b) in hair.items():
    g.hline(a, b, y, 'K')
g.vline(25, 8, 13, 'k')   # 发丝高光
g.vline(31, 8, 12, 'k')
g.px(24, 10, 'k')
g.px(32, 11, 'k')
for x in (26, 28, 30):    # 发尾锯齿
    g.px(x, 28, 'K')

# ---------- 尾巴(右后方,麒麟尾) ----------
g.paint(35, 28, ["K", "K"])
g.paint(35, 30, ["WW.", "WGW", ".W."])

# ---------- 腿部(长裤) ----------
g.rect(25, 32, 7, 2, 'g')            # 胯
g.rect(25, 34, 3, 5, 'g')            # 左腿
g.rect(29, 34, 3, 5, 'g')            # 右腿
g.vline(27, 34, 38, 'd')             # 裤影
g.vline(29, 34, 38, 'd')
g.px(28, 34, 'O')                    # 裆部分隔

# ---------- 靴子(中靴+细跟) ----------
g.hline(25, 27, 39, 'Y'); g.hline(29, 31, 39, 'Y')   # 金色靴口
g.rect(25, 40, 3, 3, 'B'); g.rect(29, 40, 3, 3, 'B')
g.hline(24, 27, 43, 'B'); g.hline(29, 32, 43, 'B')   # 脚尖外八
g.px(26, 44, 'B'); g.px(30, 44, 'B')                 # 高跟

# ---------- 躯干(露肩上衣,白→玉绿渐变) ----------
g.hline(25, 31, 21, 'S')             # 肩颈
g.hline(26, 30, 22, 'S')
g.px(24, 22, 'S'); g.px(32, 22, 'S') # 肩头
g.rect(24, 23, 9, 5, 'W')            # 白衣
for y in range(23, 28):              # 侧影
    g.px(24, y, 'w'); g.px(32, y, 'w')
g.hline(27, 29, 23, 'S')             # 一字领露肤
g.px(25, 25, 'w'); g.px(31, 25, 'w')
g.hline(24, 32, 28, 'w')             # 渐变过渡
g.hline(24, 32, 29, 'G')
g.hline(24, 32, 30, 'g')             # 绿色衣摆
g.hline(24, 32, 31, 'B')             # 腰带
g.px(27, 31, 'Y'); g.px(28, 31, 'Y') # 金扣

# ---------- 手臂 ----------
# 左臂(观众左):抬手掌心向上,遥控青霆剑
g.rect(22, 22, 2, 3, 'S')            # 上臂
g.rect(22, 24, 2, 2, 'G')            # 分离袖
g.px(22, 26, 'g'); g.px(23, 26, 'g')
g.rect(19, 26, 4, 2, 'S')            # 前臂外伸
g.rect(17, 26, 2, 2, 'S')            # 手掌(掌心向上)
# 右臂:自然下垂
g.rect(33, 22, 2, 3, 'S')
g.rect(33, 24, 2, 2, 'G')
g.px(33, 26, 'g'); g.px(34, 26, 'g')
g.rect(33, 27, 2, 4, 'S')
g.rect(33, 31, 2, 2, 'S')

# ---------- 脖子 ----------
g.hline(27, 29, 19, 's')             # 下巴投影
g.hline(27, 29, 20, 'S')

# ---------- 脸 ----------
g.hline(24, 32, 10, 'S')
for y in range(11, 18):
    g.hline(24, 32, y, 'S')
g.hline(25, 31, 18, 'S')             # 下巴
g.px(24, 12, 's'); g.px(32, 12, 's') # 鬓角影
g.hline(25, 26, 14, 'O'); g.hline(30, 31, 14, 'O')   # 睫毛
g.hline(25, 26, 15, 'E'); g.hline(30, 31, 15, 'E')   # 绿瞳
g.px(25, 15, 'g'); g.px(31, 15, 'g') # 瞳外侧渐暗
g.px(26, 15, 'A'); g.px(30, 15, 'A') # 瞳内高光
g.px(28, 17, 'F')                    # 嘴

# ---------- 刘海(中分+玉绿挑染) ----------
g.hline(22, 34, 8, 'K')
g.px(28, 8, 'k')                     # 分缝
g.hline(22, 34, 9, 'K')
g.px(28, 9, 'k')
g.px(31, 9, 'G'); g.px(32, 9, 'G')   # 挑染
for x in range(22, 35):              # row10: 中缝露肤1px
    g.px(x, 10, 'S' if x == 28 else ('G' if x in (31, 32) else 'K'))
for x in range(22, 35):              # row11
    g.px(x, 11, 'S' if x in (27, 28, 29) else ('G' if x == 31 else 'K'))
for x in range(22, 35):              # row12 刘海底
    g.px(x, 12, 'S' if x in (26, 27, 28, 29, 30) else 'K')

# ---------- 长鬓角 ----------
g.vline(23, 9, 21, 'K')
g.vline(33, 9, 21, 'K')
g.px(23, 14, 'k'); g.px(33, 14, 'k')

# ---------- 麒麟角 ----------
g.paint(23, 1, [".Y..", "YH..", ".HH.", ".HH.", ".HH."])
g.paint(31, 1, ["..Y.", "..HY", ".HH.", ".HH.", ".HH."])

# ---------- 公主辫(右肩后垂下) ----------
g.paint(35, 20, ["KK", "kk", "KK", ".Y", "kk", "k.", "K."])

# ---------- 青霆剑(悬浮大剑,尖端朝下) ----------
g.px(5, 13, 'Y')                     # 剑首
g.rect(4, 14, 2, 3, 'B')             # 剑柄
g.hline(2, 8, 17, 'Y')               # 护手
for y in range(18, 37):              # 剑身
    g.px(4, y, 'd'); g.px(6, y, 'd')
for y in range(18, 24):              # 靠近护手更亮
    g.px(5, y, 'A')
for y in range(24, 37):
    g.px(5, y, 'a')
g.px(4, 37, 'd'); g.px(5, 37, 'a'); g.px(6, 37, 'd')
g.px(5, 38, 'd')
g.px(5, 39, 'A')                     # 剑尖
# 电花
g.px(8, 19, 'A'); g.px(9, 20, 'a')
g.px(2, 26, 'a'); g.px(1, 27, 'A')
g.px(8, 33, 'A'); g.px(9, 34, 'a')

# ================= 渲染 =================
def merge_runs(grid):
    runs = []
    for y, row in enumerate(grid.rows()):
        x = 0
        while x < len(row):
            c = row[x]
            if c != '.':
                x0 = x
                while x < len(row) and row[x] == c:
                    x += 1
                runs.append((x0, y, x - x0, c))
            else:
                x += 1
    return runs


def selfcheck(grid, runs):
    rebuilt = [['.'] * grid.w for _ in range(grid.h)]
    for x, y, wd, c in runs:
        for i in range(wd):
            rebuilt[y][x + i] = c
    return [''.join(r) for r in rebuilt] == grid.rows()


def hex2rgb(h):
    return tuple(int(h[i:i + 2], 16) for i in (1, 3, 5))


def write_png(path, w, h, rows_rgb):
    raw = b''.join(b'\x00' + b''.join(struct.pack('BBB', *px) for px in row)
                   for row in rows_rgb)

    def chunk(t, data):
        return struct.pack('>I', len(data)) + t + data + struct.pack(
            '>I', zlib.crc32(t + data) & 0xffffffff)

    png = (b'\x89PNG\r\n\x1a\n'
           + chunk(b'IHDR', struct.pack('>IIBBBBB', w, h, 8, 2, 0, 0, 0))
           + chunk(b'IDAT', zlib.compress(raw)) + chunk(b'IEND', b''))
    with open(path, 'wb') as f:
        f.write(png)


def render_png(grid, path, scale=8):
    w, h = grid.w * scale, grid.h * scale
    rows = []
    for gy in range(grid.h):
        line = []
        for gy0 in range(scale):
            row = []
            for gx in range(grid.w):
                c = grid.g[gy][gx]
                rgb = BG if c == '.' else hex2rgb(PAL[c])
                row.extend([rgb] * scale)
            line.extend([row] * 1)
        rows.extend([line[0]] if False else [])
    # 简化:逐行生成
    rows = []
    for gy in range(grid.h):
        band = []
        for _ in range(scale):
            row = []
            for gx in range(grid.w):
                c = grid.g[gy][gx]
                rgb = BG if c == '.' else hex2rgb(PAL[c])
                row.extend([rgb] * scale)
            band.append(row)
        rows.extend(band)
    write_png(path, w, h, rows)


def grid_to_svg_groups(grid, groups, extra=''):
    """groups: [(id, x_min, x_max)] 按 x 范围拆成动画组"""
    runs = merge_runs(grid)
    out = []
    for gid, x0, x1 in groups:
        parts = [f'  <g id="{gid}">']
        for x, y, wd, c in runs:
            if x0 <= x <= x1:
                parts.append(f'    <rect x="{x}" y="{y}" width="{wd}" height="1" fill="{PAL[c]}"/>')
        parts.append('  </g>')
        out.append('\n'.join(parts))
    return '\n'.join(out), runs


runs = merge_runs(g)
assert selfcheck(g, runs), 'round-trip mismatch'

# 带待机动画的 SVG:角色呼吸起伏 + 剑对向浮动 + 地面影子
groups_svg, _ = grid_to_svg_groups(
    g, [('sword', 0, 11), ('char', 12, 45)])
anim = f'''<svg xmlns="http://www.w3.org/2000/svg" width="460" height="460" viewBox="0 0 46 46" shape-rendering="crispEdges">
<style>
  @keyframes bob    {{ 0%,100% {{ transform: translateY(0); }} 50% {{ transform: translateY(1px); }} }}
  @keyframes floaty {{ 0%,100% {{ transform: translateY(1px); }} 50% {{ transform: translateY(0); }} }}
  @keyframes shadow {{ 0%,100% {{ opacity: .32; }} 50% {{ opacity: .22; }} }}
  #char  {{ animation: bob 2.6s steps(1,end) infinite; }}
  #sword {{ animation: floaty 2.6s steps(1,end) infinite; }}
  #shade {{ animation: shadow 2.6s steps(1,end) infinite; }}
</style>
<ellipse id="shade" cx="28" cy="45.3" rx="8.5" ry="1.1" fill="#000"/>
{groups_svg}
</svg>
'''
with open(os.path.join(OUT, 'fangyi.svg'), 'w', encoding='utf-8') as f:
    f.write(anim)

# 静态 SVG
static = f'''<svg xmlns="http://www.w3.org/2000/svg" width="460" height="460" viewBox="0 0 46 46" shape-rendering="crispEdges">
<ellipse cx="28" cy="45.3" rx="8.5" ry="1.1" fill="#000" opacity=".28"/>
{grid_to_svg_groups(g, [('all', 0, 45)])[0]}
</svg>
'''
with open(os.path.join(OUT, 'fangyi_static.svg'), 'w', encoding='utf-8') as f:
    f.write(static)

# 预览 PNG(供检查)
render_png(g, os.path.join(OUT, 'fangyi_preview.png'))

print('OK  size=%dx%d  rects=%d' % (g.w, g.h, len(runs)))
