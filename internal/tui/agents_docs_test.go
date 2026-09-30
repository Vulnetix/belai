package tui

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// The hub's tab names are the words the docs use. A renamed or added tab must
// change docs/fleet.md too.
func TestAgentHubTabsAreDocumented(t *testing.T) {
	doc := docparity.Read(t, "docs/fleet.md")
	for _, name := range agentTabNames {
		if !strings.Contains(doc, "**"+name+"**") {
			t.Errorf("docs/fleet.md does not describe the %q tab", name)
		}
	}
	for _, want := range []string{"`/agents running`", "`/agents fleet`", "never a worker and cannot be assigned or paused"} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/fleet.md lacks %q", want)
		}
	}
}
