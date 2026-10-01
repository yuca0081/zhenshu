# 部署（47.94.247.12）

一键部署（开发机 Git Bash）：

```bash
bash deploy/deploy.sh --env   # 首次（上传 .env）
bash deploy/deploy.sh         # 日常发版
```

前置（一次性）：

1. **SSH 免密**：开发机 `ssh-copy-id root@47.94.247.12`（服务器只收 publickey）。
2. **服务器 .env**（`--env` 上传即自动改写）：`PG_DSN` 的 host 自动替换为 `127.0.0.1`
   （容器 host 网络，本机回环直连宿主机 PG，不经安全组）；
   需有 `LLM_MODE=api`、`LLM_API_KEY`、`SILICONFLOW_API_KEY`（TTS 走 API 免本地模型）、
   **`API_TOKEN`（必配，见下）**。
   注意：PG 连不上时服务会拒绝启动（fail-fast）；容器内 ffmpeg 在 PATH，无需 FFMPEG_BIN。
3. **服务器安全组**：放行 8080（试用访问入口）。
4. **API_TOKEN（公网鉴权，2026-09-29）**：`.env` 里 `API_TOKEN=<openssl rand -hex 24>`。
   配置后所有 `/api` 请求需带 `X-API-Token` 头（SSE 走 `?token=`）；前端首次打开会
   提示输入一次并存 localStorage。未配 + 非回环绑定时服务启动会打警告——8080 对公网
   开放而无 token，任何人都能匿名烧 LLM 余额、上传 200MB 文件。

形态与取舍：

- **单容器**（`deploy/compose.yml`）：app 进程内直接 exec node/python/ffmpeg（代码现实如此，
  tech-stack §5 两容器是愿景形态）。host 网络 + `restart: unless-stopped`。
- **镜像内预装**：node22 + hyperframes@0.8.55（npx --offline 走本地）、ffmpeg、chromium
  （hyperframes 无头渲染）、fonts-noto-cjk（中文必需）、faster-whisper（字级对齐）。
- **对齐引擎取舍**：服务器不装 funasr+torch（约 2GB），对齐走 faster-whisper 降级路径
  （词级→字级均分）。模型首次运行时经 hf-mirror 下载（medium，约 1.5GB）。
- **数据**：`/opt/zhenshu/data/projects` + `/opt/zhenshu/data/library` + `/opt/zhenshu/data/references`
  （参考视频库）+ `/opt/zhenshu/data/hf`（whisper 模型缓存，重建容器不重下）宿主卷，
  项目产物与风格库/方法库跨发版保留；migrations 幂等，按文件名序每次部署重跑（psql 容器内执行）。
- ~~素材库远程镜像~~（2026-10-01 退役）：素材库已随元素库/素材库退役一并移除
  （catalog/MinIO 镜像，见 `server/internal/stylelib`——风格库为唯一风格事实源）。
- **发版物**：开发机交叉编译 linux 静态二进制 + web/dist + ai/（引擎+素材+注册表同在此包）→ tar 流传输。
