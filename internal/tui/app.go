package tui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/signet/internal/agent"
	"github.com/vulnetix/signet/internal/clipboard"
	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/credentials"
	"github.com/vulnetix/signet/internal/gitinfo"
	"github.com/vulnetix/signet/internal/modelinfo"
	"github.com/vulnetix/signet/internal/models"
	"github.com/vulnetix/signet/internal/modes"
	"github.com/vulnetix/signet/internal/permissions"
	"github.com/vulnetix/signet/internal/plans"
	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/profiles"
	"github.com/vulnetix/signet/internal/prompt"
	"github.com/vulnetix/signet/internal/promptlib"
	"github.com/vulnetix/signet/internal/provider"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/session"
	"github.com/vulnetix/signet/internal/tools"
	"github.com/vulnetix/signet/internal/transcript"
	"github.com/vulnetix/signet/internal/tui/components"
	"github.com/vulnetix/signet/internal/version"
)

// Options configures a new TUI app.
type Options struct {
	Workdir  string
	Client   *http.Client
	Resolver *credentials.Resolver // nil means environment only
	Provider string
	Model    string
	Prompt   string           // optional seed turn
	Settings *config.Settings // nil means load from disk
	Posture  posture.Policy   // posture gates; defaults to posture.Defaults()
	PlanMode bool
}

// streamChunkMsg wraps one chunk from the streaming channel (legacy text path).
type streamChunkMsg run.Chunk

// agentEventMsg wraps one agent streaming event.
type agentEventMsg agent.Event

// copiedMsg reports the result of a clipboard copy.
type copiedMsg struct{ text string }

// compactDoneMsg carries the result of an async compaction call.
type compactDoneMsg struct {
	summary string
	err     error
}

// sessionNamedMsg carries the result of an async session-naming call.
type sessionNamedMsg struct {
	name string
	err  error
}

const (
	editorMinHeight = 3
	editorMaxHeight = 12
)

// App is the Bubble Tea model for the Signet TUI.
type App struct {
	registry     *Registry
	messages     []components.Message
	editor       components.Editor
	footer       components.Footer
	width        int
	height       int
	mode         string
	modeExplicit bool // a manual mode choice suppresses classification this turn
	autocomplete []string

	// mode classification (optional; nil skips auto-detection)
	classifier  rolemanager.Classifier
	namedAgent  string
	modeWarning string

	// provider & streaming state
	ctx      context.Context
	cancel   context.CancelFunc
	cfg      run.Config
	status   run.Status
	client   *http.Client
	resolver *credentials.Resolver
	posture  posture.Policy
	planMode bool
	agent    *agent.Session
	events   <-chan agent.Event
	pending  string // pending prompt to send once configured

	// session display overrides (ctrl+r / ctrl+t), shadowing the resolved
	// settings without rewriting the settings file.
	reasoningOverride *bool
	toolCallsOverride *bool

	// attachments state
	attachments  map[int]*attachment
	attachOrder  []int
	attachSeq    int
	attachSpin   spinner.Model
	pendingInput string // prompt held while attachments validate

	// view state
	view            viewState
	viewStack       []viewState
	credentialState credentialViewState
	settingsState   settingsViewState
	modelState      modelViewState
	permState       permissionsViewState
	importState     importViewState

	// live model catalogue cache (on-demand fetch)
	catalogCache   map[string][]models.Model
	catalogErr     map[string]string
	catalogLoading map[string]bool

	// workdir and git
	workdir string
	gitInfo gitinfo.Info
	gitOK   bool

	// settings / state persistence
	settings config.Settings
	eff      config.Effective
	flags    config.Settings
	state    config.State

	// session persistence
	store         *session.Store
	sessionID     string
	sessionName   string
	lastEntryID   string // ParentID for the next append
	nameRequested bool   // auto-naming already attempted for this session
	storeDisabled bool   // a store error was reported; degrade to memory-only

	// context metering
	summary       string            // compaction carrier; "" in a normal session
	parentSession string            // set on a compacted session
	usage         *transcript.Usage // last provider-reported usage
	usageStale    bool              // set by /compact, cleared by fresh usage

	// prompt history / library cycling
	historyActive   bool
	historyQuery    string
	historyOriginal string
	historyIndex    int
	historyResults  []string

	// autocomplete cycling
	autocompleteIndex int

	// save to library
	savePromptMode  bool
	savePromptValue string

	// layout
	vp viewport.Model

	// expandAll disables truncation and shows every message in full.
	expandAll bool
}

type tickMsg time.Time

func tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// New builds a TUI app from Options. It never writes session files: those are
// created lazily on the first Append.
func New(opts Options) *App {
	workdir := opts.Workdir
	if workdir == "" {
		workdir, _ = os.Getwd()
	}

	flags := config.Settings{Provider: opts.Provider, Model: opts.Model}
	eff, err := config.Resolve(workdir, os.Getenv, flags)
	startErr := ""
	if err != nil {
		startErr = err.Error()
		eff = config.Effective{Settings: config.Settings{}, Origin: map[string]config.Source{}}
	}

	st, _ := config.LoadState()
	mode := st.LastMode
	if mode == "" {
		mode = "agent"
	}

	// Provider fallback: a sole configured provider beats the openai default.
	name := eff.Settings.Provider
	if name == "" && opts.Resolver != nil {
		configured := opts.Resolver.ConfiguredProviders()
		if len(configured) == 1 {
			name = configured[0]
		}
	}

	src := run.CredentialSource(run.EnvSource(os.Getenv))
	if opts.Resolver != nil {
		src = opts.Resolver
	}
	cfg, status := run.Prepare(eff.Settings.Model, name, src)
	cfg.Effort = eff.Settings.Effort

	pol := opts.Posture
	if len(pol) == 0 {
		pol = posture.Defaults()
	}

	store, _ := session.NewStore()

	a := &App{
		registry:    NewRegistry(workdir),
		editor:      components.NewEditor(),
		footer:      components.Footer{Session: "new", Model: cfg.Model, Cost: "$0.00"},
		mode:        mode,
		ctx:         context.Background(),
		cfg:         cfg,
		status:      status,
		client:      opts.Client,
		resolver:    opts.Resolver,
		posture:     pol,
		planMode:    mode == "plan",
		pending:     opts.Prompt,
		workdir:     workdir,
		settings:    eff.Settings,
		eff:         eff,
		flags:       flags,
		state:       st,
		vp:          viewport.New(80, 24),
		store:       store,
		sessionID:   session.MustID(),
		attachments: map[int]*attachment{},
		attachSpin:  spinner.New(),
	}
	if a.status.Configured {
		a.SetClassifier(run.NewClassifier(a.cfg, a.client))
	}
	a.refreshGitInfo()
	_ = a.editor.Focus()

	if startErr != "" {
		a.addSystem("settings error: " + startErr)
	} else if !status.Configured {
		a.showCredentialMessage(cfg.Provider, opts.Resolver)
	}

	if opts.Prompt != "" && status.Configured {
		a.messages = append(a.messages, components.Message{Role: "user", Content: opts.Prompt})
	}

	a.initCredentialState()
	a.refreshFooter()
	return a
}

