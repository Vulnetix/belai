package docsuite

import (
	"fmt"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/rolemanager"
)

// TestContextOffloadPageStatesTheDefaults pins the settings table and the
// WebFetch page limit in docs/context-offload.md to the code.
func TestContextOffloadPageStatesTheDefaults(t *testing.T) {
	requirePhrases(t, "docs/context-offload.md",
		fmt.Sprintf("| `offload.threshold_tokens` | `%d` |", config.DefaultOffloadThresholdTokens),
		fmt.Sprintf("| `offload.preview_tokens` | `%d` |", config.DefaultOffloadPreviewTokens),
		"| `offload.enabled` | `true` |",
		fmt.Sprintf("sanitised page text (up to %s characters)", comma(rolemanager.WebFetchMaxPageChars)),
	)
}

func comma(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
