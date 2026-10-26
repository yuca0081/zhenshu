// ============================================================
// scenes.js —— 三道考题还原 + 换题考试（中性测试图）
// 考题 A = frame-01 战斗UI层；B = frame-02 白底故障特写；
// C = frame-03 暗场爆炸舞台；N = 换题考试（圆+方块+波浪）。
// 所有坐标默认逻辑像素（320x180），除非注明屏幕像素（640x360）。
// ============================================================

// ---- 共用：机器人 part 家族 ----
// 盒式机器人（frame-02 特写态）：白身+蓝描边+嵌套三框屏幕+像素心+下箭头
function roboBodyPart(rnd) {
  const w = 74, h = 96; // 逻辑px（屏幕 148x192 ≈ frame-02 机身 148 宽）
  return st.part(w, h + 34, 0, (g) => {
    // 判卷 r2 主体识别三件套：外黑内亮粗蓝双边描边 + 黑色茎干
    g.fillStyle = '#111111'; g.fillRect(0, 0, w, h);              // 外黑边（判卷 r3：加宽露脸）
    g.fillStyle = '#fafafa'; g.fillRect(6, 6, w - 12, h - 12);    // 白机身（黑边 6px）
    g.strokeStyle = '#2233cc'; g.lineWidth = 4; g.strokeRect(8.5, 8.5, w - 17, h - 17); // 粗蓝内边
    g.fillStyle = '#111111'; g.fillRect(w / 2 - 5, h - 4, 10, 30); // 黑茎干
    g.fillStyle = '#2233cc'; g.fillRect(w / 2 - 9, h + 22, 18, 4); // 茎底蓝托
    // 机身暗部（顶部面板）
    g.fillStyle = '#d8d8e6'; g.fillRect(8, 6, 22, 8);
    // 嵌套三框屏幕：黄(外)/品红(中)/青(内,左错位)
    g.strokeStyle = '#e8e000'; g.strokeRect(12, 24, 50, 40);
    g.strokeStyle = '#ff20c8'; g.strokeRect(16, 28, 42, 32);
    g.strokeStyle = '#00d8e0'; g.strokeRect(13, 31, 42, 32); // 青框故意左错 3
    // 屏内黑底 + 碎块
    g.fillStyle = '#101010'; g.fillRect(18, 33, 34, 24);
    g.fillStyle = '#2a2a3a';
    g.fillRect(20, 36, 6, 4); g.fillRect(30, 42, 8, 3); g.fillRect(44, 38, 5, 5);
    // 像素心（蓝）+ 底部品红重影
    const heart = [[1,0],[3,0],[0,1],[1,1],[2,1],[3,1],[4,1],[0,2],[1,2],[2,2],[3,2],[4,2],[1,3],[2,3],[3,3],[2,4]];
    const hx = 26, hy = 40, s = 3;
    g.fillStyle = '#ff00ff';
    for (const [cx, cy] of heart) g.fillRect(hx + cx * s, hy + cy * s + 3, s, s); // 重影在+3y
    g.fillStyle = '#2233cc';
    for (const [cx, cy] of heart) g.fillRect(hx + cx * s, hy + cy * s, s, s);
    // 下箭头（黑）——撕裂副本由 rgbSplit 做，这里本体
    g.fillStyle = '#111111';
    g.fillRect(31, 74, 12, 8); g.fillRect(28, 80, 18, 4); g.fillRect(25, 84, 24, 4);
  });
}
// 手臂 part（frame-02：两侧上举，臂端故障最重）
function roboArmPart(side) {
  // 判卷 r2：臂要连体 + 6 色像素噪声密织成弧形色块，不是 2-3 色平涂条
  return st.part(56, 96, 0, (g) => {
    const rr = mulberry32(side < 0 ? 404 : 505);
    // 判卷 r3 色配比：红/蓝为主、黑白碎块、绿只零星点缀
    const cols = ['#2233cc', '#2233cc', '#ff2020', '#ff2020', '#fafafa', '#111111', '#00d8e0', '#20ff20'];
    for (let t = 0; t <= 1; t += 0.03) {
      // 判卷 r3 形态：从(躯干)头部两侧连出，先竖升再向外上弯成钩
      const cx = 28 + (side < 0 ? -1 : 1) * (t < 0.45 ? t * 6 : (t - 0.45) * (t - 0.45) * 90);
      const cy = 92 - t * 78;
      const thick = t < 0.4 ? 9 : 12 - t * 4; // 根粗端细
      for (let k = 0; k < 8; k++) {
        const dx = (rr() - 0.5) * thick, dy = (rr() - 0.5) * thick;
        g.fillStyle = cols[(rr() * cols.length) | 0];
        g.fillRect((cx + dx) | 0, (cy + dy) | 0, 3, 3);
      }
    }
  });
}
// 小机器人剪影（frame-01 顶部舞台态：白描边深灰机身+面板+竖杆圆环摆锤）
function roboSilhouettePart() {
  return st.part(74, 62, 0, (g) => {
    g.fillStyle = '#1F1F1F'; g.fillRect(2, 2, 70, 42);
    g.strokeStyle = '#ffffff'; g.lineWidth = 2; g.strokeRect(2, 2, 70, 42);
    g.fillStyle = '#ffffff'; g.fillRect(28, 8, 20, 10); // 顶部白面板
    g.fillStyle = '#8a8a8a'; g.fillRect(30, 10, 16, 6); // 面板灰阶
    g.fillStyle = '#ffffff'; g.fillRect(34, 44, 6, 12); // 竖杆
    // 圆环（像素阶梯）
    g.strokeStyle = '#ffffff'; g.lineWidth = 3;
    g.beginPath(); g.arc(37, 54, 8, 0, 7); g.stroke();
  });
}

