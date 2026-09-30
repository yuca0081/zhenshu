<script setup lang="ts">
// 修改态活合成物（plan §4.3 预览双模）：
// 把磁盘上的单段帧 HTML（<template> 片段，惰性）包进一个可执行文档（srcdoc 壳），
// 壳内引项目本地 gsap（与渲染引擎同一份）+ 激活/交互脚本 —— 时间轴 seek、悬停高亮、点选命名元素。
// sandbox="allow-scripts"（无同源）：父子只走 postMessage，协议 live:ready / live:seek / live:pick / live:error。
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { api, assetURL } from '../api'

const props = defineProps<{
  videoId: string
  segId: string // 'seg03'
  localTime: number // 段内时刻（秒）
  rev?: number // 重做完成后 bump，强制重载帧
  transition?: string // 成片转场模式（projectView）：fade=复刻页内黑场
  post?: { vignette?: number; grain?: number; chroma?: number } | null // stitch 后期妆（与 main.mp4 同源）
}>()
const emit = defineEmits<{
  ready: [segId: string, duration: number]
  pick: [p: { segId: string; id: string; name: string; t: number }]
  lasso: [p: { segId: string; t: number; picks: { id: string; name: string }[] }]
}>()

const iframeEl = ref<HTMLIFrameElement | null>(null)
const srcdoc = ref('')
const errText = ref('')
const readySeg = ref('')
const lasso = ref(false) // 圈选模式（父层按钮开关，壳内拖框 → live:lasso 上报）

let pendingT: number | null = null
let raf = 0

function postSeek(t: number) {
  pendingT = t
  if (!raf) raf = requestAnimationFrame(flush)
}
function postLassoMode() {
  if (readySeg.value && iframeEl.value?.contentWindow) {
    iframeEl.value.contentWindow.postMessage({ type: 'live:lasso-on', on: lasso.value }, '*')
  }
}
watch(lasso, postLassoMode)
function flush() {
  raf = 0
  if (pendingT == null) return
  if (readySeg.value !== props.segId || !iframeEl.value?.contentWindow) return // 未 ready：挂起，ready 后应用
  iframeEl.value.contentWindow.postMessage({ type: 'live:seek', t: pendingT }, '*')
  pendingT = null
}

async function load() {
  readySeg.value = ''
  errText.value = ''
  srcdoc.value = ''
  const frag = await api
    .frameText(props.videoId, props.segId)
    .catch(() => null)
  if (frag == null) {
    errText.value = `段 ${props.segId} 合成物加载失败`
    return
  }
  srcdoc.value = shellDoc(frag)
}

// srcdoc 壳：base 指回项目资产接口 + gsap 本地引 + 帧片段原样 + 激活/交互脚本（ES5 风格，避免依赖构建期语法）
function shellDoc(frag: string): string {
  const gsap = assetURL(props.videoId, 'gsap.min.js')
  // 帧内图片是渲染工程相对路径（assets/img/…）：srcdoc 的 base 继承父页面 origin
  // 会全部 404（/assets 已被前端静态托管占用），指到 /api/projects/:id/ 才与成片同源
  const base = '/api/projects/' + props.videoId + '/'
  // formula 段的 katex.render 由渲染工程 index.html 头供给，壳里按需自带
  const katex = frag.includes('katex')
    ? '<link rel="stylesheet" href="' + assetURL(props.videoId, 'katex/katex.min.css') + '">' +
      '<script src="' + assetURL(props.videoId, 'katex/katex.min.js') + '"><\/script>'
    : ''
  return (
    '<!doctype html><html><head><meta charset="utf-8">' +
    '<base href="' + base + '">' + katex +
    '<script src="' + gsap + '"><\/script>' +
    '<style>' +
    'html,body{margin:0;width:100%;height:100%;background:#000;overflow:hidden}' +
    // 引擎同款 clip 语义：带 data-start/duration 的是绝对定位图层。皮肤自由 CSS 里的
    // position:relative 会按源序盖掉 .s-clip 的 absolute，纸底塌成零高——渲染引擎激活时强制，壳需复刻
    '[data-start][data-duration]{position:absolute!important;}' +
    '#__viewport{position:absolute;inset:0;display:flex;align-items:center;justify-content:center}' +
    '#__stage{position:relative;transform-origin:center center}' +
    '[data-hf-hover]{outline:2px solid #f0c674!important;outline-offset:2px;cursor:pointer}' +
    '#__lasso{position:fixed;z-index:9998;display:none;border:1.5px dashed #f0c674;' +
    'background:rgba(240,198,116,.12);pointer-events:none}' +
    '#__badge{position:fixed;z-index:9999;display:none;pointer-events:none;white-space:nowrap;' +
    'background:#f0c674;color:#14141a;font:12px/1.4 system-ui,sans-serif;padding:3px 8px;border-radius:4px}' +
    '<\/style></head><body>' +
    '<div id="__viewport"><div id="__stage"></div></div>' +
    '<div id="__badge"></div>' +
    frag +
    '<script>' + agentScript(props.segId, props.transition ?? 'fade', props.post ?? null) + '<\/script>' +
    '</body></html>'
  )
}

