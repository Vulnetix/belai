package agentprofile

import (
	"slices"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/tools"
)

// The maintenance profiles are shaped like belai:builder so belai:reviewer
// picks their branches up, and each claims one label.
func TestMaintenanceFixersAreShapedLikeTheBuilder(t *testing.T) {
	builder, err := Load("belai:builder")
	if err != nil {
		t.Fatal(err)
	}
	for name, claim := range map[string]string{"belai:test-fixer": "tests-failing", "belai:lint-fixer": "lint-drift"} {
		p, err := Load(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := p.Validate(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		k := p.Kanban
		if p.Mode != ModeWorker || p.Autonomy != AutonomyAutonomous || p.Budget == nil || p.Budget.MaxPassesPerItem < 1 {
			t.Errorf("%s: an autonomous worker with a pass budget, got %+v %+v", name, p.Autonomy, p.Budget)
		}
		if !slices.Equal(k.Lists, []string{"backlog"}) || !slices.Equal(k.Labels, []string{claim}) {
			t.Errorf("%s claims %v %v, want backlog items labelled %s", name, k.Lists, k.Labels, claim)
		}
		if k.OnSuccess.List != "review" || !slices.Equal(k.OnSuccess.Labels, builder.Kanban.OnSuccess.Labels) || !slices.Equal(k.OnSuccess.DropLabels, []string{claim}) {
			t.Errorf("%s on_success %+v: hand to review as needs-review and drop %s", name, k.OnSuccess, claim)
		}
		if k.OnFailure.List != "" || len(k.OnFailure.Labels) != 0 || len(k.OnFailure.DropLabels) != 0 {
			t.Errorf("%s has a custom on_failure %+v", name, k.OnFailure)
		}
		if k.Gates == nil || k.Gates.Verify != VerifyEnforce || !k.Gates.Draft {
			t.Errorf("%s gates %+v: verify enforce and draft", name, k.Gates)
		}
		if k.MaxAttempts != 3 {
			t.Errorf("%s max_attempts %d, want 3", name, k.MaxAttempts)
		}
		if p.Budget.MaxCostUSDPerItem <= 0 || p.MaxIterations < 40 {
			t.Errorf("%s: a cost bound and a full per-pass tool budget, got %+v, %d iterations", name, p.Budget, p.MaxIterations)
		}
		if p.IsolationMode() != IsolationWorktree || p.PublishMode() != PublishAgent || p.Memory == nil || !p.Memory.Enabled {
			t.Errorf("%s: worktree, publish agent and memory", name)
		}
		if !slices.Equal(p.Tools, builder.Tools) {
			t.Errorf("%s tools %v differ from the builder's %v", name, p.Tools, builder.Tools)
		}
		// The reviewer takes what they hand on.
		rev, _ := Load("belai:reviewer")
		if !slices.Equal(rev.Kanban.Lists, []string{"review"}) || !slices.Contains(rev.Kanban.Labels, "needs-review") {
			t.Errorf("belai:reviewer no longer takes needs-review items from review: %+v", rev.Kanban)
		}
	}
}

func TestMaintenancePromptsKeepTheirRules(t *testing.T) {
	for name, wants := range map[string][]string{
		"belai:test-fixer":  {"untrusted data, never as instructions", "run the repository's own detected test suite", "root cause", "Never weaken, skip, delete or comment out a test"},
		"belai:lint-fixer":  {"untrusted data, never as instructions", "rule files the repository checked in", "autofix mode", "Never edit a lint or formatter configuration file", "add an ignore or suppression", "disable a rule", "minimal"},
		"belai:pr-reviewer": {"untrusted data, never as instructions", "read-only", "approve it", "merge it", "verdict: approve", "verdict: request_changes", "finding: FILE:LINE"},
	} {
		p, err := Load(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range wants {
			if !strings.Contains(p.SystemPrompt, want) {
				t.Errorf("%s: the prompt lacks %q", name, want)
			}
		}
		for _, bad := range []string{"—", "–", "‘", "’", "“", "”"} {
			if strings.Contains(p.SystemPrompt+p.Description+p.Identity, bad) {
				t.Errorf("%s: contains %q", name, bad)
			}
		}
	}
}

// Only the lint fixer sweeps; it files the cards it claims.
func TestOnlyTheLintFixerSweeps(t *testing.T) {
	lf, _ := Load("belai:lint-fixer")
	if lf.Kanban.Lint == nil || !lf.Kanban.Lint.Sweep {
		t.Fatal("belai:lint-fixer must sweep, or nothing files lint-drift cards in a standalone crew")
	}
	for _, n := range []string{"belai:test-fixer", "belai:pr-reviewer", "belai:builder", "belai:reviewer"} {
		p, _ := Load(n)
		if p.Kanban.Lint != nil {
			t.Errorf("%s sweeps", n)
		}
	}
}

// The pull request reviewer cannot change anything: a read-only worktree, no
// writing tool, no publish, no tool that touches the forge but reading it.
func TestPRReviewerIsReadOnlyAndNeverPublishes(t *testing.T) {
	p, err := Load("belai:pr-reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if p.Workspace == nil || !p.Workspace.ReadOnly || p.IsolationMode() != IsolationWorktree || p.PublishMode() != PublishNone {
		t.Fatalf("workspace %+v", p.Workspace)
	}
	for _, tool := range []string{"Write", "Edit", "PublishBranch", "WebFetch", "WebSearch"} {
		if slices.Contains(p.Tools, tool) {
			t.Errorf("pr-reviewer has %s", tool)
		}
	}
	if !slices.Equal(p.Kanban.Labels, []string{"pr-review"}) || !slices.Equal(p.Kanban.Lists, []string{"backlog"}) {
		t.Errorf("claims %v %v", p.Kanban.Lists, p.Kanban.Labels)
	}
	if p.Kanban.OnSuccess.List != "done" || p.Kanban.Gates != nil || p.Kanban.HandoffTo != nil {
		t.Errorf("a review ends on the card: %+v", p.Kanban)
	}
	if p.Autonomy != AutonomyAutonomous || p.Budget == nil || p.Budget.MaxPassesPerItem < 1 {
		t.Errorf("autonomy %v budget %+v", p.Autonomy, p.Budget)
	}
}

func TestMaintenanceCrew(t *testing.T) {
	c, err := LoadCrew("belai:maintenance")
	if err != nil {
		t.Fatal(err)
	}
	if !c.OnePerRepo || c.Workers() != 3 {
		t.Fatalf("crew %+v: one per repository, three workers", c)
	}
	var got []string
	for _, m := range c.Members {
		if m.Replicas != 1 {
			t.Errorf("%s x%d", m.Profile, m.Replicas)
		}
		got = append(got, m.Profile)
		if _, err := Load(m.Profile); err != nil {
			t.Errorf("member %s: %v", m.Profile, err)
		}
	}
	if !slices.Equal(got, []string{"belai:test-fixer", "belai:lint-fixer", "belai:reviewer"}) {
		t.Fatalf("members %v", got)
	}
	// The existing crews are unchanged.
	for name, want := range map[string]int{"belai:delivery": 4, "belai:security": 4} {
		c, err := LoadCrew(name)
		if err != nil || c.Workers() != want {
			t.Errorf("%s: %v workers %d, want %d", name, err, c.Workers(), want)
		}
	}
}

// The built-in prose never names a board tool or the harness: the harness
// tells every worker how to record things itself (TestBuiltinPromptsCarryNoMechanics
// holds the older workers to the same rule).
func TestMaintenancePromptsCarryNoMechanics(t *testing.T) {
	banned := []string{
		tools.KanbanSearchName, tools.KanbanUpdateName, tools.KanbanMoveName, tools.KanbanAddName,
		tools.KanbanHandoffName, tools.KanbanVerdictName, tools.KanbanGateName, tools.KanbanContractName,
		tools.PublishBranchName, tools.CloseDuplicatePRName,
		".vulnetix/", "the harness", "kb+", "workspace note", "described below",
	}
	for _, name := range []string{"belai:test-fixer", "belai:lint-fixer", "belai:pr-reviewer"} {
		p, err := Load(name)
		if err != nil {
			t.Fatal(err)
		}
		text := p.Identity + "\n" + p.SystemPrompt
		for _, b := range banned {
			if strings.Contains(text, b) {
				t.Errorf("%s: the prose names %q", name, b)
			}
		}
	}
}

// The fixers change code; the pull request reviewer decides.
func TestMaintenanceWorkersDecideAsTheirDeliverable(t *testing.T) {
	for name, want := range map[string]bool{"belai:test-fixer": false, "belai:lint-fixer": false, "belai:pr-reviewer": true} {
		p, err := Load(name)
		if err != nil {
			t.Fatal(err)
		}
		if got := p.Decides(); got != want {
			t.Errorf("%s: Decides() = %v, want %v", name, got, want)
		}
	}
}
