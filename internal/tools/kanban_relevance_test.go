package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/kanban"
)

type fakeRelevance struct {
	unclear, clarityOK bool
	flagged            []string
	alignOK            bool
	clarityCalls       int
	alignCalls         int
}

func (f *fakeRelevance) Clarity(context.Context, string, string) (bool, bool) {
	f.clarityCalls++
	return f.unclear, f.clarityOK
}

func (f *fakeRelevance) Alignment(context.Context, []kanban.Gate) ([]string, bool) {
	f.alignCalls++
	return f.flagged, f.alignOK
}

func fileClear(t *testing.T, r *fakeRelevance, mutate func(*WorkerClaim)) kanban.Item {
	t.Helper()
	h, claim, store := autoHandoff(t)
	claim.Relevance = r
	if mutate != nil {
		mutate(claim)
	}
	args := map[string]any{"title": "clear task", "body": goodBody(), "labels": []any{"build"}, "gates": []any{gateArg("runnable", "go", "", "")}}
	if _, err := h.Execute(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	it, _ := store.Get(claim.HandedOff()[0])
	return it
}

func TestJevCanOnlySendAClearHandoffToReview(t *testing.T) {
	it := fileClear(t, &fakeRelevance{unclear: true, clarityOK: true}, nil)
	if it.List != kanban.Review || !strings.Contains(it.Body, "review: the decision model rated it unclear") {
		t.Fatalf("an unclear rating sends it to review: %s %q", it.List, it.Body)
	}
	it = fileClear(t, &fakeRelevance{flagged: []string{"G1"}, alignOK: true}, nil)
	if it.List != kanban.Review || !strings.Contains(it.Body, "gate G1 may not measure its title") {
		t.Fatalf("a flagged gate sends it to review: %s %q", it.List, it.Body)
	}
}

func TestJevNoObjectionOrNoAnswerLeavesTheCountedRuleAlone(t *testing.T) {
	for name, r := range map[string]*fakeRelevance{
		"rated clear":     {unclear: false, clarityOK: true, alignOK: true},
		"unknown":         {unclear: true, clarityOK: false, flagged: []string{"G1"}, alignOK: false},
		"no relevance":    nil,
		"nothing flagged": {clarityOK: true, alignOK: true},
	} {
		var rel HandoffRelevance
		if r != nil {
			rel = r
		}
		h, claim, store := autoHandoff(t)
		claim.Relevance = rel
		args := map[string]any{"title": "clear task", "body": goodBody(), "labels": []any{"build"}, "gates": []any{gateArg("runnable", "go", "", "")}}
		if _, err := h.Execute(context.Background(), args); err != nil {
			t.Fatal(err)
		}
		it, _ := store.Get(claim.HandedOff()[0])
		if it.List != kanban.Backlog {
			t.Errorf("%s: an unknown answer is not an objection: %s %q", name, it.List, it.Body)
		}
	}
}

func TestJevNeverMovesAHandoffOutOfReview(t *testing.T) {
	// The counted rule says review; whatever Jev says, it stays there, and the
	// backend is not even asked.
	r := &fakeRelevance{unclear: false, clarityOK: true, alignOK: true}
	h, claim, store := autoHandoff(t)
	claim.Relevance = r
	args := map[string]any{"title": "vague", "body": "short", "labels": []any{"build"}, "gates": []any{gateArg("runnable", "go", "", "")}}
	if _, err := h.Execute(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	it, _ := store.Get(claim.HandedOff()[0])
	if it.List != kanban.Review {
		t.Fatalf("a high rating cannot approve: %s", it.List)
	}
	if r.clarityCalls != 0 || r.alignCalls != 0 {
		t.Fatalf("a handoff already in review costs no backend call: %d %d", r.clarityCalls, r.alignCalls)
	}
}

func TestRelevanceIsOnlyConsultedWhenRoutingIsAuto(t *testing.T) {
	r := &fakeRelevance{unclear: true, clarityOK: true}
	it := fileClear(t, r, func(c *WorkerClaim) { c.HandoffAuto = false })
	if it.List != kanban.Review || r.clarityCalls != 0 {
		t.Fatalf("a fixed list is fixed and free: %s calls %d", it.List, r.clarityCalls)
	}
}
