package tui

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/attribution"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/filediff"
	"github.com/vulnetix/belai/internal/goals"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
)

const (
	headA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	headB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	cmdStatus    = "git status --porcelain=v1 -z --untracked-files=all"
	cmdPrefix    = "git rev-parse --show-prefix"
	cmdHead      = "git rev-parse --verify HEAD"
	cmdGitDir    = "git rev-parse --absolute-git-dir"
	cmdShortHead = "git rev-parse --short HEAD"
)

// autoCommitRunner scripts git. A command with several outputs returns them in
// order and then repeats the last; one with none is an error, so a call the
// test did not expect fails loudly. The runner is shared with the goroutine
// that reads the tree at a turn's start, hence the lock.
type autoCommitRunner struct {
	mu   sync.Mutex
	out  map[string][]string
	errs map[string]error
	// prefixes succeed with no output for any command that starts with one.
	prefixes []string
	calls    []string
}

func newAutoCommitRunner() *autoCommitRunner {
	return &autoCommitRunner{out: map[string][]string{}, errs: map[string]error{}}
}

func (f *autoCommitRunner) run(_ context.Context, _ string, argv ...string) ([]byte, error) {
	key := strings.Join(argv, " ")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, key)
	if err, ok := f.errs[key]; ok {
		return nil, err
	}
	if outs, ok := f.out[key]; ok && len(outs) > 0 {
		o := outs[0]
		if len(outs) > 1 {
			f.out[key] = outs[1:]
		}
		return []byte(o), nil
	}
	for _, p := range f.prefixes {
		if strings.HasPrefix(key, p) {
			return nil, nil
		}
	}
	return nil, errors.New("unscripted: " + key)
}

func (f *autoCommitRunner) on(cmd string, out ...string) *autoCommitRunner {
	f.out[cmd] = out
	return f
}

func (f *autoCommitRunner) onPrefix(p string) *autoCommitRunner {
	f.prefixes = append(f.prefixes, p)
	return f
}

func (f *autoCommitRunner) fail(cmd string, err error) *autoCommitRunner {
	f.errs[cmd] = err
	return f
}

