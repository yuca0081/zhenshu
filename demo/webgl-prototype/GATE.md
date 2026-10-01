# WebGL 原型闸记录(vend 方案出口判据之一)

> 日期:2026-10-01。依据 `docs/vend-stage.md` §7.1(three.js 软渲染探针)。
> 样本:`probe.html`(three.js r152 本地库 + 环面结 TorusKnot + 双灯 PBR + 网格地面 +
> 雾;唯一 paused timeline,onUpdate 驱动 `__draw(t)`,draw 为 t 的纯函数)。

## 结论:通过

| 验证点 | 结果 |
|---|---|
| **three.js 在渲染链路建上下文** | ✅ `context=webgl2`,hyperframes 走 hardware gpu capture,画面完整出图(HUD 帧内可见) |
| **真实 3D 质感** | ✅ PBR 金属度/粗糙度、双方向灯高光、雾、网格地面全部正常——不是黑屏假阳性 |
| **确定性:两次独立渲染逐像素一致** | ✅ 两渲 t=0.5/3/5.5 三时刻 md5 逐对全同 |
| **契约驱动方式兼容** | ✅ 与场景契约同构:唯一 `gsap.timeline({paused:true})` + onUpdate → `__draw(t)` 纯函数全量重渲,`preserveDrawingBuffer:true` 供截图 |
| **性能** | ✅ 6s 1080p30 全 3D 场景 ≈15s/次(180 帧,含编译),与 2D 原型同量级 |

## 对后续的直接输入

1. **风格 05(3D 渲染)/ 14(液态流动)的工程不确定性已消除**:原评估"需 WebGL 验证"
   的前置条件满足,三.js 路线可在 v2 管线直出,无需改渲染器架构或加启动 flag;
2. **vend 方案的第一位真实用户成立**:three.js 从"库房没有的框架"变为可按
   vend-stage.md §3 流程入库的常规库(本探针用本地拷贝 r152,即未来 vendor 的雏形);
3. **契约 T4 的 WebGL 落地形态确定**:`WebGLRenderer({preserveDrawingBuffer:true})` +
   `__draw_<id>(t)` 纯函数 + timeline onUpdate 驱动——写进 contract.md 的场景生成提示。

## 复现

```bash
cd demo/webgl-prototype
npx -y hyperframes@0.8.55 render . -c probe.html -o render1.mp4
npx -y hyperframes@0.8.55 render . -c probe.html -o render2.mp4
for t in 0.5 3 5.5; do ffmpeg -y -v error -ss $t -i render1.mp4 -frames:v 1 a_$t.png \
  && ffmpeg -y -v error -ss $t -i render2.mp4 -frames:v 1 b_$t.png; done
md5sum a_*.png b_*.png   # 三对全同
```

> three.min.js 取自 demo/aegis3d/assets(r152,仓库既有资产,零网络获取)。
