package fleet

import (
	"context"
	"slices"
	"testing"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/kanban"
)

// Where a verdict sends the card is the profile's to say. The built-in patcher
// and verifier route as they always did; a profile that names other lists and
// labels is followed instead of the built-in crew's wiring.
func TestAVerdictFollowsTheProfilesOwnSuccessRoute(t *testing.T) {
	store, reg := testEnv(t)
	p := patcherProfile()
	p.Name = "t-triage"
	p.Kanban.OnSuccess = agentprofile.Route{List: "review", Labels: []string{"triaged"}, DropLabels: []string{"vuln"}}
	w := newWorker(t, store, reg, p, nil)
	w.Runner = verdictRunner(store, map[string]any{
		"verdict": "false_positive", "justification": "never imported", "evidence": []any{"grep prints nothing"},
	}, false)
	it := findingCardOn(t, store, w, kanban.Backlog, "vuln")
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(it.ID)
	if got.List != kanban.Review || got.Verdict != kanban.VerdictFalsePositive {
		t.Fatalf("card %s verdict %s", got.List, got.Verdict)
	}
	if !slices.Contains(got.Labels, "triaged") || slices.Contains(got.Labels, kanban.LabelNeedsVerify) || slices.Contains(got.Labels, kanban.LabelVuln) {
		t.Fatalf("labels %v: the profile's route adds triaged and drops vuln, and nothing else is the built-in crew's business", got.Labels)
	}
}

// A card that needs a person is blocked whatever the profile does with a closed
// one, and sheds the labels that profile's success route drops.
func TestABlockedVerdictDropsTheProfilesOwnLabels(t *testing.T) {
	store, reg := testEnv(t)
	p := verifierProfile()
	p.Name = "t-auditor"
	p.Kanban.Labels = []string{"audit"}
	p.Kanban.OnSuccess = agentprofile.Route{List: "done", DropLabels: []string{"audit"}}
	w := newWorker(t, store, reg, p, nil)
	w.Runner = verdictRunner(store, map[string]any{
		"verdict": "needs_human", "justification": "the fix breaks a public API",
	}, true)
	it := findingCardOn(t, store, w, kanban.Review, "audit")
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(it.ID)
	if got.List != kanban.Blocked || slices.Contains(got.Labels, "audit") {
		t.Fatalf("card %s labels %v", got.List, got.Labels)
	}
}

// With no labels dropped by the profile the harness's own label still goes.
func TestABlockedVerdictFallsBackToTheHarnessLabel(t *testing.T) {
	store, reg := testEnv(t)
	p := verifierProfile()
	p.Kanban.OnSuccess = agentprofile.Route{List: "done"}
	w := newWorker(t, store, reg, p, nil)
	w.Runner = verdictRunner(store, map[string]any{"verdict": "needs_human", "justification": "a person must choose"}, true)
	it := findingCardOn(t, store, w, kanban.Review, "needs-verify")
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(it.ID)
	if got.List != kanban.Blocked || slices.Contains(got.Labels, kanban.LabelNeedsVerify) {
		t.Fatalf("card %s labels %v", got.List, got.Labels)
	}
}
