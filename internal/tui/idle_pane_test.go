package tui

import (
	"testing"

	"github.com/vulnetix/belai/internal/config"
)

func idleApp(t *testing.T, pane string) *App {
	t.Helper()
	a := New(Options{})
	a.width, a.height = 120, 40
	if pane != "" {
		a.settings.UI = &config.UISettings{IdlePane: ptrString(pane)}
	}
	return a
}

func TestIdlePaneDefaultsToNone(t *testing.T) {
	a, _ := kanbanApp(t)
	a.settings.UI = nil
	addItem(t, a, "open work", "review")
	if a.kanbanPaneVisible() {
		t.Fatal("the kanban pane showed with no idle pane chosen")
	}
	a = idleApp(t, "")
	a.syncIdlePane()
	if a.runsOpen {
		t.Fatal("a runs tab showed with no idle pane chosen")
	}
	if got := (config.Settings{UI: &config.UISettings{IdlePane: ptrString("bogus")}}).IdlePane(); got != config.IdlePaneNone {
		t.Fatalf("an unknown value read as %q", got)
	}
}

func TestIdlePaneOtherChoiceHidesKanban(t *testing.T) {
	a, _ := kanbanApp(t)
	addItem(t, a, "open work", "review")
	a.settings.UI = &config.UISettings{IdlePane: ptrString(config.IdlePaneActivity)}
	if a.kanbanPaneVisible() {
		t.Fatal("the kanban pane showed while another pane was chosen")
	}
}

func TestIdlePaneRunsTabOpensAndClosesWithTheComposer(t *testing.T) {
	a := idleApp(t, config.IdlePaneProcesses)
	a.syncIdlePane()
	if !a.runsOpen || !a.runsIdle || a.runsFocus || a.runsTab != tabProcesses {
		t.Fatalf("empty composer: open=%v idle=%v focus=%v tab=%d", a.runsOpen, a.runsIdle, a.runsFocus, a.runsTab)
	}
	a.editor.SetValue("hello")
	a.syncIdlePane()
	if a.runsOpen || a.runsIdle {
		t.Fatal("typing left the idle pane open")
	}
	a.editor.SetValue("")
	a.syncIdlePane()
	if !a.runsOpen || !a.runsIdle {
		t.Fatal("clearing the composer did not bring the pane back")
	}
}

func TestIdlePaneNeverClosesAPanelTheUserOpened(t *testing.T) {
	a := idleApp(t, config.IdlePaneProcesses)
	a.toggleRunsPanel(tabActivity)
	a.runsFocus = false
	a.syncIdlePane()
	if !a.runsOpen || a.runsIdle || a.runsTab != tabActivity {
		t.Fatalf("the user's panel was taken over: open=%v idle=%v tab=%d", a.runsOpen, a.runsIdle, a.runsTab)
	}
	a.editor.SetValue("x")
	a.syncIdlePane()
	if !a.runsOpen {
		t.Fatal("typing closed a panel the user opened")
	}
}

func TestIdlePaneTakenByDownStaysOpenWhileTyping(t *testing.T) {
	a := idleApp(t, config.IdlePaneActivity)
	a.syncIdlePane()
	if !a.takeIdleRuns() || a.runsIdle || !a.runsFocus {
		t.Fatalf("down did not take the pane: idle=%v focus=%v", a.runsIdle, a.runsFocus)
	}
	a.runsFocus = false
	a.editor.SetValue("x")
	a.syncIdlePane()
	if !a.runsOpen {
		t.Fatal("a pane the user took closed when they typed")
	}
}

func TestIdlePaneClosedByTheUserStaysClosed(t *testing.T) {
	a := idleApp(t, config.IdlePaneActivity)
	a.syncIdlePane()
	a.toggleRunsPanel(tabActivity) // takes it
	a.toggleRunsPanel(tabActivity) // closes it
	a.syncIdlePane()
	if a.runsOpen {
		t.Fatal("the pane reopened straight after the user closed it")
	}
	a.editor.SetValue("x")
	a.syncIdlePane()
	a.editor.SetValue("")
	a.syncIdlePane()
	if !a.runsOpen || !a.runsIdle {
		t.Fatal("the pane did not return once the composer had been used")
	}
}

func TestIdlePaneHonoursOfferedTabs(t *testing.T) {
	a := idleApp(t, config.IdlePaneCI) // no PR/MR, so the ci tab is not offered
	a.syncIdlePane()
	if a.runsOpen {
		t.Fatal("an unoffered tab showed")
	}
}

func TestIdlePaneSettingRowCyclesAndUnsets(t *testing.T) {
	seen := map[string]bool{}
	for _, n := range config.IdlePaneNames {
		seen[n] = true
	}
	for _, want := range []string{"none", "kanban", "activity", "helpers", "processes", "crew", "git", "ci", "intel"} {
		if !seen[want] {
			t.Fatalf("idle pane choices lack %q", want)
		}
	}
	if config.IdlePaneNames[0] != config.IdlePaneNone {
		t.Fatal("none must be the first choice")
	}
	if settingsGroupOf("idle_pane") != "display" {
		t.Fatal("the idle pane row is not in Display")
	}
}
