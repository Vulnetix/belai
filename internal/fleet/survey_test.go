package fleet

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/repoindex"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tools"
)

func scoutProfile() agentprofile.AgentProfile {
	return agentprofile.AgentProfile{
		Name: "t-scout", Description: "d", SystemPrompt: "sp", Mode: agentprofile.ModeWorker,
		Tools: []string{"Read"},
		Kanban: &agentprofile.KanbanSpec{
			Labels:        []string{"scout"},
			OnSuccess:     agentprofile.Route{List: "done", DropLabels: []string{"scout"}},
			HandoffLabels: []string{"build"},
			Poll:          "10ms",
			Project:       "all",
			Survey:        &agentprofile.SurveySpec{Title: "Survey {project} for work", Body: "find work"},
		},
	}
}

// handoffRunner files one handoff, asking for backlog, under the claim.
func handoffRunner(store *kanban.Store, turns *[]Turn) TurnRunner {
	return func(ctx context.Context, tt Turn) (run.Result, error) {
		*turns = append(*turns, tt)
		h := tools.KanbanHandoff{KanbanBase: tools.KanbanBase{Store: store, Source: kanban.NewSource(kanban.Provenance{}), Claim: tt.Claim}}
		if _, err := h.Execute(ctx, map[string]any{"title": "Fix the doc for -max", "list": "backlog", "labels": []any{"build"}}); err != nil {
			return run.Result{}, err
		}
		return complete(ctx, tt)
	}
}

