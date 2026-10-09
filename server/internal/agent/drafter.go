// 撰稿工位：主题 → 口播文稿，ReAct 循环（想→做→看→再想）。
// 原来是一次性 JSON 调用，时事题材全凭模型记忆，数字与事实常编。
// 升级为自主循环：涉时效事实先 web_search 核实（预算帽防搜索额度被循环烧穿），
// 细读某条结果 fetch_url，最后交纯文本成稿。工具执行体与主管共用（单点维护）。
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	openai "github.com/sashabaranov/go-openai"

	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/react"
	"webpage-video-agent/server/internal/webfetch"
)

// draftSearchBudget 单次起草的搜索次数预算帽（套餐月约 200 次额度，与主管共享）。
const draftSearchBudget = 2

// draftMaxSteps 起草工位步数上限：搜索+细读+成稿，5 步足够，少留打转空间。
const draftMaxSteps = 5

// DraftManuscript 起草一篇口播文稿。时效性题材自主联网核实后再写，返回文稿正文。
// words 是目标字数（口播节奏由字数决定，分钟数换算交给调用方/UI）。
func DraftManuscript(ctx context.Context, prov react.ChatClient, topic string, words int) (string, error) {
	return draftWithTools(ctx, prov, drafterTools(), topic, words)
}

// draftWithTools 循环本体（工具可注入，测试走假搜索不打网络）。
func draftWithTools(ctx context.Context, prov react.ChatClient, tools []react.Tool, topic string, words int) (string, error) {
	core := react.New(prov, tools, draftMaxSteps, react.Hooks{}) // 起草工位无舞台播报与聊天案卷，钩子留空
	msgs := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: draftSystemPrompt},
		{Role: openai.ChatMessageRoleUser, Content: draftUserPrompt(topic, words)},
	}
	final, err := core.Run(ctx, msgs)
	if err != nil {
		return "", err
	}
	final = strings.TrimSpace(stripContentWrapper(final))
	if final == "" {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("起草未在限定步数内交稿（模型只调工具没给正文），请重试")
	}
	return final, nil
}

// drafterTools 撰稿工位工具集：联网搜索（预算帽）+ 网页细读。执行体与主管工具表共用。
func drafterTools() []react.Tool {
	search := react.Tool{
		Def: toolDef("web_search", "联网搜索实时资料（新闻/版本号/价格/数据等）。写实性主题先搜再写，不确定的不编。额度有限（套餐月约 200 次），一次起草最多搜 2 次，一次到位", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":    map[string]any{"type": "string", "description": "搜索词，≤70 字，关键词组效果最好"},
				"recency":  map[string]any{"type": "string", "description": "时间范围（可选）：oneDay/oneWeek/oneMonth/oneYear，查新闻类用 oneWeek"},
				"location": map[string]any{"type": "string", "description": "地域（可选）：cn 中文区（默认）/ us 非中文区"},
			},
			"required": []string{"query"},
		}),
		Gate: func() string { return "" },
		Run:  runWebSearch,
	}
	fetch := react.Tool{
		Def: toolDef("fetch_url", "读取一个网页的正文（纯文本，免费不占搜索额度）。要细读某条搜索结果时用。只抓静态页面，不执行 JS", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{"type": "string", "description": "要读取的网页地址（http/https）"},
			},
			"required": []string{"url"},
		}),
		Gate: func() string { return "" },
		Run:  runFetchURL,
	}
	return []react.Tool{react.WithCallBudget(search, draftSearchBudget,
		fmt.Sprintf("搜索额度已用完（每次起草上限 %d 次）。基于已有信息成稿：不确定的数字与事实不写死，改用模糊表述。", draftSearchBudget)), fetch}
}

const draftSystemPrompt = "你是「帧述」的口播撰稿人，为短视频写口播文稿。硬性要求：" +
	"1) 第一句就是钩子（提问/反常识/画面感），不写标题和开场白；" +
	"2) 2~4 个递进的信息点或看点，口语化短句，像讲给朋友听，少形容词堆砌；" +
	"3) 结尾一句收束或抛一个问题；" +
	"4) 只输出正文：不带标题、小节序号、markdown、emoji、舞台指示；" +
	"5) 数字和事实宁缺毋滥，不确定的不编。" +
	"工具使用：涉及时效性事实（新闻/版本号/价格/数据/人物近况）先用 web_search 核实再写进稿子，" +
	"搜索额度有限非必要不搜；要细读某条结果用 fetch_url。研究完直接输出文稿全文（纯正文，" +
	"不带 JSON 包装、不带工具汇报），那将作为最终交付。"

func draftUserPrompt(topic string, words int) string {
	// 篇幅由字数直驱（用户按字数要稿）；4.7 字/秒的口播换算只作节奏参考留给分镜阶段
	return fmt.Sprintf("主题：%s\n目标篇幅：正文约 %d 字（±20%%）。", strings.TrimSpace(topic), words)
}

// stripContentWrapper 兜底：模型按老习惯把成稿包进 {"content":...} JSON 时剥出正文。
func stripContentWrapper(s string) string {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "{") {
		return t
	}
	var w struct {
		Content string `json:"content"`
	}
	if json.Unmarshal([]byte(t), &w) == nil && strings.TrimSpace(w.Content) != "" {
		return strings.TrimSpace(w.Content)
	}
	return t
}

// ── 主管与撰稿工位共用的联网工具执行体（与项目无关）──────────────────

// runWebSearch web_search 执行体：解析参数 → MCP 搜索 → 人话结果回喂。
func runWebSearch(args string) string {
	var in struct {
		Query    string `json:"query"`
		Recency  string `json:"recency"`
		Location string `json:"location"`
	}
	_ = json.Unmarshal([]byte(args), &in)
	q := strings.TrimSpace(in.Query)
	if q == "" {
		return "缺少 query"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	rs, err := llm.WebSearch(ctx, q, strings.TrimSpace(in.Recency), strings.TrimSpace(in.Location))
	if err != nil {
		return "搜索失败：" + err.Error()
	}
	if len(rs) == 0 {
		return "没有搜到相关结果，换个更具体的搜索词再试"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "「%s」搜到 %d 条：\n", q, len(rs))
	for i, r := range rs {
		meta := r.Media
		if r.PublishDate != "" {
			if meta != "" {
				meta += " · "
			}
			meta += r.PublishDate
		}
		if meta != "" {
			meta = "（" + meta + "）"
		}
		fmt.Fprintf(&b, "%d. %s%s\n%s\n%s\n", i+1, r.Title, meta, r.Content, r.Link)
	}
	return b.String()
}

// runFetchURL fetch_url 执行体：抓网页正文，超长截断防撑爆上下文。
func runFetchURL(args string) string {
	var in struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal([]byte(args), &in)
	u := strings.TrimSpace(in.URL)
	if u == "" {
		return "缺少 url"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	art, err := webfetch.Fetch(ctx, u)
	if err != nil {
		return "网页读取失败：" + err.Error()
	}
	return formatArticle(art)
}
