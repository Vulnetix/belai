package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/config"
)

// The idle pane: the one pane shown above an empty composer while the chat is
// idle (ui.idle_pane, none by default). The kanban board is drawn by its own
// composer pane (kanban.go); every other choice is a runs panel tab opened
// unfocused and closed again when the composer stops being empty. It is a
// display choice only: it reads no content, calls no model and changes no
// permission, and the tab shows exactly what the panel would.

// idlePaneTabs maps a ui.idle_pane name to the runs panel tab it shows.
var idlePaneTabs = map[string]int{
	config.IdlePaneActivity:  tabActivity,
	config.IdlePaneHelpers:   tabSubagents,
	config.IdlePaneProcesses: tabProcesses,
	config.IdlePaneCrew:      tabCrew,
	config.IdlePaneGit:       tabGit,
	config.IdlePaneCI:        tabCI,
	config.IdlePaneIntel:     tabIntel,
}

// idlePaneTab returns the runs tab ui.idle_pane names, and false for none and
// for the kanban pane.
func (a *App) idlePaneTab() (int, bool) {
	tab, ok := idlePaneTabs[a.settings.IdlePane()]
	return tab, ok
}

// idleChatQuiet reports whether the chat is idle with nothing else claiming the
// space above the composer. It does not look at the composer's text or at the
// runs panel.
func (a *App) idleChatQuiet() bool {
	if a.view != viewChat || a.phase != phaseIdle || a.preSend || a.working() {
		return false
	}
	return len(a.autocomplete) == 0 && !a.agentPickerVisible() && !a.promptPickerVisible() && !a.dirPickVisible() &&
		!a.filePickerVisible() && !a.rootConfirmVisible() && len(a.attachments) == 0 && !a.historyActive &&
		!a.savePromptMode && !a.saveFileMode && !a.promptAction && !a.forgeFlowActive() && !a.kanbanInputActive() && !a.reviewActive()
}

// composerEmpty reports whether the composer holds nothing to send.
func (a *App) composerEmpty() bool {
	return strings.TrimSpace(a.editor.Value()) == "" && !a.editor.Masked
}

// idleRunsWanted reports whether the idle pane is a runs tab that should show
// now: the composer is empty, the chat is quiet and the tab is offered.
func (a *App) idleRunsWanted() bool {
	tab, ok := a.idlePaneTab()
	if !ok || !a.runsTabVisible(tab) || a.height < minRunsPanelOpenHeight {
		return false
	}
	return a.idleChatQuiet() && a.composerEmpty()
}

// syncIdlePane opens the idle runs tab when the composer becomes empty and the
// chat quiet, and closes it when that stops being true. It acts on the edge
// only: a panel the user opened is never touched, and one they closed stays
// closed until the composer is next in use or the setting changes. Taking the
// keyboard (down, f9) makes the pane an ordinary panel, so typing no longer
// closes it.
func (a *App) syncIdlePane() tea.Cmd {
	if a.runsIdle && !a.runsOpen {
		a.runsIdle = false // something else closed it
	}
	pane := a.settings.IdlePane()
	if pane != a.idleSeen {
		a.idleSeen = pane
		a.idleArmed = true
		if a.runsIdle {
			a.runsOpen, a.runsFocus, a.runsIdle = false, false, false
		}
	}
	if !a.idleChatQuiet() || !a.composerEmpty() {
		a.idleArmed = true
	}
	wanted := a.idleRunsWanted()
	switch {
	case a.runsIdle && !wanted:
		a.runsOpen, a.runsFocus, a.runsIdle = false, false, false
	case wanted && a.idleArmed && !a.runsOpen:
		tab, _ := a.idlePaneTab()
		a.idleArmed = false
		a.runsOpen, a.runsFocus, a.runsIdle = true, false, true
		a.runsTab, a.runsSel, a.runsScroll = tab, 0, 0
		a.reloadFleet()
		return a.enterRunsTab()
	}
	return nil
}

// takeIdleRuns turns the idle pane into an ordinary open panel and gives it
// the keyboard. It reports whether there was an idle pane to take.
func (a *App) takeIdleRuns() bool {
	if !a.runsOpen || !a.runsIdle {
		return false
	}
	a.runsIdle = false
	a.runsFocus = true
	a.runsSel = 0
	return true
}
