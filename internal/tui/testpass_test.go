package tui

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/filediff"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/repomap"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/testpass"
)

// testPassApp is an app whose tests block runs the given command in a temp
// directory.
func testPassApp(t *testing.T, postEnd string, command ...string) *App {
	t.Helper()
	// The persisted mode and settings live under BELAI_HOME: keep the
	// developer's own out of the test, or a last-used plan mode changes what
	// runs.
	t.Setenv("BELAI_HOME", t.TempDir())
	a := New(Options{})
	a.workdir = t.TempDir()
	a.settings.Tests = &config.TestsSettings{PostEnd: postEnd, Command: command}
	// Isolate from the host: no provider client or credentials (a model-written
	// report would be a live call), no classifier round trip (all-ignore
	// posture sanitises only), and no scan of the real repository's suites.
	a.client = nil
	a.cfg = run.Config{}
	a.posture = posture.AllIgnore()
	a.repoMap = repomap.Map{}
	a.mode = "agent"
	return a
}

func goalDone() run.Result { return run.Result{GoalSentinel: rolemanager.GoalComplete} }

func TestFlushTestPassOffStartsNothing(t *testing.T) {
	a := testPassApp(t, "off", "sh", "-c", "exit 0")
	if cmd := a.flushTestPass(goalDone(), false); cmd != nil || a.testPass.running {
		t.Fatal("post_end off must not run the suites")
	}
}

func TestFlushTestPassOnlyAfterACompletedGoal(t *testing.T) {
	a := testPassApp(t, "goal", "sh", "-c", "exit 0")
	if cmd := a.flushTestPass(run.Result{GoalSentinel: rolemanager.GoalPartial}, false); cmd != nil {
		t.Fatal("a partial goal must not run the suites")
	}
	if cmd := a.flushTestPass(run.Result{}, false); cmd != nil {
		t.Fatal("a plain agent turn must not run the suites")
	}
}

func TestFlushTestPassGoalLevelRunsAndReportsAPass(t *testing.T) {
	a := testPassApp(t, "goal", "sh", "-c", "exit 0")
	a.testPass.edited = true
	cmd := a.flushTestPass(goalDone(), false)
	if cmd == nil || !a.testPass.running {
		t.Fatal("a completed goal at goal level must start the suites")
	}
	msg, ok := cmd().(testPassMsg)
	if !ok {
		t.Fatalf("cmd result = %T", cmd())
	}
	a.handleTestPass(msg)
	if a.testPass.running || a.testPass.edited {
		t.Fatalf("state after a pass = %+v", a.testPass)
	}
	if a.repoMap.LastTestRun == nil || !a.repoMap.LastTestRun.Passed {
		t.Fatalf("last test run = %+v", a.repoMap.LastTestRun)
	}
	if got := lastSystemMessage(a); !strings.HasPrefix(got, "Tests passed:") {
		t.Fatalf("last system message = %q", got)
	}
}

func TestFlushTestPassPlanNeedsGoalPlanLevel(t *testing.T) {
	a := testPassApp(t, "goal", "sh", "-c", "exit 0")
	if cmd := a.flushTestPass(goalDone(), true); cmd != nil {
		t.Fatal("goal level must not run after a plan")
	}
	b := testPassApp(t, "goal_plan", "sh", "-c", "exit 0")
	if cmd := b.flushTestPass(goalDone(), true); cmd == nil || b.testPass.trigger != testpass.TriggerPlan {
		t.Fatal("goal_plan level must run after a plan")
	}
}

func TestFlushTestPassNeverRunsInPlanMode(t *testing.T) {
	a := testPassApp(t, "session", "sh", "-c", "exit 0")
	a.mode = "plan"
	if cmd := a.flushTestPass(goalDone(), false); cmd != nil {
		t.Fatal("plan mode changes nothing, so it must not run the suites")
	}
}

func TestFlushTestPassDoesNotOverlap(t *testing.T) {
	a := testPassApp(t, "goal", "sh", "-c", "exit 0")
	a.testPass.running = true
	if cmd := a.flushTestPass(goalDone(), false); cmd != nil {
		t.Fatal("a running pass must not start a second one")
	}
}