func (f *autoCommitRunner) called(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// quietRepo scripts a clean repository at headA with a.go as the one file the
// turn changes, and a commit that succeeds.
func quietRepo(t *testing.T) *autoCommitRunner {
	t.Helper()
	gitDir := t.TempDir()
	commitKey := "git commit --only -m chore: ship it\n\na.go -- a.go"
	return newAutoCommitRunner().
		on(cmdPrefix, "").
		on(cmdHead, headA, headA, headA, headA, headA, headA).
		on(cmdGitDir, gitDir).
		on(cmdStatus, "", "?? a.go\x00").
		on("git add -A -- a.go", "").
		fail("git diff --cached --quiet -- a.go", errors.New("exit status 1")).
		on(commitKey, "").
		on(cmdShortHead, "abc1234")
}

func commitTestApp(t *testing.T, mode string, f *autoCommitRunner) *App {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
	a := New(Options{})
	a.lastGoal = &goals.GoalState{Objective: "ship it"}
	a.settings.CommitMode = mode
	a.forgeRunner = f.run
	return a
}

// startTurn reads the tree as a turn's start does and waits for it.
func startTurn(t *testing.T, a *App, paths ...string) {
	t.Helper()
	a.beginCommitTurn()
	if a.commitBase != nil {
		<-a.commitBase.done
	}
	a.taskPaths = append([]string(nil), paths...)
	a.taskPathSet = map[string]bool{}
	for _, p := range paths {
		a.taskPathSet[p] = true
	}
}

func complete() run.Result { return run.Result{GoalSentinel: rolemanager.GoalComplete} }

func lastSystemRow(a *App) string {
	if len(a.messages) == 0 {
		return ""
	}
	return a.messages[len(a.messages)-1].Content
}

func TestAutoCommitOffMakesNoCommit(t *testing.T) {
	f := quietRepo(t)
	a := commitTestApp(t, "", f)
	startTurn(t, a, "a.go")
	if cmd := a.flushAutoCommit(complete()); cmd != nil {
		t.Fatal("commit_mode off must not start a commit")
	}
	if len(a.taskPaths) != 0 {
		t.Fatalf("paths not reset: %v", a.taskPaths)
	}
	if a.commitBase != nil || len(f.calls) != 0 {
		t.Fatalf("the default must not read the tree: base=%v calls=%v", a.commitBase, f.calls)
	}
}

func TestAutoCommitPartialGoalMakesNoCommit(t *testing.T) {
	f := quietRepo(t)
	a := commitTestApp(t, "auto", f)
	startTurn(t, a, "a.go")
	if cmd := a.flushAutoCommit(run.Result{GoalSentinel: rolemanager.GoalPartial}); cmd != nil {
		t.Fatal("a partial goal must not commit")
	}
	if len(a.taskPaths) != 0 {
		t.Fatalf("paths not reset: %v", a.taskPaths)
	}
	// The turn still counts toward /commit.
	if got := a.commitLedger.Owned(); len(got) != 1 || got[0] != "a.go" {
		t.Fatalf("ledger owned = %v, want [a.go]", got)
	}
}

func TestLegacyAutoCommitPerTaskMeansAuto(t *testing.T) {
	f := quietRepo(t)
	a := commitTestApp(t, "", f)
	on := true
	a.settings.AutoCommitPerTask = &on
	startTurn(t, a, "a.go")
	cmd := a.flushAutoCommit(complete())
	if cmd == nil {
		t.Fatal("the legacy switch must still commit")
	}
	if msg := cmd().(autoCommitMsg); msg.sha != "abc1234" || msg.mode != "auto" {
		t.Fatalf("msg = %+v", msg)
	}
	// An explicit commit_mode wins over the legacy switch.
	a2 := commitTestApp(t, "off", quietRepo(t))
	a2.settings.AutoCommitPerTask = &on
	startTurn(t, a2, "a.go")
	if a2.flushAutoCommit(complete()) != nil {
		t.Fatal("commit_mode off must beat auto_commit_per_task")
	}
}

func TestAutoCommitCompleteGoalCommitsAndResets(t *testing.T) {
	// The commit message is asserted as built, so pin the test outside the
	// sandbox attribution file: attribution.Message appends trailers when the
	// machine running the tests carries one.
	t.Setenv(attribution.FileEnv, os.DevNull)
	f := quietRepo(t)
	a := commitTestApp(t, "auto", f)
	startTurn(t, a, "a.go")

	cmd := a.flushAutoCommit(complete())
	if cmd == nil {
		t.Fatal("a completed goal with paths must commit")
	}
	if len(a.taskPaths) != 0 {
		t.Fatalf("paths not reset before the commit goroutine: %v", a.taskPaths)
	}
	msg := cmd().(autoCommitMsg)
	if msg.err != nil || msg.refused != "" {
		t.Fatalf("commit failed: err=%v refused=%q", msg.err, msg.refused)
	}
	if msg.sha != "abc1234" {
		t.Fatalf("sha = %q, want abc1234", msg.sha)
	}
	if !strings.HasPrefix(msg.msg, "chore: ") {
		t.Fatalf("message = %q, want chore type", msg.msg)
	}
	if f.called("git push") {
		t.Fatal("the workflow must never push")
	}
	a.handleAutoCommit(msg)
	if last := lastSystemRow(a); !strings.Contains(last, "auto-commit abc1234 chore: ship it") {
		t.Fatalf("system line = %q", last)
	}
	if len(a.commitLedger.Owned()) != 0 {
		t.Fatalf("a committed path must leave the ledger: %v", a.commitLedger.Owned())
	}
}

func TestAutoCommitErrorIsReportedAndIndexRestored(t *testing.T) {
	t.Setenv(attribution.FileEnv, os.DevNull)
	f := quietRepo(t)
	f.fail("git commit --only -m chore: ship it\n\na.go -- a.go", errors.New("hook failed")).
		on("git reset -q -- a.go", "")
	a := commitTestApp(t, "auto", f)
	startTurn(t, a, "a.go")

	msg := a.flushAutoCommit(complete())().(autoCommitMsg)
	if msg.err == nil {
		t.Fatal("commit should fail")
	}
	a.handleAutoCommit(msg)
	if !strings.Contains(lastSystemRow(a), "hook failed") {
		t.Fatalf("system line = %q", lastSystemRow(a))
	}
	if !f.called("git reset -q -- a.go") {
		t.Fatalf("a failed commit must put the staged paths back; calls = %v", f.calls)
	}
}

// Each of these leaves the tree and index alone: no add, no commit.
func TestAutoCommitRefusesAndTouchesNothing(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*autoCommitRunner)
		skip  bool
		want  string
	}{
		{"head moved", func(f *autoCommitRunner) { f.on(cmdHead, headA, headB) }, false, "HEAD moved"},
		{"status unreadable after", func(f *autoCommitRunner) { f.on(cmdStatus, "", ""); f.fail(cmdStatus, errors.New("git status: boom")) }, false, "could not read"},
		{"rebase in progress", func(f *autoCommitRunner) {
			dir := t.TempDir()
			_ = os.Mkdir(filepath.Join(dir, "rebase-merge"), 0o755)
			f.on(cmdGitDir, dir)
		}, false, "a rebase is in progress"},
		{"mixed paths", func(f *autoCommitRunner) { f.on(cmdStatus, " M a.go\x00", " M a.go\x00") }, false, "edited before the turn and again in it"},
		{"no start snapshot", func(f *autoCommitRunner) {}, true, "switched on during the turn"},
	}
	for _, tc := range cases {
		f := quietRepo(t)
		tc.setup(f)
		a := commitTestApp(t, "auto", f)
		if tc.skip {
			a.taskPaths, a.taskPathSet = []string{"a.go"}, map[string]bool{"a.go": true}
		} else {
			startTurn(t, a, "a.go")
		}
		cmd := a.flushAutoCommit(complete())
		if cmd == nil {
			t.Fatalf("%s: no command", tc.name)
		}
		msg := cmd().(autoCommitMsg)
		if msg.refused == "" || !strings.Contains(msg.refused, tc.want) {
			t.Errorf("%s: refused = %q, want %q", tc.name, msg.refused, tc.want)
		}
		a.handleAutoCommit(msg)
		if !strings.Contains(lastSystemRow(a), "nothing was committed") {
			t.Errorf("%s: system row = %q", tc.name, lastSystemRow(a))
		}
		if f.called("git add") || f.called("git commit") || f.called("git reset") {
			t.Errorf("%s: the repository was touched: %v", tc.name, f.calls)
		}
	}
}

