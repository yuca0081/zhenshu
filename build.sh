#!/usr/bin/env bash
# 统一构建入口：bin/ 下只认本脚本产物（m0.exe + server.exe）。
# 禁止手工放 server.exe.new / vaserver.exe 之类副本——改完代码跑本脚本即可。
# 用法：bash build.sh            （在仓库根执行；本脚本内部自行 cd）
set -euo pipefail
cd "$(dirname "$0")"

( cd server && go build -o ../bin/m0.exe ./cmd/m0 )
( cd server && go build -o ../bin/server.exe ./cmd/server )
echo "✅ bin/m0.exe bin/server.exe"
