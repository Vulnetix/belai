package docsuite

import (
	"fmt"
	"testing"

	"github.com/vulnetix/belai/internal/checkpoint"
	"github.com/vulnetix/belai/internal/config"
)

// TestCheckpointsPageStatesTheDefaults pins the settings table and the limits
// in docs/checkpoints.md to the code.
func TestCheckpointsPageStatesTheDefaults(t *testing.T) {
	requirePhrases(t, "docs/checkpoints.md",
		"| `checkpoints.enabled` | Keep file checkpoints. | `true` |",
		fmt.Sprintf("1 to 500. | `%d` |", config.DefaultCheckpointsMaxPerSession),
		fmt.Sprintf("| `%d` (200 MiB) |", config.DefaultCheckpointsMaxBytes),
		fmt.Sprintf("1 to 365. | `%d` |", config.DefaultCheckpointsRetainDays),
		fmt.Sprintf("a file over %d MiB", checkpoint.MaxFileBytes>>20),
		"-no-checkpoints",
		"/undo",
		"/rewind",
	)
	requirePhrases(t, "AGENTS.md", "File checkpoints are the user's")
}
