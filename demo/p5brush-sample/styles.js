// ============================================================
// 画风矩阵 v2 —— 按 meme67 (kit.js + vangogh.js + song.js) 的
// 分层架构重写：底漆层 → 笔触层×N → 做旧层 → 颗粒/暗角收尾。
// 画布 960×1080（p5 WEBGL 中心坐标），2D 图层为屏幕坐标。
// 纪律：无 Math.random；种子随机；流场用完关；wash 前 noStroke。
// ============================================================

function mulberry32(a) {
  return function () {
    a |= 0; a = (a + 0x6D2B79F5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ t >>> 14) >>> 0) / 4294967296;
  };
}
// ---------------- mini-kit（抄 kit.js 的可复用件） ----------------
const clamp01 = (x) => Math.min(1, Math.max(0, x));
const sm = (a, b, x) => { const t = clamp01((x - a) / (b - a)); return t * t * (3 - 2 * t); };
const lerp = (a, b, t) => a + (b - a) * t;
const hashN = (n) => { const s = Math.sin(n * 127.1 + 311.7) * 43758.5453; return s - Math.floor(s); };
function noise2(x, y) {
  const xi = Math.floor(x), yi = Math.floor(y), xf = x - xi, yf = y - yi;
  const u = xf * xf * (3 - 2 * xf), v = yf * yf * (3 - 2 * yf);
  const h = (a, b) => hashN(a * 57.31 + b * 113.77);
  return lerp(lerp(h(xi, yi), h(xi + 1, yi), u), lerp(h(xi, yi + 1), h(xi + 1, yi + 1), u), v);
}
function fbm(x, y) {
  return 0.55 * noise2(x, y) + 0.28 * noise2(x * 2.1, y * 2.1) + 0.17 * noise2(x * 4.3, y * 4.3);
}
const rgbC = (c) => [parseInt(c.slice(1, 3), 16), parseInt(c.slice(3, 5), 16), parseInt(c.slice(5, 7), 16)];
const hexC = (r, g, b) => '#' + [r, g, b].map(x => Math.round(Math.min(255, Math.max(0, x))).toString(16).padStart(2, '0')).join('');
const mix = (a, b, t) => { const A = rgbC(a), B = rgbC(b); return hexC(A[0] + (B[0] - A[0]) * t, A[1] + (B[1] - A[1]) * t, A[2] + (B[2] - A[2]) * t); };
const withAlpha = (c, a) => { const [r, g, b] = rgbC(c); return `rgba(${r},${g},${b},${a})`; };
function ramp(stops, v) {
  v = clamp01(v);
  for (let i = 1; i < stops.length; i++) if (v <= stops[i][0]) {
    const a = stops[i - 1], b = stops[i];
    return mix(a[1], b[1], (v - a[0]) / (b[0] - a[0] || 1));
  }
  return stops[stops.length - 1][1];
}
function pip(x, y, poly) {
  let inside = false;
  for (let i = 0, j = poly.length - 1; i < poly.length; j = i++) {
    const xi = poly[i][0], yi = poly[i][1], xj = poly[j][0], yj = poly[j][1];
    if (((yi > y) !== (yj > y)) && (x < (xj - xi) * (y - yi) / (yj - yi) + xi)) inside = !inside;
  }
  return inside;
}

// 2D 全帧图层：与 kit.js V.with2d 同构（先 flush 排队笔触再合成）
let _L2 = null;
function flushBrush() {
  brush.set('pen', '#faf7f0', 0.02);
  brush.line(-300, -300, -299, -300);
}
function with2d(fn, o = {}) {
  flushBrush();
  if (!_L2) { _L2 = createGraphics(960, 1080); _L2.pixelDensity(1); }
  const ctx = _L2.drawingContext;
  ctx.setTransform(1, 0, 0, 1, 0, 0);
  ctx.globalAlpha = 1; ctx.globalCompositeOperation = 'source-over';
  ctx.clearRect(0, 0, 960, 1080);
  ctx.save(); fn(ctx); ctx.restore();
  push();
  if (o.blend) blendMode({ multiply: MULTIPLY, screen: SCREEN, add: ADD }[o.blend]);
  if (o.alpha != null && o.alpha < 1) tint(255, 255 * o.alpha);
  image(_L2, -480, -540);
  pop();
}
const gfxCache = {};
function gfxOnce(key, w, h, paint) {
  if (gfxCache[key]) return gfxCache[key];
  const g = createGraphics(w, h); g.pixelDensity(1);
  paint(g.drawingContext, g);
  gfxCache[key] = g; return g;
}
function grain(amt = 0.16) {
  const g = gfxOnce('grain', 480, 540, (ctx) => {
    const img = ctx.createImageData(480, 540), r = mulberry32(9000);
    for (let i = 0; i < img.data.length; i += 4) {
      const v = 150 + r() * 105 - (r() < 0.02 ? 70 : 0);
      img.data[i] = v; img.data[i + 1] = v; img.data[i + 2] = v; img.data[i + 3] = 255;
    }
    ctx.putImageData(img, 0, 0);
  });
  push(); tint(255, 255 * amt); blendMode(MULTIPLY); image(g, -480, -540, 960, 1080); pop();
}
function vignette(amt = 0.4) {
  with2d((ctx) => {
    const g = ctx.createRadialGradient(480, 540, 400, 480, 540, 640);
    g.addColorStop(0, 'rgba(10,10,20,0)');
    g.addColorStop(1, `rgba(10,10,20,${amt})`);
    ctx.fillStyle = g; ctx.fillRect(0, 0, 960, 1080);
  });
}
// 绢/纸底纹（fbm 斑驳 + 拼缝），multiply 压在背景色上
function silkPaper(seed, base) {
  with2d((ctx) => {
    const w = 480, h = 540, c = document.createElement('canvas');
    c.width = w; c.height = h;
    const cx = c.getContext('2d'), img = cx.createImageData(w, h);
    const r = mulberry32(seed);
    for (let j = 0; j < h; j++) for (let i = 0; i < w; i++) {
      const n = fbm(i * 0.02 + seed, j * 0.02);
      const v = 0.5 + 0.5 * Math.sin(6.283 * (n + seed * 0.001));
      const k = (j * w + i) * 4;
      img.data[k] = base[0]; img.data[k + 1] = base[1]; img.data[k + 2] = base[2];
      img.data[k + 3] = 255 * (0.25 + 0.5 * Math.abs(n - 0.5) + r() * 0.05);
    }
    cx.putImageData(img, 0, 0);
    ctx.drawImage(c, 0, 0, 960, 1080);
  }, { blend: 'multiply', alpha: 0.5 });
}
// 逐像素墨色 wash（song_wash 同构）：toneFn 出 0..1，blur 后贴入
function toneWash(poly, toneFn, o = {}) {
  const op = Object.assign({ res: 4, alpha: 0.5, blur: 3, color: [26, 22, 18] }, o);
  let x0 = 1e9, y0 = 1e9, x1 = -1e9, y1 = -1e9;
  poly.forEach(p => { x0 = Math.min(x0, p[0]); y0 = Math.min(y0, p[1]); x1 = Math.max(x1, p[0]); y1 = Math.max(y1, p[1]); });
  const bw = Math.ceil((x1 - x0) / op.res) + 2, bh = Math.ceil((y1 - y0) / op.res) + 2;
  const c = document.createElement('canvas'); c.width = bw; c.height = bh;
  const cx = c.getContext('2d'), img = cx.createImageData(bw, bh);
  for (let j = 0; j < bh; j++) for (let i = 0; i < bw; i++) {
    const T = clamp01(toneFn(x0 + i * op.res, y0 + j * op.res));
    const k = (j * bw + i) * 4;
    img.data[k] = op.color[0]; img.data[k + 1] = op.color[1]; img.data[k + 2] = op.color[2];
    img.data[k + 3] = 255 * T * op.alpha;
  }
  cx.putImageData(img, 0, 0);
  with2d((ctx) => {
    ctx.save();
    ctx.beginPath();
    poly.forEach((p, i) => i ? ctx.lineTo(p[0], p[1]) : ctx.moveTo(p[0], p[1]));
    ctx.closePath(); ctx.clip();
    ctx.filter = `blur(${op.blur}px)`;
    ctx.drawImage(c, x0, y0, bw * op.res, bh * op.res);
    ctx.restore();
  });
}

