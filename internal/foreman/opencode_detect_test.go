package foreman

import (
	"reflect"
	"testing"
)

// Sample copied from a real `opencode models` listing: apinex/beatapi/
// codebuddy/ovh/tokenharbor/reflection routes plus paid and free Zen routes.
const sampleOpencodeModels = `apinex/free/claude-opus-4.6
apinex/free/deepseek-v4.1-flash
beatapi/deepseek-v4.1-flash-free
gateway/codebuddy/auto
gateway/codebuddy/glm-5.3
gateway/llm7/gpt-oss:20b
gateway/opencode/beam-free
gateway/opencode/big-pickle
gateway/opencode/deepseek-v4.1-flash-free
gateway/opencode/flint-40b-free
gateway/opencode/glm-5.3-flash-free
gateway/opencode/mimo-v2.6-flash-free
gateway/opencode/nemotron-3.5-lightning-free
gateway/opencode/nemotron-3.5-super-vl-free
gateway/ovh/gpt-oss-20b
gateway/reflection/beam-free
gateway/tokenharbor/deepseek-v4-flash:free
gateway/tokenharbor/deepseek-v4.1-flash:free
gateway/tokenharbor/mimo-v2.5:free
`

// The OpenCode Zen preset only serves the Zen gateway's free models: paid Zen
// routes (big-pickle) and free models on other gateways (apinex, beatapi,
// codebuddy, tokenharbor, reflection) must never auto-fill the preset. The
// CLI's `gateway/opencode/` provider prefix is stripped so the Zen API's bare
// model ids (`glm-5.3-flash-free`, not `gateway/opencode/glm-5.3-flash-free`)
// land in the model list.
func TestZenFreeModelsStrictOpenCodeZenOnly(t *testing.T) {
	got := zenFreeModels(sampleOpencodeModels)
	want := []string{
		"beam-free",
		"deepseek-v4.1-flash-free",
		"flint-40b-free",
		"glm-5.3-flash-free",
		"mimo-v2.6-flash-free",
		"nemotron-3.5-lightning-free",
		"nemotron-3.5-super-vl-free",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("zenFreeModels = %v, want %v", got, want)
	}
}

// `:free` on another gateway is not Zen and must be dropped; a `:free` inside
// the Zen gateway itself is kept (with the prefix stripped).
func TestZenFreeModelsKeepsColonFreeUnderZenPrefix(t *testing.T) {
	got := zenFreeModels("gateway/tokenharbor/deepseek-v4-flash:free\ngateway/opencode/gpt-5-mini:free\n# comment\n\n")
	want := []string{"gpt-5-mini:free"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("zenFreeModels = %v, want %v", got, want)
	}
}

func TestZenFreeModelsEmptyAndDedup(t *testing.T) {
	if got := zenFreeModels("apinex/free/gemini-3.8-flash\ngateway/opencode/big-pickle\n"); len(got) != 0 {
		t.Fatalf("expected no zen-free models, got %v", got)
	}
	// Dedup happens on the stripped id: two lines with different providers
	// collapse to one entry.
	if got := zenFreeModels("gateway/opencode/glm-5.3-flash-free\ngateway/opencode/glm-5.3-flash-free\n"); len(got) != 1 || got[0] != "glm-5.3-flash-free" {
		t.Fatalf("expected dedup to one stripped id, got %v", got)
	}
}
