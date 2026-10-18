# 事件流升级:「过程」页 + 智能体对话展开 + token 记账

## 核心结论
能实现。token 用量 provider 每次调用都已返回、智能体对话在内存中完好组织,只是所有调用点用 `_` 丢弃、循环结束 msgs 就扔。本方案 = 后端把数据留住(对话文件+账行),前端开同级页签展示。

## 后端(约 350 行)

1. **新建 `server/internal/llm/transcript.go`**:Recorder 类型,一个任务一个文件 `llm/transcripts/<task-id>.jsonl`,对话逐条追加(运行中可看半程)。写入时减脂:工具结果截断(~4KB)、图片载荷换 `[图片]`。每任务一文件,避开三池并发写冲突。
2. **三个 ReAct 循环接住数据**:
   - `server/internal/react/react.go:74`、`server/internal/prep/loop.go:76`、`server/internal/review/review.go:95`
   - `msg, _, err := Prov.Chat(...)` 的 usage 接住累加;每轮消息喂 Recorder;结果结构体加 `Usage/Calls` 字段带出。接口签名不动。
3. **单发工位接住 usage**:prereview.go、frameqa.go、director.go、produce.go:1511(翻译)把 `_, rerr :=` 改接住;scenegen.go:232 从"只记最后一次"改为累计全轮。这些工位写一条极简对话记录(请求摘要+响应)。
4. **记账**:`pipeline.Manifest()` 加兄弟方法 `ManifestFields(event, detail, extra map[string]any)`,写额外 JSON 字段。任务开始/结束记 `agent.start`/`agent.done`,载荷 `{task, label, seg?, prompt_tokens, completion_tokens, calls, transcript}`。老账行格式不动。
5. **任务编号约定**:`prep.script` / `prep.style` / `prep.storyboard` / `seg.write.<段ID>` / `seg.audit.<段ID>.r<轮>` / `seg.boundary.<段ID>` / `final.audit` / `prereview` / `frameqa` 等。
6. **新接口** `GET /api/projects/:id/transcript?task=xxx`(overview.go 或 server.go):task 名正则校验 `[a-z0-9._-]+` 防目录穿越,读文件解析为消息数组返回。
7. **artifacts 分类**:`llm/transcripts/` 归 Keeper,随正本上传 MinIO(注意现有 Classify 会把 `prep-*.jsonl` 设为 Discard,新规则要放行 transcripts 目录)。

## 前端(约 350 行)

1. **新页签**:types.ts:57 TabID 加 `'activity'`;StagePanel.vue tabs computed 加 `{ id:'activity', label:'过程' }`(常驻),tabState 制作中亮蓝点;KeepAlive 分支挂新组件。
2. **删小弹窗**:StagePanel 的 showFeed/开关按钮/浮层样式删除;ActivityFeed.vue 的 `switch(ev.event)` 行翻译逻辑整体搬入新组件后退役该文件。
3. **新组件 `web/src/components/ActivityPane.vue`**:
   - 顶栏:本轮合计(输入 token/输出 token/调用次数,按 produce.start 之后求和);
   - 主体:事件按 task 字段分组——有 task 的聚成卡片(标题=人话工位名+段号,状态灯,用时,完成显示 token 徽标),无 task 的事件当普通行穿插;
   - 卡片展开:「对话」(角色气泡:用户/模型/工具;工具调用与长内容默认折叠可点开)+「事件」(该任务子事件明细);
   - 对话内容懒加载:首次展开调 transcript 接口;运行中任务展开时 5 秒轮询刷新,终态停止。
4. types.ts 扩展 ActivityEvent 可选字段(task/label/prompt_tokens/completion_tokens/calls/transcript)+ 新 TranscriptMsg 类型;api.ts 加 `transcript(id, task)`。
5. 活动接口 tail 从 500 提到 2000(activity 页传 limit 参数),防新账行把旧事件挤出窗口。

## 明确不做
- 对话栏主管聊天不重复录(已实时可见);
- 不改产线架构不加工位,production-architecture-v3-interactive.html 无需同步;
- autoTab 不动(制作中仍自动落视频页,过程页签蓝点提示)。

## 验证
1. `go build ./...` + `go test ./...`(server 目录);给 Recorder 减脂和 task 名校验补两个小单测。
2. 前端 `npm run build`。
3. 真机冒烟:跑一次 prep+制作,核对——对话能展开且运行中实时增长、token 数字合理(与 llm.usage 旧账行对得上)、重启服务器后历史项目的对话仍能看。改 Go 后记得重启服务器进程。