// providerNames returns built-in providers followed by configured custom
// names, sorted within the custom group. The model picker and the credential
// view must read this same ordered list so the 'c' key syncs indices.
func (a *App) providerNames() []string {
	names := append([]string{}, provider.Names()...)
	var custom []string
	for name := range a.settings.Providers {
		if !provider.Builtin(name) {
			custom = append(custom, name)
		}
	}
	sort.Strings(custom)
	return append(names, custom...)
}

// catalogFor returns the selectable models for a provider: the built-in
// catalogue for compiled-in names, the profile's Models for custom names.
// catalogFor returns the selectable models for a provider: the live fetched
// catalogue when available, then the profile's models, then the built-in
// catalogue. It never returns an empty list when a static catalogue exists.
func (a *App) catalogFor(name string) []models.Model {
	var out []models.Model
	seen := map[string]bool{}
	if fetched, ok := a.catalogCache[name]; ok {
		for _, m := range fetched {
			if !seen[m.ID] {
				seen[m.ID] = true
				out = append(out, m)
			}
		}
	}
	if prof, ok := a.settings.Providers[name]; ok {
		for _, m := range prof.Models {
			if !seen[m.ID] {
				seen[m.ID] = true
				label := m.Name
				if label == "" {
					label = m.ID
				}
				out = append(out, models.Model{ID: m.ID, Label: label, ContextWindow: m.ContextWindow})
			}
		}
	}
	if cat := models.Catalog(name); cat != nil {
		for _, m := range cat {
			if !seen[m.ID] {
				seen[m.ID] = true
				out = append(out, m)
			}
		}
	}
	return out
}

func (a *App) showCredentialMessage(provider string, resolver *credentials.Resolver) {
	if resolver == nil {
		a.addSystem("no provider credentials found. Type /credentials to configure.")
		return
	}
	configured := resolver.ConfiguredProviders()
	if len(configured) == 0 {
		a.addSystem("no provider credentials found. Type /credentials to configure.")
		return
	}
	var others []string
	for _, p := range configured {
		if p != provider {
			others = append(others, p)
		}
	}
	if len(others) > 0 {
		a.addSystem(fmt.Sprintf("%s is configured; %s is not. Type /model to switch provider.", strings.Join(others, ", "), provider))
	} else {
		a.addSystem(fmt.Sprintf("%s credentials missing (%s). Type /credentials to configure.", provider, strings.Join(a.status.Missing, ", ")))
	}
}

// NewApp is a deprecated shim; use New(Options{}) instead.
func NewApp(workdir, providerKey string) *App {
	return New(Options{Workdir: workdir})
}

// SetClassifier installs the operating-mode classifier.
func (a *App) SetClassifier(c rolemanager.Classifier) {
	a.classifier = c
}

// Init implements tea.Model.
func (a *App) Init() tea.Cmd {
	cmds := []tea.Cmd{tickCmd()}
	if a.pending != "" && a.status.Configured {
		cmds = append(cmds, a.sendPending())
	}
	return tea.Batch(cmds...)
}

// bannerHeight returns the rendered height of the banner when visible.
func (a *App) bannerHeight() int {
	if !a.bannerVisible() {
		return 0
	}
	return lipgloss.Height(components.Banner{
		Width:   a.width,
		Version: version.Version,
		Commit:  version.Commit,
		Built:   version.BuildDate,
	}.View())
}

// footerHeight returns the rendered height of the current footer.
func (a *App) footerHeight() int {
	return lipgloss.Height(a.footer.View())
}

// chromeHeight is the total height consumed by everything except the viewport.
func (a *App) chromeHeight() int {
	h := 2 // top+bottom padding from the outer lipgloss frame
	if a.bannerVisible() {
		h += a.bannerHeight() + 1 // separator
	}
	if len(a.autocomplete) > 0 {
		h++
	}
	h += a.attachStripHeight()
	h += a.editor.Height() + 2 // composer frame (top and bottom edges)
	h++                        // separator before the footer
	h += a.footerHeight()
	return h
}

// fitEditor clamps the editor height to [editorMinHeight, editorMaxHeight]
// based on its logical line count. It returns true if the height changed.
func (a *App) fitEditor() bool {
	want := a.editor.LineCount()
	if want < editorMinHeight {
		want = editorMinHeight
	}
	if want > editorMaxHeight {
		want = editorMaxHeight
	}
	if a.editor.Height() != want {
		a.editor.SetHeight(want)
		return true
	}
	return false
}

// relayout recomputes editor and viewport sizes from the current terminal
// dimensions and transcript state. It is cheap enough to call every frame.
func (a *App) relayout() {
	a.fitEditor()
	if a.width > 6 {
		// Two border cells and one column of padding on each side.
		a.editor.SetWidth(a.width - 6)
	}
	a.vp.Width = a.contentWidth()
	vpHeight := a.height - a.chromeHeight()
	if vpHeight < 5 {
		vpHeight = 5
	}
	a.vp.Height = vpHeight
}

func (a *App) sendPending() tea.Cmd {
	prompt := a.pending
	a.pending = ""
	return a.send([]run.Turn{{Role: "user", Content: prompt}})
}