// 成片观感复刻（与 stitch 同源口径，源自 pipeline.go videoChain / assemble xfade）：
// fade 转场 = 段首尾 0.25s 黑场微叠化（挂段时间轴，seek 随动）；post.vignette =
// ffmpeg vignette 的 CSS 径向渐变近似（增益 cos(θ·q)^3.6，R=max(W,H)/2）；post.grain =
// 静态噪点纹理（成片为时变颗粒，检视给静态质感提示）。chroma(rgbashift) 与
// xfade（concat 侧交叉叠化，段内不存在）不复刻。
function agentScript(seg: string, transition: string, post: { vignette?: number; grain?: number; chroma?: number } | null): string {
  return (
    '(function(){' +
    'var SEG=' + JSON.stringify(seg) + ';' +
    'function post(m){parent.postMessage(m,"*")}' +
    // ① 激活：__timelines 先于片段脚本初始化（index.html 同款一行）；template 内容搬家，script 重建执行
    'window.__timelines=window.__timelines||{};' +
    'var tpl=document.querySelector("template");' +
    'if(!tpl){post({type:"live:error",seg:SEG,error:"帧缺 template"});return}' +
    'var stage=document.getElementById("__stage");' +
    'Array.prototype.slice.call(tpl.content.childNodes).forEach(function(n){' +
    'if(n.nodeType!==1)return;' +
    'if(n.tagName==="SCRIPT"){var s=document.createElement("script");s.textContent=n.textContent;document.body.appendChild(s)}' +
    'else if(n.tagName==="STYLE"){document.head.appendChild(n)}' +
    'else{stage.appendChild(n)}' +
    '});' +
    'var tl=window.__timelines[SEG];' +
    'if(!tl){post({type:"live:error",seg:SEG,error:"时间轴未注册"});return}' +
    'tl.pause(0);' +
    // ② 缩放适配：合成物根 data-width/height → 等比放进 iframe 视口
    'var comp=stage.querySelector("[data-composition-id]");' +
    'var W=parseFloat(comp.getAttribute("data-width"))||1920;' +
    'var H=parseFloat(comp.getAttribute("data-height"))||1080;' +
    'comp.style.display="block";comp.style.width=W+"px";comp.style.height=H+"px";' +
    'stage.style.width=W+"px";stage.style.height=H+"px";' +
    'function fit(){var k=Math.min(innerWidth/W,innerHeight/H);stage.style.transform="scale("+k+")"}' +
    'addEventListener("resize",fit);fit();' +
    // ⑥ 成片观感层：黑场转场 + 后期妆。盖在合成物上方，无 data-hf-name、pointer-events:none
    // —— 不参与悬停/点选/圈选，纯观感（与 main.mp4 的 stitch/assemble 同源口径）
    'var LAY=' + JSON.stringify({ fade: transition === 'fade', vg: post?.vignette ?? 0, gr: post?.grain ?? 0 }) + ';' +
    '(function(){' +
    'var ov="position:absolute;left:0;top:0;width:"+W+"px;height:"+H+"px;pointer-events:none;";' +
    'if(LAY.fade){var xf=document.createElement("div");' +
    // immediateRender:false：激活时已 pause(0)，若让 fromTo 立即渲染 from 态，
    // 等时刻的后续 seek 会被 gsap 短路、黑场永远停在 opacity:1（段首近黑假象）。
    // CSS opacity:1 即 t=0 态；进入检视时刻恰为 0 时无需重渲也正确。
    'xf.style.cssText=ov+"background:#000;z-index:100;opacity:1;";stage.appendChild(xf);' +
    'tl.fromTo(xf,{opacity:1},{opacity:0,duration:.25,ease:"none",immediateRender:false},0);' +
    'tl.to(xf,{opacity:1,duration:.25,ease:"none"},Math.max(0,tl.duration()-0.25));}' +
    'if(LAY.vg>0){var s=Math.min(Math.max(LAY.vg,0),0.92);' +
    'var th=Math.min(Math.PI/(5.5-2.5*s),Math.PI/3.2);' +
    'var R=Math.max(W,H)/2,qs=[0,.25,.5,.75,1,Math.sqrt(W*W+H*H)/(2*R)],st=[];' +
    'for(var i=0;i<qs.length;i++){var q=qs[i];' +
    'st.push("rgba(0,0,0,"+Math.max(0,1-Math.pow(Math.cos(th*q),3.6)).toFixed(3)+") "+(q*100).toFixed(1)+"%")}' +
    'var vg=document.createElement("div");' +
    'vg.style.cssText=ov+"z-index:200;background:radial-gradient(circle "+R.toFixed(0)+"px at 50% 50%,"+st.join(",")+");";' +
    'stage.appendChild(vg);}' +
    'if(LAY.gr>0){var NS="http://www.w3.org/2000/svg";' +
    'var sv=document.createElementNS(NS,"svg"),fl=document.createElementNS(NS,"filter");' +
    'fl.setAttribute("id","n");' +
    'var tu=document.createElementNS(NS,"feTurbulence");' +
    'tu.setAttribute("type","fractalNoise");tu.setAttribute("baseFrequency","0.8");' +
    'tu.setAttribute("numOctaves","2");tu.setAttribute("stitchTiles","stitch");' +
    'var cm=document.createElementNS(NS,"feColorMatrix");' +
    'cm.setAttribute("type","saturate");cm.setAttribute("values","0");' +
    'fl.appendChild(tu);fl.appendChild(cm);' +
    'var rc=document.createElementNS(NS,"rect");' +
    'rc.setAttribute("width","180");rc.setAttribute("height","180");rc.setAttribute("filter","url(#n)");' +
    'sv.setAttribute("width","180");sv.setAttribute("height","180");sv.appendChild(fl);sv.appendChild(rc);' +
    'var gn=document.createElement("div");' +
    'gn.style.cssText=ov+"z-index:201;opacity:"+(0.12*LAY.gr).toFixed(3)+";";' +
    'gn.style.backgroundImage="url(data:image/svg+xml,"+encodeURIComponent(new XMLSerializer().serializeToString(sv))+")";' +
    'stage.appendChild(gn)}})();' +
    // ③ 悬停高亮 + 人话名徽标；点选上报（沿祖先找最近的 data-hf-name）
    'var badge=document.getElementById("__badge"),hovered=null;' +
    'function hitOf(el){for(var n=el;n&&n!==document;n=n.parentElement){' +
    'if(n.getAttribute&&n.getAttribute("data-hf-name"))return{el:n,name:n.getAttribute("data-hf-name")}}return null}' +
    'document.addEventListener("mousemove",function(e){' +
    'var h=hitOf(e.target);' +
    'if(h&&h.el!==hovered){if(hovered)hovered.removeAttribute("data-hf-hover");' +
    'hovered=h.el;hovered.setAttribute("data-hf-hover","1");' +
    'badge.textContent=h.name;badge.style.display="block"}' +
    'else if(!h&&hovered){hovered.removeAttribute("data-hf-hover");hovered=null;badge.style.display="none"}' +
    'if(hovered){badge.style.left=Math.min(e.clientX+12,innerWidth-badge.offsetWidth-8)+"px";' +
    'badge.style.top=Math.max(8,e.clientY-28)+"px"}});' +
    'document.addEventListener("click",function(e){' +
    'if(lassoOn)return;' +
    'var h=hitOf(e.target);if(!h)return;' +
    'e.preventDefault();e.stopPropagation();' +
    'post({type:"live:pick",seg:SEG,id:h.el.id||"",name:h.name,t:tl.time()})});' +
    // ⑤ 圈选：拖矩形，命中所有相交的命名元素（前端几何，plan §3.5/§10 圈选位置定的答案）
    'var lassoOn=false,dragging=false,sx=0,sy=0,box=document.getElementById("__lasso");' +
    'function place(x,y,w,h){box.style.left=x+"px";box.style.top=y+"px";box.style.width=w+"px";box.style.height=h+"px"}' +
    'function rectOf(e){var x0=Math.min(sx,e.clientX),y0=Math.min(sy,e.clientY);' +
    'return {x0:x0,y0:y0,x1:Math.max(sx,e.clientX),y1:Math.max(sy,e.clientY)}}' +
    'function matches(r){var out=[],els=stage.querySelectorAll("[data-hf-name]");' +
    'for(var i=0;i<els.length;i++){var el=els[i],b=el.getBoundingClientRect();' +
    'if(b.width<1||b.height<1)continue;' +
    'if(b.right>r.x0&&b.left<r.x1&&b.bottom>r.y0&&b.top<r.y1)' +
    'out.push({el:el,id:el.id||"",name:el.getAttribute("data-hf-name")})}return out}' +
    'function mark(ms){var ol=stage.querySelectorAll("[data-hf-hover]");' +
    'for(var i=0;i<ol.length;i++)ol[i].removeAttribute("data-hf-hover");' +
    'for(var j=0;j<ms.length;j++)ms[j].el.setAttribute("data-hf-hover","1")}' +
    'document.addEventListener("mousedown",function(e){' +
    'if(!lassoOn||e.button!==0)return;e.preventDefault();dragging=true;sx=e.clientX;sy=e.clientY;' +
    'box.style.display="block";place(sx,sy,0,0)});' +
    'document.addEventListener("mousemove",function(e){' +
    'if(!dragging)return;var r=rectOf(e);place(r.x0,r.y0,r.x1-r.x0,r.y1-r.y0);mark(matches(r))});' +
    'document.addEventListener("mouseup",function(e){' +
    'if(!dragging)return;dragging=false;var r=rectOf(e),ms=matches(r);' +
    'box.style.display="none";mark([]);' +
    'if(ms.length)post({type:"live:lasso",seg:SEG,t:tl.time(),' +
    'picks:ms.map(function(m){return {id:m.id,name:m.name}})});' +
    'else if(Math.abs(e.clientX-sx)<4&&Math.abs(e.clientY-sy)<4){lassoOn=false;' +
    'parent.postMessage({type:"live:lasso-off"},"*")}});' +
    // ④ seek 协议（rAF 合并拖动擦洗）+ 圈选开关
    'var pend=null,raf=0;' +
    'function flush(){raf=0;if(pend!=null){tl.pause(pend);pend=null}}' +
    'addEventListener("message",function(e){var d=e.data||{};' +
    'if(d.type==="live:seek"){pend=d.t;if(!raf)raf=requestAnimationFrame(flush)}' +
    'else if(d.type==="live:lasso-on"){lassoOn=!!d.on;' +
    'if(!lassoOn){dragging=false;box.style.display="none";mark([])}}});' +
    'post({type:"live:ready",seg:SEG,duration:tl.duration()});' +
    '})();'
  )
}

