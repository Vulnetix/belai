package fleet

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tools"
)

func testEnv(t *testing.T) (*kanban.Store, *Registry) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("BELAI_HOME", home)
	t.Setenv("BELAI_WORKTREES_DIR", t.TempDir())
	// Never reach a real account: an unroutable site and sync off.
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
	store := kanban.Open(filepath.Join(home, "kanban"))
	return store, NewRegistry(filepath.Join(home, "agents", "run"), store)
}

func builderProfile() agentprofile.AgentProfile {
	return agentprofile.AgentProfile{
		Name: "t-builder", Description: "d", SystemPrompt: "sp", Mode: agentprofile.ModeWorker,
		Tools: []string{"Read"},
		Kanban: &agentprofile.KanbanSpec{
			Labels:      []string{"build"},
			OnSuccess:   agentprofile.Route{List: "review", Labels: []string{"needs-review"}, DropLabels: []string{"build"}},
			MaxAttempts: 2,
			Project:     "all",
		},
	}
}

func newWorker(t *testing.T, store *kanban.Store, reg *Registry, p agentprofile.AgentProfile, runner TurnRunner) *Worker {
	t.Helper()
	repo := t.TempDir()
	return &Worker{
		Profile: p, Repo: repo, Settings: offline(), Posture: posture.AllIgnore(),
		Store: store, Registry: reg, Record: Record{ID: NewID(p.Name), Profile: p.Name},
		Once: true, Runner: runner, Log: &bytes.Buffer{},
	}
}

func complete(ctx context.Context, t Turn) (run.Result, error) {
	return run.Result{StopReason: run.StopComplete, GoalSentinel: rolemanager.GoalComplete, Passes: 2}, nil
}

func TestWorkerCompletesAndRoutes(t *testing.T) {
	store, reg := testEnv(t)
	it, _, _ := store.Add(kanban.ItemInput{Title: "do it", Labels: []string{"build"}}, kanban.Provenance{})
	var got Turn
	w := newWorker(t, store, reg, builderProfile(), func(ctx context.Context, tt Turn) (run.Result, error) {
		got = tt
		return complete(ctx, tt)
	})
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got.Claim == nil || got.Claim.Item != it.ID || got.Claim.Worker != w.Record.ID {
		t.Fatalf("claim handed to the runner: %+v", got.Claim)
	}
	after, _ := store.Get(it.ID)
	if after.List != kanban.Review || after.ClaimedBy != "" || !slices.Equal(after.Labels, []string{"needs-review"}) {
		t.Fatalf("released %+v", after)
	}
	if !strings.Contains(after.LastNote(), "completed it") {
		t.Fatalf("note %q", after.LastNote())
	}
	rec, err := reg.Get(w.Record.ID)
	if err != nil || rec.State != StateStopped || rec.Done != 1 {
		t.Fatalf("record %+v %v", rec, err)
	}
}

func TestWorkerFailureRetriesThenBlocks(t *testing.T) {
	store, reg := testEnv(t)
	it, _, _ := store.Add(kanban.ItemInput{Title: "hard", Labels: []string{"build"}}, kanban.Provenance{})
	fail := func(ctx context.Context, tt Turn) (run.Result, error) {
		return run.Result{StopReason: run.StopStalled, Passes: 3}, nil
	}
	w := newWorker(t, store, reg, builderProfile(), fail)
	w.Run(context.Background())
	after, _ := store.Get(it.ID)
	if after.List != kanban.Backlog || after.Attempts != 1 || !strings.Contains(after.LastNote(), "stalled") {
		t.Fatalf("first failure: %+v", after)
	}
	// The same worker never takes back an item it failed.
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if again, _ := store.Get(it.ID); again.Attempts != 1 {
		t.Fatal("the worker retried its own failed item")
	}
	// A second worker fails it too: max_attempts 2, so it is blocked.
	var notified []string
	w2 := newWorker(t, store, reg, builderProfile(), fail)
	w2.Notify = func(e string) { notified = append(notified, e) }
	w2.Run(context.Background())
	after, _ = store.Get(it.ID)
	if after.List != kanban.Blocked || after.Attempts != 2 {
		t.Fatalf("second failure: %+v", after)
	}
	if !slices.Contains(notified, "worker_blocked") {
		t.Fatal("no worker_blocked notification")
	}
}

