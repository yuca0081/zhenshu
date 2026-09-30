# -*- coding: utf-8 -*-
"""把 ai/registry/styles.json 注册风格播种进方法库（data/library/stylepacks.json）。

每个风格生成一张 960x540 静态样张（背景画法/色板/卡片质感与引擎一致），
已存在同名 direction 则跳过（幂等）。用法：python ai/seed_styles.py
"""
import datetime
import json
import pathlib
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
REGISTRY = ROOT / 'data' / 'library' / 'registry' / 'styles.json'
LIB = ROOT / 'data' / 'library' / 'stylepacks.json'


def _on(c):
    c = (c or '#888888').lstrip('#')
    if len(c) == 3:
        c = ''.join(ch * 2 for ch in c)
    r, g, b = (int(c[i:i + 2], 16) for i in (0, 2, 4))
    return '#1B2233' if (0.2126 * r + 0.7152 * g + 0.0722 * b) / 255 > 0.62 else '#FFFFFF'


def _stars(color):
    out, s = [], 7
    for _ in range(30):
        s = (s * 48271) % 2147483647
        x = (s % 997) / 10.0
        s = (s * 48271) % 2147483647
        y = (s % 929) / 10.0 * 0.8
        s = (s * 48271) % 2147483647
        r = 1.0 + (s % 18) / 10.0
        out.append(f'radial-gradient({r:.1f}px {r:.1f}px at {x:.1f}% {y:.1f}%,{color} 50%,transparent 55%)')
    return out


def bg_css(bg):
    """tokens.bg → (body background, extra html)。静态样张版（无 {scene} 占位）。
    底色走 background-color，渐变并入 background-image；图层顺序 = 叠层顺序
    （CSS 多背景第一层在最上）：fx 在前、不透明底渐变垫底；size 逐图层对齐。"""
    fx = bg.get('fx') or []
    imgs, sizes = [], []
    base_color = f'background-color:{bg.get("from", "#101423")};'
    for g in bg.get('glow') or []:
        imgs.append(f'radial-gradient({g.get("r", 45)}% {g.get("r", 40)}% at {g.get("x", 25)}% {g.get("y", 20)}%,'
                    f'{g.get("color")}{g.get("alpha", "59")} 0%,transparent 70%)')
        sizes.append('auto')
    if 'stars' in fx:
        imgs += _stars(bg.get('fx_color', '#FFFFFF'))
        sizes += ['auto'] * 30
    if 'dots' in fx:
        imgs.append(f'radial-gradient({bg.get("fx_color", "#888")}33 2px,transparent 2.5px)')
        sizes.append('54px 54px')
    if 'grid' in fx or 'paper' in fx:
        c = bg.get('fx_color', '#888')
        alpha = '30' if 'grid' in fx else '12'
        imgs += [f'linear-gradient({c}{alpha} 1px,transparent 1px)', f'linear-gradient(90deg,{c}{alpha} 1px,transparent 1px)']
        sizes += ['100px 100px'] * 2
    if bg.get('type') != 'solid':
        imgs.append(f'linear-gradient({int(bg.get("angle", 180))}deg,{bg.get("from")} 0%,{bg.get("to", bg.get("from"))} 100%)')
        sizes.append('auto')
    prop = base_color + (f'background-image:{",".join(imgs)};' if imgs else '') + \
        (f'background-size:{",".join(sizes)};' if sizes else '')
    extra = ''
    if 'grid_persp' in fx:
        c = bg.get('fx_color', '#888')
        extra = (f'<div style="position:absolute;left:-30%;right:-30%;top:-52%;bottom:-15%;'
                 f'background-image:linear-gradient({c}40 2px,transparent 2px),linear-gradient(90deg,{c}40 2px,transparent 2px);'
                 f'background-size:90px 90px;transform:perspective(1100px) rotateX(38deg);transform-origin:50% 100%"></div>')
    return prop, extra


