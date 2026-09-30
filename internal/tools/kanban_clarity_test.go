package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/kanban"
)

var runnable = []kanban.Gate{{Title: "suite passes", Kind: kanban.GateRunnable, Suite: "go"}}

func goodBody() string { return strings.Repeat("The parser drops the last record. ", 4) }

func TestRouteByClarity(t *testing.T) {
	ok := kanban.ItemInput{Title: "Fix the parser dropping the last record", Body: goodBody(), Gates: runnable}
	if l, why := routeByClarity(ok, ""); l != kanban.Backlog || len(why) != 0 {
		t.Fatalf("a clear, concise task with a runnable gate goes to backlog: %s %v", l, why)
	}
	if l, _ := routeByClarity(ok, ClarityClear); l != kanban.Backlog {
		t.Fatal("declaring it clear changes nothing")
	}
	cases := map[string]func(*kanban.ItemInput) string{
		"long title": func(in *kanban.ItemInput) string {
			in.Title = strings.Repeat("t", ClearTitleRunes+1)
			return "title over"
		},
		"short body": func(in *kanban.ItemInput) string { in.Body = strings.Repeat("b", ClearBodyMin-1); return "body under" },
		"empty body": func(in *kanban.ItemInput) string { in.Body = "  "; return "body under" },
		"long body":  func(in *kanban.ItemInput) string { in.Body = strings.Repeat("b", ClearBodyMax+1); return "body over" },
		"no gates":   func(in *kanban.ItemInput) string { in.Gates = nil; return "no runnable gate" },
		"manual only": func(in *kanban.ItemInput) string {
			in.Gates = []kanban.Gate{{Title: "m", Kind: kanban.GateManual}}
			return "no runnable gate"
		},
		"too many gates": func(in *kanban.ItemInput) string {
			in.Gates = manyRunnable(ClearMaxGates + 1)
			return "may hide several tasks"
		},
	}
	for name, mutate := range cases {
		in := ok
		in.Gates = append([]kanban.Gate(nil), ok.Gates...)
		want := mutate(&in)
		l, why := routeByClarity(in, "")
		if l != kanban.Review || !strings.Contains(strings.Join(why, ";"), want) {
			t.Errorf("%s: %s %v, want review naming %q", name, l, why, want)
		}
	}
	// The boundaries themselves are clear.
	edge := ok
	edge.Title = strings.Repeat("t", ClearTitleRunes)
	edge.Body = strings.Repeat("b", ClearBodyMin)
	edge.Gates = manyRunnable(ClearMaxGates)
	if l, why := routeByClarity(edge, ""); l != kanban.Backlog {
		t.Fatalf("the limits are inclusive: %s %v", l, why)
	}
	edge.Body = strings.Repeat("b", ClearBodyMax)
	if l, _ := routeByClarity(edge, ""); l != kanban.Backlog {
		t.Fatal("a body of exactly the maximum is clear")
	}
}

func manyRunnable(n int) []kanban.Gate {
	var out []kanban.Gate
	for i := range n {
		out = append(out, kanban.Gate{Title: strings.Repeat("g", i+1), Kind: kanban.GateRunnable, Suite: "go"})
	}
	return out
}

func TestADeclaredDoubtOnlyEverNarrows(t *testing.T) {
	ok := kanban.ItemInput{Title: "fix", Body: goodBody(), Gates: runnable}
	for _, d := range []string{ClarityUnclear, ClaritySplit} {
		l, why := routeByClarity(ok, d)
		if l != kanban.Review || len(why) != 1 {
			t.Errorf("%s: %s %v", d, l, why)
		}
	}
	// A model cannot claim clarity for a task the counts say is not clear.
	bad := kanban.ItemInput{Title: "fix", Body: "short", Gates: nil}
	if l, _ := routeByClarity(bad, ClarityClear); l != kanban.Review {
		t.Fatal("declaring a task clear never moves it out of review")
	}
}