// ============================================================
// A. 梵高·星夜（vangogh.js 架构：底漆+解析流场+四层笔触+场景纵深）
// ============================================================
function styleVanGogh() {
  randomSeed(20261031);
  background('#101a45');
  brush.scaleBrushes(5);
  brush.add('vgDab', { type: 'marker', weight: 7, scatter: 0.25, opacity: 255, spacing: 0.09, pressure: [0.8, 1, 0.75], rotate: 'natural', noise: 0.08, markerTip: false });
  // 短笔专用：marker 型笔对 2~3 点超短笔触会静默丢弃（实测），短刀一律走 default 型
  brush.add('vgDab2', { type: 'default', weight: 6, scatter: 0.35, sharpness: 0.75, grain: 7, opacity: 240, spacing: 0.08, pressure: [0.8, 1, 0.75], rotate: 'natural', noise: 0.1 });

  // ---- 解析地形（正弦叠加山脊线，笔触可沿坡走） ----
  const hillY = (x) => 700 - 42 * (x / 960) + 14 * Math.sin(x / 170 + 1.3) + 6 * Math.sin(x / 61 + 0.4);
  const ridgeY = (x) => hillY(x) + 40 + 10 * Math.sin(x / 115 + 2.6) + 5 * Math.sin(x / 47);
  const meadowY = (x) => 930 + 12 * Math.sin(x / 260 + 0.6) + 0.008 * (x - 480);
  const MOON = { x: 700, y: 205, r: 44, R: 140 };
  const VORT = [{ x: 400, y: 290, R: 125, s: 1, k: 3.4 }, { x: 565, y: 350, R: 85, s: -1, k: 3.2 }];
  const STARS = [[280, 62, 26], [505, 48, 20], [660, 82, 24], [858, 62, 16], [770, 170, 24], [920, 210, 20], [300, 228, 26], [480, 270, 18]].map(([x, y, r], i) => ({ x, y, r, i }));

  // ---- 流场：流函数波 + 涡对 + 月涡 + 星涡 + 贴山脊 ----
  const gB = (y) => Math.exp(-Math.pow((y - 330) / 160, 2));
  const psi0 = (x, y) => y - 40 * Math.sin(x / 205 + 0.4) * gB(y) - 14 * Math.sin(x / 95 - y / 140) * (0.4 + 0.6 * gB(y)) - 12 * Math.sin(x / 120 + y / 75) * (1 - sm(380, 760, x));
  function dirAt(x, y) {
    const e = 2;
    const gx = (psi0(x + e, y) - psi0(x - e, y)) / (2 * e), gy = (psi0(x, y + e) - psi0(x, y - e)) / (2 * e);
    const bl = Math.hypot(gx, gy) || 1;
    let vx = gy / bl, vy = -gx / bl;
    const dh = hillY(x) - y;
    if (dh < 90) {
      const sl = (hillY(x + 4) - hillY(x - 4)) / 8, wh = 1.2 * sm(90, 8, dh), n = Math.hypot(1, sl);
      vx += wh / n; vy += (wh * sl) / n;
    }
    for (const v of VORT) {
      const dx = x - v.x, dy = y - v.y, r = Math.hypot(dx, dy) + 1e-3;
      const w = 2.6 * Math.exp(-Math.pow(r / v.R, 2) * 1.15);
      const b = 0.27, tx = (-dy / r) * v.s, ty = (dx / r) * v.s;
      vx += w * (tx * Math.cos(b) - (dx / r) * Math.sin(b));
      vy += w * (ty * Math.cos(b) - (dy / r) * Math.sin(b));
    }
    {
      const dx = x - MOON.x, dy = y - MOON.y, r = Math.hypot(dx, dy) + 1e-3;
      const w = 4.5 * Math.exp(-Math.pow(r / (MOON.R * 1.05), 2) * 1.3);
      vx += (w * -dy) / r; vy += (w * dx) / r;
    }
    for (const s of STARS) {
      const dx = x - s.x, dy = y - s.y, r = Math.hypot(dx, dy) + 1e-3;
      const w = 2 * Math.exp(-Math.pow(r / (s.r * 1.9), 2));
      vx += (w * -dy) / r; vy += (w * dx) / r;
    }
    return Math.atan2(vy, vx);
  }

  // ---- 色阶：明度场查 10 档蓝 + 暖色地 ----
  const BLUES = [[0, '#0a1444'], [0.16, '#16267a'], [0.3, '#1d3591'], [0.42, '#2a4fa8'], [0.54, '#3f6cbd'], [0.66, '#5f95cf'], [0.77, '#8cbcd0'], [0.87, '#b9d6cd'], [0.95, '#e0e8d6'], [1, '#f6f2dc']];
  const WARM = ['#f2d24a', '#f7e58a', '#e9b44c', '#fff1a8', '#f0c74a', '#e7a43a'];
  function skyV(x, y) {
    const p = psi0(x, y);
    let v = 0.3 + 0.2 * sm(120, 640, y);
    v += 0.1 * Math.sin(p / 17 + 2.2 * noise2(x / 150, y / 150));
    const wm = sm(160, 320, x) * (1 - sm(700, 860, x));
    v += 0.38 * Math.exp(-Math.pow((p - 390) / 34, 2)) * wm;   // 大波浪光带
    v += 0.18 * Math.exp(-Math.pow((p - 445) / 20, 2)) * wm;
    v += 0.16 * Math.exp(-Math.pow((p - 225) / 24, 2)) * sm(250, 460, x) * (1 - sm(660, 790, x));
    const dh = hillY(x) - y;
    v += 0.42 * Math.exp(-Math.pow((dh - 20) / 44, 2)) * (0.35 + 0.65 * sm(350, 860, x)); // 地平线暖光
    for (const vt of VORT) {
      const dx = x - vt.x, dy = y - vt.y, r = Math.hypot(dx, dy) + 1e-3;
      const w = Math.exp(-Math.pow(r / vt.R, 2) * 1.7);
      const ph = Math.atan2(dy, dx) * vt.s + Math.log(r) * vt.k;
      let vv = 0.28 + 0.62 * (0.5 + 0.5 * Math.cos(ph));        // 涡旋明暗臂
      vv = lerp(vv, 0.92, sm(0.2, 0.06, r / vt.R));
      v = lerp(v, vv, w * 0.92);
    }
    return v;
  }
  function skyCol(x, y, lift, r) {
    const dm = Math.hypot(x - MOON.x, y - MOON.y);
    if (dm < MOON.R * 1.02) {
      const ring = dm / 13 + noise2(x / 40, y / 40) * 1.2;
      let c = WARM[Math.floor(ring + r() * 1.4) % WARM.length];
      const edge = sm(MOON.R * 0.72, MOON.R * 1.02, dm);
      if (edge > 0) c = mix(c, r() < 0.5 ? '#c9d48a' : '#9ec7c0', edge * 0.85);
      return c;
    }
    for (const s of STARS) {
      const d = Math.hypot(x - s.x, y - s.y);
      if (d < s.r * 1.25) return d < s.r * 0.95 ? '#f8f4dc' : ramp(BLUES, 0.5 + r() * 0.2);
    }
    const v = skyV(x, y) + lift + (r() - 0.5) * 0.16;
    let c = ramp(BLUES, v);
    if (v > 0.62 && r() < 0.07) c = r() < 0.5 ? '#c8cf7a' : '#e9dd8a';  // 梵高绿黄点缀
    return c;
  }
  const stroke = (x, y, len, col, w, seed, dir = 0) => {
    randomSeed(seed);
    brush.set('vgDab', col, w);
    brush.flowLine(x - 480, y - 540, len, dir);
  };

  // ---- 1) 底漆层：夜空渐变 + 月/星暖地（光谱混色防绿） ----
  with2d((ctx) => {
    const g = ctx.createLinearGradient(0, 0, 0, 720);
    g.addColorStop(0, '#0c1752'); g.addColorStop(0.45, '#1a2f8c');
    g.addColorStop(0.8, '#2c55a8'); g.addColorStop(1, '#5d8fc6');
    ctx.fillStyle = g; ctx.fillRect(0, 0, 960, 740);
    const m = ctx.createRadialGradient(MOON.x, MOON.y, 0, MOON.x, MOON.y, MOON.R * 1.04);
    m.addColorStop(0, '#f2b833'); m.addColorStop(0.72, '#f0cc48');
    m.addColorStop(0.9, 'rgba(226,214,120,0.85)'); m.addColorStop(1, 'rgba(160,190,170,0)');
    ctx.fillStyle = m; ctx.fillRect(MOON.x - MOON.R * 1.1, MOON.y - MOON.R * 1.1, MOON.R * 2.2, MOON.R * 2.2);
    STARS.forEach((s) => {
      const q = ctx.createRadialGradient(s.x, s.y, 0, s.x, s.y, s.r * 1.05);
      q.addColorStop(0, '#fbf3cc'); q.addColorStop(0.75, 'rgba(240,226,150,0.9)'); q.addColorStop(1, 'rgba(190,210,220,0)');
      ctx.fillStyle = q; ctx.fillRect(s.x - s.r * 1.1, s.y - s.r * 1.1, s.r * 2.2, s.r * 2.2);
    });
  });

  // ---- 2) 天空四层笔触（底层→主层→高光→暗缝） ----
  brush.addField('vgSky', (_t, field) => {
    const res = 9.6;
    for (let c = 0; c < field.length; c++) {
      const x = c * res - 480;
      for (let r = 0; r < field[0].length; r++) {
        const y = r * res - 540;
        field[c][r] = x < -120 || x > 1080 || y < -120 || y > 1200 ? 0 : dirAt(x, y) * 180 / Math.PI;
      }
    }
    return field;
  });
  brush.field('vgSky');
  brush.clip([-478, -538, 478, 162]);
  let r = mulberry32(7001);                                  // L0 宽笔底层
  for (let i = 0; i < 480; i++) {
    const x = -20 + r() * 1000, y = -10 + r() * 660, len = 50 + r() * 60, w = 1.4 + r() * 0.6;
    if (y > hillY(x) + 30) continue;
    stroke(x, y, len, skyCol(x, y, -0.1, r), w, 11 + i * 3);
  }
  r = mulberry32(7002);                                      // L1 网格主层
  let n = 0;
  for (let gy = -6; gy < 660; gy += 13) for (let gx = -16; gx < 976; gx += 23) {
    const x = gx + (r() - 0.5) * 24, y = gy + (r() - 0.5) * 14, len = 20 + r() * 40, w = 0.75 + r() * 0.5, jd = (r() - 0.5) * 0.22;
    n++;
    if (y > hillY(x) + 18) continue;
    stroke(x, y, len, skyCol(x, y, 0, r), w, 5000 + n * 3, jd);
  }
  r = mulberry32(7003);                                      // L2 苍白高光（亮区才加）
  for (let i = 0; i < 1000; i++) {
    const x = r() * 960, y = r() * 620, len = 14 + r() * 24, w = 0.5 + r() * 0.35;
    if (y > hillY(x) + 6) continue;
    const v = skyV(x, y), dm = Math.hypot(x - MOON.x, y - MOON.y);
    if (v < 0.5 && dm > MOON.R * 1.05) { r(); r(); continue; }
    const c = dm < MOON.R * 1.05 ? (r() < 0.5 ? '#fff6c4' : '#f6dc5a') : skyCol(x, y, 0.2, r);
    stroke(x, y, len, c, w, 20000 + i * 5);
  }
  r = mulberry32(7004);                                      // L3 深谷暗缝（"绳"的拧劲）
  for (let i = 0; i < 450; i++) {
    const x = r() * 960, y = r() * 610, len = 20 + r() * 28, w = 0.45 + r() * 0.3;
    if (y > hillY(x) - 4) continue;
    if (skyV(x, y) > 0.48 || Math.hypot(x - MOON.x, y - MOON.y) < MOON.R * 1.1 || STARS.some(st => Math.hypot(x - st.x, y - st.y) < st.r * 1.5)) continue;
    stroke(x, y, len, r() < 0.6 ? '#0d1a5c' : '#16267a', w, 40000 + i * 7);
  }
  r = mulberry32(7005);                                      // 中带加密（y500-620 密度补偿）
  for (let i = 0; i < 300; i++) {
    const x = 120 + r() * 720, y = 470 + r() * 170, len = 18 + r() * 30, w = 0.6 + r() * 0.4;
    if (y > hillY(x) + 12) continue;
    stroke(x, y, len, skyCol(x, y, 0.05, r), w, 60000 + i * 3);
  }
  brush.noClip(); brush.noField();

  // ---- 3) 月亮：光环短弧 + 柠檬白新月 + 橙底旋笔 ----
  const HALO = ['#f2c62a', '#f8da48', '#ffe873', '#eab02a', '#f6d23c', '#e09a22', '#fff2a6'];
  r = mulberry32(8100);
  const arcPts = (cx, cy, rad, a0, a1, st = 6) => {
    const cnt = Math.max(3, Math.ceil(Math.abs(a1 - a0) * rad / st)), out = [];
    for (let i = 0; i <= cnt; i++) { const a = a0 + (a1 - a0) * i / cnt; out.push([cx + Math.cos(a) * rad, cy + Math.sin(a) * rad]); }
    return out;
  };
  const inkS = (pts, col, w, seed) => {
    randomSeed(seed);
    brush.set('vgDab2', col, w);
    const P = pts.map(p => [p[0] - 480, p[1] - 540]);
    // brush.spline 两点 = 静默不渲染（2.2.3 实测铁证），两点走 line
    if (P.length === 2) brush.line(P[0][0], P[0][1], P[1][0], P[1][1]);
    else brush.spline(P, 1);
  };
  for (let rad = 62; rad < MOON.R; rad += 7) {
    const nA = 3 + Math.floor(r() * 3);
    for (let j = 0; j < nA; j++) {
      const a0 = r() * Math.PI * 2, len = 0.6 + r() * 1.3, w = 0.75 + r() * 0.4, t = rad / MOON.R;
      let c = HALO[Math.floor(r() * HALO.length)];
      if (t > 0.86) c = mix(c, r() < 0.5 ? '#b8cf7a' : '#9ec7c0', (t - 0.86) * 4);
      inkS(arcPts(MOON.x, MOON.y, rad + (r() - 0.5) * 4, a0, a0 + len), c, w, 81 + j + rad * 3);
    }
  }
  brush.noStroke(); brush.wash('#e8901e', 255); brush.circle(MOON.x - 480, MOON.y - 540, MOON.r + 3, 0.1); brush.noWash();
  for (let j = 0; j < 7; j++) {                              // 未照部橙旋
    const a0 = 1.7 + j * 0.36 + r() * 0.2, rr = MOON.r * (0.35 + 0.45 * r());
    inkS(arcPts(MOON.x - 5, MOON.y, rr, a0, a0 + 0.7 + r() * 0.5, 4), ['#f2a62c', '#e0861e', '#f6b840', '#e8942a', '#d77a1c'][j % 5], 0.55, 820 + j);
  }
  brush.noStroke(); brush.wash('#ffd93a', 255);               // 新月牙
  const cres = [];
  for (let i = 0; i <= 20; i++) { const a = -Math.PI / 2 + Math.PI * i / 20; cres.push([MOON.x + Math.cos(a) * (MOON.r + 2), MOON.y + Math.sin(a) * (MOON.r + 2)]); }
  for (let i = 20; i >= 0; i--) { const a = -Math.PI / 2 + Math.PI * i / 20; cres.push([MOON.x - 6 + Math.cos(a) * (MOON.r * 0.42), MOON.y + Math.sin(a) * (MOON.r + 1)]); }
  brush.polygon(cres.map(p => [p[0] - 480, p[1] - 540])); brush.noWash();
  for (let j = 0; j < 5; j++) {                              // 月面笔触化（破平涂）
    const f = 0.5 + j * 0.09, span = 1.45 - j * 0.04;
    const pts = [];
    for (let i = 0; i <= 14; i++) { const a = -span + 2 * span * i / 14; pts.push([MOON.x - 3 + Math.cos(a) * MOON.r * f, MOON.y + Math.sin(a) * MOON.r * 0.97]); }
    inkS(pts, j > 3 ? '#fff4b0' : j > 1 ? '#ffe46a' : '#ffd23a', 0.6, 840 + j);
  }
  inkS(arcPts(MOON.x, MOON.y, MOON.r + 5, -1.9, 1.9), '#d0782a', 0.38, 860);

  // ---- 4) 远山（坡向笔触）+ 短搭笔棱线 ----
  const polyBelow = (fy, x0, x1, yb, st = 12) => {
    const pts = [];
    for (let x = x0; x <= x1; x += st) pts.push([x, fy(x)]);
    pts.push([x1, yb], [x0, yb]); return pts;
  };
  const ridgeDabs = (fy, dy, cols, w, seed) => {
    const rr = mulberry32(seed); let x = -24, j = 0;
    while (x < 980) {
      const len = 30 + rr() * 50, x1 = x + len, o = (rr() - 0.5) * 3, pts = [];
      for (let xx = x; xx <= x1; xx += 7) pts.push([xx, fy(xx) + dy + o]);
      inkS(pts, cols[Math.floor(rr() * cols.length)], w * (0.7 + rr() * 0.6), seed * 100 + j++);
      x = x1 - 5 - rr() * 12 + (rr() < 0.15 ? 15 : 0);
    }
  };
  brush.noStroke(); brush.wash('#2b3a7a', 255);
  brush.polygon(polyBelow(hillY, -12, 972, 940).map(p => [p[0] - 480, p[1] - 540])); brush.noWash();
  r = mulberry32(9100);
  for (let i = 0; i < 300; i++) {
    const x = r() * 960, t = r(), y = lerp(hillY(x) + 4, ridgeY(x) + 26, t);
    const sl = (hillY(x + 6) - hillY(x - 6)) / 12;
    const len = 26 + r() * 44, a = Math.atan(sl) + (r() - 0.5) * 0.25;
    const c = t < 0.18 ? (r() < 0.5 ? '#6f86c4' : '#8aa0cf') : ['#33468e', '#2a3a7c', '#465a9e', '#3a3f86', '#55509a'][Math.floor(r() * 5)];
    inkS([[x, y], [x + Math.cos(a) * len * 0.5, y + Math.sin(a) * len * 0.5 + (r() - 0.5) * 6], [x + Math.cos(a) * len, y + Math.sin(a) * len]], c, 0.7 + r() * 0.4, 900 + i);
  }
  ridgeDabs(hillY, 2, ['#8fa6d6', '#a9bde0', '#7a92c8', '#c3d0e4'], 0.5, 77);
  ridgeDabs(hillY, 8, ['#1a2560', '#222e6c', '#151d50'], 0.4, 78);
  // 近脊深蓝绿 + 树簇
  brush.noStroke(); brush.wash('#1b2d50', 255);
  brush.polygon(polyBelow(ridgeY, -12, 972, 960).map(p => [p[0] - 480, p[1] - 540])); brush.noWash();
  for (let i = 0; i < 240; i++) {
    const x = r() * 960, t = r(), y = lerp(ridgeY(x) + 4, 860, t);
    const sl = (ridgeY(x + 6) - ridgeY(x - 6)) / 12;
    const len = 20 + r() * 34, a = Math.atan(sl) * (1 - t) + (r() - 0.5) * 0.35;
    inkS([[x, y], [x + Math.cos(a) * len, y + Math.sin(a) * len]], ['#24406a', '#1c3456', '#2f5068', '#203a5e', '#355a72', '#1a2a4c'][Math.floor(r() * 6)], 0.65 + r() * 0.4, 1500 + i);
  }
  ridgeDabs(ridgeY, 1, ['#4f74a0', '#5f86b0', '#3e6290'], 0.4, 79);
  const treeClump = (x, y, rx, ry, seed, dark) => {
    const rr = mulberry32(seed), pts = [];
    for (let i = 0; i < 14; i++) {
      const a = (i / 14) * Math.PI * 2, f = 0.82 + 0.3 * rr();
      pts.push([x + Math.cos(a) * rx * f, y + Math.sin(a) * ry * f * (Math.sin(a) > 0 ? 0.7 : 1)]);
    }
    brush.noStroke(); brush.wash(dark ? '#0f2030' : '#152a38', 255);
    brush.polygon(pts.map(p => [p[0] - 480, p[1] - 540])); brush.noWash();
    for (let j = 0; j < 5; j++) {
      const a = rr() * Math.PI * 2, q = rr() * 0.6;
      const px = x + Math.cos(a) * rx * q, py = y + Math.sin(a) * ry * q;
      inkS([[px - 7, py + 4], [px, py - 6], [px + 7, py + 2]], ['#24453f', '#2c5248', '#1b3436', '#3a5c4a', '#203c50'][Math.floor(rr() * 5)], 0.5, seed * 3 + j);
    }
  };
  for (let i = 0; i < 14; i++) treeClump(r() * 340, ridgeY(r() * 340) + 26 + r() * 60, 13 + r() * 16, 9 + r() * 8, 1600 + i, true);
  for (let i = 0; i < 18; i++) treeClump(300 + r() * 660, 730 + r() * 60, 13 + r() * 18, 10 + r() * 12, 60 + i, r() < 0.6);

  // ---- 5) 小村：屋墙坡向笔 + 亮窗 ----
  const HOUSES = (() => {
    const rr = mulberry32(4247), out = [];
    let x = 470 + rr() * 30;
    while (x < 860) {
      const w = lerp(26, 52, rr()), h = lerp(16, 28, rr());
      out.push({ x, w, h, base: 762 + Math.sin(x / 90) * 6 + rr() * 5, roof: 9 + rr() * 12, gable: rr() < 0.5, win: Math.floor(rr() * 2.4), shade: rr() });
      x += w + lerp(4, 30, rr());
    }
    return out;
  })();
  HOUSES.forEach((hs, i) => {
    const b = hs.base, top = b - hs.h;
    const wc = mix(['#3a4c86', '#45558c', '#2f3f74', '#56669a', '#7488b8'][Math.floor(hs.shade * 5)], '#0e1638', 0.2);
    brush.noStroke(); brush.wash(wc, 255);
    brush.polygon([[hs.x, b], [hs.x, top], [hs.x + hs.w, top], [hs.x + hs.w, b]].map(p => [p[0] - 480, p[1] - 540])); brush.noWash();
    const ns = Math.max(3, Math.round(hs.w / 11));
    for (let j = 0; j < ns; j++) {
      const f = (j + 0.5) / ns;
      const c = f > 0.62 ? mix(wc, '#9fb2d8', 0.4) : f < 0.3 ? mix(wc, '#0c1430', 0.35) : mix(wc, '#6a7cb0', 0.2);
      inkS([[hs.x + f * hs.w, b - 2], [hs.x + f * hs.w + (r() - 0.5) * 2, top + 3]], c, 0.42, 200 + i * 7 + j);
    }
    const rc = mix(['#16224a', '#1a2e48', '#202c58', '#14283e'][Math.floor(hs.shade * 4)], '#070c22', 0.2);
    const roofP = hs.gable
      ? [[hs.x - 3, top + 2], [hs.x + hs.w / 2, top - hs.roof - 5], [hs.x + hs.w + 3, top + 2]]
      : [[hs.x - 4, top + 2], [hs.x + hs.w * 0.2, top - hs.roof], [hs.x + hs.w * 0.8, top - hs.roof], [hs.x + hs.w + 4, top + 2]];
    brush.noStroke(); brush.wash(rc, 255);
    brush.polygon(roofP.map(p => [p[0] - 480, p[1] - 540])); brush.noWash();
    inkS(roofP.slice(1), '#6c8cc2', 0.26, 400 + i);
    for (let j = 0; j < hs.win; j++) {
      const wx = hs.x + hs.w * (0.3 + 0.4 * j / Math.max(1, hs.win - 1)), wy = b - hs.h * 0.5;
      brush.noStroke(); brush.wash('#ffd23a', 255);
      brush.rect(wx - 480 - 2.5, wy - 540 - 4, 5, 8); brush.noWash();
    }
  });

  // ---- 6) 柏树（火焰柱，两臂随节拍）+ 麦地 ----
  r = mulberry32(113);
  const cc = ['#16260f', '#1d3315', '#26421a', '#1a2c12'];
  for (let i = 0; i < 150; i++) {
    const t = i / 150;
    const y = 1010 - t * 880;
    const flame = Math.sin(Math.min(1, t * 1.3) * Math.PI);
    const half = 6 + 34 * flame * (1 - t * 0.35);
    const x = 168 + Math.sin(t * 11) * 6;
    inkS([[x + (r() - 0.5) * half * 2, y], [x + (r() - 0.5) * half * 2, y - 24 - r() * 30]], cc[i % 4], 0.75 + r() * 0.5, 9000 + i);
  }
  for (let i = 0; i < 26; i++) {                             // 柏树两臂
    const arm = i < 13 ? -1 : 1, t = i % 13 / 13;
    const y = 420 - t * 220;
    const bx = 168 + arm * (8 + 26 * Math.sin(t * 2.4));
    inkS([[bx, y], [bx + arm * (18 + r() * 26), y - 16 - r() * 22]], cc[i % 4], 0.55, 9600 + i);
  }
  r = mulberry32(109);                                       // 麦地三色横笔
  const gcols = ['#b8862f', '#8a6a2a', '#5d7a3a', '#c9a03a'];
  for (let i = 0; i < 380; i++) {
    const x = 10 + r() * 940, y = meadowY(x) + r() * (1080 - meadowY(x));
    inkS([[x, y], [x + 16 + r() * 26, y + (r() - 0.5) * 5]], gcols[Math.floor(r() * 4)], 0.6 + r() * 0.5, 12000 + i);
  }

  grain(0.14); vignette(0.34);
}