func TestAutoCommitUnreadableStartSnapshotRefuses(t *testing.T) {
	f := quietRepo(t)
	f.fail(cmdPrefix, errors.New("git: not a repository"))
	a := commitTestApp(t, "auto", f)
	startTurn(t, a, "a.go")
	msg := a.flushAutoCommit(complete())().(autoCommitMsg)
	if !strings.Contains(msg.refused, "could not read the tree before the turn") {
		t.Fatalf("refused = %q", msg.refused)
	}
	if f.called("git add") {
		t.Fatal("staged on an unreadable tree")
	}
	// The turn's path is not trusted later either.
	if got := a.commitLedger.Mixed(); len(got) != 1 {
		t.Fatalf("an unreadable snapshot must make the path mixed, got %v", got)
	}
}

func TestPreExistingAndUnrelatedPathsAreLeftAlone(t *testing.T) {
	f := quietRepo(t)
	// Before the turn: notes.txt was already modified. After: a.go is new,
	// notes.txt still modified, extra.go appeared outside the tools.
	f.on(cmdStatus, " M notes.txt\x00", " M notes.txt\x00?? a.go\x00?? extra.go\x00")
	a := commitTestApp(t, "auto", f)
	startTurn(t, a, "a.go")
	msg := a.flushAutoCommit(complete())().(autoCommitMsg)
	if msg.sha == "" {
		t.Fatalf("expected a commit, got refused=%q err=%v", msg.refused, msg.err)
	}
	if len(msg.split.Task) != 1 || msg.split.Task[0] != "a.go" {
		t.Fatalf("task = %v", msg.split.Task)
	}
	if got := msg.split.PreExisting; len(got) != 1 || got[0] != "notes.txt" {
		t.Fatalf("pre-existing = %v", got)
	}
	if got := msg.split.Unrelated; len(got) != 1 || got[0] != "extra.go" {
		t.Fatalf("unrelated = %v", got)
	}
	a.handleAutoCommit(msg)
	row := lastSystemRow(a)
	if !strings.Contains(row, "notes.txt") || !strings.Contains(row, "extra.go") {
		t.Fatalf("the left-behind files must be named: %q", row)
	}
}