func surveyItems(t *testing.T, store *kanban.Store) []kanban.Item {
	t.Helper()
	out, err := store.Search(kanban.Query{Labels: []string{agentprofile.SurveyLabel}, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// With nothing to claim, a surveying worker files a dated survey item,
// works it, and every handoff lands in review whatever the model asked.
// It surveys once, then exits after the quiet window.
func TestWorkerSurveysWhenTheBoardIsEmpty(t *testing.T) {
	store, reg := testEnv(t)
	var turns []Turn
	w := newWorker(t, store, reg, scoutProfile(), handoffRunner(store, &turns))
	w.Once = false
	rec, _ := runFor(t, w, 5*time.Second)
	if len(turns) != 1 {
		t.Fatalf("%d turns, want one survey", len(turns))
	}
	if !strings.HasPrefix(rec.Reason, "done: nothing left to claim") {
		t.Fatalf("record %+v", rec)
	}
	sv := surveyItems(t, store)
	if len(sv) != 1 {
		t.Fatalf("survey items %+v", sv)
	}
	date := time.Now().Format("2006-01-02")
	if !strings.HasPrefix(sv[0].Title, "Survey ") || !strings.HasSuffix(sv[0].Title, "for work ("+date+")") || strings.Contains(sv[0].Title, "{project}") {
		t.Fatalf("survey title %q", sv[0].Title)
	}
	if sv[0].List != kanban.Done || sv[0].Body != "find work" {
		t.Fatalf("survey item %+v", sv[0])
	}
	if turns[0].Claim.HandoffList != kanban.Review {
		t.Fatalf("claim handoff list %q", turns[0].Claim.HandoffList)
	}
	handed := turns[0].Claim.HandedOff()
	if len(handed) != 1 {
		t.Fatalf("handoffs %v", handed)
	}
	h, _ := store.Get(handed[0])
	if h.List != kanban.Review || !slices.Equal(h.Labels, []string{"build"}) || h.Parent != sv[0].ID {
		t.Fatalf("handoff %+v", h)
	}
}

// A survey is at most once per kanban.survey.every for the same repository
// on the same machine: a restarted worker skips it, and surveys again once
// the interval has passed.
func TestWorkerSurveysAtMostOncePerInterval(t *testing.T) {
	store, reg := testEnv(t)
	var turns []Turn
	first := newWorker(t, store, reg, scoutProfile(), handoffRunner(store, &turns))
	first.Once = false
	runFor(t, first, 5*time.Second)

	second := newWorker(t, store, reg, scoutProfile(), handoffRunner(store, &turns))
	second.Once, second.Repo = false, first.Repo
	runFor(t, second, 5*time.Second)
	if len(turns) != 1 || len(surveyItems(t, store)) != 1 {
		t.Fatalf("restart surveyed again: %d turns", len(turns))
	}
	if !strings.Contains(second.Log.(*bytes.Buffer).String(), "survey skipped") {
		t.Fatalf("log %q", second.Log.(*bytes.Buffer).String())
	}

	later := time.Now().Add(25 * time.Hour)
	third := newWorker(t, store, reg, scoutProfile(), handoffRunner(store, &turns))
	third.Once, third.Repo, third.now = false, first.Repo, func() time.Time { return later }
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = third.Run(ctx)
	if len(turns) != 2 || len(surveyItems(t, store)) != 2 {
		t.Fatalf("after the interval: %d turns, %d survey items", len(turns), len(surveyItems(t, store)))
	}
}

// Filed work comes first: a survey happens only when nothing is claimable,
// and never for -once or -item.
func TestWorkerSurveysOnlyWhenIdle(t *testing.T) {
	store, reg := testEnv(t)
	req, _, _ := store.Add(kanban.ItemInput{Title: "survey internal/foo", Labels: []string{"scout"}}, kanban.Provenance{})
	var turns []Turn
	w := newWorker(t, store, reg, scoutProfile(), handoffRunner(store, &turns))
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || turns[0].Item.ID != req.ID || turns[0].Claim.HandoffList != "" {
		t.Fatalf("turns %+v", turns)
	}
	if h, _ := store.Get(turns[0].Claim.HandedOff()[0]); h.List != kanban.Backlog {
		t.Fatalf("a requested scout's handoff went to %s", h.List)
	}

	once := newWorker(t, store, reg, scoutProfile(), handoffRunner(store, &turns))
	if err := once.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(surveyItems(t, store)) != 0 || len(turns) != 1 {
		t.Fatalf("-once surveyed: %d turns", len(turns))
	}
}

// A profile without kanban.survey never files its own work.
func TestWorkerWithoutSurveyFilesNothing(t *testing.T) {
	store, reg := testEnv(t)
	p := scoutProfile()
	p.Kanban.Survey = nil
	var turns []Turn
	w := newWorker(t, store, reg, p, handoffRunner(store, &turns))
	w.Once = false
	runFor(t, w, 5*time.Second)
	if len(turns) != 0 || len(surveyItems(t, store)) != 0 {
		t.Fatalf("%d turns", len(turns))
	}
}

func branches(t *testing.T, repo string) []string {
	t.Helper()
	out, err := exec.Command("git", "-C", repo, "branch", "--list", "belai/*", "--format=%(refname:short)").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(out))
}

// A read-only worker runs its checks in a worktree, but what the checks
// leave behind is not committed, and the worktree and branch are discarded.
func TestReadOnlyWorkerCommitsNothing(t *testing.T) {
	store, reg := testEnv(t)
	it, _, _ := store.Add(kanban.ItemInput{Title: "survey the tests", Labels: []string{"scout"}}, kanban.Provenance{})
	p := scoutProfile()
	p.Workspace = &agentprofile.WorkspaceSpec{Isolation: agentprofile.IsolationWorktree, ReadOnly: true}
	var dir string
	w := newWorker(t, store, reg, p, func(ctx context.Context, tt Turn) (run.Result, error) {
		dir = tt.Workdir
		if !strings.Contains(tt.Workspace.Branch, "belai/") {
			t.Errorf("workspace %+v", tt.Workspace)
		}
		_ = os.WriteFile(filepath.Join(tt.Workdir, "coverage.out"), []byte("mode: set\n"), 0o644)
		return complete(ctx, tt)
	})
	w.Repo = gitRepo(t)
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, _ := store.Get(it.ID)
	if after.List != kanban.Done || strings.Contains(after.LastNote(), "files changed") || strings.Contains(after.LastNote(), "belai/") {
		t.Fatalf("released to %s with note %q", after.List, after.LastNote())
	}
	if b := branches(t, w.Repo); len(b) != 0 {
		t.Fatalf("branches left behind: %v", b)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("worktree %s still there: %v", dir, err)
	}
}

// Discard deletes an empty worker branch but keeps one with work on it.
func TestDiscardKeepsBranchesWithWork(t *testing.T) {
	store, reg := testEnv(t)
	for _, work := range []bool{false, true} {
		it, _, _ := store.Add(kanban.ItemInput{Title: "change " + map[bool]string{false: "nothing", true: "something"}[work], Labels: []string{"build"}}, kanban.Provenance{})
		p := builderProfile()
		p.Workspace = &agentprofile.WorkspaceSpec{Isolation: agentprofile.IsolationWorktree}
		w := newWorker(t, store, reg, p, func(ctx context.Context, tt Turn) (run.Result, error) {
			if work {
				_ = os.WriteFile(filepath.Join(tt.Workdir, "new.txt"), []byte("x\n"), 0o644)
			}
			return complete(ctx, tt)
		})
		w.Item = it.ID
		w.Repo = gitRepo(t)
		if err := w.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		if b := branches(t, w.Repo); (len(b) == 1) != work {
			t.Fatalf("work=%v: branches %v", work, b)
		}
	}
}

func TestReadOnlyDirective(t *testing.T) {
	ws := &Workspace{Worktree: true, Branch: "belai/K-1/a1", Base: "0123456789abcdef"}
	d := readOnlyDirective(ws)
	if !strings.Contains(d, "0123456789ab,") || !strings.Contains(d, "Do not edit") || strings.Contains(d, "harness commits") {
		t.Fatalf("directive %q", d)
	}
	if readOnlyDirective(SharedWorkspace("/x")) != "" || readOnlyDirective(nil) != "" {
		t.Fatal("a directive without a worktree")
	}
	w := &Worker{Profile: scoutProfile()}
	w.Profile.Workspace = &agentprofile.WorkspaceSpec{Isolation: agentprofile.IsolationWorktree, ReadOnly: true}
	if w.directive(ws, true) != d {
		t.Fatal("a read-only worker got the writing directive")
	}
}

func hourlyProfile() agentprofile.AgentProfile {
	p := scoutProfile()
	p.Kanban.Survey.Every = "1h"
	return p
}

// An hourly survey is not skipped because the next start came a moment before
// the hour was up: the interval is checked against a stamp written after the
// item was filed, so a start exactly one interval later is always a hair short.
func TestHourlySurveyRunsWhenTheNextStartIsASecondsEarly(t *testing.T) {
	store, reg := testEnv(t)
	var turns []Turn
	t0 := time.Now()
	first := newWorker(t, store, reg, hourlyProfile(), handoffRunner(store, &turns))
	first.Once, first.now = false, func() time.Time { return t0 }
	runFor(t, first, 3*time.Second)
	if len(turns) != 1 {
		t.Fatalf("first start: %d turns", len(turns))
	}

	for name, c := range map[string]struct {
		after   time.Duration
		surveys bool
	}{
		"exactly an hour":                {time.Hour, true},
		"thirty seconds early":           {time.Hour - 30*time.Second, true},
		"just inside the grace":          {time.Hour - 119*time.Second, true},
		"three minutes early is skipped": {time.Hour - 3*time.Minute, false},
		"half the interval is skipped":   {30 * time.Minute, false},
	} {
		t.Run(name, func(t *testing.T) {
			store, reg := testEnv(t)
			var turns []Turn
			a := newWorker(t, store, reg, hourlyProfile(), handoffRunner(store, &turns))
			a.Once, a.now = false, func() time.Time { return t0 }
			runFor(t, a, 3*time.Second)
			at := t0.Add(c.after)
			b := newWorker(t, store, reg, hourlyProfile(), handoffRunner(store, &turns))
			b.Once, b.Repo, b.now = false, a.Repo, func() time.Time { return at }
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = b.Run(ctx)
			want := 1
			if c.surveys {
				want = 2
			}
			if len(turns) != want {
				t.Fatalf("%d survey turns, want %d\n%s", len(turns), want, b.Log.(*bytes.Buffer).String())
			}
			if !c.surveys && !strings.Contains(b.Log.(*bytes.Buffer).String(), "survey skipped") {
				t.Fatalf("log %q", b.Log.(*bytes.Buffer).String())
			}
		})
	}
}

// A finished survey does not block the next hour's: the board refuses only a
// second open item with the same title, and the title carries the date, so the
// hours of one day share a title.
func TestHourlySurveysOfOneDayShareATitleWithoutColliding(t *testing.T) {
	store, reg := testEnv(t)
	var turns []Turn
	t0 := time.Now()
	repo := ""
	for i := 0; i < 3; i++ {
		at := t0.Add(time.Duration(i) * time.Hour)
		w := newWorker(t, store, reg, hourlyProfile(), handoffRunner(store, &turns))
		w.Once, w.now = false, func() time.Time { return at }
		if repo != "" {
			w.Repo = repo
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = w.Run(ctx)
		cancel()
		repo = w.Repo
	}
	if len(turns) != 3 {
		t.Fatalf("%d surveys over three hours, want 3", len(turns))
	}
}

// A worktree needs a git repository. A worker started in a plain directory is
// refused up front, instead of claiming (or filing) an item and then failing to
// prepare a workspace for it.
func TestCheckRepoRefusesAWorktreeProfileOutsideGit(t *testing.T) {
	plain := t.TempDir()
	worktree := scoutProfile()
	worktree.Workspace = &agentprofile.WorkspaceSpec{Isolation: agentprofile.IsolationWorktree, ReadOnly: true}

	err := CheckRepo(worktree, plain)
	if err == nil {
		t.Fatal("a worktree profile in a plain directory must be refused")
	}
	for _, want := range []string{"t-scout", "workspace.isolation: worktree", "git repository", plain} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}

	if out, err := exec.Command("git", "-C", plain, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git init: %v %s", err, out)
	}
	if err := CheckRepo(worktree, plain); err != nil {
		t.Errorf("a git repository: %v", err)
	}
	sub := filepath.Join(plain, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CheckRepo(worktree, sub); err != nil {
		t.Errorf("a directory inside a repository: %v", err)
	}
}

// Only a worktree needs git: a profile with no isolation or a shared checkout
// is not refused for the directory it starts in.
func TestCheckRepoLeavesOtherIsolationAlone(t *testing.T) {
	plain := t.TempDir()
	none := scoutProfile()
	if err := CheckRepo(none, plain); err != nil {
		t.Errorf("no workspace block: %v", err)
	}
	for _, iso := range []string{"", agentprofile.IsolationNone, agentprofile.IsolationShared} {
		p := scoutProfile()
		p.Workspace = &agentprofile.WorkspaceSpec{Isolation: iso}
		if err := CheckRepo(p, plain); err != nil {
			t.Errorf("isolation %q: %v", iso, err)
		}
	}
}

// A profile's handoff_repos reaches the claim a worker's session works under,
// and an unset one does not.
func TestClaimCarriesHandoffRepos(t *testing.T) {
	for _, on := range []bool{false, true} {
		store, reg := testEnv(t)
		var turns []Turn
		p := scoutProfile()
		p.Kanban.HandoffRepos = on
		w := newWorker(t, store, reg, p, handoffRunner(store, &turns))
		w.Once = false
		runFor(t, w, 3*time.Second)
		if len(turns) != 1 || turns[0].Claim == nil {
			t.Fatalf("handoff_repos %v: %d turns", on, len(turns))
		}
		if turns[0].Claim.HandoffRepos != on {
			t.Errorf("handoff_repos %v: claim has %v", on, turns[0].Claim.HandoffRepos)
		}
	}
}

// A worker in a plain folder that is not a git repository works without one:
// it surveys, runs its turn in the folder itself, hands the finding off under a
// repository the index found beside it, and finishes the survey item. This is
// the hourly CloudWatch analyzer's shape.
func TestSurveyingWorkerInAPlainFolderFilesUnderTheOwningRepo(t *testing.T) {
	store, reg := testEnv(t)
	base := t.TempDir()
	folder := filepath.Join(base, "work")
	website := filepath.Join(folder, "website")
	for _, dir := range []string{folder, website} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "https://github.com/acme/website.git"}} {
		if out, err := exec.Command("git", append([]string{"-C", website}, args...)...).CombinedOutput(); err != nil {
			t.Skipf("git %v: %v %s", args, err, out)
		}
	}
	if _, err := os.Stat(filepath.Join(folder, ".git")); err == nil {
		t.Fatal("the worker's folder must not be a repository")
	}

	p := scoutProfile()
	p.Kanban.Project = "" // the folder's own project
	p.Kanban.HandoffRepos = true
	p.Kanban.Survey.List = "review"
	p.Workspace = nil

	var turn Turn
	runner := func(ctx context.Context, tt Turn) (run.Result, error) {
		turn = tt
		tt.Claim.UseRepoIndex(repoindex.Scan(ctx, folder))
		h := tools.KanbanHandoff{KanbanBase: tools.KanbanBase{Store: store, Source: kanban.NewSource(kanban.ProvenanceFor(folder, tt.SessionID, "h")), Claim: tt.Claim}}
		if _, err := h.Execute(ctx, map[string]any{"title": "Raise the queue timeout", "body": "evidence", "list": "backlog", "labels": []any{"build"}, "repo": "acme/website"}); err != nil {
			return run.Result{}, err
		}
		return complete(ctx, tt)
	}
	w := newWorker(t, store, reg, p, runner)
	w.Repo, w.Once = folder, false
	runFor(t, w, 3*time.Second)

	if turn.Workdir != folder {
		t.Fatalf("the turn ran in %q, want the plain folder %q", turn.Workdir, folder)
	}
	var survey, handoff kanban.Item
	items, err := store.Search(kanban.Query{})
	if err != nil {
		t.Fatal(err)
	}
	done, err := store.Search(kanban.Query{Lists: []kanban.List{kanban.Done}})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range append(items, done...) {
		switch {
		case slices.Contains(it.Labels, agentprofile.SurveyLabel):
			survey = it
		case it.Title == "Raise the queue timeout":
			handoff = it
		}
	}
	if survey.ID == "" || survey.List != kanban.Done {
		t.Fatalf("the survey item should be filed and done: %+v", survey)
	}
	want := kanban.ProvenanceFor(website, "", "")
	if handoff.ID == "" || handoff.ProjectKey != want.ProjectKey || handoff.Dir != want.Dir {
		t.Fatalf("the handoff should sit under the website checkout: %+v", handoff)
	}
	if handoff.ProjectKey == survey.ProjectKey {
		t.Error("the handoff stayed in the folder's own project")
	}
	if handoff.List != kanban.Review {
		t.Errorf("a survey's handoff waits in review, got %s", handoff.List)
	}
	if handoff.Parent != survey.ID {
		t.Errorf("the handoff should link to the survey item, got parent %q", handoff.Parent)
	}
}