// send starts a streaming request with the given conversation turns.
func (a *App) send(turns []run.Turn) tea.Cmd {
	if !a.status.Configured {
		return func() tea.Msg {
			return agentEventMsg{Kind: agent.EventErrorKind, Err: fmt.Errorf("%s credentials missing (%s). Type /credentials to configure.", a.cfg.Provider, strings.Join(a.status.Missing, ", "))}
		}
	}
	// The last turn is the new user prompt; the rest is history.
	history := turns
	var promptText string
	if n := len(turns); n > 0 && turns[n-1].Role == "user" {
		promptText = turns[n-1].Content
		history = turns[:n-1]
	}
	sess, err := a.agentSession()
	if err != nil {
		return func() tea.Msg {
			return agentEventMsg{Kind: agent.EventErrorKind, Err: err}
		}
	}
	in := agent.TurnInput{Prompt: promptText}
	if a.namedAgent != "" {
		in.ForceAgent = a.namedAgent
	}
	if n := len(turns); n > 0 && turns[n-1].Role == "user" {
		in.Attachments = turns[n-1].Attachments
		in.HasReferences = len(in.Attachments) > 0
		for _, att := range turns[n-1].Attachments {
			if att.Kind == "shell" {
				in.ForceAgent = profiles.DebugProfile
				break
			}
		}
	}
	if a.cancel != nil {
		a.cancel()
	}
	a.ctx, a.cancel = context.WithCancel(context.Background())
	a.events = sess.RunStream(a.ctx, history, in)
	a.messages = append(a.messages, components.Message{Role: "assistant"})
	return a.nextAgent()
}

// submitInput finalises one user prompt, including any SAFE attachments, and
// starts the agent turn.
func (a *App) submitInput(input string) tea.Cmd {
	if !a.modeExplicit {
		a.classifyMode(input)
	}
	a.modeExplicit = false

	var safe []run.Attachment
	for _, id := range a.attachOrder {
		att := a.attachments[id]
		if att.state == attachSafe && att.body != "" {
			safe = append(safe, run.Attachment{Kind: "file", Label: att.text, Body: att.body})
		}
	}
	a.attachments = map[int]*attachment{}
	a.attachOrder = nil
	a.pendingInput = ""
	a.editor.Reset()
	a.autocomplete = nil

	firstUser := !a.hasUserMessage()
	if firstUser && a.shouldAutoName() {
		a.nameRequested = true
		return tea.Batch(a.sendWithAttachments(input, safe), a.nameSessionCmd(input))
	}
	return a.sendWithAttachments(input, safe)
}

func (a *App) nextAgent() tea.Cmd {
	return func() tea.Msg {
		e, ok := <-a.events
		if !ok {
			return agentEventMsg{Kind: agent.EventDoneKind}
		}
		return agentEventMsg(e)
	}
}

// agentSession builds (or reuses) the agent session for the current
// config/posture/permissions. It is invalidated whenever any of those change.
func (a *App) agentSession() (*agent.Session, error) {
	if a.agent != nil {
		return a.agent, nil
	}
	reg := tools.Default(a.workdir, a.settings.BashReadOnlyEnabled())
	perms := permissions.From(a.settings.Permissions.Allow, a.settings.Permissions.Ask, a.settings.Permissions.Deny)
	var promptOpts prompt.Options
	if a.settings.Caveman != nil && *a.settings.Caveman {
		promptOpts.Caveman = true
	}
	sess, err := agent.NewSession(agent.Options{
		Cfg:           a.cfg,
		Client:        a.client,
		Registry:      reg,
		Perms:         perms,
		Posture:       a.posture,
		PlanMode:      a.planMode,
		Workdir:       a.workdir,
		Settings:      a.settings,
		PromptOptions: promptOpts,
	})
	if err != nil {
		return nil, err
	}
	a.agent = sess
	return sess, nil
}

// invalidateAgentSession drops the cached agent session so the next send
// re-resolves the carrier and reseals the system prompt.
func (a *App) invalidateAgentSession() {
	a.agent = nil
}

// syncPlanMode keeps the agent session's plan mode in lockstep with the mode
// chip and drops the cached session so the next send picks up the new mode.
func (a *App) syncPlanMode() {
	a.planMode = a.mode == "plan"
	a.invalidateAgentSession()
}

// Update implements tea.Model.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		a.width = m.Width
		a.height = m.Height
		a.relayout()
		return a, nil

	case tickMsg:
		a.refreshGitInfo()
		a.refreshFooter()
		return a, tickCmd()

	case streamChunkMsg:
		return a, a.handleStreamChunk(m)

	case agentEventMsg:
		return a, a.handleAgentEvent(m)

	case copiedMsg:
		a.addSystem(m.text)
		return a, nil

	case compactDoneMsg:
		return a, a.handleCompactDone(m)

	case sessionNamedMsg:
		return a, a.handleSessionNamed(m)

	case codeReviewDoneMsg:
		return a, a.handleCodeReviewDone(m)

	case modelsFetchedMsg:
		return a, a.handleModelsFetched(m)

	case attachValidatedMsg:
		return a, a.handleAttachValidated(m)

	case shellDoneMsg:
		return a, a.handleShellDone(m)

	case tea.KeyMsg:
		// Global keys work on every screen.
		switch m.String() {
		case "ctrl+c":
			return a, a.copyPrompt()
		case "ctrl+d":
			return a, tea.Quit
		case "ctrl+r":
			a.reasoningOverride = nextBoolPtr(a.reasoningOverride)
			a.addSystem("reasoning display: " + boolLabel(a.reasoningVisible()))
			return a, nil
		case "ctrl+t":
			a.toolCallsOverride = nextBoolPtr(a.toolCallsOverride)
			a.addSystem("tool-call display: " + boolLabel(a.toolCallsVisible()))
			return a, nil
		case "ctrl+alt+p":
			a.cycleMode()
			a.syncPlanMode()
			return a, nil
		}
		if a.view != viewChat {
			if h, ok := viewHandlers[a.view]; ok {
				return h.key(a, m)
			}
			return a, nil
		}
		return a, a.handleChatKey(m)
	}

	cmd := a.editor.Update(msg)
	a.autocomplete = a.registry.Complete(a.editor.Value())
	spin, spinCmd := a.attachSpin.Update(msg)
	a.attachSpin = spin
	a.relayout()
	return a, tea.Batch(cmd, spinCmd)
}

