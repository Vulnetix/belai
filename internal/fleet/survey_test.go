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
