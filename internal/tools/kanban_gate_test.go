package tools

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/kanban"
)

func gateReviewSetup(t *testing.T) (KanbanGate, *WorkerClaim, *kanban.Store, kanban.Item) {
	t.Helper()
	store := kanban.Open(filepath.Join(t.TempDir(), "kanban"))
	it, _, err := store.Add(kanban.ItemInput{Title: "card", Labels: []string{"needs-review"}, Gates: []kanban.Gate{
		{Title: "suite passes", Kind: kanban.GateRunnable, Suite: "go"},
		{Title: "wording reviewed", Kind: kanban.GateManual},
	}}, kanban.Provenance{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimID(it.ID, kanban.ClaimRequest{Worker: "w1", Profile: "p", Lease: time.Minute}); err != nil {
		t.Fatal(err)
	}
	claim := &WorkerClaim{Worker: "w1", Item: it.ID, GateReview: true}
	return KanbanGate{KanbanBase{Store: store, Claim: claim}}, claim, store, it
}

func gateCall(t KanbanGate, gate, state, evidence string) error {
	_, err := t.Execute(context.Background(), map[string]any{"gate": gate, "state": state, "evidence": evidence})
	return err
}

func TestKanbanGateDecidesAManualGateOnTheClaimedItem(t *testing.T) {
	tool, _, store, it := gateReviewSetup(t)
	if err := gateCall(tool, "g2", "met", "\x1b[1mdocs/x.md:12 states the rule\x1b[0m"); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(it.ID)
	g, _ := got.GateByID("G2")
	if g.State != kanban.GateMet || g.Note != "docs/x.md:12 states the rule" {
		t.Fatalf("gate G2 %+v", g)
	}
	if err := gateCall(tool, "G2", "abandoned", "the owner is unreachable"); err != nil {
		t.Fatal(err)
	}
	got, _ = store.Get(it.ID)
	if !got.GatesAbandoned() || !got.GatesOpen() {
		t.Fatal("an abandoned gate is recorded and leaves the card open")
	}
}

func TestKanbanGateCannotDecideARunnableGate(t *testing.T) {
	tool, _, store, it := gateReviewSetup(t)
	err := gateCall(tool, "G1", "met", "I ran the tests myself")
	if err == nil || !strings.Contains(err.Error(), "the harness decides it") {
		t.Fatalf("a model can never mark a runnable gate met: %v", err)
	}
	got, _ := store.Get(it.ID)
	if g, _ := got.GateByID("G1"); g.State != kanban.GateUnmet {
		t.Fatalf("G1 changed: %+v", g)
	}
}

func TestKanbanGateArgumentChecks(t *testing.T) {
	tool, _, _, _ := gateReviewSetup(t)
	for name, c := range map[string]struct{ gate, state, evidence, want string }{
		"unknown state":  {"G2", "passed", "x", "state must be"},
		"no evidence":    {"G2", "met", "  ", "one line of evidence"},
		"unknown gate":   {"G7", "met", "x", "has no gate"},
		"malformed gate": {"../x", "met", "x", "has no gate"},
	} {
		err := gateCall(tool, c.gate, c.state, c.evidence)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want %q, got %v", name, c.want, err)
		}
	}
	// The evidence is one capped line.
	long := strings.Repeat("word ", 200)
	if err := gateCall(tool, "G2", "unmet", long); err != nil {
		t.Fatal(err)
	}
}

func TestKanbanGateIsBoundToItsClaim(t *testing.T) {
	tool, claim, store, it := gateReviewSetup(t)
	claim.GateReview = false
	if err := gateCall(tool, "G2", "met", "x"); err == nil {
		t.Fatal("a worker without review recorded a gate")
	}
	claim.GateReview = true
	if _, err := store.Unclaim(it.ID, "taken", "", false); err != nil {
		t.Fatal(err)
	}
	if err := gateCall(tool, "G2", "met", "x"); err == nil {
		t.Fatal("a gate was recorded without holding the claim")
	}
	if err := gateCall(KanbanGate{KanbanBase{Store: store}}, "G2", "met", "x"); err == nil {
		t.Fatal("a session with no claim recorded a gate")
	}
}

func TestKanbanGateOnlyOnTheSurfaceOfAReviewer(t *testing.T) {
	store := kanban.Open(filepath.Join(t.TempDir(), "kanban"))
	names := func(claim *WorkerClaim) []string {
		var out []string
		for _, d := range NewRegistry().WithKanbanWorker(store, nil, claim).Definitions() {
			out = append(out, d.Name)
		}
		return out
	}
	if slices.Contains(names(&WorkerClaim{Worker: "w", Item: "i"}), KanbanGateName) {
		t.Fatal("a worker that does not review got KanbanGate")
	}
	got := names(&WorkerClaim{Worker: "w", Item: "i", GateReview: true, Verdicts: []string{"fixed"}})
	if !slices.Contains(got, KanbanGateName) || !slices.Contains(got, KanbanVerdictName) {
		t.Fatalf("a reviewer with verdicts keeps both tools: %v", got)
	}
	if !IsCoreTool(KanbanGateName) {
		t.Fatal("KanbanGate must be advertised in full")
	}
}
