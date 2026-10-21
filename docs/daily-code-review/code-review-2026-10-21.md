# 全面代码巡检报告 · 2026-10-21（21:31 增补轮）

- **巡检对象**：HEAD = `d03ef71`（PV 第0步原型闸全链，今晚 21:29 提交）
- **本轮性质**：今晚 20:40 例行轮已覆盖到 `744327a` 并出首版报告；本增补轮 = 细审其后唯一新提交 d03ef71 + 逐条复核首版全部发现 + 重跑机械检查 + 追踪 WIP 演进
- **工作区**：并行会话「样张检查」功能 WIP 进行中（改 `ai/font_tool.py`、`ai/snap_samples.py`、`pipeline/fontcatalog.go`、`prep/style.go` + 新 `samplecheck.go/_test.go`）——WIP 照常审，「未提交」本身不当问题
- **方法**：机械检查全跑 + 两路并行子智能体（后端复核+WIP / PV 原型链细审）+ 关键新断言主会话亲验（90s 掐字体下载、失败删旧图、右锚幽灵盒三条属实）

## 总评

首版抓的问题**一个都没被今晚的后续提交修掉**（d03ef71 是纯原型链提交，没碰 server/），2 高危 5 中危全部仍在，行号已重新钉死。新增两块内容：① d03ef71 的 PV 原型脚本链质量高于一般一次性脚本（复用而非复制、注释全是"为什么"型），真值得动手的只有 3 处（两处闸门报错不说人话 + tracks.json 原子写）；② WIP「样张检查」功能五个旧问题五个都还在，其中两个比上轮更严重——修复保险丝是永远不可能触发的死逻辑（三重矛盾），缓存键错位的实害路径已走通（未修复的原始 HTML 会绕过机械检查回流草稿）。另新抓一条 WIP 高危链条：单张截图 90 秒超时掐不过冷启动字体下载（最长 6 分钟），失败后正好落进「谎称机判全过」的分支。

---

## 一、首版发现复核（2 高危 + 5 中危全部仍在，行号重钉）

### 高危（2 个）

1. **`server/internal/llm/chat.go:87` + `provider.go:230,347`——磁盘缓存命中仍按全价记账，预算闸被假账打死**（仍在）
   缓存命中的响应照旧 `budgetAdd` 全价、照记 `agent.done`：同一请求重放 N 次账面涨 N 倍而真实花费只有 1 倍。本系统常态恰恰是取消/429 后同请求重放（P0-2 设计目标），重跑越密集 8M 预算闸越早打死健康项目——两个 P0 特性互相拆台。**新抓一刀**：budgetCheck 排在查缓存之前，超限时连免费的缓存命中都会被拒——假账不但多扣钱，还拦着免费的路。
   改法：缓存命中时 budgetAdd 记 0（或在 agent.done 打 replay 标记让播种剔除重放账），预算检查挪到缓存命中之后。

2. **`server/internal/llm/diskcache.go:106`——缓存写计数器无锁自增，确定性数据竞争**（仍在）
   `chatCache_.writes++` 被多个并发 goroutine（写画面并发 3 + 审核/续写）同时调，`-race` 必炸。
   改法：换 `atomic.Int64`。

### 中危（5 个）

3. **`server/internal/produce/bgmpick.go:267-271` + `pipeline.go:1968-1969`——`set_bgm off` 被自动选曲推翻**（仍在）
   off 走删键；「已有 bgm 永不覆盖」只认非空字符串，读到空就当「没配乐」，自动选曲照跑并在 bgmpick.go:305 写回。用户「不要配乐」的拍板被填空逻辑推翻。改法：off 写哨兵值或另设标记，pickBGM 见 off 跳过。

4. **`server/internal/produce/scenepatch.go:129-142`——patch 替换片段不校验 id 保持，静默丢元素**（仍在）
   模型交回 `<div id="别的">` 时旧节点被删新片段顶入，GSAP 按 id 找元素落空——动画失效且 sceneValidate/lint 都查不到。讽刺的是文件头注释（10-11 行）自己就写着「必须保持同一 id」。改法：套用前 ParseFragment 校验同 id 存在，否则整单作废走整份回退。

5. **`server/internal/pipeline/design.go:509-530`——接缝钳制只修了一半：漏 Entry（首帧侧）**（仍在）
   D5 契约「首帧即景+末帧定格」是对称条款、接缝闸两头都查，但 clampBoundaryFades 只钳 Exit/Exits/Transition 三个收尾侧字段，`EntryExitSpec.Entry` 没人管——「整场淡入」写进 entry 照样落盘，靠打回兜底，正是 87a97af 要消灭的「打回整链刹车」路径复现。改法：Entry 同过钳制，改写为「首帧即景」模板。另 `design.go:595-633` `hasUnnegated` 只回看关键词前 6 字节，「叠化可用，但本片不用叠化」这类先肯后否句式照样判成 xfade。

