package docsuite

import (
	"fmt"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/fleet"
)

// TestWorktreesPageStatesTheBoundsAndTheRules pins docs/worktrees.md and the
// pages that point at it to the code.
func TestWorktreesPageStatesTheBoundsAndTheRules(t *testing.T) {
	requirePhrases(t, "docs/worktrees.md",
		"**Status:** new.",
		"`/worktree new TITLE`", "`/worktree list`", "`/worktree update ID`", "`/worktree drop ID`",
		"`belai worktree new [-agent NAME] [-label L] TITLE`",
		"`belai worktree list [-json]`", "`belai worktree update [-yes] ID`", "`belai worktree drop [-yes] ID`",
		fmt.Sprintf("More than %d such files fails the setup", fleet.MaxSetupOutputPaths),
		fmt.Sprintf("at most %d", 12),
		"`worktree` label",
		"Teardown does not run for a worktree whose setup did not finish.",
		"A teardown that fails keeps the worktree.",
		"There is no model-facing tool that updates or removes a worktree.",
		"<state>/agents/worktrees/<project key>/<id>.json",
	)
	for _, mode := range config.WorktreeUpdateModes {
		requirePhrases(t, "docs/worktrees.md", "`"+mode+"`")
	}
	requirePhrases(t, "AGENTS.md", "Worktree setup and teardown are the user's argv, run by the harness, and their output classifies.")
	requirePhrases(t, "docs/settings.md", "`worktrees.setup`", "`worktrees.update_mode`", "`worktrees.network`")
	requirePhrases(t, "docs/README.md", "[Worktree sessions](worktrees.md)")
	requirePhrases(t, "docs/sanitization.md", "## Worktree setup and teardown output")
	requirePhrases(t, "README.md", "`belai worktree`", "`/worktree`", "docs/worktrees.md")
}
