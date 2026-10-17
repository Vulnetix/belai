package kanban

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// docs/kanban.md is the contract for human checkpoints. These tests fail when
// a reason or a rule changes without the pages following.
func TestCheckpointsAreDocumented(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/kanban.md")), " ")
	for _, want := range []string{
		"### Human checkpoints",
		"No worker claims a card that carries a checkpoint label",
		"belai kanban checkpoint ID approve",
		"never a checkpoint",
		"it never holds a card",
		"`" + CheckpointPrefix + "<reason>`",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/kanban.md lacks %q", want)
		}
	}
	for _, r := range CheckpointReasons {
		if !strings.Contains(doc, "`"+r+"`") {
			t.Errorf("docs/kanban.md does not name the %q checkpoint", r)
		}
	}
	profiles := docparity.Read(t, "docs/agent-profiles.md")
	if !strings.Contains(profiles, "checkpoint") || !strings.Contains(profiles, "kanban.md#human-checkpoints") {
		t.Error("docs/agent-profiles.md does not describe kanban.on_success.checkpoint")
	}
}
