// Package review：制作阶段审核工位（docs/production-stage-design.md 工作项⑯/⑮）——
//
//	一个判卷型 ReAct agent、三种调用档案（段审 / 边界审 / 全片终审）：
//	多模态、温度 ≈0、回合预算小、submit_verdict 唯一出口（submit-or-die）。
//	机制不进提示词：预算、缓存、schema 校验、打回判定、刹车判定都在框架侧；
//	LLM 只做判卷（取证 → 意见单）。
//
// 循环骨架照准备阶段三工位（server/internal/prep）的惯例落地：程序装配开场材料，
// 工具观察人话回流，校验失败错误清单回流循环内自修。与 prep 的差异：审核的
// submit 载荷是结构化意见单，程序按权威 schema + 跨字段规则校验。
package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"

	openai "github.com/sashabaranov/go-openai"

	"webpage-video-agent/server/internal/llm"
)

// Observation 工具执行结果（与 prep.Observation 同形）：Text 回喂模型；
// Images 非空以「系统附图」user 消息跟随观察（工具消息塞图端点不可靠，
// user 消息是实测可靠通道）；Done 仅 submit 工具校验通过后置位。
type Observation struct {
	Text   string
	Images []string
	Done   bool
}

// Tool 单个审核工具。
type Tool struct {
	Def openai.Tool
	Run func(ctx context.Context, args string) Observation
}

// ErrBudgetExhausted 回合预算耗尽仍未提交（调用方记账；审核不冷重试——
// 判卷预算烧尽说明取证收敛失败，按审查器故障降级）。
var ErrBudgetExhausted = errors.New("回合预算耗尽仍未提交")

// ChatClient 循环对模型的全部依赖：带温度的对话轮（判卷低温）。*llm.Provider 隐式满足。
type ChatClient interface {
	ChatTemp(ctx context.Context, msgs []openai.ChatCompletionMessage, tools []openai.Tool, temp float64) (openai.ChatCompletionMessage, openai.Usage, error)
}

// effortClient 可选的档位扩展（*llm.Provider 实现）。分开声明而非并进 ChatClient：
// 测试 mock 只实现 ChatTemp 就能跑，档位路径走接口探测降级。
type effortClient interface {
	ChatTempEffort(ctx context.Context, msgs []openai.ChatCompletionMessage, tools []openai.Tool, temp float64, effort llm.Effort) (openai.ChatCompletionMessage, openai.Usage, error)
}

// Sink 对话留痕接收端（*llm.Transcript 隐式满足）。可空：nil 照跑不留痕。
type Sink interface {
	Message(msg openai.ChatCompletionMessage)
	Note(format string, a ...any)
}

// Loop 单次审核会话。与 prep.Loop 同款 submit-or-die：无 ToolCalls 的文本回复
// 不落盘、nudge 后继续（回合照计）。
type Loop struct {
	Prov      ChatClient
	System    string
	Kickoff   string
	KickoffIm []string // 开场注入的图片（锚点帧/样张等，程序装配——跟 kickoff 同一条 user 消息）
	// KickoffRefs 与 KickoffIm 一一对应：每张图的项目内路径+标签。落留痕时随
	// kickoff 记进 images 字段，「过程」页据此渲染缩略图/点击放大（10-19 起：
	// 旧版只接 preReview/frameQA，而段级流水线带图的是这三类审查，缩略图永不亮）。
	KickoffRefs []llm.ImgRef
	Tools       []Tool
	Submit      string
	MaxRounds   int
	Temp        float64 // 判卷低温（≈0）
	// Effort 思考档位（可空 = 走 ChatTemp 的 high 默认）。判卷档案由 runAudit
	// 按 LLM_AUDIT_EFFORT 注入——降档省 reasoning token，判卷质量不满意改环境变量。
	Effort  llm.Effort
	LogPath string // 取证留痕 jsonl（非空时自动接 OnTool）
	// OnStep 每步记账（kind: action|observation|text）；OnTool 取证留痕（工具+参数+结果指纹由调用方算）。
	OnStep func(round int, kind, detail string)
	OnTool func(name, args, result string)
	// Rec 对话留痕（可空）；Usage/Calls 本轮累计（Run 返回后宿主读）。
	Rec Sink
	// TaskID/TaskLabel 非空时由宿主侧统一记账（runAudit 负责 agent.start/done）。
	TaskID    string
	TaskLabel string
	Usage     openai.Usage
	Calls     int
}