def sample_html(st):
    t = st['tokens']
    acc = (t.get('accent') + t.get('accent'))[:6]
    primary = t.get('primary', acc[0])
    ink, txt = t.get('ink', '#333'), t.get('txt', '#333')
    surface, surface_txt = t.get('surface', '#FFFFFF'), t.get('surface_txt', '#333')
    font = t.get('font', "'Microsoft YaHei',sans-serif")
    title = t.get('title') or {}
    cap = t.get('cap') or {}
    shadow = t.get('shadow', 'none')
    radius = t.get('radius', 16)
    bw = t.get('border_w', 0)
    border = f'border:{bw}px solid {ink};' if bw else 'border:1px solid ' + ('rgba(255,255,255,.25)' if str(ink).startswith('rgba') else ink + '55') + ';'
    prop, extra = bg_css(t.get('bg') or {'type': 'solid', 'from': '#101423'})
    c0, c1, c2 = acc[0], acc[1], acc[2]
    tcol = title.get('color') or txt
    return f"""<!doctype html><html><head><meta charset="utf-8"><style>
*{{margin:0;box-sizing:border-box}}html,body{{width:960px;height:540px;overflow:hidden;font-family:{font};color:{txt}}}
#p{{position:relative;width:960px;height:540px;overflow:hidden;{prop}}}
.t{{position:absolute;left:0;right:0;top:52px;text-align:center;font-size:52px;font-weight:{title.get("weight", 800)};letter-spacing:{title.get("spacing", 2)}px;color:{tcol}}}
.sub{{position:absolute;left:0;right:0;top:126px;text-align:center;font-size:22px;font-weight:700;color:{primary};letter-spacing:2px}}
.card{{position:absolute;background:{surface};{border}border-radius:{radius}px;overflow:hidden;box-shadow:{shadow}}}
.hd{{background:{c0};color:{_on(c0)};font-weight:800;font-size:26px;padding:12px 24px}}
.bd{{padding:16px 24px;font-size:22px;font-weight:600;line-height:1.6;color:{surface_txt}}}
.stat{{position:absolute;left:620px;top:170px;width:280px;background:{surface};{border}border-radius:{radius}px;box-shadow:{shadow};padding:22px 26px}}
.num{{font-size:74px;font-weight:900;line-height:1.15;color:{primary};letter-spacing:1px}}
.lab{{margin-top:6px;font-size:22px;font-weight:700;color:{surface_txt};opacity:.85}}
.chip{{position:absolute;font-weight:800;font-size:24px;padding:10px 26px;border-radius:{min(radius + 8, 999)}px;box-shadow:{shadow}}}
.cap{{position:absolute;left:50%;transform:translateX(-50%);bottom:22px;font-size:24px;font-weight:700;padding:10px 36px;border-radius:{'999px' if cap.get('mode') == 'pill' else '14px'};background:{cap.get("bg", "rgba(0,0,0,.7)")};color:{cap.get("color", "#fff")};border:{cap.get("border", "none")};max-width:880px}}
</style></head><body><div id="p">{extra}
<div class="t">{st["direction"].split("（")[0]}</div>
<div class="sub">{st["genre"].split("/")[0]} · 风格样张</div>
<div class="card" style="left:60px;top:170px;width:520px"><div class="hd">卡片标题栏（强调色）</div><div class="bd">卡片质感、字幕条、胶囊标签与大数字都和成片同一套 token；配图调性：{st["photo"][:24]}…</div></div>
<div class="stat"><div class="num">13亿</div><div class="lab">风格包指标卡</div></div>
<div class="chip" style="left:60px;top:432px;background:{c1};color:{_on(c1)}">色板 A</div>
<div class="chip" style="left:210px;top:432px;background:{c2};color:{_on(c2)}">色板 B</div>
<div class="chip" style="left:360px;top:432px;background:{acc[3]};color:{_on(acc[3])}">色板 C</div>
<div class="cap">字幕条样式 · 逐词高亮</div>
</div></body></html>"""


def main() -> int:
    styles = json.load(open(REGISTRY, encoding='utf-8'))
    packs = json.load(open(LIB, encoding='utf-8')) if LIB.exists() else []
    have = {p.get('direction') for p in packs}
    now = datetime.datetime.now().astimezone().isoformat(timespec='seconds')
    added = 0
    for st in styles:
        if st['direction'] in have:
            continue
        packs.append({
            'id': f"sp-style-{st['id']}",
            'name': st['direction'],
            'direction': st['direction'],
            'desc': f"内置风格。适用题材：{st.get('genre', '通用')}。配图调性：{st.get('photo', '')}",
            'sample_html': sample_html(st),
            'sample_tag': '样张 · 风格预览',
            'origin_project': 'style-registry',
            'published': True,
            'created_at': now,
        })
        added += 1
    LIB.parent.mkdir(parents=True, exist_ok=True)
    json.dump(packs, open(LIB, 'w', encoding='utf-8'), ensure_ascii=False, indent=2)
    print(f'播种完成：新增 {added}，共 {len(packs)} 条')
    return 0


if __name__ == '__main__':
    sys.exit(main())
