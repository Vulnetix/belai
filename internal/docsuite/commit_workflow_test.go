package docsuite

import (
	"fmt"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/docparity"
	"github.com/vulnetix/belai/internal/forge"
)

// TestCommitWorkflowPageStatesTheModesAndLimits pins the page to the code.
func TestCommitWorkflowPageStatesTheModesAndLimits(t *testing.T) {
	doc := docparity.Read(t, "docs/commit-workflow.md")
	for _, mode := range config.CommitModeNames {
		if !strings.Contains(doc, "| `"+mode+"` |") {
			t.Errorf("docs/commit-workflow.md has no row for commit_mode %q", mode)
		}
	}
	requirePhrases(t, "docs/commit-workflow.md",
		"`/commit [message]`",
		"| `commit_mode` |",
		"| `commit_draft` |",
		"| `commit_exclude` |",
		fmt.Sprintf("at most %d of at most %d characters", config.MaxCommitExclude, config.MaxCommitExcludeLength),
		fmt.Sprintf("more than %d dirty paths", forge.MaxDirtyPaths),
		fmt.Sprintf("over %d MiB", forge.MaxCommitFileBytes>>20),
		"`commit_ask`", "`auto_commit`", "`refused`",
		".vulnetix/belai/commits/<session>.json",
		"nothing is pushed",
	)
	for _, typ := range forge.ConventionalTypes {
		if !strings.Contains(doc, "`"+typ+"`") {
			t.Errorf("docs/commit-workflow.md does not list the conventional type %q", typ)
		}
	}
	requirePhrases(t, "AGENTS.md", "The commit workflow is the harness's, and a model supplies words at most.")
	requirePhrases(t, "docs/settings.md", "`commit_mode`", "`commit_draft`", "`commit_exclude`")
	requirePhrases(t, "docs/README.md", "[Commit workflow](commit-workflow.md)")
}
