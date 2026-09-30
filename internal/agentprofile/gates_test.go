package agentprofile

import (
	"strings"
	"testing"
)

func TestGatesSpecValidation(t *testing.T) {
	ok := worker()
	ok.Kanban.HandoffLabels = []string{"build"}
	for _, mode := range []string{"", VerifyOff, VerifyRecord, VerifyEnforce} {
		ok.Kanban.Gates = &GatesSpec{Require: true, Verify: mode}
		if err := ok.Validate(); err != nil {
			t.Fatalf("verify %q: %v", mode, err)
		}
	}
	bad := worker()
	bad.Kanban.HandoffLabels = []string{"build"}
	bad.Kanban.Gates = &GatesSpec{Verify: "always"}
	if err := bad.Validate(); err == nil {
		t.Fatal("an unknown verify mode fails validation, so it can never mean off by accident")
	}
	noHandoff := worker()
	noHandoff.Kanban.Gates = &GatesSpec{Require: true}
	if err := noHandoff.Validate(); err == nil {
		t.Fatal("require needs a handoff to attach gates to")
	}
	verifyOnly := worker()
	verifyOnly.Kanban.Gates = &GatesSpec{Verify: VerifyEnforce}
	if err := verifyOnly.Validate(); err != nil {
		t.Fatalf("a worker that only verifies needs no handoff: %v", err)
	}
}

func TestVerifyModeDefaultsOff(t *testing.T) {
	var g *GatesSpec
	if g.VerifyMode() != VerifyOff || (&GatesSpec{}).VerifyMode() != VerifyOff {
		t.Fatal("no gates block, or no mode, means off")
	}
	if (&GatesSpec{Verify: VerifyEnforce}).VerifyMode() != VerifyEnforce {
		t.Fatal("enforce is kept")
	}
}

func TestBuiltinScoutRequiresGates(t *testing.T) {
	p, err := Load("belai:scout")
	if err != nil {
		t.Fatal(err)
	}
	if p.Kanban.Gates == nil || !p.Kanban.Gates.Require {
		t.Fatalf("the delivery scout must file every handoff with gates: %+v", p.Kanban.Gates)
	}
}

func TestGatesReviewNeedsEnforce(t *testing.T) {
	p := worker()
	p.Kanban.Gates = &GatesSpec{Review: true, Verify: VerifyEnforce}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"", VerifyOff, VerifyRecord} {
		p.Kanban.Gates = &GatesSpec{Review: true, Verify: mode}
		if err := p.Validate(); err == nil {
			t.Errorf("review with verify %q must fail: manual gates are only held to under enforce", mode)
		}
	}
}

func TestBuiltinReviewerDecidesManualGates(t *testing.T) {
	p, err := Load("belai:reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Kanban.Gates.Review {
		t.Fatal("the reviewer records manual gates")
	}
	for _, name := range []string{"belai:builder", "belai:scout"} {
		o, _ := Load(name)
		if o.Kanban.Gates != nil && o.Kanban.Gates.Review {
			t.Fatalf("%s must not decide manual gates", name)
		}
	}
}

func TestAutoHandoffListValidation(t *testing.T) {
	ok := worker()
	ok.Kanban.HandoffLabels = []string{"build"}
	ok.Kanban.Quality = &QualitySpec{Sweep: true, List: ListAuto}
	if err := ok.Validate(); err != nil {
		t.Fatalf("quality list auto: %v", err)
	}
	if !ok.Kanban.Quality.Auto() || ok.Kanban.Quality.HandoffList() != "review" {
		t.Fatal("auto is routed by clarity and falls back to review")
	}
	if (QualitySpec{List: "backlog"}).Auto() || (QualitySpec{}).Auto() {
		t.Fatal("only the word auto is auto")
	}
	ok.Kanban.Quality = nil
	ok.Kanban.Survey = &SurveySpec{Title: "t", List: ListAuto}
	if err := ok.Validate(); err != nil {
		t.Fatalf("survey list auto: %v", err)
	}
	if !ok.Kanban.Survey.Auto() || ok.Kanban.Survey.HandoffList() != "review" {
		t.Fatal("survey auto falls back to review")
	}
	bad := worker()
	bad.Kanban.HandoffLabels = []string{"build"}
	bad.Kanban.Quality = &QualitySpec{Sweep: true, List: "automatic"}
	if err := bad.Validate(); err == nil {
		t.Fatal("a near miss is refused, so a typo can never mean backlog")
	}
}

func TestBuiltinScoutRoutesHandoffsByClarity(t *testing.T) {
	p, err := Load("belai:scout")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Kanban.Quality.Auto() {
		t.Fatal("the delivery scout sends clear tasks to backlog and the rest to review")
	}
}

func TestGatesCoverageValidation(t *testing.T) {
	p := worker()
	p.Kanban.Gates = &GatesSpec{Coverage: true}
	if err := p.Validate(); err == nil {
		t.Fatal("coverage needs a handoff: a request is covered by the tasks handed on")
	}
	p.Kanban.HandoffLabels = []string{"build"}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestBuiltinScoutPlansRequestsForCoverage(t *testing.T) {
	p, err := Load("belai:scout")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Kanban.Gates.Coverage || !strings.Contains(p.SystemPrompt, "KanbanContract") || !strings.Contains(p.SystemPrompt, "covers") {
		t.Fatalf("the scout records clauses and covers them: %+v", p.Kanban.Gates)
	}
}

func TestGatesDraftIsAFlagOnTheBuilder(t *testing.T) {
	p := worker()
	p.Kanban.Gates = &GatesSpec{Draft: true, Verify: VerifyEnforce}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	b, err := Load("belai:builder")
	if err != nil {
		t.Fatal(err)
	}
	if !b.Kanban.Gates.Draft {
		t.Fatal("the builder drafts manual gates for a card that has none")
	}
	for _, name := range []string{"belai:reviewer", "belai:scout"} {
		o, _ := Load(name)
		if o.Kanban.Gates != nil && o.Kanban.Gates.Draft {
			t.Fatalf("%s must not draft gates", name)
		}
	}
}
