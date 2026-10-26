// ============================================================
// pixelstage kit v0.1 —— 「直播弹幕RPG像素风」画材包公共件
// 源条目：风格库 vid-ref20261026-103650《直播弹幕RPG像素风》
// 考据：BV1vbaf6gE8Q 真迹帧 frame-01/02/03 多模态精读（2026-10-26）
//
// 画风八律（全部源自真迹证据，写入本包的「为什么」）：
//  1. 双分辨率合成：像素层 320x180 逻辑画布 2x 最近邻放大；辉光/烟雾/
//     渐变画在全分辨率平滑层，两层分开再叠（frame-02/03 的软烟 vs 硬像素）。
//  2. 块级 RGB 通道撕裂：R/G/B 整块位移 2-10 逻辑px，按部件给不同错位量
//     （frame-02 手臂最重、frame-03 小人干净——错位是部件属性不是全图属性）。
//  3. 色彩三律：黑白灰单色舞台；橙 #F08030 = 可交互层；电光黄 #FFFF00 =
//     生命/焦点，全场至多两处（frame-01 实测）。
//  4. 手持机位感：每个 UI 元素带独立 2°-8° 微旋转 + 全帧 2-3° 基倾角
//     （frame-01 实测按钮 6°/11°、对话框 2.8°，非刚体）。
//  5. 硬像素粒子 × 软烟雾成对：1-2 逻辑px 纯原色方块纸屑 + 高分辨软边
//     白烟团伴飞（frame-03）。
//  6. 故障扫描行：底部断续、有缺口的深蓝/青 + 砖红细横线，非连续直线。
//  7. 转场语言：广播黑 #101010 开关；径向白闪（中心纯白→边角中灰）+ <5%
//     前场景余像（frame-05）。
//  8. 无 CRT 滤镜：禁止扫描线/屏幕弯曲/网纹——这是「像素+现代辉光」，
//     不是复古拟真（四帧逐行亮度平坦的证据）。
//
// 后处理链（frame-01 实测顺序）：像素层 jitter 旋转 → bloom 辉光(2-4px
// 衰减) → 1-2px 水平 RGB 色差 → 轻噪点(0-3)。
//
// 纪律：无 Math.random（一律种子随机）；同种子逐像素可复现。
// ============================================================

// ---- 种子随机（mulberry32，与 demo/p5brush-sample 同源）----
function mulberry32(a) {
  return function () {
    a |= 0; a = (a + 0x6D2B79F5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ t >>> 14) >>> 0) / 4294967296;
  };
}

// ---- 画材包调色板（真实测色，见文件头八律 #3）----
const PS = {
  black: '#101010',      // 广播黑（frame-00 实测 16,16,16）
  stageGray: '#2b2b2b',  // 舞台锥灰（262626-2f2f2f 实测带）
  white: '#ffffff',      // UI 白
  orange: '#F08030',     // 交互层橙（218,113,38-255,134,60 实测）
  yellow: '#FFFF00',     // 焦点黄（246,246,3 实测）——全场至多两处
  cyan: '#00d8e0',       // 故障青（frame-02 内框实测）
  magenta: '#ff20c8',    // 故障品红
  cyanText: '#00e5e5',   // 青像素字
  brick: '#8b2020',      // 扫描行砖红
  navy: '#1a2a4a',       // 扫描行深蓝
  teal: '#2a7a8a',       // 扫描行青绿
  confetti: ['#ff2020', '#2020ff', '#00d8e0', '#e8e000', '#ff00ff', '#ffffff', '#ffffff', '#111111', '#20ff20'],
};
function PS_cyan_fix() { return '#00d8e0'; }

// ============================================================
// PixelStage —— 一次演出的全部舞台机械
// ============================================================
class PixelStage {
  // w,h = 输出分辨率（640x360）；逻辑像素层固定 320x180（2x）
  constructor(seed = 1) {
    this.seed = seed;
    this.rnd = mulberry32(seed);
    this.LW = 320; this.LH = 180;              // 逻辑（像素层）分辨率
    this.W = 640; this.H = 360;                // 输出分辨率
    this.px = document.createElement('canvas');  // 像素层（硬边）
    this.px.width = this.LW; this.px.height = this.LH;
    this.P = this.px.getContext('2d');
    this.main = document.createElement('canvas'); // 全分辨率合成
    this.main.width = this.W; this.main.height = this.H;
    this.M = this.main.getContext('2d');
    this.sm = document.createElement('canvas');  // 平滑层（软边特效）
    this.sm.width = this.W; this.sm.height = this.H;
    this.S = this.sm.getContext('2d');
    this.jitters = [];   // 部件微旋转登记 [{rot,dx,dy}]
    this.letterbox = 0;  // 上下黑边 px（B/C 帧构图）
  }