func TestWorkerBlocksOnWithheldAsksAndItems(t *testing.T) {
	store, reg := testEnv(t)
	asks, _, _ := store.Add(kanban.ItemInput{Title: "needs bash", Labels: []string{"build"}}, kanban.Provenance{})
	w := newWorker(t, store, reg, builderProfile(), func(ctx context.Context, tt Turn) (run.Result, error) {
		return run.Result{StopReason: run.StopWithheld, AsksWithheld: []string{"Bash"}}, nil
	})
	w.Run(context.Background())
	got, _ := store.Get(asks.ID)
	if got.List != kanban.Blocked || !strings.Contains(got.LastNote(), "needs permission: Bash") {
		t.Fatalf("asks: %+v", got)
	}

	bad, _, _ := store.Add(kanban.ItemInput{Title: "injected", Labels: []string{"build"}}, kanban.Provenance{})
	w = newWorker(t, store, reg, builderProfile(), func(ctx context.Context, tt Turn) (run.Result, error) {
		return run.Result{}, &agent.ItemWithheldError{Item: tt.Item.Short(), Sentinel: rolemanager.SentinelPromptInjection}
	})
	w.Run(context.Background())
	got, _ = store.Get(bad.ID)
	if got.List != kanban.Blocked || !strings.Contains(got.LastNote(), "withheld") {
		t.Fatalf("withheld: %+v", got)
	}
}

func TestWorkerLeavesAnItemWhoseLeaseWasLost(t *testing.T) {
	store, reg := testEnv(t)
	it, _, _ := store.Add(kanban.ItemInput{Title: "taken back", Labels: []string{"build"}}, kanban.Provenance{})
	p := builderProfile()
	p.Kanban.Lease = "1m"
	w := newWorker(t, store, reg, p, func(ctx context.Context, tt Turn) (run.Result, error) {
		// A human releases the claim mid-turn; the next renewal notices.
		if _, err := store.Unclaim(tt.Item.ID, "taken back", "human", false); err != nil {
			return run.Result{}, err
		}
		if err := store.Renew(tt.Item.ID, tt.Claim.Worker, time.Minute); !errors.Is(err, kanban.ErrLeaseLost) {
			t.Errorf("renew after unclaim: %v", err)
		}
		return complete(ctx, tt)
	})
	w.Run(context.Background())
	got, _ := store.Get(it.ID)
	if rec, _ := reg.Get(w.Record.ID); rec.Item != "" {
		t.Fatalf("record still shows %s after the lease was lost", rec.Item)
	}
	if got.List != kanban.Backlog || got.ClaimedBy != "" {
		t.Fatalf("item %+v", got)
	}
}

func TestRegistryReserveAndDeadWorkers(t *testing.T) {
	store, reg := testEnv(t)
	it, _, _ := store.Add(kanban.ItemInput{Title: "orphan"}, kanban.Provenance{})
	dead := Record{ID: "dead-1", Profile: "p", PID: 1 << 30, State: StateWorking, Started: 1}
	if err := reg.Save(dead); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(kanban.ClaimRequest{Worker: "dead-1", Lease: time.Minute}); err != nil {
		t.Fatal(err)
	}
	recs, err := reg.List()
	if err != nil || len(recs) != 1 || recs[0].State != StateFailed {
		t.Fatalf("list %+v %v", recs, err)
	}
	if got, _ := store.Get(it.ID); got.ClaimedBy != "" || got.List != kanban.Backlog {
		t.Fatalf("dead worker's claim kept: %+v", got)
	}
	me := Record{ID: "me-1", Profile: "p", PID: os.Getpid(), State: StateIdle}
	if err := reg.Reserve(me, 1); err != nil {
		t.Fatal(err)
	}
	if err := reg.Reserve(Record{ID: "me-2", Profile: "p", PID: os.Getpid(), State: StateIdle}, 1); !errors.Is(err, ErrFull) {
		t.Fatalf("second reserve: %v", err)
	}
	if !ValidID("belai-builder-3f9a2c") || ValidID("../x") || ValidID("") {
		t.Fatal("ValidID")
	}
}

func TestNarrowGlobsMCP(t *testing.T) {
	root := t.TempDir()
	reg := tools.NewRegistry(&tools.Read{Root: root}, &tools.Write{Root: root})
	if got := narrow(reg, []string{"Read"}).Names(); !slices.Equal(got, []string{"Read"}) {
		t.Fatalf("narrow %v", got)
	}
	if got := narrow(reg, nil).Names(); len(got) != 2 {
		t.Fatalf("empty allowlist narrowed: %v", got)
	}
}