// ============================================================
// 考题 A —— frame-01：战斗 UI 层（黑底 83%、橙=交互、黄只两处）
// ============================================================
function sceneA() {
  st.letterbox = 0;
  st.bgFlat('#000000'); // 判卷 r2：真迹 A 是纯黑不是 #101010
  // 锥光画在像素层（判卷 r2 层序：舞台光最底、UI 永远浮上、箱内纯黑）
  st.P.save(); st.P.globalAlpha = 0.16; st.P.fillStyle = '#ffffff';
  st.P.beginPath(); st.P.moveTo(185, 0); st.P.lineTo(205, 0);
  st.P.lineTo(215, 150); st.P.lineTo(175, 150); st.P.closePath(); st.P.fill();
  st.P.globalAlpha = 0.12; st.P.fillRect(160, 148, 70, 3); // 锥底光带
  st.P.restore();
  // 顶部机器人剪影（上缘出血：中心 y 取负）
  const robo = roboSilhouettePart(); robo.rot = 4 * Math.PI / 180;
  st.stamp(robo, 203, -8);
  // —— UI 全部走 part（独立微旋转，2.8° 基倾 + 各自抖动）——
  // 对话框（中部主体，右缘出血）
  const dlg = st.part(300, 112, -2.8 + 1.5, (g) => {
    g.fillStyle = '#000000'; g.fillRect(0, 0, 300, 112);
    g.strokeStyle = '#ffffff'; g.lineWidth = 4; g.strokeRect(2, 2, 296, 108);
  });
  st.stamp(dlg, 225, 100);
  // 状态行（BOMEI LV 1 HP ■ 20 / 20）
  const hp = st.part(280, 18, 2.8 - 1.2, (g) => {
    g.font = 'bold 11px monospace'; g.fillStyle = '#ffffff'; g.textBaseline = 'top';
    g.fillText('BOMEI', 2, 4);
    g.fillText('LV 1', 88, 4);
    g.fillText('HP', 140, 4);
    g.fillStyle = '#FFFF00'; g.fillRect(165, 4, 14, 12); // 黄血格（焦点黄 #1）
    g.fillStyle = '#ffffff'; g.fillText('20 / 20', 190, 4);
  });
  st.stamp(hp, 170, 143);
  // 按钮排（3 个橙框 + 右缘残条；逻辑 67x27 ≈ 屏幕 135x55）
  const btn = (label, icon, yellowIcon) => st.part(67, 27, 0, (g) => {
    g.fillStyle = '#000000'; g.fillRect(0, 0, 67, 27);
    g.strokeStyle = '#F08030'; g.lineWidth = 2; g.strokeRect(1, 1, 65, 25);
    g.font = 'bold 15px monospace'; g.textBaseline = 'top';
    if (icon === 'heart') { // 黄色实心像素心（焦点黄 #2，全场第二处）
      g.fillStyle = '#FFFF00';
      const pts = [[1,0],[3,0],[0,1],[1,1],[2,1],[3,1],[4,1],[0,2],[1,2],[2,2],[3,2],[4,2],[1,3],[2,3],[3,3],[2,4]];
      for (const [cx, cy] of pts) g.fillRect(6 + cx * 3, 5 + cy * 3, 3, 3);
    } else if (icon === 'sword') {
      g.strokeStyle = '#F08030'; g.lineWidth = 2;
      g.beginPath(); g.moveTo(7, 17); g.lineTo(15, 7); g.moveTo(8, 13); g.lineTo(12, 17); g.stroke();
    } else {
      g.strokeStyle = '#F08030'; g.lineWidth = 2; g.strokeRect(5, 5, 12, 10);
      g.fillStyle = '#F08030'; g.fillRect(8, 8, 6, 4);
    }
    g.fillStyle = '#F08030'; g.fillText(label, 24, 6);
  });
  const b1 = btn('战斗', 'sword'); b1.rot = (2.8 + 8.2) * Math.PI / 180;
  const b2 = btn('行动', 'heart'); b2.rot = (2.8 + 3.1) * Math.PI / 180;
  const b3 = btn('物品', 'box'); b3.rot = (2.8 - 4.6) * Math.PI / 180;
  st.stamp(b1, 57, 163); st.stamp(b2, 150, 161); st.stamp(b3, 243, 163);
  // 第 4 按钮残条（右缘出血）
  st.rect(315, 152, 4, 24, '#F08030');
  st.compose({ bloom: 0.3, rgbShift: [0.5], noise: 0.005 });
}

