package docsuite

import (
	"fmt"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/tools"
)

// TestKanbanPageStatesTheLimits pins the limits docs/kanban.md lists to the
// constants the store enforces.
func TestKanbanPageStatesTheLimits(t *testing.T) {
	requirePhrases(t, "docs/kanban.md",
		fmt.Sprintf("a title is at most %d characters", kanban.MaxTitleRunes),
		fmt.Sprintf("details are at most %d KiB", kanban.MaxBodyBytes>>10),
		fmt.Sprintf("a note is at most %d KiB", kanban.MaxNoteBytes>>10),
		fmt.Sprintf("an item keeps its last %d history entries", kanban.MaxHistory),
		fmt.Sprintf("a card carries at most %d acceptance gates, each title at most %d characters", kanban.MaxGates, kanban.MaxGateTitleRunes),
		fmt.Sprintf("the board holds at most %d items", kanban.MaxItems),
		fmt.Sprintf("at most %s per item", word(tools.MaxHandoffsPerItem)),
		fmt.Sprintf("a chain of handoffs stops after %s hops", word(tools.MaxHops)),
	)
}

// TestBkanPageStatesTheFieldLimits pins the per-field limits docs/bkan.md lists.
func TestBkanPageStatesTheFieldLimits(t *testing.T) {
	requirePhrases(t, "docs/bkan.md",
		fmt.Sprintf("at most %d of at most %d runes", kanban.MaxLabels, kanban.MaxLabelRunes),
		fmt.Sprintf("acceptance gates, at most %d", kanban.MaxGates),
		fmt.Sprintf("at most %d, each with an id", kanban.MaxClauses),
		fmt.Sprintf("a one-line text of at most %d characters", kanban.MaxClauseRunes),
	)
}

// TestProfilePageStatesTheKanbanBounds pins the numbers docs/agent-profiles.md
// gives for a worker's kanban block, and checks the two bounds the code holds
// as literals by validating at the documented edge.
func TestProfilePageStatesTheKanbanBounds(t *testing.T) {
	requirePhrases(t, "docs/agent-profiles.md",
		fmt.Sprintf("Claim lease (1m–2h, default %dm)", int(agentprofile.DefaultLease/time.Minute)),
		fmt.Sprintf("default %ds)", int(agentprofile.DefaultPoll/time.Second)),
		fmt.Sprintf("Failed attempts before the item goes to `blocked` (default %d)", agentprofile.DefaultMaxAttempts),
		fmt.Sprintf("0 to %d (0 or 1: one turn)", agentprofile.MaxRounds),
		fmt.Sprintf("at least `1h`, default `%dh`", int(agentprofile.DefaultSurveyEvery/time.Hour)),
		fmt.Sprintf("default %d KiB, at most %d KiB", agentprofile.DefaultMemoryBytes>>10, agentprofile.MaxMemoryBytes>>10),
	)
	if kanban.MinLease != time.Minute || kanban.MaxLease != 2*time.Hour {
		t.Errorf("the page says a lease runs 1m to 2h; the code allows %s to %s", kanban.MinLease, kanban.MaxLease)
	}

	worker := func(mut func(k *agentprofile.KanbanSpec)) error {
		p := agentprofile.AgentProfile{
			Name: "t-builder", Description: "d", SystemPrompt: "sp", Mode: agentprofile.ModeWorker, Tools: []string{"Read"},
			Kanban: &agentprofile.KanbanSpec{Labels: []string{"build"}, OnSuccess: agentprofile.Route{List: "review"}},
		}
		mut(p.Kanban)
		return p.Validate()
	}
	for _, c := range []struct {
		name string
		mut  func(k *agentprofile.KanbanSpec)
		ok   bool
	}{
		{"poll 5s is the least", func(k *agentprofile.KanbanSpec) { k.Poll = "5s" }, true},
		{"poll under 5s", func(k *agentprofile.KanbanSpec) { k.Poll = "4s" }, false},
		{"lease 1m is the least", func(k *agentprofile.KanbanSpec) { k.Lease = "1m" }, true},
		{"lease under 1m", func(k *agentprofile.KanbanSpec) { k.Lease = "59s" }, false},
		{"lease 2h is the most", func(k *agentprofile.KanbanSpec) { k.Lease = "2h" }, true},
		{"lease over 2h", func(k *agentprofile.KanbanSpec) { k.Lease = "2h1m" }, false},
		{"survey every 1h is the least", func(k *agentprofile.KanbanSpec) {
			k.HandoffTo = []string{"belai:builder"}
			k.Survey = &agentprofile.SurveySpec{Title: "t", Every: "1h"}
		}, true},
		{"survey every under 1h", func(k *agentprofile.KanbanSpec) {
			k.HandoffTo = []string{"belai:builder"}
			k.Survey = &agentprofile.SurveySpec{Title: "t", Every: "59m"}
		}, false},
	} {
		err := worker(c.mut)
		if c.ok && err != nil {
			t.Errorf("%s: refused: %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
}
