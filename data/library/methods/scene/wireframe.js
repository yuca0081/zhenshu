// 3D 线框投影（旋转立方体，可扩为晶格/多面体）。手写 3D→2D 投影约 20 行，
// 旋转角是 t 的线性函数 → 纯函数，seek 确定。
var cx = W / 2, cy = H / 2, S = Math.min(W, H) * 0.28;
var a = t * 0.5, b = t * 0.33;                 // 双轴匀速旋转
var ca = Math.cos(a), sa = Math.sin(a), cb = Math.cos(b), sb = Math.sin(b);
var V = [[-1,-1,-1],[1,-1,-1],[1,1,-1],[-1,1,-1],[-1,-1,1],[1,-1,1],[1,1,1],[-1,1,1]];
var E = [[0,1],[1,2],[2,3],[3,0],[4,5],[5,6],[6,7],[7,4],[0,4],[1,5],[2,6],[3,7]];
var pts = [];
for (var i = 0; i < 8; i++) {
  var x = V[i][0], y = V[i][1], z = V[i][2];
  var x1 = ca * x - sa * z, z1 = sa * x + ca * z;   // 绕 Y 转
  var y1 = cb * y - sb * z1, z2 = sb * y + cb * z1; // 绕 X 转
  var d = 3 / (3 + z2);                             // 透视除法
  pts.push([cx + x1 * S * d, cy + y1 * S * d, z2]);
}
for (var j = 0; j < E.length; j++) {
  var P = pts[E[j][0]], Q = pts[E[j][1]];
  var lum = Math.max(0.15, 0.6 - (P[2] + Q[2]) * 0.15); // 远暗近亮
  ctx.strokeStyle = 'rgba(140,200,255,' + lum.toFixed(3) + ')';
  ctx.lineWidth = 2;
  ctx.beginPath();
  ctx.moveTo(P[0], P[1]);
  ctx.lineTo(Q[0], Q[1]);
  ctx.stroke();
}