// ============================================================
// 考题 B —— frame-02：白底故障特写（块级 RGB 撕裂、letterbox）
// ============================================================
function sceneB() {
  st.letterbox = 0; // frame-02 像素实测顶/底全 253：满幅无黑边（首读误判）
  // 背景：中性白（判卷 r2：真迹 #fdfdfd 中性白，暖纸纹那版像旧纸张）
  st.bgRadial(160, 88, 20, 220, '#f4f4f2', '#e4e4e2');
  // 斜向粉光带（真迹 2.2% 占比，降存在感）
  st.P.save();
  st.P.globalAlpha = 0.22; st.P.translate(40, 0); st.P.rotate(0.35);
  st.P.fillStyle = '#f0d8d8'; st.P.fillRect(-40, -40, 70, 300);
  st.P.restore();
  // 左缘淡黄竖光带
  const gy = st.S.createLinearGradient(0, 0, 60, 0);
  gy.addColorStop(0, 'rgba(240,232,200,0.9)'); gy.addColorStop(1, 'rgba(240,232,200,0)');
  st.S.fillStyle = gy; st.S.fillRect(0, 0, 60, 360);
  // 顶部青字被黑边裁半（贴 letterbox 下沿）
  st.text('NEO METTATON SHOW !!', 96, 5, 7, '#00e5e5');
  // 部件级 RGB 撕裂：躯干轻、手臂重（八律 #2）
  const body = roboBodyPart(); body.rot = -3 * Math.PI / 180;
  const torn = st.rgbSplit(body.c, [4, 2], [-5, -1], [7, -3]); // 屏幕 8/-10/14px 级
  const tornPart = { c: torn, rot: body.rot };
  st.stamp(tornPart, 160, 102);
  const armL = roboArmPart(-1), armR = roboArmPart(1);
  const tL = st.rgbSplit(armL.c, [10, 4], [-12, -2], [14, -6]); // 手臂最重
  const tR = st.rgbSplit(armR.c, [-9, 3], [11, -4], [-13, -5]);
  st.stamp({ c: tL, rot: 6 * Math.PI / 180 }, 112, 82);  // 判卷 r3：从躯干上部两侧连出上弯
  st.stamp({ c: tR, rot: -6 * Math.PI / 180 }, 208, 82);
  // 手臂马赛克色块堆（故障点缀，屏幕坐标）
  st.S.fillStyle = '#ff20c8'; st.S.fillRect(96, 130, 8, 8);
  st.S.fillStyle = '#e8e000'; st.S.fillRect(500, 122, 6, 10);
  st.S.fillStyle = '#00d8e0'; st.S.fillRect(88, 176, 10, 6);
  st.S.fillStyle = '#20ff20'; st.S.fillRect(510, 168, 8, 8);
  st.compose({ bloom: 0.35, rgbShift: null, noise: 0.03 });
}