// auditImgKeepRounds 取证图保留窗（AUDIT_IMG_KEEP_ROUNDS，默认 2）：第 N 轮取的
// 证图随历史保留到第 N+K 轮，之后换一行文字占位。平台无缓存折扣时（cached_tokens=0，
// 2026-10-19 实测 80 调全零）每张老图每轮全额重发，一张 480 宽帧 ≈ 500 token；
// 窗口调大或设解析上限 50 等效关闭裁剪。开场图（锚点帧/样张）不在此列——那是全轮共享的稳定前缀。
func auditImgKeepRounds() int {
	if v := os.Getenv("AUDIT_IMG_KEEP_ROUNDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 50 {
			return n
		}
	}
	return 2
}

// trimmedEvidenceMsg 被裁取证图的占位文本。不删消息条目只换载荷：模型仍知道
// 「这里曾有图」，真要回看就重新取证——抽帧有磁盘缓存（键=段+时刻+文件指纹），
// 重取不重跑 ffmpeg，且新图作为最新附图恰好落回保留窗内。裁剪只在追加新消息时
// 发生且裁了不复原（单调）：后续请求仍互为前缀延长线，平台若开缓存照常命中。
func trimmedEvidenceMsg() openai.ChatCompletionMessage {
	return openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser,
		Content: "［系统附图·已撤］此前的取证图已从上下文撤下（省额度）。需要回看就再调一次 get_frame/get_clip 取同一时刻。"}
}

// Run 跑到 submit 校验通过，或预算耗尽 / 模型调用失败。
func (l *Loop) Run(ctx context.Context) error {
	kickParts := []openai.ChatMessagePart{{Type: openai.ChatMessagePartTypeText, Text: l.Kickoff}}
	skipped := 0
	for _, u := range l.KickoffIm {
		if !llm.ValidImageData(u) { // 坏图外发必吃 endpoint 400（image_url格式错误）整审报废——拦下并记账
			skipped++
			continue
		}
		kickParts = append(kickParts, openai.ChatMessagePart{
			Type:     openai.ChatMessagePartTypeImageURL,
			ImageURL: &openai.ChatMessageImageURL{URL: u},
		})
	}
	if skipped > 0 && l.Rec != nil {
		l.Rec.Note("警告：拦截 %d 张非法载荷注入图（空 URL/截断 data URL/本地路径——注入装配丢拷贝的症状，查 visRefs 类函数）", skipped)
	}
	kick := openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser}
	if len(kickParts) > 1 {
		kick.MultiContent = kickParts // kickParts[0] 即 kickoff 正文；Content 与 MultiContent 互斥（openai 库序列化硬约束）
	} else {
		kick.Content = l.Kickoff
	}
	msgs := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: l.System},
		kick,
	}
	if l.Rec != nil {
		l.Rec.Message(msgs[0])
		l.recordKick(kick)
	}
	schemas := make([]openai.Tool, 0, len(l.Tools))
	byName := map[string]int{}
	for _, t := range l.Tools {
		if _, dup := byName[t.Def.Function.Name]; dup {
			l.Tools[byName[t.Def.Function.Name]] = t
		} else {
			byName[t.Def.Function.Name] = len(schemas)
			schemas = append(schemas, t.Def)
		}
	}
	// 中途取证图的登记簿（下标+取证轮次）：每轮开工前按保留窗裁剪。
	// 开场图在 msgs[1]，全轮共享一个字节不改——那是稳定前缀，裁它等于自拆缓存。
	var imgs []imgMsgAt
	for round := 1; round <= l.MaxRounds; round++ {
		trimStaleImgs(msgs, imgs, round, l.Rec)
		msg, u, err := l.chatRound(ctx, msgs, schemas)
		llm.AccumulateUsage(&l.Usage, u)
		l.Calls++
		if err != nil {
			l.recNote("模型调用失败（第 %d 回合）: %v", round, err)
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if len(msg.ToolCalls) == 0 {
			l.step(round, "text", msg.Content)
			nudge := openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: fmt.Sprintf("文本回复不会落盘。任务完成的唯一方式是调用 %s 工具；继续。", l.Submit)}
			l.record(msg, nudge)
			msgs = append(msgs, msg, nudge)
			continue
		}
		l.record(msg)
		msgs = append(msgs, msg)
		for _, call := range msg.ToolCalls {
			l.step(round, "action", call.Function.Name+" "+compactJSON(call.Function.Arguments))
			obs := l.exec(ctx, call.Function.Name, call.Function.Arguments)
			l.step(round, "observation", obs.Text)
			if l.OnTool != nil {
				l.OnTool(call.Function.Name, call.Function.Arguments, obs.Text)
			}
			tool := openai.ChatCompletionMessage{
				Role: openai.ChatMessageRoleTool, Content: obs.Text,
				ToolCallID: call.ID, Name: call.Function.Name,
			}
			msgs = append(msgs, tool)
			l.record(tool)
			if len(obs.Images) > 0 {
				im := imageFollowUp(obs.Images)
				msgs = append(msgs, im)
				imgs = append(imgs, imgMsgAt{idx: len(msgs) - 1, round: round})
				l.record(im)
			}
			if call.Function.Name == l.Submit && obs.Done {
				return nil
			}
		}
	}
	return ErrBudgetExhausted
}

