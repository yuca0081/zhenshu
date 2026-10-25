# p5.brush 样张与画风矩阵

实验场：验证 p5.brush（p5.js 2.3.4 + p5.brush 2.2.3）在 headless Chrome（SwiftShader）下的画风还原能力。
产出背景与完整方案见 `docs/style-kit-distillation-plan-2026-10-26.md`。

## 文件

- `index.html` + `sketch.js`：对比样张（左半裸 Canvas / 右半 p5.brush，同一套几何）
- `style.html` + `styles.js`：四画风矩阵（`?style=vg|ink|ukyo|etch`，同一几何×四套画材包）
- `sheet.html`：2×2 总览拼图
- `vendor/`：p5.min.js + p5.brush.js（钉版本，npm pack 自 npmmirror 拉取）

## 重渲（种子确定性，逐像素一致）

渲染 PNG 按仓库惯例不入库，随时重出：

```bash
CHROME="/c/Program Files/Google/Chrome/Application/chrome.exe"
FLAGS="--headless --use-angle=swiftshader --enable-unsafe-swiftshader --hide-scrollbars --window-size=960,1080 --virtual-time-budget=60000"
# 画风单张（vg 换 ink/ukyo/etch）
"$CHROME" $FLAGS --screenshot="$PWD/style-vg.png" "file://$PWD/style.html?style=vg"
# 对比样张（1920×1080）
"$CHROME" $FLAGS --window-size=1920,1080 --screenshot="$PWD/sample.png" "file://$PWD/index.html"
```

## 已实测的库坑（详见方案文档 §1.3）

自定义笔 pressure 隐性必填 / 流场用完必须 noField / **brush.spline 两点=静默不渲染** /
wash 前必须 noStroke / 'ink' 非内置笔（内置：pen/rotring/2B/HB/2H/cpencil/pastel/crayon/charcoal/spray/marker）。