func (a *App) handleChatKey(m tea.KeyMsg) tea.Cmd {
	if a.savePromptMode {
		return a.handleSavePromptKey(m)
	}
	if a.historyActive {
		return a.handleHistoryKey(m)
	}

	switch m.String() {
	case "shift+tab":
		a.cycleMode()
		return nil
	case "ctrl+l":
		// Clear the transcript view; the session is untouched.
		a.messages = nil
		return nil
	case "ctrl+o":
		// Toggle full output for all truncated turns and tool results.
		a.expandAll = !a.expandAll
		return nil
	case "esc":
		if a.pendingInput != "" {
			a.pendingInput = ""
			return a.submitInput(strings.TrimSpace(a.editor.Value()))
		}
		if a.cancel != nil {
			a.cancel()
			a.cancel = nil
			a.addSystem("request cancelled")
		}
		return nil
	case "enter":
		input := strings.TrimSpace(a.editor.Value())
		if input == "" {
			return nil
		}
		if isShellInput(input) {
			a.editor.Reset()
			a.autocomplete = nil
			a.autocompleteIndex = 0
			return a.handleShell(input)
		}
		if strings.HasPrefix(input, "/") {
			a.editor.Reset()
			a.autocomplete = nil
			a.autocompleteIndex = 0
			return a.handleCommand(input)
		}
		cmd := a.syncAttachments()
		if a.hasPendingAttachments() {
			a.pendingInput = input
			return tea.Batch(cmd, a.attachSpin.Tick)
		}
		return a.submitInput(input)
	case "up":
		return a.startHistoryCycle()
	case "alt+s":
		return a.startSavePrompt()
	}

	if len(a.autocomplete) > 0 {
		switch m.String() {
		case "tab":
			return a.cycleAutocomplete()
		case "right":
			return a.acceptAutocomplete()
		}
	}

	cmd := a.editor.Update(m)
	a.autocomplete = a.registry.Complete(a.editor.Value())
	a.autocompleteIndex = 0
	a.relayout()
	return cmd
}

// ---------------------------------------------------------------------------
// Prompt history / library cycling
// ---------------------------------------------------------------------------

func (a *App) startHistoryCycle() tea.Cmd {
	a.historyQuery = strings.TrimSpace(a.editor.Value())
	a.historyOriginal = a.editor.Value()
	a.historyResults = a.buildHistoryResults(a.historyQuery)
	a.historyActive = true
	if len(a.historyResults) > 0 {
		a.historyIndex = 0
		a.editor.SetValue(a.historyResults[0])
		a.editor.CursorEnd()
	} else {
		a.historyIndex = -1
		a.editor.SetValue(a.historyQuery)
	}
	a.autocomplete = nil
	a.autocompleteIndex = 0
	return nil
}

func (a *App) buildHistoryResults(query string) []string {
	var results []string
	seen := make(map[string]bool)

	globalLib, _ := promptlib.LoadGlobal()
	projLib, _ := promptlib.LoadProject(a.workdir)
	lib := promptlib.Merge(globalLib, projLib)
	for _, e := range lib.Filter(query) {
		if !seen[e.Prompt] {
			seen[e.Prompt] = true
			results = append(results, e.Prompt)
		}
	}

	if a.store != nil {
		prompts, _ := a.store.UserPrompts(a.workdir)
		for _, p := range prompts {
			if !seen[p] && promptlib.Match(promptlib.Entry{Name: "", Prompt: p}, query) {
				seen[p] = true
				results = append(results, p)
			}
		}
	}

	return results
}

func (a *App) handleHistoryKey(m tea.KeyMsg) tea.Cmd {
	switch m.String() {
	case "up":
		if a.historyIndex < len(a.historyResults)-1 {
			a.historyIndex++
			a.editor.SetValue(a.historyResults[a.historyIndex])
			a.editor.CursorEnd()
		}
		return nil
	case "down":
		if a.historyIndex > 0 {
			a.historyIndex--
			a.editor.SetValue(a.historyResults[a.historyIndex])
			a.editor.CursorEnd()
		} else {
			a.exitHistoryCycle(false)
		}
		return nil
	case "enter":
		a.exitHistoryCycle(true)
		return nil
	case "esc":
		a.exitHistoryCycle(false)
		return nil
	}

	switch m.Type {
	case tea.KeyRunes:
		a.historyQuery += string(m.Runes)
	case tea.KeyBackspace:
		r := []rune(a.historyQuery)
		if len(r) > 0 {
			a.historyQuery = string(r[:len(r)-1])
		} else {
			a.exitHistoryCycle(false)
			return nil
		}
	case tea.KeySpace:
		a.historyQuery += " "
	default:
		a.exitHistoryCycle(false)
		return a.editor.Update(m)
	}

	a.historyResults = a.buildHistoryResults(a.historyQuery)
	if len(a.historyResults) > 0 {
		a.historyIndex = 0
		a.editor.SetValue(a.historyResults[0])
		a.editor.CursorEnd()
	} else {
		a.historyIndex = -1
		a.editor.SetValue(a.historyQuery)
	}
	return nil
}

func (a *App) exitHistoryCycle(accept bool) {
	a.historyActive = false
	if !accept {
		a.editor.SetValue(a.historyOriginal)
	}
	a.historyResults = nil
	a.historyIndex = 0
	a.historyQuery = ""
	a.historyOriginal = ""
	a.autocomplete = a.registry.Complete(a.editor.Value())
}

// ---------------------------------------------------------------------------
// Autocomplete cycling
// ---------------------------------------------------------------------------

func (a *App) cycleAutocomplete() tea.Cmd {
	a.autocompleteIndex++
	if a.autocompleteIndex >= len(a.autocomplete) {
		a.autocompleteIndex = 0
	}
	a.editor.SetValue(a.autocomplete[a.autocompleteIndex])
	a.editor.CursorEnd()
	return nil
}

func (a *App) acceptAutocomplete() tea.Cmd {
	if len(a.autocomplete) == 0 {
		return nil
	}
	a.editor.SetValue(a.autocomplete[0])
	a.editor.CursorEnd()
	a.autocomplete = nil
	a.autocompleteIndex = 0
	return nil
}

// ---------------------------------------------------------------------------
// Save prompt to library
// ---------------------------------------------------------------------------

