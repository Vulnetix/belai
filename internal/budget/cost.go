package budget

import (
	"math"
	"strings"

	"github.com/vulnetix/belai/internal/transcript"
)

// Price is what a model charges, in US dollars per million tokens. Input is the
// prompt tokens the cache did not serve; CacheRead and CacheWrite the parts of
// the prompt the provider read from, or wrote to, its prompt cache. A zero
// cache price falls back to Input.
type Price struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read,omitempty"`
	CacheWrite float64 `json:"cache_write,omitempty"`
}

// Known reports whether the price can cost a call.
func (p Price) Known() bool { return p.Input > 0 || p.Output > 0 }

// MaxPricePerMillion bounds a price a dispatch may set, so a mistyped price
// cannot make every call look free or end every claim on its first call.
const MaxPricePerMillion = 1000.0

// Valid reports whether every component is a finite number in range.
func (p Price) Valid() bool {
	for _, v := range []float64{p.Input, p.Output, p.CacheRead, p.CacheWrite} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > MaxPricePerMillion {
			return false
		}
	}
	return true
}

// MicroUSD is the cost of one call's usage in millionths of a dollar, rounded
// up so a run of tiny calls is never free.
func (p Price) MicroUSD(u transcript.Usage) int64 {
	if !p.Known() {
		return 0
	}
	read, write := p.CacheRead, p.CacheWrite
	if read == 0 {
		read = p.Input
	}
	if write == 0 {
		write = p.Input
	}
	uncached := max(u.PromptTokens-u.CacheReadTokens-u.CacheWriteTokens, 0)
	out := u.CompletionTokens
	if out == 0 && u.TotalTokens > u.PromptTokens {
		out = u.TotalTokens - u.PromptTokens
	}
	// $/M tokens is exactly µ$ per token.
	micro := float64(uncached)*p.Input + float64(u.CacheReadTokens)*read + float64(u.CacheWriteTokens)*write + float64(out)*p.Output
	return int64(math.Ceil(micro))
}

// builtinPrices are list prices for the model families Belai ships defaults
// for, matched by the longest id prefix after the provider's own prefix (a
// "vendor/" router prefix is dropped). They are estimates for the per-item
// cost bound; the Software Factory sends the prices it bills by, which win.
var builtinPrices = []struct {
	prefix string
	price  Price
}{
	{"claude-opus", Price{Input: 15, Output: 75, CacheRead: 1.5, CacheWrite: 18.75}},
	{"claude-sonnet", Price{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75}},
	{"claude-haiku", Price{Input: 1, Output: 5, CacheRead: 0.1, CacheWrite: 1.25}},
	{"claude-fable", Price{Input: 15, Output: 75, CacheRead: 1.5, CacheWrite: 18.75}},
	{"gpt-4o-mini", Price{Input: 0.15, Output: 0.6, CacheRead: 0.075}},
	{"gpt-4o", Price{Input: 2.5, Output: 10, CacheRead: 1.25}},
	{"gpt-4.1-mini", Price{Input: 0.4, Output: 1.6, CacheRead: 0.1}},
	{"gpt-4.1", Price{Input: 2, Output: 8, CacheRead: 0.5}},
	{"gpt-5-mini", Price{Input: 0.25, Output: 2, CacheRead: 0.025}},
	{"gpt-5", Price{Input: 1.25, Output: 10, CacheRead: 0.125}},
	{"o3", Price{Input: 2, Output: 8, CacheRead: 0.5}},
	{"gemini-2.5-pro", Price{Input: 1.25, Output: 10, CacheRead: 0.31}},
	{"gemini-2.5-flash", Price{Input: 0.3, Output: 2.5, CacheRead: 0.075}},
	{"deepseek", Price{Input: 0.28, Output: 0.42, CacheRead: 0.028}},
	{"grok-4", Price{Input: 3, Output: 15, CacheRead: 0.75}},
}

// localProviders run on the machine: their calls cost nothing per token.
var localProviders = map[string]bool{"ollama": true, "llama-server": true, "localinfer": true, "lmstudio": true}

// PriceFor is the built-in price of provider/model and whether one is known. A
// local provider is known and free.
func PriceFor(provider, model string) (Price, bool) {
	if localProviders[strings.ToLower(provider)] {
		return Price{}, true
	}
	id := strings.ToLower(model)
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	best, found := Price{}, -1
	for _, e := range builtinPrices {
		if strings.HasPrefix(id, e.prefix) && len(e.prefix) > found {
			best, found = e.price, len(e.prefix)
		}
	}
	return best, found >= 0
}
