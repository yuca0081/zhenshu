#!/bin/sh
# render.sh <html> <out.png> [WxH] [budget_ms] —— 无头 chromium 截帧。
# SwiftShader 软光栅（WEBGL 画材包也要能渲）；virtual-time-budget 让定时器/字体
# 加载快进到稳定态再截。本地冒烟可 CHROME_BIN 覆盖浏览器路径。
set -e
CHROME="${CHROME_BIN:-chromium}"
HTML=$(realpath "$1")
OUT=$(realpath "$2")
SIZE="${3:-1280x720}"
BUDGET="${4:-9000}"
cd "$(dirname "$HTML")"
exec "$CHROME" --headless --no-sandbox --disable-gpu \
  --use-angle=swiftshader --enable-unsafe-swiftshader \
  --hide-scrollbars --force-device-scale-factor=1 \
  --window-size="$SIZE" --virtual-time-budget="$BUDGET" \
  --screenshot="$OUT" "file://$HTML"
