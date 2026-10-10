package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/headless"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/scanartifacts"
	"github.com/vulnetix/belai/internal/tools"
)

func patcherProfile() agentprofile.AgentProfile {
	p := scoutProfile()
	p.Name = "t-patcher"
	p.Kanban.Survey = nil
	p.Kanban.Labels = []string{"vuln"}
	p.Kanban.OnSuccess = agentprofile.Route{List: "review", Labels: []string{"needs-verify"}, DropLabels: []string{"vuln"}}
	p.Kanban.Security = &agentprofile.SecuritySpec{Reconcile: true, Verdicts: []string{"fixed", "false_positive", "no_fix", "needs_human"}}
	return p
}

func verifierProfile() agentprofile.AgentProfile {
	p := scoutProfile()
	p.Name = "t-verifier"
	p.Kanban.Survey = nil
	p.Kanban.Lists = []string{"review"}
	p.Kanban.Labels = []string{"needs-verify"}
	p.Kanban.OnSuccess = agentprofile.Route{List: "done", DropLabels: []string{"needs-verify"}}
	p.Kanban.OnFailure = agentprofile.Route{List: "backlog", Labels: []string{"vuln"}, DropLabels: []string{"needs-verify"}}
	p.Kanban.Security = &agentprofile.SecuritySpec{Verdicts: []string{"fixed", "false_positive", "no_fix", "needs_human", "rejected"}, VEX: true}
	return p
}

// verdictRunner records a verdict through the real tool, then completes the
// goal (or not).
func verdictRunner(store *kanban.Store, args map[string]any, finish bool) TurnRunner {
	return func(ctx context.Context, tt Turn) (run.Result, error) {
		if args != nil {
			tool := tools.KanbanVerdict{KanbanBase: tools.KanbanBase{Store: store, Claim: tt.Claim}}
			if _, err := tool.Execute(ctx, args); err != nil {
				return run.Result{}, err
			}
		}
		if !finish {
			return run.Result{StopReason: run.StopIncomplete, Passes: 2}, nil
		}
		return complete(ctx, tt)
	}
}

// findingCardOn files a sweep card for w's repository, in the list and with
// the labels a worker of that profile claims.
func findingCardOn(t *testing.T, store *kanban.Store, w *Worker, list kanban.List, labels ...string) kanban.Item {
	t.Helper()
	in := findingCard(scanartifacts.ReviewFinding{ID: "GHSA-aaaa-bbbb-cccc", Kind: scanartifacts.KindSCA, Package: "lodash", Ecosystem: "npm", Version: "4.17.0", File: "package.json", Severity: "high"}, secHeadOld)
	in.Labels = labels
	it, _, err := store.UpsertFinding(in, kanban.ProvenanceFor(w.Repo, w.Record.ID, headless.HostID()))
	if err != nil {
		t.Fatal(err)
	}
	if list != kanban.Backlog {
		if it, err = store.Move(it.ID, list, "", "s"); err != nil {
			t.Fatal(err)
		}
	}
	return it
}

