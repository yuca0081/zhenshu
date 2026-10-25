// ============================================================
// p5.brush 样张：同一套几何，左半裸 Canvas，右半 p5.brush
// 确定性纪律：全图无 Math.random / Date.now，随机一律走种子
//   —— randomSeed 驯服 p5+笔刷，布局抖动走 mulberry32
// ============================================================

// ---------- 种子随机（密码本，不是骰子） ----------
function mulberry32(a) {
  return function () {
    a |= 0; a = (a + 0x6D2B79F5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}
window.addEventListener('error', function (e) {
  document.title = 'JSERR: ' + e.message;
  var ui = document.getElementById('ui').getContext('2d');
  ui.clearRect(0, 0, 1920, 1080);
  ui.fillStyle = '#c0392b';
  ui.font = 'bold 34px "Microsoft YaHei", sans-serif';
  ui.fillText('渲染出错: ' + e.message, 40, 70);
});
// 诊断：右半各段绘制状态上屏（UI 层最顶，不会被盖）
const DIAG = { lines: [] };
function diag(line) { DIAG.lines.push(line); paintDiag(); }
function paintDiag() {
  const fails = DIAG.lines.filter(l => l.startsWith('FAIL'));
  if (!fails.length) return;
  const ctx = document.getElementById('ui').getContext('2d');
  ctx.save();
  ctx.font = '22px monospace';
  ctx.textAlign = 'left'; ctx.textBaseline = 'top';
  ctx.fillStyle = '#c0392b';
  fails.forEach((l, i) => ctx.fillText(l, 990, 120 + i * 26));
  ctx.restore();
}

// ---------- 共享几何（左上角坐标系 0..960 × 0..1080，两个渲染器共用） ----------
const HZ = 640;                      // 地平线
const SUN = { x: 690, y: 235, r: 88 };
const FAR  = [[0, 640], [110, 472], [235, 532], [390, 418], [540, 505], [660, 455], [820, 522], [960, 468]];
const NEAR = [[0, 642], [70, 566], [205, 612], [330, 538], [470, 618], [640, 582], [810, 626], [960, 588]];
const PATH = [[418, 1080], [562, 1080], [520, 700], [505, 640], [478, 640], [470, 700]];
const TREE = { trunk: [[172, 665, 1.5], [162, 570, 1.15], [148, 480, 0.85]], branch: [[162, 570], [196, 505]],
  canopy: [[150, 440, 72], [215, 468, 56], [105, 478, 52], [168, 515, 58], [192, 412, 46]] };
const BIRDS = [[408, 178, 26], [468, 224, 20], [534, 166, 24], [606, 130, 17]];

function closePoly(pts, baseY) { return pts.concat([[960, baseY], [0, baseY]]); }
function ridgeTopAt(pts, x) {   // 折线线性插值
  for (let i = 0; i < pts.length - 1; i++) {
    const a = pts[i], b = pts[i + 1];
    if (x >= a[0] && x <= b[0]) return a[1] + (b[1] - a[1]) * ((x - a[0]) / (b[0] - a[0] || 1));
  }
  return pts[pts.length - 1][1];
}
function pointInPoly(x, y, poly) {
  let inside = false;
  for (let i = 0, j = poly.length - 1; i < poly.length; j = i++) {
    const xi = poly[i][0], yi = poly[i][1], xj = poly[j][0], yj = poly[j][1];
    if (((yi > y) !== (yj > y)) && (x < (xj - xi) * (y - yi) / (yj - yi) + xi)) inside = !inside;
  }
  return inside;
}

// ---------- 调色板 ----------
const P = {
  paper: '#f7f2e7', skyTop: '#cfe3ef', sun: '#f2d24a', sunRim: '#c9861d',
  farR: '#9db1c4', nearR: '#62718c', nearDark: '#4a5670', outline: '#39435c',
  field: '#b7a668', fieldGrass: '#6f7a3f', path: '#e8dcb6',
  trunk: '#6b4a33', leafA: '#5d7a4a', leafB: '#46603a', bird: '#2f2f2f',
  skyBlueA: '#3f6bb5', skyBlueB: '#2f56b3', skyBlueC: '#6aa0d8', gold: '#e0b13e'
};

// ============================================================
// 左半：裸 Canvas 2D（数学完美的线）
// ============================================================
function drawFlat() {
  const ctx = document.getElementById('flat').getContext('2d');
  // 纸底（两半都铺）
  ctx.fillStyle = P.paper; ctx.fillRect(0, 0, 1920, 1080);

  // ---- 左半场景 ----
  // 天空：垂直渐变
  const sky = ctx.createLinearGradient(0, 0, 0, HZ);
  sky.addColorStop(0, P.skyTop); sky.addColorStop(1, P.paper);
  ctx.fillStyle = sky; ctx.fillRect(0, 0, 960, HZ);

  // 太阳
  ctx.beginPath(); ctx.arc(SUN.x, SUN.y, SUN.r, 0, Math.PI * 2);
  ctx.fillStyle = P.sun; ctx.fill();
  ctx.lineWidth = 3; ctx.strokeStyle = P.sunRim; ctx.stroke();

  // 山
  const fillRidge = (pts, color) => {
    ctx.beginPath(); ctx.moveTo(pts[0][0], pts[0][1]);
    for (const p of pts) ctx.lineTo(p[0], p[1]);
    ctx.lineTo(960, 655); ctx.lineTo(0, 655); ctx.closePath();
    ctx.fillStyle = color; ctx.fill();
    ctx.lineWidth = 2; ctx.strokeStyle = '#00000022'; ctx.stroke();
  };
  fillRidge(FAR, P.farR); fillRidge(NEAR, P.nearR);

  // 田野 + 小路
  ctx.fillStyle = P.field; ctx.fillRect(0, HZ, 960, 1080 - HZ);
  ctx.beginPath(); ctx.moveTo(PATH[0][0], PATH[0][1]);
  for (const p of PATH) ctx.lineTo(p[0], p[1]);
  ctx.closePath(); ctx.fillStyle = P.path; ctx.fill();

  // 树
  ctx.strokeStyle = P.trunk; ctx.lineWidth = 14; ctx.lineCap = 'round';
  ctx.beginPath(); ctx.moveTo(172, 665); ctx.quadraticCurveTo(162, 570, 148, 480); ctx.stroke();
  ctx.lineWidth = 8; ctx.beginPath(); ctx.moveTo(162, 570); ctx.lineTo(196, 505); ctx.stroke();
  const leaf = (x, y, r, c) => { ctx.beginPath(); ctx.arc(x, y, r, 0, Math.PI * 2); ctx.fillStyle = c; ctx.fill(); };
  leaf(150, 440, 72, P.leafA); leaf(215, 468, 56, P.leafA); leaf(105, 478, 52, P.leafB);
  leaf(168, 515, 58, P.leafB); leaf(192, 412, 46, P.leafB);

  // 鸟（两段圆弧）
  ctx.strokeStyle = P.bird; ctx.lineWidth = 2.5;
  for (const [bx, by, s] of BIRDS) {
    ctx.beginPath(); ctx.arc(bx - s / 2, by, s / 2, Math.PI * 1.15, Math.PI * 1.85); ctx.stroke();
    ctx.beginPath(); ctx.arc(bx + s / 2, by, s / 2, Math.PI * 1.15, Math.PI * 1.85); ctx.stroke();
  }
}

// ============================================================
// 右半：p5.brush（手绘质感的线）
// ============================================================
const T = (pts) => pts.map(p => [p[0] - 480, p[1] - 540]);  // 左上坐标 → p5 WEBGL 中心坐标

function drawBrushScene() {
  const section = (name, fn) => {
    try { fn(); diag('OK   ' + name); }
    catch (err) { diag('FAIL ' + name + ': ' + err.message); }
  };

  section('setup-brushes', () => {
    randomSeed(20261024);   // 一颗种子同时驯服 p5 与笔刷
    brush.scaleBrushes(2);  // 先缩放内置笔，再注册自定义笔

    // ---- 自定义笔（笔工厂：参数即手感；pressure 是隐性必填——
    //      2.2.3 的 add() 不填 pressure 时渲染期读 pressure.type 直接炸）----
    brush.add('vg',      { type: 'marker', weight: 12, scatter: 1.1, opacity: 170, spacing: 0.35, rotate: 'natural', pressure: [1.1, 0.9] });
    brush.add('goldst',  { type: 'marker', weight: 9,  scatter: 0.9, opacity: 200, spacing: 0.4,  rotate: 'natural', pressure: [1, 0.85] });
    brush.add('inkline', { type: 'default', weight: 2, scatter: 1.5, sharpness: 0.55, grain: 9, opacity: 210, spacing: 0.12, pressure: [1.2, 0.8] });
    brush.add('penline', { type: 'default', weight: 2.5, scatter: 0.25, sharpness: 0.8, grain: 14, opacity: 245, spacing: 0.1, pressure: [1.1, 0.9] });
    // 树改走 default 型笔（marker 型在该区域静默不渲染，实测放弃）
    brush.add('chalkb',  { type: 'default', weight: 7, scatter: 2.2, sharpness: 0.35, grain: 8, opacity: 235, spacing: 0.12, pressure: [1.3, 0.7] });
    brush.add('leafb',   { type: 'default', weight: 5, scatter: 2.5, sharpness: 0.3, grain: 10, opacity: 175, spacing: 0.15, pressure: [1, 1] });
  });

  section('setup-field', () => {
    // ---- 自定义漩涡流场：绕太阳转（单吸引子版官方 spiral 数学）----
    const W = 960, H = 1080, res = W * 0.01;
    const sunC = (SUN.x - 480 + W) / res, sunR = (SUN.y - 540 + H) / res;
    brush.addField('swirl', function (_t, field) {
      const att = [{ c: sunC, r: sunR, dir: 1, off: 72, w: 1 },
                   { c: 40, r: 60, dir: 1, off: 70, w: 0.3 }];
      for (let c = 0; c < field.length; c++) {
        for (let r = 0; r < field[0].length; r++) {
          let wx = 0, wy = 0;
          for (const a of att) {
            const dx = c - a.c, dy = r - a.r, wgt = a.w / (dx * dx + dy * dy + 1);
            const ang = (a.dir * (Math.atan2(dy, dx) * 180 / Math.PI + a.off)) * Math.PI / 180;
            wx += wgt * Math.cos(ang); wy += wgt * Math.sin(ang);
          }
          field[c][r] = Math.atan2(wy, wx) * 180 / Math.PI;
        }
      }
      return field;
    });
  });

  section('sky', () => {
    // ---- 天空：流场笔触（只许画在天区）----
    brush.field('swirl');
    brush.clip([-478, -538, 478, -135]);   // 只许画在山脊以上，防笔触压山
    const rngS = mulberry32(11);
    const blues = [P.skyBlueA, P.skyBlueB, P.skyBlueC];
    for (let i = 0; i < 95; i++) {
      const x = -455 + rngS() * 910, y = -505 + rngS() * 580;
      const d = Math.hypot((x + 480) - SUN.x, (y + 540) - SUN.y);
      if (d < 235) { brush.set('goldst', P.gold, 1); }
      else { brush.set('vg', blues[i % 3], 1); }
      brush.flowLine(x, y, 90 + rngS() * 140, 0);
    }
    brush.noClip();
    brush.noField();   // 流场只管天空！否则样条会被涡流吹跑（树干事故根因）
  });

  section('sun', () => {
    // ---- 太阳：双层水彩填充 + 手绘圆环 ----
    brush.noStroke(); brush.noWash();
    brush.fill(P.sun, 242); brush.fillBleed(0.25, 'out');
    brush.circle(SUN.x - 480, SUN.y - 540, SUN.r * 0.95, 0.25);
    brush.fill('#f6df7a', 200);
    brush.circle(SUN.x - 480, SUN.y - 540, SUN.r * 0.55, 0.2);
    brush.noFill();
    brush.set('penline', P.sunRim, 1);
    brush.circle(SUN.x - 480, SUN.y - 540, SUN.r, 0.2);
  });

  section('mountains', () => {
    // ---- 山：平涂 + 勾线 + 排线阴影 ----
    brush.wash(P.farR, 255); brush.polygon(T(closePoly(FAR, 655)));
    brush.wash(P.nearR, 255); brush.polygon(T(closePoly(NEAR, 655)));
    brush.noWash(); brush.noFill();
    brush.set('inkline', P.outline, 1);
    brush.polygon(T(closePoly(NEAR, 655)));
    // 手工排线：近山阴影（确定性撒点 + 点在多边形判定）
    brush.set('inkline', P.nearDark, 1);
    const rngH = mulberry32(23);
    let ticks = 0;
    while (ticks < 60) {
      const x = rngH() * 960;
      const top = ridgeTopAt(NEAR, x);
      const y = top + 12 + rngH() * Math.max(8, (HZ - 8) - top);
      if (!pointInPoly(x, y, closePoly(NEAR, 655))) continue;
      const len = 13 + rngH() * 13, ang = -0.5;
      brush.line(x - 480, y - 540, x - 480 + Math.cos(ang) * len, y - 540 + Math.sin(ang) * len);
      ticks++;
    }
  });

  section('ground', () => {
    // ---- 田野 + 小路 + 草 ----
    brush.noStroke(); brush.wash(P.field, 255);
    brush.polygon(T([[0, 640], [960, 640], [960, 1080], [0, 1080]]));
    brush.wash(P.path, 255); brush.polygon(T(PATH));
    brush.noWash();
    brush.set('2B', P.fieldGrass, 2.2);
    const rngG = mulberry32(37);
    for (let i = 0; i < 110; i++) {
      const x = 25 + rngG() * 910, y = 658 + rngG() * 385;
      const len = 11 + rngG() * 11, sway = (rngG() - 0.5) * 9;
      brush.line(x - 480, y - 540, x - 480 + sway, y - 540 - len);
    }
  });

  section('tree', () => {
    // ---- 树：三条错位重笔样条叠出树干 + 软铅笔短笔触点簇树冠 ----
    brush.set('chalkb', P.trunk, 1);
    brush.spline(T(TREE.trunk), 0.5);
    brush.spline(T(TREE.trunk.map(p => [p[0] + 5, p[1] - 2, p[2]])), 0.5);
    brush.spline(T(TREE.trunk.map(p => [p[0] - 4, p[1] + 1, p[2] * 0.9])), 0.5);
    brush.set('chalkb', P.trunk, 1);
    brush.spline(T([[162, 570, 1], [196, 505, 0.7]]), 0.4);
    const rngC = mulberry32(51), rngC2 = mulberry32(53);
    let dabs = 0;
    while (dabs < 64) {
      const circ = TREE.canopy[dabs % TREE.canopy.length];
      const a = rngC() * Math.PI * 2, rr = Math.sqrt(rngC()) * circ[2] * 0.85;
      const x = circ[0] + Math.cos(a) * rr, y = circ[1] + Math.sin(a) * rr;
      brush.set('leafb', dabs % 3 === 0 ? P.leafB : P.leafA, 1);
      const dl = 6 + rngC2() * 8, da = rngC2() * Math.PI;
      brush.line(x - 480, y - 540, x - 480 + Math.cos(da) * dl, y - 540 + Math.sin(da) * dl);
      dabs++;
    }
  });

  section('birds', () => {
    // ---- 鸟：钢笔小弧 ----
    brush.set('penline', P.bird, 0.9);
    for (const [bx, by, s] of BIRDS) {
      brush.spline(T([[bx - s, by + 2, 0.8], [bx - s / 2, by - s * 0.55, 1], [bx, by + 1, 0.8]]), 0.6);
      brush.spline(T([[bx, by + 1, 0.8], [bx + s / 2, by - s * 0.55, 1], [bx + s, by + 2, 0.8]]), 0.6);
    }
  });

  diag('ALL-DONE');
}

// ============================================================
// p5 全局模式：WEBGL 画布（p5.brush 硬性要求），只画右半
// ============================================================
let painted = false;
function setup() {
  pixelDensity(1);
  const cv = createCanvas(960, 1080, WEBGL);
  cv.elt.id = 'p5c';
}
function draw() {
  if (frameCount === 1 && !painted) { painted = true; drawBrushScene(); }
  if (frameCount >= 3) document.title = 'RENDER-OK';
  // 不 noLoop：virtual-time 预算负责收尾，空帧开销可忽略
}

// ============================================================
// 顶层标注层：标签 / 分割线 / 颗粒 / 暗角
// ============================================================
function drawUI() {
  const ctx = document.getElementById('ui').getContext('2d');
  ctx.clearRect(0, 0, 1920, 1080);

  // 标签牌
  const tag = (text, cx) => {
    ctx.font = 'bold 46px "Microsoft YaHei", sans-serif';
    const w = ctx.measureText(text).width;
    ctx.fillStyle = 'rgba(247,242,231,0.88)';
    ctx.fillRect(cx - w / 2 - 26, 26, w + 52, 74);
    ctx.strokeStyle = 'rgba(57,67,92,0.35)'; ctx.lineWidth = 2;
    ctx.strokeRect(cx - w / 2 - 26, 26, w + 52, 74);
    ctx.fillStyle = '#39435c'; ctx.textAlign = 'center'; ctx.textBaseline = 'middle';
    ctx.fillText(text, cx, 65);
  };
  tag('裸 Canvas', 480);
  tag('p5.brush', 1440);

  // 右半暗角（胶片收尾）
  const vg = ctx.createRadialGradient(1440, 540, 420, 1440, 540, 1150);
  vg.addColorStop(0, 'rgba(40,40,60,0)');
  vg.addColorStop(1, 'rgba(40,40,60,0.14)');
  ctx.fillStyle = vg; ctx.fillRect(960, 0, 960, 1080);

  // 右半胶片颗粒（种子随机小点）
  const rngN = mulberry32(97);
  ctx.fillStyle = 'rgba(30,30,40,0.05)';
  for (let i = 0; i < 1400; i++) {
    ctx.fillRect(960 + rngN() * 960, rngN() * 1080, 1.6, 1.6);
  }

  // 中缝与页脚
  ctx.fillStyle = 'rgba(57,67,92,0.5)'; ctx.fillRect(958, 0, 4, 1080);
  ctx.fillStyle = 'rgba(247,242,231,0.9)'; ctx.fillRect(0, 1010, 1920, 70);
  ctx.fillStyle = '#39435c'; ctx.textAlign = 'center'; ctx.textBaseline = 'middle';
  ctx.font = '26px "Microsoft YaHei", sans-serif';
  ctx.fillText('同一套几何 · 左：裸 Canvas（完美直线） 右：p5.brush（流场+水彩+排线+炭笔） · 种子 20261024 可复现', 960, 1046);
}

drawFlat();
drawUI();
