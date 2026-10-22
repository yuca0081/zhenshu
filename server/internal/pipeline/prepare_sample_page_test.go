package pipeline

// prepare_sample_page_test.go —— 动态样张页机械整形（prep② 渲染前的确定性补齐）。

import (
	"strings"
	"testing"
)

func TestPrepareSamplePage(t *testing.T) {
	// 模型漏写 gsap/hf 引用 + 根缺 duration：都应机械补齐
	in := `<!doctype html><html><head><meta charset="UTF-8"><style>body{}</style></head>
<body><div data-composition-id="main" data-width="1080" data-height="1920" data-start="0">
<p>画面</p></div>
<script>window.__timelines=window.__timelines||{};window.__timelines["main"]=gsap.timeline({paused:true});</script>
</body></html>`
	out := PrepareSamplePage(in, 1.8)
	if !strings.Contains(out, `<script src="assets/gsap.min.js"></script>`) {
		t.Fatalf("应补 gsap 引用\n%s", out)
	}
	if !strings.Contains(out, `<script src="assets/hf.js"></script>`) {
		t.Fatalf("应补 hf.js 引用\n%s", out)
	}
	if !strings.Contains(out, `data-duration="1.80"`) {
		t.Fatalf("应补根 data-duration\n%s", out)
	}
	// 幂等：整形后再整形不变
	if again := PrepareSamplePage(out, 1.8); again != out {
		t.Fatalf("应幂等\n--- first\n%s\n--- again\n%s", out, again)
	}
	// 模型自己写全了：一字不动
	full := `<!doctype html><html><head><script src="assets/gsap.min.js"></script><script src="assets/hf.js"></script></head>
<body><div data-composition-id="main" data-width="1080" data-height="1920" data-start="0" data-duration="1.80"></div></body></html>`
	if got := PrepareSamplePage(full, 1.8); got != full {
		t.Fatalf("写全了不该动\n--- got\n%s", got)
	}
	// 已有 duration 不覆盖
	has := `<!doctype html><html><head></head><body><div data-composition-id="main" data-duration="2.50"></div></body></html>`
	if got := PrepareSamplePage(has, 1.8); !strings.Contains(got, `data-duration="2.50"`) {
		t.Fatalf("已有 duration 不该被覆盖\n%s", got)
	}
}
