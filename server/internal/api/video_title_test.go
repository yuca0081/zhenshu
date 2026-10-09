package api

import "testing"

func TestVideoTitle(t *testing.T) {
	cases := []struct {
		filename, fallback, want string
	}{
		{"03_震惊瘫坐！Opus 5.5生成短片《我从未见过太阳》_BV1b9ad6xEnD.mp4", "数学可视化",
			"震惊瘫坐！Opus 5.5生成短片《我从未见过太阳》"},
		{"卡带未来主义.mp4", "卡带未来主义", "卡带未来主义"},
		{"08_Opus5.5一句话核爆考研408统考 动画MV_BV12kae6WEPT__P02_Opus5.5一句话核爆考研408统考 动画MV p02.mp4", "暗金学院风",
			"Opus5.5一句话核爆考研408统考 动画MV P2"},
		{"video.mp4", "终端叙事UI", "终端叙事UI"},        // 无标题信息：回退风格名
		{"01_卡带未来主义.mp4", "X", "卡带未来主义"},       // 纯编号前缀剥掉
		{"11_网传：只是给 Claude 发了一句话_BV1GJa869EP9.mp4", "X", "网传：只是给 Claude 发了一句话"},
	}
	for _, c := range cases {
		if got := videoTitle(c.filename, c.fallback); got != c.want {
			t.Errorf("videoTitle(%q) = %q, want %q", c.filename, got, c.want)
		}
	}
}
