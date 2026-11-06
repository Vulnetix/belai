package docsuite

import (
	"fmt"
	"testing"

	"github.com/vulnetix/belai/internal/config"
)

// TestPromptCachePageStatesTheDefaults pins the settings table and the
// command table in docs/prompt-cache.md to the code.
func TestPromptCachePageStatesTheDefaults(t *testing.T) {
	requirePhrases(t, "docs/prompt-cache.md",
		fmt.Sprintf("| `prompt_cache.ttl` | Cache lifetime on every breakpoint: `5m` or `1h`. | `%s` |", config.PromptCacheTTL5m),
		fmt.Sprintf("0 to %d. `0` is off. | `0` |", config.MaxPromptCacheKeepalive),
		fmt.Sprintf("%d to %d. | %d with `5m`, %d with `1h` |", config.MinPromptCacheEveryS, config.MaxPromptCacheEvery1hS, config.DefaultPromptCacheEvery5mS, config.DefaultPromptCacheEvery1hS),
		fmt.Sprintf("at most %d seconds with `5m` and %d with `1h`", config.MaxPromptCacheEvery5mS, config.MaxPromptCacheEvery1hS),
		"`cache_prefix_changed`",
		"role `keepalive`",
		"/cache keepalive N",
	)
}
