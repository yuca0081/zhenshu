# scene 原型闸记录（M-A 出口判据）

> 日期：2026-09-27。依据 `docs/upgrade-plan.md` §4.1 验收①。
> 样本：`chord.html`（乘法表弦图 ×2→×3 连续演变 + 深空星尘 + 加色混合辉光 + 右上 t 计数器）。

## 结论：通过

| 验证点 | 结果 |
|---|---|
| **GSAP 占位 tween onUpdate 在 seek 时触发**（scene 通道承重假设） | ✅ 画面随 t 连续演化（弦数 36→300 生长、×2→×3），亮像素占比 1.3%→4.5%→5.4%，独立色数 3k→8.5k |
| **确定性回放 diff=0** | ✅ 两次独立渲染，t=0.5/3/5.5 三时刻抽帧 md5 逐对全同 |
| **加色混合辉光**（globalCompositeOperation='lighter'） | ✅ 可用 |
| **seeded PRNG**（mulberry32，每帧重置） | ✅ 星尘背景跨渲染逐像素一致 |
| **无音频段渲染** | ✅（audio 0.0s，管线不要求音轨） |
| **性能** | 6s 1080p30 全画幅逐帧 JS 绘制 ≈ 18–20s（硬件 GPU 捕获；含 90 星尘 + 300 弦/帧） |

## 复现

```bash
cd demo/scene-prototype
npx -y hyperframes@0.8.55 render . -c chord.html -o render1.mp4
npx -y hyperframes@0.8.55 render . -c chord.html -o render2.mp4
for t in 0.5 3 5.5; do ffmpeg -y -v error -ss $t -i render1.mp4 -frames:v 1 a_$t.png \
  && ffmpeg -y -v error -ss $t -i render2.mp4 -frames:v 1 b_$t.png; done
md5sum a_*.png b_*.png   # 三对全同
```

## 对 M-B 的直接输入

- `kind=scene` 契约按 §4.1 落地（onUpdate 驱动已验证，hyperframes 零改动）。
- overlay 计数器（P0-2）走同一 onUpdate 机制——本原型的 t 计数器即其等价物，渲染路径一并验证。
- 性能基线：单帧全画幅 canvas 重绘 + 截图 ≈ 0.1s 量级（capture 9.6s / 180 帧），长段可用。
