package agent

import (
	"context"
	"strings"
	"testing"

	"webpage-video-agent/server/internal/webfetch"
)

// fetch_url 工具的接线测试（不打网络：抓取逻辑在 webfetch 包测）。
func TestFetchURLToolRegistered(t *testing.T) {
	tl := findTool("fetch_url")
	if tl == nil {
		t.Fatal("fetch_url 未注册")
	}
	if got := tl.gate(State{}); got != "" {
		t.Errorf("fetch_url 不应设状态门，被拒原因 %q", got)
	}
	found := false
	for _, td := range toolSchemas() {
		if td.Function.Name == "fetch_url" {
			found = true
			schema, ok := td.Function.Parameters.(map[string]any)
			if !ok {
				t.Fatalf("参数 schema 类型异常: %T", td.Function.Parameters)
			}
			props, _ := schema["properties"].(map[string]any)
			if _, ok := props["url"]; !ok {
				t.Error("fetch_url 参数 schema 应含 url 字段")
			}
		}
	}
	if !found {
		t.Fatal("toolSchemas() 里没有 fetch_url")
	}
}

func TestFetchURLToolRun(t *testing.T) {
	tl := findTool("fetch_url")
	if got := tl.run(context.Background(), nil, nil, `{"url":""}`); got != "缺少 url" {
		t.Errorf("空 url 应提示缺少，得到 %q", got)
	}
	// 抓取失败路径也不打网络：非法地址直接被拒
	if got := tl.run(context.Background(), nil, nil, `{"url":"http://"}`); !strings.Contains(got, "网页读取失败") {
		t.Errorf("非法地址应报读取失败，得到 %q", got)
	}
}

func TestFormatArticle(t *testing.T) {
	short := formatArticle(&webfetch.Article{Title: "短文", Content: "一二三四五", Words: 5})
	if !strings.Contains(short, "《短文》") || !strings.Contains(short, "一二三四五") {
		t.Errorf("短正文应带标题原样回喂，得到 %q", short)
	}
	long := formatArticle(&webfetch.Article{Title: "长文", Content: strings.Repeat("长", 16000), Words: 16000})
	if !strings.Contains(long, "正文过长") || !strings.Contains(long, "15000") {
		t.Errorf("超长正文应截断并注明，得到前 100 字: %.100s", long)
	}
}
