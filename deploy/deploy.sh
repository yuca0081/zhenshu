#!/usr/bin/env bash
# 一键部署：开发机构建 → tar 流传输 → 服务器迁移 + 构建镜像 + 起容器。
# 用法（Git Bash / Linux）：
#   bash deploy/deploy.sh            # 常规部署（远端 .env 已就位）
#   bash deploy/deploy.sh --env      # 同时上传 .env（首次部署或改配置时）
# 服务器目录：/opt/zhenshu（data/projects 为宿主卷，项目产物跨发版保留）
set -euo pipefail

HOST=${HOST:-root@47.94.247.12}
REMOTE=${REMOTE:-/opt/zhenshu}

cd "$(dirname "$0")/.." # 仓库根

echo "▶ 1/4 交叉编译 linux 二进制"
( cd server && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" -o ../deploy/app-linux ./cmd/server )

echo "▶ 2/4 构建前端"
( cd web && npm run build >/dev/null )

echo "▶ 3/4 传输产物 → $HOST:$REMOTE"
ssh "$HOST" "mkdir -p $REMOTE/data/projects $REMOTE/data/references $REMOTE/data/hf $REMOTE/deploy"
# 精确清单 = 运行时所需；不含 .env（敏感，--env 单独传）与 data/projects（宿主卷自有数据）
tar --exclude='__pycache__' -cf - ai web/dist server/migrations deploy/Dockerfile deploy/compose.yml |
  ssh "$HOST" "tar -C $REMOTE -xf -"
if [[ ${1:-} == "--env" ]]; then
  scp .env "$HOST:$REMOTE/.env"
  # host 网络回环直连宿主机 PG/MinIO：服务器地址一律改写 127.0.0.1（LLM/TTS 等外部 API 地址不受影响）
  ssh "$HOST" "sed -i 's/47\.94\.247\.12/127.0.0.1/g' $REMOTE/.env"
  echo "  .env 已上传，服务器地址已改写为 127.0.0.1"
fi

echo "▶ 4/4 服务器：迁移 + 构建 + 起容器（首次 build 拉依赖约 5-10 分钟）"
# 迁移幂等（IF NOT EXISTS），按文件名序跑全部 *.sql（手工枚举会漏新增迁移）；
# psql 走容器内 client，host 网络连宿主机 PG
ssh "$HOST" "cd $REMOTE/deploy && \
  docker compose build app && \
  docker compose run --rm --no-deps app sh -c 'for f in server/migrations/*.sql; do psql \"\$PG_DSN\" -f \"\$f\" || exit 1; done' && \
  docker compose up -d app"

echo "✅ 部署完成 → http://47.94.247.12:8080"
echo "   ⚠ 公网可达：.env 务必配置 API_TOKEN，否则全部接口（含 LLM 计费调用/文件上传）对任何访客开放"
echo "   日志：ssh $HOST docker logs -f zhenshu-app"
