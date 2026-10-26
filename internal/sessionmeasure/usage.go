package sessionmeasure

import (
	"github.com/vulnetix/belai/internal/budget"
	"github.com/vulnetix/belai/internal/transcript"
)

// AddUsage writes one model call's usage into an assistant entry's meta:
//
//	prompt_tokens, completion_tokens, total_tokens   (as before)
//	cache_read_tokens, cache_write_tokens            (0 when the provider reports none)
//	cost_micro_usd                                   (only when the model has a price)
//
// The cost is budget.CostFor at provider/model: a price the Software Factory
// sent (override) wins, a local provider is free, and a model nobody has a
// price for gets no cost key, so the server keeps its tokens without reading a
// zero as free. meta must not be nil.
func AddUsage(meta map[string]any, provider, model string, override *budget.Price, u transcript.Usage) {
	meta["prompt_tokens"] = u.PromptTokens
	meta["completion_tokens"] = u.CompletionTokens
	meta["total_tokens"] = u.Total()
	meta["cache_read_tokens"] = u.CacheReadTokens
	meta["cache_write_tokens"] = u.CacheWriteTokens
	if micro, ok := budget.CostFor(provider, model, override, u); ok {
		meta["cost_micro_usd"] = micro
	}
}
