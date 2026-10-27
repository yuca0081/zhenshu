# 蒸馏毛坯房地基（docs/style-kit-distillation-plan-2026-10-26.md §5.2）。
# 一次 build 反复用；改 tools/ 下任何脚本 = 升版本号重建（:1 → :2），
# 判卷口径与渲染行为必须跟地基版本一起可追溯。
#
#   docker build -t distill-base:1 <部署根>/distill/     # 云服务器上（部署目录里的仓库工作副本）
FROM node:24-slim

# 无头浏览器（渲染+拼对照片）+ ffmpeg（排障抽帧）+ 中文字体（画面里的字要看得清）。
# chromium 走发行版包：浏览器烙在地基，不随每单下载 150MB。
RUN apt-get update && apt-get install -y --no-install-recommends \
      chromium ffmpeg fonts-noto-cjk \
    && rm -rf /var/lib/apt/lists/*

# npm 走国内镜像（云服务器实测可达）；画材包依赖每单房内自由装，不进地基。
RUN npm config set registry https://registry.npmmirror.com

# 烤进地基的工具脚本：渲染/拼对照/像素实测/许可证清单 + kit 规则书。
COPY tools/ /opt/distill-tools/
RUN chmod +x /opt/distill-tools/*.sh

USER node
WORKDIR /task