// ============================================================
// B. 北宋水墨（song.js 架构：绢底+逐像素墨色+三种皴+做旧三层）
// ============================================================
function styleInk() {
  randomSeed(20261047);
  background('#efe8d8');
  brush.scaleBrushes(5);
  brush.add('inkDry', { type: 'default', weight: 3, scatter: 0.55, sharpness: 0.7, grain: 12, opacity: 225, spacing: 0.06, pressure: [1, 1, 1], rotate: 'none', noise: 0.18 });
  const INK = '#1a1612';

  // 主峰 + 远山 + 前景坡岸几何
  const MAIN = [[-30, 730], [60, 565], [160, 600], [300, 335], [430, 172], [540, 118], [650, 180], [770, 340], [880, 525], [940, 615], [990, 730]];
  const DIST = [[-20, 668], [110, 545], [250, 588], [380, 660]];
  const BANK = [[0, 812], [330, 792], [620, 818], [960, 798], [960, 1080], [0, 1080]];
  const ridgeAt = (x) => {
    for (let i = 0; i < MAIN.length - 1; i++) {
      const a = MAIN[i], b = MAIN[i + 1];
      if (x >= a[0] && x <= b[0]) return a[1] + (b[1] - a[1]) * ((x - a[0]) / (b[0] - a[0] || 1));
    }
    return 700;
  };

  // ---- 1) 绢底 ----
  silkPaper(47, [205, 195, 165]);

  // ---- 2) 墨色函数：深度 + 棱线高光 + 皱褶暗部 + fbm ----
  const crev = (x, y) => Math.max(0, fbm(x * 0.012 + 5, y * 0.012) - 0.52) * 2.2;   // 皱褶
  const mainTone = (x, y) => {
    const top = ridgeAt(x);
    const depth = clamp01((y - top) / 420);                                          // 越山脚越淡
    const rim = Math.exp(-Math.pow((y - top) / 90, 2)) * 0.55;                        // 棱线受光
    const side = sm(560, 900, x) * 0.18;                                             // 右侧受光略淡
    let T = 0.78 * (1 - depth * 0.75) + rim - crev(x, y) * 0.5 - side;
    return clamp01(T + (fbm(x * 0.03, y * 0.03) - 0.5) * 0.35);
  };

  // 远山淡墨（2D 淡 wash）
  with2d((ctx) => {
    ctx.fillStyle = 'rgba(70,72,64,0.35)';
    ctx.beginPath();
    DIST.forEach((p, i) => i ? ctx.lineTo(p[0], p[1]) : ctx.moveTo(p[0], p[1]));
    ctx.closePath(); ctx.fill();
  });
  // 主峰逐像素墨色 wash
  toneWash(MAIN, mainTone, { res: 4, alpha: 0.62, blur: 3 });

  // ---- 3) 2D 微点织底（song_microdots） ----
  with2d((ctx) => {
    const rr = mulberry32(305);
    ctx.save();
    ctx.beginPath();
    MAIN.forEach((p, i) => i ? ctx.lineTo(p[0], p[1]) : ctx.moveTo(p[0], p[1]));
    ctx.closePath(); ctx.clip();
    for (let k = 0; k < 14000; k++) {
      const x = rr() * 960, y = 120 + rr() * 580, q = rr(), s = rr(), rot = rr();
      const T = mainTone(x, y);
      if (q > T * 0.95 || !pip(x, y, MAIN.concat([[990, 730]]))) continue;
      ctx.fillStyle = `rgba(26,22,18,${0.3 * (0.4 + 0.6 * T) * (0.5 + s * 0.6)})`;
      ctx.beginPath();
      ctx.ellipse(x, y, (0.5 + s * 0.7), (1.2 + s * 1.8), (rot - 0.5) * 0.4, 0, Math.PI * 2);
      ctx.fill();
    }
    ctx.restore();
  });

  // ---- 4) 雨点皴（tone 拒采，三笔混用）+ 长条皴 ----
  const rr2 = mulberry32(147);
  let made = 0, guard = 0;
  while (made < 2600 && guard < 30000) {
    guard++;
    const x = rr2() * 960, top = ridgeAt(x);
    if (top > 700) continue;
    const y = top + rr2() * (688 - top);
    if (!pip(x, y, MAIN.concat([[990, 730]]))) continue;
    const T = mainTone(x, y);
    if (rr2() > Math.pow(T, 1.4) * 0.95) continue;
    const len = lerp(3, 8, rr2()) * (0.75 + 0.45 * T);
    const br = ['2B', 'HB', 'charcoal'][Math.floor(rr2() * 3)];
    const w = lerp(0.5, 1.1, rr2()) * (0.8 + 0.35 * T);
    const a = lerp(0.35, 0.85, rr2()) * (0.5 + 0.5 * T);
    randomSeed(147 * 7 + made);
    brush.set(br, withAlpha(INK, a), w);
    brush.line(x - 480, y - 540, x - 480 + (rr2() - 0.5) * 2.5, y - 540 + len);
    made++;
  }
  let streaks = 0, tries = 0;
  while (streaks < 60 && tries < 600) {
    tries++;
    const x = 100 + rr2() * 720, top = ridgeAt(x), y = top + 25 + rr2() * 130;
    if (y > 680 || rr2() > mainTone(x, y)) continue;
    const len = lerp(30, 130, rr2()), bend = (rr2() - 0.5) * len * 0.12;
    randomSeed(147 * 13 + streaks);
    brush.set('inkDry', withAlpha(INK, lerp(0.3, 0.6, rr2())), lerp(0.3, 0.6, rr2()));
    brush.spline([[x - 480, y - 540], [x + bend - 480, y + len * 0.5 - 540], [x + bend * 0.4 - 480, y + len - 540]], 1);
    streaks++;
  }

  // ---- 5) 棱线枯笔（笔断意连） ----
  const rrC = mulberry32(151);
  randomSeed(1510);
  brush.set('inkDry', '#2c2d27', 0.55);
  for (let i = 0; i < MAIN.length - 1; i++) {
    const a = MAIN[i], b = MAIN[i + 1];
    if ((a[0] + b[0]) / 2 < 210) continue;
    for (let s = 0; s < 3; s++) {
      if (rrC() < 0.32) continue;
      const t0 = s / 3, t1 = (s + 0.82) / 3;
      brush.line(a[0] - 480 + (b[0] - a[0]) * t0, a[1] - 540 + (b[1] - a[1]) * t0,
                 a[0] - 480 + (b[0] - a[0]) * t1, a[1] - 540 + (b[1] - a[1]) * t1);
    }
  }

  // ---- 6) 云雾留白（模糊白带 + 白斑） ----
  with2d((ctx) => {
    ctx.filter = 'blur(14px)';
    const g1 = ctx.createLinearGradient(0, 606, 0, 700);
    g1.addColorStop(0, 'rgba(239,232,216,0)'); g1.addColorStop(0.5, 'rgba(239,232,216,1)'); g1.addColorStop(1, 'rgba(239,232,216,0)');
    ctx.fillStyle = g1; ctx.fillRect(0, 606, 960, 94);
    const rr = mulberry32(909);
    for (let i = 0; i < 9; i++) {                             // 飘渺白斑
      const x = rr() * 960, y = 610 + rr() * 90, rx = 60 + rr() * 110;
      const q = ctx.createRadialGradient(x, y, 0, x, y, rx);
      q.addColorStop(0, 'rgba(239,232,216,0.9)'); q.addColorStop(1, 'rgba(239,232,216,0)');
      ctx.fillStyle = q;
      ctx.beginPath(); ctx.ellipse(x, y, rx, rx * 0.32, 0, 0, Math.PI * 2); ctx.fill();
    }
    ctx.filter = 'none';
  });

  // ---- 7) 前景坡岸 + 树（点簇/介字点）+ 苇草 ----
  toneWash(BANK, (x, y) => 0.35 * (1 - sm(820, 1080, y) * 0.5) + (fbm(x * 0.02, y * 0.02) - 0.5) * 0.2, { res: 5, alpha: 0.4, blur: 4 });
  randomSeed(1570);
  brush.set('inkDry', '#33342e', 0.5);
  brush.spline([[176 - 480, 918 - 540, 1.5], [164 - 480, 800 - 540, 1.1], [150 - 480, 700 - 540, 0.8]], 0.5);
  brush.line(164 - 480, 810 - 540, 200 - 480, 742 - 540);
  const rrL = mulberry32(157);
  const ICAN = [[152, 652, 62], [220, 682, 48], [106, 692, 44], [170, 732, 52], [196, 620, 40]];
  const clusterDab = (px, py, col, a, seed) => {
    randomSeed(seed);
    brush.set('inkDry', withAlpha(col, a), 0.34 + (seed % 3) * 0.04);
    brush.line(px - 480, py - 540, px - 480 + (rrL() - 0.5) * 4, py - 540 + 3 + rrL() * 4);
  };
  for (let i = 0; i < 260; i++) {
    const circ = ICAN[i % ICAN.length];
    const a = rrL() * Math.PI * 2, d = Math.pow(rrL(), 0.7) * circ[2];
    const px = circ[0] + Math.cos(a) * d * 1.3, py = circ[1] + Math.sin(a) * d * 0.65;
    const al = (0.9 - 0.4 * (d / circ[2])) * (0.7 + rrL() * 0.3);
    clusterDab(px, py, i % 5 === 0 ? '#33342e' : '#4a4b45', al, 3000 + i);
    if (i % 3 === 0) clusterDab(px + 2, py - 3, '#33342e', al * 0.8, 3100 + i);   // 介字点第二笔
  }
  randomSeed(1580);
  brush.set('inkDry', '#43443d', 0.32);
  for (let i = 0; i < 30; i++) {
    const x = 40 + rrL() * 880, y = 840 + rrL() * 200;
    const len = 14 + rrL() * 18, sway = (rrL() - 0.5) * 18;
    brush.line(x - 480, y - 540, x - 480 + sway, y - 540 - len);
  }

  // ---- 8) 飞鸟 + 朱印 ----
  randomSeed(1590);
  brush.set('inkDry', '#2f302b', 0.4);
  brush.spline([[150 - 480, 168 - 540, 1], [176 - 480, 146 - 540, 1.2], [202 - 480, 166 - 540, 1]], 0.6);
  brush.spline([[218 - 480, 208 - 540, 1], [238 - 480, 192 - 540, 1.2], [258 - 480, 210 - 540, 1]], 0.6);
  brush.noStroke(); brush.wash('#b03a2e', 255);
  brush.rect(880 - 480, 985 - 540, 46, 46); brush.noWash();
  randomSeed(1600);
  brush.set('inkDry', '#efe8d8', 0.55);
  brush.spline([[892 - 480, 1008 - 540, 1], [903 - 480, 1000 - 540, 1.1], [914 - 480, 1008 - 540, 1]], 0.5);
  brush.spline([[892 - 480, 1018 - 540, 1], [903 - 480, 1010 - 540, 1.1], [914 - 480, 1018 - 540, 1]], 0.5);

  // ---- 9) 做旧三层：patina(multiply) + wear(screen) ----
  with2d((ctx) => {                                           // patina
    const rr = mulberry32(777);
    ctx.globalAlpha = 1;
    ctx.strokeStyle = 'rgba(120,100,70,0.05)'; ctx.lineWidth = 1;    // 织纹
    for (let y = 0; y < 1080; y += 3) { ctx.beginPath(); ctx.moveTo(0, y); ctx.lineTo(960, y); ctx.stroke(); }
    for (let i = 0; i < 46; i++) {                                  // foxing 霉斑
      const x = rr() * 960, y = rr() * 1080, r0 = 2 + rr() * 9;
      const q = ctx.createRadialGradient(x, y, 0, x, y, r0);
      q.addColorStop(0, `rgba(150,110,60,${0.1 + rr() * 0.1})`); q.addColorStop(1, 'rgba(150,110,60,0)');
      ctx.fillStyle = q; ctx.beginPath(); ctx.arc(x, y, r0, 0, Math.PI * 2); ctx.fill();
    }
    for (let i = 0; i < 7; i++) {                                   // 裂纹
      let x = rr() * 960, y = rr() * 1080;
      ctx.strokeStyle = 'rgba(60,50,35,0.13)'; ctx.lineWidth = 0.8;
      ctx.beginPath(); ctx.moveTo(x, y);
      for (let s = 0; s < 6; s++) { x += (rr() - 0.5) * 130; y += (rr() - 0.3) * 90; ctx.lineTo(x, y); }
      ctx.stroke();
    }
    const vg = ctx.createRadialGradient(480, 540, 330, 480, 540, 700);   // 烟熏边
    vg.addColorStop(0, 'rgba(80,65,40,0)'); vg.addColorStop(1, 'rgba(80,65,40,0.22)');
    ctx.fillStyle = vg; ctx.fillRect(0, 0, 960, 1080);
  }, { blend: 'multiply', alpha: 0.75 });
  with2d((ctx) => {                                           // wear 高光裂
    const rr = mulberry32(888);
    for (let i = 0; i < 5; i++) {
      let x = rr() * 960, y = rr() * 1080;
      ctx.strokeStyle = 'rgba(255,250,235,0.12)'; ctx.lineWidth = 1.4;
      ctx.beginPath(); ctx.moveTo(x, y);
      for (let s = 0; s < 5; s++) { x += (rr() - 0.5) * 120; y += (rr() - 0.3) * 80; ctx.lineTo(x, y); }
      ctx.stroke();
    }
  }, { blend: 'screen', alpha: 0.8 });
  grain(0.1); vignette(0.3);
}

