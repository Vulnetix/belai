package agent

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/docparity"
)

// TestSessionAssessmentStatesTheLimits pins the numbers docs/session-assessment.md
// gives for the goal draft ceiling, the default agent count and the parallel
// read-only tool bound.
func TestSessionAssessmentStatesTheLimits(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/session-assessment.md")), " ")
	for _, want := range []string{
		"the draft itself is capped at 2 minutes",
		"`DefaultMaxAgents = 15`",
		"held between 4 and 16 (`toolConcurrency`)",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/session-assessment.md does not say %q", want)
		}
	}
	if goalDraftCeiling.Minutes() != 2 {
		t.Errorf("goalDraftCeiling = %v, the page says 2 minutes", goalDraftCeiling)
	}
	if config.DefaultMaxAgents != 15 {
		t.Errorf("DefaultMaxAgents = %d, the page says 15", config.DefaultMaxAgents)
	}
	if minToolConcurrency != 4 || maxToolConcurrency != 16 {
		t.Errorf("tool concurrency bounds %d..%d, the page says 4 to 16", minToolConcurrency, maxToolConcurrency)
	}
}

// TestToolConcurrencyIsClampedToTheDocumentedBounds checks the clamp itself: a
// small max_agents is raised to 4, a large one held at 16, and the default 15
// passes through.
func TestToolConcurrencyIsClampedToTheDocumentedBounds(t *testing.T) {
	for _, c := range []struct{ agents, want int }{{0, 15}, {1, 4}, {3, 4}, {4, 4}, {10, 10}, {16, 16}, {40, 16}} {
		s := &Session{}
		if c.agents != 0 {
			s.settings.Resilience = &config.ResilienceSettings{MaxAgents: c.agents}
		}
		if got := s.toolConcurrency(); got != c.want {
			t.Errorf("max_agents %d gave concurrency %d, want %d", c.agents, got, c.want)
		}
	}
}
