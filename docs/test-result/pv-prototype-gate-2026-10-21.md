# 音乐驱动 PV · 第 0 步原型闸实录（2026-10-21）

结论先行：**闸过**。曲库后端出 80s 整曲＋谱面地图，半自动走通 40s 副歌片段
（副歌1＋break＋副歌2）全链，机械闸全绿，看片判合格（WARN 两项设计级
意见随修一项、留一项进样张迭代）。零新依赖。产物在 `demo/pv-prototype/`，
方案见 `docs/pv-music-driven-design.md`。

## 走了什么链

| 工位 | 代演物 | 产物 | 结果 |
|---|---|---|---|
| ① 音乐工位·做曲 | `ai/make_pv_track.py`（M1 种子） | `pv_proto_am120.mp3` 80.00s，七段能量弧，120BPM/40小节 | ✓ |
| ① 音乐工位·地图 | 谱面直出 | `music_map.json`（161 拍格/41 小节线/7 段/能量曲线） | ✓ |
| ① 自检环 | 脚本内机械断言 | BPM/时长带/副歌能量弧/渲染时长四项对 brief | PASS |
| 地图校验（DSP） | `verify_beats.py` | onset 260 个 → 16 分格 mean 13.8ms / p95 52.4ms，首拍 -12ms | PASS |
| ② 分镜（人演） | `shots.json` | 10 镜头 × 4s，全钉小节线，带 brief/首末帧承诺 | ✓ |
| 写画面（模板代演） | `gen_shots.py` → 10 个 HTML | `__renderFrame(t)` 纯函数，动效量化到 0.5s 拍格 | ✓ |
| 段渲染 | `demo/render_frames.js` 零改动 | 10 × 120 帧串行 ≈ 8.5min，~50s/镜头 | ✓ |
| 程序 check | `check_cuts.py` | 切点 11 个 max 0.0ms（容忍50）；时长 10 个 max 0.0ms（容忍100）；歪切点自测能抓 | PASS |
| 拼接挂原曲 | `build_pv.py` | concat＋定位挂轨＋尾淡出 300ms | `pv_final.mp4` 40.00s，账面 0ms |
| 全片终审 | `check_frames.py`＋multimodal 看片 | 帧非空 10×3 采样全有结构；看片 WARN（见下） | 修后 PASS |

走链片段：bars 16–36（t 32–72s）＝ 副歌1 16s ＋ break 8s ＋ 副歌2 16s。
终片 `demo/pv-prototype/pv_final.mp4`（1080×1920，40.00s，4.2MB）。

## 看片结论（multimodal，1fps 采样）

- 切点完整 PASS：9 个切点无黑屏/撕裂/半帧混合。
- 节拍感 PASS：镜头2 横条 1→3→5→6 条/秒与 0.5s 网格自洽、镜头8 每 0.5s
  步进 90° 精确复位、镜头10 逐拍脉动可见；**镜头3（同心环）节拍感最弱**
  （帧间变化微弱，设计使然，进样张迭代观察名单）。
- 段落能量匹配 PASS：break 明显转冷转慢，24s 副歌2 回归是全片最大反差。
- 画面健康 PASS：无乱码/叠字/锯齿；冲击帧的半透明重影形态一致、判为风格。
- **抓到真伤**：shot09「再闪一次」按小节点亮但镜头只有 2 小节，切走时
  「一次」两字永远点不亮——分镜意图与镜头时长的账错。已修（改每半小节
  点亮一字，4 字×1s 恰好收满），修复后机械复检亮占比梯度
  2.46%→4.49%→6.80%（1字→2字→4字）。

## 缺口清单（拉普拉斯妖式，按优先级）

1. **DSP 裸自相关 BPM 不可信（模式B 的 P0）**：真值 120 估出 122.90——
   整数帧滞后处两座等高峰（123/61.5 半速），真峰卡在帧缝被插值带歪；
   固定网格逐拍比对会累积出 1.7s **假漂移**。上传曲的地图脚本不能裸用
   `estimate_bpm`，需要：人工确认拍口子／分段局部 tempo／16 分格 comb
   过滤。谱面直出路线（后端#0）不受影响——DSP 只当校验器，且主判据应
   是「onset→网格贴合度」而非「BPM 估计」（`verify_beats.py` 已按此重写）。
2. **事件密度账缺机械不变量**：shot09 那类「N 元素×cadence vs 镜头时长」
   的账错是人审抓的。M2 写画面注入时要把「逐元素序列 cadence×时长 ≥
   元素数」做成 check 级 lint。
3. **写画面未真测（M2 核心风险）**：模板代演≠LLM 按镜头 brief＋节拍事件
   表生成拍格量化动效代码。LLM 能否稳定写「__renderFrame 纯函数＋拍格
   吸附」是下一个闸。
4. **重触发动效的空内容帧**：title_slam 透明度从 0 重触发，小节边界 2 帧
   整帧纯色（σ=0.0），机械检查抓到。动效 lint 需要「重触发必须有
   透明度/位置下限」。
5. **帧非空判据要收窄**：极简镜头 σ≈10 与真伤 σ=0 差一个量级；σ<3 判空、
   σ<15 记 LOW 不拦（机械执法范围越窄越稳，又一次验证）。
6. **音视频毫秒对齐靠 -shortest 兜底**：全片终审应加 ffprobe 音/视频流
   时长差核对（<50ms）。
7. **审片采样密度天花板**：multimodal 1fps 采样推不出 0.5s 级节拍（其
   自我声明）。印证设计分工：0.5s 级卡点归 check 机械判，看片只判
   「对情绪/对味」。
8. **分镜派生规则未建**：镜头边界现在是人手排的；M2 分镜工位需要
   「sections＋energy_curve → 镜头边界/密度」的提示词＋机械校验。
   shots.json 的 promise 字段还是描述性文本，未结构化（首末帧承诺要能被
   check 才算数）。
9. **风格单一**：10 镜头全是手写模板，风格库/素材工位未接，审美上限
   与像素感未测（M2+）。
10. **成本未记账**：原型闸全机械＋1 次看片（约 15.8 万子智能体 token）；
    真实链（音乐工位 agent＋写画面×N＋镜头审）的 token 账 M1/M2 各自带。

## 工程接线欠账（M1 落地时要补）

- `make_pv_track.py` 的 BRIEF/SECTIONS 硬编码在脚本里，音乐工位 agent 的
  brief→编曲参数映射未建（工具接口白名单化）。
- 渲染串行 8.5min：段级流水线的 ≤4 并行调度直接复用即可，未接。
- 曲库清单（tracks.json）还没有整曲条目形态（节拍/段落/能量 JSON 侧车）。

## 复现

```
python ai/make_pv_track.py            # 出曲+地图+自检环
python demo/pv-prototype/verify_beats.py   # DSP 交叉验证
python demo/pv-prototype/gen_shots.py      # 分镜→镜头HTML
for i in 01..10: node demo/render_frames.js pv-prototype/shots/shot$i.html demo/pv-prototype/renders/shot$i 30 4 1080 1920
python demo/pv-prototype/check_cuts.py     # 卡点机械闸
python demo/pv-prototype/build_pv.py       # 拼接挂原曲
python demo/pv-prototype/check_frames.py   # 帧非空机械检查
```