func autoHandoff(t *testing.T) (KanbanHandoff, *WorkerClaim, *kanban.Store) {
	t.Helper()
	store := kanban.Open(filepath.Join(t.TempDir(), "kanban"))
	parent, _, _ := store.Add(kanban.ItemInput{Title: "quality card"}, kanban.Provenance{})
	claim := &WorkerClaim{
		Worker: "w1", Item: parent.ID, HandoffLabels: []string{"build"},
		HandoffList: kanban.Review, HandoffAuto: true,
		GateSuites: []GateSuite{{Name: "go", Ecosystem: "go"}}, GatesRequired: true,
	}
	return KanbanHandoff{KanbanBase{Store: store, Claim: claim}}, claim, store
}

func TestAutoHandoffSendsAClearTaskToBacklogAndTheRestToReview(t *testing.T) {
	h, claim, store := autoHandoff(t)
	gates := []any{gateArg("runnable", "go", "", "")}
	if _, err := h.Execute(context.Background(), map[string]any{"title": "clear task", "body": goodBody(), "labels": []any{"build"}, "gates": gates}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Execute(context.Background(), map[string]any{"title": "vague task", "body": "short", "labels": []any{"build"}, "gates": gates}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Execute(context.Background(), map[string]any{"title": "doubtful task", "body": goodBody(), "labels": []any{"build"}, "gates": gates, "clarity": ClaritySplit}); err != nil {
		t.Fatal(err)
	}
	got := map[string]kanban.Item{}
	for _, id := range claim.HandedOff() {
		it, _ := store.Get(id)
		got[it.Title] = it
	}
	if got["clear task"].List != kanban.Backlog || strings.Contains(got["clear task"].Body, "review:") {
		t.Fatalf("clear: %s %q", got["clear task"].List, got["clear task"].Body)
	}
	if v := got["vague task"]; v.List != kanban.Review || !strings.Contains(v.Body, "review: body under 80 characters") {
		t.Fatalf("vague: %s %q", v.List, v.Body)
	}
	if d := got["doubtful task"]; d.List != kanban.Review || !strings.Contains(d.Body, "a person to split it") {
		t.Fatalf("doubtful: %s %q", d.List, d.Body)
	}
}

func TestAutoHandoffIgnoresTheListTheModelAsksFor(t *testing.T) {
	h, claim, store := autoHandoff(t)
	args := map[string]any{"title": "vague", "body": "short", "labels": []any{"build"}, "list": "backlog", "gates": []any{gateArg("runnable", "go", "", "")}}
	if _, err := h.Execute(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	it, _ := store.Get(claim.HandedOff()[0])
	if it.List != kanban.Review {
		t.Fatalf("the model cannot ask a vague task past the human gate: %s", it.List)
	}
}

func TestAutoHandoffRefusesAnUnknownClarity(t *testing.T) {
	h, claim, _ := autoHandoff(t)
	_, err := h.Execute(context.Background(), map[string]any{"title": "t", "body": goodBody(), "labels": []any{"build"}, "clarity": "obvious", "gates": []any{gateArg("runnable", "go", "", "")}})
	if err == nil || !strings.Contains(err.Error(), "clarity must be") {
		t.Fatalf("got %v", err)
	}
	if len(claim.HandedOff()) != 0 {
		t.Fatal("a refused handoff files nothing")
	}
}

func TestClarityIsOfferedOnlyWhenRoutingIsAuto(t *testing.T) {
	h, claim, _ := autoHandoff(t)
	if _, ok := h.Definition().Properties["clarity"]; !ok {
		t.Fatal("an auto worker may declare its doubt")
	}
	claim.HandoffAuto = false
	if _, ok := h.Definition().Properties["clarity"]; ok {
		t.Fatal("a worker whose list is fixed has no clarity argument")
	}
}

func TestFixedListStaysFixedWithoutAuto(t *testing.T) {
	h, claim, store := autoHandoff(t)
	claim.HandoffAuto = false
	if _, err := h.Execute(context.Background(), map[string]any{"title": "clear", "body": goodBody(), "labels": []any{"build"}, "gates": []any{gateArg("runnable", "go", "", "")}}); err != nil {
		t.Fatal(err)
	}
	it, _ := store.Get(claim.HandedOff()[0])
	if it.List != kanban.Review {
		t.Fatalf("without auto every handoff goes to the forced list: %s", it.List)
	}
}