// imgMsgAt 一条带图消息在 msgs 里的位置与取证轮次（裁剪记账用）。
type imgMsgAt struct {
	idx   int
	round int
}

// trimStaleImgs 按保留窗把超出窗口的取证图消息原位换成文字占位。单调：换过的
// （MultiContent 已 nil）不再动，后续请求互为前缀延长线。审计留痕不受影响——
// 图在追加当时已进 transcript，这里只瘦发给模型的上下文。
func trimStaleImgs(msgs []openai.ChatCompletionMessage, imgs []imgMsgAt, next int, rec Sink) {
	cut := next - 1 - auditImgKeepRounds()
	if cut < 1 {
		return
	}
	n := 0
	for _, im := range imgs {
		if im.round <= cut && msgs[im.idx].MultiContent != nil {
			msgs[im.idx] = trimmedEvidenceMsg()
			n++
		}
	}
	if n > 0 && rec != nil {
		rec.Note("第 %d 轮起撤下 %d 张早期取证图（保留最近 %d 轮，AUDIT_IMG_KEEP_ROUNDS）", next, n, auditImgKeepRounds())
	}
}

// chatRound 一轮对话。Effort 非空且底层支持档位扩展时走 ChatTempEffort（降档省
// reasoning token），否则退回 ChatTemp 的 high 默认——mock 只实现基础接口也能跑。
func (l *Loop) chatRound(ctx context.Context, msgs []openai.ChatCompletionMessage, tools []openai.Tool) (openai.ChatCompletionMessage, openai.Usage, error) {
	if l.Effort != "" {
		if ec, ok := l.Prov.(effortClient); ok {
			return ec.ChatTempEffort(ctx, msgs, tools, l.Temp, l.Effort)
		}
	}
	return l.Prov.ChatTemp(ctx, msgs, tools, l.Temp)
}

func (l *Loop) record(ms ...openai.ChatCompletionMessage) {
	if l.Rec == nil {
		return
	}
	for _, m := range ms {
		l.Rec.Message(m)
	}
}