func (a *App) startSavePrompt() tea.Cmd {
	val := strings.TrimSpace(a.editor.Value())
	if val == "" {
		a.addSystem("nothing to save; type a prompt first")
		return nil
	}
	a.savePromptValue = val
	a.savePromptMode = true
	a.editor.Reset()
	a.autocomplete = nil
	a.autocompleteIndex = 0
	return nil
}

func (a *App) handleSavePromptKey(m tea.KeyMsg) tea.Cmd {
	switch m.String() {
	case "enter":
		name := strings.TrimSpace(a.editor.Value())
		if name == "" {
			a.addSystem("save cancelled: name required")
			a.cancelSavePrompt()
			return nil
		}
		return a.finishSavePrompt(name)
	case "esc":
		a.cancelSavePrompt()
		a.addSystem("save cancelled")
		return nil
	}
	return a.editor.Update(m)
}

func (a *App) finishSavePrompt(name string) tea.Cmd {
	lib, _ := promptlib.LoadProject(a.workdir)
	lib.Add(promptlib.Entry{Name: name, Prompt: a.savePromptValue})
	if err := promptlib.SaveProject(a.workdir, lib); err != nil {
		a.addSystem("save failed: " + err.Error())
	} else {
		a.addSystem("saved prompt to project library: " + name)
	}
	a.savePromptMode = false
	a.savePromptValue = ""
	a.editor.Reset()
	return nil
}

func (a *App) cancelSavePrompt() {
	a.savePromptMode = false
	a.savePromptValue = ""
	a.editor.Reset()
}

func (a *App) handleStreamChunk(m streamChunkMsg) tea.Cmd {
	if m.Err != nil {
		a.addSystem("provider error: " + m.Err.Error())
		return nil
	}
	if len(a.messages) > 0 && a.messages[len(a.messages)-1].Role == "assistant" {
		a.messages[len(a.messages)-1].Content += m.Text
	}
	if m.Usage != nil {
		a.usage = m.Usage
		a.usageStale = false
	}
	if !m.Done {
		return nil
	}
	if len(a.messages) > 0 && a.messages[len(a.messages)-1].Role == "assistant" {
		a.messages[len(a.messages)-1].Usage = m.Usage
	}
	a.appendAssistant(m.Usage)
	a.refreshFooter()
	return nil
}

// handleAgentEvent renders one agent streaming event.
func (a *App) handleAgentEvent(m agentEventMsg) tea.Cmd {
	switch m.Kind {
	case agent.EventErrorKind:
		a.addSystem("agent error: " + m.Err.Error())
		return nil
	case agent.EventTextKind:
		if len(a.messages) == 0 || a.messages[len(a.messages)-1].Role != "assistant" {
			a.messages = append(a.messages, components.Message{Role: "assistant"})
		}
		a.messages[len(a.messages)-1].Content += m.Text
		return a.nextAgent()
	case agent.EventToolCallDeltaKind:
		// Render-only; no execution authority. The live fragment updates the
		// pending tool row if one is present.
		if len(a.messages) > 0 && a.messages[len(a.messages)-1].Role == "tool" {
			a.messages[len(a.messages)-1].ToolArgs += m.ToolDelta.Args
		}
		return a.nextAgent()
	case agent.EventToolStartKind:
		last := len(a.messages) - 1
		if last >= 0 && a.messages[last].Role == "assistant" {
			a.messages[last].ToolCalls = append(a.messages[last].ToolCalls, components.AgentToolCall{
				ID:   m.Tool.ID,
				Name: m.Tool.Name,
				Args: toolArgsString(m.Tool.Args),
			})
		}
		a.messages = append(a.messages, components.Message{
			Role:       "tool",
			ToolName:   m.Tool.Name,
			ToolArgs:   toolArgsString(m.Tool.Args),
			ToolCallID: m.Tool.ID,
		})
		return a.nextAgent()
	case agent.EventToolResultKind:
		if len(a.messages) > 0 && a.messages[len(a.messages)-1].Role == "tool" {
			a.messages[len(a.messages)-1].Content = m.ToolResult
			status := "✓"
			switch {
			case strings.HasPrefix(m.ToolResult, "tool result withheld:"):
				status = "withheld"
			case m.ToolName == "Bash" && strings.Contains(m.ToolResult, "exit status"):
				status = "✗"
			}
			a.messages[len(a.messages)-1].Status = status
		}
		return a.nextAgent()
	case agent.EventPermissionAskKind:
		a.addSystem("permission ask required for " + m.AskName)
		return a.nextAgent()
	case agent.EventRetryKind:
		// Dim the current assistant bubble so the user knows it is partial and
		// will not be replayed into context when the retry starts. Start a
		// fresh assistant bubble for the retry output.
		if len(a.messages) > 0 && a.messages[len(a.messages)-1].Role == "assistant" {
			a.messages[len(a.messages)-1].Partial = true
		}
		a.messages = append(a.messages, components.Message{Role: "assistant"})
		a.addSystem(fmt.Sprintf("retrying (%d/%d) after %s — %s", m.RetryAttempt, 10, m.RetryDelay.Round(time.Millisecond), m.RetryReason))
		return a.nextAgent()
	case agent.EventDoneKind:
		if len(a.messages) > 0 && a.messages[len(a.messages)-1].Role == "assistant" {
			a.messages[len(a.messages)-1].Usage = m.Result.Usage
		}
		if m.Result.Usage != nil {
			a.usage = m.Result.Usage
			a.usageStale = false
		}
		a.appendAssistant(m.Result.Usage)
		// In plan mode, extract a numbered plan out of the reply and persist it
		// so /todos and a later resume can rebuild progress.
		if a.planMode && m.Result.Reply != "" {
			if steps, err := plans.ExtractSteps(m.Result.Reply); err == nil && len(steps) > 0 {
				ps := modes.PlanState{Enabled: true, Executing: false}
				for i, s := range steps {
					ps.Todos = append(ps.Todos, modes.Todo{N: i + 1, Text: s})
				}
				a.appendEntry(ps.ToEntry(a.lastEntryID))
				a.addSystem(fmt.Sprintf("plan: %d steps extracted", len(steps)))
			}
		}
		a.refreshFooter()
		return nil
	}
	return nil
}

