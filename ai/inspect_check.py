# -*- coding: utf-8 -*-
"""检视↔成片一致性对比：每段取若干时刻，检视壳（compositions/frames 片段 +
LiveFrame 同款激活壳）截图 vs main.mp4 同时刻抽帧，并排出像素差异。

对比口径：壳 = web/src/components/LiveFrame.vue shellDoc 的离线复刻
（base=项目目录、gsap/katex 项目本地、clip 强制 absolute、等比缩放视口）。
差异来源分层：
  - 工程层（overlay 角标/黑场转场/@font-face）只在渲染工程 index/.hf-seg 侧 → 预期差异
  - 合成层差异（元素缺失/错位/样式不同）→ 检视保真缺陷

用法：python ai/inspect_check.py <项目目录> [--times 1.0,mid,end-0.8] [--segs seg01,seg02]
产物：<项目目录>/.inspect-check/{inspect-*,video-*,sbs-*.png} + summary.json
"""
import argparse
import json
import pathlib
import re
import subprocess
import sys
import tempfile

AI_DIR = pathlib.Path(__file__).resolve().parent
BROWSERS = [
    r'C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe',
    r'C:\Program Files\Microsoft\Edge\Application\msedge.exe',
    r'C:\Program Files\Google\Chrome\Application\chrome.exe',
]
SHOT_W, SHOT_H = 540, 960  # 9:16 预览视口（comp 1080x1920 等比缩放）


def jload(p):
    try:
        return json.load(open(p, encoding='utf-8'))
    except Exception:
        return None


def ffpath():
    for c in ('ffmpeg', r'..\ffmpeg\ffmpeg.exe'):
        try:
            subprocess.run([c, '-version'], capture_output=True, check=True)
            return c
        except Exception:
            continue
    raise SystemExit('ffmpeg 不可用')


def seg_table(proj):
    """段表：优先 renders/timeline.json（拼接实测边界）；否则 ffprobe 段片时长累计
    （段片按帧量化，裸「配音+0.35」累计逐段漂移，长片可差 0.2s+）。"""
    sb = jload(proj / 'storyboards' / 'storyboard.json') or {}
    tl = jload(proj / 'renders' / 'timeline.json')
    ids = [s['id'] for s in sb.get('segments', [])]
    if tl and isinstance(tl, list) and len(tl) == len(ids):
        return [{'id': i, 'start': e['start'], 'end': e['end'],
                 'dur': e['end'] - e['start']} for i, e in zip(ids, tl)]
    acc, out = 0.0, []
    for i in ids:
        r = subprocess.run(['ffprobe', '-v', 'error', '-show_entries', 'format=duration',
                            '-of', 'csv=p=0', str(proj / 'renders' / 'segs' / f'{i}.mp4')],
                           capture_output=True, text=True)
        d = float(r.stdout.strip()) if r.stdout.strip() else 3.0
        out.append({'id': i, 'start': round(acc, 3), 'end': round(acc + d, 3), 'dur': round(d, 3)})
        acc += d
    return out


def shell_spec(proj):
    """转场模式 + 后期参数，与 Go 侧 transitionFor/postFor 同优先级：
    project.json > 风格 tokens > 缺省（fade / 轻后期）。"""
    pj = jload(proj / 'project.json') or {}
    sb = jload(proj / 'storyboards' / 'storyboard.json') or {}
    style_id = str(sb.get('style_id') or '')
    tokens = {}
    if style_id:
        for e in jload(proj.parent.parent / 'library' / 'registry' / 'styles.json') or []:
            if str(e.get('id', '')).lower() == style_id.lower():
                tokens = e.get('tokens') or {}
                break
    trans = str(pj.get('transition') or tokens.get('transition') or 'fade').strip().lower()
    post = pj.get('post')
    if post is None:
        post = tokens.get('post')
    if post is None:
        post = {'vignette': 0.35, 'grain': 0.4}
    if post in (False, 'false', 'null'):
        post = {}
    return trans, post