// ============================================================
// C. 浮世绘·平涂粗线（v1 通过，加纸纹+收尾统一质感）
// ============================================================
function styleUkiyo() {
  randomSeed(20261059);
  background('#f3e9d0');
  brush.scaleBrushes(2);
  silkPaper(59, [225, 210, 175]);
  brush.add('bold',   { type: 'default', weight: 3.4, scatter: 0.8, sharpness: 0.65, grain: 9,  opacity: 250, spacing: 0.1,  pressure: [1.15, 0.85] });
  brush.add('flatst', { type: 'default', weight: 2,   scatter: 0.5, sharpness: 0.7,  grain: 11, opacity: 245, spacing: 0.08, pressure: [1.1, 0.9] });

  const SUN = { x: 690, y: 235, r: 82 };
  brush.noStroke(); brush.wash('#c93a2e', 255);
  brush.circle(SUN.x - 480, SUN.y - 540, SUN.r * 0.92, 0.12);
  brush.noWash(); brush.noFill();
  brush.set('bold', '#2b2b33', 1);
  brush.circle(SUN.x - 480, SUN.y - 540, SUN.r, 0.1);

  const cloud = (cx, cy, w, h) => {
    const P = [[cx - w, cy], [cx - w * 0.5, cy - h], [cx + w * 0.5, cy - h], [cx + w, cy], [cx + w * 0.45, cy + h * 0.7], [cx - w * 0.45, cy + h * 0.7]];
    brush.noStroke(); brush.wash('#f7f3e8', 255); brush.polygon(P.map(p => [p[0] - 480, p[1] - 540]));
    brush.noWash(); brush.noFill(); brush.set('bold', '#2b2b33', 1); brush.polygon(P.map(p => [p[0] - 480, p[1] - 540]));
  };
  cloud(230, 205, 150, 34);
  cloud(700, 420, 190, 30);

  const FAR = [[0, 640], [110, 472], [235, 532], [390, 418], [540, 505], [660, 455], [820, 522], [960, 468]];
  const closePoly = (pts, baseY) => pts.concat([[960, baseY], [0, baseY]]);
  brush.noStroke(); brush.wash('#7a8fa8', 200); brush.polygon(closePoly(FAR, 655).map(p => [p[0] - 480, p[1] - 540]));
  brush.noWash();
  const fuji = [[330, 648], [462, 306], [558, 306], [690, 648]];
  const cap = [[462, 306], [558, 306], [606, 418], [414, 418]];
  brush.wash('#3f5a8f', 255); brush.polygon(fuji.map(p => [p[0] - 480, p[1] - 540]));
  brush.noWash(); brush.wash('#f7f3e8', 255); brush.polygon(cap.map(p => [p[0] - 480, p[1] - 540]));
  brush.noWash(); brush.noFill(); brush.set('bold', '#2b2b33', 1);
  brush.polygon(fuji.map(p => [p[0] - 480, p[1] - 540]));
  brush.polygon(cap.map(p => [p[0] - 480, p[1] - 540]));
  brush.polygon(closePoly(FAR, 655).map(p => [p[0] - 480, p[1] - 540]));

  brush.noStroke(); brush.wash('#8fa06a', 255);
  brush.polygon([[0, 640], [960, 640], [960, 1080], [0, 1080]].map(p => [p[0] - 480, p[1] - 540]));
  brush.noWash(); brush.noFill();
  brush.set('bold', '#2b2b33', 0.8);
  brush.line(-478, 100, 478, 100);
  brush.set('flatst', '#55663d', 1);
  const rngG = mulberry32(163);
  for (let i = 0; i < 90; i++) {
    const x = 25 + rngG() * 910, y = 660 + rngG() * 390;
    brush.line(x - 480, y - 540, x - 480 + (rngG() - 0.5) * 6, y - 540 - (10 + rngG() * 10));
  }
  brush.set('bold', '#2b2b33', 1.6);
  brush.spline([[176 - 480, 662 - 540, 1.3], [168 - 480, 566 - 540, 1], [158 - 480, 486 - 540, 0.8]], 0.4);
  const pad = (cy, w) => {
    const P = [[178 - w, cy], [178 + w, cy], [178 + w * 0.55, cy - 26], [178 - w * 0.55, cy - 26]];
    brush.noStroke(); brush.wash('#3f5a48', 255); brush.polygon(P.map(p => [p[0] - 480, p[1] - 540]));
    brush.noWash(); brush.noFill(); brush.set('bold', '#2b2b33', 1); brush.polygon(P.map(p => [p[0] - 480, p[1] - 540]));
  };
  pad(500, 96); pad(452, 76); pad(408, 54);
  grain(0.1); vignette(0.24);
}

