package agentstore

import (
	"fmt"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestAgentStoresPageListsEveryRegisteredPattern keeps docs/agent-stores.md
// in step with the registry: each agent has a row, and every session, prompt
// and memory pattern the registry holds appears in that row, spelled exactly.
func TestAgentStoresPageListsEveryRegisteredPattern(t *testing.T) {
	doc := docparity.Read(t, "docs/agent-stores.md")
	rows := map[string]string{}
	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(line, "| `") {
			name, _, _ := strings.Cut(strings.TrimPrefix(line, "| `"), "`")
			rows[name] = line
		}
	}
	for _, a := range knownAgents {
		row, ok := rows[a.Name]
		if !ok {
			t.Errorf("docs/agent-stores.md has no row for the agent %q", a.Name)
			continue
		}
		for _, group := range [][]string{a.Sessions, a.Prompts, a.Memory} {
			for _, p := range group {
				if !strings.Contains(row, "`"+p+"`") {
					t.Errorf("the %q row does not list the pattern `%s`", a.Name, p)
				}
			}
		}
	}
	if len(rows) != len(knownAgents) {
		t.Errorf("the page has %d agent rows, the registry %d", len(rows), len(knownAgents))
	}
}

// TestAgentStoresPageStatesTheCaps pins the cap table to DefaultCaps.
func TestAgentStoresPageStatesTheCaps(t *testing.T) {
	doc := docparity.Read(t, "docs/agent-stores.md")
	c := DefaultCaps()
	for _, want := range []string{
		fmt.Sprintf("| Matches | %d |", c.MaxMatches),
		fmt.Sprintf("| Snippet | %d KiB |", c.MaxSnippet>>10),
		fmt.Sprintf("| Total payload | %d KiB |", c.MaxTotalBytes>>10),
		fmt.Sprintf("| Files scanned | %d |", c.MaxFiles),
		fmt.Sprintf("| Deadline | %d s |", int(c.Deadline.Seconds())),
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/agent-stores.md does not say %q", want)
		}
	}
}
