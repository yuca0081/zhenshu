# -*- coding: utf-8 -*-
"""StylePack 动效片段库（手绘叙事·纸面马克笔）——v1 四引擎迁出生产时的存活子集。

唯一消费者 demo/ak-pv-repro/build_ak.py：load() 读配音对齐缓存，
pop/fade/rise/... 生成 seek 确定性的 GSAP 时间轴片段（全部走 tl，无 repeat/yoyo）。
页面模板/元素助手/字幕带已随 v1 引擎删除——build_ak 自带模板，字幕层在
ai/assemble.py 平台层实现。
"""
import json
import pathlib

_META_CACHE = {}


def load(proj_dir):
    p = pathlib.Path(proj_dir)
    if str(p) not in _META_CACHE:
        meta = json.load(open(p / 'audio_meta.json', encoding='utf-8'))
        words = {v['id']: v['words'] for v in meta['voices']}
        phrases = {}
        for v in meta['voices']:
            ph = v.get('phrases')
            if not ph:  # 旧产物无短语 → 整段一个短语（v1 行为）
                ph = [[0, len(v['words']) - 1]] if v['words'] else []
            phrases[v['id']] = ph
        # 双语字幕开关：project.json bi_subtitle 关掉时即便有旧翻译也不显示
        bi = False
        try:
            bi = bool(json.load(open(p / 'project.json', encoding='utf-8')).get('bi_subtitle'))
        except Exception:
            pass
        phrases_en = {}
        if bi:
            for v in meta['voices']:
                pe = v.get('phrases_en')
                phrases_en[v['id']] = pe if isinstance(pe, list) else []
        _META_CACHE[str(p)] = {
            'phrases_en': phrases_en,
            'words': words,
            'phrases': phrases,
            'dur': {v['id']: v['duration_s'] for v in meta['voices']},
            'scene': {v['id']: round(v['duration_s'] + 0.35, 3) for v in meta['voices']},
        }
    return _META_CACHE[str(p)]


# ── 常用 reveal 片段 ─────────────────────────────────────────
def pop(sel, t, scale=.3):
    return f"gsap.set('{sel}',{{opacity:0,scale:{scale},transformOrigin:'50% 50%'}});\n      tl.to('{sel}',{{opacity:1,scale:1,duration:.45,ease:'back.out(2)'}},{t:.2f});"


def fade(sel, t, dur=.4):
    return f"gsap.set('{sel}',{{opacity:0}});\n      tl.to('{sel}',{{opacity:1,duration:{dur},ease:'power2.out'}},{t:.2f});"


def rise(sel, t, dy=30):
    return f"gsap.set('{sel}',{{opacity:0,y:{dy},scale:.9}});\n      tl.to('{sel}',{{opacity:1,y:0,scale:1,duration:.4,ease:'back.out(1.6)'}},{t:.2f});"


def draw_x(sel, t, dur=.5):
    return f"gsap.set('{sel}',{{scaleX:0,transformOrigin:'left center'}});\n      tl.to('{sel}',{{scaleX:1,duration:{dur},ease:'power2.out'}},{t:.2f});"


def chars_reveal(sel, t, step=.22, dy=40):
    return (f"gsap.set('{sel} [data-c]',{{opacity:0,y:{dy}}});\n"
            f"      tl.to('{sel} [data-c]',{{opacity:1,y:0,duration:.3,stagger:{step},ease:'power2.out'}},{t:.2f});")


def stagger_pop(sel, t, step=.3, origin='50% 50%'):
    return (f"gsap.set('{sel}',{{opacity:0,scale:.2,transformOrigin:'{origin}'}});\n"
            f"      tl.to('{sel}',{{opacity:1,scale:1,duration:.35,stagger:{step},ease:'back.out(1.8)'}},{t:.2f});")


def stagger_grow(sel, t, step=.28, origin='center bottom'):
    return (f"gsap.set('{sel}',{{opacity:0,scaleY:.1,transformOrigin:'{origin}'}});\n"
            f"      tl.to('{sel}',{{opacity:1,scaleY:1,duration:.38,stagger:{step},ease:'power2.out'}},{t:.2f});")


# ── 动效词表 v2（slide/wipe/blur/退场；全部走 tl，seek 确定性，无 repeat/yoyo）──
def slide(sel, t, dx=90):
    """侧滑入场（dx 正=从右，负=从左）。"""
    return (f"gsap.set('{sel}',{{opacity:0,x:{dx}}});\n      "
            f"tl.to('{sel}',{{opacity:1,x:0,duration:.5,ease:'power3.out'}},{t:.2f});")


def wipe(sel, t, dur=.55):
    """clip-path 左→右揭示（斜切色带/大字板常用）。"""
    return (f"gsap.set('{sel}',{{clipPath:'inset(0% 100% 0% 0%)'}});\n      "
            f"tl.to('{sel}',{{clipPath:'inset(0% 0% 0% 0%)',duration:{dur},ease:'power2.inOut'}},{t:.2f});")


def blur_in(sel, t, dur=.5):
    """失焦到聚焦入场（氛围主视觉）。"""
    return (f"gsap.set('{sel}',{{opacity:0,filter:'blur(14px)'}});\n      "
            f"tl.to('{sel}',{{opacity:1,filter:'blur(0px)',duration:{dur},ease:'power2.out'}},{t:.2f});")


def exit_fade(sel, t, dur=.35):
    """退场：上飘淡出（讲完即退，给后续元素腾画面）。"""
    return f"tl.to('{sel}',{{opacity:0,y:'-=14',duration:{dur},ease:'power1.in'}},{t:.2f});"
