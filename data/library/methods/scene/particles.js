// 粒子场（星尘/萤火/漂浮尘埃）。要点：rng() 每帧从同种子重置，
// 逐粒固定抽取固有参数，运动写成 t 的正弦（解析轨迹）——绝不用逐帧累加。
for (var i = 0; i < 160; i++) {
  var r1 = rng(), r2 = rng(), r3 = rng(), r4 = rng();
  var x = r1 * W, y = r2 * H;                        // 固有位置
  var drift = Math.sin(t * (0.3 + r3 * 0.7) + r4 * 6.28) * 30;
  var tw = 0.35 + 0.35 * Math.sin(t * (1 + r3 * 2) + r4 * 6.28);  // 呼吸明暗
  var sz = 1 + r3 * 2.4;
  ctx.fillStyle = 'rgba(230,238,255,' + Math.max(0, tw).toFixed(3) + ')';
  ctx.beginPath();
  ctx.arc(x + drift, y + drift * 0.4, sz, 0, 6.2832);
  ctx.fill();
}
// 提醒（接线易漏点）：上面的绘制体要由一条暂停的时间线驱动——tl.to({},{duration:段长,ease:"none",
// onUpdate 里用 tl.time() 调绘制函数,},0)——并在末尾把时间线按段 id 注册进全局时间线表（契约第 3/7 条），
// 漏注册会被 lint 拒收。占位与接线三行模板见作业正文。
