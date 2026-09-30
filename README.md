# webpage-video-agent v2

AI 视频生产平台的第二代架构:**模型当创意总监,平台当工厂和 CI** —— 模型为本片发明设计系统并直接编写场景代码(HTML/CSS/JS),平台负责运行时契约、分级质检、确定性渲染(HyperFrames 逐帧截图 + FFmpeg)、段级重生成与批量。

**必读文档:[docs/plan-v2.md](docs/plan-v2.md)(立仓方案总纲,2026-09-30 定稿)** —— 架构决策、两个笼子(运行时契约/设计系统先行)、检视分级、迁移清单、里程碑全部在里面。

## 与 v1 的关系

- v1 仓库(`webpage-video-agent`)冻结于 tag `v1-baseline`,是同题 bake-off 的对照组,不再开发;
- v2 的中间层 = 「设计系统先行 + 模型直写场景代码」,替换 v1 的「语义 spec → StylePack 引擎」;
- 分镜上游、TTS/时序、确定性渲染下游、段级结构原封继承。

## 开发宪法(全量三条)

1. 永远只有一条主路径;
2. 能力增长走数据和插件(methods/few-shot/场景插件),不走代码分支;
3. styles/templates 停止扩编,降级为 few-shot 弹药库。

## 目录

`server/` Go 管线编排 · `ai/` 渲染引擎资产与 Python 工具 · `web/` 三栏工作台 UI · `data/library/` 风格/模板/方法库(few-shot) · `docs/` 方案与拆解笔记(`docs/v2-analysis/` = 11 条 Opus5.5 爆片逐帧评审)
