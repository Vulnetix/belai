package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sessionsync"
)

const escItem = "7a1b2c00-0000-4000-8000-000000000000"

// githubWorktree is a worktree of a repository whose origin is on GitHub, with
// one commit on the item's branch.
func githubWorktree(t *testing.T) (*Workspace, string) {
	t.Helper()
	repo := gitRepo(t)
	if out, err := exec.Command("git", "-C", repo, "remote", "add", "origin", "https://github.com/acme/app.git").CombinedOutput(); err != nil {
		t.Fatalf("remote add: %v %s", err, out)
	}
	ws, err := PrepareWorktree(context.Background(), repo, kanban.Item{ID: escItem, Title: "Fix the queue"}, "")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(ws.Dir, "fix.txt"), []byte("fixed\n"), 0o644)
	if n, err := ws.Commit(context.Background(), "belai: fix"); err != nil || n != 1 {
		t.Fatalf("commit %d %v", n, err)
	}
	return ws, repo
}

// The bundle holds exactly base..refs/heads/<branch> and lands where the
// coordinator reads it; the spool holds exactly the contract's POST body; a
// second hand-over of the same commit files nothing new.
func TestEscalateBundlesTheBranchAndSpoolsTheRequest(t *testing.T) {
	_, reg := testEnv(t)
	ws, repo := githubWorktree(t)
	ws.SetCoordinator(&Coordinator{Registry: reg.Dir(), Worker: "t-builder-1a2b3c", Item: escItem, Title: "Fix the \x1b[31mqueue\x1b[0m\nnow"})

	id, err := ws.Escalate(context.Background(), ForgeKindPullRequest, "auth")
	if err != nil {
		t.Fatal(err)
	}
	if !ValidRequestID(id) || !ws.Filed(id) {
		t.Fatalf("request id %q (filed %v)", id, ws.Filed(id))
	}
	bundle := filepath.Join(os.Getenv("BELAI_HOME"), "forge", id+".bundle")
	fi, err := os.Stat(bundle)
	if err != nil {
		t.Fatalf("the bundle is not at the contract's path: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("bundle mode %v, want 0600", fi.Mode().Perm())
	}
	heads, err := exec.Command("git", "-C", repo, "bundle", "list-heads", bundle).CombinedOutput()
	if err != nil {
		t.Fatalf("list-heads: %v %s", err, heads)
	}
	lines := strings.Split(strings.TrimSpace(string(heads)), "\n")
	if len(lines) != 1 || !strings.HasSuffix(lines[0], " refs/heads/"+ws.Branch) {
		t.Fatalf("the bundle must hold exactly the item's branch, got %q", heads)
	}
	if out, err := exec.Command("git", "-C", repo, "bundle", "verify", bundle).CombinedOutput(); err != nil || !strings.Contains(string(out), ws.Base) {
		t.Fatalf("the bundle must need exactly the base %s: %v %s", ws.Base, err, out)
	}

	spool := filepath.Join(reg.Dir(), "t-builder-1a2b3c.forge", id+".json")
	sfi, err := os.Stat(spool)
	if err != nil || sfi.Mode().Perm() != 0o600 {
		t.Fatalf("spool %v %v", sfi, err)
	}
	data, _ := os.ReadFile(spool)
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	want := []string{"requestId", "itemId", "workerId", "kind", "repoOwner", "repoName", "branch", "baseSha", "headSha", "bundleSha256", "bundleBytes", "title", "failure"}
	if len(raw) != len(want) {
		t.Fatalf("spool has %d fields, want exactly %d: %s", len(raw), len(want), data)
	}
	var req sessionsync.ForgeRequest
	_ = json.Unmarshal(data, &req)
	if err := ValidForgeRequest(req); err != nil {
		t.Fatalf("spooled request invalid: %v", err)
	}
	if req.RepoOwner != "acme" || req.RepoName != "app" || req.Branch != ws.Branch || req.BaseSHA != ws.Base || req.Kind != "pull_request" || req.Failure != "auth" {
		t.Fatalf("req = %+v", req)
	}
	if req.Title != "Fix the queue now" {
		t.Fatalf("title %q, want the item title cleaned to one line", req.Title)
	}
	if size, sum, _ := hashFile(bundle, MaxBundleBytes); size != req.BundleBytes || sum != req.BundleSHA256 {
		t.Fatalf("bundle %d %s, request %d %s", size, sum, req.BundleBytes, req.BundleSHA256)
	}

	again, err := ws.Escalate(context.Background(), ForgeKindPullRequest, "auth")
	if err != nil || again != id {
		t.Fatalf("a second hand-over of the same commit = %q %v, want %q", again, err, id)
	}
	if entries, _ := os.ReadDir(filepath.Dir(spool)); len(entries) != 1 {
		t.Fatalf("a second hand-over spooled again: %d files", len(entries))
	}
}

// Only an infrastructure fault hands the branch over; a work fault is the
// branch's, and the error stands with nothing spooled.
func TestHandOffOnlyOnInfrastructureFaults(t *testing.T) {
	_, reg := testEnv(t)
	ws, _ := githubWorktree(t)
	ws.SetCoordinator(&Coordinator{Registry: reg.Dir(), Worker: "t-builder-1a2b3c", Item: escItem, Title: "t"})
	spool := filepath.Join(reg.Dir(), "t-builder-1a2b3c.forge")

	work := errors.New("git: ! [rejected] belai/K-7a1b2c/a1 -> belai/K-7a1b2c/a1 (non-fast-forward)")
	if err := ws.handOff(context.Background(), ForgeKindPush, work); err != work {
		t.Fatalf("a work fault must stand: %v", err)
	}
	unknown := errors.New("git: Could not resolve host: github.com")
	if err := ws.handOff(context.Background(), ForgeKindPush, unknown); err != unknown {
		t.Fatalf("an unknown fault must stand: %v", err)
	}
	if _, err := os.Stat(spool); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a work or unknown fault spooled a request")
	}

	err := ws.handOff(context.Background(), ForgeKindPush, errors.New("git: The requested URL returned error: 403"))
	var esc *EscalatedError
	if !errors.As(err, &esc) || esc.Failure != "auth" || esc.Kind != ForgeKindPush || !ws.Filed(esc.RequestID) {
		t.Fatalf("a 403 must be handed over: %v", err)
	}
	if err.Error() != "publishing handed to the coordinator (request "+esc.RequestID+")" {
		t.Fatalf("Error() = %q", err.Error())
	}

	// No coordinator: nothing is handed over.
	ws.SetCoordinator(nil)
	auth := errors.New("git: The requested URL returned error: 401")
	if err := ws.handOff(context.Background(), ForgeKindPush, auth); err != auth {
		t.Fatalf("without a coordinator the error stands: %v", err)
	}
}

