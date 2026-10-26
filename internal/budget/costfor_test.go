package budget

import (
	"testing"

	"github.com/vulnetix/belai/internal/transcript"
)

func TestCostForPricesOneCall(t *testing.T) {
	u := transcript.Usage{PromptTokens: 10000, CompletionTokens: 1000, CacheReadTokens: 6000, CacheWriteTokens: 2000}
	factory := &Price{Input: 1, Output: 2}
	// 2000 uncached + 6000 cache read + 2000 cache write at 1 µ$, 1000 out at 2 µ$.
	factoryCost := int64(2000 + 6000 + 2000 + 1000*2)
	cases := []struct {
		name            string
		provider, model string
		override        *Price
		want            int64
		known           bool
	}{
		{"built-in table", "anthropic", "claude-sonnet-4-5", nil, 6000 + 1800 + 7500 + 15000, true},
		{"factory price wins over the table", "anthropic", "claude-sonnet-4-5", factory, factoryCost, true},
		{"factory price for a model the table lacks", "custom", "my-finetune", factory, factoryCost, true},
		{"local provider is free", "ollama", "qwen3", nil, 0, true},
		{"explicit free override", "custom", "local-gguf", &Price{}, 0, true},
		{"unpriced model has no cost", "custom", "my-finetune", nil, 0, false},
		{"an invalid override is ignored", "custom", "my-finetune", &Price{Input: -1}, 0, false},
	}
	for _, c := range cases {
		got, known := CostFor(c.provider, c.model, c.override, u)
		if got != c.want || known != c.known {
			t.Errorf("%s: CostFor = %d, %v; want %d, %v", c.name, got, known, c.want, c.known)
		}
	}
}