// ============================================================
// 考题 C —— frame-03：暗场爆炸舞台（纸屑×软烟、扫描行、光池）
// ============================================================
function sceneC() {
  st.letterbox = 29;
  // 底色压暗进像素层（判卷 r1：真迹暗部中位 15-16，不是 3a3a3a 的 58）
  st.bgFlat('#101010');
  // 聚光灯锥：填充灰梯形（真迹形态），锥内渐亮
  st.spotlight(320, -20, 320, 300, 90, 160, 'rgba(120,120,120,0.16)');
  st.S.fillStyle = 'rgba(255,255,255,0.07)';
  st.S.beginPath(); st.S.moveTo(210, 298); st.S.lineTo(430, 298); st.S.lineTo(400, 302); st.S.lineTo(240, 302); st.S.fill();
  // 台缘斜边：真迹是红/橙厚边（判卷 r1），不是青蓝细线
  st.S.strokeStyle = 'rgba(232,130,48,0.75)'; st.S.lineWidth = 3;
  st.S.beginPath(); st.S.moveTo(212, 250); st.S.lineTo(242, 300); st.S.stroke();
  st.S.strokeStyle = 'rgba(232,60,32,0.7)';
  st.S.beginPath(); st.S.moveTo(432, 250); st.S.lineTo(400, 300); st.S.stroke();
  // —— 顶部残骸：右臂轮廓（RGB 重影描边）——
  const arm = st.part(60, 60, 18, (g) => {
    g.strokeStyle = '#ffffff'; g.lineWidth = 3;
    g.beginPath(); g.moveTo(10, 50); g.lineTo(20, 16); g.lineTo(46, 8); g.lineTo(54, 26); g.stroke();
    g.fillStyle = '#2a2a2a';
    g.fillRect(14, 20, 14, 14); g.fillRect(36, 10, 12, 10);
  });
  st.stamp(st.rgbSplit2Wrap(arm, [5, 2], [-6, 0], [7, -2]), 400, 92);
  // 半解体下箭头
  st.rect(312, 58, 12, 8, '#dddddd'); st.rect(308, 66, 20, 4, '#dddddd'); st.rect(322, 70, 10, 3, '#dddddd');
  // 胸口白光屏：白块 + RGB 块级错位描边（判卷 r2 爆点三层之一）
  const scr = st.part(34, 24, 5, (g) => {
    g.fillStyle = '#ffffff'; g.fillRect(2, 2, 30, 20);
    g.strokeStyle = '#e8e000'; g.lineWidth = 2; g.strokeRect(1, 1, 32, 22);
  });
  st.stamp(st.rgbSplit2Wrap(scr, [4, 1], [-5, 0], [6, -2]), 160, 106);
  // 黄爆发核：像素黄块团 + 辉光（判卷 r2：高光是块状像素团）
  const core = st.part(22, 16, 0, (g) => {
    const rr2 = mulberry32(61);
    for (let i = 0; i < 34; i++) {
      g.fillStyle = rr2() < 0.6 ? '#ffe070' : rr2() < 0.5 ? '#ff9020' : '#ffffff';
      g.fillRect(1 + rr2() * 17, 1 + rr2() * 11, rr2() < 0.5 ? 3 : 4, rr2() < 0.5 ? 3 : 4);
    }
  });
  st.stamp(core, 159, 117); // 判卷 r3：核长在屏幕下缘，不留漂浮光球
  st.glowSpot(318, 124, 26, 'rgba(255,224,112,0.7)', 0.8); // 单个弱辉光
  // 左臂炸开：爆点密簇 + 对角向左下抛物线下落弧（判卷 r1：要簇不要均匀撒）
  st.confettiBurst(300, 100, 205, 70, 210, 260, 5);  // 左下方向爆散
  st.confettiBurst(300, 100, 160, 120, 150, 120, 15); // 上方抛洒回落
  // 软烟团（判卷 r1：缩小 3-4 倍成小团簇）
  st.smoke(470, 96, 16, 0.5, 21);
  st.smoke(180, 78, 13, 0.4, 22);
  // 中下白色小像素人（背对、聚光下，屏幕 35x55 → 逻辑 ~17x27）
  const kid = st.part(18, 28, 0, (g) => {
    g.fillStyle = '#ffffff';
    g.fillRect(4, 0, 10, 8);            // 兜帽头
    g.fillRect(3, 8, 12, 13);           // 躯干
    g.fillStyle = '#5a5a5a';
    g.fillRect(5, 11, 3, 6); g.fillRect(10, 11, 3, 6); // 躯干暗部
    g.fillStyle = '#ffffff'; g.fillRect(3, 21, 12, 2); // 脚横杠
  });
  kid.rot = -2 * Math.PI / 180;
  st.stamp(kid, 160, 138);
  // 台缘连续红灯带紧贴锥底（判卷 r2：舞台前沿感）+ 下方故障扫描行（真迹证据保留）
  st.S.fillStyle = 'rgba(232,40,32,0.85)'; st.S.fillRect(180, 297, 282, 3);
  st.scanRows([
    { y: 303, color: PS.navy }, { y: 306, color: PS.teal },
    { y: 315, color: PS.brick },
  ]);
  st.compose({ bloom: 0.6, rgbShift: null, noise: 0.008 });
}

