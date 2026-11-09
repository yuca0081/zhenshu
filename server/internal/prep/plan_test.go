package prep

// plan_test.go —— 勾选集 → 执行闭包（主题页「本次生成」三选项）的行为锁。
// 链式依赖是产品事实：③要①+②、②要①；勾选不是三个独立开关。这里固化
// 「缺前置补跑、上游重跑下游跟跑、只勾上游则下游留空」三档语义。

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"webpage-video-agent/server/internal/pipeline"
)

// planFixture 建一个只有指定工位工件的项目（其余目录不建）。
func planFixture(t *testing.T, have ...string) *pipeline.Project {
	t.Helper()
	p := pipeline.NewProject(t.TempDir(), "p-plan")
	if err := os.MkdirAll(p.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, s := range have {
		f := artifactPath(p, s)
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func TestPlanStationsClosure(t *testing.T) {
	all := []string{StationScript, StationStyle, StationStoryboard}
	cases := []struct {
		name   string
		have   []string
		want   map[string]bool
		expect []string
	}{
		{
			name:   "全选/空工件 → 三站全跑",
			want:   map[string]bool{StationScript: true, StationStyle: true, StationStoryboard: true},
			expect: all,
		},
		{
			name:   "只勾文稿 → 只跑①（下游留空）",
			want:   map[string]bool{StationScript: true},
			expect: []string{StationScript},
		},
		{
			name:   "只勾分镜但全缺 → 前置自动补跑",
			want:   map[string]bool{StationStoryboard: true},
			expect: all,
		},
		{
			name:   "只勾分镜且①②已在 → 只跑③",
			have:   []string{StationScript, StationStyle},
			want:   map[string]bool{StationStoryboard: true},
			expect: []string{StationStoryboard},
		},
		{
			name:   "勾文稿+分镜 → 新稿使风格失效，②必须跟跑",
			have:   []string{StationScript, StationStyle},
			want:   map[string]bool{StationScript: true, StationStoryboard: true},
			expect: all,
		},
		{
			name:   "勾文稿+风格 → ①②跑，③留空",
			have:   []string{StationScript, StationStyle, StationStoryboard},
			want:   map[string]bool{StationScript: true, StationStyle: true},
			expect: []string{StationScript, StationStyle},
		},
		{
			name:   "只勾风格且文稿已在 → 只跑②",
			have:   []string{StationScript},
			want:   map[string]bool{StationStyle: true},
			expect: []string{StationStyle},
		},
		{
			name:   "只勾风格但文稿缺 → ①补跑",
			want:   map[string]bool{StationStyle: true},
			expect: []string{StationScript, StationStyle},
		},
		{
			name:   "只勾分镜、风格缺 → ②补跑",
			have:   []string{StationScript},
			want:   map[string]bool{StationStoryboard: true},
			expect: []string{StationStyle, StationStoryboard},
		},
		{
			name:   "空勾选 → 不跑",
			want:   map[string]bool{},
			expect: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := planFixture(t, tc.have...)
			want := make(map[string]bool, len(tc.want))
			for k, v := range tc.want {
				want[k] = v
			}
			got := planStations(p, want)
			if !reflect.DeepEqual(got, tc.expect) {
				t.Fatalf("plan = %v, want %v", got, tc.expect)
			}
		})
	}
}

func TestParseStations(t *testing.T) {
	got, err := parseStations(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{StationScript, StationStyle, StationStoryboard} {
		if !got[s] {
			t.Fatalf("空勾选应默认全选，缺 %s", s)
		}
	}
	if _, err := parseStations([]string{"script", "bogus"}); err == nil {
		t.Fatal("未知工位应报错")
	}
}