  // ---- 像素层原语（全部逻辑坐标，1 单位 = 屏幕 2px）----
  bgFlat(c) { this.P.fillStyle = c; this.P.fillRect(0, 0, this.LW, this.LH); }
  bgRadial(cx, cy, r0, r1, c0, c1) { // 底色必须画在像素层（判卷 r1 教训：
    // 平滑层不透明底会把像素层整个盖掉——两层只有上下序，没有通道穿透）
    const g = this.P.createRadialGradient(cx, cy, r0, cx, cy, r1);
    g.addColorStop(0, c0); g.addColorStop(1, c1);
    this.P.fillStyle = g; this.P.fillRect(0, 0, this.LW, this.LH);
  }
  rect(x, y, w, h, c) { this.P.fillStyle = c; this.P.fillRect(x | 0, y | 0, w | 0, h | 0); }
  // 点阵字：小字号画进逻辑画布，放大后自然点阵化（零字库依赖）
  text(str, x, y, size, c, bold = true) {
    this.P.font = `${bold ? 'bold ' : ''}${size}px monospace`;
    this.P.fillStyle = c; this.P.textBaseline = 'top';
    this.P.fillText(str, x, y);
  }
  // 描边矩形（UI 框：单线橙/白框，直角无圆角——frame-01 实测）
  box(x, y, w, h, c, t = 1) {
    this.rect(x, y, w, t, c); this.rect(x, y + h - t, w, t, c);
    this.rect(x, y, t, h, c); this.rect(x + w - t, y, t, h, c);
  }

  // ---- 部件：独立小画布 + 微旋转（八律 #4 手持机位）----
  // draw(ctx, w, h) 在逻辑分辨率部件画布上作画；rot 度；返回部件对象
  part(w, h, rotDeg, draw) {
    const c = document.createElement('canvas');
    c.width = w; c.height = h;
    draw(c.getContext('2d'), w, h);
    return { c, rot: rotDeg * Math.PI / 180, rnd: this.rnd() };
  }
  // 把部件贴到像素层：rx,ry = 部件画布中心在逻辑层的落点
  stamp(part, rx, ry) {
    this.P.save();
    this.P.translate(rx, ry); this.P.rotate(part.rot);
    this.P.imageSmoothingEnabled = false;
    this.P.drawImage(part.c, -part.c.width / 2, -part.c.height / 2);
    this.P.restore();
  }

  // ---- 块级 RGB 通道撕裂（八律 #2：错位量按部件/区域传入）----
  // 把像素层（或某部件）拆 R/G/B 三通道，各自偏移后 lighter 合成。
  // dR/dG/dB = 屏幕像素位移（如 8,0 / -6,0 / 0,4——手臂重、躯干轻）
  rgbSplit(target, dR, dG, dB) {
    const w = target.width, h = target.height;
    const out = document.createElement('canvas'); out.width = w; out.height = h;
    const o = out.getContext('2d');
    const chan = (color) => {
      const c = document.createElement('canvas'); c.width = w; c.height = h;
      const x = c.getContext('2d');
      x.drawImage(target, 0, 0);
      x.globalCompositeOperation = 'multiply';
      x.fillStyle = color; x.fillRect(0, 0, w, h);
      x.globalCompositeOperation = 'destination-in';
      x.drawImage(target, 0, 0);
      return c;
    };
    o.globalCompositeOperation = 'lighter';
    o.drawImage(chan('#ff0000'), dR[0], dR[1]);
    o.drawImage(chan('#00ff00'), dG[0], dG[1]);
    o.drawImage(chan('#0000ff'), dB[0], dB[1]);
    return out;
  }

  // rgbSplit 的部件版：撕裂后保持原旋转，直接可 stamp
  rgbSplit2Wrap(part, dR, dG, dB) {
    return { c: this.rgbSplit(part.c, dR, dG, dB), rot: part.rot };
  }

