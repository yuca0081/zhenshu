package methodlib

import (
	"regexp"
	"testing"
	"time"
)

// 秒级 ID 同秒起草两次必须拿到不同 id（历史缺陷：成片提炼与视频解析
// 两入口并发撞号，templates.json 出现重复主键）。
func TestNewPackIDDedup(t *testing.T) {
	packs := []StylePack{{ID: "sp" + time.Now().Format("20060102-150405")}}
	id := newPackID(packs)
	if id == packs[0].ID {
		t.Fatalf("同秒撞号未退让: %s", id)
	}
	if ok, _ := regexp.MatchString(`^sp\d{8}-\d{6}-\d+$`, id); !ok {
		t.Fatalf("退让 id 形态不对: %s", id)
	}
	// 无冲突时保持裸时间戳形态
	if id2 := newPackID(nil); id2 == id {
		t.Fatalf("无冲突场景不应追加序号: %s", id2)
	}
}
