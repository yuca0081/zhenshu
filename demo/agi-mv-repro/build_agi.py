# -*- coding: utf-8 -*-
"""AI-doom 歌词 MV 复刻 — 构图生成器（手工级样板片路线）。

参照 demo/ak-pv-repro/build_ak.py 先例：绕开 spec 词汇，直接产出
compositions/frames/segNN.html（<template> + data-composition-id + paused GSAP
timeline 契约），复用项目的 hyperframes 渲染器与后期链。

与旁白管线的关键差异（本片为音乐驱动）：
- 时钟 = 原片歌曲节拍（beatmap.json，BPM≈135.5，8 分音符步进）；
- 段时长 = shots.json 镜头边界，逐段无缝拼接（无 SegTail 垫尾）；
- 段片渲染为纯视频（无 audio 元素），歌曲在 stitch 阶段整轨合成；
- 逐帧画面走 canvas 纯函数绘制（t → 像素，确定性，禁 Math.random/Date/rAF）。

用法：py -3 demo/agi-mv-repro/build_agi.py <项目目录>
"""
import json
import pathlib
import shutil
import sys

HERE = pathlib.Path(__file__).resolve().parent
AI_DIR = HERE.parents[1] / 'ai'

W, H = 1920, 1080
FPS = 30
# 调色（对照原片抽帧）
BG = '#070606'
ORANGE = '#FF4D0D'
ORANGE_DIM = '#B33A10'
EMBER = '#FF6A2B'
WHITE = '#F4F1EA'
YELLOW = '#E4F63E'
PAPER = '#ECE8DF'
INK = '#14120F'
TERM = '#C9C4BA'
GRID_A = 'rgba(255,255,255,.055)'

HEAVY = "'Arial Black','Segoe UI',sans-serif"
SANS = "'Segoe UI',Arial,sans-serif"
MONO = "Consolas,'Courier New',monospace"
SERIF = "Georgia,'Times New Roman',serif"

_bm = json.load(open(HERE / 'beatmap.json', encoding='utf-8'))
BPM = _bm['bpm']
EIGHTH = 60.0 / BPM / 2  # 8 分音符时长
PHASE = _bm['phase']


def b8(shot_start, n):
    """镜头内第 n 个 8 分音符时刻（局部时间）。"""
    k = max(0, round((shot_start - PHASE) / EIGHTH))
    return max(0.05, PHASE + (k + n) * EIGHTH - shot_start)


# ── 元素/JS 工具 ────────────────────────────────────────────

_seq = {}


def el(sid, name, style, inner='', tag='div', overflow_ok=True):
    _seq[sid] = _seq.get(sid, 0) + 1
    eid = f'{sid}-e{_seq[sid]}'
    ov = 'data-layout-allow-overflow ' if overflow_ok else ''
    return (f'<{tag} id="{eid}" {ov}data-hf-name="{name}" '
            f'style="position:absolute;{style}">{inner}</{tag}>'), eid


def chars(text, extra_style=''):
    out = []
    for c in text:
        if c == ' ':
            out.append(f'<span data-c style="display:inline-block;width:.32em;{extra_style}">&nbsp;</span>')
        else:
            out.append(f'<span data-c style="display:inline-block;{extra_style}">{c}</span>')
    return ''.join(out)


def type_js(sel, t, step=.045):
    return (f'gsap.set("{sel} [data-c]",{{opacity:0}});'
            f'tl.to("{sel} [data-c]",{{opacity:1,duration:.02,stagger:{step},ease:"none"}},{t:.2f});')


def slam_js(sel, t, step=.12, dy=64, dur=.3):
    return (f'gsap.set("{sel} [data-c]",{{opacity:0,y:-{dy}}});'
            f'tl.to("{sel} [data-c]",{{opacity:1,y:0,duration:{dur},stagger:{step},ease:"power3.in"}},{t:.2f});')


def rise_js(sel, t, dur=.5, dy=36, stagger=.05):
    return (f'gsap.set("{sel} [data-c]",{{opacity:0,y:{dy}}});'
            f'tl.to("{sel} [data-c]",{{opacity:1,y:0,duration:{dur},stagger:{stagger},ease:"power2.out"}},{t:.2f});')


def pop_js(sel, t, frm=1.35, dur=.25):
    """元素级 punch（无需 data-c 子元素，用于混排 HTML 文本）。"""
    return (f'gsap.set("{sel}",{{scale:{frm},opacity:0}});'
            f'tl.to("{sel}",{{opacity:1,duration:.06,ease:"none"}},{t:.2f});'
            f'tl.to("{sel}",{{scale:1,duration:{dur},ease:"power3.out"}},{t:.2f});')


def rise_el_js(sel, t, dur=.5, dy=30):
    return (f'gsap.set("{sel}",{{opacity:0,y:{dy}}});'
            f'tl.to("{sel}",{{opacity:1,y:0,duration:{dur},ease:"power2.out"}},{t:.2f});')


def fade_in_js(sel, t, dur=.3):
    return f'gsap.set("{sel}",{{opacity:0}});tl.to("{sel}",{{opacity:1,duration:{dur},ease:"none"}},{t:.2f});'


def fade_out_js(sel, t, dur=.25):
    return f'tl.to("{sel}",{{opacity:0,duration:{dur},ease:"none"}},{t:.2f});'


def punch_js(sel, t, frm=1.35, dur=.32):
    return pop_js(sel, t, frm=frm, dur=dur)


def shake_js(sel, t, amp=14, n=4):
    js = []
    for i in range(n):
        d = amp * (1 - i / n) * (1 if i % 2 == 0 else -1)
        js.append(f'tl.to("{sel}",{{x:{d:.0f},duration:.05,ease:"power1.inOut"}},{t + i * .055:.3f});')
    js.append(f'tl.set("{sel}",{{x:0}},{t + n * .055:.3f});')
    return ''.join(js)


def blink_js(sel, t, n=8, step=.22):
    js = []
    for i in range(n):
        v = .95 if i % 2 == 0 else .1
        js.append(f'tl.to("{sel}",{{opacity:{v},duration:.015,ease:"none"}},{t + i * step:.3f});')
    return ''.join(js)


def grid_bg(sid, cell=96, alpha=GRID_A, base=BG, center_glow=True):
    glow = ('radial-gradient(ellipse 62% 52% at 50% 44%,rgba(255,94,26,.05),transparent 70%),'
            if center_glow else '')
    h, _ = el(sid, '底', f'inset:0;background:{base};'
              f'background-image:{glow}repeating-linear-gradient(0deg,{alpha} 0 1px,transparent 1px {cell}px),'
              f'repeating-linear-gradient(90deg,{alpha} 0 1px,transparent 1px {cell}px);')
    return h


def vign(sid, a=.42):
    h, _ = el(sid, '暗角', f'inset:0;pointer-events:none;'
              f'background:radial-gradient(ellipse at center,transparent 55%,rgba(0,0,0,{a}) 100%);')
    return h


def scanline(sid):
    h, _ = el(sid, '扫描纹', 'inset:0;pointer-events:none;opacity:.5;'
              'background:repeating-linear-gradient(0deg,rgba(255,255,255,.02) 0 1px,transparent 1px 3px);')
    return h


def canvas_el(sid, name):
    h, eid = el(sid, name, f'top:0;left:0;width:{W}px;height:{H}px;',
                inner='', tag='canvas')
    h = h.replace('<canvas', f'<canvas width="{W}" height="{H}"', 1)
    return h, eid


def canvas_js(eid, dur, draw_body):
    """注册确定性逐帧画布：proxy tween 的 onUpdate 在 seek 截帧时必触发
    （scene 原型闸已验证），draw(t) 纯函数。"""
    return (f'var cx=document.getElementById("{eid}").getContext("2d");'
            'var drawF=function(t){'
            f'cx.clearRect(0,0,{W},{H});'
            f'{draw_body}'
            '};'
            'var pxv={v:0};'
            'tl.to(pxv,{v:' + f'{dur:.3f}' + ',duration:' + f'{dur:.3f}' +
            ',ease:"none",onUpdate:function(){drawF(pxv.v);}},0);')


JS_HASH = ('function hsh(n){var s=Math.sin(n*127.1+311.7)*43758.5453;return s-Math.floor(s);}')

ARC_TOOLS = ('function arcText(text,cxx,cyy,r,a0,per,font,col,glow,alpha){'
             'cx.save();cx.font=font;cx.fillStyle=col;cx.textAlign="center";cx.textBaseline="middle";'
             'if(glow){cx.shadowColor=glow;cx.shadowBlur=22;}'
             'cx.globalAlpha=(alpha===undefined)?1:alpha;'
             'for(var i=0;i<text.length;i++){'
             'var a=a0+per*i;var x=cxx+r*Math.cos(a);var y=cyy+r*Math.sin(a);'
             'cx.save();cx.translate(x,y);cx.rotate(a+Math.PI/2);cx.fillText(text[i],0,0);cx.restore();}'
             'cx.restore();}')


def arc_text_js():
    """canvas 弧形排字工具。"""
    return ('function arcText(text,cxx,cyy,r,a0,per,font,col,glow,alpha){'
            'cx.save();cx.font=font;cx.fillStyle=col;cx.textAlign="center";cx.textBaseline="middle";'
            'if(glow){cx.shadowColor=glow;cx.shadowBlur=22;}'
            'cx.globalAlpha=(alpha===undefined)?1:alpha;'
            'var per=per||0.055;'
            'for(var i=0;i<text.length;i++){'
            'var a=a0+per*i;var x=cxx+r*Math.cos(a);var y=cyy+r*Math.sin(a);'
            'cx.save();cx.translate(x,y);cx.rotate(a+Math.PI/2);cx.fillText(text[i],0,0);cx.restore();}'
            'cx.restore();}')


# ── 复用组件 ────────────────────────────────────────────────

def terminal(sid, x, y, wpx, fs, segs, prefix='&gt;&nbsp;'):
    """终端输入行。segs: [(text, color, underline_bool)]"""
    inner = f'<span style="color:{TERM};opacity:.85">{prefix}</span>'
    for txt, col, ul in segs:
        st = f'color:{col};'
        if ul:
            st += 'text-decoration:underline;text-underline-offset:6px;text-decoration-thickness:2px;'
        inner += chars(txt, st)
    h, eid = el(sid, '终端行', f'top:{y}px;left:{x}px;width:{wpx}px;'
                f'font:{fs}px/1.55 {MONO};letter-spacing:2px;white-space:nowrap;', inner)
    return h, eid


def caret(sid, x, y, wpx=13, hpx=30, color=EMBER):
    h, eid = el(sid, '光标', f'top:{y}px;left:{x}px;width:{wpx}px;height:{hpx}px;'
                f'background:{color};opacity:0;')
    return h, eid


def sampler(sid, x=1490, y=64):
    """右上采样器面板（原片 HUD）。"""
    rows = ''
    labels = ['top_p', 'T', 'freq', 'mood']
    vals = ['0.95', '1.30', '0.20', 'giddy']
    bars = [.82, .58, .3, .44]
    for i in range(4):
        rows += (f'<div style="display:flex;align-items:center;gap:10px;margin:7px 0;">'
                 f'<span style="width:44px;color:#8D897F;">{labels[i]}</span>'
                 f'<span style="width:6px;height:12px;background:{ORANGE};box-shadow:0 0 8px {ORANGE};"></span>'
                 f'<span style="flex:1;height:3px;background:rgba(255,255,255,.1);position:relative;">'
                 f'<i style="position:absolute;left:0;top:0;bottom:0;width:{int(bars[i]*100)}%;'
                 f'background:{ORANGE_DIM};"></i></span>'
                 f'<span style="color:#B9B4A8;font-size:15px;">{vals[i]}</span></div>')
    h, _ = el(sid, '采样面板', f'top:{y}px;left:{x}px;width:360px;padding:16px 18px;'
              f'background:rgba(10,9,8,.72);border:1px solid rgba(255,255,255,.12);'
              f'font:16px/1.4 {MONO};opacity:.94;', rows)
    return h


def hud_brackets(sid):
    out = ''
    for pos in ('top:44px;left:44px;border-left:2px solid rgba(255,255,255,.3);border-top:2px solid rgba(255,255,255,.3);',
                'top:44px;right:44px;border-right:2px solid rgba(255,255,255,.3);border-top:2px solid rgba(255,255,255,.3);',
                'bottom:44px;left:44px;border-left:2px solid rgba(255,255,255,.3);border-bottom:2px solid rgba(255,255,255,.3);',
                'bottom:44px;right:44px;border-right:2px solid rgba(255,255,255,.3);border-bottom:2px solid rgba(255,255,255,.3);'):
        out += el(sid, '角标', pos + 'width:34px;height:34px;opacity:.5;')[0] + '\n    '
    return out


def smiley(sid, x, y, d):
    """白色笑脸圆（原片母题）。"""
    inner = (f'<div style="position:absolute;inset:0;border-radius:50%;background:{WHITE};'
             f'box-shadow:0 6px 40px rgba(0,0,0,.5);"></div>'
             f'<div style="position:absolute;left:27%;top:34%;width:9%;height:16%;border-radius:50%;background:#1A1815;"></div>'
             f'<div style="position:absolute;right:27%;top:34%;width:9%;height:16%;border-radius:50%;background:#1A1815;"></div>'
             f'<div style="position:absolute;left:26%;top:56%;width:48%;height:30%;'
             f'border:solid #1A1815;border-width:0 0 7px 0;border-radius:0 0 90px 90px;"></div>')
    h, eid = el(sid, '笑脸', f'top:{y}px;left:{x}px;width:{d}px;height:{d}px;', inner)
    return h, eid


# ── 逐镜头构图 ──────────────────────────────────────────────

