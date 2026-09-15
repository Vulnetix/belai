package tui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/signet/internal/clipboard"
	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/credentials"
	"github.com/vulnetix/signet/internal/gitinfo"
	"github.com/vulnetix/signet/internal/modelinfo"
	"github.com/vulnetix/signet/internal/models"
	"github.com/vulnetix/signet/internal/provider"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/session"
	"github.com/vulnetix/signet/internal/transcript"
	"github.com/vulnetix/signet/internal/tui/components"
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
}

// streamChunkMsg wraps one chunk from the streaming channel.
type streamChunkMsg run.Chunk

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
	cfg      run.Config
	status   run.Status
	client   *http.Client
	resolver *credentials.Resolver
	stream   <-chan run.Chunk
	pending  string // pending prompt to send once configured

	// view state
	view            viewState
	viewStack       []viewState
	credentialState credentialViewState
	settingsState   settingsViewState
	modelState      modelViewState
	permState       permissionsViewState
	importState     importViewState

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

	// layout
	vp viewport.Model
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

	store, _ := session.NewStore()

	a := &App{
		registry:  NewRegistry(workdir),
		editor:    components.NewEditor(),
		footer:    components.Footer{Session: "new", Model: cfg.Model, Cost: "$0.00"},
		mode:      mode,
		ctx:       context.Background(),
		cfg:       cfg,
		status:    status,
		client:    opts.Client,
		resolver:  opts.Resolver,
		pending:   opts.Prompt,
		workdir:   workdir,
		settings:  eff.Settings,
		eff:       eff,
		flags:     flags,
		state:     st,
		vp:        viewport.New(80, 24),
		store:     store,
		sessionID: session.MustID(),
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
func (a *App) catalogFor(name string) []models.Model {
	if cat := models.Catalog(name); cat != nil {
		return cat
	}
	prof, ok := a.settings.Providers[name]
	if !ok {
		return nil
	}
	out := make([]models.Model, 0, len(prof.Models))
	for _, m := range prof.Models {
		label := m.Name
		if label == "" {
			label = m.ID
		}
		out = append(out, models.Model{ID: m.ID, Label: label})
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

func (a *App) sendPending() tea.Cmd {
	prompt := a.pending
	a.pending = ""
	return a.send([]run.Turn{{Role: "user", Content: prompt}})
}

// send starts a streaming request with the given conversation turns.
func (a *App) send(turns []run.Turn) tea.Cmd {
	if !a.status.Configured {
		return func() tea.Msg {
			return streamChunkMsg{Err: fmt.Errorf("%s credentials missing (%s). Type /credentials to configure.", a.cfg.Provider, strings.Join(a.status.Missing, ", ")), Done: true}
		}
	}
	ch, err := run.Stream(a.ctx, a.cfg, turns, a.client)
	if err != nil {
		return func() tea.Msg {
			return streamChunkMsg{Err: err, Done: true}
		}
	}
	a.stream = ch
	a.messages = append(a.messages, components.Message{Role: "assistant"})
	return a.next()
}

func (a *App) next() tea.Cmd {
	return func() tea.Msg {
		c, ok := <-a.stream
		if !ok {
			c.Done = true
		}
		return streamChunkMsg(c)
	}
}

// Update implements tea.Model.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		a.width = m.Width
		a.height = m.Height
		if m.Width > 4 {
			a.editor.SetWidth(m.Width - 4)
		}
		vpHeight := m.Height - 8
		if a.bannerVisible() {
			vpHeight -= 6
		}
		if vpHeight < 5 {
			vpHeight = 5
		}
		a.vp.Width = m.Width - 2
		a.vp.Height = vpHeight
		return a, nil

	case tickMsg:
		a.refreshGitInfo()
		a.refreshFooter()
		return a, tickCmd()

	case streamChunkMsg:
		return a, a.handleStreamChunk(m)

	case copiedMsg:
		a.addSystem(m.text)
		return a, nil

	case compactDoneMsg:
		return a, a.handleCompactDone(m)

	case sessionNamedMsg:
		return a, a.handleSessionNamed(m)

	case tea.KeyMsg:
		// Global keys work on every screen.
		switch m.String() {
		case "ctrl+c":
			return a, a.copyPrompt()
		case "ctrl+d":
			return a, tea.Quit
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
	return a, cmd
}

func (a *App) handleChatKey(m tea.KeyMsg) tea.Cmd {
	switch m.String() {
	case "shift+tab":
		a.cycleMode()
		return nil
	case "ctrl+l":
		// Clear the transcript view; the session is untouched.
		a.messages = nil
		return nil
	case "esc":
		return nil
	case "enter":
		input := strings.TrimSpace(a.editor.Value())
		a.editor.Reset()
		a.autocomplete = nil
		if input == "" {
			return nil
		}
		if strings.HasPrefix(input, "/") {
			return a.handleCommand(input)
		}
		if !a.modeExplicit {
			a.classifyMode(input)
		}
		a.modeExplicit = false
		firstUser := !a.hasUserMessage()
		a.messages = append(a.messages, components.Message{Role: "user", Content: input})
		a.appendEntry(session.Entry{Type: "user", Role: "user", Content: input})
		if firstUser && a.shouldAutoName() {
			a.nameRequested = true
			return tea.Batch(a.send(a.buildTurns()), a.nameSessionCmd(input))
		}
		return a.send(a.buildTurns())
	}

	cmd := a.editor.Update(m)
	a.autocomplete = a.registry.Complete(a.editor.Value())
	return cmd
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
		return a.next()
	}
	if len(a.messages) > 0 && a.messages[len(a.messages)-1].Role == "assistant" {
		a.messages[len(a.messages)-1].Usage = m.Usage
	}
	a.appendAssistant(m.Usage)
	a.refreshFooter()
	return nil
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
		if m.Role == "user" || m.Role == "assistant" {
			turns = append(turns, run.Turn{Role: m.Role, Content: m.Content})
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
	var b strings.Builder
	for _, m := range a.messages {
		b.WriteString("[" + m.Role + "] ")
		b.WriteString(m.Content)
		b.WriteString("\n\n")
	}
	a.vp.SetContent(b.String())

	var sb strings.Builder
	if a.bannerVisible() {
		sb.WriteString(components.Banner{Width: a.width}.View())
		sb.WriteString("\n")
	}
	sb.WriteString(a.vp.View())
	sb.WriteString("\n")
	if len(a.autocomplete) > 0 {
		sb.WriteString("suggestions: " + strings.Join(a.autocomplete, "  ") + "\n")
	}
	sb.WriteString(a.editor.View())
	sb.WriteString("\n")
	a.refreshFooter()
	sb.WriteString(a.footer.View())
	return lipgloss.NewStyle().Padding(1).Render(sb.String())
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
	d, err := rolemanager.Select(a.classifier, rolemanager.ModeInput{Prompt: input})
	if err != nil {
		a.mode = "agent"
		a.modeWarning = "mode classifier error: " + err.Error()
		a.addSystem(a.modeWarning)
		return
	}
	a.mode = string(d.Mode)
	a.namedAgent = d.AgentName
	a.modeWarning = d.Warning
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
	a.footer.Width = a.width
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
		raw, err := c.Classify(rolemanager.BuildSessionNamePayload(firstUserMessage))
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
		raw, err := c.Classify(rolemanager.BuildCompactionPayload(doc))
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
