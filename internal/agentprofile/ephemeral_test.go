package agentprofile

import (
	"strings"
	"testing"
)

func TestEphemeralWriterWorksInAWorktreeAndGoesToReview(t *testing.T) {
	p, err := Ephemeral(EphemeralOptions{Prompt: "Implement the item.", Tools: []string{"Read", "Edit", "Write"}, Labels: []string{"build"}})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Ephemeral || !strings.HasPrefix(p.Name, EphemeralPrefix) || p.Mode != ModeWorker || p.Autonomy != AutonomyAutonomous {
		t.Fatalf("profile %+v", p)
	}
	if p.IsolationMode() != IsolationWorktree || p.PublishMode() != PublishAgent {
		t.Fatalf("a writer works in a worktree and publishes: %s %s", p.IsolationMode(), p.PublishMode())
	}
	if got := p.Kanban.ClaimLists(); len(got) != 1 || got[0] != "backlog" || p.Kanban.OnSuccess.List != "review" || p.Kanban.Labels[0] != "build" {
		t.Fatalf("kanban %+v", p.Kanban)
	}
	if p.Budget == nil || p.Budget.MaxPassesPerItem == 0 || p.WallBudget() == 0 {
		t.Fatalf("an autonomous worker is bounded: %+v", p.Budget)
	}
	if p.Decides() {
		t.Fatal("a writer does not decide")
	}
}

func TestEphemeralReaderDecidesAndGoesToDone(t *testing.T) {
	p, err := Ephemeral(EphemeralOptions{Prompt: "Check the branch.", Tools: []string{"Read", "Grep"}, Lists: []string{"review"}, Labels: []string{"needs-review"}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Kanban.OnSuccess.List != "done" || !p.Decides() {
		t.Fatalf("a worker with no file tool decides and closes the card: %+v", p.Kanban.OnSuccess)
	}
	if p.IsolationMode() != IsolationNone || p.Workspace != nil {
		t.Fatalf("a worker that cannot write needs no workspace: %+v", p.Workspace)
	}
}

func TestEphemeralReadOnlyRunsChecksInAThrowawayWorktree(t *testing.T) {
	p, err := Ephemeral(EphemeralOptions{Prompt: "Run the suite.", Tools: []string{"Read", "Bash"}, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if !p.ReadOnlyWorkspace() || p.IsolationMode() != IsolationWorktree || p.PublishMode() != PublishNone || p.Kanban.OnSuccess.List != "done" {
		t.Fatalf("workspace %+v to %s", p.Workspace, p.Kanban.OnSuccess.List)
	}
}

func TestEphemeralHonoursTheOptionsItIsGiven(t *testing.T) {
	p, err := Ephemeral(EphemeralOptions{Prompt: "Do it.", Tools: []string{"Write"}, To: "done", Publish: PublishNone})
	if err != nil {
		t.Fatal(err)
	}
	if p.Kanban.OnSuccess.List != "done" || p.PublishMode() != PublishNone {
		t.Fatalf("to %s publish %s", p.Kanban.OnSuccess.List, p.PublishMode())
	}
}

// The name follows the prompt, so the same prompt is the same worker identity;
// and a prompt-only worker is refused for what a stored profile would be.
func TestEphemeralIsStableAndValidated(t *testing.T) {
	a, _ := Ephemeral(EphemeralOptions{Prompt: "  Do it.  "})
	b, _ := Ephemeral(EphemeralOptions{Prompt: "Do it."})
	c, _ := Ephemeral(EphemeralOptions{Prompt: "Do it differently."})
	if a.Name != b.Name || a.Name == c.Name {
		t.Fatalf("names %q %q %q", a.Name, b.Name, c.Name)
	}
	for name, o := range map[string]EphemeralOptions{
		"no prompt":    {},
		"blank prompt": {Prompt: " \n"},
		"unknown tool": {Prompt: "x", Tools: []string{"NoSuchTool"}},
		"bad list":     {Prompt: "x", To: "nowhere"},
	} {
		if _, err := Ephemeral(o); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if p, err := Ephemeral(EphemeralOptions{Prompt: "x"}); err != nil || !p.Writes() || p.IsolationMode() != IsolationWorktree {
		t.Fatalf("with no tool list the worker has the full surface, so it works in a worktree: %v %+v", err, p.Workspace)
	}
}
