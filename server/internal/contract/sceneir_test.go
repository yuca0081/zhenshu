package contract

import (
	"strings"
	"testing"
)

func validIR() *SceneIR {
	return &SceneIR{
		Elements: []SceneIRElement{
			{ID: "paper", Name: "底", HTML: "<div></div>", Duration: 5},
			{ID: "tri", Name: "三角", HTML: "<svg></svg>", Duration: 5},
			{ID: "title", Name: "标题", HTML: "<div>字</div>", Duration: 5},
		},
		Cues: []SceneIRCue{
			{Target: "#tri", Kind: "to", To: map[string]any{"opacity": 1}, At: 0.2, Dur: 0.5},
		},
	}
}

func TestValidateSceneIRAcceptsMinimal(t *testing.T) {
	if errs := ValidateSceneIR(validIR(), 5, 1920, 1080); len(errs) > 0 {
		t.Fatalf("合法 IR 被拒: %v", errs)
	}
}

func TestValidateSceneIRRejectsCSSTransformOnCuedElement(t *testing.T) {
	ir := validIR()
	ir.Elements[1].CSS = "#tri{transform:translateX(-50%);}"
	errs := ValidateSceneIR(ir, 5, 1920, 1080)
	if len(errs) == 0 {
		t.Fatal("cued 元素的 CSS transform 应被拒（T9）")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e, "transform") {
			found = true
		}
	}
	if !found {
		t.Fatalf("错误消息没说明 transform：%v", errs)
	}
}

func TestValidateSceneIRRejectsBadCue(t *testing.T) {
	ir := validIR()
	ir.Cues[0].Target = "#missing"
	if errs := ValidateSceneIR(ir, 5, 1920, 1080); len(errs) == 0 {
		t.Fatal("cue 指向不存在元素应被拒")
	}
	ir = validIR()
	ir.Cues[0].At = 9
	if errs := ValidateSceneIR(ir, 5, 1920, 1080); len(errs) == 0 {
		t.Fatal("cue 超出段长应被拒")
	}
}
