# pixelstage 迷你手册（画面工位注入用：小抄正文 + 核心源码段，~4.5k token）

零依赖纯 Canvas2D（像素风的笔触单位是格子，不要引入笔刷库）。下面源码段是唯一真源：
整段照抄进你的自包含文件、在此之上写场景（这正是标准工作方式）；语义不得偏离。

## 画风八律（违反即走样，逐条有真迹证据）
1. 双分辨率：像素画一律画**像素层**（逻辑 320x180，`st.P`/rect/text/box/part+stamp），自动 2x 硬放大；辉光/渐变/烟雾画**平滑层**（全分辨率 `st.S`/glowSpot/spotlight）。
2. **底色必须画像素层**（bgFlat/bgRadial）——平滑层放不透明底会把像素层整个盖掉（两层只有上下序，没有通道穿透）。
3. 色彩三律：黑白灰舞台；橙 `#F08030`=可交互；电光黄 `#FFFF00`=生命/焦点，**全场至多两处**。取色用 PS 对象。
4. RGB 撕裂是**爆点专属特效**，不可下放为元素填色；主体一律白色单线稿。撕裂按部件给错位量（躯干轻、手臂/爆发重）。
5. 手持机位：每个 UI 元素 `part(w,h,rotDeg,draw)` 独立 2°-8° 微旋转再 stamp，禁止刚体对齐。
6. 粒子=4-10px 多色像素团（红蓝为主黑白夹杂），`confettiBurst` 自带从爆点向外的密度梯度；均匀撒点会走样。高光/烟=块状像素团，不是 bokeh 模糊光斑。
7. **无 CRT 滤镜**：禁扫描线/网纹/暗角拟真。故障感只来自 rgbSplit 和 scanRows（断续横线，深蓝 #1a2a4a/青绿 #2a7a8a/砖红 #8b2020）。
8. 转场：黑帧 `#101010`/径向白闪 whiteFlash。文字用 `st.text`（小字号进逻辑层自动点阵化）。

## 后处理参数带（收尾必走 compose，一次收口）
战斗UI `{bloom:0.3, rgbShift:[0.5], noise:0.005}`；白底特写 `{bloom:0.35, rgbShift:null, noise:0.03}`；
暗场 `{bloom:0.6, rgbShift:null, noise:0.008}`；换题图轻参数可 `{bloom:0.35, rgbShift:[0.5], noise:0.005}`。

## 常用配方
- 战斗UI：bgFlat('#000000') + P层锥光(alpha 0.16) + part 对话框(白4px边+黑内) + 橙框按钮(part+微旋转) + HP行(黄方块+白字)。
- 暗场舞台：bgFlat('#101010') + spotlight 灰梯形(alpha 0.16) + 白描边发光小屏(黄描边+RGB错位描边重影) + 屏下缘像素黄爆发核 + confettiBurst 双方向 + 台缘连续细红灯带 + scanRows + letterbox。
- 换题几何：bgFlat('#000000') + P层细锥光 + 三主体白线稿+微旋转 + 受击点绑 confettiBurst/glowSpot + UI 墨水层(HP行/LIVE角标/RATING 计数) + 底部细红灯带+scanRows。

## 核心源码段（压缩版，语义与全源码等价；场景代码照范例写法在此之上仿写）

