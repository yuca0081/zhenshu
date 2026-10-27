# 蒸馏毛坯房（distill base image）

风格画材包蒸馏工位的地基（docs/style-kit-distillation-plan-2026-10-26.md §5）。
一次 build 反复用；**改 `tools/` 下任何脚本 = 升版本号重建**（`:1` → `:2`，判卷
口径与渲染行为必须跟地基版本一起可追溯），同时把 Go 侧 `DISTILL_IMAGE` 环境
变量指到新 tag。

## 烙地基（只在云服务器跑；本机 Windows 不装 Docker，不参与）

```bash
# <部署根> = 服务器上的部署目录（现网 /opt/zhenshu，含本仓库工作副本的 distill/ 子目录）
cd <部署根>
docker build -t distill-base:1 distill/
```

镜像内容：node:24-slim + chromium（SwiftShader 软光栅，WEBGL 画材包也能渲）
+ ffmpeg + 中文字体（判卷要看得清画面里的字）+ `/opt/distill-tools/`：

| 脚本 | 干什么 |
|---|---|
| `render.sh <html> <png> [WxH] [budget_ms]` | 无头 chromium 截帧（--use-angle=swiftshader） |
| `sheet.mjs <png> label:path …` | 拼对照网格照（真迹×还原同框带编号，判卷官一眼对齐） |
| `measure.mjs <png> …` | 判卷像素实测（尺寸/亮度分位/唯一色数/四边均值——黑边检测），stdout JSON |
| `licenses.mjs` | node_modules 实装依赖账（name/version/license），毕业检查③对账面 |
| `SPEC.md` | 房内规则书：四类语义 API/五坑/去题材化/判卷口径/交付契约 |

本地冒烟（可选）：脚本认 `CHROME_BIN` 覆盖浏览器路径，node 直跑 `.mjs`。

## 开单（懒触发：管理员单发，不自动）

```bash
curl -X POST -H 'X-API-Token: …' http://127.0.0.1:8080/api/library/<entryID>/distill
curl    -H 'X-API-Token: …' http://127.0.0.1:8080/api/library/<entryID>/distill   # 状态轮询
```

前提：条目有真迹帧、还没有 kit、蒸馏房空闲（**并发=1**，占用中回 409）。
任务目录 `data/library/distill/<entryID>-<ts>/`：`in/` 考卷袋（brief+真迹三帧，
题材 Subject 永不进袋）、`out/` 交付、`distill.jsonl` 画师对话留痕——留档排障，
不自动清理。

## 旋钮（env）

| 变量 | 默认 | 说明 |
|---|---|---|
| `DISTILL_IMAGE` | `distill-base:1` | 地基镜像 tag |
| `DISTILL_ROUNDS` | 48 | 画师回合预算 |
| `DISTILL_WALL_MINUTES` | 50 | 整单墙钟（到点带伤收卷） |
| `DISTILL_JUDGE_MODEL` | 同画师 | 判卷官模型（配了第二个视觉模型就分家，防自夸偏差） |

## 承重墙三规矩（代码钉死在 `prep/distill_room.go`，测试对账）

1. 屋里不放任何密码钥匙：LLM key 只在 Go 侧，容器只收 docker exec。
2. 用完销毁：`--rm` + 结束 `rm -f`，一单一房。
3. 交接只走 `out/`：出料只认清单文件（字符集白名单+单文件 2MB），路径守门
   防穿越；写文件走 stdin 管道（命令行传长内容会被通道静默损伤）。

npm 默认 `--ignore-scripts`（提示词纪律，容器兜底）。
