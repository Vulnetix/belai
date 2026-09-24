package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// viewState selects which full-screen view is active.
type viewState int

const (
	viewChat viewState = iota
	viewProviders
	viewProviderDetail
	viewProviderNew
	viewSettings
	viewPermissions
	viewModel
	viewImport
	viewAgent
	viewClarify
	viewPermissionAsk
	viewPlanReview
	viewResume
	viewResumeCompact
	viewVulnetixConfig
	viewVulnetixList
	viewVulnetixArtifacts
	viewRunsOutput
	viewPrompts
	viewProcesses
	viewLSP
	viewScreens
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
	viewHandlers[viewProviders] = viewHandler{name: "providers", enter: (*App).enterProviders, key: (*App).handleProvidersKey, render: (*App).providersView}
	viewHandlers[viewProviderDetail] = viewHandler{name: "provider-detail", key: (*App).handleProviderDetailKey, render: (*App).providerDetailView}
	viewHandlers[viewProviderNew] = viewHandler{name: "provider-new", key: (*App).handleProviderNewKey, render: (*App).providerNewView}
	viewHandlers[viewSettings] = viewHandler{name: "settings", key: (*App).handleSettingsKey, render: (*App).settingsView}
	viewHandlers[viewPermissions] = viewHandler{name: "permissions", key: (*App).handlePermissionsKey, render: (*App).permissionsView}
	viewHandlers[viewModel] = viewHandler{name: "model", enter: (*App).enterModel, key: (*App).handleModelKey, render: (*App).modelView}
	viewHandlers[viewImport] = viewHandler{name: "import", enter: (*App).enterImport, key: (*App).handleImportKey, render: (*App).importView}
	viewHandlers[viewAgent] = viewHandler{name: "agent", enter: (*App).enterAgentView, key: (*App).handleAgentKey, render: (*App).agentView}
	viewHandlers[viewClarify] = viewHandler{name: "clarify", key: (*App).handleClarifyKey, render: (*App).clarifyView}
	viewHandlers[viewPermissionAsk] = viewHandler{name: "permission-ask", key: (*App).handlePermissionAskKey, render: (*App).permissionAskView}
	viewHandlers[viewPlanReview] = viewHandler{name: "plan-review", enter: (*App).enterPlanReview, key: (*App).handlePlanReviewKey, render: (*App).planReviewView}
	viewHandlers[viewResume] = viewHandler{name: "resume", enter: (*App).enterResume, key: (*App).handleResumeKey, render: (*App).resumeView}
	viewHandlers[viewResumeCompact] = viewHandler{name: "resume-compact", key: (*App).handleResumeCompactKey, render: (*App).resumeCompactView}
	viewHandlers[viewVulnetixConfig] = viewHandler{name: "vulnetix-config", enter: (*App).enterVulnetixConfig, key: (*App).handleVulnetixConfigKey, render: (*App).vulnetixConfigView}
	viewHandlers[viewVulnetixList] = viewHandler{name: "vulnetix-list", enter: (*App).enterVulnetixList, key: (*App).handleVulnetixListKey, render: (*App).vulnetixListView}
	viewHandlers[viewVulnetixArtifacts] = viewHandler{name: "vulnetix-artifacts", key: (*App).handleVulnetixArtifactsKey, render: (*App).vulnetixArtifactsView}
	viewHandlers[viewRunsOutput] = viewHandler{name: "runs-output", enter: (*App).enterRunsOutput, key: (*App).handleRunsOutputKey, render: (*App).runsOutputView}
	viewHandlers[viewPrompts] = viewHandler{name: "prompts", enter: (*App).enterPrompts, key: (*App).handlePromptsKey, render: (*App).promptsView}
	viewHandlers[viewProcesses] = viewHandler{name: "processes", enter: (*App).enterProcesses, key: (*App).handleProcessesKey, render: (*App).processesView}
	viewHandlers[viewLSP] = viewHandler{name: "lsp", enter: (*App).enterLSP, key: (*App).handleLSPKey, render: (*App).lspView}
	viewHandlers[viewScreens] = viewHandler{name: "screens", enter: (*App).enterScreens, key: (*App).handleScreensKey, render: (*App).screensView}
}

// push navigates to a full-screen view, remembering the current one on the
// stack so a nested view pops back to its parent rather than chat.
func (a *App) push(v viewState) tea.Cmd {
	a.viewStack = append(a.viewStack, v)
	a.view = v
	a.editor.Reset()
	a.editor.Masked = false
	a.clearLoadedPrompt()
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
	a.clearLoadedPrompt()
	a.restoreChatDraft()
}

// popToChat leaves every full-screen view at once.
func (a *App) popToChat() {
	a.viewStack = nil
	a.view = viewChat
	a.editor.Reset()
	a.editor.Masked = false
	a.clearLoadedPrompt()
	a.restoreChatDraft()
}

// restoreChatDraft puts back the composer text the screen switcher set aside
// when it was opened from chat.
func (a *App) restoreChatDraft() {
	if a.view != viewChat || a.chatDraft == "" {
		return
	}
	a.editor.SetValue(a.chatDraft)
	a.editor.CursorEnd()
	a.chatDraft = ""
}