// A failing run starts a visible goal-mode turn with the harness prompt and
// the gated output; the turn's end re-runs the suites.
func TestFailingRunStartsFixTurnThenReruns(t *testing.T) {
	a := testPassApp(t, "goal", "sh", "-c", "exit 1")
	a.testPass.edited = true
	msg := a.flushTestPass(goalDone(), false)().(testPassMsg)
	if msg.step.Fix == nil {
		t.Fatalf("a failing run must offer a fix: %+v", msg.step.Outcome)
	}
	cmd := a.handleTestPass(msg)
	if cmd == nil {
		t.Fatal("the fail branch must start a turn")
	}
	if !a.testPass.fixing || a.testPass.fixPasses != 1 {
		t.Fatalf("state = %+v", a.testPass)
	}
	if a.forceMode != modes.ModeGoal && a.forceMode != "" {
		t.Fatalf("forceMode = %q", a.forceMode)
	}
	// The run resolved the edits it covered; the fix turn's own edits set the
	// flag again through noteTaskEdit.
	if a.testPass.edited {
		t.Fatal("a completed run must clear the edited flag")
	}
	if a.repoMap.LastTestRun == nil || a.repoMap.LastTestRun.Passed {
		t.Fatalf("last test run = %+v", a.repoMap.LastTestRun)
	}

	// The fix turn ends: the suites run again, counted as pass 1.
	again := a.flushTestPass(goalDone(), false)
	if again == nil || a.testPass.fixing || !a.testPass.running {
		t.Fatalf("re-run not started: %+v", a.testPass)
	}
	if got := lastSystemMessage(a); !strings.Contains(got, "fix pass 1") {
		t.Fatalf("last system message = %q", got)
	}
}

func TestFixBudgetEndsTheLoopInTheTUI(t *testing.T) {
	a := testPassApp(t, "goal", "sh", "-c", "exit 1")
	a.settings.Tests.MaxFixPasses = 1
	msg := a.flushTestPass(goalDone(), false)().(testPassMsg)
	a.handleTestPass(msg)
	msg = a.flushTestPass(goalDone(), false)().(testPassMsg)
	if msg.step.Fix != nil {
		t.Fatalf("budget spent, yet a fix was offered: %+v", msg.step.Fix)
	}
	if cmd := a.handleTestPass(msg); cmd != nil {
		t.Fatal("no turn may start once the budget is spent")
	}
	if a.testPass.fixing || a.testPass.fixPasses != 0 {
		t.Fatalf("state = %+v", a.testPass)
	}
	if got := lastSystemMessage(a); !strings.HasPrefix(got, "tests fail") {
		t.Fatalf("last system message = %q", got)
	}
}

func TestDiagnoseRunsOneReadOnlyTurnWithoutARerun(t *testing.T) {
	a := testPassApp(t, "goal", "sh", "-c", "exit 1")
	a.settings.Tests.OnFail = "diagnose"
	msg := a.flushTestPass(goalDone(), false)().(testPassMsg)
	if cmd := a.handleTestPass(msg); cmd == nil {
		t.Fatal("diagnose must start its one turn")
	}
	if a.testPass.fixing {
		t.Fatal("a read-only diagnosis has nothing to re-run")
	}
}

func TestOnFailOffOnlyReports(t *testing.T) {
	a := testPassApp(t, "goal", "sh", "-c", "exit 1")
	a.settings.Tests.OnFail = "off"
	msg := a.flushTestPass(goalDone(), false)().(testPassMsg)
	if cmd := a.handleTestPass(msg); cmd != nil || a.testPass.fixing {
		t.Fatal("on_fail off must not start a turn")
	}
}

func TestSkippedPassIsReportedAndLeavesRepoMapAlone(t *testing.T) {
	a := testPassApp(t, "goal") // no command and no detected suite
	msg := a.flushTestPass(goalDone(), false)().(testPassMsg)
	a.handleTestPass(msg)
	if a.repoMap.LastTestRun != nil {
		t.Fatalf("a skipped pass must not set a result: %+v", a.repoMap.LastTestRun)
	}
	if got := lastSystemMessage(a); got != "tests: no test suite detected" {
		t.Fatalf("last system message = %q", got)
	}
}

