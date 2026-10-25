package fleet

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tools"
)

// outOfPasses is a turn that runs out of passes. write leaves a file in the
// worktree (progress the harness commits).
func outOfPasses(t *testing.T, write bool, turns *[]Turn) TurnRunner {
	return func(ctx context.Context, tt Turn) (run.Result, error) {
		if turns != nil {
			*turns = append(*turns, tt)
		}
		if write {
			name := filepath.Join(tt.Workdir, "progress-"+tt.SessionID)
			if err := os.WriteFile(name, []byte("step\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return run.Result{StopReason: run.StopMaxPasses, Passes: 8}, nil
	}
}

func continuingWorker(t *testing.T, runner TurnRunner, maxCont int) (*kanban.Store, kanban.Item, *Worker) {
	t.Helper()
	store, reg := testEnv(t)
	it, _, _ := store.Add(kanban.ItemInput{Title: "make the tests pass", Labels: []string{"build"}}, kanban.Provenance{})
	p := builderProfile()
	p.Workspace = &agentprofile.WorkspaceSpec{Isolation: agentprofile.IsolationWorktree}
	p.Kanban.MaxContinuations = maxCont
	w := newWorker(t, store, reg, p, runner)
	w.Repo = gitRepo(t)
	return store, it, w
}

func runOnce(t *testing.T, w *Worker, store *kanban.Store, id string) kanban.Item {
	t.Helper()
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// A claim that runs out of budget after changing files goes back to be resumed:
// no attempt is counted, the branch is kept, and the next claim is told to
// resume it.
func TestABudgetRunOutWithProgressIsContinued(t *testing.T) {
	var turns []Turn
	store, it, w := continuingWorker(t, outOfPasses(t, true, &turns), 0)
	got := runOnce(t, w, store, it.ID)
	if got.List != kanban.Backlog || got.Attempts != 0 || got.Continuations != 1 || got.Branch == "" {
		t.Fatalf("progress was failed: %s attempts=%d continuations=%d branch=%q (%q)", got.List, got.Attempts, got.Continuations, got.Branch, got.LastNote())
	}
	if !strings.Contains(got.LastNote(), "continued (1 of 5)") {
		t.Fatalf("the release note does not say the card was continued: %q", got.LastNote())
	}
	// The failure skip-list does not hold a continued card: the same worker
	// claims it again and is told to resume the branch.
	if _, held := w.failures[it.ID]; held {
		t.Fatal("a continued card was put on the worker's failure list")
	}
	got = runOnce(t, w, store, it.ID)
	if got.Continuations != 2 || got.Attempts != 0 {
		t.Fatalf("second claim: attempts=%d continuations=%d", got.Attempts, got.Continuations)
	}
	if len(turns) != 2 || turns[1].Item.Branch == "" || w.resumeDirective(turns[1].Item) == "" {
		t.Fatalf("the second claim was not told to resume: %d turns", len(turns))
	}
}

// A claim that runs out of budget having done nothing is a failed attempt.
func TestABudgetRunOutWithoutProgressCountsAnAttempt(t *testing.T) {
	store, it, w := continuingWorker(t, outOfPasses(t, false, nil), 0)
	got := runOnce(t, w, store, it.ID)
	if got.Attempts != 1 || got.Continuations != 0 {
		t.Fatalf("no progress was continued: %s attempts=%d continuations=%d (%q)", got.List, got.Attempts, got.Continuations, got.LastNote())
	}
}

// A finding recorded on the card is progress too, even with no file changed.
func TestAFindingRecordedBeforeTheBudgetEndsIsProgress(t *testing.T) {
	var store *kanban.Store
	runner := func(ctx context.Context, tt Turn) (run.Result, error) {
		up := tools.KanbanUpdate{KanbanBase: tools.KanbanBase{Store: store, Source: kanban.NewSource(kanban.Provenance{}), Claim: tt.Claim}}
		if _, err := up.Execute(ctx, map[string]any{"id": tt.Item.ID, "note": "the failing test is TestDial: it dials a port nothing listens on"}); err != nil {
			t.Fatal(err)
		}
		return run.Result{StopReason: run.StopMaxPasses, Passes: 8}, nil
	}
	s, it, w := continuingWorker(t, runner, 0)
	store = s
	got := runOnce(t, w, store, it.ID)
	if got.Attempts != 0 || got.Continuations != 1 {
		t.Fatalf("a recorded finding was not progress: attempts=%d continuations=%d (%q)", got.Attempts, got.Continuations, got.LastNote())
	}
}

// Continuations are bounded: past max_continuations the card waits for a person.
func TestContinuationsPastTheLimitBlockTheCard(t *testing.T) {
	store, it, w := continuingWorker(t, outOfPasses(t, true, nil), 2)
	runOnce(t, w, store, it.ID)
	runOnce(t, w, store, it.ID)
	got := runOnce(t, w, store, it.ID)
	if got.List != kanban.Blocked || !strings.Contains(got.LastNote(), "blocked after 2 continuations") {
		t.Fatalf("the third run-out was not blocked: %s continuations=%d (%q)", got.List, got.Continuations, got.LastNote())
	}
}

// The continuation count starts again when the card moves on, like attempts.
func TestContinuationsResetWhenTheCardMovesOn(t *testing.T) {
	store, it, w := continuingWorker(t, outOfPasses(t, true, nil), 0)
	runOnce(t, w, store, it.ID)
	w.Runner = complete
	got := runOnce(t, w, store, it.ID)
	if got.List != kanban.Review || got.Continuations != 0 {
		t.Fatalf("moved on with continuations=%d (%s)", got.Continuations, got.List)
	}
}
