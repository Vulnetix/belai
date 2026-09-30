package fleet

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/quality"
	"github.com/vulnetix/belai/internal/rolemanager"
)

type fastRole struct {
	mu       sync.Mutex
	reply    string
	err      error
	payloads []rolemanager.ClassifierPayload
}

func (f *fastRole) Classify(_ context.Context, p rolemanager.ClassifierPayload) (string, error) {
	f.mu.Lock()
	f.payloads = append(f.payloads, p)
	f.mu.Unlock()
	return f.reply, f.err
}

func (f *fastRole) calls() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.payloads) }

func drafter(draft bool) agentprofile.AgentProfile {
	p := gatedBuilder(agentprofile.VerifyEnforce)
	p.Kanban.Gates.Draft = draft
	return p
}

func claimOnce(t *testing.T, p agentprofile.AgentProfile, in kanban.ItemInput, fast rolemanager.Classifier) (kanban.Item, *Worker) {
	t.Helper()
	store, reg := testEnv(t)
	it, _, err := store.Add(in, kanban.Provenance{})
	if err != nil {
		t.Fatal(err)
	}
	w := newWorker(t, store, reg, p, complete)
	w.Repo = gitRepo(t)
	w.Fast = fast
	w.fastOnce.Do(func() {})
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(it.ID)
	return got, w
}

var plainCard = kanban.ItemInput{Title: "make the parser keep the last record", Body: "It drops the final record when the file has no trailing newline.", Labels: []string{"build"}}

func TestBuilderDraftsManualGatesForACardWithNone(t *testing.T) {
	f := &fastRole{reply: "- the last record is kept without a trailing newline\n- an empty file still returns no records"}
	got, _ := claimOnce(t, drafter(true), plainCard, f)
	if len(got.Gates) != 2 {
		t.Fatalf("two drafted gates: %+v", got.Gates)
	}
	for i, g := range got.Gates {
		if g.Kind != kanban.GateManual || g.Suite != "" || g.State != kanban.GateUnmet || g.ID != []string{"G1", "G2"}[i] {
			t.Errorf("a drafted gate is manual and unmet with a harness id: %+v", g)
		}
	}
	if f.calls() != 1 {
		t.Fatalf("one draft call, got %d", f.calls())
	}
	p := f.payloads[0]
	if p.UseCase != rolemanager.UseCaseGateDraft || len(p.Tools) != 0 || !strings.Contains(p.User, "parser keep the last record") {
		t.Fatalf("payload %+v", p)
	}
}

func TestDraftingNeverRunsWhenItShouldNot(t *testing.T) {
	f := &fastRole{reply: "- some outcome to check for the card"}
	if got, _ := claimOnce(t, drafter(false), plainCard, f); len(got.Gates) != 0 || f.calls() != 0 {
		t.Fatalf("draft off: gates %d calls %d", len(got.Gates), f.calls())
	}
	with := plainCard
	with.Gates = []kanban.Gate{{Title: "already has one", Kind: kanban.GateManual}}
	f = &fastRole{reply: "- some outcome to check for the card"}
	got, _ := claimOnce(t, drafter(true), with, f)
	if len(got.Gates) != 1 || got.Gates[0].Title != "already has one" || f.calls() != 0 {
		t.Fatalf("a card that has gates keeps them and costs no call: %+v calls %d", got.Gates, f.calls())
	}
}

func TestDraftFailureLeavesTheCardAsItWas(t *testing.T) {
	for name, f := range map[string]*fastRole{
		"error":    {err: errors.New("down")},
		"empty":    {reply: "   "},
		"commands": {reply: "`go test ./...`\nrm -rf /; echo"},
	} {
		got, _ := claimOnce(t, drafter(true), plainCard, f)
		if len(got.Gates) != 0 {
			t.Errorf("%s: no usable draft, no gates: %+v", name, got.Gates)
		}
	}
	got, _ := claimOnce(t, drafter(true), plainCard, nil)
	if len(got.Gates) != 0 {
		t.Fatalf("no fast model, no gates: %+v", got.Gates)
	}
}

func TestDraftedLinesCannotNameASuiteOrACommand(t *testing.T) {
	f := &fastRole{reply: "suite go passes\nrun the tests with make test\nthe parser keeps the last record"}
	got, _ := claimOnce(t, drafter(true), plainCard, f)
	for _, g := range got.Gates {
		if g.Kind != kanban.GateManual || g.Suite != "" || g.Dir != "" || g.Test != "" {
			t.Fatalf("a drafted gate never references a suite: %+v", g)
		}
	}
}

func TestDeliveryReportUsesFactsAndFallsBack(t *testing.T) {
	store, reg := testEnv(t)
	it, _, err := store.Add(kanban.ItemInput{Title: "t", Gates: []kanban.Gate{
		{Title: "suite passes", Kind: kanban.GateRunnable, Suite: "go"},
		{Title: "wording reviewed", Kind: kanban.GateManual},
	}}, kanban.Provenance{})
	if err != nil {
		t.Fatal(err)
	}
	w := newWorker(t, store, reg, gatedBuilder(agentprofile.VerifyEnforce), complete)
	w.Repo = gitRepo(t)
	if _, err := store.ClaimID(it.ID, kanban.ClaimRequest{Worker: w.Record.ID, Profile: "p", Lease: time.Hour}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"G1", "G2"} {
		if _, err := store.SetGate(it.ID, w.Record.ID, id, kanban.GateMet, "", "ok"); err != nil {
			t.Fatal(err)
		}
	}
	ws, err := PrepareWorktree(context.Background(), w.Repo, it, "")
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Remove(context.Background())

	// No fast model: the harness-composed note, from facts.
	got := w.deliveryReport(context.Background(), ws, it, 3)
	if !strings.Contains(got, "All 2 acceptance gate(s) are met.") || !strings.Contains(got, "ran the runnable gates") {
		t.Fatalf("composed note %q", got)
	}
	// A fast model writes it, seeing facts only.
	f := &fastRole{reply: "The harness checked both gates and they are met."}
	w.Fast = f
	if got := w.deliveryReport(context.Background(), ws, it, 3); got != f.reply {
		t.Fatalf("role note %q", got)
	}
	p := f.payloads[0]
	if p.UseCase != rolemanager.UseCaseDeliveryReport || strings.Contains(p.User, "suite passes") || strings.Contains(p.User, "wording reviewed") {
		t.Fatalf("the report sees ids and states, never a gate's or a card's words: %q", p.User)
	}
	// A verification record for the branch adds the regression facts.
	head, _ := ws.Head(context.Background())
	v := quality.NewVerification(it.Short(), head, "", w.clock())
	v.Compared, v.Regressed = true, []string{"go"}
	if err := quality.WriteVerification(w.Repo, v); err != nil {
		t.Fatal(err)
	}
	w.Fast = nil
	if got := w.deliveryReport(context.Background(), ws, it, 3); !strings.Contains(got, "1 regression(s) against the base commit.") {
		t.Fatalf("note %q", got)
	}
	// An unmet runnable gate is not called verified.
	if _, err := store.SetGate(it.ID, w.Record.ID, "G1", kanban.GateUnmet, "", "exit 1"); err != nil {
		t.Fatal(err)
	}
	if got := w.deliveryReport(context.Background(), ws, it, 3); strings.Contains(got, "ran the runnable gates") || !strings.Contains(got, "1 of 2") {
		t.Fatalf("note %q", got)
	}
}
