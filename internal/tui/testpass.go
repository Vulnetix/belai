package tui

import (
	"context"
	"strconv"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/filediff"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sandbox"
	"github.com/vulnetix/belai/internal/testpass"
)

// testPassState is the TUI's post-end test pass bookkeeping. The pass runs off
// the UI loop, and its fail branch is turn-driven: a failing run starts an
// ordinary visible turn on the main session (goal mode for a fix, plan mode
// for a diagnosis), and that turn's end re-runs the suites.
type testPassState struct {
	// running is true while the suites run off the UI loop.
	running bool
	// fixing is true while a fail-branch turn is in flight.
	fixing bool
	// trigger and fixPasses carry the pass across its fail-branch turns.
	trigger   testpass.Trigger
	fixPasses int
	// edited is true once the session changed a file since the last pass, so
	// a session-end pass never spends a suite on an idle session.
	edited bool
	// cancel stops the suites of a running pass.
	cancel context.CancelFunc
}

// testPassMsg carries one run of the suites back to the UI loop.
type testPassMsg struct {
	trigger testpass.Trigger
	step    testpass.Step
}

// noteTaskEdit records that the session changed a file. It is fed by the same
// observed diffs auto-commit collects.
func (a *App) noteTaskEdit(ch *filediff.Change) {
	if ch != nil && len(ch.Files) > 0 {
		a.testPass.edited = true
	}
}

// resetTestPass drops the pass state for a new conversation.
func (a *App) resetTestPass() {
	a.cancelTestPass()
	a.testPass = testPassState{}
}

// flushTestPass decides, at the end of a successful turn, whether a test pass
// starts or continues. planDone is true when the turn completed an approved
// plan. Plan mode changes nothing, so it never runs one.
func (a *App) flushTestPass(res run.Result, planDone bool) tea.Cmd {
	if !a.settings.TestsEnabled() || a.testPass.running || a.mode == "plan" {
		return nil
	}
	if a.testPass.fixing {
		// A fail-branch turn ended: re-run the suites and count the pass.
		a.testPass.fixing = false
		return a.startTestPass(a.testPass.trigger, a.testPass.fixPasses)
	}
	if res.GoalSentinel != rolemanager.GoalComplete {
		return nil
	}
	trigger := testpass.TriggerGoal
	if planDone {
		trigger = testpass.TriggerPlan
	}
	if !testpass.Should(a.settings, trigger) {
		return nil
	}
	return a.startTestPass(trigger, 0)
}

// startTestPass snapshots every input on the UI loop and runs the suites off
// it as a tea.Cmd.
func (a *App) startTestPass(trigger testpass.Trigger, fixPasses int) tea.Cmd {
	a.testPass.running = true
	a.testPass.trigger = trigger
	pol := a.effectivePosture()
	cfg := testpass.Config{
		Settings: a.settings,
		Dir:      a.workdir,
		Suites:   append(a.repoMap.TestSuites[:0:0], a.repoMap.TestSuites...),
		Perms:    permissions.From(a.settings.Permissions.Allow, a.settings.Permissions.Ask, a.settings.Permissions.Deny),
		Sandbox:  sandbox.FromSettings(a.settings.Sandbox, append([]string{a.workdir}, a.workspaceDirs...), pol),
		Gate:     testpass.NewGate(a.cfg, a.client, a.cache, pol),
	}
	if a.client != nil {
		cfg.Report = run.NewRoleClassifier(a.cfg, a.client, nil)
	}
	m := a.repoMap
	cfg.Changed = func() []string {
		m.RefreshStatus(context.Background())
		out := make([]string, 0, len(m.Changed))
		for _, c := range m.Changed {
			out = append(out, c.Path)
		}
		return out
	}
	if fixPasses == 0 {
		a.addSystem("running tests: " + string(trigger))
	} else {
		a.addSystem("re-running tests after fix pass " + strconv.Itoa(fixPasses))
	}
	// The pass owns its context: a.ctx is the previous turn's, cancelled when
	// that turn ended. Quitting again cancels the suites.
	ctx, cancel := context.WithCancel(context.Background())
	a.testPass.cancel = cancel
	return func() tea.Msg {
		defer cancel()
		return testPassMsg{trigger: trigger, step: testpass.RunOnce(ctx, cfg, trigger, fixPasses)}
	}
}

// cancelTestPass stops a running pass. It is safe to call with none running.
func (a *App) cancelTestPass() {
	if a.testPass.cancel != nil {
		a.testPass.cancel()
		a.testPass.cancel = nil
	}
}

// handleTestPass reports one run and, for a failing run with budget left,
// starts the fail-branch turn.
func (a *App) handleTestPass(m testPassMsg) tea.Cmd {
	a.testPass.running = false
	o := m.step.Outcome
	a.addSystem(o.Line())
	if o.Skipped != "" {
		a.testPass.fixPasses = 0
		return nil
	}
	a.testPass.edited = false
	a.repoMap.LastTestRun = o.Summary()
	if fix := m.step.Fix; fix != nil {
		a.testPass.fixing = true
		a.testPass.fixPasses = fix.Pass
		prompt := testpass.FixPrompt(*fix)
		a.harnessPrompt = prompt
		if fix.Mode == "diagnose" {
			// A read-only diagnosis is one turn: nothing changes, so there is
			// nothing to re-run.
			a.testPass.fixing = false
			a.forceMode = modes.ModePlan
		} else {
			a.forceMode = modes.ModeGoal
		}
		return a.sendWithAttachments(prompt, testpass.FixAttachments(*fix), "")
	}
	a.testPass.fixPasses = 0
	if o.Report != "" {
		a.addSystem(o.Report)
	}
	return nil
}

// quitTestPass runs the session-end pass when the user asks to quit, if the
// settings ask for it and the session edited something. It returns the
// command to run and true when the quit is deferred to the pass; a second
// quit request while the pass is pending exits at once.
func (a *App) quitTestPass() (tea.Cmd, bool) {
	if a.testPass.running || a.testPass.fixing {
		return nil, false
	}
	if !testpass.Should(a.settings, testpass.TriggerSession) || !a.testPass.edited || a.mode == "plan" {
		return nil, false
	}
	a.addSystem("running the session-end test pass before exit; quit again to exit now")
	return a.startTestPass(testpass.TriggerSession, 0), true
}
