package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// viewState selects which full-screen view is active.
type viewState int

const (
	viewChat viewState = iota
	viewCredentials
	viewSettings
	viewPermissions
	viewModel
	viewImport
	viewAgent
	viewClarify
	viewPermissionAsk
	viewClassifier
	viewPlanReview
	viewResume
)

// viewHandler is one full-screen view. Chat is the base state and lives
// outside this table.
type viewHandler struct {
	name   string
	enter  func(*App) tea.Cmd
	key    func(*App, tea.KeyMsg) (tea.Model, tea.Cmd)
	render func(*App) string
}

var viewHandlers = map[viewState]viewHandler{}

func init() {
	viewHandlers[viewCredentials] = viewHandler{name: "credentials", key: (*App).handleCredentialKey, render: (*App).credentialView}
	viewHandlers[viewSettings] = viewHandler{name: "settings", key: (*App).handleSettingsKey, render: (*App).settingsView}
	viewHandlers[viewPermissions] = viewHandler{name: "permissions", key: (*App).handlePermissionsKey, render: (*App).permissionsView}
	viewHandlers[viewModel] = viewHandler{name: "model", enter: (*App).enterModel, key: (*App).handleModelKey, render: (*App).modelView}
	viewHandlers[viewImport] = viewHandler{name: "import", enter: (*App).enterImport, key: (*App).handleImportKey, render: (*App).importView}
	viewHandlers[viewAgent] = viewHandler{name: "agent", enter: (*App).enterAgentView, key: (*App).handleAgentKey, render: (*App).agentView}
	viewHandlers[viewClarify] = viewHandler{name: "clarify", key: (*App).handleClarifyKey, render: (*App).clarifyView}
	viewHandlers[viewPermissionAsk] = viewHandler{name: "permission-ask", key: (*App).handlePermissionAskKey, render: (*App).permissionAskView}
	viewHandlers[viewClassifier] = viewHandler{name: "classifier", enter: (*App).enterClassifier, key: (*App).handleClassifierKey, render: (*App).classifierView}
	viewHandlers[viewPlanReview] = viewHandler{name: "plan-review", enter: (*App).enterPlanReview, key: (*App).handlePlanReviewKey, render: (*App).planReviewView}
	viewHandlers[viewResume] = viewHandler{name: "resume", enter: (*App).enterResume, key: (*App).handleResumeKey, render: (*App).resumeView}
}

// push navigates to a full-screen view, remembering the current one on the
// stack so a nested view pops back to its parent rather than chat.
func (a *App) push(v viewState) tea.Cmd {
	a.viewStack = append(a.viewStack, v)
	a.view = v
	a.editor.Reset()
	a.editor.Masked = false
	if h, ok := viewHandlers[v]; ok && h.enter != nil {
		return h.enter(a)
	}
	return nil
}

// pop returns to the previous view (or chat). It must reset the editor and
// clear Masked: the editor is shared across chat, credential secret entry,
// settings and permission inline edits, and a secret left in the buffer is a
// leak waiting to happen.
func (a *App) pop() {
	if len(a.viewStack) > 0 {
		a.viewStack = a.viewStack[:len(a.viewStack)-1]
	}
	if len(a.viewStack) > 0 {
		a.view = a.viewStack[len(a.viewStack)-1]
	} else {
		a.view = viewChat
	}
	a.editor.Reset()
	a.editor.Masked = false
}
