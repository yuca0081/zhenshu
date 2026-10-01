# -*- coding: utf-8 -*-
"""贴纸化素材工位:公有领域老图 → 拼贴贴纸 PNG(抠图/白边/网点/撕边)。

素材(均公有领域,Wikimedia Commons):
  rawimg/p2.jpg    Unidentified man with beard, LCCN2016653170(国会图书馆)
  rawimg/page0.jpg Harper's Weekly Vol.VI, Christmas 1861(MET DP875214)

产物(assets/img/,场景契约 A4 的落盘路径):
  sticker_portrait.png  人像贴纸:rembg 抠图+白边+报纸网点+暖纸调+直切底边
  scrap_masthead.png    报头撕块(真印刷字面)
  scrap_engrave.png     插画撕块
  stickers_qc.png       在牛皮纸底上的三件产物预览

全部随机走固定种子;本脚本是做片期工具,不进渲染契约。
"""
import random
import pathlib
import numpy as np
from PIL import Image, ImageDraw, ImageFilter, ImageOps

ROOT = pathlib.Path(__file__).resolve().parent
RAW = ROOT / 'rawimg'
OUT = ROOT / 'assets' / 'img'
OUT.mkdir(parents=True, exist_ok=True)

INK = (44, 37, 28)        # 暖墨
PAPER = (238, 229, 207)   # 暖纸
BAYER8 = np.array([[0, 32, 8, 40, 2, 34, 10, 42],
                   [48, 16, 56, 24, 50, 18, 58, 26],
                   [12, 44, 4, 36, 14, 46, 6, 38],
                   [60, 28, 52, 20, 62, 30, 54, 22],
                   [3, 35, 11, 43, 1, 33, 9, 41],
                   [51, 19, 59, 27, 49, 17, 57, 25],
                   [15, 47, 7, 39, 13, 45, 5, 37],
                   [63, 31, 55, 23, 61, 29, 53, 21]], dtype=np.float32) / 64.0