func toolArgsString(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	b, _ := json.Marshal(args)
	return string(b)
}

// buildTurns renders the live transcript as provider turns. A compacted
// session leads with a synthetic user turn carrying the summary plus a
// synthetic assistant acknowledgement, so Anthropic never sees two
// consecutive user turns and the model does not treat the summary as the
// request to answer.
func (a *App) buildTurns() []run.Turn {
	var turns []run.Turn
	if a.summary != "" {
		turns = append(turns,
			run.Turn{Role: "user", Content: rolemanager.SummaryPrefix + a.summary + rolemanager.SummarySuffix},
			run.Turn{Role: "assistant", Content: rolemanager.SummaryAck},
		)
	}
	for _, m := range a.messages {
		if m.Partial {
			continue
		}
		switch m.Role {
		case "user":
			turns = append(turns, run.Turn{Role: m.Role, Content: m.Content})
		case "assistant":
			turn := run.Turn{Role: m.Role, Content: m.Content}
			if len(m.ToolCalls) > 0 {
				for _, tc := range m.ToolCalls {
					args := map[string]any{}
					if tc.Args != "" {
						_ = json.Unmarshal([]byte(tc.Args), &args)
					}
					turn.ToolCalls = append(turn.ToolCalls, rolemanager.ToolCall{ID: tc.ID, Name: tc.Name, Args: args})
				}
			}
			turns = append(turns, turn)
		case "tool":
			turns = append(turns, run.Turn{
				Role:       "tool",
				Content:    m.Content,
				ToolCallID: m.ToolCallID,
				ToolName:   m.ToolName,
			})
		}
	}
	return turns
}

// View implements tea.Model.
func (a *App) View() string {
	if a.view != viewChat {
		if h, ok := viewHandlers[a.view]; ok {
			return h.render(a)
		}
	}
	return a.chatView()
}

func (a *App) chatView() string {
	a.relayout()
	a.vp.SetContent(components.MessageList{Messages: a.messages, Width: a.contentWidth(), ExpandAll: a.expandAll}.View())

	var sb strings.Builder
	if a.bannerVisible() {
		sb.WriteString(components.Banner{
			Width:   a.width,
			Version: version.Version,
			Commit:  version.Commit,
			Built:   version.BuildDate,
		}.View())
		sb.WriteString("\n")
	}
	sb.WriteString(a.vp.View())
	sb.WriteString("\n")
	if len(a.autocomplete) > 0 {
		sb.WriteString(a.renderSuggestions())
		sb.WriteString("\n")
	}
	if len(a.attachments) > 0 {
		sb.WriteString(a.renderAttachStrip())
		sb.WriteString("\n")
	}
	sb.WriteString(a.renderComposer())
	sb.WriteString("\n")
	a.refreshFooter()
	sb.WriteString(a.footer.View())
	return lipgloss.NewStyle().Padding(1).Render(sb.String())
}

// contentWidth is the width available inside the outer one-column padding.
func (a *App) contentWidth() int {
	if a.width <= 2 {
		return 76
	}
	return a.width - 2
}

// renderComposer frames the editor. The frame is the only chrome the input
// carries: send/newline hints ride the top edge instead of costing a line.
func (a *App) renderComposer() string {
	title, accent := "ask", lipgloss.TerminalColor(components.ColorTeal)
	meta := "⏎ send · ctrl+j newline"
	if a.editor.Masked {
		title, accent, meta = "secret", lipgloss.TerminalColor(components.ColorAmber), "input hidden · ⏎ save"
	}
	if a.savePromptMode {
		title, accent, meta = "name prompt", lipgloss.TerminalColor(components.ColorAmber), "⏎ save · esc cancel"
	}
	if a.historyActive {
		meta = "↑↓ cycle · type to search · esc cancel"
	}
	return components.Panel{
		Title:  title,
		Meta:   meta,
		Body:   a.editor.View(),
		Width:  a.contentWidth(),
		Accent: accent,
		Raw:    true,
	}.View()
}

// renderFieldEditor frames the shared editor for an inline field edit inside a
// full-screen view, matching the chat composer's frame.
func (a *App) renderFieldEditor(title string, width int) string {
	accent := lipgloss.TerminalColor(components.ColorTeal)
	meta := "⏎ save · esc cancel"
	if a.editor.Masked {
		accent = lipgloss.TerminalColor(components.ColorAmber)
		meta = "input hidden · ⏎ save"
	}
	return components.Panel{
		Title:  title,
		Meta:   meta,
		Body:   a.editor.View(),
		Width:  width,
		Accent: accent,
		Raw:    true,
	}.View()
}

// renderSuggestions draws slash-command completions as a row of chips.
func (a *App) renderSuggestions() string {
	parts := make([]string, 0, len(a.autocomplete))
	for _, s := range a.autocomplete {
		parts = append(parts, components.KeyStyle.Render(s))
	}
	line := components.MutedStyle.Render("⌕ ") + strings.Join(parts, components.MutedStyle.Render("  ·  "))
	return lipgloss.NewStyle().MaxWidth(a.contentWidth()).Render(line)
}

func (a *App) bannerVisible() bool {
	if a.settings.UI != nil && a.settings.UI.Banner != nil {
		return *a.settings.UI.Banner
	}
	return len(a.messages) < 3
}

// handleCommand dispatches a slash command, resolving aliases first.
func (a *App) handleCommand(input string) tea.Cmd {
	name, arg, _ := strings.Cut(strings.TrimPrefix(input, "/"), " ")
	name = strings.TrimSpace(name)
	canonical := a.registry.Canonical(name)
	cmd, ok := a.registry.Command(canonical)
	if !ok || cmd.Run == nil {
		a.addSystem("unknown command: " + input)
		return nil
	}
	return cmd.Run(a, arg)
}

func (a *App) cycleMode() {
	switch a.mode {
	case "agent":
		a.mode = "plan"
		a.addSystem("plan mode on (read-only)")
	case "plan":
		a.mode = "goal"
		a.addSystem("goal mode on")
	case "goal":
		a.mode = "agent"
		a.addSystem("agent mode on")
	}
	a.modeExplicit = true
	a.syncPlanMode()
	a.saveMode()
}

func (a *App) saveMode() {
	st, _ := config.LoadState()
	st.LastMode = a.mode
	_ = config.SaveState(st)
}