func TestCredentialAndStatePathsAreWithheld(t *testing.T) {
	f := quietRepo(t)
	f.on(cmdStatus, "", "?? a.go\x00?? .env\x00?? config/id_rsa\x00?? .vulnetix/belai/credentials.json\x00?? notes.log\x00")
	a := commitTestApp(t, "auto", f)
	a.settings.CommitExclude = []string{"*.log"}
	startTurn(t, a, "a.go", ".env", "config/id_rsa", ".vulnetix/belai/credentials.json", "notes.log")
	msg := a.flushAutoCommit(complete())().(autoCommitMsg)
	if msg.sha == "" {
		t.Fatalf("refused=%q err=%v", msg.refused, msg.err)
	}
	if len(msg.split.Task) != 1 || msg.split.Task[0] != "a.go" {
		t.Fatalf("task = %v", msg.split.Task)
	}
	if len(msg.split.Withheld) != 4 {
		t.Fatalf("withheld = %v", msg.split.Withheld)
	}
}

func TestAskModeAsksThenCommitsOnYes(t *testing.T) {
	f := quietRepo(t)
	a := commitTestApp(t, "ask", f)
	startTurn(t, a, "a.go")
	msg := a.flushAutoCommit(complete())().(autoCommitMsg)
	if msg.ask == nil || msg.sha != "" {
		t.Fatalf("ask mode must ask first: %+v", msg)
	}
	if f.called("git add") || f.called("git commit") {
		t.Fatal("ask mode changed the repository before the answer")
	}
	a.Update(msg)
	if !a.viewOpen(viewCommit) || a.commitAsk.plan == nil {
		t.Fatalf("the question did not open: view=%v", a.view)
	}
	if v := a.commitAskView(); !strings.Contains(v, "a.go") || !strings.Contains(v, "chore: ship it") {
		t.Fatalf("the question must show the paths and the message:\n%s", v)
	}
	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd == nil {
		t.Fatal("y returned no command")
	}
	ans := cmd().(commitAnsweredMsg)
	a.Update(ans)
	if a.viewOpen(viewCommit) {
		t.Fatal("the question stayed open")
	}
	if !strings.Contains(lastSystemRow(a), "commit abc1234 chore: ship it") {
		t.Fatalf("row = %q", lastSystemRow(a))
	}
}

func TestAskModeDenyAndEscCommitNothing(t *testing.T) {
	for _, key := range []tea.KeyMsg{{Type: tea.KeyRunes, Runes: []rune{'n'}}, {Type: tea.KeyEsc}} {
		f := quietRepo(t)
		a := commitTestApp(t, "ask", f)
		startTurn(t, a, "a.go")
		a.Update(a.flushAutoCommit(complete())().(autoCommitMsg))
		a.Update(key)
		if a.viewOpen(viewCommit) || a.commitAsk.plan != nil {
			t.Fatalf("%v left the question open", key)
		}
		if f.called("git add") || f.called("git commit") {
			t.Fatalf("%v committed", key)
		}
	}
}

func TestAskModeRefusesWhenTheTreeChangedMeanwhile(t *testing.T) {
	f := quietRepo(t)
	a := commitTestApp(t, "ask", f)
	startTurn(t, a, "a.go")
	a.Update(a.flushAutoCommit(complete())().(autoCommitMsg))
	// While the question is open a.go is reverted by someone.
	f.on(cmdStatus, "")
	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	a.Update(cmd())
	if f.called("git add") || f.called("git commit") {
		t.Fatal("committed a tree that no longer matched the question")
	}
	if !strings.Contains(lastSystemRow(a), "the tree changed while the question was open") {
		t.Fatalf("row = %q", lastSystemRow(a))
	}
}

func TestANewTurnDropsAnOpenQuestion(t *testing.T) {
	f := quietRepo(t)
	a := commitTestApp(t, "ask", f)
	startTurn(t, a, "a.go")
	a.Update(a.flushAutoCommit(complete())().(autoCommitMsg))
	a.beginCommitTurn()
	if a.commitAsk.plan != nil || a.viewOpen(viewCommit) {
		t.Fatal("an unanswered question must be denied when a turn starts")
	}
	if f.called("git add") {
		t.Fatal("an unanswered question committed")
	}
}

func TestCommitCommandRefusals(t *testing.T) {
	f := quietRepo(t)
	a := commitTestApp(t, "", f)
	if a.handleCommand("/commit") != nil || !strings.Contains(lastSystemRow(a), "commit_mode is off") {
		t.Fatalf("off: %q", lastSystemRow(a))
	}
	a = commitTestApp(t, "auto", f)
	a.cancel = func() {}
	if a.handleCommand("/commit") != nil || !strings.Contains(lastSystemRow(a), "wait for the running turn") {
		t.Fatalf("running: %q", lastSystemRow(a))
	}
	a.cancel = nil
	if a.handleCommand("/commit Fix the thing.") != nil || !strings.Contains(lastSystemRow(a), "not a conventional commit") {
		t.Fatalf("bad message: %q", lastSystemRow(a))
	}
}