6. **`server/internal/produce/segcheck.go:481`（路径勘误：在 produce 包不是 pipeline）——接缝闸中段帧缺失静默失能**（仍在，机制比首版报的更清楚）
   `lumas[mid]` 对 mid 键不做存在性检查——中段帧缺失时 map 零值 mid=0，落入 `mid<40` 分支（floor=0），`mid>0 &&` 守卫又把相对塌陷关了：闸彻底失能且无 manifest 记账。对比：首/尾帧缺失（466-479 行）都有降级记账，唯独 mid 没有。改法：mid 缺失按首尾帧同款降级记账。

7. **`ai/make_bgm_v2.py:410`——tracks.json 非原子写，半截写坏全项目配乐挂**（仍在，且是本批最值得修的：清单是 Go 选曲/混音唯一事实源，v1 旧条目只活在这一个文件里，截断=真丢数据不是可再生）
   `write_text` 先截断再写；`json.loads` 读到损坏文件直接炸整轮（v1 make_bgm.py:232 同款）。改法：tmp + `os.replace` 原子替换，读失败备份重建。

### 低危（压缩列出）

**后端**
- `produce/segcheck.go:248` unreachable code（go vet 唯一发现，`for attempt := 1; ; attempt++` 无 break 出口）
- `design.go:513` 钳制正则不认否定——「叠化过渡、不用黑场」的合法叠化被碾成直切模板；与 :600 的 hasUnnegated 否定语义三处不一致
- `server.go:867` 删项目 Cancel 后不等退出即 RemoveAll（毫秒级竞速残口）
- `diskcache.go:63` 缓存键不含 BaseURL、temperature omitempty 致 temp=0 与未设互串（当前不触发，理论口子）；`:102` 缓存写失败静默 + .tmp 残留永不清理；`:31` 14 天窗口内无总量帽
- `scenegen.go:221` contRepair 两类无效输出兜底轻重不一（打不中有追加重试，缺字段直接退全新会话）
- `bgmpick_test.go` 8 项单测全在纯函数，编排层（不覆盖判定/off 语义/回喂重试）零覆盖——恰是 #3 bug 藏身处
- `make_bgm_v2.py:475` 单轨渲染失败静默沿用旧 mp3、manifest 挪位致库序 tie-break 漂移
- clampBoundaryFades 本身零单测

**前端（d03ef71 未动 web/，维持首版结论）**
- `ActivityPane.vue:313` 任务卡 run→ok 转换后停止刷新，最后 5s 窗口的收尾消息（含最终回复）可能永久缺失
- 10-17 遗留未修 2 项：PrepPane 终审样张仍无迷你真段、types.ts 死字段仍无消费方

## 二、d03ef71 PV 原型链细审（新增）

整体质量高于一般一次性脚本：复用 make_bgm_v2 而非复制、`__file__` 锚定路径（Windows 安全）、注释全是「为什么」型（贝斯 ProgramChange 落段落起点、word_stack 半小节账是真教训沉淀）。

**值得动手的 3 处：**
- `demo/pv-prototype/check_cuts.py:31,78-79`——ffprobe 失败返回 None 没人接，`abs(d - x)` 直接 TypeError，traceback 指向 TypeError 而不是「ffprobe 挂了/文件读不了」。闸门脚本的价值就在报错可排查。改：`if d is None: findings.append(...); continue`
- `demo/pv-prototype/check_frames.py:23-27,42-43`——解码失败被标成「帧空白」（空数组→std=-1.0→判 EMPTY），排查方向被带偏；`(g>150).mean()` 对空数组打 RuntimeWarning 和 nan。改：解码失败单独报一行
- `ai/make_bgm_v2.py:410` 原子写（即中危 #7，账本类唯一不可再生文件）

**顺手件（一行级）：**
- `make_pv_track.py:282`——280 行守卫修了一半，「有 verse 无 chorus」时 `max(空序列)` 仍会 ValueError（当前 self_check 吃硬编码段表触发不了，潜伏缝）；条件补 `chorus and`
- `demo/pv-prototype/verify_beats.py:58`——硬编码 hop 512，`analyze_audio.py:15` 有现成 `HOP=512` 常量却没 import；上游改 hop 这里的 onset 时间轴静默全歪。改：`from analyze_audio import HOP`
- `demo/pv-prototype/gen_shots.py:292`——死 mkdir（321 行 out.parent.mkdir 已全覆盖）；shots.json 的 `n` 字段是装饰品（gen_shots 不读，改了也不生效，防后人误以为有用）

