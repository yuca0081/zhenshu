# 音乐驱动 PV · M1 音乐工位真机冒烟实录（2026-10-23）

结论先行：**过**。8081 独立实例（共享 DATA_DIR，不动并行会话在用的 8080）真 LLM 全链：
建音乐项目 → 主题做曲 → 三件套落盘 → 试听接口出声 → 前端想法页签渲染验收，
三道拦截口全对。首跑抓到 1 枚真伤（产物名不匹配）已修，复跑 20 秒干净通过。

## 链路与结果

| 检查项 | 结果 |
|---|---|
| 建项目（mode=music，UTF-8 文件 body） | ✓ p20261023-103806 |
| runPrep 无主题 | ✓ 400「要先给主题」 |
| runPrep（真 LLM）首跑 | ✗ 抓到真伤（见下） |
| force 重跑（修后） | ✓ 20s 完成：r1 compose_track → r2 submit_music |
| 三件套落盘 | ✓ brief.json（Am/128BPM/48 小节/8 段）＋ track.mp3（90.00s）＋ music_map.json（段落/能量弧全对） |
| GET /music/audio | ✓ 200，2.16MB，90.00s，mean_volume −24.3dB（有乐声非静音） |
| confirmPrep | ✓ 400「PV 打包门随 M2」 |
| startProduce | ✓ 400「PV 管线开发中」 |
| token 账 | ✓ 2 次调用，3129＋352 token；会话日志 llm/prep-music.jsonl 在 |
| OnDone 聊天通知 | ✓「✅ 整曲完成（…）——「想法」页签可试听」 |
| GUI 验收（浏览器） | ✓ 主题回填/brief chips/地图 8 行能量条/重新做曲武装确认/audio 元素 src 正确 |

## 首跑真伤与修复

**产物名不匹配**：python 按曲名出 `Neon-Midnight.mp3`，Go 认 `music/track.mp3`
——submit_music 找不到曲子被拒，agent 误判要重做，烧穿做曲封顶与回合预算
（12 回合耗尽）。**修**：`--brief` 通道渲完改名固定 `track.mp3`（产物名与创作名
解耦，重做换名不再产生孤儿文件）。**连带修**：地图摘要标签写反（「拍格 2ms/小节
0.47s」→「一小节 2.00s、一拍 469ms」）。

**教训**：跨端契约（脚本产物 ↔ Go 工件路径）必须在工具边界收口成常量，不能
两边各自默认；「能产出」不等于「产在约定路径」。

## 顺带发现（不拦 M1，挂账）

- agent 首跑自选了 8 段结构（两段 build 各 4 小节、intro 8 小节 E2）——lint 全过，
  曲子成立；段落弧线的「品味」属样张迭代范畴，不进机械执法。
- brief lint 双层校验生效：Go 层就地打回省 python 往返，python 层权威复核。
- 第二实例对账把 p20261023-092920 的卡死 status 补写了终态（该制作早已
  produce.error 在案，非在跑任务，无干扰）。

## 复现

```
bash build.sh && ADDR=127.0.0.1:8081 ./bin/server.exe &
# 建音乐项目（中文 body 走 UTF-8 文件 + --data-binary，Win curl GBK 坑）
python ai/make_pv_track.py --brief brief.json --out DIR   # 脚本单测：exit 0/2/3 三路验过
```