func vexDoc(t *testing.T, repo, rel string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	if err := json.Unmarshal(data, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func vexStatus(t *testing.T, repo, rel string) string {
	t.Helper()
	st := vexDoc(t, repo, rel)["statements"].([]any)[0].(map[string]any)
	return st["status"].(string)
}

func TestPatcherFalsePositiveGoesToTheVerifierWithoutABranch(t *testing.T) {
	store, reg := testEnv(t)
	w := newWorker(t, store, reg, patcherProfile(), nil)
	w.Runner = verdictRunner(store, map[string]any{
		"verdict": "false_positive", "justification": "the vulnerable function is never imported",
		"evidence": []any{"grep -rn 'lodash.template' . prints nothing"},
	}, false) // a false positive does not need the fix goal to complete
	it := findingCardOn(t, store, w, kanban.Backlog, "vuln")
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(it.ID)
	if got.List != kanban.Review || got.Verdict != kanban.VerdictFalsePositive || got.ClaimedBy != "" ||
		!slices.Contains(got.Labels, kanban.LabelNeedsVerify) || slices.Contains(got.Labels, kanban.LabelVuln) {
		t.Fatalf("card %+v", got)
	}
	if got.Attempts != 0 {
		t.Fatalf("a verdict is not a failed attempt: %d", got.Attempts)
	}
}

func TestPatcherWithoutAVerdictIsTheOrdinaryFixRoute(t *testing.T) {
	store, reg := testEnv(t)
	w := newWorker(t, store, reg, patcherProfile(), verdictRunner(store, nil, true))
	it := findingCardOn(t, store, w, kanban.Backlog, "vuln")
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(it.ID)
	if got.List != kanban.Review || got.Verdict != kanban.VerdictFixed || !slices.Contains(got.Labels, kanban.LabelNeedsVerify) {
		t.Fatalf("card %+v", got)
	}
}

func TestPatcherThatDoesNotCompleteAndRecordsNothingFails(t *testing.T) {
	store, reg := testEnv(t)
	w := newWorker(t, store, reg, patcherProfile(), verdictRunner(store, nil, false))
	it := findingCardOn(t, store, w, kanban.Backlog, "vuln")
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(it.ID)
	if got.List != kanban.Backlog || got.Attempts != 1 || got.Verdict != "" {
		t.Fatalf("card %+v", got)
	}
}

func TestVerifierRoutesEachVerdictAndWritesTheVEX(t *testing.T) {
	for name, c := range map[string]struct {
		args   map[string]any
		list   kanban.List
		status string
	}{
		"fixed": {map[string]any{"verdict": "fixed", "justification": "the scan is clean on this checkout"}, kanban.Done, "fixed"},
		"false positive": {map[string]any{"verdict": "false_positive", "justification": "never imported", "vex_reason": "vulnerable_code_not_in_execute_path",
			"evidence": []any{"grep prints nothing"}}, kanban.Done, "not_affected"},
		"no fix":      {map[string]any{"verdict": "no_fix", "justification": "upstream has no patched release", "tried": []any{"bumped to latest"}}, kanban.Blocked, "affected"},
		"needs human": {map[string]any{"verdict": "needs_human", "justification": "a licence decision is needed"}, kanban.Blocked, "under_investigation"},
	} {
		store, reg := testEnv(t)
		w := newWorker(t, store, reg, verifierProfile(), verdictRunner(store, c.args, true))
		it := findingCardOn(t, store, w, kanban.Review, "needs-verify")
		if err := w.Run(context.Background()); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got, _ := store.Get(it.ID)
		if got.List != c.list || string(got.Verdict) != fmtVerdict(c.args) || got.Attempts != 0 || slices.Contains(got.Labels, kanban.LabelNeedsVerify) {
			t.Fatalf("%s: card %+v", name, got)
		}
		if got.VEX != ".vulnetix/vex/GHSA-aaaa-bbbb-cccc.openvex.json" {
			t.Fatalf("%s: VEX path %q", name, got.VEX)
		}
		if s := vexStatus(t, w.Repo, got.VEX); s != c.status {
			t.Fatalf("%s: VEX status %s, want %s", name, s, c.status)
		}
		if doc := vexDoc(t, w.Repo, got.VEX); !strings.Contains(doc["statements"].([]any)[0].(map[string]any)["products"].([]any)[0].(map[string]any)["@id"].(string), "pkg:npm/lodash@4.17.0") {
			t.Fatalf("%s: product not taken from the card: %v", name, doc)
		}
	}
}

func fmtVerdict(args map[string]any) string { return args["verdict"].(string) }

func TestVerifierRejectionSendsTheCardBackAsAFailedAttempt(t *testing.T) {
	store, reg := testEnv(t)
	w := newWorker(t, store, reg, verifierProfile(), verdictRunner(store, map[string]any{"verdict": "rejected", "justification": "the branch still pins 4.17.0"}, true))
	it := findingCardOn(t, store, w, kanban.Review, "needs-verify")
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(it.ID)
	if got.List != kanban.Backlog || got.Attempts != 1 || !slices.Contains(got.Labels, kanban.LabelVuln) || slices.Contains(got.Labels, kanban.LabelNeedsVerify) {
		t.Fatalf("card %+v", got)
	}
	if got.VEX != "" {
		t.Fatal("a rejection wrote a VEX")
	}
	if _, err := os.Stat(filepath.Join(w.Repo, ".vulnetix", "vex")); !os.IsNotExist(err) {
		t.Fatalf("a VEX directory exists after a rejection: %v", err)
	}
}

func TestVerifierMustRecordAVerdictToCloseACard(t *testing.T) {
	store, reg := testEnv(t)
	w := newWorker(t, store, reg, verifierProfile(), verdictRunner(store, nil, true))
	it := findingCardOn(t, store, w, kanban.Review, "needs-verify")
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(it.ID)
	if got.List == kanban.Done || got.Attempts != 1 || !strings.Contains(got.LastNote(), "without recording a verdict") {
		t.Fatalf("a verifier closed a card with no verdict: %+v (%q)", got, got.LastNote())
	}
}

func TestVerifierCannotWriteAVEXForACardWithNoFindingID(t *testing.T) {
	store, reg := testEnv(t)
	w := newWorker(t, store, reg, verifierProfile(), verdictRunner(store, map[string]any{"verdict": "fixed", "justification": "clean"}, true))
	it, _, _ := store.Add(kanban.ItemInput{Title: "hand-filed", List: kanban.Review, Labels: []string{"needs-verify"}}, kanban.ProvenanceFor(w.Repo, w.Record.ID, headless.HostID()))
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(it.ID)
	if got.List == kanban.Done || !strings.Contains(got.LastNote(), "VEX could not be written") {
		t.Fatalf("card %+v (%q)", got, got.LastNote())
	}
}

// The worktree is dropped before a card reaches Review so the next worker can
// check the branch out; only a card that goes to Done is published from it.
func TestRoutesToDone(t *testing.T) {
	w := &Worker{Profile: agentprofile.AgentProfile{Kanban: &agentprofile.KanbanSpec{OnSuccess: agentprofile.Route{List: "done"}}}}
	if !w.routesToDone(outcome{}) {
		t.Error("a success routed to done must publish from the worktree")
	}
	for name, o := range map[string]outcome{
		"failed":  {failed: true},
		"blocked": {blocked: true},
		"review":  {to: kanban.Review},
	} {
		if w.routesToDone(o) {
			t.Errorf("%s outcome reported as routed to done", name)
		}
	}
	w.Profile.Kanban.OnSuccess.List = "review"
	if w.routesToDone(outcome{}) {
		t.Error("a success routed to review must free the worktree first")
	}
	if (&Worker{}).routesToDone(outcome{}) {
		t.Error("no kanban spec reported as routed to done")
	}
}

// A verifier that recorded its verdict and then ran out of passes has still
// decided: the verdict stands and the card closes, instead of going round again.
func verdictThenStop(store *kanban.Store, args map[string]any, stop run.StopReason, err error) TurnRunner {
	return func(ctx context.Context, tt Turn) (run.Result, error) {
		tool := tools.KanbanVerdict{KanbanBase: tools.KanbanBase{Store: store, Claim: tt.Claim}}
		if _, e := tool.Execute(ctx, args); e != nil {
			return run.Result{}, e
		}

		return run.Result{StopReason: stop, Passes: 4}, err
	}
}

// The wall budget ends the item's context with an error, which is how a slow
// model's overrun actually arrives. The recorded verdict still stands.
func TestVerifierVerdictStandsWhenTheWallBudgetEndsAfterIt(t *testing.T) {
	store, reg := testEnv(t)
	p := verifierProfile()
	p.Budget = &agentprofile.BudgetSpec{MaxWallPerItem: "300ms"}
	args := map[string]any{"verdict": "fixed", "justification": "the scan is clean on this checkout"}
	w := newWorker(t, store, reg, p, func(ctx context.Context, tt Turn) (run.Result, error) {
		tool := tools.KanbanVerdict{KanbanBase: tools.KanbanBase{Store: store, Claim: tt.Claim}}
		if _, err := tool.Execute(ctx, args); err != nil {
			return run.Result{}, err
		}
		<-ctx.Done() // keeps re-checking until the wall budget ends the turn

		return run.Result{StopReason: run.StopCancelled, Passes: 4}, ctx.Err()
	})
	it := findingCardOn(t, store, w, kanban.Review, "needs-verify")
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(it.ID)
	if got.List != kanban.Done || got.Verdict != kanban.VerdictFixed || got.Attempts != 0 || got.VEX == "" {
		t.Fatalf("the verdict was discarded for the overrun: %+v (%q)", got, got.LastNote())
	}
}

func TestVerifierVerdictDoesNotStandAfterAnError(t *testing.T) {
	store, reg := testEnv(t)
	args := map[string]any{"verdict": "fixed", "justification": "the scan is clean on this checkout"}
	w := newWorker(t, store, reg, verifierProfile(), verdictThenStop(store, args, run.StopError, errors.New("boom")))
	it := findingCardOn(t, store, w, kanban.Review, "needs-verify")
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(it.ID)
	if got.List == kanban.Done || got.VEX != "" {
		t.Fatalf("a verdict closed the card after a failed turn: %+v", got)
	}
}

// publishedThenOverrun commits a change, marks it published as PublishBranch
// does, optionally leaves more work uncommitted, then keeps re-checking until the
// wall budget ends the turn.
func publishedThenOverrun(t *testing.T, more bool) TurnRunner {
	return func(ctx context.Context, tt Turn) (run.Result, error) {
		gitIn := func(args ...string) string {
			out, err := exec.Command("git", append([]string{"-C", tt.Workdir}, args...)...).CombinedOutput()
			if err != nil {
				t.Fatalf("git %v: %v %s", args, err, out)
			}
			return strings.TrimSpace(string(out))
		}
		if err := os.WriteFile(filepath.Join(tt.Workdir, "README"), []byte("fixed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitIn("commit", "-q", "-am", "fix the README")
		tt.Workspace.published = gitIn("rev-parse", "HEAD")
		if more {
			if err := os.WriteFile(filepath.Join(tt.Workdir, "NOTES"), []byte("unpublished\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		<-ctx.Done()

		return run.Result{StopReason: run.StopCancelled, Passes: 4}, ctx.Err()
	}
}

func overrunBuilder(t *testing.T, more bool) (*kanban.Store, kanban.Item) {
	store, reg := testEnv(t)
	it, _, _ := store.Add(kanban.ItemInput{Title: "fix the README", Labels: []string{"build"}}, kanban.Provenance{})
	p := builderProfile()
	p.Workspace = &agentprofile.WorkspaceSpec{Isolation: agentprofile.IsolationWorktree}
	p.Budget = &agentprofile.BudgetSpec{MaxWallPerItem: "300ms"}
	w := newWorker(t, store, reg, p, publishedThenOverrun(t, more))
	w.Repo = gitRepo(t)
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(it.ID)
	return store, got
}

// A builder that published its work and then ran out of wall time re-checking it
// has finished: the card moves on and no attempt is counted.
func TestWorkPublishedBeforeTheWallBudgetEndsStillCounts(t *testing.T) {
	_, got := overrunBuilder(t, false)
	if got.List != kanban.Review || got.Attempts != 0 || !strings.Contains(got.LastNote(), "published its work before") {
		t.Fatalf("published work was failed for the overrun: %s attempts=%d (%q)", got.List, got.Attempts, got.LastNote())
	}
}

// Work left beside what was published is not finished, so the overrun fails it.
func TestUnpublishedWorkAtTheWallBudgetStillFails(t *testing.T) {
	_, got := overrunBuilder(t, true)
	if got.List == kanban.Review || got.Attempts != 1 {
		t.Fatalf("unpublished work moved on: %s attempts=%d (%q)", got.List, got.Attempts, got.LastNote())
	}
}