function onMessage(e: MessageEvent) {
  if (!iframeEl.value || e.source !== iframeEl.value.contentWindow) return
  const d = e.data as { type?: string; seg?: string; duration?: number; id?: string; name?: string; t?: number; error?: string; picks?: { id: string; name: string }[] }
  switch (d.type) {
    case 'live:ready':
      if (d.seg) readySeg.value = d.seg
      emit('ready', d.seg ?? props.segId, d.duration ?? 0)
      flush() // 应用挂起的 seek
      postLassoMode() // 重载后恢复圈选模式
      break
    case 'live:pick':
      emit('pick', { segId: d.seg ?? props.segId, id: d.id ?? '', name: d.name ?? '', t: d.t ?? 0 })
      break
    case 'live:lasso':
      emit('lasso', { segId: d.seg ?? props.segId, t: d.t ?? 0, picks: d.picks ?? [] })
      break
    case 'live:lasso-off':
      lasso.value = false // 壳内空拖取消：同步按钮态
      break
    case 'live:error':
      errText.value = d.error ?? '活合成物初始化失败'
      break
  }
}

// Esc 退出圈选（window 级监听只在圈选中挂）
function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') lasso.value = false
}

onMounted(() => {
  window.addEventListener('message', onMessage)
  window.addEventListener('keydown', onKey)
})
onBeforeUnmount(() => {
  window.removeEventListener('message', onMessage)
  window.removeEventListener('keydown', onKey)
  if (raf) cancelAnimationFrame(raf)
})

