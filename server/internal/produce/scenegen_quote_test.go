package produce

import (
	"strings"
	"testing"
)

// 2026-10-30 para05 实测：模型产出 window.__timelines['para05']（单引号，JS 等价），
// 旧双引号字面量匹配恒判「未注册」，六轮重生成全灭（自动重跑 2/2 烧完）。两处逐字
// 标记必须引号无关——与 scene_lint.py T4 __draw_ 的 2026-10-29 单引号教训同款。
func TestContractMarkErrsQuoteAgnostic(t *testing.T) {
	ok := []struct{ name, attr, tl string }{
		{"双引号", `"para05"`, `window.__timelines["para05"]=tl;`},
		{"单引号", `'para05'`, `window.__timelines['para05']=tl;`},
		{"属性单引号+注册带空白", `'para05'`, `window.__timelines [ "para05" ] = tl;`},
	}
	for _, c := range ok {
		html := `<template><div data-composition-id=` + c.attr + `></div><script>` + c.tl + `</script></template>`
		if errs := contractMarkErrs(html, "para05"); len(errs) != 0 {
			t.Errorf("%s：引号/空白不构成违规，得 %v", c.name, errs)
		}
	}
	// key 逐字一致语义不变：错段 id 两条都要报
	bad := `<template><div data-composition-id="para04"></div><script>window.__timelines["para04"]=tl;</script></template>`
	errs := contractMarkErrs(bad, "para05")
	if len(errs) != 2 {
		t.Fatalf("错段 id 应报 2 条，得 %v", errs)
	}
	for _, e := range errs {
		if !strings.Contains(e, `"para05"`) {
			t.Errorf("报错文案应保留双引号范本（对人的说明）: %s", e)
		}
	}
}
