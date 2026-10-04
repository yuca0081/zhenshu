package stylelib

import (
	"strings"
	"testing"
)

// subject（原片题材）只存条目供浏览/检索：新条目带出；同 seed 重解析只补缺不覆盖；
// 老条目升级时能补上。它永不进 StyleSamplesJSON（生成提示词），那是题材中毒的口子。
func TestIngestVideoRefSubject(t *testing.T) {
	lib := New(t.TempDir())
	mk := func(subject string) VideoStyleIn {
		return VideoStyleIn{
			Direction: "等距视图（硬几何）", Subject: subject, Genre: "城市",
			Samples: []Sample{{Tag: "A", HTML: "<!doctype html>"}},
		}
	}
	st, err := lib.IngestVideoRef("ref1", mk("等距视角的城市宣传动画"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Subject != "等距视角的城市宣传动画" {
		t.Fatalf("新条目应带 subject: %+v", st)
	}
	// 同 seed 再入库（用户重解析同视频）：已填的 subject 不被清掉
	st2, err := lib.IngestVideoRef("ref1", mk(""))
	if err != nil {
		t.Fatal(err)
	}
	if st2.Subject != "等距视角的城市宣传动画" {
		t.Fatalf("升级不应覆盖已填 subject: %+v", st2)
	}
	// 老条目没 subject → 重解析补齐
	if _, err := lib.IngestVideoRef("ref2", mk("")); err != nil {
		t.Fatal(err)
	}
	st3, err := lib.IngestVideoRef("ref2", mk("黑板手绘课程动画"))
	if err != nil {
		t.Fatal(err)
	}
	if st3.Subject != "黑板手绘课程动画" {
		t.Fatalf("缺 subject 的条目应被升级补齐: %+v", st3)
	}
}

// StyleSamplesJSON（进项目、进生成提示词的那份）不得携带题材元数据。
func TestStyleSamplesJSONOmitsSubjectMeta(t *testing.T) {
	s := Style{
		ID: "vid-x", Direction: "等距视图", Subject: "城市宣传片", Genre: "城市",
		Keywords: []string{"楼宇", "街道"},
		Samples:  []Sample{{Tag: "A", HTML: "<!doctype html>"}},
	}
	b := string(s.StyleSamplesJSON())
	for _, poison := range []string{"城市宣传片", "楼宇", "街道", "适用题材"} {
		if strings.Contains(b, poison) {
			t.Fatalf("StyleSamplesJSON 不应携带题材元数据 %q：%s", poison, b)
		}
	}
}