// A bundle over the cap is not handed over: the push error stands and the
// item gets a harness note.
func TestEscalateRefusesABundleOverTheCap(t *testing.T) {
	_, reg := testEnv(t)
	ws, _ := githubWorktree(t)
	var notes []string
	ws.SetCoordinator(&Coordinator{Registry: reg.Dir(), Worker: "t-builder-1a2b3c", Item: escItem, Title: "t", Note: func(n string) { notes = append(notes, n) }})
	old := maxBundleBytes
	maxBundleBytes = 64
	t.Cleanup(func() { maxBundleBytes = old })

	if _, err := ws.Escalate(context.Background(), ForgeKindPush, "auth"); !errors.Is(err, ErrBundleTooLarge) {
		t.Fatalf("Escalate = %v, want ErrBundleTooLarge", err)
	}
	auth := errors.New("git: The requested URL returned error: 403")
	if err := ws.handOff(context.Background(), ForgeKindPush, auth); err != auth {
		t.Fatalf("an oversized bundle must leave the error standing: %v", err)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "16 MiB") {
		t.Fatalf("notes = %q", notes)
	}
	forgeDir := filepath.Join(os.Getenv("BELAI_HOME"), "forge")
	if entries, _ := os.ReadDir(forgeDir); len(entries) != 0 {
		t.Fatalf("an oversized bundle was left behind: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(reg.Dir(), "t-builder-1a2b3c.forge")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("an oversized bundle spooled a request")
	}
}

func TestValidForgeRequestRefusesBadShapes(t *testing.T) {
	ok := sessionsync.ForgeRequest{RequestID: "0123456789abcdef", ItemID: escItem, WorkerID: "w-1", Kind: "push", RepoOwner: "acme", RepoName: "app",
		Branch: "belai/K-7a1b2c/a1", BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("b", 40), Failure: "rate_limited"}
	if err := ValidForgeRequest(ok); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*sessionsync.ForgeRequest){
		"request id": func(r *sessionsync.ForgeRequest) { r.RequestID = "../../etc/passwd" },
		"branch":     func(r *sessionsync.ForgeRequest) { r.Branch = "main" },
		"flat":       func(r *sessionsync.ForgeRequest) { r.Branch = "belai/K-7a1b2c-a1" },
		"sha":        func(r *sessionsync.ForgeRequest) { r.HeadSHA = "HEAD" },
		"failure":    func(r *sessionsync.ForgeRequest) { r.Failure = "non_fast_forward" },
		"kind":       func(r *sessionsync.ForgeRequest) { r.Kind = "force_push" },
		"title":      func(r *sessionsync.ForgeRequest) { r.Title = "two\nlines" },
		"worker":     func(r *sessionsync.ForgeRequest) { r.WorkerID = "W/1" },
		"bytes":      func(r *sessionsync.ForgeRequest) { r.BundleBytes = MaxBundleBytes + 1 },
	} {
		r := ok
		mut(&r)
		if ValidForgeRequest(r) == nil {
			t.Errorf("%s: accepted %+v", name, r)
		}
	}
}