```js
function mulberry32(a) {
  return function () {
    a |= 0; a = (a + 0x6D2B79F5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ t >>> 14) >>> 0) / 4294967296;
  };
}
const PS = {
  black: '#101010', white: '#ffffff', orange: '#F08030', yellow: '#FFFF00',
  cyan: '#00d8e0', magenta: '#ff20c8', cyanText: '#00e5e5',
  brick: '#8b2020', navy: '#1a2a4a', teal: '#2a7a8a',
  confetti: ['#ff2020', '#2020ff', '#00d8e0', '#e8e000', '#ff00ff', '#ffffff', '#ffffff', '#111111', '#20ff20'],
};
class PixelStage {
  constructor(seed = 1) {           // 输出 640x360；像素层逻辑 320x180（2x 最近邻）
    this.seed = seed; this.rnd = mulberry32(seed);
    this.LW = 320; this.LH = 180; this.W = 640; this.H = 360;
    this.px = document.createElement('canvas'); this.px.width = this.LW; this.px.height = this.LH;
    this.P = this.px.getContext('2d');                        // 像素层（硬边）
    this.sm = document.createElement('canvas'); this.sm.width = this.W; this.sm.height = this.H;
    this.S = this.sm.getContext('2d');                        // 平滑层（软边特效）
    this.main = document.createElement('canvas'); this.main.width = this.W; this.main.height = this.H;
    this.M = this.main.getContext('2d');                      // 合成输出
    this.letterbox = 0;             // 上下黑边（屏幕px）
  }
  bgFlat(c) { this.P.fillStyle = c; this.P.fillRect(0, 0, this.LW, this.LH); }
  bgRadial(cx, cy, r0, r1, c0, c1) { // 底色画像素层（八律2）
    const g = this.P.createRadialGradient(cx, cy, r0, cx, cy, r1);
    g.addColorStop(0, c0); g.addColorStop(1, c1);
    this.P.fillStyle = g; this.P.fillRect(0, 0, this.LW, this.LH);
  }
  rect(x, y, w, h, c) { this.P.fillStyle = c; this.P.fillRect(x | 0, y | 0, w | 0, h | 0); }
  text(str, x, y, size, c, bold = true) { // 点阵字：小字号进逻辑层
    this.P.font = `${bold ? 'bold ' : ''}${size}px monospace`;
    this.P.fillStyle = c; this.P.textBaseline = 'top'; this.P.fillText(str, x, y);
  }
  box(x, y, w, h, c, t = 1) { // UI 描边框：直角无圆角
    this.rect(x, y, w, t, c); this.rect(x, y + h - t, w, t, c);
    this.rect(x, y, t, h, c); this.rect(x + w - t, y, t, h, c);
  }
  part(w, h, rotDeg, draw) {        // 部件=独立小画布+微旋转（八律5）
    const c = document.createElement('canvas'); c.width = w; c.height = h;
    draw(c.getContext('2d'), w, h);
    return { c, rot: rotDeg * Math.PI / 180 };
  }
  stamp(part, rx, ry) {             // 贴到像素层：rx,ry=部件中心落点
    this.P.save();
    this.P.translate(rx, ry); this.P.rotate(part.rot);
    this.P.imageSmoothingEnabled = false;
    this.P.drawImage(part.c, -part.c.width / 2, -part.c.height / 2);
    this.P.restore();
  }
  rgbSplit(target, dR, dG, dB) {    // 块级通道撕裂：multiply 通道掩膜+lighter（八律4）
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
  rgbSplit2Wrap(part, dR, dG, dB) { return { c: this.rgbSplit(part.c, dR, dG, dB), rot: part.rot }; }
  glowSpot(x, y, r, c, a = 0.8) {   // 辉光斑（平滑层，屏幕坐标）
    const g = this.S.createRadialGradient(x, y, 0, x, y, r);
    g.addColorStop(0, c); g.addColorStop(1, 'rgba(0,0,0,0)');
    this.S.save(); this.S.globalAlpha = a; this.S.globalCompositeOperation = 'lighter';
    this.S.fillStyle = g; this.S.beginPath(); this.S.arc(x, y, r, 0, 7); this.S.fill(); this.S.restore();
  }
  pixSmoke(x, y, r, n, seedOff = 0) { // 块状像素烟（真迹高光形态，八律6）
    const rr = mulberry32(this.seed + seedOff);
    this.S.save();
    for (let i = 0; i < n; i++) {
      const ang = rr() * 6.283, d = rr() * r;
      const w = 2 * (rr() < 0.6 ? 1 : rr() < 0.9 ? 2 : 3);
      this.S.fillStyle = `rgba(255,255,255,${0.25 + rr() * 0.5})`;
      this.S.fillRect((x + Math.cos(ang) * d) | 0, (y + Math.sin(ang) * d * 0.7) | 0, w, w);
    }
    this.S.restore();
  }
  smoke(x, y, r, a = 0.5, seedOff = 0) { // 软烟雾团（与硬像素成对，八律6）
    const s = this.S; s.save();
    s.globalAlpha = a; s.globalCompositeOperation = 'screen';
    const rr = mulberry32(this.seed + seedOff);
    for (let i = 0; i < 5; i++) {
      const dx = (rr() - 0.5) * r, dy = (rr() - 0.5) * r * 0.7, rad = r * (0.4 + rr() * 0.5);
      const g = s.createRadialGradient(x + dx, y + dy, 0, x + dx, y + dy, rad);
      g.addColorStop(0, 'rgba(255,255,255,0.85)'); g.addColorStop(1, 'rgba(255,255,255,0)');
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
  scanRows(ys) {                    // 断续故障扫描行（八律7，屏幕坐标）
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
  confettiBurst(cx, cy, dirDeg, spreadDeg, distMax, n, seedOff = 0) { // 爆点簇（八律6）
    const rr = mulberry32(this.seed + seedOff);
    for (let i = 0; i < n; i++) {
      const t = rr() * rr();        // 密度梯度：贴爆点密、远处疏
      const ang = (dirDeg + (rr() - 0.5) * spreadDeg) * Math.PI / 180;
      const d = 8 + t * distMax;
      const drop = t * t * (rr() * 30 + 10); // 抛物线下坠
      const c = PS.confetti[(rr() * PS.confetti.length) | 0];
      const w = 2 * (rr() < 0.7 ? 1 : 2);
      this.S.fillStyle = c;
      this.S.fillRect((cx + Math.cos(ang) * d) | 0, (cy + Math.sin(ang) * d + drop) | 0, w, w);
    }
  }
  whiteFlash(a = 1) {               // 径向白闪转场
    const g = this.M.createRadialGradient(this.W / 2, this.H / 2, 40, this.W / 2, this.H / 2, 420);
    g.addColorStop(0, `rgba(255,255,255,${a})`); g.addColorStop(1, `rgba(153,153,153,${a * 0.85})`);
    this.M.fillStyle = g; this.M.fillRect(0, 0, this.W, this.H);
  }
  compose(o = {}) {                 // 后处理一次收口（八律：场景不得自行叠加后处理）
    const op = Object.assign({ bloom: 0.55, noise: 0.04, rgbShift: null }, o);
    const M = this.M;
    M.fillStyle = PS.black; M.fillRect(0, 0, this.W, this.H);
    M.imageSmoothingEnabled = false;
    M.drawImage(this.px, 0, 0, this.LW, this.LH, 0, 0, this.W, this.H); // 2x 硬放大
    M.drawImage(this.sm, 0, 0);
    if (this.letterbox > 0) { M.fillStyle = '#0f0f0f';
      M.fillRect(0, 0, this.W, this.letterbox); M.fillRect(0, this.H - this.letterbox, this.W, this.letterbox); }
    if (op.rgbShift) this._frameRGBShift(op.rgbShift);
    if (op.bloom > 0) this._bloom(op.bloom);
    if (op.noise > 0) this._noise(op.noise);
  }
  _frameRGBShift([dx]) {            // 全帧 1-2px 水平色差
    const t = document.createElement('canvas'); t.width = this.W; t.height = this.H;
    t.getContext('2d').drawImage(this.main, 0, 0);
    this.M.drawImage(this.rgbSplit(t, [dx, 0], [0, 0], [-dx, 0]), 0, 0);
  }
  _bloom(a) {                       // 亮部提取→blur→lighter 叠回
    const w = this.W >> 1, h = this.H >> 1;
    const t = document.createElement('canvas'); t.width = w; t.height = h;
    const x = t.getContext('2d');
    x.drawImage(this.main, 0, 0, w, h);
    x.globalCompositeOperation = 'multiply';
    x.fillStyle = 'rgb(90,90,90)'; x.fillRect(0, 0, w, h);
    x.globalCompositeOperation = 'destination-in';
    x.drawImage(this.main, 0, 0, w, h);
    this.M.save();
    this.M.globalAlpha = a; this.M.globalCompositeOperation = 'lighter';
    this.M.filter = 'blur(3px)';
    this.M.drawImage(t, 0, 0, this.W, this.H);
    this.M.restore();
  }
  _noise(a) {                       // 灰阶抖动噪
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
// 用法：const st = new PixelStage(seed); …作画…; st.compose({…});
//      document.body.appendChild(st.main); window.__done = true;
```

## 纪律
种子随机（构造传 seed，禁 Math.random）；中文字 14-15 逻辑px；已知弱点：换题图上撕裂/扫描行
存在感偏低——用时主动加强爆点簇与撕裂。扫描行是**单色断续细横线**（每条线一种颜色），
不是彩色碎块串；撕裂只许出现在打击/爆炸点及其飞散物上。
