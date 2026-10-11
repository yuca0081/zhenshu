package stylelib

// 风格简报（v3 §4.3）：选定条目在项目内的本地拷贝 style/style_brief.json。
// 写入者是 applyStyle API（前端开关数组）与 agent 选定分支——同一文件同一格式；
// 重选条目 = 整体重写。简报与库解耦：条目后来被删/改不影响已选项目。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"webpage-video-agent/server/internal/contract"
)

// maxBriefEvidence 简报证据帧上限（§4.3：≤6 张，控制设计阶段多模态请求体积）。
const maxBriefEvidence = 6

// BuildBrief 组装简报：disabled 是用户关掉的特色 id 集合（默认全开）。
// Evidence 从 style_media 实存文件取（标签证据优先，再补特色证据帧，去重截 6）。
func (l *Library) BuildBrief(id string, disabled map[string]bool) (*contract.StyleBrief, error) {
	st, ok := l.Get(id)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNoEntry, id)
	}
	b := &contract.StyleBrief{
		EntryID:   st.ID,
		EntryName: st.Name,
		Tags:      st.Tags,
		TagBriefs: l.VocabBriefs(append([]string{st.Tags.Main}, st.Tags.Aux...)...),
	}
	for _, sg := range st.Signatures {
		if disabled[sg.ID] {
			continue
		}
		b.Signatures = append(b.Signatures, sg)
	}
	seen := map[string]bool{}
	add := func(names []string) {
		if len(b.Evidence) >= maxBriefEvidence { // 上限检查必须在入口：写在循环尾只挡得住本组
			return
		}
		for _, n := range names {
			if n == "" || seen[n] {
				continue
			}
			if _, err := os.Stat(filepath.Join(l.MediaDir(st.ID), n)); err != nil {
				continue // 快照缺失的帧不进简报
			}
			seen[n] = true
			b.Evidence = append(b.Evidence, filepath.Join(l.MediaDir(st.ID), n))
			if len(b.Evidence) >= maxBriefEvidence {
				return
			}
		}
	}
	for _, ev := range st.TagEvidence { // 标签证据优先（气质基调的对照物）
		add(ev.Frames)
	}
	for _, sg := range b.Signatures {
		add(sg.Frames)
	}
	return b, nil
}

// WriteBriefFile 简报落盘（原子写；路径通常是 <project>/style/style_brief.json）。
func WriteBriefFile(path string, b *contract.StyleBrief) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	j, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, j, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
