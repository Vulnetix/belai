package agent

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/repomap"
	"github.com/vulnetix/belai/internal/rolemanager/jev"
	"github.com/vulnetix/belai/internal/run"
)

// scaleDecider answers the request-scale items with fixed scores.
type scaleDecider struct{ simple, staged float64 }

func (d *scaleDecider) Decide(_ context.Context, r decisions.Request) (decisions.Result, error) {
	out := map[string]decisions.Answer{}
	for id := range r.Questions {
		v := d.staged
		if id == "s:simple" {
			v = d.simple
		}
		out[id] = decisions.Answer{Type: decisions.TypeNoul, Noul: v}
	}
	return decisions.Result{Answers: out}, nil
}
func (d *scaleDecider) Identity() string           { return "fake/systemone" }
func (d *scaleDecider) Backend() decisions.Backend { return decisions.BackendSystemOne }

func scaleSession(t *testing.T, dec decisions.Decider) *Session {
	t.Helper()
	m := repomap.Map{Commands: repomap.Commands{Test: []string{"just check"}}}
	s, err := NewSession(Options{Cfg: run.Config{Provider: "openai", Model: "test"}, Client: http.DefaultClient, RepoMap: &m})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if dec != nil {
		s.jev = &jev.Jobs{Client: jev.NewWith(dec)}
	}
	return s
}

func TestRateScaleSimpleOnlyOnAClearVerdict(t *testing.T) {
	ctx := context.Background()
	if !scaleSession(t, &scaleDecider{simple: 0.95, staged: 0.05}).rateScale(ctx, "commit and push") {
		t.Fatal("a clear simple verdict must be simple")
	}
	if scaleSession(t, &scaleDecider{simple: 0.2, staged: 0.9}).rateScale(ctx, "redesign the auth layer") {
		t.Fatal("a staged request must not be simple")
	}
	if scaleSession(t, &scaleDecider{simple: 0.7, staged: 0.1}).rateScale(ctx, "x") {
		t.Fatal("a score under simple_at must not be simple")
	}
	if scaleSession(t, nil).rateScale(ctx, "commit and push") {
		t.Fatal("no backend means the ordinary behaviour")
	}
	if scaleSession(t, &scaleDecider{simple: 0.99, staged: 0}).rateScale(ctx, "") {
		t.Fatal("an empty request is never simple")
	}
}

func TestSimpleTurnDropsTheVerificationCeremony(t *testing.T) {
	s := scaleSession(t, nil)
	if got := s.goalAckDirective(); !strings.Contains(got, "just check") || !strings.Contains(got, "only runs commands") {
		t.Fatalf("the ordinary directive must name the surface as conditional:\n%s", got)
	}
	s.turnSimple = true
	got := s.goalAckDirective()
	if got != goalSimpleDirective || strings.Contains(got, "just check") || strings.Contains(got, "update_plan") {
		t.Fatalf("a simple turn gets the lean directive alone:\n%s", got)
	}
	s.turnExecutePlan = true
	if s.goalAckDirective() == goalSimpleDirective {
		t.Fatal("an approved plan is never a simple turn")
	}
}

func TestSimpleLedgerHasNoWriteStall(t *testing.T) {
	l := passLedger{goalText: "commit", simple: true}
	for i := 0; i < 5; i++ {
		l.noteWrites(passOutcome{})
	}
	if l.stalledOnWrites() {
		t.Fatal("a simple request may change no file, so it never stalls on writes")
	}
	l = passLedger{goalText: "edit"}
	for i := 0; i < goalNoWritePasses; i++ {
		l.noteWrites(passOutcome{})
	}
	if !l.stalledOnWrites() {
		t.Fatal("an ordinary goal must still stall on writes")
	}
}
