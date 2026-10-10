package agent

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// 音色别名漂移门禁：voiceAliases（Go）与 VOICE_ALIASES（ai/tts_align.py）是隐式契约
// ——Go 侧放行的中文名，Py 侧必须翻译得出同一个音色 id，否则音色被静默换成默认。
func TestVoiceAliasesMatchPython(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "ai", "tts_align.py"))
	if err != nil {
		t.Fatalf("tts_align.py 不可读: %v", err)
	}
	py := string(b)
	for cn, id := range voiceAliases {
		re := regexp.MustCompile(regexp.QuoteMeta(cn) + `'\s*:\s*'` + regexp.QuoteMeta(id) + `'`)
		if !re.MatchString(py) {
			t.Errorf("别名 %q→%q 在 ai/tts_align.py VOICE_ALIASES 中缺失或不一致", cn, id)
		}
	}
}