def shell_html(proj, seg_id, t):
    """LiveFrame shellDoc 的离线复刻：片段 + 激活脚本，直接 seek 到 t。"""
    trans, post = shell_spec(proj)
    frag = (proj / 'compositions' / 'frames' / f'{seg_id}.html').read_text(encoding='utf-8')
    katex = ''
    if 'katex' in frag and (proj / 'assets' / 'katex' / 'katex.min.js').exists():
        katex = ('<link rel="stylesheet" href="assets/katex/katex.min.css" />'
                 '<script src="assets/katex/katex.min.js"></script>')
    # 壳 CSS 与 shellDoc 逐条对齐（clip 强制 absolute / viewport 居中 / 高亮框不进离线壳）
    css = ('html,body{margin:0;width:100%;height:100%;background:#000;overflow:hidden}'
           '[data-start][data-duration]{position:absolute!important;}'
           '#__viewport{position:absolute;inset:0;display:flex;align-items:center;justify-content:center}'
           '#__stage{position:relative;transform-origin:center center}')
    agent = f'''(function(){{
var dbg=document.createElement("div");
dbg.style.cssText="position:fixed;left:0;top:0;z-index:99999;background:#900;color:#fff;font:14px monospace;padding:2px 6px;max-width:100%;white-space:pre-wrap";
document.body.appendChild(dbg);
function bug(m){{dbg.textContent+=m+"\\n"}}
window.onerror=function(m,s,l){{bug("ERR@"+l+": "+m)}};
window.__timelines=window.__timelines||{{}};
var tpl=document.querySelector("template");
if(!tpl){{bug("ERR 帧缺template");return}}
var stage=document.getElementById("__stage");
Array.prototype.slice.call(tpl.content.childNodes).forEach(function(n){{
if(n.nodeType!==1)return;
if(n.tagName==="SCRIPT"){{var s=document.createElement("script");s.textContent=n.textContent;document.body.appendChild(s)}}
else if(n.tagName==="STYLE"){{document.head.appendChild(n)}}
else{{stage.appendChild(n)}}
}});
var tl=window.__timelines[{json.dumps(seg_id)}];
if(!tl){{bug("ERR 时间轴未注册");return}}
tl.pause({t!r});
var comp=stage.querySelector("[data-composition-id]");
var W=parseFloat(comp.getAttribute("data-width"))||1080;
var H=parseFloat(comp.getAttribute("data-height"))||1920;
comp.style.display="block";comp.style.width=W+"px";comp.style.height=H+"px";
stage.style.width=W+"px";stage.style.height=H+"px";
var k=Math.min({SHOT_W}/W,{SHOT_H}/H);stage.style.transform="scale("+k+")";
var LAY={{fade:{json.dumps(trans == 'fade')},vg:{post.get('vignette') or 0},gr:{post.get('grain') or 0}}};
var ov="position:absolute;left:0;top:0;width:"+W+"px;height:"+H+"px;pointer-events:none;";
if(LAY.fade){{
var xf=document.createElement("div");
xf.style.cssText=ov+"background:#000;z-index:100;opacity:1;";stage.appendChild(xf);
tl.fromTo(xf,{{opacity:1}},{{opacity:0,duration:.25,ease:"none",immediateRender:false}},0);
tl.to(xf,{{opacity:1,duration:.25,ease:"none"}},Math.max(0,tl.duration()-0.25));
}}
if(LAY.vg>0){{
var s=Math.min(Math.max(LAY.vg,0),0.92),th=Math.min(Math.PI/(5.5-2.5*s),Math.PI/3.2);
var R=Math.max(W,H)/2,qs=[0,.25,.5,.75,1,Math.sqrt(W*W+H*H)/(2*R)],st=[];
for(var i=0;i<qs.length;i++){{var q=qs[i];
st.push("rgba(0,0,0,"+Math.max(0,1-Math.pow(Math.cos(th*q),3.6)).toFixed(3)+") "+(q*100).toFixed(1)+"%")}}
var vg=document.createElement("div");
vg.style.cssText=ov+"z-index:200;background:radial-gradient(circle "+R.toFixed(0)+"px at 50% 50%,"+st.join(",")+");";
stage.appendChild(vg);
}}
if(LAY.gr>0){{
var NS="http://www.w3.org/2000/svg",sv=document.createElementNS(NS,"svg"),fl=document.createElementNS(NS,"filter");
fl.setAttribute("id","n");
var tu=document.createElementNS(NS,"feTurbulence");
tu.setAttribute("type","fractalNoise");tu.setAttribute("baseFrequency","0.8");
tu.setAttribute("numOctaves","2");tu.setAttribute("stitchTiles","stitch");
var cm=document.createElementNS(NS,"feColorMatrix");
cm.setAttribute("type","saturate");cm.setAttribute("values","0");
fl.appendChild(tu);fl.appendChild(cm);
var rc=document.createElementNS(NS,"rect");
rc.setAttribute("width","180");rc.setAttribute("height","180");rc.setAttribute("filter","url(#n)");
sv.setAttribute("width","180");sv.setAttribute("height","180");sv.appendChild(fl);sv.appendChild(rc);
var gn=document.createElement("div");
gn.style.cssText=ov+"z-index:201;opacity:"+(0.12*LAY.gr).toFixed(3)+";";
gn.style.backgroundImage="url(data:image/svg+xml,"+encodeURIComponent(new XMLSerializer().serializeToString(sv))+")";
stage.appendChild(gn);
}}
bug("OK t="+tl.time()+" dur="+tl.duration()+" k="+k+" fade="+LAY.fade+" vg="+LAY.vg);
tl.pause(0);tl.pause({t!r}); // 同刻 pause 会被 gsap 短路：0→t 两跳强制重渲（含后挂的黑场 tween）
var xfd=document.getElementById("__xfade");
if(xfd)bug("xf opacity="+xfd.style.opacity);
}})();'''
    return ('<!doctype html><html><head><meta charset="utf-8">' + katex +
            '<script src="assets/gsap.min.js"></script><style>' + css +
            '</style></head><body><div id="__viewport"><div id="__stage"></div></div>' +
            frag + '<script>' + agent + '</script></body></html>')


