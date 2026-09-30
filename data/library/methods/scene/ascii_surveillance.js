// ASCII 监控画布（圆形监狱美学）。世界观：纯黑底 + 等宽字符画 + 白灰层次 + 红色仅点缀。
// 硬规则：一切图案 = 字符网格的纯函数 of t；随机一律用 rng()（每帧同种子重置）抽取
// 固有参数，闪烁/蠕动用 sin-hash（下例 h2）；时间只用入参 t，其余随机/时间类
// API 全是门禁禁词（教训：注释里也别写它们的字面名，黑名单不区分注释）。
// 字符画用 ctx.font = '<px>px Consolas, monospace' + fillText，逐格绘制。
// 亮度→字符梯度：RAMP=' .:-=+*#%@'；HUD 文本（CAM 编号/REC/时间戳）直接 fillText 小号等宽字。
var RAMP = ' .:-=+*#%@';
function h2(i, j, s) {                       // 确定性 hash 0..1（闪烁/蠕动全部用它）
  var x = Math.sin(i * 127.1 + j * 311.7 + s * 74.7) * 43758.5453;
  return x - Math.floor(x);
}
var CW = 13, CH = 19;                        // 字符格宽高（px）
var cols = Math.ceil(W / CW), rows = Math.ceil(H / CH);
ctx.textAlign = 'center'; ctx.textBaseline = 'middle';
for (var j = 0; j < rows; j++) {
  for (var i = 0; i < cols; i++) {
    var u = (i / cols - 0.5) * 2.0, v = (j / rows - 0.5) * 2.2;
    var lim = 0.82 * (1 - u * u);            // 杏仁眼上下睑
    if (Math.abs(u) <= 1 && Math.abs(v) <= lim) {
      var ix = u - 0.12, iy = v * 1.35;
      var d = Math.sqrt(ix * ix + iy * iy);
      var tw = h2(i, j, Math.floor(t * 9));  // 蠕动：随 t 整帧换哈希
      var ch = ' ', red = false;
      if (d < 0.30) ch = ' ';                // 瞳孔留黑
      else if (d < 0.50) {                   // 虹膜：红
        ch = RAMP[Math.min(9, 4 + Math.floor(tw * 3.5))]; red = true;
      } else {                               // 巩膜：暗纹
        ch = RAMP[Math.min(3, Math.floor((0.14 + 0.14 * tw) * 9.99))];
      }
      var edge = Math.abs(Math.abs(v) - lim);
      if (edge < 0.06 && Math.abs(u) < 0.99) ch = RAMP[7 + Math.floor(tw * 3)];
      if (ch !== ' ') {
        ctx.font = CH - 5 + 'px Consolas, monospace';
        ctx.fillStyle = red ? '#E3170A' : 'rgba(190,190,190,0.8)';
        ctx.fillText(ch, (i + 0.5) * CW, (j + 0.5) * CH);
      }
    }
  }
}
// ── HUD 包边（画在字符层之上）：左上 CAM、右上 REC、右下毫秒走时、左下参数 ──
ctx.textAlign = 'left'; ctx.font = '17px Consolas, monospace';
ctx.fillStyle = '#9A9A9A';
ctx.fillText('CAM-00 / EYE', 46, 40);
ctx.textAlign = 'right';
ctx.fillText('ZOOM x1.0   MODE: OBSERVE', W - 46, H - 44);
var ts = '2026/09/28 SUN 23:59:' + ('0' + Math.floor(t % 60)).slice(-2) + '.' +
  ('0' + Math.floor((t * 100) % 100)).slice(-2);
ctx.fillText(ts, W - 46, H - 20);
if (Math.floor(t * 1.6) % 2 === 0) {         // REC 红点闪烁（t 的整数函数）
  ctx.fillStyle = '#E3170A';
  ctx.fillText('● REC', W - 46, 40);
}
// ── 扫描亮线：一条随 t 下移的细亮带（整屏 CSS 般的雷达感）──────────
var sy = ((t * 90) % (H + 200)) - 100;
var grad = ctx.createLinearGradient(0, sy - 60, 0, sy + 60);
grad.addColorStop(0, 'rgba(255,255,255,0)');
grad.addColorStop(0.5, 'rgba(255,255,255,0.05)');
grad.addColorStop(1, 'rgba(255,255,255,0)');
ctx.fillStyle = grad;
ctx.fillRect(0, sy - 60, W, 120);
