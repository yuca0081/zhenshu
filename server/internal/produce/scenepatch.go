package produce

// scenepatch.go —— 场景 patch 协议（2026-10-20 降本 D2/§3.3）：续写修复轮模型只回
// 元素级改动单，程序确定性套用——不再整文件重抄（C3：改一处重抄 7-10KB，每轮
// 1.2-1.7 万 prompt + 3-5k completion）。
//
// 三类改动（全部可机械定位、可校验）：
//   text  —— 元素文字替换（textContent 整换）
//   style —— 元素 style 属性合并（不动类名/结构）
//   html  —— 元素外层 HTML 片段整体替换（结构级修改的最小口子；必须保持同一 id，
//            GSAP 补间按 id 找元素）
// target 定位：#dom-id。任何一条打不中/空改动 → 整单失败（返回错误），调用方
// 追加一条「改出整份文件」续写兜底——宁回退整份重写，不出「改了一半」的产物。
// 时间轴/GSAP 补间活在 <script> 里，不进 patch 口径：模型改时序必须走整份重写。

import (
	"bytes"
	"fmt"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// scenePatch 一条元素级改动。Text/Style/HTML 至少一项。
type scenePatch struct {
	Target string            `json:"target"`          // "#dom-id"
	Text   *string           `json:"text,omitempty"`  // 文字替换
	Style  map[string]string `json:"style,omitempty"` // style 属性合并
	HTML   *string           `json:"html,omitempty"`  // 外层 HTML 整体替换
}

// applyScenePatches 把改动单套用到场景文件（<template> 片段），返回新文件。
// 解析失败/打不中/非法改动一律报错，不产出半套用的结果。
func applyScenePatches(fileHTML string, patches []scenePatch) (string, error) {
	if len(patches) == 0 {
		return "", fmt.Errorf("空改动单")
	}
	prefix, inner, suffix, ok := splitTemplate(fileHTML)
	if !ok {
		return "", fmt.Errorf("场景文件不是 <template> 片段，无法套用改动单")
	}
	top, err := html.ParseFragment(strings.NewReader(inner),
		&html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div})
	if err != nil {
		return "", fmt.Errorf("场景解析失败: %w", err)
	}
	byID := map[string]*html.Node{}
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if id := attrOf(n, "id"); id != "" {
				if _, dup := byID[id]; !dup {
					byID[id] = n
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	for _, n := range top {
		walk(n)
	}
	for i, p := range patches {
		n := byID[strings.TrimPrefix(p.Target, "#")]
		if n == nil {
			return "", fmt.Errorf("改动单第 %d 条打不中 target %q（DOM 里无此 id）", i+1, p.Target)
		}
		if err := applyOnePatch(n, p); err != nil {
			return "", fmt.Errorf("改动单第 %d 条（%s）: %w", i+1, p.Target, err)
		}
	}
	var b strings.Builder
	b.WriteString(prefix)
	for _, n := range top {
		var buf bytes.Buffer
		if err := html.Render(&buf, n); err != nil {
			return "", fmt.Errorf("场景回写失败: %w", err)
		}
		b.Write(buf.Bytes())
	}
	b.WriteString(suffix)
	return b.String(), nil
}

// applyOnePatch 单条改动套用。
func applyOnePatch(n *html.Node, p scenePatch) error {
	acts := 0
	if p.Text != nil {
		acts++
	}
	if len(p.Style) > 0 {
		acts++
	}
	if p.HTML != nil {
		acts++
	}
	if acts == 0 {
		return fmt.Errorf("空改动（text/style/html 至少一项）")
	}
	if acts > 1 {
		return fmt.Errorf("一条改动只准做一件事（text/style/html 互斥）")
	}
	switch {
	case p.Text != nil:
		for n.FirstChild != nil {
			n.RemoveChild(n.FirstChild)
		}
		n.AppendChild(&html.Node{Type: html.TextNode, Data: *p.Text})
	case len(p.Style) > 0:
		cur := map[string]string{}
		if s := attrOf(n, "style"); s != "" {
			for _, decl := range strings.Split(s, ";") {
				kv := strings.SplitN(decl, ":", 2)
				if len(kv) == 2 && strings.TrimSpace(kv[0]) != "" {
					cur[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
				}
			}
		}
		for k, v := range p.Style {
			cur[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
		parts := make([]string, 0, len(cur))
		for k, v := range cur {
			parts = append(parts, k+": "+v)
		}
		setAttr(n, "style", strings.Join(parts, "; "))
	case p.HTML != nil:
		frag, err := html.ParseFragment(strings.NewReader(*p.HTML),
			&html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div})
		if err != nil || len(frag) == 0 {
			return fmt.Errorf("替换片段解析失败或为空")
		}
		// 替换片段必须保持原 id（文件头契约）：GSAP 补间按 id 找元素——模型交回
		// 换了门牌的片段时旧节点被删、id 蒸发，动画静默失效且 sceneValidate/lint
		// 都查不出（2026-10-21 巡检中危#4）。id 丢了整单作废，调用方回退整份重写。
		wantID := attrOf(n, "id")
		if wantID == "" || !fragHasID(frag, wantID) {
			return fmt.Errorf("替换片段丢了原 id %q（必须保持同一 id，GSAP 按 id 找元素）", wantID)
		}
		parent := n.Parent
		if parent == nil {
			return fmt.Errorf("元素无父节点")
		}
		for _, f := range frag {
			parent.InsertBefore(f, n)
		}
		parent.RemoveChild(n)
	}
	return nil
}

// fragHasID 替换片段子树里是否还带着这个 id。
func fragHasID(frag []*html.Node, id string) bool {
	var found bool
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if found {
			return
		}
		if n.Type == html.ElementNode && attrOf(n, "id") == id {
			found = true
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	for _, f := range frag {
		walk(f)
	}
	return found
}

// splitTemplate 拆出 <template …> 前缀与 </template> 后缀，返回（前缀, 内部, 后缀）。
// 大小写不敏感认开头，后缀取最后一个闭合（内部不该再出现 template，出现了也不该由我们猜）。
func splitTemplate(s string) (prefix, inner, suffix string, ok bool) {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(strings.ToLower(t), "<template") {
		return "", "", "", false
	}
	gt := strings.Index(t, ">")
	if gt < 0 {
		return "", "", "", false
	}
	end := strings.LastIndex(strings.ToLower(t), "</template>")
	if end < gt {
		return "", "", "", false
	}
	return t[:gt+1], t[gt+1 : end], t[end:], true
}

func attrOf(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

func setAttr(n *html.Node, key, val string) {
	for i := range n.Attr {
		if strings.EqualFold(n.Attr[i].Key, key) {
			n.Attr[i].Val = val
			return
		}
	}
	n.Attr = append(n.Attr, html.Attribute{Key: key, Val: val})
}