def halftone(img, screen=3):
    """报纸网点感:缩小→Bayer 有序抖动二值→近邻放大→上墨/纸双色。"""
    g = ImageOps.autocontrast(img.convert('L'))
    w, h = g.size
    small = g.resize((max(1, w // screen), max(1, h // screen)), Image.LANCZOS)
    a = np.asarray(small, dtype=np.float32) / 255.0
    tile = np.tile(BAYER8, (a.shape[0] // 8 + 1, a.shape[1] // 8 + 1))[:a.shape[0], :a.shape[1]]
    bits = (a > tile).astype(np.uint8) * 255
    big = Image.fromarray(bits, 'L').resize((w, h), Image.NEAREST)
    out = Image.new('RGB', (w, h), PAPER)
    ink_layer = Image.new('RGB', (w, h), INK)
    out.paste(ink_layer, (0, 0), big)
    return out


def print_tone(img, screen=2, dot_opacity=0.30):
    """老印刷双色:平滑墨纸渐变打底(保脸可读)+ 细网点叠印(保印刷颗粒)。"""
    g = ImageOps.autocontrast(img.convert('L'), cutoff=(2, 2))
    a = (np.asarray(g, dtype=np.float32) / 255.0) ** 0.82
    w, h = g.size
    small = g.resize((max(1, w // screen), max(1, h // screen)), Image.LANCZOS)
    arr = np.asarray(small, dtype=np.float32) / 255.0
    tile = np.tile(BAYER8, (arr.shape[0] // 8 + 1, arr.shape[1] // 8 + 1))[:arr.shape[0], :arr.shape[1]]
    dots = Image.fromarray(((arr > tile) * 255).astype('uint8'), 'L').resize((w, h), Image.NEAREST)
    d = np.asarray(dots, dtype=np.float32) / 255.0
    ink = np.array(INK, dtype=np.float32)
    paper = np.array(PAPER, dtype=np.float32)
    smooth = paper + (ink - paper) * (1 - a)[:, :, None]
    dotted = paper + (ink - paper) * (1 - d)[:, :, None]
    out = smooth * (1 - dot_opacity) + dotted * dot_opacity
    return Image.fromarray(np.clip(out, 0, 255).astype('uint8'), 'RGB')


def sticker_border(img_rgba, border=14):
    """白色 die-cut 描边:alpha 膨胀一圈填白垫底。"""
    a = img_rgba.getchannel('A')
    big_a = a.filter(ImageFilter.MaxFilter(border * 2 + 1))
    big_a = big_a.filter(ImageFilter.GaussianBlur(1.2))
    canvas = Image.new('RGBA', img_rgba.size, (255, 255, 255, 0))
    white = Image.new('RGBA', img_rgba.size, (255, 255, 255, 255))
    canvas.paste(white, (0, 0), big_a)
    canvas.alpha_composite(img_rgba)
    return canvas


def torn_mask(w, h, seed, step=16, amp=9, bites=2):
    """撕纸边:矩形边缘逐点抖动 + 随机咬口,轻模糊模拟纸纤维。"""
    rng = random.Random(seed)
    pts = []
    sides = [((0, 0), (w, 0)), ((w, 0), (w, h)), ((w, h), (0, h)), ((0, h), (0, 0))]
    for (x0, y0), (x1, y1) in sides:
        n = max(2, int(((x1 - x0) + (y1 - y0)) / step))
        for i in range(n):
            t = i / n
            x = x0 + (x1 - x0) * t
            y = y0 + (y1 - y0) * t
            inward = rng.uniform(0, amp)
            dx = (x1 - x0) or -(y1 - y0)
            dy = (y1 - y0) or (x1 - x0)
            norm = (dx * dx + dy * dy) ** 0.5 or 1
            pts.append((x - dx / norm * inward, y - dy / norm * inward))
    m = Image.new('L', (w, h), 0)
    ImageDraw.Draw(m).polygon(pts, fill=255)
    for _ in range(bites):
        sx, sy = rng.randint(int(w * .2), int(w * .8)), rng.randint(0, 1)
        r = rng.randint(8, 16)
        if sy == 0:
            ImageDraw.Draw(m).ellipse([sx - r, -r, sx + r, r], fill=0)
        else:
            ImageDraw.Draw(m).ellipse([sx - r, h - r, sx + r, h + r], fill=0)
    return m.filter(ImageFilter.GaussianBlur(0.8))


def make_scrap(src, box, out_name, target_w, seed):
    """报纸撕块:裁切→缩放→纸色微调+颗粒→撕边 alpha。"""
    crop = src.crop(box)
    scale = target_w / crop.width
    crop = crop.resize((target_w, int(crop.height * scale)), Image.LANCZOS)
    a = np.asarray(crop.convert('RGB'), dtype=np.int16)
    rng = random.Random(seed)
    tint = np.array([10, 4, -8], dtype=np.int16)   # 略偏暖
    a = np.clip(a + tint, 0, 255)
    noise = np.random.default_rng(seed).integers(-5, 6, a.shape[:2] + (1,), dtype=np.int16)
    a = np.clip(a + noise, 0, 255).astype(np.uint8)
    sc = Image.fromarray(a, 'RGB').convert('RGBA')
    sc.putalpha(torn_mask(sc.width, sc.height, seed))
    sc.save(OUT / out_name)
    return sc


def main():
    # ── 1. 人像贴纸 ──────────────────────────────────────────────
    card = Image.open(RAW / 'p2.jpg')                      # 2110x3374,黑边包卡片
    card = card.crop((130, 110, 1985, 3260))               # 去黑边,留奶白卡面
    head = card.resize((int(card.width * 2000 / card.height), 2000), Image.LANCZOS)
    try:
        from rembg import remove, new_session
        session = new_session('u2netp')   # 4.7MB 轻量模型:棚拍纯背景人像足够
        cut = remove(head, session=session)
    except Exception as e:
        raise SystemExit(f'rembg 失败: {e}')
    # 亮度键并肩:晕影淡出区(肩/大衣)rembg 咬不住,暗部并回 alpha。
    # 键值取自原图 head(rembg 输出的背景区 RGB 已被抹黑);只取中央椭圆,
    # 排除卡片的装饰边线与霉斑;中值滤波去掉孤立小点。
    L0 = np.asarray(head.convert('L'), dtype=np.float32)
    extra = np.clip((195 - L0) / 70.0, 0, 1)
    w0, h0 = head.size
    em = Image.new('L', head.size, 0)
    ImageDraw.Draw(em).ellipse([w0 * .06, -h0 * .12, w0 * .94, h0 * 1.05], fill=255)
    extra *= np.asarray(em, dtype=np.float32) / 255.0
    a0 = np.asarray(cut.getchannel('A'), dtype=np.float32)
    alpha = Image.fromarray(np.maximum(a0, extra * 255.0).astype('uint8'), 'L')
    alpha = alpha.filter(ImageFilter.MedianFilter(5))
    cut.putalpha(alpha)
    a = np.asarray(cut.getchannel('A'))
    ys, xs = np.where(a > 40)
    cut = cut.crop((xs.min(), ys.min(), xs.max() + 1, min(ys.max() + 1, cut.height)))
    w, h = cut.size
    cut = cut.crop((0, max(0, h - int(h * 0.86)), w, h))   # 底部直切(bust 收边)
    cut.thumbnail((540, 660), Image.LANCZOS)
    hlt = print_tone(cut).convert('RGBA')
    hlt.putalpha(cut.getchannel('A'))
    sticker = sticker_border(hlt, border=13)
    sticker.save(OUT / 'sticker_portrait.png')

    # ── 2. 报纸撕块(Harper's Weekly 1861)───────────────────────
    page = Image.open(RAW / 'page0.jpg')                   # 2429x3650
    make_scrap(page, (0, 0, 2429, 620), 'scrap_masthead.png', 980, seed=1861)
    make_scrap(page, (430, 1560, 2010, 2900), 'scrap_engrave.png', 860, seed=1862)

    # ── 3. QC 预览 ───────────────────────────────────────────────
    bg = Image.new('RGB', (1180, 760), (168, 135, 82))
    bg.paste(Image.new('RGB', (1180, 760), (0, 0, 0)),
             (0, 0), Image.new('L', (1180, 760), 0))
    bg.paste(Image.open(OUT / 'scrap_masthead.png'), (30, 40), Image.open(OUT / 'scrap_masthead.png'))
    bg.paste(Image.open(OUT / 'scrap_engrave.png'), (620, 90), Image.open(OUT / 'scrap_engrave.png'))
    bg.paste(Image.open(OUT / 'sticker_portrait.png'), (380, 260), Image.open(OUT / 'sticker_portrait.png'))
    bg.save(RAW / 'stickers_qc.png')
    for f in sorted(OUT.glob('*.png')):
        print(f.name, f.stat().st_size // 1024, 'KB')


if __name__ == '__main__':
    main()