def shot(browser, html_path, out_png, ud):
    subprocess.run(
        [browser, '--headless', '--disable-gpu', '--no-first-run', f'--user-data-dir={ud}',
         '--hide-scrollbars', f'--screenshot={out_png.resolve()}',
         f'--window-size={SHOT_W},{SHOT_H}', '--virtual-time-budget=4000',
         html_path.resolve().as_uri()], check=True, timeout=40, capture_output=True)


def video_frame(ff, mp4, g_t, out_png):
    subprocess.run([ff, '-y', '-v', 'error', '-ss', f'{g_t:.3f}', '-i', str(mp4),
                    '-frames:v', '1', '-vf', f'scale={SHOT_W}:{SHOT_H}', str(out_png)],
                   check=True, timeout=30, capture_output=True)


def diff_stats(a_png, b_png):
    from PIL import Image, ImageChops
    a, b = Image.open(a_png).convert('RGB'), Image.open(b_png).convert('RGB')
    if a.size != b.size:
        b = b.resize(a.size)
    d = ImageChops.difference(a, b)
    h = d.convert('L').histogram()
    total = sum(h)
    n_changed = sum(h[8:])  # 亮度差 >8/255 计为变化像素
    big = sum(h[40:])
    return {'changed_pct': round(100 * n_changed / total, 2),
            'big_pct': round(100 * big / total, 2)}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('proj')
    ap.add_argument('--times', default='1.0,mid,end-0.8')
    ap.add_argument('--segs', default='')
    ap.add_argument('--mp4', default='renders/main.mp4')  # main_voice.mp4 = 无后期母版
    args = ap.parse_args()
    proj = pathlib.Path(args.proj)
    mp4 = proj / args.mp4
    if not mp4.exists():
        raise SystemExit('renders/main.mp4 不存在')
    browser = next((p for p in BROWSERS if pathlib.Path(p).exists()), None)
    if not browser:
        raise SystemExit('未找到 Edge/Chrome')
    ff = ffpath()
    table = seg_table(proj)
    if args.segs:
        keep = set(args.segs.split(','))
        table = [s for s in table if s['id'] in keep]
    outdir = proj / '.inspect-check'
    outdir.mkdir(exist_ok=True)
    summary = []
    with tempfile.TemporaryDirectory(prefix='hf-inspect-') as udtmp:
        for s in table:
            for spec in args.times.split(','):
                spec = spec.strip()
                if spec == 'mid':
                    t = s['dur'] / 2
                elif spec.startswith('end-'):
                    t = s['dur'] - float(spec[4:])
                else:
                    t = float(spec)
                t = max(0.0, min(t, s['dur']))
                tag = f"{s['id']}-{t:.2f}"
                ip = outdir / f'inspect-{tag}.png'
                vp = outdir / f'video-{tag}.png'
                # 壳必须写在项目根：相对路径 assets/gsap、assets/img/… 才与渲染工程同解
                hp = proj / '.inspect-shell.html'
                hp.write_text(shell_html(proj, s['id'], t), encoding='utf-8')
                try:
                    shot(browser, hp, ip, udtmp)
                    video_frame(ff, mp4, s['start'] + t, vp)
                except subprocess.CalledProcessError as e:
                    summary.append({'seg': s['id'], 't': round(t, 2), 'error': str(e)[:200]})
                    continue
                finally:
                    hp.unlink(missing_ok=True)
                st = diff_stats(ip, vp)
                # 并排长图：左检视右成片
                from PIL import Image
                a, b = Image.open(ip), Image.open(vp)
                sbs = Image.new('RGB', (a.width * 2 + 8, max(a.height, b.height)), (30, 30, 34))
                sbs.paste(a, (0, 0)); sbs.paste(b, (a.width + 8, 0))
                sbs.save(outdir / f'sbs-{tag}.png')
                summary.append({'seg': s['id'], 't': round(t, 2),
                                'global_t': round(s['start'] + t, 2), **st})
    (outdir / 'summary.json').write_text(json.dumps(summary, ensure_ascii=False, indent=1),
                                         encoding='utf-8')
    for r in summary:
        if 'error' in r:
            print(f"{r['seg']} t={r['t']} ERROR {r['error'][:80]}")
        else:
            flag = '  <<< 差异大' if r['big_pct'] > 5 else ''
            print(f"{r['seg']} t={r['t']:>5} (全局 {r['global_t']:>7}) 变化像素 {r['changed_pct']:>6}% 显著 {r['big_pct']:>6}%{flag}")
    print(f"\n产物在 {outdir}")


if __name__ == '__main__':
    sys.exit(main())
