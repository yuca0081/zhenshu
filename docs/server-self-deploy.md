# 服务器自部署（服务器视角）

> 给**云服务器上的 agent/运维**照抄的部署手册：拉码 → 编译 → 构建镜像 → 迁移 → 起容器 → 验收。
> 开发机视角的一键流程见 [deploy.md](deploy.md)（deploy.sh 脚本不入库）。
> 本文档假设：PG/MinIO infra 已在跑（`docker ps` 能看到 postgres/minio）、`.env` 已就位于
> `/opt/zhenshu/.env`、服务器可拉取本仓库源码。秘钥一律不入库不进文档。

## 0. 目录布局约定

```
/opt/zhenshu/            部署根（数据与运行时，别动）
├── .env                 运行配置（秘钥；PG/MinIO 端点是容器服务名 postgres/minio）
├── data/                宿主卷：projects/library/references/hf——重建容器不丢
├── deploy/              构建+运行现场（compose.yml/Dockerfile/编译产物）
└── src/                 仓库源码工作副本（git 拉取更新）
```

源码 clone 到 `src/`（已有裸快照就换掉它：`mv src src.old && git clone <仓库> src`，
确认 `git log` 最新后删 `src.old`）。

## 1. 拉码/更新

```bash
cd /opt/zhenshu/src && git pull --ff-only && git log --oneline -1   # 记下部署的 commit
```

## 2. 编译后端（host 无 Go，进 golang 容器编；go.mod 要求 go 1.27）

```bash
docker run --rm -v /opt/zhenshu/src:/src -w /src/server golang:1.27 \
  sh -c "mkdir -p /src/build && CGO_ENABLED=0 GOOS=linux go build -ldflags '-s -w' -o /src/build/app-linux ./cmd/server"
```

- `CGO_ENABLED=0` 必须：产物是静态二进制，运行镜像（node:22-bookworm-slim）里没有 gcc。
- 首次会拉 golang:1.27 镜像；机器上已有 golang:latest 时先 `docker run --rm golang:latest go version`
  确认 ≥1.27 再复用。

## 3. 构建前端（host 无 Node，进 node 容器构建）

```bash
docker run --rm -v /opt/zhenshu/src:/src -w /src/web node:22-bookworm-slim \
  sh -c "npm ci --no-audit --no-fund && npm run build"
```

产物在 `src/web/dist/`。`npm ci` 失败（无 lockfile 时）退 `npm install`。

## 4. 产物落位 deploy/（compose 的 build 上下文就是这里）

```bash
cd /opt/zhenshu
cp src/build/app-linux deploy/app-linux
rsync -a --delete src/web/dist/ deploy/web/dist/
rsync -a --delete src/ai/ deploy/ai/
rsync -a --delete src/server/migrations/ deploy/server/migrations/
```

