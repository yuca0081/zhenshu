package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/pipeline"
)

// 分镜页保存后镜头字段必须回写工件 C：终审镜头门读 draft.json，不回写就会出现
// 「分镜页补好了，终审仍报缺镜」的双源分裂。
func TestSyncDraftShots(t *testing.T) {
	p := pipeline.NewProject(t.TempDir(), "p1")
	dir := filepath.Join(p.Dir, "storyboards")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dr := &contract.StoryboardDraft{
		World: "旧世界", CastLegend: "旧角色",
		Segments: []contract.DraftSegment{
			{ID: "seg01", Idx: 1, Key: "一"},
			{ID: "seg02", Idx: 2, Key: "二"},
		},
	}
	raw, _ := json.Marshal(dr)
	if err := os.WriteFile(filepath.Join(dir, "draft.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	shot := &contract.ShotDirection{
		Size: "medium", Camera: "横摇", Stage: "木桌", Action: "点脚尖",
		Cast: []contract.ShotCast{{Who: "AI", Pose: "俯身"}},
	}
	sb := &contract.Storyboard{
		World: "新世界", CastLegend: "新角色",
		Segments: []contract.Segment{
			{ID: "seg01", Shot: shot},
			{ID: "seg02"},
		},
	}
	if err := syncDraftShots(p, sb); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "draft.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got contract.StoryboardDraft
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.World != "新世界" || got.CastLegend != "新角色" {
		t.Errorf("世界/角色表未回写: world=%q cast=%q", got.World, got.CastLegend)
	}
	if got.Segments[0].Shot == nil || got.Segments[0].Shot.Camera != "横摇" {
		t.Errorf("seg01 镜头未回写: %+v", got.Segments[0].Shot)
	}
	if got.Segments[1].Shot != nil {
		t.Errorf("seg02 不应凭空有镜头: %+v", got.Segments[1].Shot)
	}
}
