package review

import "testing"

func segVerdict() Verdict {
	return Verdict{
		Dims: map[string]string{"content": "pass", "fact": "fail", "style": "pass",
			"first_last": "pass", "device": "NA", "subtitle": "pass"},
		Findings: []Finding{{
			Category: "fact", Anchor: "N3", Severity: "high",
			Evidence: "帧 t=12.4s：画面标题写 1985，N3 旁白为 1984", Fix: "redraw",
		}},
	}
}

func TestValidateSegmentOK(t *testing.T) {
	v := segVerdict()
	if err := v.Validate(profSegment, nil); err != nil {
		t.Fatalf("合法意见单被拒: %v", err)
	}
}

func TestValidateSegmentRejections(t *testing.T) {
	cases := map[string]func(*Verdict){
		"缺 dims 键":  func(v *Verdict) { delete(v.Dims, "subtitle") },
		"dims 越界键":  func(v *Verdict) { v.Dims["extra"] = "pass" },
		"anchor 越界": func(v *Verdict) { v.Findings[0].Anchor = "seg01:12" },
		"high+pass": func(v *Verdict) { v.Findings[0].Fix = "pass" },
		"low+redraw": func(v *Verdict) {
			v.Findings[0].Severity = "low"
			v.Findings[0].Fix = "redraw"
		},
		"high 无帧时刻":   func(v *Verdict) { v.Findings[0].Evidence = "标题写 1985，旁白为 1984" },
		"fail 无意见":    func(v *Verdict) { v.Findings = nil },
		"非法 category": func(v *Verdict) { v.Findings[0].Category = "pacing" },
	}
	for name, mutate := range cases {
		v := segVerdict()
		mutate(&v)
		if err := v.Validate(profSegment, nil); err == nil {
			t.Errorf("%s：应被拦截，实际放行", name)
		}
	}
}

func TestValidateRepairRoundReassessment(t *testing.T) {
	prior := segVerdict()
	prior.AssignIDs()
	// 修复轮漏复核 → 拒
	v := segVerdict()
	if err := v.Validate(profSegment, &prior); err == nil {
		t.Fatal("修复轮缺 reassessment 应被拒")
	}
	// 补齐 reassessment → 过
	v.Reassessment = []Reassess{{ID: "f1", Result: "fixed"}}
	if err := v.Validate(profSegment, &prior); err != nil {
		t.Fatalf("补齐 reassessment 后应过: %v", err)
	}
	// still_broken 缺 evidence → 拒
	v.Reassessment = []Reassess{{ID: "f1", Result: "still_broken"}}
	if err := v.Validate(profSegment, &prior); err == nil {
		t.Fatal("still_broken 缺 evidence 应被拒")
	}
}

func TestValidateBoundaryAndFinal(t *testing.T) {
	b := Verdict{
		Dims: map[string]string{"outro_done": "pass", "intro_started": "pass",
			"continuity": "fail", "glitch": "pass"},
		Findings: []Finding{{
			Category: "continuity", Anchor: "b02|t=-0.2", Severity: "high",
			Evidence: "末帧 t=6.80 推近终点在中心，首帧起点在左 1/3", Fix: "redraw",
		}},
	}
	if err := b.Validate(profBoundary, nil); err != nil {
		t.Fatalf("边界审合法单被拒: %v", err)
	}
	// stitch_fix 是边界审的合法 high fix
	b.Findings[0].Fix = "stitch_fix"
	if err := b.Validate(profBoundary, nil); err != nil {
		t.Fatalf("stitch_fix high 应合法: %v", err)
	}
	// 段审的 anchor 形态在边界审不合法
	b.Findings[0].Anchor = "N3"
	if err := b.Validate(profBoundary, nil); err == nil {
		t.Fatal("边界审 anchor=N3 应被拒")
	}

	f := Verdict{Findings: []Finding{{
		Category: "drift", Anchor: "t=83.5", Severity: "high",
		Evidence: "t=83.5s 起底色跳到深霓虹再没回来",
	}}}
	if err := f.Validate(profFinal, nil); err != nil {
		t.Fatalf("终审合法单被拒: %v", err)
	}
	f.Findings[0].Fix = "redraw"
	if err := f.Validate(profFinal, nil); err == nil {
		t.Fatal("终审带 fix 应被拒（处置由程序映射）")
	}
}

func TestSignatures(t *testing.T) {
	v := segVerdict()
	v.Findings = append(v.Findings, Finding{Category: "style", Anchor: "t=4.0", Severity: "low", Evidence: "密度偏高", Fix: "pass"})
	got := v.Signatures()
	if len(got) != 1 || got[0] != "fact@N3" {
		t.Fatalf("签名应只含 high（fact@N3），得到 %v", got)
	}
}
