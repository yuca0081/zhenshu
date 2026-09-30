# TTS 付费档评估（CosyVoice2 / 硅基流动）

> 日期：2026-09-28。对应 `docs/upgrade-plan.md` P2-5「音频升级：TTS 付费档评估」。
> 结论先行：**接入已完成、成本可忽略，推荐正式采用为默认档**（配 key 即生效，无需其他改动）；
> 唯一待办是音色 A/B（alex vs benjamin/charles/david），建议出 2 条 60s 对照样片人工定。

## 1. 现状链路（本次落地后的完整形态）

引擎优先级（`ai/tts_align.py`，逐段级联、前者失败落后者）：

1. **CosyVoice2（硅基流动付费档）**——`SILICONFLOW_API_KEY` 配了即默认启用；
   `TTS_MODEL` / `TTS_VOICE` 环境变量可换模型与音色（默认 `FunAudioLLM/CosyVoice2-0.5B:alex`）。
2. **Edge TTS（微软免费）**——无 key 时的默认；`EDGE_TTS_VOICE` / `EDGE_TTS_RATE` 可调。
3. **Kokoro（hyperframes 离线）**——断网/全挂应急兜底。

项目级音色路由（`project.json "voice"` 字段，agent 工具 `set_voice` 设置）：

- 值是 Edge 音色（中文名/完整 id）→ 锁 Edge 引擎全量重配；
- 值是 CosyVoice 音色（短名 `alex/benjamin/charles/david` 或完整 id）且配了 key
  → 锁付费档；未配 key 打警告回落 Edge 晓晓（不静默降级到 Kokoro）；
- 音色变更由 `audio_meta.json` 的 voice 字段检测触发全量重配（既有机制，不变）。

Go/Py 两端音色表由 `voice_alias_test.go` 钉住防漂移。

## 2. 定价与成本测算

硅基流动 CosyVoice2-0.5B：**¥105 / 1M UTF-8 字节**（中文一字 3 字节，官方页为准）。

| 场景 | 旁白字数 | UTF-8 字节 | 成本 |
|---|---|---|---|
| 60s 测试片 | ~180 字 | ~0.5 KB | ¥0.00006 |
| 4 分钟科普片 | ~600 字 | ~1.8 KB | ¥0.0002 |
| 10 分钟长片（含重配 3 次） | ~1500×4 字 | ~18 KB | ¥0.002 |

**结论：成片级用量成本在厘级，评估"要不要花钱"本身比花钱贵。** 与生图付费档
（IMG_GEN_HD，数分/张）不同档：TTS 付费档可以放心设为"配 key 即默认"，
不需要项目级确认。

## 3. 质量对比（已有证据 + 待办）

- 已有共识（M-A 前 Edge TTS 切换时的听感）：Edge 晓晓"免费自然"，
  在免费档里明显好于 Kokoro；CosyVoice2 是再上一档（官方定位对标商业 TTS，
  中文韵律/多音字更稳）。
- **待办 A/B**：alex（默认）vs benjamin/charles/david，科普解说气质定一个。
  方法：同一条 60s 文稿，`set_voice alex` 出一条、`set_voice charles` 出一条，
  盲听定档后把胜者写进 `.env` 的 `TTS_VOICE`（或保持 alex 默认）。
  这一步需要真实 key 出片，留给账号持有者做（一次性、几分钟）。

## 4. 风险与对策

| 风险 | 对策 |
|---|---|
| API 失败/限流 | 既有级联回落 Edge（免费），不会挡片 |
| 余额耗尽 | 同上——回落链是按段级联的，中途挂只影响当段引擎选择 |
| 计费口径变化 | 官方价格页为准；成本敏感性极低，不构成架构约束 |
| 声音克隆/参考音频档位 | 未启用（涉授权与合规）；如需人声克隆走 SiliconFlow 复刻接口另评估 |

## 5. 建议

1. 正式采用：`.env` 填 `SILICONFLOW_API_KEY` 即是"付费档为默认"（已是现状行为）；
2. 跑一次四音色 A/B（§3），定档后无需改代码（`TTS_VOICE` 或 `set_voice` 均可）；
3. 本文档结案后，`upgrade-plan.md` P2-5 的"TTS 付费档评估"项可勾销。
