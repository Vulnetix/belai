package fleet

import (
	"context"
	"testing"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/lintdetect"
	"github.com/vulnetix/belai/internal/quality"
	"github.com/vulnetix/belai/internal/testrun"
)

// The claim rules the website's engine depends on: a card is taken by a
// builtin maintenance worker when its label matches the profile's claim label
// and its assignee, if any, names the profile as the host advertises it
// (belai:test-fixer), the worker's crew, or nobody. The stripped spelling the
// engine once wrote (test-fixer) names no worker.
func TestBuiltinMaintenanceWorkersClaimOnTheirLabelAndAssignee(t *testing.T) {
	cases := []struct {
		profile, label, assignee, crew string
		takes                          bool
	}{
		{"belai:test-fixer", "tests-failing", "", "", true},
		{"belai:test-fixer", "tests-failing", "belai:test-fixer", "", true},
		{"belai:test-fixer", "tests-failing", "belai:maintenance", "belai:maintenance", false}, // a bare name is a profile
		{"belai:test-fixer", "tests-failing", "crew:belai:maintenance", "belai:maintenance", true},
		{"belai:test-fixer", "tests-failing", "crew:belai:maintenance", "", false},
		{"belai:test-fixer", "tests-failing", "test-fixer", "", false},
		{"belai:test-fixer", "tests-failing", "belai:lint-fixer", "", false},
		{"belai:test-fixer", "tests-failing", "person:chris", "", false},
		{"belai:test-fixer", "lint-drift", "", "", false},
		{"belai:test-fixer", "build", "", "", false},
		{"belai:lint-fixer", "lint-drift", "", "", true},
		{"belai:lint-fixer", "lint-drift", "belai:lint-fixer", "", true},
		{"belai:lint-fixer", "lint-drift", "lint-fixer", "", false},
		{"belai:lint-fixer", "tests-failing", "", "", false},
		{"belai:pr-reviewer", "pr-review", "", "", true},
		{"belai:pr-reviewer", "pr-review", "belai:pr-reviewer", "", true},
		{"belai:pr-reviewer", "pr-review", "pr-reviewer", "", false},
		{"belai:pr-reviewer", "tests-failing", "", "", false},
		{"belai:builder", "tests-failing", "", "", false},
		{"belai:builder", "lint-drift", "", "", false},
	}
	for _, c := range cases {
		store, reg := testEnv(t)
		p, err := agentprofile.Load(c.profile)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.Add(kanban.ItemInput{Title: "fix it", Labels: []string{c.label}, Assignee: c.assignee}, kanban.Provenance{}); err != nil {
			t.Fatal(err)
		}
		w := newWorker(t, store, reg, p, complete)
		w.Record.Crew = c.crew
		_, err = store.Claim(w.claimRequest(""))
		if (err == nil) != c.takes {
			t.Errorf("%s on a card labelled %q assigned to %q (crew %q): claimed=%v (%v), want %v", c.profile, c.label, c.assignee, c.crew, err == nil, err, c.takes)
		}
	}
}

// The lint card the sweep files is the card belai:lint-fixer claims, and
// belai:builder, which claims build, does not take it.
func TestTheLintSweepsCardIsTheLintFixersToClaim(t *testing.T) {
	store, reg := testEnv(t)
	p, err := agentprofile.Load("belai:lint-fixer")
	if err != nil {
		t.Fatal(err)
	}
	w := newWorker(t, store, reg, p, complete)
	w.Head = head(secHeadNew)
	w.Lints = func(context.Context) []lintdetect.Linter { return []lintdetect.Linter{golangci} }
	w.RunTests = func(_ context.Context, plan testrun.Plan) []testrun.Result {
		return []testrun.Result{{Suite: plan.Names[0], Command: plan.Argvs[0], Status: testrun.Failed, ExitCode: 1, Output: golangciDirty}}
	}
	w.lintSweep(context.Background(), secHeadNew)
	cards := lintCards(t, store)
	if len(cards) != 1 {
		t.Fatalf("cards %+v", cards)
	}
	if got := cards[0]; got.Assignee != "" || got.List != kanban.Backlog || got.Finding[:len(quality.PrefixLint)] != quality.PrefixLint {
		t.Fatalf("card %+v", got)
	}
	builder, _ := agentprofile.Load("belai:builder")
	other := newWorker(t, store, reg, builder, complete)
	if _, err := store.Claim(other.claimRequest("")); err == nil {
		t.Fatal("the builder took a lint card")
	}
	got, err := store.Claim(w.claimRequest(""))
	if err != nil || got.ID != cards[0].ID {
		t.Fatalf("the lint fixer did not claim its card: %v", err)
	}
}
