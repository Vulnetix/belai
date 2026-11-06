package agentprofile

import (
	"strings"
	"testing"
)

// kanban.candidates asks for best-of-N attempts at a card. The harness picks the
// winner from the gates it runs on every candidate branch, so a profile that
// asks for candidates must be able to have them run and judged, and must not
// carry anything that would make one candidate's result a fact shared by all.
func candidateBase(t *testing.T) AgentProfile {
	t.Helper()
	p, err := Load("belai:builder")
	if err != nil {
		t.Fatal(err)
	}
	k := *p.Kanban
	g := *k.Gates
	k.Gates = &g
	k.Candidates = 3
	p.Kanban = &k
	w := *p.Workspace
	p.Workspace = &w
	return p
}

func TestCandidatesValidProfile(t *testing.T) {
	p := candidateBase(t)
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := p.Kanban.CandidateCount(); got != 3 {
		t.Fatalf("CandidateCount %d", got)
	}
	for _, v := range []string{VerifyRecord, VerifyEnforce} {
		p.Kanban.Gates.Verify = v
		if err := p.Validate(); err != nil {
			t.Errorf("verify %s: %v", v, err)
		}
	}
}

func TestCandidateCountDefaultsAndClamps(t *testing.T) {
	var nilSpec *KanbanSpec
	if nilSpec.CandidateCount() != 1 {
		t.Fatal("a nil spec means one attempt")
	}
	for in, want := range map[int]int{0: 1, 1: 1, 2: 2, MaxCandidates: MaxCandidates, MaxCandidates + 5: MaxCandidates, -3: 1} {
		if got := (&KanbanSpec{Candidates: in}).CandidateCount(); got != want {
			t.Errorf("candidates %d gave %d, want %d", in, got, want)
		}
	}
}

func TestCandidatesValidationRefusesWhatWouldBeShared(t *testing.T) {
	for name, mutate := range map[string]func(p *AgentProfile){
		"too many":              func(p *AgentProfile) { p.Kanban.Candidates = MaxCandidates + 1 },
		"negative":              func(p *AgentProfile) { p.Kanban.Candidates = -1 },
		"shared workspace":      func(p *AgentProfile) { p.Workspace.Isolation = IsolationShared },
		"no isolation":          func(p *AgentProfile) { p.Workspace.Isolation = IsolationNone },
		"read only workspace":   func(p *AgentProfile) { p.Workspace.ReadOnly = true },
		"gates off":             func(p *AgentProfile) { p.Kanban.Gates.Verify = VerifyOff },
		"no gates":              func(p *AgentProfile) { p.Kanban.Gates = nil },
		"handoffs":              func(p *AgentProfile) { p.Kanban.HandoffLabels = []string{"build"} },
		"handoff targets":       func(p *AgentProfile) { p.Kanban.HandoffTo = []string{"belai:reviewer"} },
		"a survey":              func(p *AgentProfile) { p.Kanban.Survey = &SurveySpec{Title: "t"} },
		"a conflict loop":       func(p *AgentProfile) { p.Kanban.Conflicts = &ConflictsSpec{} },
		"verdicts":              func(p *AgentProfile) { p.Kanban.Security = &SecuritySpec{Verdicts: []string{"fixed"}} },
		"a reviewer's gates":    func(p *AgentProfile) { p.Kanban.Gates.Review = true },
		"coverage of a request": func(p *AgentProfile) { p.Kanban.Gates.Coverage = true; p.Kanban.HandoffLabels = []string{"build"} },
	} {
		p := candidateBase(t)
		mutate(&p)
		if err := p.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// The refusals that are this key's own say so.
	for name, mutate := range map[string]func(p *AgentProfile){
		"gates off":         func(p *AgentProfile) { p.Kanban.Gates.Verify = VerifyOff },
		"verdicts":          func(p *AgentProfile) { p.Kanban.Security = &SecuritySpec{Verdicts: []string{"fixed"}} },
		"a reviewer's gate": func(p *AgentProfile) { p.Kanban.Gates.Review = true },
	} {
		p := candidateBase(t)
		mutate(&p)
		if err := p.Validate(); err == nil || (!strings.Contains(err.Error(), "kanban.candidates") && !strings.Contains(err.Error(), "kanban.gates.review")) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// One candidate is the ordinary path and needs none of the above.
func TestOneCandidateNeedsNothingExtra(t *testing.T) {
	for _, n := range []int{0, 1} {
		p, err := Load("belai:reviewer")
		if err != nil {
			t.Fatal(err)
		}
		k := *p.Kanban
		k.Candidates = n
		p.Kanban = &k
		if err := p.Validate(); err != nil {
			t.Errorf("candidates %d: %v", n, err)
		}
	}
}