watch(() => [props.segId, props.rev, props.videoId], load, { immediate: true })
// immediate：挂载时就把初始时刻挂起（pendingT），ready 后 flush 应用——
// 否则壳停在 t=0（段首黑场转场时整框近黑，正是检视与视频对不上的观感）
watch(() => props.localTime, postSeek, { immediate: true })
</script>

<template>
  <div v-if="errText" class="live-err">{{ errText }}</div>
  <template v-else-if="srcdoc">
    <iframe
      ref="iframeEl"
      :key="`${segId}-${rev ?? 0}`"
      class="live-frame"
      :srcdoc="srcdoc"
      sandbox="allow-scripts"
    />
    <!-- 圈选开关：检视态常驻左上（绝对定位于最近定位祖先 .player），开启后壳内拖框 → 命中元素批量进引用 -->
    <button v-if="readySeg && !lasso" class="lasso-btn" @click="lasso = true">▭ 圈选元素</button>
    <div v-else-if="readySeg && lasso" class="lasso-tip">
      拖框圈住要改的元素（可一次多个）；空点或 Esc 退出
    </div>
  </template>
  <div v-else class="live-loading">活合成物载入中…</div>
</template>

<style scoped>
.live-frame {
  width: 100%;
  border: 0;
  border-radius: 8px;
  background: #000;
  display: block;
  aspect-ratio: 16 / 9;
}
.lasso-btn, .lasso-tip {
  position: absolute; left: 10px; top: 10px; z-index: 4;
  font-size: 12px; padding: 4px 12px; border-radius: 999px;
  border: 1px solid rgba(240, 198, 116, .45); background: rgba(16, 14, 8, .78); color: #f0c674;
  backdrop-filter: blur(3px);
}
.lasso-btn { cursor: pointer; }
.lasso-btn:hover, .lasso-tip { background: rgba(42, 36, 22, .92); }
.live-loading,
.live-err {
  width: 100%;
  aspect-ratio: 16 / 9;
  border-radius: 8px;
  background: #0a0a0e;
  color: #7c7c88;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 13px;
}
.live-err { color: #ff9d9d; }
</style>
