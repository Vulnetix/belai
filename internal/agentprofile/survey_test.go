package agentprofile

import (
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/kanban"
)

func surveyor() AgentProfile {
	p := worker()
	p.Kanban.HandoffLabels = []string{"build"}
	p.Kanban.Survey = &SurveySpec{Title: "Survey {project} for work"}
	return p
}

func TestSurveyValidation(t *testing.T) {
	if err := surveyor().Validate(); err != nil {
		t.Fatalf("minimal survey: %v", err)
	}
	for name, c := range map[string]struct {
		mutate func(*AgentProfile)
		want   string
	}{
		"no title":         {func(p *AgentProfile) { p.Kanban.Survey.Title = "  " }, "survey.title"},
		"bad list":         {func(p *AgentProfile) { p.Kanban.Survey.List = "done" }, "survey.list"},
		"in_progress list": {func(p *AgentProfile) { p.Kanban.Survey.List = "in_progress" }, "survey.list"},
		"short every":      {func(p *AgentProfile) { p.Kanban.Survey.Every = "30m" }, "survey.every"},
		"bad every":        {func(p *AgentProfile) { p.Kanban.Survey.Every = "daily" }, "survey.every"},
		"no handoffs":      {func(p *AgentProfile) { p.Kanban.HandoffLabels = nil }, "handoff"},
	} {
		p := surveyor()
		c.mutate(&p)
		if err := p.Validate(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want one naming %q", name, err, c.want)
		}
	}
	for _, list := range []string{"review", "backlog"} {
		p := surveyor()
		p.Kanban.Survey.List = list
		if err := p.Validate(); err != nil {
			t.Errorf("list %s: %v", list, err)
		}
	}
}

func TestSurveyDefaults(t *testing.T) {
	s := SurveySpec{Title: "t"}
	if s.HandoffList() != kanban.Review || s.EveryOr() != DefaultSurveyEvery || DefaultSurveyEvery != 24*time.Hour {
		t.Fatalf("defaults: list %s every %s", s.HandoffList(), s.EveryOr())
	}
	s = SurveySpec{Title: "t", List: "backlog", Every: "6h"}
	if s.HandoffList() != kanban.Backlog || s.EveryOr() != 6*time.Hour {
		t.Fatalf("set: list %s every %s", s.HandoffList(), s.EveryOr())
	}
}

func TestReadOnlyWorkspaceValidation(t *testing.T) {
	ro := func() AgentProfile {
		p := worker()
		p.Tools = []string{"Read", "Bash"}
		p.Workspace = &WorkspaceSpec{Isolation: IsolationWorktree, ReadOnly: true}
		return p
	}
	if p := ro(); p.Validate() != nil || !p.ReadOnlyWorkspace() {
		t.Fatalf("read-only worktree: %v", p.Validate())
	}
	for name, mutate := range map[string]func(*AgentProfile){
		"shared":  func(p *AgentProfile) { p.Workspace.Isolation = IsolationShared },
		"keep":    func(p *AgentProfile) { p.Workspace.Keep = true },
		"publish": func(p *AgentProfile) { p.Workspace.Publish = PublishDraftPR },
	} {
		p := ro()
		mutate(&p)
		if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "read_only") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if worker().ReadOnlyWorkspace() {
		t.Fatal("no workspace block is not read-only")
	}
}

// The built-in scout surveys on its own into review, and runs checks in a
// read-only worktree: Bash, but nothing it leaves is committed.
func TestBuiltinScoutSurveys(t *testing.T) {
	p, err := Load("belai:scout")
	if err != nil {
		t.Fatal(err)
	}
	s := p.Kanban.Survey
	if s == nil || s.HandoffList() != kanban.Review || s.EveryOr() != 24*time.Hour || !strings.Contains(s.Title, "{project}") {
		t.Fatalf("survey %+v", s)
	}
	for _, want := range []string{"tests", "docs", "site", "PRD"} {
		if !strings.Contains(s.Body, want) {
			t.Errorf("survey body does not mention %q", want)
		}
	}
	if !p.HasTool("Bash") || !p.ReadOnlyWorkspace() || p.IsolationMode() != IsolationWorktree || p.PublishMode() != PublishNone {
		t.Fatalf("scout workspace %+v tools %v", p.Workspace, p.Tools)
	}
}

func TestSecuritySpecValidation(t *testing.T) {
	ok := func(mutate func(*AgentProfile)) AgentProfile {
		p := worker()
		p.Tools = []string{"Read", "Vulnetix"}
		mutate(&p)
		return p
	}
	if err := ok(func(p *AgentProfile) { p.Kanban.Security = &SecuritySpec{Sweep: true, Reconcile: true} }).Validate(); err != nil {
		t.Fatalf("sweep and reconcile: %v", err)
	}
	if err := ok(func(p *AgentProfile) {
		p.Kanban.Security = &SecuritySpec{Verdicts: []string{"fixed", "false_positive", "no_fix", "needs_human", "rejected"}, VEX: true}
	}).Validate(); err != nil {
		t.Fatalf("verifier block: %v", err)
	}
	for name, c := range map[string]struct {
		mutate func(*AgentProfile)
		want   string
	}{
		"sweep without the tool": {func(p *AgentProfile) {
			p.Tools = []string{"Read"}
			p.Kanban.Security = &SecuritySpec{Sweep: true}
		}, "Vulnetix tool"},
		"vex without verdicts": {func(p *AgentProfile) { p.Kanban.Security = &SecuritySpec{VEX: true} }, "needs verdicts"},
		"unknown verdict":      {func(p *AgentProfile) { p.Kanban.Security = &SecuritySpec{Verdicts: []string{"wontfix"}} }, "not a verdict"},
		"reject without vex":   {func(p *AgentProfile) { p.Kanban.Security = &SecuritySpec{Verdicts: []string{"rejected"}} }, "only a worker that writes the VEX"},
		"duplicate verdict":    {func(p *AgentProfile) { p.Kanban.Security = &SecuritySpec{Verdicts: []string{"fixed", "fixed"}} }, "twice"},
	} {
		p := ok(c.mutate)
		if err := p.Validate(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want one naming %q", name, err, c.want)
		}
	}
}