// recordKick 落开场注入消息：带图片引用清单时走 MessageImgs（路径给「过程」页
// 缩略图），Sink 不支持或无引用退回普通 Message。
func (l *Loop) recordKick(kick openai.ChatCompletionMessage) {
	// 只记真正外发的图：Run() 的空 URL 守卫跳过的条目不进留痕——留痕多于外发
	// 会渲染一张模型从没见过的缩略图，与「跳过 N 张空 URL」的警告自相矛盾
	refs := make([]llm.ImgRef, 0, len(l.KickoffRefs))
	for _, r := range l.KickoffRefs {
		if r.URL != "" {
			refs = append(refs, r)
		}
	}
	if len(refs) == 0 {
		l.Rec.Message(kick)
		return
	}
	if ti, ok := l.Rec.(interface {
		MessageImgs(msg openai.ChatCompletionMessage, imgs []llm.ImgRef)
	}); ok {
		ti.MessageImgs(kick, refs)
		return
	}
	l.Rec.Message(kick)
}

func (l *Loop) recNote(format string, a ...any) {
	if l.Rec != nil {
		l.Rec.Note(format, a...)
	}
}

func (l *Loop) exec(ctx context.Context, name, args string) Observation {
	for i := range l.Tools {
		if l.Tools[i].Def.Function.Name == name {
			return l.Tools[i].Run(ctx, args)
		}
	}
	names := make([]string, 0, len(l.Tools))
	for _, t := range l.Tools {
		names = append(names, t.Def.Function.Name)
	}
	return Observation{Text: "未知工具 " + name + "（可用：" + strings.Join(names, "、") + "）"}
}

func (l *Loop) step(round int, kind, detail string) {
	if l.OnStep != nil {
		l.OnStep(round, kind, detail)
	}
}

// imageFollowUp 多模态附图（与 prep 同通道）。坏载荷在这里拦下（P0-3）：
// 取证管线偶产坏帧时，丢一张图好过整审报废。
func imageFollowUp(images []string) openai.ChatCompletionMessage {
	parts := []openai.ChatMessagePart{{
		Type: openai.ChatMessagePartTypeText, Text: "［系统附图］上面工具取回的画面证据，供你判卷。",
	}}
	for _, u := range images {
		if !llm.ValidImageData(u) {
			continue
		}
		parts = append(parts, openai.ChatMessagePart{
			Type:     openai.ChatMessagePartTypeImageURL,
			ImageURL: &openai.ChatMessageImageURL{URL: u},
		})
	}
	return openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, MultiContent: parts}
}

func compactJSON(args string) string {
	var m map[string]any
	if json.Unmarshal([]byte(args), &m) == nil {
		b, _ := json.Marshal(m)
		return string(b)
	}
	return args
}

// decodeArgs 工具参数解析（人话错误回流；与 prep.decodeArgs 同款口径）。
func decodeArgs(args string, out any) error {
	err := json.Unmarshal([]byte(args), out)
	if err == nil {
		return nil
	}
	var te *json.UnmarshalTypeError
	if errors.As(err, &te) {
		where := te.Field
		if where == "" {
			where = "顶层参数"
		}
		return fmt.Errorf("参数类型不对：字段 %s 需要%s，实际收到的是%s——按类型改好后重交", where, humanJSONType(te.Type), humanJSONVal(te.Value))
	}
	return fmt.Errorf("参数不是合法 JSON（多半是引号/逗号/括号问题）：%v", err)
}

func humanJSONVal(v string) string {
	switch v {
	case "number":
		return "数字"
	case "string":
		return "字符串"
	case "boolean":
		return "布尔 true/false"
	case "array":
		return "数组"
	case "object":
		return "对象"
	case "null":
		return "null"
	}
	return v
}

func humanJSONType(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		return "字符串"
	case reflect.Bool:
		return "布尔 true/false"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "数字"
	case reflect.Slice, reflect.Array:
		return "数组"
	case reflect.Struct, reflect.Map:
		return "对象"
	}
	return t.String()
}

// toolSchema 工具定义构造（结构化字段格式进 parameters，不进提示词）。
func toolSchema(name, desc string, params map[string]any) openai.Tool {
	if params == nil {
		params = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{
		Name: name, Description: desc, Parameters: params,
	}}
}