func TestPreflightFailsClosed(t *testing.T) {
	p := builderProfile()
	off := false
	if err := Preflight(p, config.Settings{Agents: &config.AgentsSettings{Enabled: &off}}, posture.Defaults()); err == nil {
		t.Fatal("agents.enabled off ignored")
	}
	if err := Preflight(p, config.Settings{Kanban: &off}, posture.Defaults()); err == nil {
		t.Fatal("kanban off ignored")
	}
	bash := builderProfile()
	bash.Tools = []string{"Bash"}
	bash.Autonomy = agentprofile.AutonomyAutonomous
	bash.Budget = &agentprofile.BudgetSpec{MaxPassesPerItem: 2}
	bash.Workspace = &agentprofile.WorkspaceSpec{Isolation: agentprofile.IsolationWorktree}
	if err := Preflight(bash, config.Settings{}, posture.AllIgnore()); err == nil {
		t.Fatal("an autonomous Bash worker started with the sandbox off")
	}
	single := builderProfile()
	single.Mode, single.Kanban = agentprofile.ModeSingle, nil
	if err := Preflight(single, config.Settings{}, posture.Defaults()); err == nil {
		t.Fatal("a non-worker profile passed preflight")
	}
}

func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.name", "Test"},
		{"config", "user.email", "test@example.invalid"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	os.WriteFile(filepath.Join(dir, "README"), []byte("hi\n"), 0o644)
	for _, args := range [][]string{{"add", "README"}, {"commit", "-q", "-m", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return dir
}

func TestWorktreeLifecycleIsHardened(t *testing.T) {
	testEnv(t)
	repo := gitRepo(t)
	// A repository hook must never run on a worker's account.
	marker := filepath.Join(t.TempDir(), "hook-ran")
	hook := filepath.Join(repo, ".git", "hooks", "post-checkout")
	os.WriteFile(hook, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755)
	os.WriteFile(filepath.Join(repo, ".git", "hooks", "pre-commit"), []byte("#!/bin/sh\ntouch "+marker+"\nexit 1\n"), 0o755)

	it := kanban.Item{ID: "3f9a2c00-0000-4000-8000-000000000000", Title: "t"}
	ctx := context.Background()
	ws, err := PrepareWorktree(ctx, repo, it, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ws.Branch, "belai/K-3f9a2c-a1") || strings.HasPrefix(ws.Dir, repo) {
		t.Fatalf("workspace %+v", ws)
	}
	os.WriteFile(filepath.Join(ws.Dir, "new.txt"), []byte("x\n"), 0o644)
	n, err := ws.Commit(ctx, "belai: test")
	if err != nil || n != 1 {
		t.Fatalf("commit %d %v", n, err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a repository hook ran")
	}
	log, _ := exec.Command("git", "-C", repo, "log", "--format=%s", ws.Branch).CombinedOutput()
	if !strings.Contains(string(log), "belai: test") {
		t.Fatalf("branch log %s", log)
	}

	// A second attempt checks out the same branch.
	it.Branch = ws.Branch
	if err := ws.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	ws2, err := PrepareWorktree(ctx, repo, it, "")
	if err != nil || ws2.Branch != ws.Branch {
		t.Fatalf("reuse %v %v", ws2, err)
	}
	if _, err := os.Stat(filepath.Join(ws2.Dir, "new.txt")); err != nil {
		t.Fatal("the branch's work is missing")
	}
	// A rewritten .git pointer stops every later git call.
	os.WriteFile(filepath.Join(ws2.Dir, ".git"), []byte("gitdir: /tmp/evil\n"), 0o644)
	os.WriteFile(filepath.Join(ws2.Dir, "more.txt"), []byte("x\n"), 0o644)
	if _, err := ws2.Commit(ctx, "x"); err == nil {
		t.Fatal("committed through a tampered .git file")
	}
	ws2.Remove(ctx)

	for _, branch := range []string{"main", "belai/--upload-pack=x", "belai/nope"} {
		it.Branch = branch
		if _, err := PrepareWorktree(ctx, repo, it, ""); err == nil {
			t.Errorf("branch %q accepted", branch)
		}
	}
	it.Branch = ""
	if _, err := PrepareWorktree(ctx, repo, it, "--orphan"); err == nil {
		t.Error("option-shaped base accepted")
	}
}

func TestMemoryAppendIsCapped(t *testing.T) {
	testEnv(t)
	now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	for i := range 50 {
		AppendMemory("belai:builder", []string{strings.Repeat("x", 100) + string(rune('a'+i%26))}, 1024, now)
	}
	got := ReadMemory("belai:builder")
	if len(got) > 1024 || !strings.Contains(got, "2026-09-28") {
		t.Fatalf("memory %d bytes", len(got))
	}
	lessons := ParseLessons("1. use go test ./...\n- NONE\n* keep diffs small\n\nthird\nfourth")
	if len(lessons) != 3 || lessons[0] != "use go test ./..." {
		t.Fatalf("lessons %q", lessons)
	}
	if err := ClearMemory("belai:builder"); err != nil || ReadMemory("belai:builder") != "" {
		t.Fatal("clear")
	}
}

// offline is settings with board sync off.
func offline() config.Settings {
	f := false
	return config.Settings{Sync: &config.SyncSettings{Enabled: &f}}
}