// ============================================================
// 换题考试 N —— 中性测试图：圆 + 方块 + 波浪（同一画材包）
// ============================================================
function sceneN() {
  // 换题考试 v2（三方对比 r1 教训：旧版停在迭代前参数——灰底/无 UI/无源光晕/软烟，
  // 被纯形容词版反超 40 分。本版与考题场景同步到终审配方）。
  st.letterbox = 0;
  st.bgFlat('#000000');
  // 像素层锥光（A 帧层序：光最底、UI 浮上）
  st.P.save(); st.P.globalAlpha = 0.14; st.P.fillStyle = '#ffffff';
  st.P.beginPath(); st.P.moveTo(150, 0); st.P.lineTo(170, 0);
  st.P.lineTo(185, 165); st.P.lineTo(135, 165); st.P.closePath(); st.P.fill();
  st.P.restore();
  // —— 三元素为一等主体（白线稿描边 + 微旋转 + 部件级 RGB 撕裂）——
  // 圆：白描边像素阶梯圆 + 橙内环（橙=可交互）
  const circ = st.part(64, 64, 3.5, (g) => {
    for (let a = 0; a < 64; a++) { // 阶梯圆周（像素圆）
      const t = a / 64 * Math.PI * 2, x = 32 + Math.cos(t) * 26, y = 32 + Math.sin(t) * 26;
      g.fillStyle = '#ffffff'; g.fillRect(x | 0, y | 0, 3, 3);
    }
    g.strokeStyle = '#F08030'; g.lineWidth = 2;
    g.beginPath(); g.arc(32, 32, 16, 0, 7); g.stroke();
  });
  st.stamp(st.rgbSplit2Wrap(circ, [3, 1], [-4, 0], [5, -2]), 76, 62);
  // 方块：白描边深灰填充 + 顶部黄面板（焦点黄全场唯一）
  const sq = st.part(52, 52, -5, (g) => {
    g.fillStyle = '#1F1F1F'; g.fillRect(2, 2, 48, 48);
    g.strokeStyle = '#ffffff'; g.lineWidth = 3; g.strokeRect(2, 2, 48, 48);
    g.fillStyle = '#FFFF00'; g.fillRect(16, 8, 20, 7);
  });
  st.stamp(sq, 160, 58);
  // 波浪：青色扫描线波 + 红青重影
  const wav = st.part(170, 34, 5.5, (g) => {
    for (let i = 0; i <= 30; i++) {
      const x = 6 + i * 5.3, y = 17 + Math.sin(i * 0.55) * 10;
      g.fillStyle = '#00d8e0'; g.fillRect(x | 0, y | 0, 3, 3);
      g.fillStyle = 'rgba(255,32,64,0.5)'; g.fillRect((x - 2) | 0, y | 0, 3, 3); // 重影
    }
  });
  st.stamp(wav, 230, 108);
  // 方块受击小爆点（纸屑绑定打击点，判卷 r1 三方教训：禁无源撒点）
  st.confettiBurst(320, 116, 230, 65, 70, 70, 3);
  st.glowSpot(322, 114, 20, 'rgba(255,224,112,0.7)', 0.85); // 辉光只绑爆点
  // —— UI 墨水层（真迹 UI 语言：像素字 + 白线稿 + LIVE 角标 + 计数器）——
  const hp = st.part(250, 16, 1.5, (g) => {
    g.font = 'bold 10px monospace'; g.fillStyle = '#ffffff'; g.textBaseline = 'top';
    g.fillText('KITT  LV 1  HP', 2, 3);
    g.fillStyle = '#FFFF00'; g.fillRect(122, 3, 12, 10);
    g.fillStyle = '#ffffff'; g.fillText('18 / 20', 142, 3);
  });
  st.stamp(hp, 152, 132);
  // LIVE 红点 + CAM（青字）+ 收视率翻牌计数器（sig1 特色件）
  const live = st.part(96, 14, -2, (g) => {
    g.fillStyle = '#ff2020'; g.fillRect(2, 3, 8, 8);
    g.font = 'bold 9px monospace'; g.fillStyle = '#ffffff'; g.textBaseline = 'top';
    g.fillText('LIVE', 14, 3); g.fillStyle = '#00e5e5'; g.fillText('CAM 02', 46, 3);
  });
  st.stamp(live, 40, 26);
  const cnt = st.part(110, 18, 2.5, (g) => {
    g.font = 'bold 9px monospace'; g.fillStyle = '#00e5e5'; g.textBaseline = 'top';
    g.fillText('RATING 12000', 2, 4);
  });
  st.stamp(cnt, 262, 24);
  // 台缘红灯带 + 故障扫描行（真迹证据保留）
  st.S.fillStyle = 'rgba(232,40,32,0.8)'; st.S.fillRect(120, 168, 400, 2);
  st.scanRows([{ y: 178, color: PS.navy }, { y: 186, color: PS.brick }]);
  st.compose({ bloom: 0.35, rgbShift: [0.6], noise: 0.005 });
}
