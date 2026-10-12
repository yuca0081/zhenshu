#!/usr/bin/env bash
# 一键部署：开发机构建 → tar 流传输 → 服务器迁移 + 构建镜像 + 起容器。
# 前提：服务器已完成 infra（deploy/infra/ 的 PG+MinIO，/opt/docker/infra）并首次建库，
#       app 经 infra 桥接网络以服务名 postgres / minio 直连（见 deploy/compose.yml）。
# 用法（Git Bash / Linux）：
#   bash deploy/deploy.sh            # 常规部署（远端 .env 已就位）
#   bash deploy/deploy.sh --env      # 同时上传 .env（首次部署或改配置时）
#   HOST=new@1.2.3.4 bash deploy/deploy.sh   # 非 47.94.247.12 的服务器用 HOST 覆盖
# 服务器目录：/opt/zhenshu（data/ 各子目录为宿主卷，数据跨发版保留）
set -euo pipefail

HOST=${HOST:-root@47.94.247.12}
REMOTE=${REMOTE:-/opt/zhenshu}

cd "$(dirname "$0")/.." # 仓库根

echo "▶ 1/4 交叉编译 linux 二进制"
( cd server && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" -o ../deploy/app-linux ./cmd/server )

echo "▶ 2/4 构建前端"
( cd web && npm run build >/dev/null )

echo "▶ 3/4 传输产物 → $HOST:$REMOTE"
ssh "$HOST" "mkdir -p $REMOTE/data/projects $REMOTE/data/library $REMOTE/data/references $REMOTE/data/hf $REMOTE/deploy"
# 精确清单 = 运行时所需；不含 .env（敏感，--env 单独传）与 data/projects（宿主卷自有数据）。
# data/library 首次部署必须带上（风格库/方法库/BGM 在 git 里，vendor/fonts 不在但 tar 直拷工作区一起带）
tar --exclude='__pycache__' -cf - ai web/dist server/migrations data/library deploy/Dockerfile deploy/compose.yml |
  ssh "$HOST" "tar -C $REMOTE -xf -"
if [[ ${1:-} == "--env" ]]; then
  scp .env "$HOST:$REMOTE/.env"
  # infra 桥接网络：PG/MinIO 走服务名直连（外部 API 的绝对地址不受影响，如 LLM_BASE_URL/PUBLIC_BASE_URL）；
  # ADDR 强制 :8080——本地若写 127.0.0.1 原样进容器，端口映射将够不着
  ssh "$HOST" "sed -i -e 's/host=127\.0\.0\.1/host=postgres/g' -e 's|^MINIO_ENDPOINT=.*|MINIO_ENDPOINT=minio:9000|' -e 's|^ADDR=.*|ADDR=:8080|' $REMOTE/.env"
  echo "  .env 已上传，PG/MinIO 端点已改写为容器服务名"
fi

echo "▶ 4/4 服务器：迁移 + 构建 + 起容器（首次 build 拉依赖约 5-10 分钟）"
# 迁移幂等（IF NOT EXISTS），按文件名序跑全部 *.sql（手工枚举会漏新增迁移）；
# psql 走容器内 client，经 infra 网络连 postgres 服务
ssh "$HOST" "cd $REMOTE/deploy && \
  docker compose build app && \
  docker compose run --rm --no-deps app sh -c 'for f in server/migrations/*.sql; do psql \"\$PG_DSN\" -f \"\$f\" || exit 1; done' && \
  docker compose up -d app"

echo "✅ 部署完成 → http://${HOST#*@}:8080"
echo "   ⚠ 公网可达：.env 务必配置 SESSION_SECRET（或 API_TOKEN）与 INITIAL_ADMIN_PASSWORD，否则登录体系不设防，全部接口（含 LLM 计费调用/文件上传）对任何访客开放"
echo "   日志：ssh $HOST docker logs -f zhenshu-app"