func TestErroredTurnEndsTheFailBranch(t *testing.T) {
	a := testPassApp(t, "goal", "sh", "-c", "exit 1")
	a.testPass.fixing = true
	a.testPass.fixPasses = 1
	// An errored turn clears fixing, so the next flush is an ordinary one.
	a.testPass.fixing = false
	if cmd := a.flushTestPass(run.Result{}, false); cmd != nil {
		t.Fatal("no re-run after an errored turn")
	}
}

func TestNoteTaskEditMarksTheSessionEdited(t *testing.T) {
	a := testPassApp(t, "session", "sh", "-c", "exit 0")
	a.noteTaskEdit(nil)
	a.noteTaskEdit(&filediff.Change{})
	if a.testPass.edited {
		t.Fatal("an empty change must not mark the session edited")
	}
	a.noteTaskEdit(&filediff.Change{Files: []filediff.FileChange{{Path: "a.go"}}})
	if !a.testPass.edited {
		t.Fatal("an observed edit must mark the session edited")
	}
}

func TestQuitRunsTheSessionEndPassOnlyWhenDueAndThenExits(t *testing.T) {
	// Not at session level: no deferral.
	a := testPassApp(t, "goal", "sh", "-c", "exit 0")
	a.testPass.edited = true
	if _, deferred := a.quitTestPass(); deferred {
		t.Fatal("goal level must not defer the quit")
	}
	// Session level but nothing edited: no deferral.
	b := testPassApp(t, "session", "sh", "-c", "exit 0")
	if _, deferred := b.quitTestPass(); deferred {
		t.Fatal("an idle session must quit at once")
	}
	// Session level with edits: the pass runs, and a second quit exits.
	c := testPassApp(t, "session", "sh", "-c", "exit 0")
	c.testPass.edited = true
	cmd, deferred := c.quitTestPass()
	if !deferred || cmd == nil || !c.testPass.running || c.testPass.trigger != testpass.TriggerSession {
		t.Fatalf("session-end pass not started: deferred=%v state=%+v", deferred, c.testPass)
	}
	if _, again := c.quitTestPass(); again {
		t.Fatal("a second quit while the pass runs must exit at once")
	}
	c.cancelTestPass()
}

func TestResetTestPassClearsState(t *testing.T) {
	a := testPassApp(t, "goal", "sh", "-c", "exit 0")
	a.testPass = testPassState{running: true, fixing: true, fixPasses: 2, edited: true}
	a.resetTestPass()
	if s := a.testPass; s.running || s.fixing || s.fixPasses != 0 || s.edited || s.cancel != nil {
		t.Fatalf("state = %+v", a.testPass)
	}
}

// eachTurnApp is a test app with only tests.each_turn on; post_end stays off.
func eachTurnApp(t *testing.T, command ...string) *App {
	t.Helper()
	a := testPassApp(t, "off", command...)
	on := true
	a.settings.Tests.EachTurn = &on
	a.testPass.edited = true
	return a
}

func TestEachTurnStartsAPassAfterAnEditedPlainTurn(t *testing.T) {
	a := eachTurnApp(t, "sh", "-c", "exit 0")
	cmd := a.flushTestPass(run.Result{}, false)
	if cmd == nil || !a.testPass.running || a.testPass.trigger != testpass.TriggerTurn {
		t.Fatalf("a plain edited turn must start the suites: %+v", a.testPass)
	}
	a.handleTestPass(cmd().(testPassMsg))
	if a.testPass.edited || a.repoMap.LastTestRun == nil || !a.repoMap.LastTestRun.Passed {
		t.Fatalf("state after the pass = %+v, %+v", a.testPass, a.repoMap.LastTestRun)
	}
}

