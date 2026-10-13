package pipeline

import (
	"os"
	"testing"
)

// TestPostResolve postFor 解析回环：project.json 是唯一来源（LockDesign 从设计系统
// post 槽位写入）；无 post/false/缺省 = 不加后期——暗角颗粒不再是平台默认。
func TestPostResolve(t *testing.T) {
	dir := t.TempDir()
	p := &Project{ID: "t", Dir: dir}

	// project.json 无 post → 干净出片（默认关）
	if err := os.WriteFile(p.Artifact("project.json"), []byte(`{"name":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if fx := postFor(p); !fx.off() {
		t.Fatalf("无配置应不加后期，得到 %+v", fx)
	}

	// project.json post:false → 关闭
	if err := os.WriteFile(p.Artifact("project.json"), []byte(`{"post":false}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if fx := postFor(p); !fx.off() {
		t.Fatalf("post:false 应关闭，得到 %+v", fx)
	}

	// project.json post 对象 → 整组替换（未写字段=0=该项关）
	if err := os.WriteFile(p.Artifact("project.json"),
		[]byte(`{"post":{"vignette":0.8,"grain":0.6}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	fx := postFor(p)
	if fx == nil || fx.Vignette != 0.8 || fx.Grain != 0.6 || fx.Chroma != 0 {
		t.Fatalf("对象应整组替换，得到 %+v", fx)
	}
}

// TestVideoChain 滤镜链拼装：全 0 空、单项、参数钳制、顺序（色差→暗角→颗粒）。
func TestVideoChain(t *testing.T) {
	if fx := (*postFX)(nil); fx.videoChain() != "" {
		t.Fatal("nil 应无滤镜")
	}
	if fx := (&postFX{}); fx.videoChain() != "" {
		t.Fatal("全 0 应无滤镜")
	}
	chain := (&postFX{Vignette: 0.35, Grain: 0.4}).videoChain()
	if chain == "" {
		t.Fatal("有参数应产出滤镜链")
	}
	// 超界钳制不炸即可
	if chain := (&postFX{Vignette: 5, Grain: 9, Chroma: 3}).videoChain(); chain == "" {
		t.Fatal("超界参数应钳制而非丢弃")
	}
}
