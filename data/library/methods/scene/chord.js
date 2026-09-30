var N = 60, cx = W / 2, cy = H / 2, R = Math.min(W, H) * 0.38;
var p = Math.min(1, t / 5);      // 段内进度：缓一点更优雅
var k = 2 + p;                   // ×2 → ×3 倍率演变（换起止即任意倍率）
var rot = 0.2 * p;               // 缓慢旋转
ctx.strokeStyle = 'rgba(255,255,255,.3)';
ctx.beginPath(); ctx.arc(cx, cy, R, 0, 6.2832); ctx.stroke();
ctx.globalCompositeOperation = 'lighter';   // 加色混合 = 霓虹辉光
for (var i = 0; i < N; i++) {
  var a0 = i / N * 6.2832 + rot, a1 = i / N * k * 6.2832 + rot;
  var hue = Math.round(i / N * 300);
  ctx.strokeStyle = 'hsla(' + hue + ',90%,60%,.45)';
  ctx.lineWidth = 1.2;
  ctx.beginPath();
  ctx.moveTo(cx + R * Math.cos(a0), cy + R * Math.sin(a0));
  ctx.lineTo(cx + R * Math.cos(a1), cy + R * Math.sin(a1));
  ctx.stroke();
}
ctx.globalCompositeOperation = 'source-over';
for (var j = 0; j < N; j += 3) {             // 节点微光
  var aj = j / N * 6.2832 + rot;
  ctx.fillStyle = 'rgba(255,244,214,.9)';
  ctx.beginPath();
  ctx.arc(cx + R * Math.cos(aj), cy + R * Math.sin(aj), 2.5, 0, 6.2832);
  ctx.fill();
}