func TestCommitCommandCommitsTheSessionsPathsInAutoMode(t *testing.T) {
	f := quietRepo(t)
	f.on(cmdStatus, "", " M notes.txt\x00?? a.go\x00")
	a := commitTestApp(t, "auto", f)
	startTurn(t, a, "a.go")
	// A plain turn: no goal, so nothing is committed on its own.
	if a.flushAutoCommit(run.Result{}) != nil {
		t.Fatal("a plain turn must not commit by itself")
	}
	f.onPrefix("git commit --only -m feat(tui): add a")
	cmd := a.handleCommand("/commit feat(tui): add a")
	if cmd == nil {
		t.Fatalf("no command: %q", lastSystemRow(a))
	}
	msg := cmd().(autoCommitMsg)
	if msg.sha != "abc1234" || msg.mode != "manual" {
		t.Fatalf("msg = %+v", msg)
	}
	if len(msg.split.Unrelated) != 1 || msg.split.Unrelated[0] != "notes.txt" {
		t.Fatalf("unrelated = %v", msg.split.Unrelated)
	}
	a.Update(msg)
	if a.commitBusy {
		t.Fatal("busy flag left set")
	}
	if !strings.Contains(lastSystemRow(a), "commit abc1234 feat(tui): add a") {
		t.Fatalf("row = %q", lastSystemRow(a))
	}
}

func TestCommitCommandWithNothingTrackedPrintsTheSplit(t *testing.T) {
	f := quietRepo(t)
	f.on(cmdStatus, " M notes.txt\x00")
	a := commitTestApp(t, "auto", f)
	cmd := a.handleCommand("/commit")
	msg := cmd().(autoCommitMsg)
	if msg.sha != "" || msg.note == "" {
		t.Fatalf("msg = %+v", msg)
	}
	a.Update(msg)
	row := lastSystemRow(a)
	if !strings.Contains(row, "nothing to commit") || !strings.Contains(row, "notes.txt") {
		t.Fatalf("row = %q", row)
	}
	if f.called("git add") || f.called("git commit") {
		t.Fatal("committed with nothing tracked")
	}
}

func TestCommitCommandInAskModeAsksFirst(t *testing.T) {
	f := quietRepo(t)
	a := commitTestApp(t, "ask", f)
	startTurn(t, a, "a.go")
	a.flushAutoCommit(run.Result{})
	cmd := a.handleCommand("/commit")
	msg := cmd().(autoCommitMsg)
	if msg.ask == nil {
		t.Fatalf("ask mode /commit must ask: %+v", msg)
	}
	if f.called("git add") {
		t.Fatal("staged before the answer")
	}
}

func TestCommitDraftFallsBackOnBadOutput(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		err  error
		want string // substring of the message that is used
	}{
		{"good draft", "feat(tui): add the thing\n\nWhy it matters.\n\nCo-authored-by: Mallory <m@example.com>\nSigned-off-by: X", nil, "feat(tui): add the thing"},
		{"fenced", "```\nfix: handle empty input\n```", nil, "fix: handle empty input"},
		{"garbage", "I would be happy to help with that!", nil, "chore: ship it"},
		{"uppercase", "Feat: Add Thing", nil, "chore: ship it"},
		{"classifier error", "", errors.New("boom"), "chore: ship it"},
	}
	for _, tc := range cases {
		f := quietRepo(t)
		a := commitTestApp(t, "auto", f)
		on := true
		a.settings.CommitDraft = &on
		fc := &fakeClassifier{raw: tc.raw, err: tc.err}
		a.SetClassifier(fc)
		a.admitSummary = admitEcho(new(int32))
		startTurn(t, a, "a.go")
		// A draft changes the commit's message, so any commit succeeds here.
		f.onPrefix("git commit --only -m ")
		msg := a.flushAutoCommit(complete())().(autoCommitMsg)
		if !strings.Contains(msg.msg, tc.want) {
			t.Errorf("%s: message = %q, want %q", tc.name, msg.msg, tc.want)
		}
		if strings.Contains(strings.ToLower(msg.msg), "co-authored-by") || strings.Contains(strings.ToLower(msg.msg), "signed-off-by") {
			t.Errorf("%s: a model-written trailer survived: %q", tc.name, msg.msg)
		}
		if len(fc.payloads) != 1 || fc.payloads[0].UseCase != rolemanager.UseCaseCommitDraft || fc.payloads[0].Tools != nil {
			t.Errorf("%s: payloads = %+v", tc.name, fc.payloads)
		}
		if strings.Contains(fc.payloads[0].User, "package main") {
			t.Errorf("%s: the drafter must see names and counts only", tc.name)
		}
	}
}

