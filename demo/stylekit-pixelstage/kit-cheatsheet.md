# pixelstage 画材包小抄（注入画面工位提示词用，~1.5k token）

零依赖纯 Canvas2D（像素风的笔触单位是格子，不要引入笔刷库）。装载：`<script src="{kit路径}/kit.js">`，`const st = new PixelStage(seed)`，作画后 `document.body.appendChild(st.main)` 出 640x360。

## 画风八律（违反即走样，逐条有真迹证据）
1. 双分辨率：像素画一律画**像素层**（逻辑 320x180，`st.P`/rect/text/box/part+stamp），自动 2x 硬放大；辉光/渐变/烟雾画**平滑层**（全分辨率 `st.S`/glowSpot/spotlight）。
2. **底色必须画像素层**（bgFlat/bgRadial）——平滑层放不透明底会把像素层整个盖掉（踩过）。
3. 色彩三律：黑白灰舞台；橙 `#F08030`=可交互；电光黄 `#FFFF00`=生命/焦点，**全场至多两处**。PS 对象取色。
4. RGB 撕裂按部件：`st.rgbSplit(部件c, [4,2],[-5,-1],[7,-3])` 三通道错位再 lighter 合成；躯干轻、手臂/爆发重——错位是部件属性不是全图属性。
5. 手持机位：每个 UI 元素 `part(w,h,rotDeg,draw)` 独立 2°-8° 微旋转再 stamp，禁止刚体对齐。
6. 粒子=4-10px 多色像素团（红蓝为主黑白夹杂，PS.confetti），`confettiBurst(x,y,方向°,散布°,距离,n,seedOff)` 自带从爆点向外的密度梯度；均匀撒点会走样。
7. 高光/烟=**块状像素团**（pixSmoke / part 里 3-4px 填充块），不是 bokeh 模糊光斑。
8. **无 CRT 滤镜**：禁扫描线/网纹/暗角拟真。故障感只来自 rgbSplit 和 scanRows（断续横线，深蓝 #1a2a4a/青绿 #2a7a8a/砖红 #8b2020）。

## 后处理（收尾必走）
`st.compose({bloom, rgbShift, noise})`：像素层→平滑层→letterbox→辉光→全帧 1-2px 色差→轻噪点。参数带：战斗UI {bloom:0.3, rgbShift:[0.5], noise:0.005}；白底特写 {bloom:0.35, noise:0.03}；暗场 {bloom:0.6, noise:0.008}。

## 常用配方
- 战斗UI：bgFlat('#000000') + P层锥光(alpha 0.16) + part 对话框(白4px边+黑内) + 橙框按钮(part+微旋转) + HP行(黄方块+白字)。
- 故障特写：bgRadial 中性白 + part 机器人(外黑边/白身/蓝粗边三重描边) + rgbSplit 手臂(噪声密织弧形臂)。
- 爆炸舞台：bgFlat('#101010') + spotlight 灰梯形(alpha 0.16) + RGB错位描边小屏 + 像素黄爆发核贴屏下缘 + confettiBurst 三方向 + 台缘连续红灯带 + scanRows。
- 段落转场：st.whiteFlash(0.9)（径向白闪）；黑场直接 bgFlat('#101010')。

## 纪律
种子随机（构造时传 seed，禁 Math.random）；文字用 `st.text`（小字号进逻辑层自动点阵化）；中文按钮字 14-15 逻辑px。已知弱点：换题图上撕裂/扫描行存在感低——用时主动加强 rgbShift。
