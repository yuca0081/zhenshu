# 帧述 app+renderer 一体镜像（单容器形态：app 进程内直接 exec node/python/ffmpeg，
# tech-stack §5 的两容器是愿景形态，代码现实是进程内编排，按现实部署）。
# 构建位置：服务器（开发机无 docker；构建产物已由 deploy.sh 在开发机交叉编译好）。
FROM node:22-bookworm-slim

# ffmpeg：渲染合成；chromium：hyperframes 无头渲染；Noto CJK：中文字形（缺了全豆腐块）；
# postgresql-client：部署时跑 migrations（deploy.sh 经 compose run 调用）
RUN apt-get update && apt-get install -y --no-install-recommends \
      ffmpeg python3 python3-pip chromium fonts-noto-cjk ca-certificates tini \
      postgresql-client \
    && rm -rf /var/lib/apt/lists/*
ENV PUPPETEER_EXECUTABLE_PATH=/usr/bin/chromium

# 锁版渲染/合成 CLI 预装入镜像（runCLI 用 npx --offline，必须本地已缓存）
RUN npm i -g hyperframes@0.8.55

# ai/ 管线 Python 依赖（requirements.txt 单一来源；jieba/soundfile 缺失曾致容器内
# TTS 对齐必崩。funasr+torch 约 2GB 不装，走 faster-whisper 降级档；
# rembg+onnxruntime 为素材工位依赖，镜像 +约180MB，可接受——vend-stage.md §4）
COPY ai/requirements.txt /tmp/requirements.txt
RUN pip3 install --break-system-packages -r /tmp/requirements.txt && rm -f /tmp/requirements.txt
# u2netp 模型（4.7MB）构建期预下，运行期零下载（模型下载是最易断的环节）
RUN python3 -c "from rembg import new_session; new_session('u2netp')"
ENV HF_ENDPOINT=https://hf-mirror.com
# whisper 模型缓存落数据卷（compose 挂 /app/data/hf），重建容器不重下 ~1.5GB
ENV HF_HOME=/app/data/hf

WORKDIR /app
# 开发机交叉编译/构建产物（deploy.sh 生成）
COPY app-linux /app/app
COPY web/dist /app/web/dist
COPY ai /app/ai
COPY server/migrations /app/server/migrations

# root = DATA_DIR 上一级（main.go 推断逻辑要求 root 下有 ai/），CWD 必须是 /app
ENV DATA_DIR=/app/data ADDR=:8080
EXPOSE 8080
ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["/app/app"]
