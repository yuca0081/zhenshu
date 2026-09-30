package agent

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 音色别名漂移门禁：voiceAliases（Go）与 VOICE_ALIASES（ai/tts_align.py）是隐式契约
//——Go 侧放行的中文名，Py 侧必须翻译得出同一个 Edge id，否则音色被静默换成默认。
// CosyVoice 短名同理（cosyVoiceNames ↔ COSY_VOICES）。
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
	cosy := regexp.MustCompile(`COSY_VOICES\s*=\s*\(([^)]*)\)`).FindStringSubmatch(py)
	if cosy == nil {
		t.Fatal("ai/tts_align.py 缺 COSY_VOICES 元组")
	}
	for name := range cosyVoiceNames {
		if !strings.Contains(cosy[1], "'"+name+"'") {
			t.Errorf("CosyVoice 音色 %q 在 ai/tts_align.py COSY_VOICES 中缺失", name)
		}
	}
}