func (a *App) saveState() {
	st, _ := config.LoadState()
	st.Model = a.cfg.Model
	st.Provider = a.cfg.Provider
	st.LastMode = a.mode
	_ = config.SaveState(st)
}

// saveSession persists the active session id plus the last-used model/provider
// and mode. It reloads state first so unrelated fields are never clobbered.
func (a *App) saveSession() {
	st, _ := config.LoadState()
	st.ActiveSession = a.sessionID
	st.Model = a.cfg.Model
	st.Provider = a.cfg.Provider
	st.LastMode = a.mode
	_ = config.SaveState(st)
}

func (a *App) addSystem(text string) {
	a.messages = append(a.messages, components.Message{Role: "system", Content: text})
}

// classifyMode runs the operating-mode classifier on a user prompt that did
// not explicitly specify a mode. The classifier sees only the prompt text.
func (a *App) classifyMode(input string) {
	if a.classifier == nil {
		return
	}
	d, err := rolemanager.Select(a.ctx, a.classifier, rolemanager.ModeInput{Prompt: input})
	if err != nil {
		a.mode = "agent"
		a.modeWarning = "mode classifier error: " + err.Error()
		a.addSystem(a.modeWarning)
		a.syncPlanMode()
		return
	}
	a.mode = string(d.Mode)
	a.namedAgent = d.AgentName
	a.modeWarning = d.Warning
	a.syncPlanMode()
	if d.AgentName != "" {
		a.addSystem("engaged agent: " + d.AgentName)
	} else {
		msg := "mode: " + string(d.Mode)
		if d.Explore {
			msg += " (launch explore agents)"
		}
		a.addSystem(msg)
	}
	if d.Warning != "" {
		a.addSystem(d.Warning)
	}
}

// refreshProvider re-prepares from the resolver, rebuilds the classifier only
// when configured, and refreshes the footer. Call after any credential or
// provider/model/effort change. Fixes the stale/nil classifier bugs.
func (a *App) refreshProvider() tea.Cmd {
	src := run.CredentialSource(run.EnvSource(os.Getenv))
	if a.resolver != nil {
		src = a.resolver
	}
	cfg, status := run.Prepare(a.cfg.Model, a.cfg.Provider, src)
	cfg.Effort = a.settings.Effort
	a.cfg = cfg
	a.status = status
	a.classifier = nil
	a.invalidateAgentSession()
	if status.Configured {
		a.SetClassifier(run.NewClassifier(cfg, a.client))
	}
	a.refreshFooter()
	if a.pending != "" && status.Configured {
		return a.sendPending()
	}
	return nil
}

// reloadSettings recomputes the merged settings view after a mutation.
func (a *App) reloadSettings() error {
	eff, err := config.Resolve(a.workdir, os.Getenv, a.flags)
	if err != nil {
		return err
	}
	a.settings = eff.Settings
	a.eff = eff
	a.refreshFooter()
	return nil
}

func (a *App) refreshFooter() {
	// The footer draws a full-width rule, so it must measure the space inside
	// the outer padding, not the terminal.
	a.footer.Width = a.contentWidth()
	a.footer.Mode = a.mode
	a.footer.Provider = a.cfg.Provider
	a.footer.Model = a.cfg.Model
	a.footer.Cost = "$0.00"
	if a.gitOK {
		a.footer.Branch = a.gitInfo.Branch
		a.footer.Cwd = a.workdir
	}
	a.footer.Session = a.sessionDisplay()
	a.footer.SessionName = a.sessionName
	a.footer.ShowName = a.settings.SessionNamesVisible()

	est := transcript.EstimateContext(a.transcriptMessages())
	a.footer.Tokens = est.Tokens
	a.footer.Estimated = est.LastUsageIndex < 0
	a.footer.ContextStale = a.usageStale
	if limit, ok := modelinfo.Resolve(a.cfg.Model, a.settings.ContextWindows); ok {
		a.footer.ContextLimit = limit
	} else {
		a.footer.ContextLimit = 0
	}
}

func (a *App) sessionDisplay() string {
	if len(a.sessionID) >= 8 {
		return a.sessionID[:8]
	}
	return a.sessionID
}

func (a *App) refreshGitInfo() {
	if info, ok := gitinfo.Detect(a.workdir); ok {
		a.gitInfo = info
		a.gitOK = true
	}
}

func (a *App) copyPrompt() tea.Cmd {
	text := a.editor.Value()
	if text == "" {
		return nil
	}
	return func() tea.Msg {
		method, err := clipboard.Copy(text)
		if err != nil {
			return copiedMsg{text: "copy failed: " + err.Error()}
		}
		return copiedMsg{text: "copied prompt to clipboard (" + method + ")"}
	}
}

// transcriptMessages maps the TUI transcript onto provider-neutral messages.
func (a *App) transcriptMessages() []transcript.Message {
	out := make([]transcript.Message, 0, len(a.messages))
	for _, m := range a.messages {
		out = append(out, transcript.Message{
			Role:    m.Role,
			Content: m.Content,
			Usage:   m.Usage,
		})
	}
	return out
}

