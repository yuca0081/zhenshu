# server — 帧述后端（Go）

> M0 当前形态：`cmd/m0` 管线 CLI（无 UI、无数据库依赖），LLM 走**会话模式**（文件契约）。
> 架构与阶段语义见 `docs/plan.md` §3/§4，选型见 `docs/tech-stack.md`。

## 目录

```
server/
  cmd/m0/                 # M0 管线 CLI
  internal/
    config/               # .env 配置加载
    contract/             # 数据契约：文稿/分镜/风格样张/元素注册表 + 校验（三前置硬门）
    pipeline/             # 阶段运行器 + LLM 请求模板（prompt 库 v0）
  migrations/0001_init.sql# 全部建表 DDL（PG，M1 接入时执行）
```

## 管线阶段（固定顺序 = 依赖方向）

```
manuscript → storyboard → style_samples → tts → compositions → check → render → stitch
   粘贴文      LLM#1          LLM#2        引擎    LLM#3(每段)    门禁    段级并行    FFmpeg
```

- **每步产物落盘**：重跑自动跳过已完成阶段，失败从任意步重放（manifest.jsonl 记录全部事件）。
- **三前置硬门**（代码强制）：文稿 + 分镜（schema 校验）+ 风格样张（`confirmed=true` 才放行）。
- 阶段状态：`✓ 完成` / `⏸ 等待 LLM 产物` / `∘ 待实现`（TTS/渲染环境后续里程碑接入）。

## 会话模式工作流（LLM_MODE=manual）

```bash
go run ./cmd/m0 new ../data/sample-article.txt "天空为什么是蓝的"
go run ./cmd/m0 run p20260922-xxxx     # 跑到 ⏸，打印等待哪个产物文件
#    → AI/人工按 llm/*.request.md 的契约写 llm/*.json
go run ./cmd/m0 run p20260922-xxxx     # 重跑续上（已完成的阶段自动跳过）
go run ./cmd/m0 status p20260922-xxxx  # 查看产物与 manifest
```

LLM 作业契约（请求 md → 产物 json）：

| 作业 | 请求 | 产物 | 校验 |
|---|---|---|---|
| 分镜 | `llm/storyboard.request.md` | `llm/storyboard.json` | `contract.ValidateStoryboard` |
| 风格样张 | `llm/style_samples.request.md` | `llm/style_samples.json` | `ValidateStyleSamples`（含 confirmed 硬门） |
| 段合成物×N | `llm/comp-segNN.request.md` | `llm/comp-segNN.json` | html 非空 + elements 非空（元素命名规范） |

切 API 模式：`LLM_MODE=api` + `LLM_API_KEY`（默认 GLM 编码套餐端点，任何 OpenAI 兼容平台改 `LLM_BASE_URL`/`LLM_MODEL` 即换）。

## M1 前置事项（服务器侧）

1. `postgres18` 容器加端口映射 `-p 5432:5432`（当前 PORTS 为空，外部不可达）；
2. 阿里云安全组放行 `5432`/`9000`（建议仅白名单新服务器 + 开发机 IP；2026-09-22 实测均不通）；
3. 建库建账号并执行 `migrations/0001_init.sql`。