// ============================================================
// D. 铜版蚀刻（v1 通过，加纸纹+收尾）
// ============================================================
function styleEtch() {
  randomSeed(20261073);
  background('#f2ecdd');
  brush.scaleBrushes(2);
  silkPaper(73, [215, 205, 178]);
  brush.add('etch',  { type: 'default', weight: 1.5, scatter: 0.5, sharpness: 0.7, grain: 12, opacity: 230, spacing: 0.08, pressure: [1, 1] });
  brush.add('etch2', { type: 'default', weight: 2.4, scatter: 0.7, sharpness: 0.6, grain: 11, opacity: 245, spacing: 0.08, pressure: [1.1, 0.9] });
  const INK = '#3a3226';
  const SUN = { x: 690, y: 235, r: 82 };
  const FAR  = [[0, 640], [110, 472], [235, 532], [390, 418], [540, 505], [660, 455], [820, 522], [960, 468]];
  const NEAR = [[0, 642], [70, 566], [205, 612], [330, 538], [470, 618], [640, 582], [810, 626], [960, 588]];
  const BIRDS = [[408, 178, 26], [468, 224, 20], [534, 166, 24]];
  const closePoly = (pts, baseY) => pts.concat([[960, baseY], [0, baseY]]);
  const ridgeTopAt = (pts, x) => {
    for (let i = 0; i < pts.length - 1; i++) {
      const a = pts[i], b = pts[i + 1];
      if (x >= a[0] && x <= b[0]) return a[1] + (b[1] - a[1]) * ((x - a[0]) / (b[0] - a[0] || 1));
    }
    return pts[pts.length - 1][1];
  };
  const pipE = (x, y, poly) => pip(x, y, poly);

  const rngS = mulberry32(191);
  for (let y = 70; y < 560; y += 15) {
    const density = (y - 40) / 560;
    const segs = 2 + Math.floor(density * 4);
    for (let s = 0; s < segs; s++) {
      const x0 = rngS() * 960, len = 60 + rngS() * 160 * density + 40;
      brush.set('etch', INK, 0.8 + density * 0.5);
      brush.line(x0 - 480, y - 540, Math.min(960, x0 + len) - 480, y - 540 + (rngS() - 0.5) * 3);
    }
  }
  brush.set('etch2', INK, 1); brush.noFill();
  brush.circle(SUN.x - 480, SUN.y - 540, SUN.r * 0.8, 0.15);
  const rngR = mulberry32(193);
  for (let i = 0; i < 16; i++) {
    const a = (i / 16) * Math.PI * 2;
    const r0 = SUN.r * 0.95, r1 = r0 + 14 + rngR() * 18;
    brush.line(SUN.x - 480 + Math.cos(a) * r0, SUN.y - 540 + Math.sin(a) * r0,
               SUN.x - 480 + Math.cos(a) * r1, SUN.y - 540 + Math.sin(a) * r1);
  }
  const hatchPoly = (poly, count, ang, seed, weight) => {
    const rng = mulberry32(seed);
    brush.set('etch', INK, weight);
    let n = 0, g = 0;
    while (n < count && g < count * 8) {
      g++;
      const x = rng() * 960;
      const yTop = ridgeTopAt(poly, x), y = yTop + rng() * Math.max(8, 645 - yTop);
      if (!pipE(x, y, closePoly(poly, 650))) continue;
      const len = 14 + rng() * 18;
      brush.line(x - 480, y - 540, x - 480 + Math.cos(ang) * len, y - 540 + Math.sin(ang) * len);
      n++;
    }
  };
  hatchPoly(FAR, 170, -0.12, 197, 0.8);
  hatchPoly(NEAR, 190, -0.55, 199, 1.1);
  brush.set('etch2', INK, 1); brush.noFill();
  brush.polygon(closePoly(NEAR, 650).map(p => [p[0] - 480, p[1] - 540]));
  hatchPoly(NEAR, 70, 0.5, 211, 0.7);
  const rngG = mulberry32(217);
  for (let y = 668; y < 1050; y += 20) {
    let x = 15 + rngG() * 60;
    while (x < 930) {
      const len = 34 + rngG() * 60;
      brush.set('etch', INK, 0.9);
      brush.line(x - 480, y - 540, x - 480 + len, y - 540 + (rngG() - 0.5) * 5);
      x += len + 10 + rngG() * 40;
    }
  }
  brush.set('etch2', INK, 1);
  brush.spline([[172 - 480, 665 - 540, 1.5], [162 - 480, 570 - 540, 1.15], [148 - 480, 480 - 540, 0.85]], 0.5);
  brush.spline([[176 - 480, 665 - 540, 1.5], [166 - 480, 570 - 540, 1.15], [152 - 480, 480 - 540, 0.85]], 0.5);
  const rngC = mulberry32(223);
  const CAN = [[150, 440, 72], [215, 468, 56], [105, 478, 52], [168, 515, 58], [192, 412, 46]];
  for (let i = 0; i < 90; i++) {
    const circ = CAN[i % CAN.length];
    const a = rngC() * Math.PI * 2, rr = Math.sqrt(rngC()) * circ[2] * 0.9;
    const x = circ[0] + Math.cos(a) * rr, y = circ[1] + Math.sin(a) * rr;
    brush.set('etch', INK, 0.9);
    const dl = 6 + rngC() * 9;
    brush.line(x - 480, y - 540, x - 480 + Math.cos(-0.4) * dl, y - 540 + Math.sin(-0.4) * dl);
  }
  brush.set('etch2', INK, 0.9);
  for (const [bx, by, s] of BIRDS) {
    brush.spline([[bx - s - 480, by + 2 - 540, 1], [bx - s / 2 - 480, by - s * 0.5 - 540, 1.2], [bx - 480, by + 1 - 540, 1]], 0.6);
    brush.spline([[bx - 480, by + 1 - 540, 1], [bx + s / 2 - 480, by - s * 0.5 - 540, 1.2], [bx + s - 480, by + 2 - 540, 1]], 0.6);
  }
  grain(0.12); vignette(0.28);
}

// ============================================================
// 派发：?style=vg|ink|ukyo|etch
// ============================================================
const STYLES = { vg: styleVanGogh, ink: styleInk, ukyo: styleUkiyo, etch: styleEtch };
const which = new URLSearchParams(location.search).get('style') || 'vg';

function setup() {
  pixelDensity(1);
  createCanvas(960, 1080, WEBGL);
  try {
    (STYLES[which] || styleVanGogh)();
  } catch (err) {
    document.getElementById('err').textContent = which + ' 渲染出错: ' + err.message;
  }
}
function draw() {}
window.addEventListener('error', e => {
  document.getElementById('err').textContent = 'window错误: ' + e.message;
});
