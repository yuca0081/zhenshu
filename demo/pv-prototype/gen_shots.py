# -*- coding: utf-8 -*-
"""镜头 HTML 生成器（音乐驱动 PV · 原型闸）——代演「写画面」工位。

读 shots.json（分镜工位产物原型），把每个镜头的视觉意图编译成独立
shotNN.html：1080×1920、window.__READY / window.__renderFrame(t) 契约
（render_frames.js 零依赖截帧驱动直接可渲）。动效纪律：

  - __renderFrame 是 t 的纯函数——同一 t 永远同一帧，禁累积状态；
  - 副歌动效全部量化到拍格（0.5s 吸附/每拍事件），break 段刻意去量化
    （慢漂移）——「段萟能量→画面能量」的判据要能被肉眼分辨；
  - 配色高对比（暗底亮件），吸取 pushedColor 极性事故的教训。

原型闸定位：模板代演 ≠ 真实工位（真实 M2 是 LLM 按镜头 brief 生成 HTML，
样式多样得多），这里只验证「镜头=HTML=时间轴」契约与卡点可行性。
"""
import json
import pathlib
import sys

HERE = pathlib.Path(__file__).resolve().parent

PALETTES = {
    'hot': {'BG0': '#140420', 'C0': '#ff2d78', 'C1': '#ffb300',
            'C2': '#00e5ff', 'C3': '#ffffff'},
    'cool': {'BG0': '#081522', 'C0': '#7fd8ff', 'C1': '#8affc1',
             'C2': '#c8b6ff', 'C3': '#e8f4ff'},
}

PAGE = """<!doctype html><html><head><meta charset="utf-8">
<style>
html,body{margin:0;padding:0;width:@@W@@px;height:@@H@@px;overflow:hidden;
  background:@@BG0@@;font-family:'Microsoft YaHei',sans-serif;}
#stage{position:relative;width:@@W@@px;height:@@H@@px;overflow:hidden;}
@@CSS@@
</style></head><body><div id="stage">@@HTML@@</div>
<script>
window.__READY = 1;
var BEAT = @@BEAT@@, BAR = @@BAR@@, DUR = @@DUR@@;
function clamp(v, a, b) { return Math.max(a, Math.min(b, v)); }
function eoc(x) { return 1 - Math.pow(1 - x, 3); }   // easeOutCubic
window.__renderFrame = function (t) {
@@JS@@
};
</script></body></html>
"""

# ── 镜头处理法：css / html / js 三段，@@C0@@ 等调色板与 @@TEXT@@ 文本注入 ──