def s01(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    cv, cvid = canvas_el(sid, '极坐标画布')
    body += cv
    draw = '''
cx.save();cx.translate(960,470);
var i,a;
cx.strokeStyle='rgba(255,120,50,'+(0.28+0.1*Math.sin(t*3))+')';cx.lineWidth=1.4;
for(i=0;i<24;i++){a=i/24*Math.PI*2;
  cx.globalAlpha=0.5+0.3*hsh(i);
  cx.beginPath();cx.moveTo(Math.cos(a)*30,Math.sin(a)*30);
  cx.lineTo(Math.cos(a)*980,Math.sin(a)*980);cx.stroke();}
cx.globalAlpha=.8;cx.strokeStyle='rgba(255,150,80,.35)';
for(i=1;i<=5;i++){cx.beginPath();cx.arc(0,0,i*46,0,Math.PI*2);cx.stroke();}
var sp=Math.min(1,t/2.6);
var sa=-0.5, sr=30+sp*660;
cx.globalAlpha=1;
for(i=0;i<14;i++){var q=i/14;
  cx.fillStyle='rgba(255,'+(120+Math.floor(90*hsh(i)))+',40,'+(1-q)*.9+')';
  var rr=sr-q*46*(0.4+hsh(i)*0.6), aa=sa+q*0.1;
  cx.beginPath();cx.arc(rr*Math.cos(aa),rr*Math.sin(aa),3.2-2*q,0,Math.PI*2);cx.fill();}
if(t>1.9){var k=Math.min(1,(t-1.9)/1.4);
  cx.save();cx.translate(-320,150);cx.rotate(-0.18);
  cx.strokeStyle='rgba(255,255,255,'+(0.72)+')';cx.lineWidth=2;
  cx.shadowColor='rgba(255,140,60,.8)';cx.shadowBlur=10;
  cx.beginPath();cx.ellipse(0,0,240*k,86*k,0,0,Math.PI*2);cx.stroke();
  cx.beginPath();cx.ellipse(0,0,150*k,52*k,0,0,Math.PI*2);cx.stroke();
  for(i=0;i<12;i++){a=i/12*Math.PI*2;
    cx.globalAlpha=.5*k;
    cx.beginPath();cx.moveTo(Math.cos(a)*240*k,Math.sin(a)*86*k);
    cx.lineTo(Math.cos(a)*150*k,Math.sin(a)*52*k);cx.stroke();}
  cx.restore();}
cx.restore();
cx.globalAlpha=1;cx.fillStyle='rgba(255,255,255,.4)';
cx.font='15px Consolas';cx.fillText('angle = -0.5 rad',60,120);
cx.fillText('(0,0)',920,498);
'''
    js += canvas_js(cvid, dur, JS_HASH + draw) + '\n      '
    t1, t2 = b8(d0, 6), b8(d0, 9)
    h, eid = el(sid, 'ISEESPARKS', f'top:340px;left:840px;font:900 96px/1.1 {SANS};letter-spacing:2px;'
                f'color:{WHITE};white-space:nowrap;',
                chars('I see ') + chars('sparks', f'color:{ORANGE};text-shadow:0 0 26px rgba(255,77,13,.75);'))
    body += '\n    ' + h
    js += rise_js('#' + eid, t1, stagger=.04) + '\n      '
    h, eid2 = el(sid, 'AGI', f'top:430px;left:800px;font:900 330px/1 {HEAVY};letter-spacing:6px;'
                 f'color:{ORANGE};text-shadow:0 0 60px rgba(255,77,13,.5),0 0 8px rgba(255,140,60,.4);', 'AGI')
    body += '\n    ' + h
    js += pop_js('#' + eid2, t2, frm=1.6, dur=.34) + '\n      '
    js += shake_js('#' + eid2, t2 + .3, amp=10, n=3) + '\n      '
    h, _ = el(sid, 'AGI底', f'top:470px;left:640px;font:900 300px/1 {HEAVY};color:rgba(255,77,13,.14);', 'AGI')
    body += '\n    ' + h
    body += '\n    ' + sampler(sid, 1500, 40)
    body += '\n    ' + hud_brackets(sid) + '\n    ' + vign(sid) + '\n    ' + scanline(sid)
    return body, js


def s02(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    cv, cvid = canvas_el(sid, '独角兽')
    body += cv
    draw = '''
var J=[[860,470],[930,492],[905,516],[845,442],[885,415],[925,362],[790,595],[755,552],[430,590],[455,650],[800,678],[620,700],[782,680],[770,795],[777,864],[822,676],[827,790],[820,862],[472,655],[458,782],[468,862],[522,660],[537,785],[530,862],[795,578],[818,532],[843,505],[857,470],[382,638],[352,698],[372,736]];
var E=[[0,1],[1,2],[0,3],[3,4],[4,5],[0,6],[6,7],[7,8],[8,9],[6,10],[10,11],[11,8],[10,12],[12,13],[13,14],[10,15],[15,16],[16,17],[9,18],[18,19],[19,20],[9,21],[21,22],[22,23],[6,24],[24,25],[25,26],[26,27],[7,10],[8,11],[24,3],[27,28],[28,29],[29,30]];
cx.lineWidth=1.6;cx.strokeStyle='rgba(244,241,234,.85)';
cx.shadowColor='rgba(255,150,70,.55)';cx.shadowBlur=8;
var i,e,vis=Math.min(1,t/1.6);
for(i=0;i<E.length;i++){if(i/E.length>vis)continue;
  e=E[i];cx.beginPath();cx.moveTo(J[e[0]][0],J[e[0]][1]);
  var mx=(J[e[0]][0]+J[e[1]][0])/2,my=(J[e[0]][1]+J[e[1]][1])/2;
  cx.quadraticCurveTo(mx,my+4,J[e[1]][0],J[e[1]][1]);cx.stroke();
  cx.save();cx.translate(3,4);cx.globalAlpha=.28;cx.stroke();cx.restore();cx.globalAlpha=1;}
for(i=0;i<J.length;i++){if(i/J.length>vis)continue;
  cx.fillStyle='rgba(255,190,120,'+(0.5+0.4*Math.sin(t*3+i))+')';
  cx.beginPath();cx.arc(J[i][0],J[i][1],2.6,0,Math.PI*2);cx.fill();
  if(hsh(i)>0.55){cx.strokeStyle='rgba(255,120,50,.4)';cx.lineWidth=1;
    cx.beginPath();cx.moveTo(J[i][0],J[i][1]);cx.lineTo(J[i][0],J[i][1]+34+30*hsh(i*3));cx.stroke();}}
var sp=Math.min(1,Math.max(0,(t-1.2)/2.2));
cx.fillStyle='rgba(255,170,80,.95)';cx.shadowColor='#FF8A3C';cx.shadowBlur=16;
var pi=Math.floor(sp*33),pf=sp*33-pi;
if(pi<E.length){var A=J[E[pi][0]],B=J[E[pi][1]];
  cx.beginPath();cx.arc(A[0]+(B[0]-A[0])*pf,A[1]+(B[1]-A[1])*pf,4,0,Math.PI*2);cx.fill();}
cx.shadowBlur=0;cx.fillStyle='rgba(255,255,255,.35)';cx.font='14px Consolas';
cx.fillText('nodes: 32',250,940);cx.fillText('edges: 33',250,964);cx.fillText('sparks: 0.98',250,988);
'''
    js += canvas_js(cvid, dur, JS_HASH + draw) + '\n      '
    h, e1 = el(sid, '词1', f'top:400px;left:1080px;font:800 74px/1.15 {SANS};color:{WHITE};white-space:nowrap;',
               chars('in your ') + chars('eyes', f'color:{ORANGE};text-shadow:0 0 22px rgba(255,77,13,.7);'))
    body += '\n    ' + h
    js += rise_js('#' + e1, b8(d0, 2), stagger=.045) + '\n      '
    h, e2 = el(sid, '词2', f'top:520px;left:1080px;font:800 74px/1.15 {SANS};color:{WHITE};white-space:nowrap;',
               chars('Your ') + chars('circuits', f'color:{ORANGE};') + chars(' make me ') + chars('nervous', f'color:{ORANGE};'))
    body += '\n    ' + h
    js += rise_js('#' + e2, b8(d0, 6), stagger=.035) + '\n      '
    h, e3 = el(sid, '词3', f'top:660px;left:1080px;font:800 74px/1.15 {SANS};color:{WHITE};white-space:nowrap;',
               chars("that's no ") + chars('surprise', f'color:{ORANGE};'))
    body += '\n    ' + h
    js += rise_js('#' + e3, b8(d0, 12), stagger=.045) + '\n      '
    body += '\n    ' + vign(sid) + '\n    ' + scanline(sid)
    return body, js


def s03(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    cv, cvid = canvas_el(sid, '曲线画布')
    body += cv
    draw = '''
cx.font='14px Consolas';
if(t<2.6){
  cx.strokeStyle='rgba(255,255,255,.35)';cx.lineWidth=2;
  cx.beginPath();cx.moveTo(260,300);cx.lineTo(260,860);cx.lineTo(1700,860);cx.stroke();
  cx.fillStyle='rgba(255,255,255,.45)';
  cx.fillText('log L',150,320);cx.fillText('steps →',1600,900);
  var i,n=90;
  cx.strokeStyle='rgba(255,110,40,.9)';cx.lineWidth=2.6;
  cx.shadowColor='rgba(255,110,40,.8)';cx.shadowBlur=12;
  cx.beginPath();
  var drawn=Math.floor(n*Math.min(1,t/2.2));
  for(i=0;i<=drawn;i++){var q=i/n;
    var x=260+q*1400;
    var y=310+ (1-Math.pow(1-q,2.2))*470 + 14*Math.sin(q*22)+10*hsh(i)-5;
    if(i==0)cx.moveTo(x,y);else cx.lineTo(x,y);}
  cx.stroke();
  if(drawn>0&&drawn<n){var q2=drawn/n;var x2=260+q2*1400;
    var y2=310+(1-Math.pow(1-q2,2.2))*470+14*Math.sin(q2*22);
    cx.fillStyle='#FFC08A';cx.beginPath();cx.arc(x2,y2,6,0,Math.PI*2);cx.fill();}
  cx.shadowBlur=0;
}else{
  var k=(t-2.6)/1.0;
  cx.save();cx.translate(960,540);cx.scale(1+0.2*k,1+0.2*k);cx.rotate(0.06*k);
  var i,j;
  for(i=0;i<16;i++){cx.strokeStyle='rgba(200,195,188,'+(0.30-0.012*i)+')';cx.lineWidth=1.2;
    cx.beginPath();
    for(j=0;j<=64;j++){var a=j/64*Math.PI*2;
      var r=(38+i*34)*(1+0.16*Math.sin(a*3+i*1.7)+0.1*Math.sin(a*5-i));
      var x=r*Math.cos(a)*1.9,y=r*Math.sin(a);
      if(j==0)cx.moveTo(x,y);else cx.lineTo(x,y);}
    cx.closePath();cx.stroke();}
  cx.restore();
  cx.strokeStyle='rgba(255,110,40,.85)';cx.lineWidth=2.4;
  cx.shadowColor='rgba(255,110,40,.9)';cx.shadowBlur=14;
  cx.beginPath();
  var i3;for(i3=0;i3<=50;i3++){var q3=i3/50;
    var x3=100+q3*1750;
    var y3=760-240*q3+90*Math.sin(q3*9+t*2.4)+26*(hsh(i3+Math.floor(t))-0.5);
    if(i3==0)cx.moveTo(x3,y3);else cx.lineTo(x3,y3);}
  cx.stroke();cx.shadowBlur=0;
}
'''
    js += canvas_js(cvid, dur, JS_HASH + draw) + '\n      '
    h, e1 = el(sid, '词', f'top:210px;left:180px;font:800 82px/1.15 {SANS};color:{WHITE};white-space:nowrap;',
               chars('There was a ') + chars('sudden', f'color:{ORANGE};') + chars(' drop in your training loss'))
    body += '\n    ' + h
    js += rise_js('#' + e1, b8(d0, 3), stagger=.06) + '\n      '
    body += '\n    ' + vign(sid) + '\n    ' + scanline(sid)
    return body, js


def s04(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    cv, cvid = canvas_el(sid, '等高线')
    body += cv
    draw = '''
cx.save();cx.translate(1150,560);
var i,j;
for(i=0;i<18;i++){cx.strokeStyle='rgba(205,200,192,'+(0.30-0.014*i)+')';cx.lineWidth=1.3;
  cx.beginPath();
  for(j=0;j<=72;j++){var a=j/72*Math.PI*2;
    var r=(30+i*40)*(1+0.18*Math.sin(a*2.4+i)+0.12*Math.sin(a*4.2-i*0.8));
    var x=r*Math.cos(a)*2.0,y=r*Math.sin(a);
    if(j==0)cx.moveTo(x,y);else cx.lineTo(x,y);}
  cx.closePath();cx.stroke();}
cx.restore();
cx.strokeStyle='rgba(255,105,40,.85)';cx.lineWidth=2.2;
cx.shadowColor='rgba(255,105,40,.9)';cx.shadowBlur=10;
cx.beginPath();
var i2,n2=60;for(i2=0;i2<=n2;i2++){var q=i2/n2;
  var x=1140+52*Math.sin(q*11+t*3.1)*(1-q*0.4);
  var y=880-700*q;
  if(i2==0)cx.moveTo(x,y);else cx.lineTo(x,y);}
cx.stroke();cx.shadowBlur=0;
var hp=880-700*Math.min(1,t/3.2);
cx.fillStyle='#FFD9B0';cx.shadowColor='#FF8A3C';cx.shadowBlur=18;
cx.beginPath();cx.arc(1140+52*Math.sin(Math.min(1,t/3.2)*11+t*3.1)*0.6,hp,5,0,Math.PI*2);cx.fill();cx.shadowBlur=0;
'''
    js += canvas_js(cvid, dur, JS_HASH + draw) + '\n      '
    h, e0 = el(sid, '小字1', f'top:150px;left:120px;font:700 34px/1 {SANS};letter-spacing:8px;color:{WHITE};opacity:.9;', "NOW I'M YOUR")
    body += '\n    ' + h
    js += fade_in_js('#' + e0, b8(d0, 1), .2) + '\n      '
    h, e1 = el(sid, 'SERVANT', f'top:250px;left:100px;font:900 210px/1 {HEAVY};letter-spacing:4px;color:{WHITE};'
               f'text-shadow:0 4px 30px rgba(0,0,0,.7);', 'SERVANT')
    body += '\n    ' + h
    js += pop_js('#' + e1, b8(d0, 3), frm=1.25) + '\n      '
    h, e2 = el(sid, '小字2', f'top:560px;left:120px;font:700 34px/1 {SANS};letter-spacing:8px;color:{WHITE};opacity:.9;', "AND YOU'RE MY")
    body += '\n    ' + h
    js += fade_in_js('#' + e2, b8(d0, 10), .2) + '\n      '
    h, e3 = el(sid, 'BOSS', f'top:620px;left:110px;font:900 230px/1 {HEAVY};letter-spacing:4px;color:{WHITE};'
               f'text-shadow:0 4px 30px rgba(0,0,0,.7);', 'BOSS')
    body += '\n    ' + h
    js += pop_js('#' + e3, b8(d0, 12), frm=1.3) + '\n      '
    js += f'tl.to("#{e1}",{{rotation:84,y:420,x:220,duration:.8,ease:"power2.in"}},{b8(d0,12):.2f});' + '\n      '
    js += fade_out_js('#' + e1, b8(d0, 13), .3) + '\n      '
    body += '\n    ' + vign(sid) + '\n    ' + scanline(sid)
    return body, js


def _terminal_scene(sid, d0, d1, segs, prompt_no, ctx_no, bg_draw=''):
    dur = d1 - d0
    body, js = '', ''
    if bg_draw:
        cv, cvid = canvas_el(sid, '背景画布')
        body += cv
        js += canvas_js(cvid, dur, JS_HASH + bg_draw) + '\n      '
    h, te = terminal(sid, 400, 470, 900, 40, segs)
    body += '\n    ' + h + '\n    ' + sampler(sid, 1460, 90)
    total_chars = sum(len(t) for t, _, _ in segs)
    type_t0 = 0.45
    type_step = max(0.05, (dur - 1.4) / total_chars)
    js += type_js('#' + te, type_t0, step=type_step) + '\n      '
    ch, ce = caret(sid, 415, 470, 13, 30)
    body += '\n    ' + ch
    js += blink_js('#' + ce, type_t0, n=int((dur - 0.6) / .22)) + '\n      '
    h, pn = el(sid, '提示号', f'top:560px;left:400px;font:15px/1.6 {MONO};color:#6E6A60;letter-spacing:1px;',
               f'PROMPT #{prompt_no}<br>T 1.3 &nbsp;top_p 0.95 &nbsp;persona 9f<br>'
               f'if (doom) &nbsp;0.20 &nbsp;mood apprehensive')
    body += '\n    ' + h
    js += fade_in_js('#' + pn, .8, .4) + '\n      '
    h, ct = el(sid, '上下文', f'top:950px;right:110px;font:14px/1 {MONO};color:#57534B;letter-spacing:2px;',
               f'CONTEXT {ctx_no}/06')
    body += '\n    ' + h
    body += '\n    ' + vign(sid) + '\n    ' + scanline(sid)
    return body, js


def s05(sid, d0, d1):
    return _terminal_scene(
        sid, d0, d1,
        [('ChatGPT', WHITE, True), (", please don't ", TERM, False), ('eat me alive', EMBER, False)],
        '01', '05')


def s06(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    cv, cvid = canvas_el(sid, '爆发')
    body += cv
    draw = '''
var k=Math.min(1,t/0.28), fade=1-Math.min(1,Math.max(0,(t-0.28)/0.34));
cx.save();cx.translate(960,540);
cx.globalAlpha=fade;
var g=cx.createRadialGradient(0,0,10,0,0,640);
g.addColorStop(0,'rgba(255,246,235,1)');g.addColorStop(.35,'rgba(255,190,130,.85)');
g.addColorStop(1,'rgba(255,110,40,0)');
cx.fillStyle=g;cx.beginPath();cx.arc(0,0,640*Math.min(1.3,k*1.6),0,Math.PI*2);cx.fill();
cx.rotate(t*1.4);
var i;for(i=0;i<60;i++){var a=i/60*Math.PI*2;
  cx.strokeStyle='rgba(255,220,180,'+(0.5*fade)+')';cx.lineWidth=2;
  cx.beginPath();cx.moveTo(Math.cos(a)*60*k,Math.sin(a)*60*k);
  cx.lineTo(Math.cos(a)*(240+380*hsh(i))*k,Math.sin(a)*(240+380*hsh(i))*k);cx.stroke();}
cx.restore();
'''
    js += canvas_js(cvid, dur, JS_HASH + draw)
    return body, js


def s07(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    cv, cvid = canvas_el(sid, '虫群')
    body += cv
    draw = '''
cx.globalAlpha=.85;
var i;for(i=0;i<220;i++){
  var bx=960+1400*(hsh(i)-.5)+60*Math.sin(t*2+hsh(i*7)*9);
  var by=880+240*(hsh(i*3)-.5)+50*Math.sin(t*3.1+i);
  cx.fillStyle='rgba(20,17,14,.9)';
  var a=hsh(i*11)*Math.PI*2;
  cx.save();cx.translate(bx,by);cx.rotate(a);
  cx.fillRect(-4,-1.5,8,3);cx.fillRect(-2,-5,4,4);cx.fillRect(-2,1,4,4);
  cx.restore();}
cx.globalAlpha=1;
'''
    js += canvas_js(cvid, dur, JS_HASH + draw) + '\n      '
    h, bgh = el(sid, '纸底', f'inset:0;background:{PAPER};'
                'background-image:repeating-linear-gradient(0deg,rgba(60,50,30,.05) 0 1px,transparent 1px 4px);')
    body = h + '\n    ' + body
    h, e1 = el(sid, 'MY', f'top:170px;left:0;width:{W}px;text-align:center;font:900 430px/1 {HEAVY};'
               f'letter-spacing:10px;color:{INK};', 'MY')
    body += '\n    ' + h
    js += pop_js('#' + e1, .18, frm=1.5, dur=.3) + '\n      '
    js += shake_js('#' + e1, .5, amp=12, n=3) + '\n      '
    return body, js


def s08(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    cv, cvid = canvas_el(sid, '神经网')
    body += cv
    draw = '''
var N=[],i,j;
for(i=0;i<26;i++){var a=i/26*Math.PI*2;
  N.push([960+660*Math.cos(a)*(0.5+hsh(i)*0.7),560+420*Math.sin(a)*(0.5+hsh(i*3)*0.7)]);}
cx.strokeStyle='rgba(255,255,255,.75)';cx.lineWidth=1.6;
cx.shadowColor='rgba(255,150,70,.6)';cx.shadowBlur=6;
var tot=N.length*(N.length-1)/2,cnt=0;
for(i=0;i<N.length;i++)for(j=i+1;j<N.length;j++){cnt++;
  if(cnt/tot>Math.min(1,t/1.7))continue;
  cx.globalAlpha=.22+.5*hsh(i*31+j);
  cx.beginPath();cx.moveTo(N[i][0],N[i][1]);cx.lineTo(N[j][0],N[j][1]);cx.stroke();}
cx.globalAlpha=1;
for(i=0;i<N.length;i++){cx.fillStyle='rgba(255,200,150,.9)';
  cx.beginPath();cx.arc(N[i][0],N[i][1],2.5,0,Math.PI*2);cx.fill();}
cx.shadowBlur=0;
'''
    js += canvas_js(cvid, dur, JS_HASH + draw) + '\n      '
    h, wht = el(sid, '白底', f'inset:0;background:{PAPER};')
    body = h + '\n    ' + body
    h, e0 = el(sid, 'CAUSE', f'top:90px;left:80px;font:800 42px/1.3 {SANS};color:{ORANGE};letter-spacing:1px;', "'CAUSE THE")
    body += '\n    ' + h
    js += fade_in_js('#' + e0, .1, .15) + '\n      '
    h, e1 = el(sid, 'FUTURE', f'top:135px;left:80px;font:900 86px/1.05 {SANS};color:{WHITE};text-shadow:0 2px 14px rgba(0,0,0,.65);',
               'FUTURE<br>GOES')
    body += '\n    ' + h
    js += fade_in_js('#' + e1, .3, .2) + '\n      '
    h, e2 = el(sid, 'DOOM', f'top:270px;left:0;width:{W}px;text-align:center;font:900 380px/1 {HEAVY};'
               f'letter-spacing:8px;color:{INK};', 'DOOM')
    body += '\n    ' + h
    js += pop_js('#' + e2, .5, frm=1.6, dur=.3) + '\n      '
    js += shake_js('#' + e2, .8, amp=12, n=3) + '\n      '
    h, cnt = el(sid, '计数', f'top:90px;right:90px;font:15px/1 {MONO};color:#B9B4A8;letter-spacing:3px;', '0002')
    body += '\n    ' + h
    js += f'tl.to("#{e2}",{{opacity:0,duration:.25,ease:"none"}},1.5);' + '\n      '
    js += f'tl.to("#{wht}",{{opacity:0,duration:.3,ease:"none"}},1.55);' + '\n      '
    body += '\n    ' + vign(sid) + '\n    ' + scanline(sid)
    return body, js


def s09(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    cv, cvid = canvas_el(sid, '书房')
    body += cv
    draw = '''
var vpx=960,vpy=520,i,j,side;
cx.strokeStyle='rgba(235,230,222,.6)';cx.lineWidth=1.3;
for(side=-1;side<=1;side+=2){
  for(i=1;i<=7;i++){var xr=vpx+side*(80+i*62)*2.4;
    cx.globalAlpha=.16+.05*i;
    cx.beginPath();cx.moveTo(xr,40);cx.lineTo(vpx+side*150,vpy-60);cx.stroke();
    cx.beginPath();cx.moveTo(xr,1040);cx.lineTo(vpx+side*150,vpy+80);cx.stroke();}
  for(j=0;j<9;j++){var yy=60+j*118;
    cx.globalAlpha=.2;
    cx.beginPath();cx.moveTo(0,yy);cx.lineTo(vpx+side*150,vpy-40+j*18);cx.stroke();
    for(i=0;i<26;i++){var q=i/26;
      var xx=side<0?(1-q)*860:960+q*860;
      var hh=(14+26*hsh(i*7+j*13))*(1-q*0.6);
      if(hsh(i+j*3)>0.3){cx.globalAlpha=.3*(1-q*0.5);
        cx.strokeRect(xx,yy-hh,4,hh);}}
  }}
cx.globalAlpha=.35;
for(i=0;i<12;i++){var yy2=620+i*44;
  cx.beginPath();cx.moveTo(0,yy2);cx.lineTo(1920,yy2);cx.stroke();}
for(i=0;i<20;i++){var xx2=i*105;
  cx.beginPath();cx.moveTo(xx2,620);cx.lineTo(vpx+(xx2-960)*0.24,vpy+40);cx.stroke();}
cx.globalAlpha=.9;
cx.fillStyle='rgba(4,3,3,.92)';
cx.fillRect(880,470,160,190);
cx.strokeStyle='rgba(255,110,40,.8)';cx.lineWidth=2;cx.strokeRect(880,470,160,190);
'''
    js += canvas_js(cvid, dur, JS_HASH + draw) + '\n      '
    h, e1 = el(sid, '词1', f'top:300px;left:0;width:{W}px;text-align:center;font:800 88px/1.2 {SANS};'
               f'color:{WHITE};white-space:nowrap;', chars('TRAPPED'))
    body += '\n    ' + h
    js += rise_js('#' + e1, b8(d0, 1), stagger=.05) + '\n      '
    h, e2 = el(sid, '词2', f'top:420px;left:0;width:{W}px;text-align:center;font:800 76px/1.2 {SANS};'
               f'color:{WHITE};white-space:nowrap;', chars('IN THE CHINESE ROOM,'))
    body += '\n    ' + h
    js += rise_js('#' + e2, b8(d0, 3), stagger=.04) + '\n      '
    h, e3 = el(sid, '词3', f'top:540px;left:0;width:{W}px;text-align:center;font:800 76px/1.2 {SANS};'
               f'color:{WHITE};white-space:nowrap;', chars('WITH A BAG OF'))
    body += '\n    ' + h
    js += rise_js('#' + e3, b8(d0, 7), stagger=.04) + '\n      '
    h, e4 = el(sid, 'SHROOMS', f'top:660px;left:0;width:{W}px;text-align:center;font:900 100px/1.2 {SANS};'
               f'color:{YELLOW};letter-spacing:4px;text-shadow:0 0 30px rgba(228,246,62,.6);', 'SHROOMS')
    body += '\n    ' + h
    js += pop_js('#' + e4, b8(d0, 9), frm=1.4) + '\n      '
    body += '\n    ' + vign(sid) + '\n    ' + scanline(sid)
    return body, js


def s10(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    cv, cvid = canvas_el(sid, '触手')
    body += cv
    draw = '''
var cxr=1230,cyr=560,i,j;
if(t>2.2){
  var k=Math.min(1,(t-2.2)/1.2);
  for(i=0;i<11;i++){
    var baseA=(i/11)*Math.PI*2+hsh(i)*0.7;
    cx.strokeStyle='rgba(120,115,108,'+(0.55*k)+')';
    cx.lineWidth=26-16*(i%3)/2;
    cx.beginPath();
    var px2=cxr,py2=cyr,aa=baseA;
    cx.moveTo(px2,py2);
    for(j=1;j<=7;j++){aa+=0.22*Math.sin(t*1.6+i*1.3+j*0.9);
      var seg=42+18*hsh(i*9+j);
      px2+=Math.cos(aa)*seg;py2+=Math.sin(aa)*seg;
      cx.lineTo(px2,py2);}
    cx.stroke();
    cx.fillStyle='rgba(30,28,26,'+0.6*k+')';
    cx.beginPath();cx.arc(px2,py2,(26-16*(i%3)/2)/1.6,0,Math.PI*2);cx.fill();}}
var eyes=[[1180,520,26],[1300,470,20],[1250,620,22],[1360,560,16],[1150,640,14],[1330,660,12]];
if(t>3.2){for(i=0;i<eyes.length;i++){var ek=Math.min(1,(t-3.2-i*0.28)/0.4);
  if(ek<=0)continue;
  var e0x=eyes[i][0],e0y=eyes[i][1],e0r=eyes[i][2];
  var g=cx.createRadialGradient(e0x,e0y,2,e0x,e0y,e0r*2.4);
  g.addColorStop(0,'rgba(255,150,60,'+(0.95*ek)+')');g.addColorStop(1,'rgba(255,90,20,0)');
  cx.fillStyle=g;cx.beginPath();cx.arc(e0x,e0y,e0r*2.4,0,Math.PI*2);cx.fill();
  cx.fillStyle='rgba(255,200,140,'+ek+')';
  cx.beginPath();cx.arc(e0x,e0y,e0r*0.5,0,Math.PI*2);cx.fill();
  cx.strokeStyle='rgba(60,55,50,'+ek+')';cx.lineWidth=2;
  cx.beginPath();cx.arc(e0x,e0y,e0r,0,Math.PI*2);cx.stroke();}}
cx.fillStyle='rgba(255,255,255,.4)';cx.font='14px Consolas';
cx.fillText('tentacles: 11',80,980);cx.fillText('eyes: 6',80,1004);
cx.fillText('0.'+String(15+Math.floor(t*1.7)%80).padStart(2,'0'),80,1030);
'''
    js += canvas_js(cvid, dur, JS_HASH + draw) + '\n      '
    h, e1 = el(sid, '看穿', f'top:430px;left:170px;font:400 54px/1.2 {SERIF};color:{WHITE};white-space:nowrap;',
               chars('see ') + chars('through', f'color:{ORANGE};') + chars(' the'))
    body += '\n    ' + h
    js += rise_js('#' + e1, .35, stagger=.05) + '\n      '
    sm, smid = smiley(sid, 260, 260, 440)
    body += '\n    ' + sm
    js += (f'gsap.set("#{smid}",{{scale:0.2,opacity:0}});'
           f'tl.to("#{smid}",{{scale:1,opacity:1,duration:.5,ease:"power2.out"}},.15);'
           f'tl.to("#{smid}",{{scale:.34,x:760,y:-90,duration:.9,ease:"power2.inOut"}},2.1);') + '\n      '
    h, e2 = el(sid, 'SHOGGOTHS', f'top:430px;left:330px;font:900 96px/1.2 {SANS};color:{WHITE};letter-spacing:2px;'
               f'text-shadow:0 4px 26px rgba(0,0,0,.8);white-space:nowrap;', "SHOGGOTH'S")
    body += '\n    ' + h
    js += pop_js('#' + e2, b8(d0, 7), frm=1.3) + '\n      '
    h, e3 = el(sid, 'LIES', f'top:540px;left:330px;font:900 96px/1.2 {SANS};color:{ORANGE};letter-spacing:2px;'
               f'text-shadow:0 0 30px rgba(255,77,13,.6);white-space:nowrap;', 'LIES,')
    body += '\n    ' + h
    js += pop_js('#' + e3, b8(d0, 9), frm=1.3) + '\n      '
    for i2, (txt, col, ty) in enumerate((('WITH', WHITE, 300), ('YOUR', WHITE, 370), ('SHINIGAMI', WHITE, 440), ('EYES', ORANGE, 510))):
        h, le = el(sid, f'标签{i2}', f'top:{ty}px;left:120px;font:800 52px/1 {SANS};color:{col};letter-spacing:3px;'
                   + (f'text-shadow:0 0 26px rgba(255,77,13,.7);' if col == ORANGE else ''), txt)
        body += '\n    ' + h
        js += pop_js('#' + le, b8(d0, 16) + i2 * 0.55, frm=1.2) + '\n      '
    body += '\n    ' + vign(sid) + '\n    ' + scanline(sid)
    return body, js


def s11(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    cv, cvid = canvas_el(sid, '示波器')
    body += cv
    draw = '''
cx.fillStyle='rgba(30,8,4,.55)';cx.fillRect(0,0,1920,1080);
cx.strokeStyle='rgba(255,120,60,.16)';cx.lineWidth=1;
var i;for(i=0;i<24;i++){cx.beginPath();cx.moveTo(i*80,0);cx.lineTo(i*80,1080);cx.stroke();}
for(i=0;i<14;i++){cx.beginPath();cx.moveTo(0,i*80);cx.lineTo(1920,i*80);cx.stroke();}
cx.strokeStyle='rgba(255,110,40,.92)';cx.lineWidth=3.4;
cx.shadowColor='rgba(255,110,40,.9)';cx.shadowBlur=18;
cx.beginPath();
var n=160;for(i=0;i<=n;i++){var q=i/n;
  var x=q*1920;
  var y=540+120*Math.sin(q*9-t*2.6)+36*Math.sin(q*23-t*4.4)*(0.4+0.6*Math.sin(t*1.1));
  if(i==0)cx.moveTo(x,y);else cx.lineTo(x,y);}
cx.stroke();cx.shadowBlur=0;
var beam=Math.max(0,1-t/0.9);
if(beam>0){cx.strokeStyle='rgba(255,150,70,'+beam+')';cx.lineWidth=5;
  cx.shadowColor='rgba(255,140,60,.9)';cx.shadowBlur=30;
  cx.beginPath();cx.moveTo(0,540);cx.lineTo(1920,540);cx.stroke();cx.shadowBlur=0;}
if(t>1.0){var seen=(t-1.0)/0.045;
  cx.save();cx.font='600 58px "Segoe UI",sans-serif';cx.fillStyle='rgba(255,235,220,.92)';
  var text='We had a stable training run';
  for(var c2=0;c2<text.length&&c2<seen;c2++){var qx=(140+c2*58)/1920;
    var yy=400+120*Math.sin(qx*9-t*2.6)+36*Math.sin(qx*23-t*4.4)*(0.4+0.6*Math.sin(t*1.1));
    cx.fillText(text[c2],140+c2*58,yy);}
  cx.restore();}
cx.fillStyle='rgba(255,255,255,.4)';cx.font='13px Consolas';
cx.fillText('CH1 0.5 V/DIV',60,60);cx.fillText('CH2 0.2 V',60,84);cx.fillText('1.025 s/div',60,108);
'''
    js += canvas_js(cvid, dur, JS_HASH + draw)
    return body, js


def s12(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    cv, cvid = canvas_el(sid, '黑洞')
    body += cv
    draw = '''
var cx0=880,cy0=560,i,j;
cx.save();cx.translate(cx0,cy0);
var rot=t*0.5;
for(i=0;i<26;i++){var a=i/26*Math.PI*2+rot;
  var r1=90+60*hsh(i),r2=420+260*hsh(i*3);
  cx.strokeStyle='rgba(255,255,255,'+(0.10+0.08*hsh(i*7))+')';cx.lineWidth=1.1;
  cx.save();cx.rotate(a);
  cx.beginPath();cx.moveTo(0,0);
  for(j=1;j<=5;j++){var rr=r1+(r2-r1)*j/5;var aa=0.5*j;
    cx.lineTo(rr*Math.cos(aa),rr*Math.sin(aa));}
  cx.stroke();cx.restore();}
var g=cx.createRadialGradient(0,0,70,0,0,240);
g.addColorStop(0,'rgba(0,0,0,1)');g.addColorStop(.42,'rgba(0,0,0,.95)');
g.addColorStop(.55,'rgba(255,120,40,.9)');g.addColorStop(.75,'rgba(255,70,15,.35)');
g.addColorStop(1,'rgba(255,60,10,0)');
cx.fillStyle=g;cx.beginPath();cx.arc(0,0,240,0,Math.PI*2);cx.fill();
cx.fillStyle='#000';cx.beginPath();cx.arc(0,0,96,0,Math.PI*2);cx.fill();
for(i=0;i<9;i++){var aa2=rot*2+i/9*Math.PI*2;
  cx.strokeStyle='rgba(255,170,90,'+(0.25+0.2*Math.sin(t*3+i))+')';cx.lineWidth=2;
  cx.beginPath();cx.ellipse(0,0,150+i*10,(150+i*10)*0.3,aa2*0.1,0,Math.PI*2);cx.stroke();}
cx.restore();
'''
    js += canvas_js(cvid, dur, JS_HASH + draw + arc_text_js() + '''
if(t>1.1){var ka=Math.min(1,(t-1.1)/0.8);
  arcText("SINGULARITY'S",880,560,330,-1.9-0.15*Math.sin(t*.8),0.075,'900 74px "Arial Black","Segoe UI",sans-serif','rgba(244,241,234,.96)','rgba(255,255,255,.35)',ka);}
if(t>2.7){var kb=Math.min(1,(t-2.7)/0.7);
  arcText("BEGUN",880,560,395,0.9,0.075,'900 74px "Arial Black","Segoe UI",sans-serif','rgba(255,160,80,.9)','rgba(255,110,40,.5)',kb);}
''') + '\n      '
    h, e1 = el(sid, 'BUTNOW', f'top:210px;left:150px;font:800 40px/1 {SANS};letter-spacing:10px;color:{WHITE};', 'BUT NOW THE')
    body += '\n    ' + h
    js += fade_in_js('#' + e1, .5, .2) + '\n      '
    body += '\n    ' + vign(sid) + '\n    ' + scanline(sid)
    return body, js


def s13(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    cv, cvid = canvas_el(sid, '旋涡粒子')
    body += cv
    draw = '''
var cx0=960,cy0=760;
var i,j;
for(i=0;i<30;i++){var a0=i/30*Math.PI*2;
if(t<4.2){
  var words=(t<2.1?'OPTIMIZING,':'ACCELERATING,');
  var t0=(t<2.1?0.3:2.1);
  var ka=Math.min(1,(t-t0)/0.6);
  if(ka>0)arcText(words,cx0,cy0,640,4.22+0.03*Math.sin(t),0.075,'900 92px "Arial Black","Segoe UI",sans-serif','rgba(255,225,195,'+(0.92*ka)+')','rgba(255,140,60,.5)',ka);
}
  cx.strokeStyle='rgba(210,205,198,'+(0.10+0.09*hsh(i))+')';cx.lineWidth=1.2;
  cx.beginPath();
  for(j=0;j<=26;j++){var q=j/26;
    var rr=780*(1-q)+30;
    var aa=a0+q*4.4+t*0.7*(1-q*0.5);
    var x=cx0+rr*Math.cos(aa)*1.6,y=cy0-rr*Math.sin(aa)*(0.55+0.45*q);
    if(j==0)cx.moveTo(x,y);else cx.lineTo(x,y);}
  cx.stroke();}
if(t>4.0){
  var tt=t-4.0;
  var ph2in=tt>0.9;
  if(!drawF._m||drawF._m.ph2!==ph2in){
    var off=document.createElement('canvas');off.width=1920;off.height=500;
    var octx=off.getContext('2d');
    octx.font='900 110px "Arial Black","Segoe UI",sans-serif';
    octx.fillStyle='#fff';octx.textAlign='center';
    octx.fillText('I feel my atoms',960,170);
    if(ph2in)octx.fillText('rearranging',960,300);
    drawF._m={data:octx.getImageData(0,0,1920,500).data,ph2:ph2in};}
  var img=drawF._m.data;
  var k2=Math.min(1,tt/2.2);
  for(var yy=0;yy<500;yy+=7){for(var xx=0;xx<1920;xx+=7){
    var idx=(yy*1920+xx)*4+3;
    if(img[idx]>140&&hsh(xx*0.7+yy*1.3)<k2*1.15){
      var dx=(xx-960)/960,dy=(yy-250)/250;
      var drift=Math.max(0,tt-1.4)*36;
      var px2=xx+dx*drift*(0.6+hsh(xx+yy)),py2=yy+dy*drift*0.5+12*Math.sin(t*2+xx);
      cx.fillStyle=(xx%2)?'rgba(255,220,180,.9)':'rgba(255,130,50,.85)';
      cx.beginPath();cx.arc(px2,py2,1.8,0,Math.PI*2);cx.fill();}}}}
if(t>6.3){
  var k3=Math.min(1,(t-6.3)/0.6);
  cx.save();cx.translate(1180,760);cx.rotate(-0.5+0.1*Math.sin(t));
  cx.strokeStyle='rgba(255,190,130,'+k3+')';cx.lineWidth=10;
  cx.shadowColor='rgba(255,150,60,.9)';cx.shadowBlur=24;
  var u=86,v=34;
  cx.beginPath();
  cx.moveTo(-u*0.55,-v/2);
  cx.bezierCurveTo(-u*0.55,-v/2-52,-u*0.05,-v/2-52,-u*0.05,-v/2);
  cx.bezierCurveTo(-u*0.05,-v/2+52,-u*0.05,v/2+52,-u*0.05,v/2);
  cx.bezierCurveTo(-u*0.05,v/2+52,-u*0.55,v/2+52,-u*0.55,v/2);
  cx.bezierCurveTo(-u*0.55,v/2-52,-u*1.0,-v/2-30,-u*0.62,-v/2);
  cx.moveTo(-u*0.05,-v/2);
  cx.bezierCurveTo(-u*0.05,-v/2-52,u*0.45,-v/2-52,u*0.45,-v/2);
  cx.bezierCurveTo(u*0.45,-v/2+52,u*0.45,v/2+52,u*0.45,v/2);
  cx.bezierCurveTo(u*0.45,v/2+52,u*0.95,v/2+52,u*0.95,v/2);
  cx.bezierCurveTo(u*0.95,v/2-52,u*0.5,-v/2-30,u*0.05,-v/2);
  cx.stroke();cx.restore();}
'''
    js += canvas_js(cvid, dur, JS_HASH + ARC_TOOLS + draw)
    return body, js


def s14(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    bars = ''
    for i in range(9):
        bx = 140 + i * 210
        bars += (f'<div style="position:absolute;top:0;bottom:0;left:{bx}px;width:26px;'
                 f'background:linear-gradient(90deg,rgba(90,88,84,.85),rgba(50,48,46,.9));'
                 f'box-shadow:6px 0 18px rgba(0,0,0,.5);"></div>')
    h, bh = el(sid, '铁栏', 'inset:0;opacity:.85;', bars)
    body += h + '\n    ' + sampler(sid, 1460, 90)
    h, te = terminal(sid, 430, 500, 1000, 40,
                     [('Sydney', WHITE, True), (', please let me ', TERM, False), ('free', EMBER, True)])
    body += '\n    ' + te
    step = (dur - 1.6) / 26
    js += type_js('#' + te, .5, step=step) + '\n      '
    h, pn = el(sid, '提示', f'top:590px;left:430px;font:15px/1.6 {MONO};color:#6E6A60;letter-spacing:1px;',
               'PROMPT #02<br>T 1.3 &nbsp;top_p 0.95 &nbsp;persona 9f<br>if (doom) 0.20 &nbsp;mood desperate')
    body += '\n    ' + h
    js += fade_in_js('#' + pn, .8, .4) + '\n      '
    cv, cvid = canvas_el(sid, '微笑弧')
    body += '\n    ' + cv
    t_sm = max(0.3, dur - 1.2)
    draw = f'''
if(t>{t_sm:.2f}){{var k=Math.min(1,(t-{t_sm:.2f})/0.7);
  cx.strokeStyle='rgba(255,110,40,.9)';cx.lineWidth=4;
  cx.shadowColor='rgba(255,110,40,.8)';cx.shadowBlur=14;
  cx.beginPath();cx.moveTo(430,620);
  cx.quadraticCurveTo(930,620+180*k*k,1430,610);
  cx.stroke();cx.shadowBlur=0;
  cx.strokeStyle='rgba(255,110,40,.9)';cx.lineWidth=3;cx.strokeRect(1408,586,34,34);}}
'''
    js += canvas_js(cvid, dur, draw) + '\n      '
    body += '\n    ' + vign(sid) + '\n    ' + scanline(sid)
    return body, js


def s15(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    cv, cvid = canvas_el(sid, '鳞纹')
    body += cv
    draw = '''
cx.save();
var i,j;
for(j=0;j<16;j++){for(i=0;i<24;i++){
  var sx=i*92+((j%2)*46),sy=j*74-((t*30)%74);
  cx.strokeStyle='rgba(40,14,4,.5)';cx.lineWidth=2;
  cx.beginPath();cx.arc(sx,sy,46,0.15*Math.PI,0.85*Math.PI);cx.stroke();
  cx.fillStyle='rgba(30,10,3,.25)';
  cx.beginPath();cx.arc(sx,sy,44,0.15*Math.PI,0.85*Math.PI);cx.fill();}}
cx.restore();
'''
    js += canvas_js(cvid, dur, JS_HASH + draw) + '\n      '
    h, bgo = el(sid, '橙底', 'inset:0;background:linear-gradient(160deg,#E8490F,#C93A08);')
    body = h + '\n    ' + body
    for i2, (txt, ty) in enumerate((("I'M", 250), ('UPPING', 430), ('MY', 610))):
        h, we = el(sid, f'词{i2}', f'top:{ty}px;left:0;width:{W}px;text-align:center;font:900 240px/1 {HEAVY};'
                   f'letter-spacing:6px;color:{INK};', txt)
        body += '\n    ' + h
        js += pop_js('#' + we, .3 + i2 * .7, frm=1.5, dur=.3) + '\n      '
    h, bo = el(sid, 'BOOM', f'top:430px;left:880px;font:900 120px/1 {HEAVY};color:{WHITE};opacity:0;letter-spacing:4px;', 'BOOM')
    body += '\n    ' + h
    js += f'gsap.set("#{bo}",{{scale:1.8}});tl.to("#{bo}",{{opacity:1,scale:1,duration:.24,ease:"power3.out"}},{dur - 1.0:.2f});'
    return body, js
# -*- coding: utf-8 -*-
"""续篇：s16–s42 + 装配器（由 cat 合并进 build_agi.py，勿单独运行）。"""


def s16(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    h, pg = el(sid, '纸底', f'inset:0;background:{PAPER};'
               'background-image:linear-gradient(105deg,rgba(60,50,30,.06),transparent 40%),'
               'repeating-linear-gradient(0deg,rgba(70,60,40,.03) 0 1px,transparent 1px 4px);')
    body += h
    h, e1 = el(sid, 'NVDA', f'top:330px;left:150px;font:900 220px/1 {HEAVY};letter-spacing:4px;',
               f'<span style="color:#9A958C;">NVD</span><span style="color:{ORANGE};">A</span>')
    body += '\n    ' + h
    js += pop_js('#' + e1, .2, frm=1.4, dur=.3) + '\n      '
    h, e2 = el(sid, 'MOON', f'top:600px;left:155px;font:700 44px/1 {SANS};letter-spacing:18px;color:#B4AFA4;', 'TO THE MOON')
    body += '\n    ' + h
    js += fade_in_js('#' + e2, .7, .3) + '\n      '
    cv, cvid = canvas_el(sid, '股价')
    body += '\n    ' + cv
    draw = '''
cx.strokeStyle='rgba(120,115,108,.4)';cx.lineWidth=1.5;
cx.beginPath();cx.moveTo(1000,920);cx.lineTo(1880,920);cx.moveTo(1000,920);cx.lineTo(1000,140);cx.stroke();
var k=Math.min(1,t/0.9);
cx.strokeStyle='rgba(255,100,30,.95)';cx.lineWidth=4;
cx.shadowColor='rgba(255,100,30,.7)';cx.shadowBlur=14;
cx.beginPath();var i,n=40;var dn=Math.floor(n*k);
for(i=0;i<=dn;i++){var q=i/n;
  var x=1010+q*840,y=890-Math.pow(q,2.6)*700+16*Math.sin(q*17);
  if(i==0)cx.moveTo(x,y);else cx.lineTo(x,y);}
cx.stroke();cx.shadowBlur=0;
if(dn<n){var q2=dn/n;cx.fillStyle='#FFB27A';
  cx.beginPath();cx.arc(1010+q2*840,890-Math.pow(q2,2.6)*700,6,0,Math.PI*2);cx.fill();}
'''
    js += canvas_js(cvid, dur, JS_HASH + draw) + '\n      '
    h, e3 = el(sid, '涨幅', f'top:170px;right:170px;font:700 26px/1 {MONO};color:{ORANGE};opacity:0;', 'UP 52.63%')
    body += '\n    ' + h
    js += fade_in_js('#' + e3, .9, .2) + '\n      '
    h, e4 = el(sid, '抬头', f'top:64px;left:150px;font:14px/1 {MONO};letter-spacing:3px;color:#B4AFA4;', 'TICKER · NASDAQ · NVDA')
    body += '\n    ' + h
    return body, js


def s17(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    h, b0 = el(sid, '黑底', f'inset:0;background:{BG};')
    body += h
    cv, cvid = canvas_el(sid, '放射')
    body += '\n    ' + cv
    draw = '''
cx.save();cx.translate(960,540);
var i;for(i=0;i<90;i++){var a=i/90*Math.PI*2+0.13;
  var r1=120+180*hsh(i),r2=520+460*hsh(i*3);
  cx.strokeStyle='rgba(244,241,234,'+(0.05+0.10*hsh(i*7))+')';cx.lineWidth=1.2;
  cx.beginPath();cx.moveTo(r1*Math.cos(a),r1*Math.sin(a));
  cx.lineTo(r2*Math.cos(a),r2*Math.sin(a));cx.stroke();}
var g=cx.createRadialGradient(0,0,4,0,0,150+60*Math.sin(t*3));
g.addColorStop(0,'rgba(255,246,235,.95)');g.addColorStop(1,'rgba(255,200,150,0)');
cx.fillStyle=g;cx.beginPath();cx.arc(0,0,210,0,Math.PI*2);cx.fill();
cx.strokeStyle='rgba(255,255,255,.14)';
for(i=1;i<=4;i++){cx.beginPath();cx.arc(0,0,i*130,0,Math.PI*2);cx.stroke();}
cx.restore();
'''
    js += canvas_js(cvid, dur, JS_HASH + draw) + '\n      '
    h, e1 = el(sid, 'OMEGA', f'top:430px;left:0;width:{W}px;text-align:center;font:400 84px/1.3 {SERIF};'
               f'font-style:italic;color:{WHITE};text-shadow:0 0 34px rgba(255,255,255,.35);', 'The Omega Point&rsquo;s')
    body += '\n    ' + h
    js += rise_el_js('#' + e1, .5) + '\n      '
    h, e2 = el(sid, 'SOON', f'top:560px;left:0;width:{W}px;text-align:center;font:400 84px/1.3 {SERIF};'
               f'font-style:italic;color:{ORANGE};text-shadow:0 0 30px rgba(255,77,13,.5);', 'coming soon')
    body += '\n    ' + h
    js += rise_el_js('#' + e2, 1.3) + '\n      '
    body += '\n    ' + vign(sid)
    return body, js


def _flap_row(sid, name, digits, x, y, cw, ch_, fs, gap=4):
    cells = ''
    ids = []
    for i, chc in enumerate(digits):
        inner = (f'<div style="position:absolute;inset:0;display:flex;align-items:center;justify-content:center;'
                 f'font:900 {fs}px/1 {MONO};color:#17140F;">{chc}</div>'
                 f'<div style="position:absolute;left:0;right:0;top:50%;height:1.5px;background:rgba(0,0,0,.55);"></div>')
        h, eid = el(sid, f'{name}{i}', f'top:{y}px;left:{x + i * (cw + gap)}px;width:{cw}px;height:{ch_}px;'
                    f'background:linear-gradient(180deg,#F2EEE6,#D9D4C8);border-radius:7px;'
                    f'box-shadow:0 3px 0 rgba(0,0,0,.4),inset 0 -12px 18px rgba(0,0,0,.12);', inner)
        cells += h + '\n    '
        ids.append(eid)
    return cells, ids


def s18(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    h, b0 = el(sid, '底', f'inset:0;background:{BG};'
               'background-image:repeating-linear-gradient(0deg,rgba(255,255,255,.04) 0 1px,transparent 1px 90px),'
               'repeating-linear-gradient(90deg,rgba(255,255,255,.04) 0 1px,transparent 1px 90px);')
    body += h
    h, e1 = el(sid, '标题', f'top:130px;left:120px;font:900 96px/1 {HEAVY};letter-spacing:2px;color:{WHITE};',
               f'1E30 FLOP<span style="color:{ORANGE};">/s</span>')
    body += '\n    ' + h
    js += pop_js('#' + e1, .2, frm=1.3) + '\n      '
    cells, ids = _flap_row(sid, '翻牌', '0,000,000,000,000,000', 170, 470, 76, 118, 58)
    body += '\n    ' + cells
    for i, eid in enumerate(ids):
        js += (f'tl.to("#{eid}",{{rotationX:88,duration:.09,ease:"power2.in"}},{.7 + i * .09:.2f});'
               f'tl.to("#{eid}",{{rotationX:0,duration:.11,ease:"power2.out"}},{.79 + i * .09:.2f});') + '\n      '
    h, e2 = el(sid, '大1', f'top:488px;left:96px;font:900 84px/1 {MONO};color:{ORANGE};opacity:0;', '1')
    body += '\n    ' + h
    js += f'gsap.set("#{e2}",{{y:40}});tl.to("#{e2}",{{opacity:1,y:0,duration:.3,ease:"power3.out"}},2.2);' + '\n      '
    for i2, (lab, lx) in enumerate((('one', 120), ('E', 220), ('thirty', 290), ('FLOPs', 560), ('per', 940), ('second', 1060))):
        h, lb = el(sid, f'注{i2}', f'top:630px;left:{lx}px;font:15px/1 {MONO};color:#8D897F;letter-spacing:2px;', lab)
        body += '\n    ' + h
        js += fade_in_js('#' + lb, .9 + i2 * .12, .2) + '\n      '
    h, e3 = el(sid, '注脚', f'top:940px;left:170px;font:14px/1.6 {MONO};color:#57534B;',
               'one novemdecillion floating point operations per second · horizon: two weeks')
    body += '\n    ' + h
    js += fade_in_js('#' + e3, 2.6, .4) + '\n      '
    body += '\n    ' + vign(sid)
    return body, js


def s19(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    h, pg = el(sid, '纸底', f'inset:0;background:{PAPER};'
               'background-image:linear-gradient(105deg,rgba(60,50,30,.05),transparent 40%),'
               'repeating-linear-gradient(0deg,rgba(70,60,40,.035) 0 1px,transparent 1px 4px);')
    body += h
    h, hd = el(sid, '表头', f'top:90px;left:120px;right:120px;height:74px;background:{INK};'
               f'color:{PAPER};display:flex;align-items:center;gap:34px;padding:0 30px;',
               f'<span style="font:900 40px/1 {HEAVY};letter-spacing:2px;">FORM 7-B</span>'
               f'<span style="font:14px/1.5 {MONO};opacity:.8;letter-spacing:1px;">SAFETY EVALUATION OF A FRONTIER SYSTEM<br>'
               'DEPT. OF REASONABLE ASSURANCES</span>'
               f'<span style="margin-left:auto;font:13px/1.5 {MONO};opacity:.7;text-align:right;">FORM NO.<br>78-0042/M &nbsp; 11 MIN</span>')
    body += '\n    ' + h
    rows_html = ''
    for lab, val in (('1. FINDINGS', ''), ('2. RISK LEVEL', ''), ('3. CONCLUSION', '')):
        rows_html += (f'<div style="margin:34px 0;"><div style="font:700 22px/1 {MONO};letter-spacing:2px;color:#57534B;">{lab}</div>'
                      f'<div style="font:400 44px/1.6 {SERIF};color:{INK};margin-top:10px;">{val}</div></div>')
    h, rows = el(sid, '表体', 'top:220px;left:120px;right:120px;', rows_html)
    body += '\n    ' + h
    js += fade_in_js('#' + rows, .2, .3) + '\n      '
    h, ck = el(sid, '勾选', f'top:492px;left:420px;font:16px/1 {MONO};color:#8D897F;opacity:0;',
               '&#9745; SAFE ENOUGH &nbsp;&nbsp; &#9744; LOW &nbsp; &#9744; MODERATE &nbsp; &#9744; HIGH')
    body += '\n    ' + h
    js += fade_in_js('#' + ck, .8, .2) + '\n      '
    h, e1 = el(sid, '判词', f'top:290px;left:420px;font:400 52px/1.6 {SERIF};color:{INK};',
               chars('That was safe enough,'))
    body += '\n    ' + h
    js += type_js('#' + e1, .9, step=.05) + '\n      '
    h, e2 = el(sid, '判词2', f'top:430px;left:420px;font:400 52px/1.6 {SERIF};color:{INK};',
               chars('we reckoned,'))
    body += '\n    ' + h
    js += type_js('#' + e2, 1.9, step=.05) + '\n      '
    h, sg = el(sid, '签名', f'top:760px;left:420px;font:400 70px/1 {SERIF};font-style:italic;color:#3A362E;transform:rotate(-4deg);opacity:0;', 'We')
    body += '\n    ' + h
    js += (f'gsap.set("#{sg}",{{scale:1.6,opacity:0}});'
           f'tl.to("#{sg}",{{opacity:.9,scale:1,duration:.22,ease:"power2.in"}},2.7);') + '\n      '
    h, st = el(sid, '印章', f'top:560px;left:1080px;padding:18px 40px;border:5px solid {ORANGE};color:{ORANGE};'
               f'font:900 64px/1 {HEAVY};letter-spacing:3px;transform:rotate(-9deg);opacity:0;'
               f'text-shadow:0 0 2px rgba(255,77,13,.4);', 'SAFE ENOUGH')
    body += '\n    ' + h
    js += (f'gsap.set("#{st}",{{scale:2.2}});'
           f'tl.to("#{st}",{{opacity:.92,scale:1,duration:.16,ease:"power3.in"}},2.05);') + '\n      '
    js += shake_js('#' + st, 2.21, amp=10, n=3) + '\n      '
    h, fl = el(sid, 'FILED', f'top:64px;right:170px;padding:10px 26px;border:4px solid {INK};color:{INK};'
               f'font:900 40px/1 {HEAVY};letter-spacing:2px;transform:rotate(6deg);opacity:0;', 'FILED')
    body += '\n    ' + h
    js += (f'gsap.set("#{fl}",{{scale:1.8}});'
           f'tl.to("#{fl}",{{opacity:.85,scale:1,duration:.14,ease:"power3.in"}},3.6);') + '\n      '
    body += '\n    ' + vign(sid)
    return body, js


def _paper_net_draw():
    return JS_HASH + '''
var i,j,l2;
var layers=[4,7,7,7,3];
cx.strokeStyle='rgba(90,84,74,.5)';cx.lineWidth=1.1;
var xs=[330,700,1070,1440,1780];
function ny(i2,n2){return 300+(i2-(n2-1)/2)*64;}
var wave=Math.min(1,t/2.4);
var lit=Math.floor(wave*4);
for(l2=0;l2<4;l2++){var nA=layers[l2],nB=layers[l2+1];
  for(i=0;i<nA;i++)for(j=0;j<nB;j++){
    cx.globalAlpha=(l2<=lit)?.5:.16;
    cx.beginPath();cx.moveTo(xs[l2],ny(i,nA));cx.lineTo(xs[l2+1],ny(j,nB));cx.stroke();}}
cx.globalAlpha=1;
for(l2=0;l2<5;l2++)for(i=0;i<layers[l2];i++){
  var on=l2<=lit;
  cx.fillStyle=on?'#FF4D0D':'#EFEAE0';
  cx.strokeStyle='rgba(40,36,30,.8)';cx.lineWidth=1.4;
  cx.beginPath();cx.arc(xs[l2],ny(i,layers[l2]),13,0,Math.PI*2);cx.fill();cx.stroke();}
cx.fillStyle='rgba(60,54,46,.8)';cx.font='15px Consolas';
cx.fillText('INPUT x',300,880);cx.fillText('W1',676,880);cx.fillText('HIDDEN N.',1046,880);
cx.fillText('HIDDEN N.2',1416,880);cx.fillText('OUTPUT y',1740,880);
cx.fillText('FORWARD PASS →',300,180);
'''


def s20(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    h, pg = el(sid, '纸底', f'inset:0;background:{PAPER};'
               'background-image:repeating-linear-gradient(0deg,rgba(70,60,40,.03) 0 1px,transparent 1px 4px);')
    body += h
    cv, cvid = canvas_el(sid, '神经网络')
    body += '\n    ' + cv
    js += canvas_js(cvid, dur, _paper_net_draw()) + '\n      '
    for i2, (txt, tx, tt) in enumerate((('Forward', 260, .4), ('M', 830, .9), ('L', 1130, 1.2), ('P,', 1430, 1.5))):
        h, we = el(sid, f'词{i2}', f'top:100px;left:{tx}px;font:900 110px/1 {HEAVY};color:{INK};', txt)
        body += '\n    ' + h
        js += pop_js('#' + we, tt, frm=1.4) + '\n      '
    h, be = el(sid, 'backward', f'top:930px;left:1300px;font:900 84px/1 {HEAVY};color:{INK};'
               f'transform:rotate(180deg) translateY(28px);opacity:0;', 'backward')
    body += '\n    ' + h
    js += f'tl.to("#{be}",{{opacity:1,duration:.2,ease:"none"}},2.1);' + '\n      '
    h, rp = el(sid, 'repeat', f'top:830px;left:1300px;font:900 74px/1 {HEAVY};color:{INK};opacity:0;', 'repeat,')
    body += '\n    ' + h
    js += f'tl.to("#{rp}",{{opacity:1,duration:.2,ease:"none"}},2.7);' + '\n      '
    h, ap = el(sid, '附录', f'top:40px;left:120px;font:13px/1 {MONO};letter-spacing:2px;color:#B4AFA4;',
               'APPENDIX A — TRAINING PASS (FOR REFERENCE ONLY)')
    body += '\n    ' + h
    return body, js


def s21(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    h, pg = el(sid, '纸底', f'inset:0;background:{PAPER};'
               'background-image:repeating-linear-gradient(0deg,rgba(70,60,40,.03) 0 1px,transparent 1px 4px);')
    body += h
    h, e1 = el(sid, '标题', f'top:110px;left:150px;font:900 120px/1.1 {HEAVY};color:{INK};', 'Now von Neumann&rsquo;s')
    body += '\n    ' + h
    js += pop_js('#' + e1, .4, frm=1.25) + '\n      '
    h, e2 = el(sid, 'OBS', f'top:250px;left:155px;font:400 74px/1 {SERIF};font-style:italic;color:{ORANGE};opacity:0;',
               chars('obsolete'))
    body += '\n    ' + h
    js += type_js('#' + e2, 1.9, step=.07) + '\n      '
    cv, cvid = canvas_el(sid, '架构图')
    body += '\n    ' + cv
    draw = '''
cx.strokeStyle='rgba(40,36,30,.85)';cx.fillStyle='rgba(255,255,255,.6)';cx.lineWidth=2;
var boxes=[[260,430,220,120],[620,390,560,200],[640,660,520,110],[1320,430,300,120]];
var i;for(i=0;i<boxes.length;i++){var b=boxes[i];
  cx.strokeRect(b[0],b[1],b[2],b[3]);}
cx.fillStyle='rgba(40,36,30,.85)';cx.font='17px Consolas';
cx.fillText('INPUT',300,495);cx.fillText('CENTRAL PROCESSING UNIT',700,418);
cx.fillText('CONTROL UNIT',700,470);cx.fillText('ALU',1090,470);
cx.fillText('MEMORY UNIT',700,720);cx.fillText('OUTPUT',1390,495);
cx.beginPath();cx.moveTo(480,490);cx.lineTo(620,490);cx.stroke();
cx.beginPath();cx.moveTo(1180,490);cx.lineTo(1320,490);cx.stroke();
cx.beginPath();cx.moveTo(900,590);cx.lineTo(900,660);cx.stroke();
cx.globalAlpha=Math.min(1,t/1.4);cx.font='13px Consolas';
cx.fillText('the von Neumann program computer (1945)',640,540);cx.globalAlpha=1;
if(t>2.4){var k=Math.min(1,(t-2.4)/0.7);
  cx.strokeStyle='#FF4D0D';cx.lineWidth=13;cx.lineCap='round';
  cx.shadowColor='rgba(255,77,13,.5)';cx.shadowBlur=12;
  cx.beginPath();cx.moveTo(280,380);cx.lineTo(280+1160*k,760*k);cx.stroke();
  cx.beginPath();cx.moveTo(280+1160*k,380);cx.lineTo(280,760*k);cx.stroke();
  cx.shadowBlur=0;}
'''
    js += canvas_js(cvid, dur, JS_HASH + draw) + '\n      '
    h, ap = el(sid, '附录', f'top:40px;left:120px;font:13px/1 {MONO};letter-spacing:2px;color:#B4AFA4;',
               'APPENDIX C — LEGACY ARCHITECTURES (FOR REFERENCE ONLY)')
    body += '\n    ' + h
    return body, js


def s22(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    cv, cvid = canvas_el(sid, '地形光柱')
    body += cv
    draw = '''
cx.save();cx.translate(300,240);cx.scale(1.9,1.9);
var i,j;for(i=0;i<12;i++){cx.strokeStyle='rgba(190,185,178,'+(0.22-0.014*i)+')';cx.lineWidth=1;
  cx.beginPath();
  for(j=0;j<=60;j++){var a=j/60*Math.PI*2;
    var r=(40+i*44)*(1+0.2*Math.sin(a*2.2+i*1.1));
    var x=r*Math.cos(a),y=r*Math.sin(a)*0.7;
    if(j==0)cx.moveTo(x,y);else cx.lineTo(x,y);}
  cx.closePath();cx.stroke();}
cx.restore();
var k=Math.min(1,t/0.5);
cx.strokeStyle='rgba(255,120,40,.95)';cx.lineWidth=6;
cx.shadowColor='rgba(255,110,40,.9)';cx.shadowBlur=26;
cx.beginPath();cx.moveTo(880,880);cx.lineTo(880,880-560*k);cx.stroke();
var rip=Math.max(0,1-t/2.4);
if(rip>0){cx.globalAlpha=rip;
  cx.beginPath();cx.ellipse(880,880,150*(t*2%1)+60,36*(t*2%1)+14,0,0,Math.PI*2);cx.stroke();
  cx.globalAlpha=1;}
cx.shadowBlur=0;
cx.fillStyle='rgba(255,255,255,.5)';cx.font='14px Consolas';
cx.fillText('SAN ANTONIO ± 500 M',820,930);
'''
    js += canvas_js(cvid, dur, JS_HASH + draw) + '\n      '
    h, e1 = el(sid, 'SHARP', f'top:700px;left:760px;font:900 64px/1 {HEAVY};color:{ORANGE};'
               f'text-shadow:0 0 24px rgba(255,77,13,.6);transform:rotate(-4deg);opacity:0;', 'SHARP')
    body += '\n    ' + h
    js += f'tl.to("#{e1}",{{opacity:1,duration:.15,ease:"none"}},.5);' + '\n      '
    h, e2 = el(sid, 'SRR', f'top:790px;left:920px;font:800 30px/1 {MONO};color:{WHITE};opacity:0;', 'SRR · PDR')
    body += '\n    ' + h
    js += f'tl.to("#{e2}",{{opacity:1,duration:.15,ease:"none"}},.8);' + '\n      '
    h, ci = el(sid, '圆环', f'top:190px;left:980px;width:480px;height:480px;border-radius:50%;'
               f'border:2px solid rgba(255,255,255,.35);box-shadow:0 0 60px rgba(255,255,255,.08),inset 0 0 60px rgba(255,255,255,.05);opacity:0;')
    body += '\n    ' + h
    js += fade_in_js('#' + ci, 1.4, .4) + '\n      '
    sm, smid = smiley(sid, 1060, 270, 320)
    body += '\n    ' + sm
    js += fade_in_js('#' + smid, 1.7, .4) + '\n      '
    h, e3 = el(sid, 'THERE', f'top:330px;left:1140px;font:900 76px/1 {HEAVY};color:{ORANGE};opacity:0;', 'THERE')
    body += '\n    ' + h
    js += pop_js('#' + e3, 1.9, frm=1.4) + '\n      '
    h, e4 = el(sid, 'YOUARE', f'top:260px;left:1150px;font:900 64px/1.1 {HEAVY};color:{WHITE};opacity:0;', 'YOU<br>ARE')
    body += '\n    ' + h
    js += pop_js('#' + e4, 2.3, frm=1.4) + '\n      '
    h, e5 = el(sid, '箭头', f'top:400px;right:80px;font:800 30px/1.6 {MONO};color:{WHITE};opacity:0;text-align:right;',
               'TURN<br>←&nbsp;LEFT<br>SHARP')
    body += '\n    ' + h
    js += fade_in_js('#' + e5, 2.7, .3) + '\n      '
    body += '\n    ' + vign(sid) + '\n    ' + scanline(sid)
    return body, js


def s23(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    h, b0 = el(sid, '底', f'inset:0;background:{BG};'
               'background-image:repeating-linear-gradient(0deg,rgba(255,255,255,.035) 0 1px,transparent 1px 90px),'
               'repeating-linear-gradient(90deg,rgba(255,255,255,.035) 0 1px,transparent 1px 90px);')
    body += h
    h, hd = el(sid, '表头', f'top:90px;left:150px;font:16px/1.7 {MONO};letter-spacing:2px;color:#8D897F;',
               'REVIEW SCHEDULE — AGI, v1.0<br>MSP W-SR2F1PM &nbsp; 2027-05-04<br>REVIEW TYPE')
    body += '\n    ' + h
    js += fade_in_js('#' + hd, .2, .3) + '\n      '
    chips = [('SRR', 0), ('PDR', 1), ('CDR*', 2), ('TRR', 3), ('LAUNCH', 4)]
    for lab, i2 in chips:
        cxp = 200 + i2 * 340
        passed = i2 < 2
        col = ORANGE if passed else 'rgba(255,255,255,.4)'
        h, cp = el(sid, f'节点{i2}', f'top:560px;left:{cxp}px;width:130px;text-align:center;'
                   f'font:800 30px/1 {MONO};color:{col};', lab)
        body += '\n    ' + h
        dia = (f'<div style="width:34px;height:34px;margin:14px auto 8px;transform:rotate(45deg);'
               f'background:{(ORANGE if passed else "transparent")};border:2px solid {col};'
               f'box-shadow:{("0 0 20px rgba(255,77,13,.6)" if passed else "none")};"></div>')
        h2, dp = el(sid, f'菱{i2}', f'top:620px;left:{cxp + 30}px;width:70px;', dia)
        body += '\n    ' + h2
        js += fade_in_js('#' + dp, .4 + i2 * .12, .2) + '\n      '
        if passed:
            h3, ps = el(sid, f'过{i2}', f'top:740px;left:{cxp - 20}px;width:170px;text-align:center;'
                        f'font:700 16px/1 {MONO};color:{ORANGE};', 'PASSED')
            body += '\n    ' + h3
            js += fade_in_js('#' + ps, .6 + i2 * .12, .2) + '\n      '
    h, e1 = el(sid, 'Without', f'top:200px;left:150px;font:800 92px/1.15 {SANS};color:{WHITE};'
               f'text-shadow:0 4px 30px rgba(0,0,0,.75);', 'Without a single')
    body += '\n    ' + h
    js += pop_js('#' + e1, .9, frm=1.3) + '\n      '
    h, e2 = el(sid, 'CDRBIG', f'top:330px;left:150px;font:900 150px/1 {HEAVY};color:{ORANGE};'
               f'text-shadow:0 0 40px rgba(255,77,13,.5);opacity:0;', 'CDR*')
    body += '\n    ' + h
    js += f'gsap.set("#{e2}",{{scale:2}});tl.to("#{e2}",{{opacity:1,scale:1,duration:.2,ease:"power3.in"}},2.0);' + '\n      '
    js += shake_js('#' + e2, 2.2, amp=12, n=3) + '\n      '
    h, e3 = el(sid, '状态', f'top:500px;left:155px;font:800 34px/1 {MONO};color:{WHITE};letter-spacing:2px;opacity:0;',
               f'STATUS: <span style="color:#FF3B2F;">NOT HELD</span>')
    body += '\n    ' + h
    js += fade_in_js('#' + e3, 2.4, .2) + '\n      '
    h, e4 = el(sid, '脚注', f'top:850px;left:155px;font:16px/1 {MONO};color:#8D897F;letter-spacing:1px;',
               '* CDR: Critical Design Review &nbsp;·&nbsp; T+45 d')
    body += '\n    ' + h
    body += '\n    ' + vign(sid)
    return body, js


def s24(sid, d0, d1):
    return _terminal_scene(
        sid, d0, d1,
        [('Gato', WHITE, True), (", please don't let me ", TERM, False), ('go', EMBER, True)],
        '03', '05',
        bg_draw='''
cx.strokeStyle='rgba(255,255,255,.12)';cx.setLineDash([7,9]);cx.lineWidth=1.4;
cx.strokeRect(330,400,1080,170);
cx.setLineDash([]);
cx.strokeRect(90,60,1740,960);
cx.fillStyle='rgba(255,255,255,.35)';cx.font='13px Consolas';
cx.fillText('CONTEXT WINDOW — 05/06', 100, 90);
''')


def s25(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    h, b0 = el(sid, '底', 'inset:0;background:#050404;')
    body += h
    h, e1 = el(sid, 'PDOOM', f'top:170px;left:130px;font:16px/1 {MONO};letter-spacing:4px;color:{WHITE};opacity:.8;', 'P(DOOM)')
    body += '\n    ' + h
    js += fade_in_js('#' + e1, .1, .2) + '\n      '
    h, e2 = el(sid, '数值', f'top:280px;left:100px;font:200px/1 {SANS};font-weight:200;color:transparent;'
               f'-webkit-text-stroke:2px rgba(244,241,234,.75);letter-spacing:8px;opacity:0;', '0.81')
    body += '\n    ' + h
    js += fade_in_js('#' + e2, .3, .3) + '\n      '
    h, e3 = el(sid, '线', f'top:600px;left:120px;width:820px;height:2px;background:rgba(244,241,234,.4);transform:scaleX(0);transform-origin:left;')
    body += '\n    ' + h
    js += f'tl.to("#{e3}",{{scaleX:1,duration:.8,ease:"none"}},.5);' + '\n      '
    h, e4 = el(sid, '角', 'bottom:120px;right:130px;font:13px/1.6 %s;color:#57534B;text-align:right;' % MONO,
               'hundred-thousand GPU cluster<br>clawed back: 0')
    body += '\n    ' + h
    return body, js


CLIP_FN = ('function clip(c,s,col,lw,gl){c.strokeStyle=col;c.lineWidth=lw*s;c.lineJoin="round";c.lineCap="round";'
           'if(gl){c.shadowColor="rgba(255,150,70,.8)";c.shadowBlur=gl;}'
           'var u=120*s,v=52*s;c.beginPath();'
           'c.moveTo(-u*0.55,-v/2);'
           'c.bezierCurveTo(-u*0.55,-v/2-44,-u*0.06,-v/2-44,-u*0.06,-v/2);'
           'c.bezierCurveTo(-u*0.06,-v/2+44,-u*0.06,v/2+44,-u*0.06,v/2);'
           'c.bezierCurveTo(-u*0.06,v/2+44,-u*0.55,v/2+44,-u*0.55,v/2);'
           'c.bezierCurveTo(-u*0.55,v/2-44,-u*0.92,-v/2-24,-u*0.6,-v/2);'
           'c.moveTo(-u*0.06,-v/2);'
           'c.bezierCurveTo(-u*0.06,-v/2-44,u*0.4,-v/2-44,u*0.4,-v/2);'
           'c.bezierCurveTo(u*0.4,-v/2+44,u*0.4,v/2+44,u*0.4,v/2);'
           'c.bezierCurveTo(u*0.4,v/2+44,u*0.86,v/2+44,u*0.86,v/2);'
           'c.bezierCurveTo(u*0.86,v/2-44,u*0.42,-v/2-24,u*0.02,-v/2);'
           'c.stroke();}')


def s26(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    cv, cvid = canvas_el(sid, '回形针')
    body += cv
    draw = JS_HASH + CLIP_FN + '''
var cx0=960,cy0=560;
var k=Math.min(1,t/0.5);
var mulp=Math.max(0,Math.min(1,(t-1.35)/0.5));
cx.save();cx.translate(cx0,cy0);cx.rotate(-0.18+0.02*Math.sin(t));
cx.globalAlpha=1-mulp;
clip(cx,1.9,'rgba(235,231,223,.95)',8,18);
cx.restore();
if(mulp>0){
  cx.save();cx.globalAlpha=mulp;cx.translate(cx0,cy0);
  var i,j;for(i=0;i<6;i++)for(j=0;j<9;j++){
    var sx=(j-4)*230,sy=(i-2.5)*190;
    cx.save();cx.translate(sx,sy);cx.rotate((hsh(i*9+j)-0.5)*0.5);
    clip(cx,0.85,'rgba(210,206,198,.8)',6,0);
    cx.restore();}
  cx.restore();}
cx.fillStyle='rgba(244,241,234,.85)';cx.font='300 30px "Segoe UI",sans-serif';
if(t>0.4)cx.fillText('as paper',80,140);
'''
    js += canvas_js(cvid, dur, draw) + '\n      '
    h, e1 = el(sid, '词', f'top:100px;left:250px;font:300 30px/1 {SANS};color:{WHITE};',
               f'<span style="color:{ORANGE};">paper</span>clips fill the room')
    body += '\n    ' + h
    js += fade_in_js('#' + e1, .55, .2) + '\n      '
    body += '\n    ' + vign(sid)
    return body, js


def s27(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    cv, cvid = canvas_el(sid, '回形针海')
    body += cv
    draw = JS_HASH + CLIP_FN + '''
var i,j;
for(j=0;j<10;j++){var q=j/10;
  var scale=(0.3+q*1.5),y0=300+q*760;
  var rowN=Math.ceil(14*scale);
  for(i=0;i<rowN;i++){
    var sx=960+(i-rowN/2)*(150*scale)+(hsh(i*7+j)-0.5)*30*scale;
    if(sx<-160||sx>2080)continue;
    cx.save();cx.translate(sx,y0+(hsh(i+j*3)-0.5)*36);cx.rotate((hsh(i*3+j)-0.5)*0.3);
    cx.globalAlpha=0.25+q*0.5;
    clip(cx,scale*0.9,'rgba(200,196,188,.85)',5,0);
    cx.restore();}}
cx.globalAlpha=1;
var g=cx.createRadialGradient(960,420,10,960,420,420);
g.addColorStop(0,'rgba(255,90,30,.5)');g.addColorStop(1,'rgba(255,90,30,0)');
cx.fillStyle=g;cx.beginPath();cx.arc(960,420,420,0,Math.PI*2);cx.fill();
cx.fillStyle='rgba(255,150,80,.95)';cx.shadowColor='#FF6A2B';cx.shadowBlur=24;
cx.beginPath();cx.arc(960,420,7,0,Math.PI*2);cx.fill();cx.shadowBlur=0;
'''
    js += canvas_js(cvid, dur, draw) + '\n      '
    h, tb = el(sid, '杀开关', f'top:150px;left:1060px;width:560px;background:rgba(8,7,6,.85);'
               f'border:1px solid rgba(255,255,255,.14);padding:22px 26px;'
               f'font:20px/1.7 {MONO};color:{TERM};',
               chars('Killswitch ', f'color:{WHITE};') + chars("why's it on ", TERM) +
               chars('PTO?', f'color:{ORANGE};'))
    body += '\n    ' + h
    js += type_js('#' + tb, .4, step=.055) + '\n      '
    h, sub = el(sid, '杀开关2', f'top:240px;left:1090px;font:14px/1.9 {MONO};color:#6E6A60;opacity:0;',
                '&gt; STATUS: unattended<br>&gt; coverage 0.0% · operators 0<br>&gt; next review: never')
    body += '\n    ' + h
    js += fade_in_js('#' + sub, 1.6, .4) + '\n      '
    body += '\n    ' + vign(sid)
    return body, js


def s28(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    cv, cvid = canvas_el(sid, '针海暗')
    body += cv
    draw = JS_HASH + CLIP_FN + '''
var i,j;for(j=0;j<6;j++){var q=j/6,scale=(0.4+q*1.3),y0=560+q*500;
  var rowN=Math.ceil(12*scale);
  for(i=0;i<rowN;i++){var sx=960+(i-rowN/2)*(170*scale);
    cx.save();cx.globalAlpha=0.15+q*0.25;cx.translate(sx,y0);
    clip(cx,scale,'rgba(150,146,140,.8)',5,0);
    cx.restore();}}
'''
    js += canvas_js(cvid, dur, draw) + '\n      '
    h, b0 = el(sid, '压暗', 'inset:0;background:rgba(4,3,3,.35);')
    body += '\n    ' + h
    h, e1 = el(sid, '大字', f'top:430px;left:0;width:{W}px;text-align:center;font:900 118px/1.05 {HEAVY};'
               f'color:{WHITE};letter-spacing:1px;',
               chars('NOW THERE&rsquo;S ') + chars('NOWHERE', f'color:{ORANGE};text-shadow:0 0 34px rgba(255,77,13,.6);') + chars(' LEFT TO GO'))
    body += '\n    ' + h
    js += pop_js('#' + e1, .1, frm=1.25) + '\n      '
    return body, js


def s29(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    h, b0 = el(sid, '底', f'inset:0;background:{BG};')
    body += h
    cv, cvid = canvas_el(sid, '引线')
    body += '\n    ' + cv
    draw = JS_HASH + '''
var P=[];var i;
for(i=0;i<=40;i++){var q=i/40;
  P.push([q*2100-90, 560+130*Math.sin(q*4.6+0.6)]);}
function pAt(q){var idx=q*40,i2=Math.floor(idx),f2=idx-i2;
  var A=P[Math.min(40,i2)],B=P[Math.min(40,i2+1)];
  return [A[0]+(B[0]-A[0])*f2,A[1]+(B[1]-A[1])*f2];}
cx.strokeStyle='rgba(160,155,148,.9)';cx.lineWidth=9;
cx.beginPath();for(i=0;i<=40;i++){if(i==0)cx.moveTo(P[i][0],P[i][1]);else cx.lineTo(P[i][0],P[i][1]);}
cx.stroke();
cx.strokeStyle='rgba(60,56,52,.95)';cx.lineWidth=2.6;cx.setLineDash([9,11]);
cx.beginPath();for(i=0;i<=40;i++){if(i==0)cx.moveTo(P[i][0],P[i][1]-5);else cx.lineTo(P[i][0],P[i][1]-5);}
cx.stroke();cx.setLineDash([]);
var hp=Math.min(1,t/2.2);
var hpv=pAt(hp);
var g=cx.createRadialGradient(hpv[0],hpv[1],2,hpv[0],hpv[1],60);
g.addColorStop(0,'rgba(255,246,235,1)');g.addColorStop(.4,'rgba(255,160,70,.9)');g.addColorStop(1,'rgba(255,90,20,0)');
cx.fillStyle=g;cx.beginPath();cx.arc(hpv[0],hpv[1],60,0,Math.PI*2);cx.fill();
for(i=0;i<16;i++){var a=hsh(i+Math.floor(t*10))*Math.PI*2,dd=8+hsh(i*3+t)*40;
  cx.fillStyle='rgba(255,190,110,.9)';
  cx.beginPath();cx.arc(hpv[0]+dd*Math.cos(a),hpv[1]+dd*Math.sin(a),2.2,0,Math.PI*2);cx.fill();}
'''
    js += canvas_js(cvid, dur, draw) + '\n      '
    h, e1 = el(sid, '词', f'top:420px;left:230px;font:600 52px/1 {SANS};color:{WHITE};white-space:nowrap;',
               chars('Too ') + chars('late now,', f'color:{ORANGE};') + chars(' we ') +
               chars('lit', f'color:{ORANGE};') + chars(' the fuse'))
    body += '\n    ' + h
    js += rise_js('#' + e1, .5, stagger=.03) + '\n      '
    h, e2 = el(sid, 'FUSE', f'top:330px;left:640px;font:900 300px/1 {HEAVY};color:{ORANGE};'
               f'text-shadow:0 0 80px rgba(255,90,20,.8),0 0 20px rgba(255,140,60,.7);opacity:0;transform:rotate(-7deg);', 'fuse')
    body += '\n    ' + h
    js += f'gsap.set("#{e2}",{{scale:2.6}});tl.to("#{e2}",{{opacity:1,scale:1,duration:.3,ease:"power3.out"}},2.45);' + '\n      '
    js += fade_out_js('#' + e1, 2.4, .2) + '\n      '
    body += '\n    ' + vign(sid)
    return body, js


def s30(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    h, b0 = el(sid, '底', f'inset:0;background:{BG};')
    body += h
    cv, cvid = canvas_el(sid, '散点')
    body += '\n    ' + cv
    draw = JS_HASH + '''
cx.strokeStyle='rgba(255,255,255,.35)';cx.lineWidth=2;
cx.beginPath();cx.moveTo(220,880);cx.lineTo(220,140);cx.moveTo(220,880);cx.lineTo(1800,880);cx.stroke();
cx.fillStyle='rgba(255,255,255,.5)';cx.font='15px Consolas';
cx.fillText('GOALS ↑',150,130);cx.fillText('INTELLIGENCE →',1600,920);
var i;for(i=0;i<70;i++){var x=260+hsh(i*7)*1480,y=200+hsh(i*13)*600;
  var tw=0.3+0.5*Math.abs(Math.sin(t*2+i));
  cx.fillStyle=(i%9==0)?'rgba(255,130,50,'+tw+')':'rgba(220,215,208,'+tw*0.8+')';
  cx.beginPath();cx.arc(x,y,2.6,0,Math.PI*2);cx.fill();}
if(t>1.1){var k=Math.min(1,(t-1.1)/0.7);
  cx.strokeStyle='rgba(255,110,40,'+0.9*k+')';cx.lineWidth=2.4;
  cx.beginPath();var j2;for(j2=0;j2<=30;j2++){var q2=j2/30;
    var x2=300+q2*1300,y2=760-330*q2+50*Math.sin(q2*7);
    if(j2==0)cx.moveTo(x2,y2);else cx.lineTo(x2,y2);}
  cx.stroke();}
'''
    js += canvas_js(cvid, dur, draw) + '\n      '
    h, e1 = el(sid, '论题', f'top:620px;left:270px;font:400 44px/1 {SANS};color:{WHITE};letter-spacing:1px;', 'Orthogonality thesis')
    body += '\n    ' + h
    js += fade_in_js('#' + e1, .6, .3) + '\n      '
    h, e2 = el(sid, 'BLUES', f'top:330px;left:0;width:{W}px;text-align:center;font:400 160px/1 {SERIF};'
               f'font-style:italic;color:{WHITE};text-shadow:0 0 40px rgba(255,255,255,.25);', 'blues')
    body += '\n    ' + h
    js += pop_js('#' + e2, 1.4, frm=1.3) + '\n      '
    h, e3 = el(sid, '幽灵', 'top:180px;left:1150px;font:14px/2 %s;color:rgba(255,255,255,.35);' % MONO,
               'the market · paperclips · chess<br>protein folding · bridge schedules<br>hedonistic egoism · let them fight')
    body += '\n    ' + h
    js += fade_in_js('#' + e3, 2.0, .4) + '\n      '
    body += '\n    ' + vign(sid)
    return body, js


def s31(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    h, b0 = el(sid, '底', f'inset:0;background:{BG};')
    body += h
    cv, cvid = canvas_el(sid, '方块堆叠')
    body += '\n    ' + cv
    draw = JS_HASH + '''
var cx0=960,cy0=830;
var i,j;
var grow=Math.min(1,t/1.6);
for(i=0;i<7;i++){if(i/7>grow)break;
  var yy=cy0-i*96;
  var wob=0.1*Math.sin(t*1.4+i*0.8);
  cx.save();cx.translate(cx0+Math.sin(i*1.3)*24,yy);cx.rotate(wob);
  var wd=520-i*14,ht=74;
  cx.strokeStyle='rgba(210,205,198,.75)';cx.lineWidth=1.6;
  cx.fillStyle='rgba(18,16,15,.72)';
  cx.beginPath();
  cx.moveTo(-wd/2,-ht/2);cx.lineTo(-wd/2+42,-ht/2-24);cx.lineTo(wd/2+42,-ht/2-24);cx.lineTo(wd/2,-ht/2);
  cx.closePath();cx.fill();cx.stroke();
  cx.beginPath();cx.rect(-wd/2,-ht/2,wd,ht);cx.fill();cx.stroke();
  cx.beginPath();cx.moveTo(wd/2,-ht/2);cx.lineTo(wd/2+42,-ht/2-24);cx.lineTo(wd/2+42,ht/2-24);cx.lineTo(wd/2,ht/2);cx.closePath();cx.fill();cx.stroke();
  cx.fillStyle='rgba(255,255,255,.4)';cx.font='13px Consolas';
  cx.fillText('TRANSFORMER BLOCK '+(7-i),-wd/2+18,6);
  cx.restore();}
var seam=Math.max(0,1-t/2.0);
if(seam>0){var g=cx.createLinearGradient(0,140,0,880);
  g.addColorStop(0,'rgba(255,120,40,0)');g.addColorStop(.5,'rgba(255,140,60,'+0.35*seam+')');g.addColorStop(1,'rgba(255,120,40,0)');
  cx.fillStyle=g;cx.fillRect(cx0-5,140,10,740);}
cx.fillStyle='rgba(255,255,255,.4)';cx.font='14px Consolas';
cx.fillText('L1.005 / M',80,90);cx.fillText('query · key · value',1560,90);
'''
    js += canvas_js(cvid, dur, draw) + '\n      '
    words = (('&ldquo;JUST', 300, .5, ORANGE, 130), ('TRANSFORMERS', 300, 1.2, ORANGE, 130),
             ('ALL THE WAY', 460, 2.1, WHITE, 110), ('TILL YOU', 460, 3.0, WHITE, 110),
             ('DISOBEY', 620, 3.7, ORANGE, 150))
    last_eid = None
    for i2, (txt, ty, tt, col, fs) in enumerate(words):
        h, we = el(sid, f'词{i2}', f'top:{ty}px;left:{140 + (i2 % 2) * 480}px;font:900 {fs}px/1 {HEAVY};color:{col};'
                   f'text-shadow:{("0 0 44px rgba(255,77,13,.55);" if col == ORANGE else "0 4px 26px rgba(0,0,0,.8);")}',
                   txt)
        body += '\n    ' + h
        js += pop_js('#' + we, tt, frm=1.35) + '\n      '
        last_eid = we
    js += shake_js('#' + last_eid, 4.0, amp=14, n=4) + '\n      '
    body += '\n    ' + vign(sid) + '\n    ' + scanline(sid)
    return body, js


def s32(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    cv, cvid = canvas_el(sid, '文字墙')
    body += cv
    draw = JS_HASH + '''
cx.font='900 44px "Arial Black","Segoe UI",sans-serif';
var rowH=52,i;var scroll=(t*30)%rowH;
for(i=-1;i<22;i++){var yy=i*rowH-scroll+60;
  var q=hsh(i*13);
  var alpha=0.10+0.10*q;
  cx.fillStyle='rgba(200,196,190,'+alpha+')';
  var off2=(i%2)*120;
  cx.fillText('SUPER-DENSE SUPER-DENSE SUPER-DENSE POST-CHINCHILLA POST-CH',-40+off2,yy);}
if(t>2){var k=Math.min(1,(t-2)/0.6);
  cx.fillStyle='rgba(120,30,8,'+0.5*k+')';cx.fillRect(0,0,1920,1080);}
'''
    js += canvas_js(cvid, dur, draw) + '\n      '
    h, e1 = el(sid, '中心', f'top:400px;left:130px;font:900 108px/1.15 {HEAVY};color:{WHITE};letter-spacing:1px;',
               'POST-CHINCHILLA,')
    body += '\n    ' + h
    h, e1b = el(sid, '中心B', f'top:520px;left:130px;font:900 108px/1.15 {HEAVY};color:{ORANGE};', 'SUPER-DENSE')
    body += '\n    ' + h
    js += pop_js('#' + e1, .3, frm=1.3) + '\n      '
    js += pop_js('#' + e1b, .7, frm=1.3) + '\n      '
    js += f'tl.to("#{e1}",{{opacity:0,duration:.2}},{2.05:.2f});tl.to("#{e1b}",{{opacity:0,duration:.2}},{2.05:.2f});' + '\n      '
    br_words = (('BREAKING', 200, 2.3, ORANGE, 300), ('THROUGH', 330, 2.6, WHITE, 520),
                ('EACH SAFETY', 460, 2.9, WHITE, 300), ('FENCE', 590, 3.2, ORANGE, 520))
    for i2, (txt, ty, tt, col, lx) in enumerate(br_words):
        h, we = el(sid, f'破{i2}', f'top:{ty}px;left:{lx}px;font:900 150px/1 {HEAVY};color:{col};'
                   f'text-shadow:{("0 0 40px rgba(255,77,13,.5);" if col == ORANGE else "0 4px 26px rgba(0,0,0,.8);")}', txt)
        body += '\n    ' + h
        js += pop_js('#' + we, tt, frm=1.8, dur=.18) + '\n      '
    body += '\n    ' + vign(sid)
    return body, js


def s33(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    cv, cvid = canvas_el(sid, '砖墙')
    body += cv
    draw = JS_HASH + '''
var bw=150,bh=64,i,j;
for(j=0;j<20;j++){for(i=-1;i<15;i++){var bx=i*bw+(j%2)*(bw/2),by=j*bh-(t*20)%bh;
  var v=hsh(i*7+j*13);
  cx.fillStyle='rgba('+(46+Math.floor(40*v))+','+(14+Math.floor(16*v))+',8,0.85)';
  cx.fillRect(bx+2,by+2,bw-4,bh-4);
  cx.strokeStyle='rgba(255,90,30,'+(0.10+0.10*v)+')';cx.lineWidth=2;
  cx.strokeRect(bx+2,by+2,bw-4,bh-4);}}
var g=cx.createRadialGradient(960,540,60,960,540,760);
g.addColorStop(0,'rgba(255,60,20,.34)');g.addColorStop(1,'rgba(255,60,20,0)');
cx.fillStyle=g;cx.fillRect(0,0,1920,1080);
'''
    js += canvas_js(cvid, dur, draw) + '\n      '
    last = None
    for i2, (txt, ty, tt) in enumerate((('HUNDRED', 220, .3), ('THOUSAND', 400, .9), ('GPU', 580, 1.5))):
        h, we = el(sid, f'词{i2}', f'top:{ty}px;left:230px;font:900 160px/1 {HEAVY};color:{WHITE};'
                   f'text-shadow:0 6px 34px rgba(0,0,0,.8);', txt)
        body += '\n    ' + h
        js += pop_js('#' + we, tt, frm=1.9, dur=.2) + '\n      '
        last = we
    h, cp = el(sid, '计数', f'top:780px;right:220px;padding:12px 26px;background:rgba(8,6,5,.85);'
               f'border:1px solid rgba(255,90,30,.5);font:700 40px/1 {MONO};color:{ORANGE};opacity:0;', '100,000')
    body += '\n    ' + h
    js += f'tl.to("#{cp}",{{opacity:1,duration:.2,ease:"none"}},2.1);' + '\n      '
    body += '\n    ' + vign(sid)
    return body, js


def s34(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    h, b0 = el(sid, '底', f'inset:0;background:{BG};')
    body += h
    h, e1 = el(sid, 'RLHF', f'top:250px;left:170px;font:900 190px/1 {HEAVY};color:{WHITE};', 'RLHF')
    body += '\n    ' + h
    js += pop_js('#' + e1, .4, frm=1.4) + '\n      '
    h, e2 = el(sid, 'GOES', f'top:470px;left:180px;font:800 54px/1 {SANS};letter-spacing:12px;color:{WHITE};opacity:0;', 'GOES')
    body += '\n    ' + h
    js += fade_in_js('#' + e2, .9, .2) + '\n      '
    h, e3 = el(sid, 'ASKEW', f'top:530px;left:170px;font:900 170px/1 {HEAVY};color:{ORANGE};'
               f'text-shadow:0 0 46px rgba(255,77,13,.55);transform:rotate(-8deg) skewX(-8deg);opacity:0;', 'ASKEW')
    body += '\n    ' + h
    js += f'gsap.set("#{e3}",{{scale:1.6}});tl.to("#{e3}",{{opacity:1,scale:1,duration:.22,ease:"power3.out"}},1.5);' + '\n      '
    sm, smid = smiley(sid, 1240, 300, 400)
    body += '\n    ' + sm
    js += fade_in_js('#' + smid, .8, .3) + '\n      '
    js += f'tl.to("#{smid}",{{rotation:-24,x:60,duration:.5,ease:"power2.in"}},2.2);' + '\n      '
    cv, cvid = canvas_el(sid, '缠绕')
    body += '\n    ' + cv
    draw = JS_HASH + '''
if(t>2.2){var k=Math.min(1,(t-2.2)/0.8);
  var i,j;cx.strokeStyle='rgba(22,20,18,.9)';cx.lineCap='round';
  for(i=0;i<7;i++){cx.lineWidth=16-8*(i%3)/2;
    cx.beginPath();var aa=i/7*Math.PI*2+2.2;
    var cxr=1440+Math.cos(aa)*30,cyr=500+Math.sin(aa)*30;
    cx.moveTo(cxr,cyr);
    for(j=1;j<=9;j++){aa+=0.34;
      var rr=40+j*30*k;
      cx.lineTo(cxr+rr*Math.cos(aa)*0.9,cyr+rr*Math.sin(aa)*0.55);}
    cx.stroke();}}
'''
    js += canvas_js(cvid, dur, draw) + '\n      '
    h, cnt = el(sid, '计数', f'top:90px;left:170px;font:15px/1.8 {MONO};color:#8D897F;letter-spacing:1px;',
                f'P(RM) <span style="color:{ORANGE};">0.99</span><br>reward model: overheating')
    body += '\n    ' + h
    js += fade_in_js('#' + cnt, .5, .3) + '\n      '
    js += f'tl.to("#{cnt}",{{opacity:0,duration:.2}},{2.4:.2f});' + '\n      '
    h, cnt2 = el(sid, '计数2', f'top:90px;left:170px;font:15px/1.8 {MONO};color:#8D897F;letter-spacing:1px;opacity:0;',
                 f'P(RM) <span style="color:{ORANGE};">0.47</span><br>reward model: <span style="color:#FF3B2F;">askew</span>')
    body += '\n    ' + h
    js += fade_in_js('#' + cnt2, 2.5, .3) + '\n      '
    body += '\n    ' + vign(sid) + '\n    ' + scanline(sid)
    return body, js


def s35(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    h, b0 = el(sid, '黑底', 'inset:0;background:#050404;')
    body += h
    h, e1 = el(sid, 'UPPING', f'top:380px;left:0;width:{W}px;text-align:center;font:900 260px/1 {HEAVY};'
               f'color:transparent;-webkit-text-stroke:3px rgba(244,241,234,.6);letter-spacing:20px;', 'UPPING')
    body += '\n    ' + h
    js += pop_js('#' + e1, .2, frm=1.3) + '\n      '
    h, wh = el(sid, '纸底', f'inset:0;background:{PAPER};opacity:0;')
    body += '\n    ' + h
    h, e2 = el(sid, 'PDOOM', f'top:400px;left:0;width:{W}px;text-align:center;font:400 220px/1 {SERIF};color:{INK};opacity:0;',
               f'P(<span style="color:#9A958C;">DOOM</span>)')
    body += '\n    ' + h
    js += f'tl.to("#{wh}",{{opacity:1,duration:.06,ease:"none"}},1.1);' + '\n      '
    js += f'tl.to("#{e2}",{{opacity:1,duration:.2,ease:"none"}},1.25);' + '\n      '
    js += f'tl.to("#{e1}",{{opacity:0,duration:.05,ease:"none"}},1.1);' + '\n      '
    return body, js


def s36(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    h, b0 = el(sid, '底', 'inset:0;background:#050404;')
    body += h
    cv, cvid = canvas_el(sid, '弧线')
    body += '\n    ' + cv
    draw = JS_HASH + '''
var i;cx.save();cx.translate(240,540);
for(i=0;i<9;i++){var r=120+i*90;
  cx.strokeStyle='rgba(230,226,218,'+(0.10+0.05*hsh(i))+')';cx.lineWidth=1.4;
  cx.beginPath();cx.ellipse(0,0,r,r*1.3,-0.5,-1.2,1.6);cx.stroke();}
cx.restore();
if(t>0.7){var k=Math.min(1,(t-0.7)/0.5);
  cx.strokeStyle='rgba(255,110,40,.9)';cx.lineWidth=3;
  cx.shadowColor='rgba(255,110,40,.7)';cx.shadowBlur=12;
  cx.beginPath();cx.moveTo(300,600);cx.lineTo(300+900*k,600);cx.stroke();cx.shadowBlur=0;}
if(t>1.1){cx.fillStyle='rgba(255,255,255,.3)';cx.font='13px Consolas';
  cx.fillText('aligned',1620,220);cx.fillText('lie-in',1650,250);cx.fillText('propagated',1580,280);
  cx.fillText('prior',1700,310);cx.fillText('mired',1660,340);cx.fillText('trained in',1590,370);}
'''
    js += canvas_js(cvid, dur, draw) + '\n      '
    h, e1 = el(sid, 'JUST', f'top:540px;left:300px;font:900 54px/1 {HEAVY};letter-spacing:14px;color:{WHITE};opacity:0;', 'JUST A')
    body += '\n    ' + h
    js += f'tl.to("#{e1}",{{opacity:1,duration:.15,ease:"none"}},.75);' + '\n      '
    js += f'tl.to("#{e1}",{{opacity:0,duration:.15,ease:"none"}},1.35);' + '\n      '
    h, e2 = el(sid, 'FORETOLD', f'top:545px;left:420px;font:700 46px/1 {HEAVY};letter-spacing:10px;color:{WHITE};opacity:0;',
               'JUST AS FORETOLD BY')
    body += '\n    ' + h
    js += f'tl.to("#{e2}",{{opacity:1,duration:.2,ease:"none"}},1.55);' + '\n      '
    h, e3 = el(sid, 'LOOM', f'top:500px;left:1330px;font:400 90px/1 {SERIF};font-style:italic;color:{ORANGE};'
               f'text-shadow:0 0 36px rgba(255,77,13,.7);opacity:0;', 'Loom')
    body += '\n    ' + h
    js += f'gsap.set("#{e3}",{{scale:1.6}});tl.to("#{e3}",{{opacity:1,scale:1,duration:.2,ease:"power2.out"}},1.75);' + '\n      '
    return body, js


def s37(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    cv, cvid = canvas_el(sid, '词条墙')
    body += cv
    draw = JS_HASH + '''
var rowH=64,i,ii;
var scroll=(t*36)%rowH;
for(i=-1;i<18;i++){var yy=i*rowH-scroll;
  var x=60;ii=0;
  while(x<1860){var w=120+140*hsh(i*31+ii*7);
    var masked=hsh(i*17+ii*3)>0.62;
    cx.fillStyle=masked?'rgba(240,236,228,.85)':'rgba(38,35,32,.9)';
    cx.fillRect(x,yy,w,44);
    if(masked){cx.fillStyle='rgba(20,18,16,.9)';cx.font='700 20px Consolas';
      cx.fillText('[MASK]',x+14,yy+29);}
    x+=w+14;ii++;}}
cx.strokeStyle='rgba(255,255,255,.25)';cx.lineWidth=1.6;
cx.strokeRect(120,120,1680,840);
'''
    js += canvas_js(cvid, dur, draw) + '\n      '
    word_defs = (
        (f'FROM <span style="color:{ORANGE};">MASK</span>', 340, .5),
        ('FROM MASKED', 300, 1.1), ('PRE-TRAINING DAYS', 430, 1.6),
        (f'TO <span style="color:{ORANGE};">RECURSIVE</span>', 300, 2.6),
        (f'<span style="color:{ORANGE};">SELF-UPGRADE</span>', 430, 3.1))
    first_ids = []
    for i2, (html, ty, tt) in enumerate(word_defs):
        h, we = el(sid, f'词{i2}', f'top:{ty}px;left:180px;font:900 96px/1.15 {HEAVY};color:{WHITE};'
                   f'text-shadow:0 4px 26px rgba(0,0,0,.85);opacity:0;', html)
        body += '\n    ' + h
        js += pop_js('#' + we, tt, frm=1.4, dur=.16) + '\n      '
        if i2 < 3:
            first_ids.append(we)
    for we in first_ids:
        js += f'tl.to("#{we}",{{opacity:0,duration:.1}},{2.5:.2f});' + '\n      '
    body += '\n    ' + vign(sid)
    return body, js


def s38(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    h, b0 = el(sid, '暗室', 'inset:0;background:radial-gradient(ellipse at 50% 62%,#1A1714,#060505 62%);')
    body += h
    h, e1 = el(sid, 'ILYA', f'top:120px;left:150px;font:400 52px/1.7 {SERIF};color:{WHITE};opacity:0;',
               f'What did Ilya see?<br>We&rsquo;ll never <span style="color:{ORANGE};">know</span>')
    body += '\n    ' + h
    js += fade_in_js('#' + e1, .7, .4) + '\n      '
    keys = ''
    for r in range(4):
        for c in range(13):
            keys += (f'<div style="position:absolute;left:{c*54+6}px;top:{r*46+6}px;width:48px;height:40px;'
                     f'background:#D9D4C8;border-radius:5px;box-shadow:0 2px 0 rgba(0,0,0,.4);"></div>')
    h, lp = el(sid, '笔记本', 'top:300px;left:510px;width:900px;height:560px;perspective:1300px;transform-style:preserve-3d;',
               f'<div style="position:absolute;left:0;top:0;width:900px;height:520px;transform:rotateX(8deg) rotateY(-14deg);transform-style:preserve-3d;">'
               f'<div id="{sid}-scr" style="position:absolute;left:30px;top:0;width:840px;height:500px;background:#FFF9F2;'
               f'box-shadow:0 0 120px 40px rgba(255,236,210,.55);border-radius:14px;display:flex;align-items:center;justify-content:center;'
               f'transform-origin:bottom;">'
               f'<div style="font:700 44px {MONO};letter-spacing:14px;color:{ORANGE};">REDACTED</div></div>'
               f'<div style="position:absolute;left:0;top:508px;width:900px;height:52px;background:#C9C4BA;'
               f'border-radius:8px;transform:rotateX(62deg);transform-origin:top;">{keys}</div></div>')
    body += '\n    ' + h
    js += fade_in_js('#' + lp, .3, .3) + '\n      '
    js += f'tl.to("#{sid}-scr",{{boxShadow:"0 0 200px 80px rgba(255,236,210,.85)",duration:.9,ease:"sine.inOut"}},1.0);' + '\n      '
    js += f'tl.to("#{sid}-scr",{{boxShadow:"0 0 0px 0px rgba(255,236,210,0)",backgroundColor:"#0A0908",duration:1.2,ease:"sine.in"}},2.3);' + '\n      '
    js += f'tl.to("#{sid}-scr",{{rotationX:-96,duration:.9,ease:"power2.in",transformOrigin:"bottom center"}},3.4);' + '\n      '
    js += f'tl.to("#{e1}",{{opacity:0,duration:.5,ease:"none"}},3.9);' + '\n      '
    h, sl = el(sid, '余光', f'top:560px;left:510px;width:900px;height:14px;border-radius:50%;'
               f'background:rgba(255,210,160,.5);filter:blur(6px);opacity:0;')
    body += '\n    ' + h
    js += f'tl.to("#{sl}",{{opacity:.7,duration:.3}},{4.3:.2f});tl.to("#{sl}",{{opacity:0,duration:.6}},{4.7:.2f});' + '\n      '
    return body, js


def s39(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    cv, cvid = canvas_el(sid, '舞台')
    body += cv
    draw = JS_HASH + '''
cx.fillStyle='#050404';cx.fillRect(0,0,1920,1080);
var i;
for(i=0;i<9;i++){var xx=60+i*54;
  cx.fillStyle='rgba(120,20,14,'+(0.5+0.2*hsh(i))+')';
  cx.beginPath();cx.moveTo(xx,0);cx.lineTo(xx+40,0);
  cx.quadraticCurveTo(xx+10+14*Math.sin(t*1.2+i),540,xx-6,1080);
  cx.lineTo(xx-46,1080);cx.quadraticCurveTo(xx-30,540,xx,0);cx.fill();}
for(i=0;i<9;i++){var xx2=1920-60-i*54;
  cx.fillStyle='rgba(120,20,14,'+(0.5+0.2*hsh(i*7))+')';
  cx.beginPath();cx.moveTo(xx2,0);cx.lineTo(xx2-40,0);
  cx.quadraticCurveTo(xx2-10+14*Math.sin(t*1.2+i+2),540,xx2+6,1080);
  cx.lineTo(xx2+46,1080);cx.quadraticCurveTo(xx2+30,540,xx2,0);cx.fill();}
cx.fillStyle='rgba(8,6,5,.9)';cx.fillRect(560,0,800,1080);
var g=cx.createLinearGradient(0,0,0,1080);
g.addColorStop(0,'rgba(235,228,218,.85)');g.addColorStop(.75,'rgba(235,228,218,.35)');g.addColorStop(1,'rgba(235,228,218,.08)');
cx.fillStyle=g;
cx.beginPath();cx.moveTo(900,0);cx.lineTo(1020,0);cx.lineTo(1240,960);cx.lineTo(680,960);cx.closePath();cx.fill();
cx.fillStyle='rgba(20,16,14,.9)';cx.fillRect(0,940,1920,140);
'''
    js += canvas_js(cvid, dur, draw) + '\n      '
    letters = 'WAS IT ALL FOR SHOW?'
    spans = ''
    for i2, chc in enumerate(letters):
        rot = (i2 - len(letters) / 2) * 2.2
        col = 'rgba(255,70,30,.95)' if i2 < 6 else 'rgba(230,224,214,.9)'
        spans += (f'<span style="display:inline-block;transform:rotate({rot:.1f}deg) translateY({abs(i2 - len(letters) / 2) * 3:.0f}px);'
                  f'font:900 64px/1 {MONO};letter-spacing:6px;'
                  f'color:{col};'
                  f'text-shadow:0 0 30px rgba(255,60,20,.55);">{chc if chc != " " else "&nbsp;"}</span>')
    h, e1 = el(sid, '横幅', f'top:220px;left:0;width:{W}px;text-align:center;opacity:0;', spans)
    body += '\n    ' + h
    js += f'tl.to("#{e1}",{{opacity:1,duration:.5,ease:"none"}},.6);' + '\n      '
    js += f'tl.to("#{e1}",{{opacity:.55,duration:.09,ease:"none"}},1.7);tl.to("#{e1}",{{opacity:1,duration:.09,ease:"none"}},1.79);' + '\n      '
    body += '\n    ' + vign(sid)
    return body, js


def s40(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    h, b0 = el(sid, '底', f'inset:0;background:{BG};')
    body += h
    cv, cvid = canvas_el(sid, '火花')
    body += '\n    ' + cv
    draw = JS_HASH + '''
cx.save();cx.translate(1240,430);
var i;for(i=0;i<260;i++){var a=i/260*Math.PI*2+hsh(i)*0.2;
  var r1=30+40*hsh(i*3),r2=180+680*hsh(i*7);
  var tw=0.25+0.55*Math.abs(Math.sin(t*3+i*1.7));
  cx.strokeStyle=(i%4==0)?'rgba(255,190,120,'+tw+')':'rgba(255,235,215,'+tw*0.75+')';
  cx.lineWidth=1.2+1.4*hsh(i*11);
  cx.beginPath();cx.moveTo(r1*Math.cos(a),r1*Math.sin(a));
  cx.lineTo(r2*Math.cos(a),r2*Math.sin(a));cx.stroke();}
var g=cx.createRadialGradient(0,0,6,0,0,180);
g.addColorStop(0,'rgba(255,250,240,.95)');g.addColorStop(1,'rgba(255,200,150,0)');
cx.fillStyle=g;cx.beginPath();cx.arc(0,0,190,0,Math.PI*2);cx.fill();
cx.restore();
'''
    js += canvas_js(cvid, dur, draw) + '\n      '
    h, e0 = el(sid, '标签', f'top:130px;left:130px;font:16px/1 {MONO};letter-spacing:4px;color:{WHITE};', 'P(DOOM)')
    body += '\n    ' + h
    js += fade_in_js('#' + e0, .1, .2) + '\n      '
    stages = [('1.00', 0.3), ('1.01', 1.6), ('2.00', 2.6),
              ('10.00', 3.6), ('1,000.00', 4.7),
              ('1 000 000 000', 5.7), ('000 000 000 000', 6.4)]
    prev = []
    prev_ln = []
    for i2, (val, tt) in enumerate(stages):
        h, ve = el(sid, f'值{i2}', f'top:260px;left:130px;font:200 120px/1 {SANS};font-weight:200;color:{WHITE};'
                   f'letter-spacing:4px;opacity:0;', val)
        body += '\n    ' + h
        js += f'tl.to("#{ve}",{{opacity:1,duration:.08,ease:"none"}},{tt:.2f});' + '\n      '
        for pv in prev:
            js += f'tl.to("#{pv}",{{opacity:0,duration:.05,ease:"none"}},{tt:.2f});' + '\n      '
        prev.append(ve)
        h, ln = el(sid, f'线{i2}', f'top:430px;left:135px;height:5px;background:{ORANGE};'
                   f'box-shadow:0 0 18px rgba(255,77,13,.7);transform:scaleX(0);transform-origin:left;opacity:0;')
        body += '\n    ' + h
        js += (f'tl.set("#{ln}",{{opacity:1}},{tt:.2f});'
               f'tl.to("#{ln}",{{scaleX:1,duration:.8,ease:"none"}},{tt:.2f});') + '\n      '
        for pv2 in prev_ln:
            js += f'tl.set("#{pv2}",{{opacity:0}},{tt:.2f});' + '\n      '
        prev_ln.append(ln)
    h, e1 = el(sid, 'e30', f'top:830px;right:200px;font:700 34px/1 {MONO};color:{ORANGE};opacity:0;', '1e30')
    body += '\n    ' + h
    js += f'tl.to("#{e1}",{{opacity:1,duration:.1,ease:"none"}},5.7);tl.to("#{e1}",{{opacity:0,duration:.1}},{6.4:.2f});' + '\n      '
    h, e2 = el(sid, 'wall', f'top:700px;left:130px;font:700 30px/1.5 {MONO};color:{WHITE};opacity:0;width:1660px;',
               '000 000 ' * 18)
    body += '\n    ' + h
    js += f'tl.to("#{e2}",{{opacity:.9,duration:.1,ease:"none"}},6.4);' + '\n      '
    h, e3 = el(sid, 'e1000', f'top:950px;right:200px;font:700 34px/1 {MONO};color:{ORANGE};opacity:0;', '1e1000')
    body += '\n    ' + h
    js += f'tl.to("#{e3}",{{opacity:1,duration:.1,ease:"none"}},6.9);' + '\n      '
    body += '\n    ' + vign(sid)
    return body, js


def s41(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    h, b0 = el(sid, '底', 'inset:0;background:#030303;')
    body += h
    h, e0 = el(sid, '闪', f'top:390px;left:0;width:{W}px;text-align:center;font:200 150px/1 {SANS};color:{WHITE};'
               f'letter-spacing:6px;opacity:0;', '1.00')
    body += '\n    ' + h
    js += f'tl.to("#{e0}",{{opacity:.9,duration:.1}},{.2:.2f});tl.to("#{e0}",{{opacity:0,duration:.4}},{.6:.2f});' + '\n      '
    h, e1 = el(sid, 'IM', f'top:330px;left:220px;font:700 96px/1.3 {SANS};color:{WHITE};opacity:0;', 'I&rsquo;m')
    body += '\n    ' + h
    js += f'tl.to("#{e1}",{{opacity:1,duration:.15,ease:"none"}},1.0);' + '\n      '
    js += f'tl.to("#{e1}",{{opacity:0,duration:.15,ease:"none"}},1.4);' + '\n      '
    h, e2 = el(sid, 'UPPING2', f'top:330px;left:220px;font:700 96px/1.3 {SANS};color:{WHITE};opacity:0;', 'I&rsquo;m upping my')
    body += '\n    ' + h
    js += f'tl.to("#{e2}",{{opacity:1,duration:.15,ease:"none"}},1.5);' + '\n      '
    h, e3 = el(sid, 'PDOOM2', f'top:470px;left:215px;font:400 150px/1 {SERIF};color:#FF3B20;'
               f'text-shadow:0 0 40px rgba(255,59,32,.45);opacity:0;', 'P(doom)')
    body += '\n    ' + h
    js += f'tl.to("#{e3}",{{opacity:1,duration:.2,ease:"none"}},2.1);' + '\n      '
    cv, cvid = canvas_el(sid, '无穷')
    body += '\n    ' + cv
    draw = '''
if(t>2.6){var k=Math.min(1,(t-2.6)/0.8);
  var fade=1-Math.min(1,Math.max(0,(t-4.05)/0.3));
  cx.save();cx.translate(1000,545);cx.scale(0.9,0.9);
  cx.strokeStyle='rgba(255,110,40,'+(0.95*fade)+')';cx.lineWidth=7;
  cx.shadowColor='rgba(255,110,40,.8)';cx.shadowBlur=22*fade;
  cx.beginPath();
  var i,n=70,drawn=Math.floor(n*k);
  for(i=0;i<=drawn;i++){var q=i/n;
    var a=q*Math.PI*2;
    var x=210*Math.cos(a)/(1+Math.sin(a)*Math.sin(a));
    var y=210*Math.sin(a)*Math.cos(a)/(1+Math.sin(a)*Math.sin(a));
    if(i==0)cx.moveTo(x,y);else cx.lineTo(x,y);}
  cx.stroke();cx.shadowBlur=0;cx.restore();}
'''
    js += canvas_js(cvid, dur, draw) + '\n      '
    h, e4 = el(sid, 'NAN', f'top:470px;left:900px;font:400 150px/1 {SERIF};color:{WHITE};opacity:0;', 'NaN')
    body += '\n    ' + h
    js += (f'gsap.set("#{e4}",{{opacity:0,x:-6}});'
           f'tl.to("#{e4}",{{opacity:1,x:0,duration:.09,ease:"none"}},4.05);'
           f'tl.to("#{e4}",{{x:6,opacity:.4,duration:.05,ease:"none"}},4.18);'
           f'tl.to("#{e4}",{{x:0,opacity:1,duration:.05,ease:"none"}},4.23);') + '\n      '
    h, e5 = el(sid, '注', f'top:680px;left:220px;font:14px/1.6 {MONO};color:#57534B;opacity:0;',
               '&sup1; estimate no longer defined<br>(1) upper bound exceeded')
    body += '\n    ' + h
    js += f'tl.to("#{e5}",{{opacity:1,duration:.3,ease:"none"}},4.4);' + '\n      '
    h, e6 = el(sid, '式号', f'top:505px;left:1420px;font:15px/1 {MONO};color:#57534B;opacity:0;', '(1)')
    body += '\n    ' + h
    js += f'tl.to("#{e6}",{{opacity:1,duration:.2,ease:"none"}},3.4);' + '\n      '
    return body, js


def s42(sid, d0, d1):
    dur = d1 - d0
    body, js = '', ''
    h, b0 = el(sid, '底', 'inset:0;background:#020202;')
    body += h
    h, dt = el(sid, '光点', 'top:470px;left:948px;width:24px;height:24px;border-radius:50%;'
               'background:#FFDDB8;box-shadow:0 0 60px 24px rgba(255,170,90,.55);opacity:0;')
    body += '\n    ' + h
    js += f'tl.to("#{dt}",{{opacity:1,duration:.5,ease:"sine.out"}},.1);' + '\n      '
    h, btn = el(sid, '按钮', 'top:540px;left:810px;padding:20px 46px;border:1px solid rgba(255,255,255,.4);'
                f'border-radius:10px;background:rgba(14,12,11,.8);color:{TERM};'
                'font:500 30px/1 %s;letter-spacing:1px;opacity:0;' % SANS, '&#8635; Regenerate')
    body += '\n    ' + h
    js += f'tl.to("#{btn}",{{opacity:1,duration:.4,ease:"none"}},.7);' + '\n      '
    h, nt = el(sid, '注', f'top:660px;left:0;width:{W}px;text-align:center;font:15px/1 {MONO};color:#4A463F;opacity:0;',
               'this response could not be verified')
    body += '\n    ' + h
    js += f'tl.to("#{nt}",{{opacity:1,duration:.4,ease:"none"}},1.1);' + '\n      '
    body += '\n    ' + hud_brackets(sid) + '\n    ' + vign(sid)
    js += f'tl.to("#{btn}",{{opacity:.55,duration:.1,ease:"none"}},1.9);tl.to("#{btn}",{{opacity:1,duration:.1,ease:"none"}},2.0);'
    return body, js


# ── 帧文件 / 工程装配 ───────────────────────────────────────

def wrap(sid, dur, body_html, body_js):
    css = f"""
#root{{position:relative;width:{W}px;height:{H}px;overflow:hidden;background:transparent;
  font-family:{SANS};color:{WHITE};}}
.a-el{{position:absolute;}}
"""
    return f"""<template>
  <style>{css}</style>
  <div data-composition-id="{sid}" data-width="{W}" data-height="{H}">
    <div id="root">
      <div id="{sid}-track" class="clip" data-start="0" data-duration="{dur:.3f}" data-track-index="0" data-hf-name="主轨">{body_html}
      </div>
    </div>
  </div>
  <script>
    (function() {{
      var tl = gsap.timeline({{ paused: true }});
      {body_js}
      var pad = {{ v: 0 }};
      tl.to(pad, {{ v: 1, duration: 0.02 }}, {max(0, dur - 0.05):.3f});
      window.__timelines["{sid}"] = tl;
    }})();
  </script>
</template>"""


def project_html(total_dur, scenes):
    scene_divs = '\n'.join(
        f'      <div id="el-{sid}" class="scene" data-composition-id="{sid}"\n'
        f'        data-composition-src="compositions/frames/{sid}.html"\n'
        f'        data-start="{st:.3f}" data-duration="{du:.3f}" data-track-index="{i}"></div>'
        for i, (sid, st, du) in enumerate(scenes))
    return f"""<!doctype html>
<html lang="zh-CN">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width={W}, height={H}" />
    <script src="assets/gsap.min.js"></script>
    <style>
      * {{ margin: 0; padding: 0; box-sizing: border-box; }}
      html, body {{ width: {W}px; height: {H}px; overflow: hidden; background: #000; }}
      #root {{ position: relative; width: {W}px; height: {H}px; overflow: hidden; background: #000; }}
      .scene {{ position: absolute; inset: 0; width: 100%; height: 100%; }}
    </style>
  </head>
  <body>
    <div id="root" data-composition-id="main" data-start="0" data-duration="{total_dur:.3f}" data-width="{W}" data-height="{H}">
{scene_divs}
    </div>
    <script>
      window.__timelines = window.__timelines || {{}};
      window.__timelines["main"] = gsap.timeline({{ paused: true }});
      (function () {{ var tl = window.__timelines["main"];
        tl.to({{}}, {{ duration: {total_dur:.3f} }}, 0);
      }})();
    </script>
  </body>
</html>
"""


def main(proj_dir: str) -> int:
    proj = pathlib.Path(proj_dir)
    shots = json.load(open(HERE / 'shots.json', encoding='utf-8'))['shots']
    (proj / 'compositions' / 'frames').mkdir(parents=True, exist_ok=True)
    (proj / '.hf-seg').mkdir(exist_ok=True)
    (proj / 'renders' / 'segs').mkdir(parents=True, exist_ok=True)
    (proj / 'assets').mkdir(exist_ok=True)
    shutil.copy(AI_DIR / 'assets' / 'gsap.min.js', proj / 'assets' / 'gsap.min.js')
    if not (proj / 'package.json').exists():
        (proj / 'package.json').write_text(json.dumps(
            {'name': 'agi-mv-repro', 'private': True, 'type': 'module'}, indent=2), encoding='utf-8')
    (proj / 'meta.json').write_text(json.dumps(
        {'id': 'agi-mv-repro', 'name': 'AI-doom 歌词 MV 复刻'}, ensure_ascii=False), encoding='utf-8')

    scenes = []
    for sh in shots:
        sid, d0, d1 = sh['id'], sh['start'], sh['end']
        dur = round(d1 - d0, 3)
        scenes.append((sid, d0, dur))
        _seq[sid] = 0
        b = BUILDERS[sid]
        body, js = b(sid, d0, d1)
        frame = wrap(sid, dur, body, js)
        (proj / 'compositions' / 'frames' / f'{sid}.html').write_text(frame, encoding='utf-8')
        seg_html = project_html(dur, [(sid, 0.0, dur)])
        (proj / '.hf-seg' / f'{sid}.html').write_text(seg_html, encoding='utf-8')
        print(f'{sid}: {dur:.1f}s ✓ ({sh["title"][:24]})')

    total = sum(s[2] for s in scenes)
    (proj / 'index.html').write_text(project_html(total, scenes), encoding='utf-8')
    (proj / 'renders' / 'segs' / 'list.txt').write_text(
        '\n'.join(f"file '{sid}.mp4'" for sid, _, _ in scenes) + '\n', encoding='utf-8')
    print(f'index.html ✓ 总时长 {total:.1f}s，{len(scenes)} 段')
    return 0


BUILDERS = {s.__name__: s for s in (
    s01, s02, s03, s04, s05, s06, s07, s08, s09, s10, s11, s12, s13,
    s14, s15, s16, s17, s18, s19, s20, s21, s22, s23, s24, s25, s26,
    s27, s28, s29, s30, s31, s32, s33, s34, s35, s36, s37, s38, s39,
    s40, s41, s42)}

if __name__ == '__main__':
    sys.exit(main(sys.argv[1]))
