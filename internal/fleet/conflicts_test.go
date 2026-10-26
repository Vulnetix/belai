package fleet

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/builddetect"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/lintdetect"
	"github.com/vulnetix/belai/internal/mergefix"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/testdetect"
	"github.com/vulnetix/belai/internal/testrun"
)

// pinGit keeps the machine's own git configuration out of the test.
func pinGit(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_AUTHOR_NAME", "Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.invalid")
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// conflictRepo is a repository whose feature branch conflicts with main in
// two files, one hunk each, with origin naming owner/name on GitHub.
func conflictRepo(t *testing.T) string {
	pinGit(t)
	repo := gitRepo(t)
	gitIn(t, repo, "remote", "add", "origin", "https://github.com/owner/name.git")
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("list.txt", "alpha\nmiddle\nomega\n")
	write("notes.txt", "top\nx\nbottom\n")
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-q", "-m", "common")
	gitIn(t, repo, "checkout", "-q", "-b", "feature")
	write("list.txt", "alpha\nfrom-head\nomega\n")
	write("notes.txt", "top\nhead\nbottom\n")
	gitIn(t, repo, "commit", "-q", "-am", "head side")
	gitIn(t, repo, "checkout", "-q", "main")
	write("list.txt", "alpha\nfrom-base\nomega\n")
	write("notes.txt", "top\nbase\nbottom\n")
	gitIn(t, repo, "commit", "-q", "-am", "base side")
	return repo
}

const conflictCardBody = "```\npr: https://github.com/owner/name/pull/7\nrepo: owner/name\nnumber: 7\nhead: feature\nbase: main\nfork: false\n```\nResolve the merge conflicts of this pull request.\n"

// fakeConflictForge stands in for origin: the two sides are local branches,
// and a push is recorded after checking it is a fast-forward merge.
type fakeConflictForge struct {
	t        *testing.T
	repo     string
	mu       sync.Mutex
	pushed   []string
	comments []string
}

func (f *fakeConflictForge) Fetch(ctx context.Context, repo string, c mergefix.Card) (ConflictRefs, error) {
	return ConflictRefs{Head: gitIn(f.t, repo, "rev-parse", c.Head), Base: gitIn(f.t, repo, "rev-parse", c.Base)}, nil
}

func (f *fakeConflictForge) Push(ctx context.Context, ws *Workspace, c mergefix.Card, sha string) error {
	head := gitIn(f.t, f.repo, "rev-parse", c.Head)
	if err := exec.Command("git", "-C", f.repo, "merge-base", "--is-ancestor", head, sha).Run(); err != nil {
		return fmt.Errorf("not a fast-forward of %s", c.Head)
	}
	f.mu.Lock()
	f.pushed = append(f.pushed, sha)
	f.mu.Unlock()
	return nil
}

func (f *fakeConflictForge) Publish(ctx context.Context, ws *Workspace) error { return nil }

func (f *fakeConflictForge) Comment(ctx context.Context, ws *Workspace, c mergefix.Card, body string) error {
	f.mu.Lock()
	f.comments = append(f.comments, body)
	f.mu.Unlock()
	return nil
}

var (
	attemptRE  = regexp.MustCompile(`Merge conflict attempt (\d+) of`)
	strategyRE = regexp.MustCompile(`(?m)^- (\S+) hunk (\d+): (\S+)$`)
)

// resolveFile rewrites one conflicted file, choosing for each hunk.
func resolveFile(t *testing.T, dir, path string, choose func(hunk int) mergefix.Choice) {
	t.Helper()
	p := filepath.Join(dir, path)
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	f, err := mergefix.ParseFile(path, data)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for i, h := range f.Hunks {
		out = append(out, f.Common[i]...)
		out = append(out, mergefix.Resolve(h, choose(h.Index))...)
	}
	out = append(out, f.Common[len(f.Hunks)]...)
	if err := os.WriteFile(p, []byte(strings.Join(out, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeResolver is an agent turn: it reads the brief's attempt and strategy and
// resolves both files by its own rule.
type fakeResolver struct {
	t     *testing.T
	mu    sync.Mutex
	turns []Turn
	dir   string
	// pick decides each hunk from the attempt number, whether this is the turn
	// after a refused repeat, and the strategy's choice for the hunk.
	pick func(attempt int, retry bool, suggested mergefix.Choice) mergefix.Choice
}

func (r *fakeResolver) run(ctx context.Context, tt Turn) (run.Result, error) {
	r.mu.Lock()
	r.turns = append(r.turns, tt)
	r.dir = tt.Workdir
	r.mu.Unlock()
	m := attemptRE.FindStringSubmatch(tt.Conflicts)
	if m == nil {
		r.t.Fatalf("a conflict turn without its brief: %q", tt.Conflicts)
	}
	n, _ := strconv.Atoi(m[1])
	retry := strings.Contains(tt.Conflicts, "same choices as attempt")
	suggested := map[string]mergefix.Choice{}
	for _, s := range strategyRE.FindAllStringSubmatch(tt.Conflicts, -1) {
		suggested[s[1]+"#"+s[2]] = mergefix.Choice(s[3])
	}
	for _, path := range []string{"list.txt", "notes.txt"} {
		resolveFile(r.t, tt.Workdir, path, func(h int) mergefix.Choice {
			return r.pick(n, retry, suggested[path+"#"+strconv.Itoa(h+1)])
		})
	}
	return run.Result{StopReason: run.StopComplete, GoalSentinel: rolemanager.GoalComplete, Passes: 1}, nil
}

func (r *fakeResolver) workdir() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dir
}

// conflictWorker is a belai:conflict-resolver worker over repo whose build
// check fails whenever failing says so of the worktree.
func conflictWorker(t *testing.T, store *kanban.Store, reg *Registry, repo string, r *fakeResolver, failing func(dir string) bool) (*Worker, *fakeConflictForge) {
	t.Helper()
	p, err := agentprofile.Load("belai:conflict-resolver")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeConflictForge{t: t, repo: repo}
	w := &Worker{
		Profile: p, Repo: repo, Settings: offline(), Posture: posture.AllIgnore(),
		Store: store, Registry: reg, Record: Record{ID: NewID(p.Name), Profile: p.Name},
		Once: true, Runner: r.run, Log: &bytes.Buffer{}, Conflicts: f,
		Builds: func(context.Context) []builddetect.Build {
			return []builddetect.Build{{Name: "build-fake", Ecosystem: "fake", Command: []string{"fake-build"}}}
		},
		Lints:  func(context.Context) []lintdetect.Linter { return nil },
		Suites: func(context.Context) []testdetect.Suite { return []testdetect.Suite{} },
		RunTests: func(ctx context.Context, plan testrun.Plan) []testrun.Result {
			var out []testrun.Result
			dir := r.workdir()
			for i, name := range plan.Names {
				res := testrun.Result{Suite: name, Command: plan.Argvs[i], Status: testrun.Passed}
				if dir != "" && failing(dir) {
					res.Status, res.ExitCode, res.Output = testrun.Failed, 1, "notes.txt:2: does not build\n"
				}
				out = append(out, res)
			}
			return out
		},
	}
	return w, f
}

func fileHas(dir, name string, all ...string) bool {
	data, _ := os.ReadFile(filepath.Join(dir, name))
	for _, s := range all {
		if !strings.Contains(string(data), s) {
			return false
		}
	}
	return true
}

func addConflictCard(t *testing.T, store *kanban.Store, repo string) kanban.Item {
	t.Helper()
	it, _, err := store.Add(kanban.ItemInput{Title: "Resolve merge conflicts: owner/name#7", Body: conflictCardBody, Labels: []string{agentprofile.MergeConflictLabel}, Assignee: "belai:conflict-resolver"},
		kanban.ProvenanceFor(repo, "s", "h"))
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func notesOf(it kanban.Item) []string {
	var out []string
	for _, m := range it.History {
		if m.Note != "" {
			out = append(out, m.Note)
		}
	}
	return out
}

func ledgerNotes(it kanban.Item) []mergefix.Attempt {
	var out []mergefix.Attempt
	for _, n := range notesOf(it) {
		if a, ok := mergefix.ParseNote(n); ok {
			out = append(out, a)
		}
	}
	return out
}

func follow(_ int, _ bool, s mergefix.Choice) mergefix.Choice { return s }

func TestConflictLoopPassesOnTheFirstAttempt(t *testing.T) {
	store, reg := testEnv(t)
	repo := conflictRepo(t)
	it := addConflictCard(t, store, repo)
	r := &fakeResolver{t: t, pick: follow}
	w, f := conflictWorker(t, store, reg, repo, r, func(string) bool { return false })
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, _ := store.Get(it.ID)
	if after.List != kanban.Review || !slices.Equal(after.Labels, []string{"needs-review"}) {
		t.Fatalf("released to %s with %v; notes %q", after.List, after.Labels, notesOf(after))
	}
	if len(r.turns) != 1 || !strings.Contains(r.turns[0].Conflicts, "Strategy for this attempt: both everywhere.") {
		t.Fatalf("%d turns; brief %q", len(r.turns), r.turns[0].Conflicts)
	}
	led := ledgerNotes(after)
	if len(led) != 1 || led[0].N != 1 || led[0].Max != 5 || led[0].Result.Outcome != mergefix.Passed {
		t.Fatalf("ledger %+v", led)
	}
	want := regexp.MustCompile(`^conflict attempt 1/5 [0-9a-f]{8}: list\.txt=both, notes\.txt=both -> passed$`)
	if !slices.ContainsFunc(notesOf(after), want.MatchString) {
		t.Fatalf("notes %q", notesOf(after))
	}
	if len(f.pushed) != 1 {
		t.Fatalf("pushed %v", f.pushed)
	}
	sha := f.pushed[0]
	if !slices.Contains(notesOf(after), mergefix.FinalResolved(1, sha)) {
		t.Fatalf("no final note for %s in %q", sha, notesOf(after))
	}
	parents := strings.Fields(gitIn(t, repo, "rev-list", "--parents", "-n", "1", sha))
	if len(parents) != 3 || parents[1] != gitIn(t, repo, "rev-parse", "feature") || parents[2] != gitIn(t, repo, "rev-parse", "main") {
		t.Fatalf("the merge's parents %v", parents)
	}
	if body := gitIn(t, repo, "show", sha+":list.txt"); body != "alpha\nfrom-head\nfrom-base\nomega" {
		t.Fatalf("list.txt %q", body)
	}
	if msg := gitIn(t, repo, "log", "-1", "--format=%B", sha); !strings.Contains(msg, "conflict attempt 1 of 5 (strategy: both everywhere)") {
		t.Fatalf("message %q", msg)
	}
	if len(f.comments) != 1 || !strings.Contains(f.comments[0], "| 1/5 |") || !strings.Contains(f.comments[0], "as a fast-forward") {
		t.Fatalf("comments %q", f.comments)
	}
	// The card names what was pushed, the pull request's head, so a reviewer on
	// any host checks it out, not this host's attempt branch.
	if after.Branch != "feature" {
		t.Fatalf("branch %q, want the pull request's head", after.Branch)
	}

	// The review stage on another host: the head is fetched from origin (here,
	// as if origin's head is now the pushed merge) and checked out on a branch
	// of the card's own; the user's own feature branch is never checked out.
	orig := fetchPRHead
	defer func() { fetchPRHead = orig }()
	fetchPRHead = func(ctx context.Context, repo string, c mergefix.Card) (string, error) {
		if c.Head != "feature" || c.Number != 7 {
			t.Fatalf("fetched %+v", c)
		}
		return sha, nil
	}
	ws, err := PrepareWorktree(context.Background(), repo, after, "")
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Remove(context.Background())
	if ws.PRHead != "feature" || !strings.HasPrefix(ws.Branch, BranchPrefix+after.Short()+"/") || gitIn(t, ws.Dir, "rev-parse", "HEAD") != sha || ws.Base != sha {
		t.Fatalf("review worktree %+v at %s", ws, gitIn(t, ws.Dir, "rev-parse", "HEAD"))
	}
}

// Only the head of the item's own same-repository pull request may stand in
// for a belai/ branch.
func TestPrepareWorktreeRefusesOtherBranches(t *testing.T) {
	testEnv(t)
	repo := conflictRepo(t)
	orig := fetchPRHead
	defer func() { fetchPRHead = orig }()
	fetchPRHead = func(ctx context.Context, repo string, c mergefix.Card) (string, error) {
		return gitIn(t, repo, "rev-parse", c.Head), nil
	}
	id := "6f9a2c00-0000-4000-8000-000000000000"
	for name, it := range map[string]kanban.Item{
		"no block":       {ID: id, Title: "t", Branch: "feature"},
		"another branch": {ID: id, Title: "t", Branch: "main", Body: conflictCardBody},
		"a fork":         {ID: id, Title: "t", Branch: "feature", Body: strings.Replace(conflictCardBody, "fork: false", "fork: true", 1)},
	} {
		if ws, err := PrepareWorktree(context.Background(), repo, it, ""); err == nil {
			ws.Remove(context.Background())
			t.Errorf("%s: checked out %s", name, it.Branch)
		}
	}
}

// A fork's pull request is never pushed to: the attempt's branch goes to
// origin, and the card keeps that branch, which every host can fetch.
func TestConflictLoopForkKeepsThePublishedBranch(t *testing.T) {
	store, reg := testEnv(t)
	repo := conflictRepo(t)
	it, _, err := store.Add(kanban.ItemInput{Title: "Resolve merge conflicts: owner/name#7", Body: strings.Replace(conflictCardBody, "fork: false", "fork: true", 1),
		Labels: []string{agentprofile.MergeConflictLabel}}, kanban.ProvenanceFor(repo, "s", "h"))
	if err != nil {
		t.Fatal(err)
	}
	r := &fakeResolver{t: t, pick: follow}
	w, f := conflictWorker(t, store, reg, repo, r, func(string) bool { return false })
	var published []string
	w.Conflicts = &publishRecorder{fakeConflictForge: f, published: &published}
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, _ := store.Get(it.ID)
	if after.List != kanban.Review || len(f.pushed) != 0 || len(published) != 1 {
		t.Fatalf("list %s pushed %v published %v", after.List, f.pushed, published)
	}
	if after.Branch != published[0] || !strings.HasPrefix(after.Branch, BranchPrefix+after.Short()+"/a") {
		t.Fatalf("branch %q, want the published %v", after.Branch, published)
	}
	if !slices.Contains(notesOf(after), mergefix.FinalNotPushed(1, "the pull request is from a fork")) {
		t.Fatalf("notes %q", notesOf(after))
	}
	if parents := strings.Fields(gitIn(t, repo, "rev-list", "--parents", "-n", "1", after.Branch)); len(parents) != 3 {
		t.Fatalf("the published branch is not the merge: %v", parents)
	}
	if len(f.comments) != 1 || !strings.Contains(f.comments[0], "`"+after.Branch+"`") {
		t.Fatalf("comments %q", f.comments)
	}
}

// publishRecorder records the branch a fork's resolution is published as.
type publishRecorder struct {
	*fakeConflictForge
	published *[]string
}

func (p *publishRecorder) Publish(ctx context.Context, ws *Workspace) error {
	*p.published = append(*p.published, ws.Branch)
	return nil
}

// Keeping both sides of notes.txt breaks the build; the check names the file,
// so after both orders of both the harness suggests the current side there.
func TestConflictLoopTriesAnotherVariationAfterAFailure(t *testing.T) {
	store, reg := testEnv(t)
	repo := conflictRepo(t)
	it := addConflictCard(t, store, repo)
	r := &fakeResolver{t: t, pick: follow}
	w, f := conflictWorker(t, store, reg, repo, r, func(dir string) bool { return fileHas(dir, "notes.txt", "head", "base") })
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, _ := store.Get(it.ID)
	led := ledgerNotes(after)
	if after.List != kanban.Review || len(led) != 3 {
		t.Fatalf("list %s ledger %+v notes %q", after.List, led, notesOf(after))
	}
	seen := map[string]bool{}
	for i, a := range led {
		if seen[a.Fingerprint] {
			t.Fatalf("attempt %d repeats a fingerprint: %+v", a.N, led)
		}
		seen[a.Fingerprint] = true
		if i < 2 && (a.Result.Outcome != mergefix.BuildFailed || a.Result.Command != "fake-build") {
			t.Fatalf("attempt %d: %+v", a.N, a.Result)
		}
	}
	if led[2].Result.Outcome != mergefix.Passed || !slices.Contains(led[2].Files, mergefix.FileChoice{File: "notes.txt", Choice: "current"}) {
		t.Fatalf("the passing attempt %+v", led[2])
	}
	if !strings.Contains(r.turns[2].Conflicts, "Strategy for this attempt: current in notes.txt, both elsewhere.") {
		t.Fatalf("third brief %q", r.turns[2].Conflicts)
	}
	if !strings.Contains(r.turns[1].Conflicts, "conflict attempt 1/5") {
		t.Fatal("the second attempt was not told about the first")
	}
	if len(f.pushed) != 1 {
		t.Fatalf("pushed %v", f.pushed)
	}
	// Each attempt had its own fresh checkout, and the failed ones are gone.
	dirs := map[string]bool{}
	for _, tt := range r.turns {
		dirs[tt.Workdir] = true
		if _, err := os.Stat(tt.Workdir); err == nil {
			t.Errorf("worktree %s was left behind", tt.Workdir)
		}
	}
	if len(dirs) != 3 {
		t.Fatalf("%d worktrees for 3 attempts", len(dirs))
	}
}

// A resolution that repeats an earlier attempt is refused once with a second
// turn in the same worktree, which does not use up the attempt.
func TestConflictLoopRefusesARepeatedFingerprint(t *testing.T) {
	store, reg := testEnv(t)
	repo := conflictRepo(t)
	it := addConflictCard(t, store, repo)
	r := &fakeResolver{t: t, pick: func(n int, retry bool, s mergefix.Choice) mergefix.Choice {
		if n > 1 && !retry {
			return mergefix.Both // stubborn: the first turn of each attempt repeats attempt 1
		}
		return s
	}}
	w, _ := conflictWorker(t, store, reg, repo, r, func(dir string) bool { return fileHas(dir, "notes.txt", "head", "base") })
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, _ := store.Get(it.ID)
	led := ledgerNotes(after)
	if after.List != kanban.Review || len(led) != 3 {
		t.Fatalf("list %s ledger %+v", after.List, led)
	}
	for _, a := range led {
		if a.Result.Outcome == mergefix.Repeat {
			t.Fatalf("a single repeat used up an attempt: %+v", led)
		}
	}
	if len(r.turns) != 5 {
		t.Fatalf("%d turns, want 5 (one, then two per later attempt)", len(r.turns))
	}
	if r.turns[1].Workdir != r.turns[2].Workdir || !strings.Contains(r.turns[2].Conflicts, "same choices as attempt 1") {
		t.Fatalf("the refused repeat was not answered in the same worktree: %q", r.turns[2].Conflicts)
	}
}

// Five failed checkouts end the series: the card is blocked and held for a
// person, a second repeat is recorded as one, and nothing is pushed.
func TestConflictLoopBlocksAfterFiveFailures(t *testing.T) {
	store, reg := testEnv(t)
	repo := conflictRepo(t)
	it := addConflictCard(t, store, repo)
	r := &fakeResolver{t: t, pick: func(n int, retry bool, s mergefix.Choice) mergefix.Choice {
		if n == 2 {
			return mergefix.Both // repeats attempt 1 on both turns
		}
		return s
	}}
	w, f := conflictWorker(t, store, reg, repo, r, func(string) bool { return true })
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, _ := store.Get(it.ID)
	if after.List != kanban.Blocked || !slices.Contains(after.Labels, kanban.CheckpointLabel(kanban.CheckpointConflictsUnresolved)) {
		t.Fatalf("list %s labels %v", after.List, after.Labels)
	}
	if after.Labels[0] != "checkpoint:conflicts-unresolved" && !slices.Contains(after.Labels, "checkpoint:conflicts-unresolved") {
		t.Fatal(after.Labels)
	}
	led := ledgerNotes(after)
	if len(led) != 5 {
		t.Fatalf("ledger %+v", led)
	}
	if led[1].Result != (mergefix.Result{Outcome: mergefix.Repeat, RepeatOf: 1}) {
		t.Fatalf("attempt 2 %+v", led[1])
	}
	fps := map[string]bool{}
	for _, a := range led {
		if a.Result.Outcome != mergefix.Repeat {
			if fps[a.Fingerprint] {
				t.Fatalf("a fingerprint was tried twice: %+v", led)
			}
			fps[a.Fingerprint] = true
		}
	}
	if !slices.Contains(notesOf(after), mergefix.FinalUnresolved(5)) {
		t.Fatalf("notes %q", notesOf(after))
	}
	if len(f.pushed) != 0 || len(f.comments) != 0 {
		t.Fatalf("pushed %v commented %v", f.pushed, f.comments)
	}
	// No worker claims a held card.
	if _, err := store.Claim(kanban.ClaimRequest{Profile: "belai:conflict-resolver", Worker: "w2", Lists: []kanban.List{kanban.Blocked}, Labels: []string{agentprofile.MergeConflictLabel}, Lease: 60e9}); err == nil {
		t.Fatal("a held card was claimed")
	}
	// The ledger file holds the series for the next claim.
	dir, _ := conflictLedgerDir()
	l, ok := mergefix.ReadLedger(dir, it.ID)
	if !ok || len(l.Attempts) != 5 || l.Baseline == nil {
		t.Fatalf("ledger file %+v %v", l, ok)
	}
}

// A person sends an unresolved card back: the next claim starts round 2,
// numbered from 1 again, and never repeats a fingerprint round 1 tried.
func TestConflictLoopRetryStartsANewRound(t *testing.T) {
	store, reg := testEnv(t)
	repo := conflictRepo(t)
	it := addConflictCard(t, store, repo)
	r1 := &fakeResolver{t: t, pick: follow}
	w1, _ := conflictWorker(t, store, reg, repo, r1, func(string) bool { return true })
	if err := w1.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	held, _ := store.Get(it.ID)
	if held.List != kanban.Blocked || !slices.Contains(notesOf(held), mergefix.FinalUnresolved(5)) {
		t.Fatalf("round 1 ended %s: %q", held.List, notesOf(held))
	}
	// While it is held, no worker takes it.
	again := &fakeResolver{t: t, pick: follow}
	wHeld, _ := conflictWorker(t, store, reg, repo, again, func(string) bool { return false })
	if err := wHeld.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(again.turns) != 0 {
		t.Fatal("a held card was worked")
	}

	// The person's decision: clear the checkpoint and send it to the backlog.
	if _, err := store.Decide(it.ID, kanban.Decision{Back: kanban.Backlog, Note: "try again"}, "person"); err != nil {
		t.Fatal(err)
	}
	r2 := &fakeResolver{t: t, pick: follow}
	w2, f2 := conflictWorker(t, store, reg, repo, r2, func(string) bool { return len(r2.turns) < 3 })
	if err := w2.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, _ := store.Get(it.ID)
	if after.List != kanban.Review || len(f2.pushed) != 1 || slices.ContainsFunc(after.Labels, func(l string) bool { return strings.HasPrefix(l, kanban.CheckpointPrefix) }) {
		t.Fatalf("round 2 ended %s %v, pushed %v", after.List, after.Labels, f2.pushed)
	}
	notes := notesOf(after)
	retryAt := slices.Index(notes, mergefix.RetryNote(2))
	if retryAt < 0 || retryAt < slices.Index(notes, mergefix.FinalUnresolved(5)) {
		t.Fatalf("no retry note after the unresolved one: %q", notes)
	}
	var round1, round2 []mergefix.Attempt
	for i, n := range notes {
		if a, ok := mergefix.ParseNote(n); ok {
			if i < retryAt {
				round1 = append(round1, a)
			} else {
				round2 = append(round2, a)
			}
		}
	}
	if len(round1) != 5 || len(round2) != 3 {
		t.Fatalf("round 1 %d attempts, round 2 %d", len(round1), len(round2))
	}
	tried := map[string]bool{}
	for _, a := range round1 {
		tried[a.Fingerprint] = true
	}
	for i, a := range round2 {
		if a.N != i+1 || a.Max != 5 {
			t.Fatalf("round 2 numbering %+v", round2)
		}
		if tried[a.Fingerprint] {
			t.Fatalf("round 2 attempt %d repeats a fingerprint of an earlier attempt: %+v %+v", a.N, round1, round2)
		}
		tried[a.Fingerprint] = true
	}
	brief := r2.turns[0].Conflicts
	if !strings.Contains(brief, "Merge conflict attempt 1 of 5") || !strings.Contains(brief, "This is round 2") || !strings.Contains(brief, round1[0].Fingerprint) {
		t.Fatalf("round 2's first brief %q", brief)
	}
	dir, _ := conflictLedgerDir()
	if l, ok := mergefix.ReadLedger(dir, it.ID); !ok || l.Round != 2 || len(l.Tried) != 8 {
		t.Fatalf("ledger %+v %v", l, ok)
	}
}

// A card naming another repository than the worker's origin is blocked
// before anything is fetched.
func TestConflictLoopRefusesAnotherRepository(t *testing.T) {
	store, reg := testEnv(t)
	repo := conflictRepo(t)
	gitIn(t, repo, "remote", "set-url", "origin", "https://github.com/other/repo.git")
	it, _, _ := store.Add(kanban.ItemInput{Title: "Resolve", Body: conflictCardBody, Labels: []string{agentprofile.MergeConflictLabel}}, kanban.ProvenanceFor(repo, "s", "h"))
	r := &fakeResolver{t: t, pick: follow}
	w, _ := conflictWorker(t, store, reg, repo, r, func(string) bool { return false })
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, _ := store.Get(it.ID)
	if after.List != kanban.Blocked || len(r.turns) != 0 || !strings.Contains(after.LastNote(), "the card names owner/name") {
		t.Fatalf("list %s turns %d note %q", after.List, len(r.turns), after.LastNote())
	}
}
