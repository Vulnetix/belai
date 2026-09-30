package kanban

import (
	"fmt"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

func TestRequestCoverageIsDocumented(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/fleet.md")), " ")
	for _, want := range []string{
		"#### Request coverage",
		fmt.Sprintf("at most %d, each one line of at most %d characters", MaxClauses, MaxClauseRunes),
		"numbered `C1`, `C2`",
		"A handoff before the clauses are recorded",
		"A handoff that covers no clause, or an unknown one",
		"Recording clauses after a handoff exists",
		"A request the scout completed with no clauses recorded",
		"A clause no handoff covers when the scout finishes",
		"It names ids only, never the clause text",
		"never reopened after it is done, never doubled by a retry or a relaunch, and never recreated after a person deletes it",
		"A deleted handoff covers nothing",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/fleet.md lacks %q", want)
		}
	}
	bkan := docparity.Read(t, "docs/bkan.md")
	for _, want := range []string{"| `Clauses` |", "| `Covers` |", "00 04"} {
		if !strings.Contains(bkan, want) {
			t.Errorf("docs/bkan.md lacks %q", want)
		}
	}
	if formatVersion != 4 {
		t.Errorf("the board is version %d; docs/bkan.md describes version 4", formatVersion)
	}
}