**原型容忍（转正前处理）：** `make_pv_track.py:301` argv 越界（崩得响不算静默）、`gen_shots.py:316` words 缺省时 JS 渲出字符串 "undefined"（不炸但出鬼字）、`make_pv_track.py:265` `secs[3]` 硬编码下标取副歌、free_ch 6 通道池 StopIteration（当前只用 4 乐器）。

## 三、WIP「样张检查」功能（比首版更明确，合并前先修 a/b/e）

上轮五个形状问题**五个都还在**，其中两个升级：

- **c 升级（死上加死）**：`style.go:385` 定义 `sampleRepairBrake=12`、489 行硬编码 `inner > 6`——而这个保险丝**永远不可能触发**：`round >= sampleRepairMax`(=2) 先 break，inner 每轮最多 +1 到不了 3，何况 6/12。三重矛盾，建议要么删常量要么把逻辑接对。
- **d 升级（实害路径走通）**：`style.go:508-512` 入库的是修复版，532 行 `markRenderCache` 记的却是模型原始版。后果链：模型重发同一份原始 HTML → dup 比对（677-680 行，比对草稿里的修复版）失配 → 走 hash 命中回填分支（432-457 行）→ **把未修复的原始版当新样张追加进草稿，机械检查白做**。

其余三个维持：a）`samplecheck.go:97,119,123` ToLower 变字节长度后坐标错位，211 行 lowered 比 html 长时直接越界 panic；b）`:182` box.text 未剥标签，img/src 属性字母被当文本误报叠字；e）`style.go:472-475` round 0 截图失败即 break 丢当轮 mechIssues，529 行回喂文案谎称「机检与判卷全过」（480-481 判卷降级分支同病）。

**本轮新发现（WIP）：**

- **【链条级，建议按高危对待】90 秒掐不过冷启动字体下载**：`style.go:650` 单张截图整条命令 90s 超时，但 `snap_samples.py:77` 的 `--one` 模式里 `ensure_fonts` 同步跑 font_tool.ensure（下载 3 次重试 × 120s 超时、zip 上限 64MB）——冷启动首次样张触发字体下载必被掐死，然后正好落进 e 项「截图失败谎称全过」。改：ensure 只在 submit 的 6 分钟档做，`--one` 模式跳过或有产物才用。
- `samplecheck.go:147-167` 右/下锚点盒子几何镜像：`right:R` 锚定缺 width 时幽灵盒落在锚点**右侧** `[W-R, W]`，真实元素向**左**延伸——叠字判定对这类元素既可能误报也可能漏报。改：右锚缺宽直接 continue 放过（同 175 行无高度的待遇）。
- `samplecheck.go:33` `reDrawDef` 只认 `function(t` 形态——箭头函数或参数名 `(time)` 的合法纯函数被判「没有 __draw_ 纯函数」打回重修（误报）。
- `ai/snap_samples.py:89` snap_one 失败分支 `unlink` 把上一轮截好的 PNG 删掉，Go 侧 note 却仍宣称样张在 style/sample-N.png。改：失败时不删既有产物。
- 小项：`samplecheck.go:45` reFontFamily 死代码（实际用 fontface.go:41/43）；snap_samples.py `--one` 传错目录也静默 mkdir 掩盖错误；font_tool.py `_ttf_from_zip` 重复 `import io`；samplecheck_test.go:41-43 注释与断言自相矛盾；style.go judge/repair 调用失败也 `loop.Calls++` 账虚高。

**WIP 总评**：新功能主体（机检五件套 + 判卷修复环 + 字体闸）结构是对的，测试过；但 a/b/e 三个会直接产出错误回喂文案 + 90s/字体下载链条，建议合并前修掉这四个。

## 四、机械检查（本轮全跑）

`go build` ✅ / `go vet` 1 发现（`produce/segcheck.go:248` unreachable code，同首版）/ `go test` 16 包全过 ✅ / 前端 `npm run build` ✅（chunk 体积警告为老现象）/ py_compile 新改脚本（含 PV 链全部 6 个）✅ / **gofmt 0 漂移** ✅。

## 五、修复优先级建议

1. **#1 缓存记账假账**（顺带把 budgetCheck 挪到缓存之后）+ **#2 原子计数器**——一行级修复收益最大，直接决定预算闸还信不信账本
2. **WIP 四件套**：90s/字体下载链条 + a/b/e——合并前必须清，否则样张检查上线第一天就产假「全过」文案
3. **#5 Entry 侧钳制**——接缝闸已经两头查，钳制补对称就能把「打回整链刹车」真正关死
4. **#3 set_bgm off**——用户拍板被推翻是信任问题，配 pickBGM 编排层测试
5. #4 patch id 校验、#6 mid 帧记账、#7 tracks.json 原子写、PV 闸门三处报错说人话——可攒一批；低危继续攒
