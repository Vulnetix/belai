package fleet

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tools"
)

// recording returns a runner that records an outcome through the real tool, as
// the model would, and ends the turn with stop.
func recording(store *kanban.Store, outcome, reason string, stop run.StopReason) TurnRunner {
	return func(ctx context.Context, tt Turn) (run.Result, error) {
		tool := tools.KanbanOutcome{KanbanBase: tools.KanbanBase{Store: store, Claim: tt.Claim}}
		if _, err := tool.Execute(ctx, map[string]any{"outcome": outcome, "reason": reason}); err != nil {
			return run.Result{}, err
		}
		return run.Result{StopReason: stop, Passes: 3}, nil
	}
}

func reviewerLikeProfile() agentprofile.AgentProfile {
	p := builderProfile()
	p.Name = "t-reviewer"
	p.Kanban.Labels = []string{"needs-review"}
	p.Kanban.OnSuccess = agentprofile.Route{List: "done", DropLabels: []string{"needs-review"}}
	p.Kanban.OnFailure = agentprofile.Route{List: "backlog", Labels: []string{"build"}, DropLabels: []string{"needs-review"}}
	return p
}

// A recorded outcome routes the card, whatever stop reason ended the turn: it is
// the worker's own statement, so it does not depend on an evaluator reading the
// closing words, and running out of passes after it does not undo it.
func TestRecordedOutcomeRoutesTheCard(t *testing.T) {
	for _, c := range []struct {
		name     string
		profile  agentprofile.AgentProfile
		labels   []string
		outcome  string
		stop     run.StopReason
		wantList kanban.List
		attempts int
		note     string
	}{
		{"success", builderProfile(), []string{"build"}, "success", run.StopDecided, kanban.Review, 0, "recorded success"},
		{"success then out of passes", builderProfile(), []string{"build"}, "success", run.StopMaxPasses, kanban.Review, 0, "recorded success"},
		{"failure goes back with the profile's labels", reviewerLikeProfile(), []string{"needs-review"}, "failure", run.StopDecided, kanban.Backlog, 1, "recorded failure"},
		{"approval", reviewerLikeProfile(), []string{"needs-review"}, "success", run.StopDecided, kanban.Done, 0, "recorded success"},
		{"blocked", builderProfile(), []string{"build"}, "blocked", run.StopDecided, kanban.Blocked, 1, "cannot continue"},
	} {
		t.Run(c.name, func(t *testing.T) {
			store, reg := testEnv(t)
			it, _, _ := store.Add(kanban.ItemInput{Title: "do it", Labels: c.labels}, kanban.Provenance{})
			w := newWorker(t, store, reg, c.profile, recording(store, c.outcome, "because", c.stop))
			if err := w.Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			after, _ := store.Get(it.ID)
			if after.List != c.wantList || after.Attempts != c.attempts || after.ClaimedBy != "" {
				t.Fatalf("released %s attempts %d claimed %q, want %s attempts %d", after.List, after.Attempts, after.ClaimedBy, c.wantList, c.attempts)
			}
			var notes []string
			for _, h := range after.History {
				notes = append(notes, h.Note)
			}
			if !slices.ContainsFunc(notes, func(n string) bool { return strings.Contains(n, c.note) }) {
				t.Fatalf("no release note with %q in %q", c.note, notes)
			}
			if !slices.Contains(notes, "outcome "+c.outcome+": because") {
				t.Fatalf("the worker's reason is on the item: %q", notes)
			}
			if c.name == "failure goes back with the profile's labels" && !slices.Contains(after.Labels, "build") {
				t.Fatalf("labels %v", after.Labels)
			}
		})
	}
}

// A recorded success is not a pass for work the harness can see is incomplete:
// a worker whose profile needs a verdict still fails without one.
func TestRecordedSuccessDoesNotReplaceAVerdict(t *testing.T) {
	store, reg := testEnv(t)
	it, _, _ := store.Add(kanban.ItemInput{Title: "verify it", Labels: []string{"needs-verify"}}, kanban.Provenance{})
	p := builderProfile()
	p.Name = "t-verifier"
	p.Kanban.Labels = []string{"needs-verify"}
	p.Kanban.OnSuccess = agentprofile.Route{List: "done"}
	p.Kanban.Security = &agentprofile.SecuritySpec{VEX: true, Verdicts: []string{"fixed", "false_positive", "no_fix", "needs_human", "rejected"}}
	w := newWorker(t, store, reg, p, recording(store, "success", "looks fine", run.StopDecided))
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, _ := store.Get(it.ID)
	if after.List == kanban.Done || after.Attempts != 1 || !strings.Contains(after.LastNote(), "without recording a verdict") {
		t.Fatalf("released %s attempts %d note %q", after.List, after.Attempts, after.LastNote())
	}
}

// judge reads the record only when the turn ended in a way that leaves it
// standing: out of budget after it was made does, an error or a cancellation
// does not.
func TestJudgeHonoursARecordedOutcomeOnlyWhenTheTurnSettled(t *testing.T) {
	store, reg := testEnv(t)
	w := newWorker(t, store, reg, builderProfile(), complete)
	it := kanban.Item{}
	board := kanban.Open(filepath.Join(t.TempDir(), "board"))
	record := func(outcome string) *tools.WorkerClaim {
		item, _, _ := board.Add(kanban.ItemInput{Title: "x"}, kanban.Provenance{})
		c := &tools.WorkerClaim{Worker: "w", Item: item.ID}
		if _, err := (tools.KanbanOutcome{KanbanBase: tools.KanbanBase{Store: board, Claim: c}}).Execute(context.Background(), map[string]any{"outcome": outcome, "reason": "r"}); err != nil {
			t.Fatal(err)
		}
		return c
	}
	decided := run.Result{StopReason: run.StopDecided, Passes: 2}
	cases := []struct {
		name       string
		claim      *tools.WorkerClaim
		res        run.Result
		runErr     error
		cause      error
		wantFailed bool
		wantBlock  bool
	}{
		{"success", record("success"), decided, nil, nil, false, false},
		{"success, wall budget ended after", record("success"), run.Result{StopReason: run.StopCancelled}, context.DeadlineExceeded, errWallBudget, false, false},
		{"success, passes ran out after", record("success"), run.Result{StopReason: run.StopMaxPasses}, errors.New("max passes"), nil, false, false},
		{"failure", record("failure"), decided, nil, nil, true, false},
		{"blocked", record("blocked"), decided, nil, nil, true, true},
		{"success, then a provider error", record("success"), run.Result{StopReason: run.StopError}, errors.New("boom"), nil, true, false},
		{"success, then cancelled", record("success"), run.Result{StopReason: run.StopCancelled}, context.Canceled, context.Canceled, true, false},
		{"nothing recorded, complete", &tools.WorkerClaim{}, run.Result{StopReason: run.StopComplete}, nil, nil, false, false},
		{"nothing recorded, out of passes", &tools.WorkerClaim{}, run.Result{StopReason: run.StopMaxPasses}, errors.New("max"), nil, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := w.judge(it, c.res, c.runErr, c.cause, c.claim)
			if o.failed != c.wantFailed || o.blocked != c.wantBlock {
				t.Fatalf("failed %v blocked %v (%q), want failed %v blocked %v", o.failed, o.blocked, o.note, c.wantFailed, c.wantBlock)
			}
		})
	}
}