func (a *App) hasUserMessage() bool {
	for _, m := range a.messages {
		if m.Role == "user" {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Session persistence
// ---------------------------------------------------------------------------

// appendEntry chains ParentID from lastEntryID and never fails the TUI: a
// store error is surfaced once and the session then degrades to memory-only.
func (a *App) appendEntry(e session.Entry) {
	if a.store == nil || a.storeDisabled {
		return
	}
	id, err := session.NewID()
	if err != nil {
		a.disableStore("generate session entry id: " + err.Error())
		return
	}
	e.ID = id
	if e.Timestamp == 0 {
		e.Timestamp = time.Now().UnixMilli()
	}
	if e.ParentID == "" {
		e.ParentID = a.lastEntryID
	}
	if err := a.store.Append(a.workdir, a.sessionID, e); err != nil {
		a.disableStore("session store error: " + err.Error())
		return
	}
	a.lastEntryID = id
}

func (a *App) disableStore(msg string) {
	if a.storeDisabled {
		return
	}
	a.storeDisabled = true
	a.addSystem(msg + " (continuing without persistence)")
}

func (a *App) appendAssistant(usage *transcript.Usage) {
	if len(a.messages) == 0 || a.messages[len(a.messages)-1].Role != "assistant" {
		return
	}
	content := a.messages[len(a.messages)-1].Content
	meta := map[string]any{"model": a.cfg.Model, "provider": a.cfg.Provider}
	if usage != nil {
		meta["prompt_tokens"] = usage.PromptTokens
		meta["completion_tokens"] = usage.CompletionTokens
		meta["total_tokens"] = usage.Total()
	}
	a.appendEntry(session.Entry{Type: "assistant", Role: "assistant", Content: content, Meta: meta})
}

// startNewSession resets to a brand-new, unnamed session. The previous
// session's file is left untouched; nothing is written until the next user
// message.
func (a *App) startNewSession() {
	a.sessionID = session.MustID()
	a.lastEntryID = ""
	a.sessionName = ""
	a.parentSession = ""
	a.nameRequested = false
	a.messages = nil
	a.summary = ""
	a.usage = nil
	a.usageStale = false
	a.saveSession()
	a.refreshFooter()
}

func (a *App) shouldAutoName() bool {
	return a.classifier != nil && a.sessionName == "" && !a.nameRequested && a.summary == ""
}

func (a *App) nameSessionCmd(firstUserMessage string) tea.Cmd {
	c := a.classifier
	return func() tea.Msg {
		raw, err := c.Classify(a.ctx, rolemanager.BuildSessionNamePayload(firstUserMessage))
		if err != nil {
			return sessionNamedMsg{err: err}
		}
		name, err := rolemanager.ParseSessionName(raw)
		return sessionNamedMsg{name: name, err: err}
	}
}

func (a *App) handleSessionNamed(m sessionNamedMsg) tea.Cmd {
	if m.err != nil || m.name == "" {
		return nil // fail closed to no name
	}
	a.sessionName = m.name
	a.appendEntry(session.Entry{Type: session.EntryTypeSessionName, Role: "", Content: m.name, Meta: map[string]any{"source": "model"}})
	a.refreshFooter()
	return nil
}

// handleCodeReviewDone renders the result of an async /code-review run.
func (a *App) handleCodeReviewDone(m codeReviewDoneMsg) tea.Cmd {
	if m.err != nil {
		a.addSystem("code-review failed: " + m.err.Error())
		return nil
	}
	if m.report.Summary != "" {
		a.addSystem("code-review:\n" + m.report.Summary)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Compaction
// ---------------------------------------------------------------------------

func (a *App) compactCmd() tea.Cmd {
	if a.classifier == nil {
		return func() tea.Msg { return compactDoneMsg{err: errors.New("no classifier configured")} }
	}
	msgs := a.transcriptMessages()
	if len(msgs) == 0 {
		return func() tea.Msg { return compactDoneMsg{err: errors.New("nothing to compact yet")} }
	}
	doc := transcript.Serialize(msgs, transcript.SerializeOptions{Nonce: nonceHex()})
	c := a.classifier
	return func() tea.Msg {
		raw, err := c.Classify(a.ctx, rolemanager.BuildCompactionPayload(doc))
		if err != nil {
			return compactDoneMsg{err: err}
		}
		s, err := rolemanager.ValidateSummary(raw)
		return compactDoneMsg{summary: s, err: err}
	}
}

func (a *App) handleCompactDone(m compactDoneMsg) tea.Cmd {
	if m.err != nil {
		a.addSystem("compact failed: " + m.err.Error())
		return nil
	}
	return a.applyCompaction(m.summary)
}

func (a *App) applyCompaction(summary string) tea.Cmd {
	msgs := a.transcriptMessages()
	est := transcript.EstimateContext(msgs)
	old := a.sessionID
	oldName := a.sessionName

	a.sessionID = session.MustID()
	a.lastEntryID = ""
	a.parentSession = old

	a.appendEntry(session.Entry{
		Type:    "summary",
		Role:    "",
		Content: summary,
		Meta: map[string]any{
			"parent_session":  old,
			"kind":            "compaction",
			"model":           a.cfg.Model,
			"source_messages": len(msgs),
			"source_tokens":   est.Tokens,
		},
	})
	if oldName != "" {
		a.appendEntry(session.Entry{Type: session.EntryTypeSessionName, Role: "", Content: oldName, Meta: map[string]any{"source": "inherited"}})
	}

	a.sessionName = oldName
	a.nameRequested = true
	a.summary = summary
	a.messages = nil
	a.usage = nil
	a.usageStale = true

	a.addSystem(fmt.Sprintf("compacted %s into %s", shortID(old), shortID(a.sessionID)))
	a.saveSession()
	a.refreshFooter()
	return nil
}

func (a *App) renameSession(arg string) tea.Cmd {
	if arg == "" {
		if a.sessionName != "" {
			a.addSystem("session name: " + a.sessionName)
		} else {
			a.addSystem("no name set; use /rename <name>")
		}
		return nil
	}
	name, err := rolemanager.SanitizeSessionName(arg)
	if err != nil {
		a.addSystem("rename failed: " + err.Error())
		return nil
	}
	a.sessionName = name
	a.appendEntry(session.Entry{Type: session.EntryTypeSessionName, Role: "", Content: name, Meta: map[string]any{"source": "user"}})
	a.refreshFooter()
	a.addSystem("session renamed to " + name)
	return nil
}

func shortID(id string) string {
	if len(id) >= 8 {
		return id[:8]
	}
	return id
}

func nonceHex() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000"
	}
	return hex.EncodeToString(b[:])
}

// reasoningVisible reports whether reasoning deltas render, honouring the
// ctrl+r session override over the resolved setting.
func (a *App) reasoningVisible() bool {
	if a.reasoningOverride != nil {
		return *a.reasoningOverride
	}
	return a.settings.ReasoningVisible()
}

// toolCallsVisible reports whether tool-call rows render, honouring the ctrl+t
// session override over the resolved setting.
func (a *App) toolCallsVisible() bool {
	if a.toolCallsOverride != nil {
		return *a.toolCallsOverride
	}
	return a.settings.ToolCallsVisible()
}

func nextBoolPtr(b *bool) *bool {
	if b == nil {
		t := true
		return &t
	}
	if *b {
		f := false
		return &f
	}
	return nil
}