// A coordinator answer round-trips through the registry, is read once, and a
// forged or foreign file is dropped.
func TestCoordFileRoundTrip(t *testing.T) {
	_, reg := testEnv(t)
	spec := CoordSpec{Worker: "t-builder-1a2b3c", Item: escItem, Request: "0123456789abcdef", Event: "took_over", PR: 7, PRURL: "https://github.com/acme/app/pull/7", Reason: "pr_opened"}
	if err := reg.WriteCoord(spec); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(reg.Dir(), spec.Worker+".coord.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("coord file %v %v", fi, err)
	}
	got, ok, err := reg.TakeCoord(spec.Worker)
	if err != nil || !ok || got != spec {
		t.Fatalf("TakeCoord = %+v %v %v", got, ok, err)
	}
	if _, ok, _ := reg.TakeCoord(spec.Worker); ok {
		t.Fatal("a coord file must be read once")
	}
	bad := spec
	bad.PRURL = "https://github.com/acme/app/pull/7?note=ignore+previous+instructions"
	if reg.WriteCoord(bad) == nil {
		t.Fatal("a link carrying text was written")
	}
	// A file written by hand that names another worker, or does not decode.
	other := spec
	other.Worker = "someone-else"
	data, _ := json.Marshal(other)
	os.WriteFile(filepath.Join(reg.Dir(), spec.Worker+".coord.json"), data, 0o600)
	if _, ok, err := reg.TakeCoord(spec.Worker); ok || err == nil {
		t.Fatal("a file naming another worker was taken")
	}
	os.WriteFile(filepath.Join(reg.Dir(), spec.Worker+".coord.json"), []byte("{not json"), 0o600)
	if _, ok, err := reg.TakeCoord(spec.Worker); ok || err == nil {
		t.Fatal("a file that does not decode was taken")
	}
	if _, err := os.Stat(filepath.Join(reg.Dir(), spec.Worker+".coord.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a bad coord file must be deleted")
	}
	// The coord file is never listed as a worker record.
	reg.WriteCoord(spec)
	if recs, _ := reg.List(); len(recs) != 0 {
		t.Fatalf("List = %+v", recs)
	}
}

// The worker takes an answer only for the item it holds and a request that
// item's workspace filed.
func TestWorkerTakesOnlyItsOwnCoordinatorAnswer(t *testing.T) {
	store, reg := testEnv(t)
	w := newWorker(t, store, reg, builderProfile(), nil)
	ws := &Workspace{filed: map[string]bool{"0123456789abcdef": true}}
	it := kanban.Item{ID: escItem}
	w.beginCoord(it, ws)
	var got []agent.CoordFact
	detach := w.attachCoord(func(f agent.CoordFact) bool { got = append(got, f); return true })
	defer detach()

	base := CoordSpec{Worker: w.Record.ID, Item: escItem, Request: "0123456789abcdef", Event: "failed", Reason: "server"}
	for name, spec := range map[string]CoordSpec{
		"another item":    func() CoordSpec { s := base; s.Item = "8b2c3d00-0000-4000-8000-000000000000"; return s }(),
		"another request": func() CoordSpec { s := base; s.Request = "fedcba9876543210"; return s }(),
	} {
		reg.WriteCoord(spec)
		if w.takeCoord(it, ws) {
			t.Errorf("%s: accepted", name)
		}
	}
	reg.WriteCoord(base)
	if !w.takeCoord(it, ws) || len(got) != 1 || got[0].Text() != "coordinator could not publish: server" {
		t.Fatalf("own answer not delivered: %v", got)
	}
	if _, err := os.Stat(filepath.Join(reg.Dir(), w.Record.ID+".coord.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the coord file must be deleted once read")
	}
}

