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
		// 标签证据一并继承（2026-10-24）：衍生=基风格调个方向，标签所指的气质
		// 没变，例证帧同源适用——此前不继承，库里衍生条目 tag_evidence 全空，
		// 简报的「标签证据优先」对它们整层落空。覆盖了标签的衍生，旧证据描述的
		// 是被换掉的标签，留给库页人工修剪——机械层不做猜谜。
		s.TagEvidence = append([]contract.TagEvidence(nil), st.TagEvidence...)
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
	// 标签自动种词：词表冷启动为空表，「自定义登记的标签必须在词表」会把自定义
	// 路径整个堵死（模型瞎猜词表名永被拒）。词表是策展登记册不是闸门——缺的
	// 标签种进去（「标签必在词表」不变量靠生长维持，词表页可修剪），登记畅通。
	if v, verr := l.LoadVocab(); verr == nil {
		seed := func(tag *string) {
			t := strings.TrimSpace(*tag)
			if t == "" {
				return
			}
			if hit, ok := v.Find(t); ok {
				*tag = hit.ID // 归一化命中：换正式词条 id
				return
			}
			if id, err := l.vocabCreateLocked(VocabEntry{
				Name:  t,
				Brief: "风格工位登记「" + name + "」时收入",
			}); err == nil && id != "" {
				*tag = id
			}
			// 种词失败按原值走 validateWrite——要么碰巧在词表要么被拒，不静默放行
		}
		seed(&s.Tags.Main)
		for i := range s.Tags.Aux {
			seed(&s.Tags.Aux[i])
		}
	}
	s.Signatures = renumberSigs(s.Signatures)
	if err := l.validateWrite(&s); err != nil {
		return nil, err
	}
	if err := l.cat.PutStyle(&s); err != nil {
		return nil, err
	}
	return &s, nil
}

// RefillFacets 存量分面回填（-backfill-facets 子命令用）：只写 Facets，其余字段
// 不动。已有任一分面轴的条目拒绝写——人工填过的值不被机械回填覆盖。
func (l *Library) RefillFacets(id string, facets StyleFacets) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.Get(id)
	if !ok {
		return fmt.Errorf("%w: %s", ErrNoEntry, id)
	}
	if st.Facets != (StyleFacets{}) {
		return fmt.Errorf("条目 %s 已有分面，拒绝覆盖（人工值优先）", id)
	}
	st.Facets = facets
	return l.cat.PutStyle(st)
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
	// 标签检索走词条展开（2026-10-24）：Tags 存的是词条 id（如 claymation），中文
	// 查询「粘土」对 id 做子串永远 miss——词条名+别名一并进检索键，词表养的
	// 同义词这才真正参战。词表读失败降级纯子串，检索不被词表故障挡死。
	vocab, _ := l.LoadVocab()
	if vocab == nil {
		vocab = &Vocab{Entries: map[string]VocabEntry{}}
	}
	tagWords := func(ids ...string) string {
		parts := make([]string, 0, len(ids)*3)
		for _, id := range ids {
			if e, hit := vocab.Entries[id]; hit {
				parts = append(parts, e.Name)
				parts = append(parts, e.Aliases...)
			} else {
				parts = append(parts, id) // 词条被删/自定义原值：按原值参战
			}
		}
		return strings.Join(parts, " ")
	}
	terms := strings.Fields(strings.ToLower(strings.TrimSpace(query)))
	match := func(hay, needle string) bool {
		return needle == "" || strings.Contains(strings.ToLower(hay), needle)
	}
	var out []Style
	for _, st := range styles {
		ok := true
		for _, q := range terms {
			keys := []string{st.ID, st.Name, st.Subject, tagWords(st.Tags.Main), tagWords(st.Tags.Aux...), st.Facets.Mood, st.Facets.Tone, st.Facets.Density}
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
