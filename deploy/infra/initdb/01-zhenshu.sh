#!/bin/bash
# 首次启动（/opt/docker/postgres/data 为空卷）时由 postgres 官方镜像 entrypoint 执行。
# 之后的重启/重建容器不会再跑——改角色密码请直接 psql。
set -e
# initdb 目录以 :ro 挂载（不可执行位时 entrypoint 会 source 本文件），环境变量可用
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-EOSQL
	CREATE ROLE zhenshu LOGIN PASSWORD '${ZHENSHU_PG_PASSWORD}';
	CREATE DATABASE zhenshu OWNER zhenshu;
EOSQL
