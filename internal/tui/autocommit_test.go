package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/filediff"
	"github.com/vulnetix/belai/internal/goals"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
)

type autoCommitRunner struct {
	out   map[string]string
	errs  map[string]error
	calls []string
}

func newAutoCommitRunner() *autoCommitRunner {
	return &autoCommitRunner{out: map[string]string{}, errs: map[string]error{}}
}

func (f *autoCommitRunner) run(_ context.Context, _ string, argv ...string) ([]byte, error) {
	key := strings.Join(argv, " ")
	f.calls = append(f.calls, key)
	if err, ok := f.errs[key]; ok {
		return []byte(f.out[key]), err
	}
	if out, ok := f.out[key]; ok {
		return []byte(out), nil
	}
	return nil, errors.New("unscripted: " + key)
}

func (f *autoCommitRunner) on(cmd, out string) *autoCommitRunner {
	f.out[cmd] = out
	return f
}

func (f *autoCommitRunner) fail(cmd string, err error) *autoCommitRunner {
	f.errs[cmd] = err
	return f
}

func (f *autoCommitRunner) called(prefix string) bool {
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func autocommitApp() *App {
	a := New(Options{})
	a.lastGoal = &goals.GoalState{Objective: "ship it"}
	a.taskPaths = []string{"a.go"}
	a.taskPathSet = map[string]bool{"a.go": true}
	return a
}

func TestAutoCommitOffMakesNoCommit(t *testing.T) {
	a := autocommitApp()
	cmd := a.flushAutoCommit(run.Result{GoalSentinel: rolemanager.GoalComplete})
	if cmd != nil {
		t.Fatal("setting off must not start a commit")
	}
	if len(a.taskPaths) != 0 {
		t.Fatalf("paths not reset: %v", a.taskPaths)
	}
}

func TestAutoCommitPartialGoalMakesNoCommit(t *testing.T) {
	a := autocommitApp()
	on := true
	a.settings.AutoCommitPerTask = &on
	cmd := a.flushAutoCommit(run.Result{GoalSentinel: rolemanager.GoalPartial})
	if cmd != nil {
		t.Fatal("a partial goal must not commit")
	}
	if len(a.taskPaths) != 0 {
		t.Fatalf("paths not reset: %v", a.taskPaths)
	}
}

func TestAutoCommitCompleteGoalCommitsAndResets(t *testing.T) {
	a := autocommitApp()
	on := true
	a.settings.AutoCommitPerTask = &on
	commitKey := "git commit --only -m chore: ship it\n\na.go -- a.go"
	f := newAutoCommitRunner().
		on("git add -A -- a.go", "").
		fail("git diff --cached --quiet -- a.go", errors.New("exit status 1")).
		on(commitKey, "").
		on("git rev-parse --short HEAD", "abc1234")
	a.forgeRunner = f.run

	cmd := a.flushAutoCommit(run.Result{GoalSentinel: rolemanager.GoalComplete})
	if cmd == nil {
		t.Fatal("a completed goal with paths must commit")
	}
	if len(a.taskPaths) != 0 {
		t.Fatalf("paths not reset before the commit goroutine: %v", a.taskPaths)
	}

	msg := cmd().(autoCommitMsg)
	if msg.err != nil {
		t.Fatalf("commit error: %v", msg.err)
	}
	if msg.sha != "abc1234" {
		t.Fatalf("sha = %q, want abc1234", msg.sha)
	}
	if !strings.HasPrefix(msg.msg, "chore: ") {
		t.Fatalf("message = %q, want chore type", msg.msg)
	}
	if !f.called(commitKey) {
		t.Fatalf("commit argv not run, calls = %v", f.calls)
	}

	a.handleAutoCommit(msg)
	last := a.messages[len(a.messages)-1]
	if !strings.Contains(last.Content, "auto-commit abc1234 chore: ship it") {
		t.Fatalf("system line = %q", last.Content)
	}
}

func TestAutoCommitErrorIsReported(t *testing.T) {
	a := autocommitApp()
	on := true
	a.settings.AutoCommitPerTask = &on
	commitKey := "git commit --only -m chore: ship it\n\na.go -- a.go"
	f := newAutoCommitRunner().
		on("git add -A -- a.go", "").
		fail("git diff --cached --quiet -- a.go", errors.New("exit status 1")).
		fail(commitKey, errors.New("hook failed"))
	a.forgeRunner = f.run

	msg := a.flushAutoCommit(run.Result{GoalSentinel: rolemanager.GoalComplete})().(autoCommitMsg)
	if msg.err == nil {
		t.Fatal("commit should fail")
	}
	a.handleAutoCommit(msg)
	last := a.messages[len(a.messages)-1]
	if !strings.Contains(last.Content, "hook failed") {
		t.Fatalf("system line = %q", last.Content)
	}
}

func TestObserveTaskDiffDedupesAndSkipsUnavailableOnly(t *testing.T) {
	a := New(Options{})
	a.observeTaskDiff(&filediff.Change{Files: []filediff.FileChange{
		{Path: "a.go", Old: "x", New: "y"},
		{Path: "a.go", Old: "y", New: "z"},
		{Path: "b.go", Old: "", New: "b"},
	}})
	if len(a.taskPaths) != 2 || a.taskPaths[0] != "a.go" || a.taskPaths[1] != "b.go" {
		t.Fatalf("taskPaths = %v, want deduped [a.go b.go]", a.taskPaths)
	}

	a.resetTaskPaths()
	a.observeTaskDiff(&filediff.Change{Unavailable: "outside a repo"})
	if len(a.taskPaths) != 0 {
		t.Fatalf("unavailable-only change must not collect paths: %v", a.taskPaths)
	}
}