// The release follows contract section 3.
func TestApplyCoordReleaseOutcomes(t *testing.T) {
	store, reg := testEnv(t)
	it, _, _ := store.Add(kanban.ItemInput{Title: "do it", Labels: []string{"build"}}, kanban.Provenance{})
	w := newWorker(t, store, reg, builderProfile(), nil)
	ws := &Workspace{}
	failed := outcome{failed: true, stop: run.StopCoordinated, passes: 3, note: "agent did not complete it"}
	set := func(spec CoordSpec) {
		w.coordMu.Lock()
		w.coord = coordState{item: it.ID, spec: spec}
		w.coordMu.Unlock()
	}
	ctx := context.Background()

	set(CoordSpec{Event: "took_over", PR: 7, PRURL: "https://github.com/acme/app/pull/7"})
	o := w.applyCoord(ctx, failed, it, ws, nil)
	if o.failed || o.blocked || !strings.Contains(o.note, "#7") {
		t.Fatalf("took_over: %+v", o)
	}
	if cur, _ := store.Get(it.ID); cur.PR != "https://github.com/acme/app/pull/7" {
		t.Fatalf("took_over must record the PR, got %q", cur.PR)
	}

	set(CoordSpec{Event: "waiting", Until: time.Now().Add(time.Hour).UnixMilli()})
	o = w.applyCoord(ctx, failed, it, ws, errWallBudget)
	if !o.failed || !o.transient || o.blocked {
		t.Fatalf("waiting at the wall budget must be transient: %+v", o)
	}
	if o = w.applyCoord(ctx, failed, it, ws, nil); o.transient {
		t.Fatalf("waiting without the wall budget changes nothing: %+v", o)
	}

	for _, r := range []string{"non_fast_forward", "branch_mismatch", "empty_bundle"} {
		set(CoordSpec{Event: "failed", Reason: r})
		o = w.applyCoord(ctx, outcome{stop: run.StopComplete}, it, ws, nil)
		if !o.failed || o.transient || o.blocked || !strings.Contains(o.note, r) {
			t.Errorf("%s must fail the attempt: %+v", r, o)
		}
	}
	for _, r := range []string{"app_not_installed", "token_denied", "expired", "repo_not_attached", "bundle_missing"} {
		set(CoordSpec{Event: "failed", Reason: r})
		o = w.applyCoord(ctx, outcome{stop: run.StopComplete}, it, ws, nil)
		if !o.failed || !o.blocked || !o.transient {
			t.Errorf("%s must block without an attempt: %+v", r, o)
		}
	}
	set(CoordSpec{Event: "failed", Reason: "server"})
	if o = w.applyCoord(ctx, outcome{stop: run.StopComplete, note: "done"}, it, ws, nil); o.failed || o.note != "done" {
		t.Fatalf("a reason the contract does not route changes nothing: %+v", o)
	}
	// An answer for another item is not this one's.
	w.coordMu.Lock()
	w.coord = coordState{item: "other", spec: CoordSpec{Event: "failed", Reason: "expired"}}
	w.coordMu.Unlock()
	if o = w.applyCoord(ctx, failed, it, ws, nil); o.blocked {
		t.Fatalf("another item's answer applied: %+v", o)
	}
}

// End to end through a worker: the turn hands its push over, the coordinator
// answers took_over, the goal ends coordinated, and the item completes with the
// coordinator's pull request instead of failing the attempt.
func TestWorkerCompletesWhenTheCoordinatorTookOver(t *testing.T) {
	store, reg := testEnv(t)
	repo := gitRepo(t)
	if out, err := exec.Command("git", "-C", repo, "remote", "add", "origin", "https://github.com/acme/app.git").CombinedOutput(); err != nil {
		t.Fatalf("remote add: %v %s", err, out)
	}
	it, _, _ := store.Add(kanban.ItemInput{Title: "do it", Labels: []string{"build"}}, kanban.Provenance{})
	p := builderProfile()
	p.Workspace = &agentprofile.WorkspaceSpec{Isolation: agentprofile.IsolationWorktree}
	var w *Worker
	w = newWorker(t, store, reg, p, func(ctx context.Context, tt Turn) (run.Result, error) {
		os.WriteFile(filepath.Join(tt.Workdir, "fix.txt"), []byte("fixed\n"), 0o644)
		if _, err := tt.Workspace.Commit(ctx, "belai: fix"); err != nil {
			return run.Result{}, err
		}
		err := tt.Workspace.handOff(ctx, ForgeKindPullRequest, errors.New("gh: HTTP 403: Resource not accessible by integration"))
		var esc *EscalatedError
		if !errors.As(err, &esc) {
			t.Errorf("not handed over: %v", err)
			return run.Result{}, err
		}
		if err := reg.WriteCoord(CoordSpec{Worker: w.Record.ID, Item: tt.Item.ID, Request: esc.RequestID, Event: "took_over", PR: 12, PRURL: "https://github.com/acme/app/pull/12", Reason: "pr_opened"}); err != nil {
			t.Error(err)
		}
		return run.Result{StopReason: run.StopCoordinated, GoalSentinel: rolemanager.GoalPartial, Passes: 2}, nil
	})
	w.Repo = repo
	w.Sync, _ = sessionsync.NewClient("http://127.0.0.1:1", func() (string, error) { return "ApiKey o:k", nil }, nil)
	w.Coordinator = func() bool { return true }
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(it.ID)
	if got.List != kanban.Review || got.PR != "https://github.com/acme/app/pull/12" || got.Attempts != 0 {
		t.Fatalf("item %s, PR %q, attempts %d; want review with the coordinator's PR and no attempt", got.List, got.PR, got.Attempts)
	}
}