func TestCommitDraftIsClassifiedBeforeAndAfter(t *testing.T) {
	f := quietRepo(t)
	a := commitTestApp(t, "auto", f)
	on := true
	a.settings.CommitDraft = &on
	fc := &fakeClassifier{raw: "feat: add x"}
	a.SetClassifier(fc)
	var n int32
	a.admitSummary = admitEcho(&n)
	startTurn(t, a, "a.go")
	a.flushAutoCommit(complete())()
	if n != 2 {
		t.Fatalf("admissions = %d, want 2 (the facts out, the draft back)", n)
	}

	// A flagged reply is not used.
	f = quietRepo(t)
	a = commitTestApp(t, "auto", f)
	a.settings.CommitDraft = &on
	a.SetClassifier(&fakeClassifier{raw: "feat: add x"})
	a.admitSummary = func(_ context.Context, s string, _ posture.Policy) (rolemanager.Decision, error) {
		if strings.HasPrefix(s, "feat:") {
			return rolemanager.Decision{}, &rolemanager.RefusalError{Sentinel: rolemanager.SentinelPromptInjection}
		}
		return rolemanager.Decision{Action: rolemanager.ActionProceed, Content: s}, nil
	}
	startTurn(t, a, "a.go")
	msg := a.flushAutoCommit(complete())().(autoCommitMsg)
	if !strings.HasPrefix(msg.msg, "chore: ship it") || msg.draftNote == "" {
		t.Fatalf("a flagged draft must fall back: msg=%q note=%q", msg.msg, msg.draftNote)
	}
}

