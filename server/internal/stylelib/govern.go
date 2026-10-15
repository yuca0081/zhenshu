package stylelib

// govern.go —— 风格库治理与检索（prep-agents-design #8）：
// 衍生登记（register_style 底座，自动带出处+未验证标签）、使用记账（⑤审核回写，
// ②的数据驱动选型闭环）、分面检索（风格工位 search_styles 底座）。
// 治理规则：衍生/自定义条目 published=false + unverified=true；用过它的片出片
// 才转正；已有条目的编辑/删除不开放给 agent。

import (
	"fmt"
	"strings"
	"time"

	"webpage-video-agent/server/internal/contract"
)

// DeriveInput 衍生/自定义登记的输入（register_style 工具载荷）。
// base_id 非空 = 衍生（标签/特色/分面从基风格继承，delta 覆盖分面）；
// base_id 为空 = 自定义（tags.main 必填，须在词表内）。
type DeriveInput struct {
	BaseID  string
	Name    string
	Delta   string // 与基风格的差异一句话（自定义时是风格说明）
	Subject string
	Tags    *contract.StyleTags
	Facets  *StyleFacets
}

// Derive 衍生/自定义登记：新条目 published=false + unverified=true，衍生自动带
// derived_from 出处。单次 run 限登记 1 个的治理在调用方（风格工位）掐。
func (l *Library) Derive(in DeriveInput) (*Style, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, fmt.Errorf("风格名为空")
	}
	s := Style{
		Name:        name,
		Subject:     strings.TrimSpace(in.Subject),
		Published:   false,
		Unverified:  true,
		CreatedAt:   time.Now(),
		DerivedFrom: strings.TrimSpace(in.BaseID),
	}
	if base := s.DerivedFrom; base != "" {
		st, ok := l.Get(base)
		if !ok {
			return nil, fmt.Errorf("基风格 %s 不存在", base)
		}
		s.Tags = st.Tags
		s.Facets = st.Facets
		s.Signatures = append([]contract.Signature(nil), st.Signatures...)
	}
	if in.Tags != nil {
		if strings.TrimSpace(in.Tags.Main) != "" {
			s.Tags.Main = strings.TrimSpace(in.Tags.Main)
		}
		if len(in.Tags.Aux) > 0 {
			s.Tags.Aux = in.Tags.Aux
		}
	}
	if in.Facets != nil {
		s.Facets = *in.Facets
	}
	if s.Tags.Main == "" {
		return nil, fmt.Errorf("tags.main 为空（自定义风格须给主标签，衍生自动继承基风格）")
	}
	if strings.TrimSpace(in.Delta) == "" {
		return nil, fmt.Errorf("delta 为空（与基风格的差异必须成文）")
	}
	styles, err := l.cat.ListStyles()
	if err != nil {
		return nil, fmt.Errorf("风格库读取失败（不写入）: %w", err)
	}
	s.ID = newID(styles)
	s.Signatures = renumberSigs(s.Signatures)
	if err := l.validateWrite(&s); err != nil {
		return nil, err
	}
	if err := l.cat.PutStyle(&s); err != nil {
		return nil, err
	}
	return &s, nil
}

// BumpUsage 使用记账：出片即 count++（passed=true 时 passes 同步）。score>0 记为
// 最近审核分。promote=true 且条目是「未验证」的衍生/自定义 → 转正（published=true，
// unverified 保持史实字段不清——出处链不受影响）。条目不存在静默返回（记账不拦制作）。
func (l *Library) BumpUsage(id string, score float64, passed, promote bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.Get(id)
	if !ok {
		return
	}
	st.Usage.Count++
	if passed {
		st.Usage.Passes++
	}
	if score > 0 {
		st.Usage.LastScore = score
	}
	st.Usage.LastUsedAt = time.Now()
	if promote && st.Unverified {
		st.Published = true
	}
	_ = l.cat.PutStyle(st)
}

// Search 分面检索（风格工位 search_styles 底座）：query 词组与分面全做子串匹配
// （不区分大小写），空条件全量分页。返回当页与总数。
func (l *Library) Search(query string, facets StyleFacets, page, size int) ([]Style, int, error) {
	styles, err := l.List()
	if err != nil {
		return nil, 0, err
	}
	terms := strings.Fields(strings.ToLower(strings.TrimSpace(query)))
	match := func(hay, needle string) bool {
		return needle == "" || strings.Contains(strings.ToLower(hay), needle)
	}
	var out []Style
	for _, st := range styles {
		ok := true
		for _, q := range terms {
			keys := []string{st.ID, st.Name, st.Subject, st.Tags.Main, st.Facets.Mood, st.Facets.Tone, st.Facets.Density}
			keys = append(keys, st.Tags.Aux...)
			for _, sg := range st.Signatures {
				keys = append(keys, sg.Name, sg.Mechanism)
			}
			if !match(strings.Join(keys, " "), q) {
				ok = false
				break
			}
		}
		if !ok || !match(st.Facets.Mood, strings.TrimSpace(facets.Mood)) ||
			!match(st.Facets.Tone, strings.TrimSpace(facets.Tone)) ||
			!match(st.Facets.Density, strings.TrimSpace(facets.Density)) {
			continue
		}
		out = append(out, st)
	}
	total := len(out)
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 50 {
		size = 20
	}
	lo := (page - 1) * size
	if lo >= total {
		return nil, total, nil
	}
	if hi := lo + size; hi < total {
		return out[lo:hi], total, nil
	}
	return out[lo:], total, nil
}