**不要把 `data/` 或 `.env` 弄进 deploy/**——build 上下文会整目录进镜像。

## 5. 构建 app 镜像 + 迁移 + 起容器

```bash
cd /opt/zhenshu/deploy
docker tag zhenshu-app:latest zhenshu-app:prev          # 留回滚点（首次部署跳过）
docker compose build app
docker compose run --rm --no-deps app sh -c \
  'for f in server/migrations/*.sql; do psql "$PG_DSN" -f "$f" || exit 1; done'
docker compose up -d app
```

- 迁移幂等（IF NOT EXISTS），按文件名序全量重跑；`--no-deps` 防误拉起 infra。
- compose.yml 若不在（新机器），照 §7 模板落一份；infra 外部网络名以
  `docker network ls` 实查为准（现网叫 `infra`）。

## 6. 验收

```bash
sleep 3
docker logs --tail 30 zhenshu-app          # 起动日志：PG/MinIO 连不上会 fail-fast 拒启
curl -sf http://127.0.0.1:8080/ >/dev/null && echo OK
```

回滚（build 前打过 prev tag）：

```bash
cd /opt/zhenshu/deploy && docker tag zhenshu-app:prev zhenshu-app:latest && docker compose up -d app
```

数据全在宿主卷，回滚只动镜像不动数据；迁移只加不改，降级二进制安全。

## 7. app 镜像构件（compose.yml / Dockerfile）

现网 `/opt/zhenshu/deploy/` 已有上一版（deploy.sh 每次随包同步）。新机器或要重写时，
**逐字用下面两份模板**——三条注释标了「雷」的行是实战烧出来的，删了哪条都是
"容器能起、渲染/对齐必崩、夜里才暴露"：

```yaml
# compose.yml —— app 容器：加入共享 infra 网络直连 PG（postgres:5432）/MinIO（minio:9000）
services:
  app:
    build: .
    image: zhenshu-app:latest
    container_name: zhenshu-app
    networks: [infra]
    ports:
      - "8080:8080"
    env_file: /opt/zhenshu/.env
    volumes:
      - /opt/zhenshu/data/projects:/app/data/projects
      - /opt/zhenshu/data/library:/app/data/library
      - /opt/zhenshu/data/references:/app/data/references
      - /opt/zhenshu/data/hf:/app/data/hf        # whisper 模型缓存，不挂则重建容器重下 ~1.5GB
    restart: unless-stopped

networks:
  infra:
    external: true
    name: infra
```

```dockerfile
# Dockerfile —— app+renderer 一体镜像（单容器：app 进程内直接 exec node/python/ffmpeg）
FROM node:22-bookworm-slim

# ffmpeg：渲染合成；chromium：hyperframes 无头渲染；Noto CJK：中文字形（缺了全豆腐块）；
# postgresql-client：部署时跑 migrations
RUN apt-get update && apt-get install -y --no-install-recommends \
      ffmpeg python3 python3-pip chromium fonts-noto-cjk ca-certificates tini \
      postgresql-client \
    && rm -rf /var/lib/apt/lists/*
ENV PUPPETEER_EXECUTABLE_PATH=/usr/bin/chromium   # 【雷1】hyperframes 找浏览器的唯一通道，没有它渲染必崩

RUN npm i -g hyperframes@0.8.55                   # 【雷2】必须 -g 预装钉版：渲染走 npx --offline，运行期没网

COPY ai/requirements.txt /tmp/requirements.txt
RUN pip3 install --break-system-packages -r /tmp/requirements.txt && rm -f /tmp/requirements.txt
                                                  # 【雷3】bookworm 的 pip 有系统保护，不带 --break-system-packages 必被拒
ENV HF_ENDPOINT=https://hf-mirror.com             # whisper 模型下载走镜像源
ENV HF_HOME=/app/data/hf                          # 缓存落数据卷

WORKDIR /app
COPY app-linux /app/app
COPY web/dist /app/web/dist
COPY ai /app/ai
COPY server/migrations /app/server/migrations
# root = DATA_DIR 上一级，且 root 下必须有 ai/（main.go 推断逻辑）——目录布局不能动
ENV DATA_DIR=/app/data ADDR=:8080
EXPOSE 8080
ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["/app/app"]
```

## 8. 蒸馏地基镜像（画材包蒸馏工位用，与 app 镜像无关）

```bash
cd /opt/zhenshu/src && docker build -t distill-base:1 distill/
```

详见 [distill/README.md](../distill/README.md)（工具清单/env 旋钮/开单方式）。
改过 `distill/tools/` 下任何脚本 = 升 tag 重建（`:1`→`:2`）并把 `DISTILL_IMAGE` 指过去。

## 9. 纪律

- `.env`、deploy key、任何秘钥：不进 git、不进文档、不进镜像（build 上下文只放 §4 清单）。
- 渲染期不许在容器里自由装包（缺件处置方针：部署时固化/白名单安装/降级三层）。
- 部署完把 commit 号记进运维日志；`docker system df` 超阈值再 `docker image prune -f`（只清 dangling）。