func TestCommitOutcomesAreAClosedSet(t *testing.T) {
	got := []string{commitOutcomeAsk, commitOutcomeAuto, commitOutcomeRefused}
	want := []string{"commit_ask", "auto_commit", "refused"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("outcome %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestCommitRecordIsWrittenWithoutTheMessage(t *testing.T) {
	f := quietRepo(t)
	a := commitTestApp(t, "auto", f)
	a.workdir = t.TempDir()
	a.sessionID = "sess1"
	f.on(cmdHead, headA, headA, headA, headA, headA, headA)
	startTurn(t, a, "a.go")
	msg := a.flushAutoCommit(complete())().(autoCommitMsg)
	a.handleAutoCommit(msg)
	data, err := os.ReadFile(filepath.Join(config.ProjectCommitsDir(a.workdir), "sess1.json"))
	if err != nil {
		t.Fatalf("no record: %v", err)
	}
	if strings.Contains(string(data), "ship it") {
		t.Fatalf("the record holds message text: %s", data)
	}
	if !strings.Contains(string(data), `"mode": "auto"`) || !strings.Contains(string(data), `"outcome": "committed"`) {
		t.Fatalf("record = %s", data)
	}
}

func TestSettingsCommitModeIsGlobalOnly(t *testing.T) {
	if !settingsWritesGlobalOnly("commit_mode") || !settingsWritesGlobalOnly("commit_draft") {
		t.Fatal("commit_mode and commit_draft rows must write the global layer only")
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

// realRepo makes a repository with one commit, an isolated git identity and
// config, and returns its path.
func realRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	for name, body := range map[string]string{"a.go": "package a\n", "notes.txt": "n\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git("add", "-A")
	git("commit", "--no-gpg-sign", "-q", "-m", "initial")
	return dir
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestRealRepoFailingHookLeavesTheTreeUntouched(t *testing.T) {
	dir := realRepo(t)
	hook := filepath.Join(dir, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho 'lint failed' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
	a := New(Options{Workdir: dir})
	a.settings.CommitMode = "auto"
	a.lastGoal = &goals.GoalState{Objective: "ship it"}
	a.sessionID = "real1"
	head := gitIn(t, dir, "rev-parse", "HEAD")

	a.beginCommitTurn()
	<-a.commitBase.done
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n// edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.taskPaths, a.taskPathSet = []string{"a.go"}, map[string]bool{"a.go": true}
	statusBefore := gitIn(t, dir, "status", "--porcelain")

	msg := a.flushAutoCommit(complete())().(autoCommitMsg)
	if msg.err == nil || !strings.Contains(msg.err.Error(), "lint failed") {
		t.Fatalf("the hook's refusal must be reported: err=%v refused=%q", msg.err, msg.refused)
	}
	a.handleAutoCommit(msg)
	if got := gitIn(t, dir, "rev-parse", "HEAD"); got != head {
		t.Fatal("HEAD moved on a refused commit")
	}
	// The record the workflow writes lives in .vulnetix and is not a change to
	// the user's files.
	if got := strings.ReplaceAll(gitIn(t, dir, "status", "--porcelain"), "\n?? .vulnetix/", ""); got != statusBefore {
		t.Fatalf("status changed: %q -> %q", statusBefore, got)
	}
	if staged := gitIn(t, dir, "diff", "--cached", "--name-only"); staged != "" {
		t.Fatalf("the index kept staged paths: %q", staged)
	}
	if body, _ := os.ReadFile(filepath.Join(dir, "a.go")); !strings.Contains(string(body), "// edit") {
		t.Fatal("the working tree was changed")
	}
	rec, err := os.ReadFile(filepath.Join(config.ProjectCommitsDir(dir), "real1.json"))
	if err != nil || !strings.Contains(string(rec), `"outcome": "refused"`) {
		t.Fatalf("the refusal must be recorded: %v %s", err, rec)
	}
}

func TestRealRepoCommitsOnlyTaskPaths(t *testing.T) {
	dir := realRepo(t)
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
	// notes.txt is already modified before the turn, and the user has a
	// staged change of their own in another file.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "staged.txt"), []byte("s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "staged.txt")

	a := New(Options{Workdir: dir})
	a.settings.CommitMode = "auto"
	a.lastGoal = &goals.GoalState{Objective: "fix a"}
	a.sessionID = "real2"
	a.beginCommitTurn()
	<-a.commitBase.done
	for name, body := range map[string]string{"a.go": "package a\n// edit\n", "fresh.go": "package a\n", ".env": "KEY=1\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	a.taskPaths = []string{"a.go", "fresh.go", ".env", "notes.txt"}
	a.taskPathSet = map[string]bool{"a.go": true, "fresh.go": true, ".env": true, "notes.txt": true}

	// notes.txt was edited before and again in the turn: auto refuses whole.
	msg := a.flushAutoCommit(complete())().(autoCommitMsg)
	if !strings.Contains(msg.refused, "edited before the turn and again in it") {
		t.Fatalf("auto must refuse a mixed path: %+v", msg)
	}
	if gitIn(t, dir, "log", "--oneline") != gitIn(t, dir, "log", "--oneline", "-1") {
		t.Fatal("a commit was made")
	}

	// The same turn through /commit commits the clean-start paths only.
	a.commitLedger.Reset()
	a.commitLedger.Settle(nil, []string{"a.go", "fresh.go", ".env"}, true)
	a.settings.CommitMode = "auto"
	cmd := a.handleCommand("/commit fix(a): edit a and add fresh")
	if cmd == nil {
		t.Fatalf("no command: %q", lastSystemRow(a))
	}
	m := cmd().(autoCommitMsg)
	if m.sha == "" {
		t.Fatalf("no commit: refused=%q err=%v note=%q", m.refused, m.err, m.note)
	}
	files := strings.Fields(gitIn(t, dir, "show", "--name-only", "--format=", "HEAD"))
	if strings.Join(files, ",") != "a.go,fresh.go" {
		t.Fatalf("committed %v; the credential file, the user's edits and their staged file must stay out", files)
	}
	if st := gitIn(t, dir, "status", "--porcelain"); !strings.Contains(st, "notes.txt") || !strings.Contains(st, ".env") || !strings.Contains(st, "A  staged.txt") {
		t.Fatalf("left-behind files were disturbed: %q", st)
	}
	if body := gitIn(t, dir, "log", "-1", "--format=%B"); strings.Contains(strings.ToLower(body), "co-authored-by") {
		t.Fatalf("a trailer was added: %q", body)
	}
}