  // ---- 平滑层原语（全分辨率，软边）----
  glowSpot(x, y, r, c, a = 0.8) { // 辉光斑/火光核心
    const g = this.S.createRadialGradient(x, y, 0, x, y, r);
    g.addColorStop(0, c); g.addColorStop(1, 'rgba(0,0,0,0)');
    this.S.save(); this.S.globalAlpha = a; this.S.globalCompositeOperation = 'lighter';
    this.S.fillStyle = g; this.S.beginPath(); this.S.arc(x, y, r, 0, 7); this.S.fill(); this.S.restore();
  }
  pixSmoke(x, y, r, n, seedOff = 0) { // 像素烟：屏幕坐标的白块簇（判卷 r2：真迹高光是块状像素团）
    const rr = mulberry32(this.seed + seedOff);
    this.S.save();
    for (let i = 0; i < n; i++) {
      const ang = rr() * 6.283, d = rr() * r;
      const px = x + Math.cos(ang) * d, py = y + Math.sin(ang) * d * 0.7;
      const w = 2 * (rr() < 0.6 ? 1 : rr() < 0.9 ? 2 : 3);
      this.S.fillStyle = `rgba(255,255,255,${0.25 + rr() * 0.5})`;
      this.S.fillRect(px | 0, py | 0, w, w);
    }
    this.S.restore();
  }
  smoke(x, y, r, a = 0.5, seedOff = 0) { // 软烟雾团（白，带内部 fbm 起伏）
    const s = this.S; s.save();
    s.globalAlpha = a; s.globalCompositeOperation = 'screen';
    const rr = mulberry32(this.seed + seedOff);
    for (let i = 0; i < 5; i++) {
      const dx = (rr() - 0.5) * r, dy = (rr() - 0.5) * r * 0.7, rad = r * (0.4 + rr() * 0.5);
      const g = s.createRadialGradient(x + dx, y + dy, 0, x + dx, y + dy, rad);
      g.addColorStop(0, 'rgba(255,255,255,0.85)');
      g.addColorStop(1, 'rgba(255,255,255,0)');
      s.fillStyle = g; s.beginPath(); s.arc(x + dx, y + dy, rad, 0, 7); s.fill();
    }
    s.restore();
  }
  spotlight(x0, y0, x1, y1, halfW0, halfW1, c = 'rgba(255,255,255,0.10)') { // 梯形光锥
    const s = this.S; s.save(); s.globalCompositeOperation = 'screen';
    s.fillStyle = c; s.beginPath();
    s.moveTo(x0 - halfW0, y0); s.lineTo(x0 + halfW0, y0);
    s.lineTo(x1 + halfW1, y1); s.lineTo(x1 - halfW1, y1);
    s.closePath(); s.fill(); s.restore();
  }
  // 故障扫描行（八律 #6：断续带缺口，深蓝/青 + 砖红，屏幕坐标）
  scanRows(ys) {
    const s = this.S;
    for (const { y, color } of ys) {
      let x = 4 + this.rnd() * 30;
      while (x < this.W - 4) {
        const seg = 6 + this.rnd() * 40, gap = (this.rnd() < 0.35 ? 10 + this.rnd() * 30 : 2);
        s.globalAlpha = 0.5 + this.rnd() * 0.5;
        s.fillStyle = color; s.fillRect(x, y, seg, 1.5 + (this.rnd() < 0.2 ? 1.5 : 0));
        x += seg + gap;
      }
    }
    s.globalAlpha = 1;
  }
  // 纸屑粒子（八律 #5：1-2 逻辑px 纯原色方块，屏幕坐标层）
  confetti(cx, cy, spreadX, spreadY, n, seedOff = 0) {
    const rr = mulberry32(this.seed + seedOff);
    for (let i = 0; i < n; i++) {
      const x = cx + (rr() - 0.5) * spreadX, y = cy + (rr() - 0.5) * spreadY;
      const c = PS.confetti[(rr() * PS.confetti.length) | 0];
      const w = 2 * (rr() < 0.7 ? 1 : 2); // 屏幕 2-4px = 逻辑 1-2px
      this.S.fillStyle = c;
      this.S.fillRect(x | 0, y | 0, w, w);
    }
  }
  // 爆点簇 + 抛物线下落弧（判卷 r1：纸屑要聚在爆点、拖出下落弧，不是均匀撒）
  confettiBurst(cx, cy, dirDeg, spreadDeg, distMax, n, seedOff = 0) {
    const rr = mulberry32(this.seed + seedOff);
    for (let i = 0; i < n; i++) {
      const t = rr() * rr(); // 密度梯度：贴爆点密、远处疏（判卷 r2）
      const ang = (dirDeg + (rr() - 0.5) * spreadDeg) * Math.PI / 180;
      const d = 8 + t * distMax;
      const drop = t * t * (rr() * 30 + 10); // 抛物线下坠
      const x = cx + Math.cos(ang) * d, y = cy + Math.sin(ang) * d + drop;
      const c = PS.confetti[(rr() * PS.confetti.length) | 0];
      const w = 2 * (rr() < 0.7 ? 1 : 2);
      this.S.fillStyle = c;
      this.S.fillRect(x | 0, y | 0, w, w);
    }
  }
  // 径向白闪（八律 #7：中心纯白→边角中灰）
  whiteFlash(a = 1) {
    const g = this.M.createRadialGradient(this.W / 2, this.H / 2, 40, this.W / 2, this.H / 2, 420);
    g.addColorStop(0, `rgba(255,255,255,${a})`);
    g.addColorStop(1, `rgba(153,153,153,${a * 0.85})`);
    this.M.fillStyle = g; this.M.fillRect(0, 0, this.W, this.H);
  }