TREATMENTS = {

'title_slam': dict(
    css="""
.bgA{position:absolute;inset:0;background:@@BG0@@;}
.bgB{position:absolute;inset:0;background:@@C0@@;opacity:0;}
.word{position:absolute;left:50%;top:50%;transform:translate(-50%,-50%);
  font-size:560px;font-weight:900;color:@@C3@@;
  text-shadow:0 0 90px @@C0@@,0 0 180px @@C0@@;}""",
    html="""<div class="bgA"></div><div class="bgB" id="bgB"></div>
<div class="word" id="word">@@TEXT@@</div>""",
    js="""
  var barIdx = Math.floor(t / BAR), bp = (t % BAR) / BAR;
  var w = document.getElementById('word');
  var s = 1.55 - 0.55 * eoc(clamp(bp * 3, 0, 1));       // 每小节砸入
  w.style.transform = 'translate(-50%,-50%) scale(' + s + ')';
  // 透明度给 0.25 下限：从 0 重触发会在小节边界产生整帧空内容的眨眼
  w.style.opacity = String(0.25 + 0.75 * clamp(bp * 8, 0, 1));
  var bgB = document.getElementById('bgB');
  bgB.style.opacity = (barIdx % 2 === 0)
    ? String(0.5 * (1 - clamp(bp * 2.5, 0, 1))) : '0';   // 双色随小节闪
"""),

'bars_in': dict(
    css="""
.bar{position:absolute;left:0;width:100%;height:96px;border-radius:10px;
  transform:translateX(-110%);}""",
    html=''.join(
        f'<div class="bar" id="b{i}" style="top:{330 + i * 210}px"></div>'
        for i in range(6)),
    js="""
  var idx = Math.floor(t / BEAT), bp = (t % BEAT) / BEAT;
  var cols = ['@@C1@@', '@@C0@@', '@@C2@@'];
  for (var i = 0; i < 6; i++) {
    var el = document.getElementById('b' + i);
    var enter = eoc(clamp((t - i * BEAT) / 0.45, 0, 1));  // 一拍一条入场
    el.style.transform = 'translateX(' + (-110 * (1 - enter)) + '%) scaleY('
      + (1 + 0.08 * ((idx % 6 === i) ? Math.max(0, 1 - bp * 4) : 0)) + ')';
    el.style.background = cols[i % 3];
    el.style.opacity = String(0.35 + 0.65 * enter);
  }
"""),

'rings': dict(
    css="""
.dot{position:absolute;left:50%;top:50%;width:120px;height:120px;margin:-60px
  0 0 -60px;border-radius:50%;background:@@C3@@;box-shadow:0 0 60px @@C0@@;}
.ring{position:absolute;left:50%;top:50%;border-radius:50%;border:7px solid;}""",
    html='<div class="dot" id="dot"></div>' + ''.join(
        f'<div class="ring" id="r{i}" style="border-color:{c}"></div>'
        for i, c in enumerate(['@@C0@@', '@@C1@@', '@@C2@@'])),
    js="""
  var idx = Math.floor(t / BEAT), bp = (t % BEAT) / BEAT;
  document.getElementById('dot').style.transform =
    'scale(' + (1 + 0.5 * Math.max(0, 1 - bp * 5)) + ')';   // 中心逐拍脉冲
  for (var i = 0; i < 3; i++) {
    var el = document.getElementById('r' + i);
    var age = t - (idx - i) * BEAT;                          // 每拍诞生一圈
    if (age < 0) { el.style.opacity = '0'; continue; }
    var p = clamp(age / 1.5, 0, 1), r = 90 + 560 * p;
    el.style.width = el.style.height = r * 2 + 'px';
    el.style.marginLeft = el.style.marginTop = -r + 'px';
    el.style.opacity = String(0.9 * (1 - p));
  }
"""),

'stripes': dict(
    css="""
.wrap{position:absolute;left:-30%;top:-20%;width:180%;height:150%;
  transform-origin:0 0;}
.stripe{position:absolute;left:0;width:100%;height:130px;border-radius:8px;}""",
    html='<div class="wrap" id="wrap">' + ''.join(
        f'<div class="stripe" id="s{i}" style="top:{-260 + i * 300}px;'
        f'background:{c}"></div>'
        for i, c in enumerate(['@@C0@@', '@@C1@@', '@@C2@@', '@@C0@@',
                               '@@C1@@', '@@C2@@', '@@C0@@'])) + '</div>',
    js="""
  var pos = Math.floor(t / BEAT), frac = clamp((t % BEAT) / 0.12, 0, 1);
  document.getElementById('wrap').style.transform = 'rotate(-18deg) translateX('
    + (-(pos + eoc(frac)) * 150) + 'px)';                    // 逐拍步进吸附
  for (var i = 0; i < 7; i++) {
    document.getElementById('s' + i).style.opacity =
      ((pos + i) % 2 === 0) ? '0.95' : '0.4';                // 明暗按拍交替
  }
"""),

'word_stack': dict(
    css="""
.stack{position:absolute;inset:0;display:flex;flex-direction:column;
  align-items:center;justify-content:center;gap:30px;}
.w{font-size:300px;font-weight:900;line-height:1.1;opacity:0.16;
  transition:none;}""",
    html='<div class="stack">' + ''.join(
        f'<div class="w" id="w{i}"></div>' for i in range(4)) + '</div>',
    js="""
  var step = BAR / 2;                                        // 每半小节点亮一字：
  var idx = Math.min(3, Math.floor(t / step));               // 四字×1s 恰好收满镜头，
  var sp = (t % step) / step;                                // 末帧四字全亮（按小节
  var words = @@WORDS@@;                                     // 点亮会漏后两字的账错）
  for (var i = 0; i < 4; i++) {
    var el = document.getElementById('w' + i);
    el.textContent = words[i];
    if (i < idx) { el.style.opacity = '0.95';
      el.style.transform = 'scale(1)'; el.style.color = '@@C3@@'; }
    else if (i === idx) {                                    // 点亮的字带弹入
      el.style.opacity = '1';
      el.style.transform = 'scale(' + (1 + 0.15 * Math.max(0, 1 - sp * 4)) + ')';
      el.style.color = '@@C3@@';
      el.style.textShadow = '0 0 60px @@C0@@';
    } else { el.style.opacity = '0.16'; el.style.transform = 'scale(1)';
      el.style.color = '@@C2@@'; el.style.textShadow = 'none'; }
  }
"""),

'drift': dict(
    css="""
.sky{position:absolute;inset:0;background:radial-gradient(120% 90% at 30% 20%,
  #123 0%, @@BG0@@ 70%);}
.orb{position:absolute;left:0;top:0;width:160px;height:160px;border-radius:50%;
  filter:blur(2px);}""",
    html='<div class="sky"></div>' + ''.join(
        f'<div class="orb" id="o{i}" style="background:{c};opacity:0.5;'
        f'box-shadow:0 0 70px {c}"></div>'
        for i, c in enumerate(['@@C0@@', '@@C1@@', '@@C2@@', '@@C0@@',
                               '@@C1@@', '@@C2@@', '@@C0@@', '@@C1@@'])),
    js="""
  for (var i = 0; i < 8; i++) {                              // 慢漂移·无拍点量化
    var bx = (Math.sin(i * 12.9898) * 0.5 + 0.5) * 1080;
    var by = (Math.sin(i * 78.233 + 4.0) * 0.5 + 0.5) * 1920;
    var sc = 0.45 + (Math.sin(i * 3.7) * 0.5 + 0.5) * 0.8;
    var x = bx + 70 * Math.sin(t * 0.31 + i * 1.7);
    var y = by + 50 * Math.sin(t * 0.24 + i * 2.3);
    document.getElementById('o' + i).style.transform =
      'translate(' + x + 'px,' + y + 'px) scale('
      + (sc * (1 + 0.1 * Math.sin(t * 0.5 + i))) + ')';
  }
"""),

'line_draw': dict(
    css="""
.line{position:absolute;left:50%;top:50%;transform:translateX(-50%);height:7px;
  width:8%;border-radius:4px;
  background:linear-gradient(90deg,@@C0@@,@@C1@@);
  box-shadow:0 0 50px @@C0@@;}
.word{position:absolute;left:50%;top:44%;transform:translateX(-50%);
  font-size:150px;font-weight:900;color:@@C0@@;opacity:0.5;}""",
    html='<div class="word" id="word">@@TEXT@@</div>'
         '<div class="line" id="line"></div>',
    js="""
  var g = eoc(clamp(t / (DUR - 0.5), 0, 1));                // 整镜头单调生长
  document.getElementById('line').style.width = (8 + 82 * g) + '%';
  document.getElementById('line').style.opacity =
    String(0.65 + 0.35 * Math.sin(t * 1.3));                // 呼吸辉光
  document.getElementById('word').style.opacity =
    String(0.45 + 0.4 * (0.5 + 0.5 * Math.sin(t * 0.9 + 1)));
"""),

'color_flip': dict(
    css="""
.cbg{position:absolute;inset:0;}
.num{position:absolute;left:50%;top:50%;transform:translate(-50%,-50%);
  font-size:520px;font-weight:900;}
.prog{position:absolute;left:0;bottom:120px;height:10px;background:@@C3@@;
  width:0;}""",
    html='<div class="cbg" id="bg"></div><div class="num" id="num"></div>'
         '<div class="prog" id="prog"></div>',
    js="""
  var barIdx = Math.floor(t / BAR), bp = (t % BAR) / BAR;
  var cols = ['@@C0@@', '@@C1@@', '@@C2@@', '@@C0@@'];
  var fg = ['@@C3@@', '@@BG0@@', '@@BG0@@', '@@C3@@'];
  document.getElementById('bg').style.background = cols[barIdx % 4];  // 逐小节硬翻
  var num = document.getElementById('num');
  num.textContent = ('0' + (barIdx + 1)).slice(-2);
  num.style.color = fg[barIdx % 4];
  num.style.transform = 'translate(-50%,-50%) scale('
    + (1 + 0.22 * Math.max(0, 1 - bp * 5)) + ')';
  document.getElementById('prog').style.width = (t / DUR * 100) + '%';
"""),

'orbit': dict(
    css="""
.ering{position:absolute;left:50%;top:50%;width:860px;height:1280px;
  margin:-640px 0 0 -430px;border-radius:50%;border:3px solid @@C2@@;
  opacity:0.35;}
.ob{position:absolute;left:0;top:0;width:84px;height:84px;border-radius:50%;}""",
    html='<div class="ering" id="ering"></div>' + ''.join(
        f'<div class="ob" id="ob{i}" style="background:{c};'
        f'box-shadow:0 0 50px {c}"></div>'
        for i, c in enumerate(['@@C0@@', '@@C1@@', '@@C2@@', '@@C3@@'])),
    js="""
  var pos = Math.floor(t / BEAT), frac = clamp((t % BEAT) / 0.14, 0, 1);
  var bpb = (t % BAR) / BAR;
  var ang = (pos + eoc(frac)) * Math.PI / 2;                 // 角度吸附到拍
  for (var i = 0; i < 4; i++) {
    var a = ang + i * Math.PI / 2;
    var x = 540 + 430 * Math.cos(a), y = 960 + 640 * Math.sin(a);
    document.getElementById('ob' + i).style.transform =
      'translate(' + (x - 42) + 'px,' + (y - 42) + 'px)';
  }
  document.getElementById('ering').style.transform =
    'scale(' + (1 + 0.05 * Math.max(0, 1 - bpb * 4)) + ')';  // 重拍环脉冲
"""),

'finale_pulse': dict(
    css="""
.inner{position:absolute;inset:0;background:radial-gradient(100% 70% at 50%
  45%, #2a0a3a 0%, @@BG0@@ 75%);}
.fword{position:absolute;left:50%;top:50%;transform:translate(-50%,-50%);
  font-size:620px;font-weight:900;color:@@C3@@;
  text-shadow:0 0 110px @@C0@@;}
.ticks{position:absolute;left:50%;bottom:160px;transform:translateX(-50%);
  display:flex;gap:22px;}
.tk{width:70px;height:14px;border-radius:7px;background:@@C3@@;opacity:0.2;}""",
    html='<div class="inner" id="inner"></div>'
         '<div class="fword" id="word">@@TEXT@@</div>'
         '<div class="ticks">' + ''.join(
             f'<div class="tk" id="tk{i}"></div>' for i in range(4))
         + '</div>',
    js="""
  var bp = (t % BEAT) / BEAT, barIdx = Math.floor(t / BAR);
  var bpb = (t % BAR) / BAR;
  var pulse = Math.max(0, 1 - bp * 3);                       // 逐拍脉冲衰减
  var inner = document.getElementById('inner');
  inner.style.transform = 'scale(' + (1 + 0.05 * pulse) + ')';
  inner.style.filter = 'brightness(' + (1 + 0.3 * pulse
    * (barIdx % 2 === 0 ? 1 : 0.5)) + ')';
  var w = document.getElementById('word');
  w.style.transform = 'translate(-50%,-50%) scale(' + (1 + 0.12 * pulse) + ')';
  w.style.opacity = String(0.7 + 0.3 * Math.max(0, 1 - bpb * 1.2));
  var on = Math.floor(t / BEAT) % 4;
  for (var i = 0; i < 4; i++) {
    document.getElementById('tk' + i).style.opacity = (on >= i) ? '1' : '0.2';
  }
"""),
}


