package agentprofile

import (
	"slices"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/tools"
)

// belai:conflict-resolver claims merge-conflict cards, runs the harness's
// loop with five checkouts preferring both sides, and hands its merge to the
// reviewer as a builder would.
func TestConflictResolverProfile(t *testing.T) {
	p, err := Load("belai:conflict-resolver")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	k := p.Kanban
	if p.Mode != ModeWorker || p.Autonomy != AutonomyAutonomous || p.Budget == nil || p.Budget.MaxPassesPerItem < 1 || p.Budget.MaxCostUSDPerItem <= 0 {
		t.Fatalf("an autonomous worker with a budget: %+v %+v", p.Autonomy, p.Budget)
	}
	if !slices.Equal(k.Labels, []string{MergeConflictLabel}) || !slices.Equal(k.Lists, []string{"backlog"}) {
		t.Fatalf("claims %v %v", k.Lists, k.Labels)
	}
	if k.OnSuccess.List != "review" || !slices.Equal(k.OnSuccess.Labels, []string{"needs-review"}) || !slices.Equal(k.OnSuccess.DropLabels, []string{MergeConflictLabel}) {
		t.Fatalf("on_success %+v", k.OnSuccess)
	}
	if k.Conflicts == nil || k.Conflicts.MaxCheckoutsOr() != 5 || k.Conflicts.PreferOr() != "both" {
		t.Fatalf("conflicts %+v", k.Conflicts)
	}
	if p.IsolationMode() != IsolationWorktree || p.PublishMode() != PublishNone {
		t.Fatalf("a worktree, and no publishing of its own: the harness pushes")
	}
	if p.Decides() {
		t.Fatal("the resolver edits files")
	}
	banned := []string{
		tools.KanbanSearchName, tools.KanbanUpdateName, tools.KanbanMoveName, tools.KanbanAddName,
		tools.KanbanHandoffName, tools.KanbanVerdictName, tools.KanbanGateName, tools.KanbanContractName,
		tools.PublishBranchName, tools.CloseDuplicatePRName,
		".vulnetix/", "the harness", "kb+", "workspace note", "described below",
	}
	text := p.Identity + "\n" + p.SystemPrompt
	for _, b := range banned {
		if strings.Contains(text, b) {
			t.Errorf("the prose names %q", b)
		}
	}
	for _, want := range []string{"both sides", "different variation", "Never delete", "outside the conflicted ones"} {
		if !strings.Contains(p.SystemPrompt, want) {
			t.Errorf("the prompt does not say %q", want)
		}
	}
	for _, dash := range []string{"—", "–"} {
		if strings.Contains(text+p.Description, dash) {
			t.Error("the prose has a dash the prose rules forbid")
		}
	}
}

func TestConflictsSpecValidation(t *testing.T) {
	base := func() AgentProfile {
		p, _ := Load("belai:conflict-resolver")
		c := *p.Kanban.Conflicts
		k := *p.Kanban
		k.Conflicts = &c
		p.Kanban = &k
		w := *p.Workspace
		p.Workspace = &w
		return p
	}
	for name, mutate := range map[string]func(p *AgentProfile){
		"too many checkouts": func(p *AgentProfile) { p.Kanban.Conflicts.MaxCheckouts = MaxCheckouts + 1 },
		"negative":           func(p *AgentProfile) { p.Kanban.Conflicts.MaxCheckouts = -1 },
		"unknown preference": func(p *AgentProfile) { p.Kanban.Conflicts.Prefer = "mixed" },
		"shared workspace":   func(p *AgentProfile) { p.Workspace.Isolation = IsolationShared },
		"no file tools":      func(p *AgentProfile) { p.Tools = []string{"Read", "Bash"} },
	} {
		p := base()
		mutate(&p)
		if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "kanban.conflicts") {
			t.Errorf("%s: %v", name, err)
		}
	}
	p := base()
	p.Kanban.Conflicts.MaxCheckouts = 0
	if err := p.Validate(); err != nil || p.Kanban.Conflicts.MaxCheckoutsOr() != DefaultMaxCheckouts {
		t.Fatalf("the default: %v", err)
	}
}