func TestEachTurnStartsNothingWhenGuardsFail(t *testing.T) {
	cases := map[string]func(a *App) run.Result{
		"setting off": func(a *App) run.Result {
			a.settings.Tests.EachTurn = nil
			return run.Result{}
		},
		"no edit": func(a *App) run.Result {
			a.testPass.edited = false
			return run.Result{}
		},
		"plan mode": func(a *App) run.Result {
			a.mode = "plan"
			return run.Result{}
		},
		"goal mode": func(a *App) run.Result {
			a.mode = "goal"
			return run.Result{}
		},
		"code mode": func(a *App) run.Result {
			a.mode = "code"
			return run.Result{}
		},
		"goal turn": func(a *App) run.Result {
			return run.Result{GoalSentinel: rolemanager.GoalPartial}
		},
		"plan sentinel": func(a *App) run.Result {
			return run.Result{PlanSentinel: rolemanager.PlanSentinel("x")}
		},
		"approved plan executing": func(a *App) run.Result {
			a.planExecuting = true
			return run.Result{}
		},
		"auto picked goal": func(a *App) run.Result {
			a.mode = "auto"
			return run.Result{ModeDecision: rolemanager.ModeDecision{Mode: modes.ModeGoal}}
		},
		"already running": func(a *App) run.Result {
			a.testPass.running = true
			return run.Result{}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			a := eachTurnApp(t, "sh", "-c", "exit 0")
			res := setup(a)
			if cmd := a.flushTestPass(res, false); cmd != nil {
				t.Fatalf("%s must not start a pass", name)
			}
		})
	}
}

func TestEachTurnDoesNotOverrideAnAutoAgentChoice(t *testing.T) {
	a := eachTurnApp(t, "sh", "-c", "exit 0")
	a.mode = "auto"
	res := run.Result{ModeDecision: rolemanager.ModeDecision{Mode: modes.ModeAgent}}
	if cmd := a.flushTestPass(res, false); cmd == nil {
		t.Fatal("auto mode that chose agent is a plain turn")
	}
}

// The fix loop a turn pass starts is bounded by tests.max_fix_passes and the
// fix turn's own end re-runs the suites rather than starting a fresh pass.
func TestEachTurnFixLoopIsBoundedAndNotRetriggered(t *testing.T) {
	a := eachTurnApp(t, "sh", "-c", "exit 1")
	a.settings.Tests.MaxFixPasses = 1
	msg := a.flushTestPass(run.Result{}, false)().(testPassMsg)
	if msg.step.Fix == nil {
		t.Fatalf("a failing run must offer a fix: %+v", msg.step.Outcome)
	}
	if cmd := a.handleTestPass(msg); cmd == nil || !a.testPass.fixing {
		t.Fatal("the fail branch must start a fix turn")
	}
	// The fix turn edits files, then ends as a plain turn.
	a.testPass.edited = true
	again := a.flushTestPass(run.Result{}, false)
	if again == nil || a.testPass.fixing || a.testPass.trigger != testpass.TriggerTurn || !a.testPass.running {
		t.Fatalf("the fix turn's end must re-run the suites: %+v", a.testPass)
	}
	msg = again().(testPassMsg)
	if msg.step.Fix != nil {
		t.Fatalf("budget spent, yet a fix was offered: %+v", msg.step.Fix)
	}
	if cmd := a.handleTestPass(msg); cmd != nil || a.testPass.fixing {
		t.Fatal("no turn may start once the budget is spent")
	}
	if cmd := a.flushTestPass(run.Result{}, false); cmd != nil {
		t.Fatal("the loop ended with nothing edited since: no new pass")
	}
}

func TestEachTurnSkippedPassStartsNoFixBranch(t *testing.T) {
	a := eachTurnApp(t) // no command and no detected suite
	msg := a.flushTestPass(run.Result{}, false)().(testPassMsg)
	if cmd := a.handleTestPass(msg); cmd != nil || a.testPass.fixing {
		t.Fatal("no suite ran, so there is nothing to fix")
	}
}

// A project layer cannot turn the setting on, so a repository cannot make the
// session run its suites after each turn.
func TestEachTurnProjectLayerCannotEnableIt(t *testing.T) {
	a := testPassApp(t, "off", "sh", "-c", "exit 0")
	on := true
	a.settings = a.settings.Override(config.Settings{Tests: &config.TestsSettings{EachTurn: &on}})
	a.testPass.edited = true
	if cmd := a.flushTestPass(run.Result{}, false); cmd != nil {
		t.Fatal("a project each_turn true must not start a pass")
	}
}