def main() -> int:
    for stream in (sys.stdout, sys.stderr):
        if hasattr(stream, 'reconfigure'):
            stream.reconfigure(errors='replace')
    spec = json.loads((HERE / 'shots.json').read_text(encoding='utf-8'))
    meta = spec['meta']

    bar_s = meta['bar']
    for shot in spec['shots']:
        tr = TREATMENTS.get(shot['style'])
        if tr is None:
            print(f"{shot['id']}: 未知处理法 {shot['style']}")
            return 1
        # 小节账对齐：时长必须 = 小节数 × 每小节秒数（分镜工位的机械不变量）
        want = (shot['bar1'] - shot['bar0']) * bar_s
        if abs((shot['t1'] - shot['t0']) - want) > 1e-6:
            print(f"{shot['id']}: 时长 {shot['t1']-shot['t0']} ≠ 小节账 {want}")
            return 1
        pal = PALETTES[shot['palette']]
        page = (PAGE
                .replace('@@CSS@@', tr['css'])
                .replace('@@HTML@@', tr['html'])
                .replace('@@JS@@', tr['js'])
                .replace('@@W@@', str(meta['width']))
                .replace('@@H@@', str(meta['height']))
                .replace('@@BEAT@@', str(meta['beat']))
                .replace('@@BAR@@', str(bar_s))
                .replace('@@DUR@@', str(shot['t1'] - shot['t0']))
                .replace('@@TEXT@@', shot.get('text', ''))
                .replace('@@WORDS@@', json.dumps(
                    shot.get('words', []), ensure_ascii=False)))
        for k, v in pal.items():
            page = page.replace(f'@@{k}@@', v)
        out = HERE / shot['file']
        out.parent.mkdir(parents=True, exist_ok=True)
        out.write_text(page, encoding='utf-8')
        print(f"{shot['id']}: {shot['style']} → {out.relative_to(HERE)} "
              f"({shot['t0']}–{shot['t1']}s, {shot['section']}) ✓")
    print(f"共 {len(spec['shots'])} 个镜头 HTML 生成完毕")
    return 0


if __name__ == '__main__':
    sys.exit(main())
