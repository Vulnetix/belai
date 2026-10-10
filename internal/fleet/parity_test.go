package fleet

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/kanban"
)

// boardOutcome runs one worker over one fresh card and returns what the board
// shows afterwards: where the card is, how it got there, and what it carries.
func boardOutcome(t *testing.T, p agentprofile.AgentProfile) (list kanban.List, labels []string, route []string, attempts, bounces int) {
	t.Helper()
	store, reg := testEnv(t)
	it, _, _ := store.Add(kanban.ItemInput{Title: "do it", Labels: []string{"build"}}, kanban.Provenance{})
	if err := newWorker(t, store, reg, p, complete).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(it.ID)
	for _, h := range got.History {
		route = append(route, string(h.From)+">"+string(h.To))
	}
	return got.List, got.Labels, route, got.Attempts, got.Bounces
}

// A profile's prose is persona. Take it away entirely and the card goes through
// the same stages, with the same labels and the same counts, and the model is
// told the same about how the run works.
func TestTheBoardOutcomeDoesNotDependOnTheProfileProse(t *testing.T) {
	withProse := builderProfile()
	withProse.SystemPrompt = "You are a careful builder. Call KanbanOutcome when done, publish with PublishBranch."
	withProse.Identity = "Odo, a builder"
	withProse.Description = "builds things"
	bare := builderProfile()
	bare.SystemPrompt, bare.Identity, bare.Description = "x", "", "x"

	l1, lab1, r1, a1, b1 := boardOutcome(t, withProse)
	l2, lab2, r2, a2, b2 := boardOutcome(t, bare)
	if l1 != l2 || !slices.Equal(lab1, lab2) || !slices.Equal(r1, r2) || a1 != a2 || b1 != b2 {
		t.Fatalf("with prose: %s %v %v %d %d\nwithout:    %s %v %v %d %d", l1, lab1, r1, a1, b1, l2, lab2, r2, a2, b2)
	}
	if l1 != kanban.Review {
		t.Fatalf("the card went to %s", l1)
	}

	ws := &Workspace{Worktree: true, Branch: "belai/K-aaaaaa/a1", Base: "0123456789abcdef"}
	for _, p := range []agentprofile.AgentProfile{withProse, bare} {
		p.Workspace = &agentprofile.WorkspaceSpec{Isolation: agentprofile.IsolationWorktree, Publish: agentprofile.PublishAgent}
		p.Kanban.HandoffTo = []string{"x"}
		w := &Worker{Profile: p}
		if a, b := w.directive(ws, true, true), (&Worker{Profile: bareOf(p)}).directive(ws, true, true); a != b {
			t.Fatalf("the operating contract depends on the prose:\n%s\n---\n%s", a, b)
		}
	}
}

func bareOf(p agentprofile.AgentProfile) agentprofile.AgentProfile {
	p.SystemPrompt, p.Identity, p.Description = "other words entirely", "", "other"
	return p
}

// A worker made from a prompt is routed as the same declarations would route a
// stored profile: nothing about the run depends on there being a profile on disk.
func TestAPromptOnlyWorkerIsRoutedLikeAStoredProfileWithTheSameDeclarations(t *testing.T) {
	adhoc, err := agentprofile.Ephemeral(agentprofile.EphemeralOptions{Prompt: "Implement the item.", Tools: []string{"Read"}, Labels: []string{"build"}, To: "review"})
	if err != nil {
		t.Fatal(err)
	}
	// The card is filed with no project, so claim across projects as the other fixtures do.
	adhoc.Kanban.Project = "all"
	stored := adhoc
	stored.Name, stored.Ephemeral = "t-stored", false
	stored.SystemPrompt = "A different prompt, stored on disk."

	l1, lab1, r1, a1, b1 := boardOutcome(t, adhoc)
	l2, lab2, r2, a2, b2 := boardOutcome(t, stored)
	if l1 != l2 || !slices.Equal(lab1, lab2) || !slices.Equal(r1, r2) || a1 != a2 || b1 != b2 {
		t.Fatalf("prompt-only: %s %v %v %d %d\nstored:      %s %v %v %d %d", l1, lab1, r1, a1, b1, l2, lab2, r2, a2, b2)
	}
	if l1 != kanban.Review || !strings.Contains(strings.Join(r1, " "), "in_progress>review") {
		t.Fatalf("route %v ended in %s", r1, l1)
	}
}
