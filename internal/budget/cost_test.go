package budget

import (
	"math"
	"testing"

	"github.com/vulnetix/belai/internal/transcript"
)

func TestPriceCostsEachPartOfACall(t *testing.T) {
	p := Price{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75}
	u := transcript.Usage{PromptTokens: 10000, CompletionTokens: 1000, CacheReadTokens: 6000, CacheWriteTokens: 2000}
	// 2000 uncached × 3 + 6000 × 0.3 + 2000 × 3.75 + 1000 × 15 µ$
	if got, want := p.MicroUSD(u), int64(6000+1800+7500+15000); got != want {
		t.Fatalf("cost = %d µ$, want %d", got, want)
	}
}

func TestACachePriceOfZeroIsTheInputPrice(t *testing.T) {
	p := Price{Input: 2, Output: 8}
	u := transcript.Usage{PromptTokens: 100, CacheReadTokens: 100}
	if got := p.MicroUSD(u); got != 200 {
		t.Fatalf("cost = %d, want 200", got)
	}
}

func TestOutputComesFromTheTotalWhenOnlyTheTotalIsReported(t *testing.T) {
	p := Price{Input: 1, Output: 10}
	if got := p.MicroUSD(transcript.Usage{PromptTokens: 10, TotalTokens: 15}); got != 60 {
		t.Fatalf("cost = %d, want 60", got)
	}
}

func TestAnUnknownPriceCostsNothing(t *testing.T) {
	if got := (Price{}).MicroUSD(transcript.Usage{PromptTokens: 1e6}); got != 0 {
		t.Fatalf("cost = %d", got)
	}
}

func TestPriceForMatchesTheLongestPrefix(t *testing.T) {
	mini, ok := PriceFor("openai", "gpt-4o-mini-2024-07-18")
	if !ok || mini.Input != 0.15 {
		t.Fatalf("gpt-4o-mini: %+v %v", mini, ok)
	}
	routed, ok := PriceFor("openrouter", "anthropic/claude-sonnet-4.5")
	if !ok || routed.Output != 15 {
		t.Fatalf("routed sonnet: %+v %v", routed, ok)
	}
	if _, ok := PriceFor("custom", "my-finetune"); ok {
		t.Fatal("an unknown model has a price")
	}
	if p, ok := PriceFor("ollama", "qwen3"); !ok || p.Known() {
		t.Fatalf("a local model is not known and free: %+v %v", p, ok)
	}
}

func TestPriceValidRefusesNonsense(t *testing.T) {
	for _, p := range []Price{{Input: -1}, {Output: math.NaN()}, {Input: math.Inf(1)}, {CacheRead: MaxPricePerMillion + 1}} {
		if p.Valid() {
			t.Errorf("%+v is valid", p)
		}
	}
	if !(Price{Input: 3, Output: 15}).Valid() {
		t.Error("a normal price is invalid")
	}
}