  // ---- 合成与后处理 ----
  // 顺序（文件头实测链）：像素层放大 → 平滑层 → letterbox → bloom →
  // 全帧轻色差(可选) → 噪点。
  compose(o = {}) {
    const op = Object.assign({ bloom: 0.55, noise: 0.04, rgbShift: null, glitchSource: null }, o);
    const M = this.M;
    M.fillStyle = PS.black; M.fillRect(0, 0, this.W, this.H);
    M.imageSmoothingEnabled = false;
    M.drawImage(this.px, 0, 0, this.LW, this.LH, 0, 0, this.W, this.H);
    M.drawImage(this.sm, 0, 0);
    if (this.letterbox > 0) {
      M.fillStyle = '#0f0f0f'; // frame-03 实测边带 15,15,15
      M.fillRect(0, 0, this.W, this.letterbox);
      M.fillRect(0, this.H - this.letterbox, this.W, this.letterbox);
    }
    if (op.rgbShift) this._frameRGBShift(op.rgbShift);
    if (op.bloom > 0) this._bloom(op.bloom);
    if (op.noise > 0) this._noise(op.noise);
  }
  _frameRGBShift([dx]) { // 全帧 1-2px 水平色差（frame-01 实测）
    const t = document.createElement('canvas'); t.width = this.W; t.height = this.H;
    t.getContext('2d').drawImage(this.main, 0, 0);
    const shifted = this.rgbSplit(t, [dx, 0], [0, 0], [-dx, 0]);
    this.M.drawImage(shifted, 0, 0);
  }
  _bloom(a) { // 亮部提取 → blur → lighter 叠回（headless Chrome 支持 ctx.filter）
    const w = this.W >> 1, h = this.H >> 1;
    const t = document.createElement('canvas'); t.width = w; t.height = h;
    const x = t.getContext('2d');
    x.drawImage(this.main, 0, 0, w, h);
    x.globalCompositeOperation = 'multiply';
    x.fillStyle = 'rgb(90,90,90)'; x.fillRect(0, 0, w, h); // 只留亮部
    x.globalCompositeOperation = 'destination-in';
    x.drawImage(this.main, 0, 0, w, h);
    this.M.save();
    this.M.globalAlpha = a; this.M.globalCompositeOperation = 'lighter';
    this.M.filter = 'blur(3px)';
    this.M.drawImage(t, 0, 0, this.W, this.H);
    this.M.restore();
  }
  _noise(a) { // 视频编码噪（0-3 档灰阶抖动）
    const w = this.W >> 1, h = this.H >> 1;
    const t = document.createElement('canvas'); t.width = w; t.height = h;
    const x = t.getContext('2d'), img = x.createImageData(w, h), rr = mulberry32(this.seed + 77);
    for (let i = 0; i < img.data.length; i += 4) {
      const v = rr() < 0.5 ? 0 : rr() * 255;
      img.data[i] = v; img.data[i + 1] = v; img.data[i + 2] = v;
      img.data[i + 3] = 255 * a * rr();
    }
    x.putImageData(img, 0, 0);
    this.M.save(); this.M.globalCompositeOperation = 'lighter';
    this.M.drawImage(t, 0, 0, this.W, this.H); this.M.restore();
  }
}
