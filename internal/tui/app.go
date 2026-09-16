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
	"github.com/vulnetix/signet/internal/agentprofile"
	"github.com/vulnetix/signet/internal/bgagent"
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
	"github.com/vulnetix/signet/internal/todos"
	"github.com/vulnetix/signet/internal/tools"
	"github.com/vulnetix/signet/internal/trace"
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

// agentReadyMsg carries the result of an async agent-session build. Session
// construction can block on a nonce GET (3s deadline), so it runs on a tea.Cmd
// goroutine instead of freezing the Update loop.
type agentReadyMsg struct {
	sess    *agent.Session
	history []run.Turn
	in      agent.TurnInput
	err     error
}

// credentialsResolvedMsg carries the result of async credential resolution.
// Resolving a provider may probe the host keychain (DBus with a 5s timeout),
// so the first frame paints from the env-only resolution and this lands later.
type credentialsResolvedMsg struct {
	cfg    run.Config
	status run.Status
}

// copiedMsg reports the result of a clipboard copy.
type copiedMsg struct{ text string }

// compactDoneMsg carries the result of an async compaction call.
type compactDoneMsg struct {
	summary string
	err     error
}

// agentBuilderDoneMsg carries the result of an async /agent create run.
type agentBuilderDoneMsg struct {
	profile agentprofile.AgentProfile
	path    string
	err     error
}

// modeClassifiedMsg carries the async mode-classification outcome plus the
// prompt to send once the decision lands.
type modeClassifiedMsg struct {
	input     string
	atts      []run.Attachment
	firstUser bool
	decision  rolemanager.ModeDecision
	err       error
}

// workingPhase describes what the in-flight prompt is doing so the composer
// can show a specific signal instead of a bare "working": either the Role
// Manager is classifying content, or it is generic provider/disk I/O.
type workingPhase int

const (
	phaseIdle        workingPhase = iota // no prompt in flight
	phaseRoleManager                     // Role Manager is classifying (admission, mode, tool result, steering)
	phaseWorking                         // generic network/disk I/O with no specific signal
)

// bgAgentEventMsg carries one background-agent event into the TUI loop.
type bgAgentEventMsg bgagent.Event

// sessionNamedMsg carries the result of an async session-naming call.
type sessionNamedMsg struct {
	name string
	err  error
}

// gitInfoMsg carries the result of an async git-context detection. Detection
// walks up from the workdir stat-ing for .git and reads HEAD, so it is moved
// off the render goroutine onto a tea.Cmd rather than running on the 2s tick.
type gitInfoMsg struct {
	info gitinfo.Info
	ok   bool
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
	// forceMode carries that manual choice into the agent session. Suppressing
	// the TUI's own classification is not enough: Session.run classifies again
	// internally, so without this the user's explicit mode is discarded.
	// One-shot, matching modeExplicit.
	forceMode    modes.Mode
	autocomplete []string

	// mode classification (optional; nil skips auto-detection)
	classifier  rolemanager.Classifier
	namedAgent  string
	modeWarning string

	// working indicator
	phase   workingPhase // current activity; phaseIdle when no prompt is in flight
	rmPhase string       // Role Manager sub-phase (agent.RoleManagerPhase*) for the caption
	preSend bool         // prompt echoed, awaiting the async mode classification
	// phaseStartedAt is when the in-flight turn (or pre-send classification)
	// began. It drives the live "working · N.Ns" elapsed label and is zeroed
	// when the turn ends.
	phaseStartedAt time.Time
	// painted marks the first rendered chat frame, used to emit one
	// SIGNET_TRACE first_paint event.
	painted bool
	// trace is the opt-in SIGNET_TRACE writer; nil when tracing is off.
	trace *trace.Writer

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
	// requestedProvider is the provider name from settings/state/env/flags
	// before any sole-configured-provider fallback. Empty means none was
	// configured; the async credential resolution may then pick a sole provider.
	requestedProvider string
	// pendingEvent holds one lookahead agent event read while coalescing a run
	// of text/reasoning deltas but belonging to a different kind, so it is
	// replayed by the next nextAgent call instead of being dropped.
	pendingEvent    agent.Event
	pendingEventSet bool

	// session display overrides (ctrl+r / ctrl+t), shadowing the resolved
	// settings without rewriting the settings file.
	reasoningOverride *bool
	toolCallsOverride *bool
	lastPlanText      string

	// attachments state
	attachments  map[int]*attachment
	attachOrder  []int
	attachSeq    int
	attachSpin   spinner.Model
	workSpin     spinner.Model
	pendingInput string // prompt held while attachments validate

	// view state
	view            viewState
	viewStack       []viewState
	credentialState credentialViewState
	settingsState   settingsViewState
	modelState      modelViewState
	permState       permissionsViewState
	importState     importViewState
	clarifyState    clarifyViewState

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

	// est memoises the footer's context estimate. refreshFooter runs every
	// frame, and the estimate walks the whole transcript; it is only
	// recomputed when the transcript shape or tail changes.
	est     transcript.Estimate
	estKey  string
	estInit bool

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
	vp     viewport.Model
	follow bool // autoscroll: keep the transcript pinned to the tail

	// bannerH/footerH memoise the two chrome heights that are constant per
	// width. relayout() measures them every frame to size the viewport, and
	// measuring means re-rendering; caching by width halves the chrome renders
	// per frame. The footer cache is invalidated when git info changes because
	// a branch/cwd line appears, which changes the footer's height.
	bannerH int
	bannerW int // width bannerH was measured for; -1 invalid
	footerH int
	footerW int

	// drag-selection state (see selection.go). lastFrame is the geometry and
	// provenance of the last rendered transcript frame; lastBody is its
	// unhighlighted text, the single compare that clears a stale selection.
	sel       selection
	lastFrame frame
	lastBody  string

	// expandAll disables truncation and shows every message in full.
	expandAll bool

	// background agent manager
	bgManager *bgagent.Manager

	// todos is the shared goal/plan todo list rendered in the chat chrome and
	// persisted to the session. The agent emits it; the TUI owns persistence.
	todos *todos.List
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

	// The provider name is read from the merged settings; a sole-configured
	// provider fallback is deferred to the async credential resolution because
	// it walks every provider and (before caching) probed the keychain per
	// field. The first frame therefore paints from an env-only resolution.
	name := eff.Settings.Provider
	initial, initialStatus := run.Prepare(eff.Settings.Model, name, run.EnvSource(os.Getenv))
	initial.Effort = eff.Settings.Effort
	if cc, err := run.ResolveClassifier(initial, eff.Settings.Classifier, run.EnvSource(os.Getenv)); err == nil {
		initial.Classifier = cc
	}

	pol := opts.Posture
	if len(pol) == 0 {
		pol = posture.Defaults()
	}

	client := opts.Client
	if client == nil {
		client = http.DefaultClient
	}

	store, _ := session.NewStore()

	a := &App{
		registry:          NewRegistry(workdir),
		editor:            components.NewEditor(),
		footer:            components.Footer{Session: "new", Model: initial.Model, Cost: "$0.00"},
		mode:              mode,
		ctx:               context.Background(),
		cfg:               initial,
		status:            initialStatus,
		client:            client,
		resolver:          opts.Resolver,
		posture:           pol,
		planMode:          mode == "plan",
		pending:           opts.Prompt,
		requestedProvider: name,
		workdir:           workdir,
		settings:          eff.Settings,
		eff:               eff,
		flags:             flags,
		state:             st,
		vp:                viewport.New(80, 24),
		store:             store,
		sessionID:         session.MustID(),
		attachments:       map[int]*attachment{},
		attachSpin:        spinner.New(),
		workSpin:          spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		follow:            true,
		bannerW:           -1,
		footerW:           -1,
		trace:             trace.Env(),
	}
	if initialStatus.Configured {
		a.SetClassifier(run.NewClassifier(initial, a.client))
		a.bgManager = bgagent.NewManager(workdir, initial, a.client, a.settings, a.posture)
	}
	a.applyGitInfo(gitinfo.Detect(a.workdir))
	_ = a.editor.Focus()

	if startErr != "" {
		a.addSystem("settings error: " + startErr)
	} else if !initialStatus.Configured && opts.Resolver == nil {
		// No resolver means no async credential resolution will land, so the
		// hint is emitted here. With a resolver it is deferred to
		// handleCredentialsResolved so the keychain probe stays off the first
		// frame.
		a.showCredentialMessage(initial.Provider, nil)
	}

	if opts.Prompt != "" {
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
	// Credential resolution can probe the host keychain; run it off the first
	// frame so the TUI paints from the env-only resolution immediately.
	if a.resolver != nil {
		cmds = append(cmds, a.resolveCredentialsCmd())
	}
	if a.pending != "" && a.status.Configured {
		cmds = append(cmds, a.sendPending())
	}
	return tea.Batch(cmds...)
}

// bannerHeight returns the rendered height of the banner when visible. The
// height is constant per width, so it is memoised: relayout() measures it
// every frame and measuring re-renders the whole banner.
func (a *App) bannerHeight() int {
	if !a.bannerVisible() {
		return 0
	}
	if a.bannerW == a.width {
		return a.bannerH
	}
	a.bannerH = lipgloss.Height(components.Banner{
		Width:   a.width,
		Version: version.Version,
		Commit:  version.Commit,
		Built:   version.BuildDate,
	}.View())
	a.bannerW = a.width
	return a.bannerH
}

// footerHeight returns the rendered height of the current footer, memoised per
// width. refreshGitInfo invalidates the cache when the branch/cwd line
// appears or disappears, since that changes the height at the same width.
func (a *App) footerHeight() int {
	if a.footerW == a.width {
		return a.footerH
	}
	a.footerH = lipgloss.Height(a.footer.View())
	a.footerW = a.width
	return a.footerH
}

// chromeHeight is the total height consumed by everything except the viewport.
// headerHeight is the rows above the viewport — the outer frame's top
// padding plus the banner. It is also the screen row of the viewport's first
// line, which is what the mouse hit-testing in selection.go needs.
func (a *App) headerHeight() int {
	h := 1 // top padding from the outer lipgloss frame
	if a.bannerVisible() {
		h += a.bannerHeight()
	}
	return h
}

// belowViewportHeight is the rows below the viewport — the outer frame's
// bottom padding, the autocomplete and attachment strips, the todo panel, the
// composer and the footer.
func (a *App) belowViewportHeight() int {
	h := 1 // bottom padding from the outer lipgloss frame
	if len(a.autocomplete) > 0 {
		h++
	}
	h += a.attachStripHeight()
	h += a.todoPanelHeight()
	h += a.editor.Height() + 2 // composer frame (top and bottom edges)
	h += a.footerHeight()
	return h
}

// The "\n" written after the banner, viewport, suggestions, attach strip, and
// composer are line terminators, not blank rows, so they cost nothing here.
func (a *App) chromeHeight() int {
	return a.headerHeight() + a.belowViewportHeight()
}

// contentLeft is the screen column of content column 0: the outer Padding(1)
// left column.
func (a *App) contentLeft() int { return 1 }

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
	in := agent.TurnInput{Prompt: promptText, ForceMode: a.forceMode}
	a.forceMode = ""
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
	a.setPhaseRoleManager(agent.RoleManagerPhasePrePrompt)
	a.messages = append(a.messages, components.Message{Role: "assistant"})

	// A cached session starts synchronously; a cold session build (which can
	// block on a nonce GET) is hoisted onto a tea.Cmd goroutine so the TUI
	// keeps painting. The build reads a snapshot taken here, never live App
	// fields, so the goroutine cannot race a config change.
	if a.agent != nil {
		return a.startAgent(a.agent, history, in)
	}
	params := a.sessionBuildParams()
	return func() tea.Msg {
		sess, err := buildAgentSession(params)
		return agentReadyMsg{sess: sess, history: history, in: in, err: err}
	}
}

// startAgent begins the streaming turn on an already-built session.
func (a *App) startAgent(sess *agent.Session, history []run.Turn, in agent.TurnInput) tea.Cmd {
	a.events = sess.RunStream(a.ctx, history, in)
	return tea.Batch(a.nextAgent(), a.workSpin.Tick)
}

// submitInput finalises one user prompt, including any SAFE attachments, and
// starts the agent turn. The prompt is echoed to the transcript the instant
// Enter is pressed — before mode classification and any provider I/O — and
// the composer shows the Role Manager indicator while the pre-prompt
// classifier runs.
func (a *App) submitInput(input string) tea.Cmd {
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

	// Instant echo: the prompt becomes a "User prompt" in the transcript and
	// a session entry before any classification or provider I/O.
	a.echoUser(input)
	a.setPhaseRoleManager(agent.RoleManagerPhasePrePrompt)

	if a.modeExplicit || a.classifier == nil {
		if a.modeExplicit {
			a.forceMode = modes.Mode(a.mode)
		}
		a.modeExplicit = false
		return a.sendTurn(firstUser, input, safe)
	}
	a.modeExplicit = false
	a.preSend = true
	return a.classifyAndSend(input, safe, firstUser)
}

// echoUser appends a submitted prompt to the transcript and persists it as a
// user entry.
func (a *App) echoUser(input string) {
	a.messages = append(a.messages, components.Message{Role: "user", Content: input})
	a.appendEntry(session.Entry{Type: "user", Role: "user", Content: input})
}

// sendTurnNoEcho starts the agent turn for a prompt already echoed to the
// transcript. The echoed prompt is the transcript's last user message, so the
// validated attachments are folded into it rather than appending a duplicate
// turn.
func (a *App) sendTurnNoEcho(input string, atts []run.Attachment) tea.Cmd {
	turns := a.buildTurns()
	if n := len(turns); n > 0 && turns[n-1].Role == "user" {
		turns[n-1].Attachments = atts
		return a.send(turns)
	}
	turns = append(turns, run.Turn{Role: "user", Content: input, Attachments: atts})
	return a.send(turns)
}

// sendTurn starts the agent turn and, for the session's first prompt, also
// kicks off the async session-naming call.
func (a *App) sendTurn(firstUser bool, input string, atts []run.Attachment) tea.Cmd {
	a.preSend = false
	cmd := a.sendTurnNoEcho(input, atts)
	if firstUser && a.shouldAutoName() {
		a.nameRequested = true
		return tea.Batch(cmd, a.nameSessionCmd(input))
	}
	return cmd
}

// classifyAndSend runs the mode classifier in a goroutine and sends the turn
// when the decision lands. By the time this command starts, the prompt is
// already echoed and the Role Manager indicator is up.
func (a *App) classifyAndSend(input string, atts []run.Attachment, firstUser bool) tea.Cmd {
	c := a.classifier
	ctx := a.ctx
	return func() tea.Msg {
		d, err := rolemanager.Select(ctx, c, rolemanager.ModeInput{Prompt: input})
		return modeClassifiedMsg{input: input, atts: atts, firstUser: firstUser, decision: d, err: err}
	}
}

// handleModeClassified applies the mode decision and starts the agent turn.
// A prompt cancelled with esc during classification is dropped.
func (a *App) handleModeClassified(m modeClassifiedMsg) tea.Cmd {
	if !a.preSend {
		return nil
	}
	a.applyModeDecision(m.decision, m.err)
	return a.sendTurn(m.firstUser, m.input, m.atts)
}

// handleAgentReady starts the streaming turn once the async session build
// lands. A turn cancelled while the session was building is dropped: the ctx
// was already cancelled and the phase cleared by the esc path.
func (a *App) handleAgentReady(m agentReadyMsg) tea.Cmd {
	if a.ctx != nil && a.ctx.Err() != nil {
		return nil // cancelled during the build
	}
	if m.err != nil {
		a.cancel = nil
		a.endPhase()
		a.addSystem("agent error: " + m.err.Error())
		return nil
	}
	a.agent = m.sess
	return a.startAgent(m.sess, m.history, m.in)
}

// setPhaseRoleManager marks the Role Manager as the active signal, with the
// sub-phase used for the composer caption.
func (a *App) setPhaseRoleManager(subphase string) {
	a.startPhase()
	a.phase = phaseRoleManager
	a.rmPhase = subphase
}

// setPhaseWorking marks generic I/O with no Role Manager signal.
func (a *App) setPhaseWorking() {
	a.startPhase()
	a.phase = phaseWorking
}

// startPhase stamps the turn-start time once per in-flight turn. A turn runs
// through several sub-phases (pre-prompt, working, tool result, …) but the
// elapsed label counts from the user pressing enter, so it is set only when
// no turn is already running.
func (a *App) startPhase() {
	if a.phaseStartedAt.IsZero() {
		a.phaseStartedAt = time.Now()
	}
}

// elapsedLabel renders the live turn elapsed time, or "" when idle.
func (a *App) elapsedLabel() string {
	if a.phaseStartedAt.IsZero() {
		return ""
	}
	return " · " + time.Since(a.phaseStartedAt).Round(100*time.Millisecond).String()
}

// endPhase marks the in-flight prompt finished.
func (a *App) endPhase() {
	a.phase = phaseIdle
	a.phaseStartedAt = time.Time{}
}

// rmCaption is the sub-phase caption shown beside the role manager pill.
func (a *App) rmCaption() string {
	switch a.rmPhase {
	case agent.RoleManagerPhaseToolResult:
		return "classifying tool result"
	case agent.RoleManagerPhaseSteer:
		return "classifying steering"
	case agent.RoleManagerPhaseClarify:
		return "clarifying"
	default:
		return "pre-prompt processing"
	}
}

// nextEvent reads one agent event, replaying a coalescing lookahead first.
func (a *App) nextEvent() (agent.Event, bool) {
	if a.pendingEventSet {
		a.pendingEventSet = false
		return a.pendingEvent, true
	}
	e, ok := <-a.events
	return e, ok
}

// nextAgent drains the agent event channel with coalescing. Consecutive
// text (or reasoning) deltas are concatenated into a single event so Bubble
// Tea updates — and therefore full transcript re-renders — happen once per
// drain, not once per streamed token. The loop stops at the first event of a
// different kind (which is stashed as a lookahead and replayed next) or at an
// empty channel, so nothing is dropped and ordering is preserved.
func (a *App) nextAgent() tea.Cmd {
	return func() tea.Msg {
		e, ok := a.nextEvent()
		if !ok {
			return agentEventMsg{Kind: agent.EventDoneKind}
		}
		if e.Kind != agent.EventTextKind && e.Kind != agent.EventReasoningKind {
			return agentEventMsg(e)
		}

		var b strings.Builder
		if e.Kind == agent.EventTextKind {
			b.WriteString(e.Text)
		} else {
			b.WriteString(e.Reasoning)
		}
		for {
			var next agent.Event
			select {
			case n, ok2 := <-a.events:
				if !ok2 {
					return agentEventMsg(coalesced(e, b.String()))
				}
				next = n
			default:
				return agentEventMsg(coalesced(e, b.String()))
			}
			if next.Kind != e.Kind {
				a.pendingEvent = next
				a.pendingEventSet = true
				return agentEventMsg(coalesced(e, b.String()))
			}
			if e.Kind == agent.EventTextKind {
				b.WriteString(next.Text)
			} else {
				b.WriteString(next.Reasoning)
			}
		}
	}
}

// coalesced folds a run of same-kind deltas accumulated in b back into the
// first event's text (or reasoning) field.
func coalesced(first agent.Event, acc string) agent.Event {
	if first.Kind == agent.EventTextKind {
		first.Text = acc
	} else {
		first.Reasoning = acc
	}
	return first
}

// agentSession builds (or reuses) the agent session for the current
// config/posture/permissions. It is invalidated whenever any of those change.
// sessionBuildParams is an immutable snapshot of every App field a session
// build reads. It is captured on the Bubble Tea goroutine so the async build
// goroutine never races a config change.
type sessionBuildParams struct {
	workdir      string
	settings     config.Settings
	cfg          run.Config
	client       *http.Client
	posture      posture.Policy
	planMode     bool
	allowClarify bool
}

func (a *App) sessionBuildParams() sessionBuildParams {
	return sessionBuildParams{
		workdir:      a.workdir,
		settings:     a.settings,
		cfg:          a.cfg,
		client:       a.client,
		posture:      a.posture,
		planMode:     a.planMode,
		allowClarify: true,
	}
}

// buildAgentSession constructs a top-level agent session from a snapshot. It
// is the pure construction half of agentSession, safe to run off the Bubble
// Tea goroutine.
func buildAgentSession(p sessionBuildParams) (*agent.Session, error) {
	reg := tools.Default(p.workdir, p.settings.BashReadOnlyEnabled())
	perms := permissions.From(p.settings.Permissions.Allow, p.settings.Permissions.Ask, p.settings.Permissions.Deny)
	var promptOpts prompt.Options
	if p.settings.Caveman != nil && *p.settings.Caveman {
		promptOpts.Caveman = true
	}
	return agent.NewSession(agent.Options{
		Cfg:           p.cfg,
		Client:        p.client,
		Registry:      reg,
		Perms:         perms,
		Posture:       p.posture,
		PlanMode:      p.planMode,
		Workdir:       p.workdir,
		Settings:      p.settings,
		PromptOptions: promptOpts,
		// Top-level session: explore subagents may fan out from here. A
		// subagent sets this false so it can never fan out again.
		AllowExplore: true,
		// Top-level TUI session: the user is present, so the interactive
		// clarification loop may run. Subagents and the non-interactive CLI
		// leave this false.
		AllowClarify: true,
		// Top-level goal-mode prompts may run the unbounded pass loop; a
		// subagent never does.
		AllowPassLoop: true,
	})
}

// agentSession returns the cached session, building it from the live App state
// when absent. It is the synchronous path used by callers that cannot defer to
// a tea.Cmd (for example the /agent builder).
func (a *App) agentSession() (*agent.Session, error) {
	if a.agent != nil {
		return a.agent, nil
	}
	sess, err := buildAgentSession(a.sessionBuildParams())
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
		// A height-only change leaves the rendered body identical, so the
		// content compare in chatView cannot catch it: clear the selection
		// explicitly, or it would highlight stale rows.
		a.sel = selection{}
		return a, nil

	case tickMsg:
		a.refreshFooter()
		return a, tea.Batch(tickCmd(), a.refreshGitInfoCmd())

	case streamChunkMsg:
		return a, a.handleStreamChunk(m)

	case agentEventMsg:
		return a, a.handleAgentEvent(m)

	case agentReadyMsg:
		return a, a.handleAgentReady(m)

	case credentialsResolvedMsg:
		return a, a.handleCredentialsResolved(m)

	case copiedMsg:
		a.addSystem(m.text)
		return a, nil

	case compactDoneMsg:
		return a, a.handleCompactDone(m)

	case sessionNamedMsg:
		return a, a.handleSessionNamed(m)

	case gitInfoMsg:
		a.applyGitInfo(m.info, m.ok)
		a.refreshFooter()
		return a, nil

	case codeReviewDoneMsg:
		return a, a.handleCodeReviewDone(m)

	case agentBuilderDoneMsg:
		return a, a.handleAgentBuilderDone(m)

	case bgAgentEventMsg:
		return a, a.handleBgAgentEvent(m)

	case modelsFetchedMsg:
		return a, a.handleModelsFetched(m)

	case attachValidatedMsg:
		return a, a.handleAttachValidated(m)

	case shellDoneMsg:
		return a, a.handleShellDone(m)

	case modeClassifiedMsg:
		return a, a.handleModeClassified(m)

	case tea.MouseMsg:
		var vpCmd, copyCmd tea.Cmd
		if a.view == viewChat {
			// Branch order is load-bearing:
			//   1. wheel — wheel events are Action==Press with a wheel button;
			//      this must precede the press branch or every tick re-anchors
			//      the selection. It also works mid-drag: the selection is
			//      stored in content coordinates, so scrolling never invalidates
			//      it — press, wheel, keep dragging, release.
			//   2. release — X10 reports release with Button==None while SGR
			//      keeps Left, so the button is never tested here.
			//   3. motion / 4. left press. Press, motion and release are not
			//      forwarded to a.vp: viewport v1.0.0 discards them anyway.
			switch {
			case tea.MouseEvent(m).IsWheel():
				a.vp, vpCmd = a.vp.Update(m)
				a.follow = a.vp.AtBottom()
			case m.Action == tea.MouseActionRelease && a.sel.dragging:
				a.sel.dragging = false
				if a.sel.empty() {
					// A release at the anchor cell (bare click) clears the
					// selection and copies nothing.
					a.sel.active = false
				} else {
					a.sel.active = true
					copyCmd = a.copySelection()
				}
			case m.Action == tea.MouseActionMotion && a.sel.dragging:
				a.sel.cursor = clampPos(m.X, m.Y, a.lastFrame)
				a.sel.active = !a.sel.empty()
			case m.Action == tea.MouseActionPress && m.Button == tea.MouseButtonLeft:
				if p, ok := contentPos(m.X, m.Y, a.lastFrame); ok {
					a.sel.anchor = p
					a.sel.cursor = p
					a.sel.dragging = true
					a.sel.active = true
				} else {
					// A press outside the viewport clears the selection and
					// copies nothing.
					a.sel = selection{}
				}
			default:
				// Anything else (right/middle press, stray motion) keeps today's
				// behaviour.
				a.vp, vpCmd = a.vp.Update(m)
				a.follow = a.vp.AtBottom()
			}
		}
		cmd := a.editor.Update(m)
		a.autocomplete = a.registry.Complete(a.editor.Value())
		spin, spinCmd := a.attachSpin.Update(m)
		a.attachSpin = spin
		workSpin, workCmd := a.workSpin.Update(m)
		a.workSpin = workSpin
		if a.phase == phaseIdle {
			workCmd = nil
		}
		if !a.hasPendingAttachments() {
			spinCmd = nil
		}
		a.relayout()
		return a, tea.Batch(vpCmd, copyCmd, cmd, spinCmd, workCmd)

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
	workSpin, workCmd := a.workSpin.Update(msg)
	a.workSpin = workSpin
	if a.phase == phaseIdle {
		workCmd = nil
	}
	if !a.hasPendingAttachments() {
		spinCmd = nil
	}
	a.relayout()
	return a, tea.Batch(cmd, spinCmd, workCmd)
}

func (a *App) handleChatKey(m tea.KeyMsg) tea.Cmd {
	if a.savePromptMode {
		return a.handleSavePromptKey(m)
	}
	if a.historyActive {
		return a.handleHistoryKey(m)
	}

	switch m.String() {
	case "pgup", "pgdown", "shift+up", "shift+down", "ctrl+home", "ctrl+end":
		switch m.String() {
		case "pgup":
			a.vp.PageUp()
		case "pgdown":
			a.vp.PageDown()
		case "shift+up":
			a.vp.ScrollUp(1)
		case "shift+down":
			a.vp.ScrollDown(1)
		case "ctrl+home":
			a.vp.GotoTop()
		case "ctrl+end":
			a.vp.GotoBottom()
		}
		a.follow = a.vp.AtBottom()
		return nil
	case "shift+tab":
		a.cycleMode()
		return nil
	case "ctrl+l":
		// Clear the transcript view; the session is untouched.
		a.messages = nil
		return nil
	case "ctrl+o":
		// Toggle full output for all truncated turns and tool results, then
		// re-attach to the tail so the reflow lands somewhere sensible.
		a.expandAll = !a.expandAll
		a.follow = true
		return nil
	case "esc":
		// A live selection is cleared first, ahead of the existing esc
		// behaviour: the first esc dismisses the highlight, the second does
		// whatever esc would have done (cancel the request, drop pre-send…).
		if a.sel.active || a.sel.dragging {
			a.sel = selection{}
			return nil
		}
		if a.pendingInput != "" {
			a.pendingInput = ""
			return a.submitInput(strings.TrimSpace(a.editor.Value()))
		}
		if a.preSend {
			// Cancel the in-flight pre-send: mode classification is still
			// running; the turn is dropped when the decision lands.
			a.preSend = false
			a.endPhase()
			a.addSystem("request cancelled")
			return nil
		}
		if a.cancel != nil {
			a.cancel()
			a.cancel = nil
			a.endPhase()
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
		if a.working() {
			a.messages = append(a.messages, components.Message{Role: "user", Content: input, Steering: true})
			if a.agent == nil || !a.agent.Steer(input) {
				a.addSystem("steering queue full — message dropped")
			}
			a.editor.Reset()
			a.autocomplete = nil
			a.autocompleteIndex = 0
			return nil
		}
		if a.preSend {
			// The previous prompt is still in pre-send classification; the
			// model has not started, so there is nothing to steer yet.
			a.addSystem("still preparing the previous prompt — one moment")
			return nil
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

	return a.forwardToEditor(m)
}

// forwardToEditor hands a key to the composer and refreshes the state that
// depends on its contents.
func (a *App) forwardToEditor(m tea.KeyMsg) tea.Cmd {
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
	case tea.KeySpace:
		a.historyQuery += " "
	default:
		// Any other key — backspace, cursor motion, delete — means the user is
		// done browsing and wants to edit the prompt that was loaded. Leave the
		// cycle with the loaded text intact and hand the key to the editor so it
		// performs its normal edit instead of clearing the composer.
		a.exitHistoryCycle(true)
		return a.forwardToEditor(m)
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

// exitHistoryCycle leaves the browse cycle. accept keeps whatever prompt is
// loaded in the composer; otherwise the text from before the cycle is restored.
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
		a.messages[len(a.messages)-1].AppendText(m.Text)
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
		a.messages[len(a.messages)-1].Materialise()
	}
	a.appendAssistant(m.Usage)
	a.refreshFooter()
	return nil
}

// handleAgentEvent renders one agent streaming event.
func (a *App) handleAgentEvent(m agentEventMsg) tea.Cmd {
	evStart := time.Now()
	defer func() { a.trace.Event("tui", "agent_event", time.Since(evStart)) }()
	switch m.Kind {
	case agent.EventErrorKind:
		if !a.phaseStartedAt.IsZero() {
			a.trace.Event("tui", "turn_total", time.Since(a.phaseStartedAt))
		}
		a.cancel = nil
		a.preSend = false
		a.endPhase()
		// Drop a trailing empty assistant bubble so an aborted turn does not
		// leave a bare frame above the error row.
		if last := len(a.messages) - 1; last >= 0 && a.messages[last].Role == "assistant" &&
			strings.TrimSpace(a.messages[last].Text()) == "" && len(a.messages[last].ToolCalls) == 0 {
			a.messages = a.messages[:last]
		}
		// A partially streamed assistant bubble keeps its accumulated text: the
		// turn is over, so flush the builder back into Content.
		if last := len(a.messages) - 1; last >= 0 && a.messages[last].Role == "assistant" {
			a.messages[last].Materialise()
		}
		a.addSystem("agent error: " + m.Err.Error())
		return nil
	case agent.EventTextKind:
		a.setPhaseWorking()
		if len(a.messages) == 0 || a.messages[len(a.messages)-1].Role != "assistant" {
			a.messages = append(a.messages, components.Message{Role: "assistant"})
		}
		a.messages[len(a.messages)-1].AppendText(m.Text)
		return a.nextAgent()
	case agent.EventReasoningKind:
		a.setPhaseWorking()
		if len(a.messages) == 0 || a.messages[len(a.messages)-1].Role != "reasoning" {
			a.messages = append(a.messages, components.Message{Role: "reasoning"})
		}
		a.messages[len(a.messages)-1].AppendText(m.Reasoning)
		return a.nextAgent()
	case agent.EventToolCallDeltaKind:
		a.setPhaseWorking()
		// Render-only; no execution authority. The live fragment updates the
		// pending tool row if one is present.
		if len(a.messages) > 0 && a.messages[len(a.messages)-1].Role == "tool" {
			a.messages[len(a.messages)-1].ToolArgs += m.ToolDelta.Args
		}
		return a.nextAgent()
	case agent.EventToolStartKind:
		a.setPhaseWorking()
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
			StartedAt:  time.Now(),
		})
		return a.nextAgent()
	case agent.EventToolResultKind:
		if len(a.messages) > 0 && a.messages[len(a.messages)-1].Role == "tool" {
			a.messages[len(a.messages)-1].SetContent(m.ToolResult)
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
	case agent.EventClarifyAskKind:
		q := *m.Clarify
		a.clarifyState = newClarifyState(q, m.Reply)
		a.addSystem(formatQuestionnaire(q))
		return a.push(viewClarify)
	case agent.EventRoleManagerKind:
		// The Role Manager is actively classifying (admission, mode selection,
		// steering, or a tool result): show the dedicated indicator instead of
		// the generic working label.
		a.setPhaseRoleManager(m.Phase)
		return a.nextAgent()
	case agent.EventRetryKind:
		a.setPhaseWorking()
		// Dim the current assistant bubble so the user knows it is partial and
		// will not be replayed into context when the retry starts. Start a
		// fresh assistant bubble for the retry output.
		if len(a.messages) > 0 && a.messages[len(a.messages)-1].Role == "assistant" {
			a.messages[len(a.messages)-1].Partial = true
		}
		a.messages = append(a.messages, components.Message{Role: "assistant"})
		a.addSystem(fmt.Sprintf("retrying (%d/%d) after %s — %s", m.RetryAttempt, 10, m.RetryDelay.Round(time.Millisecond), m.RetryReason))
		return a.nextAgent()
	case agent.EventPassKind:
		// A new goal-mode pass started. The agent owns the todo list; it is
		// carried here so the panel renders live without the TUI re-deriving
		// it from transcript text.
		if m.Todos != nil {
			a.setTodos(m.Todos)
		}
		return a.nextAgent()
	case agent.EventTodosKind:
		if m.Todos != nil {
			a.setTodos(m.Todos)
		}
		return a.nextAgent()
	case agent.EventGoalEvalKind:
		if m.Todos != nil {
			a.setTodos(m.Todos)
		}
		if m.GoalSentinel != "" {
			a.addSystem(fmt.Sprintf("goal evaluator: %s (pass %d)", m.GoalSentinel, m.Pass))
		}
		return a.nextAgent()
	case agent.EventDoneKind:
		if !a.phaseStartedAt.IsZero() {
			a.trace.Event("tui", "turn_total", time.Since(a.phaseStartedAt))
		}
		a.cancel = nil
		a.endPhase()
		if len(a.messages) > 0 && a.messages[len(a.messages)-1].Role == "assistant" {
			a.messages[len(a.messages)-1].Usage = m.Result.Usage
		}
		// Backfill the final reply into the trailing assistant bubble so a
		// turn that streamed only tool calls or arrived as one final chunk is
		// never persisted (or rendered) as an empty frame.
		if last := len(a.messages) - 1; last >= 0 && a.messages[last].Role == "assistant" &&
			strings.TrimSpace(a.messages[last].Text()) == "" && m.Result.Reply != "" {
			a.messages[last].SetContent(m.Result.Reply)
		}
		// The turn is over: flush any streamed builder back into Content so the
		// message is a plain value for the persistence and rebuild paths.
		if last := len(a.messages) - 1; last >= 0 && a.messages[last].Role == "assistant" {
			a.messages[last].Materialise()
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
				a.lastPlanText = strings.Join(steps, "\n")
				l := todos.New(m.Result.SanitizedPrompt, steps)
				a.setTodos(&l)
				a.addSystem(fmt.Sprintf("plan: %d steps extracted — /execute, /stay, or /refine", len(steps)))
			}
		}
		// Advance the shared todo list from [DONE:n] markers in the assistant
		// reply. Only assistant text is passed — tool results never reach this.
		if a.todos != nil && m.Result.Reply != "" {
			a.todos.ApplyMarkers(m.Result.Reply)
			a.appendEntry(a.todos.ToEntry(""))
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
			turns = append(turns, run.Turn{Role: m.Role, Content: m.Text()})
		case "assistant":
			turn := run.Turn{Role: m.Role, Content: m.Text()}
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
				Content:    m.Text(),
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
	renderStart := time.Now()
	a.relayout()
	body, lm := components.MessageList{
		Messages:      a.messages,
		Width:         a.contentWidth(),
		ExpandAll:     a.expandAll,
		ShowReasoning: a.reasoningVisible(),
		ShowTools:     a.toolCallsVisible(),
	}.Render()
	// The one content compare replaces an enumerated clear list: new message,
	// streaming delta, ctrl+l, ctrl+o, ctrl+r, ctrl+t and width changes all
	// clear a stale selection for free.
	if body != a.lastBody {
		a.sel = selection{}
		a.lastBody = body
	}
	if a.sel.active {
		// Highlighting happens before SetContent, so the selection looks like
		// a content change and self-clears on the next message.
		from, to := components.Order(a.sel.anchor, a.sel.cursor)
		body = components.Highlight(body, lm, from, to, a.vp.YOffset, a.vp.Height)
	}
	a.vp.SetContent(body)
	if a.follow {
		a.vp.GotoBottom()
	}
	// The frame snapshot must come after GotoBottom, which mutates YOffset.
	a.lastFrame = frame{
		lines:   lm,
		top:     a.headerHeight(),
		left:    a.contentLeft(),
		width:   a.vp.Width,
		height:  a.vp.Height,
		yOffset: a.vp.YOffset,
	}

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
	if a.todosVisible() {
		sb.WriteString(a.renderTodoPanel())
		sb.WriteString("\n")
	}
	sb.WriteString(a.renderComposer())
	sb.WriteString("\n")
	a.refreshFooter()
	sb.WriteString(a.footer.View())

	// Per-frame render timing: first_paint once, then one render event per
	// frame while a turn is in flight. Idle frames (typing, scrolling) are
	// deliberately not traced.
	el := time.Since(renderStart)
	if !a.painted {
		a.painted = true
		a.trace.Event("tui", "first_paint", el)
	} else if a.phase != phaseIdle {
		a.trace.Event("tui", "render", el)
	}

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
	if a.phase == phaseRoleManager {
		// Role Manager activity gets its own branded signal: a filled
		// "role manager" pill plus a sub-phase caption. The generic working
		// label is reserved for I/O without this signal.
		pill := components.Chip("role manager", components.ColorTeal)
		title = a.spinMark() + " " + pill + " " + components.MutedStyle.Render(a.rmCaption()+a.elapsedLabel())
		accent = lipgloss.TerminalColor(components.ColorTeal)
		if a.preSend {
			meta = "preparing · esc cancel"
		} else {
			meta = "⏎ steer · esc cancel"
		}
	} else if a.phase == phaseWorking {
		title = a.spinMark() + " working" + a.elapsedLabel()
		accent = lipgloss.TerminalColor(components.ColorAmber)
		meta = "⏎ steer · esc cancel"
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

// spinMark is the working-indicator spinner, or a static dot when the
// ui.spinner setting is off.
func (a *App) spinMark() string {
	if !a.settings.SpinnerEnabled() {
		return "•"
	}
	return a.workSpin.View()
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
	a.state.LastMode = a.mode
	_ = config.SaveState(a.state)
}

func (a *App) saveState() {
	a.state.Model = a.cfg.Model
	a.state.Provider = a.cfg.Provider
	a.state.LastMode = a.mode
	_ = config.SaveState(a.state)
}

// saveSession persists the active session id plus the last-used model/provider
// and mode. The in-memory a.state is the single writer of state.json in this
// process, so it is mutated and saved directly instead of re-reading the file
// first — the reload was redundant disk I/O per mode keypress.
func (a *App) saveSession() {
	a.state.ActiveSession = a.sessionID
	a.state.Model = a.cfg.Model
	a.state.Provider = a.cfg.Provider
	a.state.LastMode = a.mode
	_ = config.SaveState(a.state)
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
	a.applyModeDecision(d, err)
}

// applyModeDecision records a mode decision (or its error) on the session: the
// mode chip, the engaged named agent, and warning lines.
func (a *App) applyModeDecision(d rolemanager.ModeDecision, err error) {
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

// resolveCredentialsCmd re-prepares provider configuration with the resolver
// (which may probe the host keychain) on a background command. It is the async
// half of the first-frame paint: New resolved from environment only, and this
// lands the richer resolver answer once it is ready.
func (a *App) resolveCredentialsCmd() tea.Cmd {
	resolver := a.resolver
	model := a.settings.Model
	effort := a.settings.Effort
	name := a.requestedProvider
	cls := a.settings.Classifier
	return func() tea.Msg {
		src := run.CredentialSource(run.EnvSource(os.Getenv))
		if resolver != nil {
			src = resolver
			// Sole-provider fallback: when no provider was configured, a single
			// configured provider beats the openai default.
			if name == "" {
				if configured := resolver.ConfiguredProviders(); len(configured) == 1 {
					name = configured[0]
				}
			}
		}
		cfg, status := run.Prepare(model, name, src)
		cfg.Effort = effort
		if cc, err := run.ResolveClassifier(cfg, cls, src); err == nil {
			cfg.Classifier = cc
		}
		return credentialsResolvedMsg{cfg: cfg, status: status}
	}
}

// handleCredentialsResolved adopts the resolver-based configuration. A pending
// seed prompt that only the resolver could configure is sent now; a still
// unconfigured provider surfaces the credential hint.
func (a *App) handleCredentialsResolved(m credentialsResolvedMsg) tea.Cmd {
	a.cfg = m.cfg
	a.status = m.status
	a.classifier = nil
	a.invalidateAgentSession()
	if m.status.Configured {
		a.SetClassifier(run.NewClassifier(m.cfg, a.client))
		if a.bgManager == nil {
			a.bgManager = bgagent.NewManager(a.workdir, m.cfg, a.client, a.settings, a.posture)
		}
	} else {
		a.showCredentialMessage(m.cfg.Provider, a.resolver)
	}
	a.setCredentialBackendDefault()
	a.refreshFooter()
	if a.pending != "" && m.status.Configured {
		return a.sendPending()
	}
	return nil
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
	if cc, err := run.ResolveClassifier(cfg, a.settings.Classifier, src); err == nil {
		cfg.Classifier = cc
	}
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

	est := a.contextEstimate()
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

// contextEstimate memoises the footer's context-window estimate. refreshFooter
// runs every rendered frame, and both transcriptMessages (an allocation per
// message) and EstimateContext (a full walk) are wasted on an unchanged
// transcript. The key is the message count plus the tail message's role and
// content length, so it is stable while idle and recomputes only when the
// transcript grows (a new message or a streaming delta).
func (a *App) contextEstimate() transcript.Estimate {
	key := a.estimateKey()
	if a.estInit && a.estKey == key {
		return a.est
	}
	a.estInit = true
	a.estKey = key
	a.est = transcript.EstimateContext(a.transcriptMessages())
	return a.est
}

func (a *App) estimateKey() string {
	n := len(a.messages)
	if n == 0 {
		return "0"
	}
	last := a.messages[n-1]
	return fmt.Sprintf("%d:%s:%d", n, last.Role, len(last.Text()))
}

// refreshGitInfoCmd runs git-context detection off the render goroutine. The
// result lands as a gitInfoMsg and is applied to the footer.
func (a *App) refreshGitInfoCmd() tea.Cmd {
	workdir := a.workdir
	return func() tea.Msg {
		info, ok := gitinfo.Detect(workdir)
		return gitInfoMsg{info: info, ok: ok}
	}
}

// applyGitInfo records detected repository context. The first successful
// detection adds the branch/cwd line to the footer, which changes its height
// at the same width, so the memo is dropped.
func (a *App) applyGitInfo(info gitinfo.Info, ok bool) {
	if !ok {
		return
	}
	a.gitInfo = info
	if !a.gitOK {
		a.gitOK = true
		a.footerW = -1
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

// copySelection puts the selected clean text on the clipboard through the
// same copiedMsg path as copyPrompt. The text comes from lastFrame.lines —
// the frame the user was looking at, with borders, prefixes and ANSI already
// removed by the renderers and truncation markers expanded to their hidden
// remainder.
//
// The resulting copiedMsg calls addSystem, which changes the body and so
// clears the highlight on the next frame. That is the intended "copied,
// done" feel — do not "fix" it.
func (a *App) copySelection() tea.Cmd {
	text := a.lastFrame.lines.Text(a.sel.anchor, a.sel.cursor)
	if text == "" {
		return nil
	}
	return func() tea.Msg {
		method, err := clipboard.Copy(text)
		if err != nil {
			return copiedMsg{text: "copy failed: " + err.Error()}
		}
		note := ""
		// OSC 52 is a silent-drop risk for large payloads (xterm's
		// maxStringParseSize, tmux without set-clipboard on); say so.
		if method == "osc52" && len(text) > 8*1024 {
			note = " — large payload, terminal may have dropped it"
		}
		lines := strings.Count(text, "\n") + 1
		return copiedMsg{text: fmt.Sprintf("copied %d lines to clipboard (%s)%s", lines, method, note)}
	}
}

// transcriptMessages maps the TUI transcript onto provider-neutral messages.
func (a *App) transcriptMessages() []transcript.Message {
	out := make([]transcript.Message, 0, len(a.messages))
	for _, m := range a.messages {
		out = append(out, transcript.Message{
			Role:    m.Role,
			Content: m.Text(),
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
	content := a.messages[len(a.messages)-1].Text()
	if strings.TrimSpace(content) == "" {
		return
	}
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
	// The todo list belongs to the session that produced it. Carrying it into
	// a fresh one would render stale work and write it back under the new id.
	a.todos = nil
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

// handleAgentBuilderDone renders the result of an async /agent create run.
func (a *App) handleAgentBuilderDone(m agentBuilderDoneMsg) tea.Cmd {
	if m.err != nil {
		a.addSystem("agent builder failed: " + m.err.Error())
		return nil
	}
	a.addSystem("agent saved: " + m.path)
	return nil
}

// handleBgAgentEvent appends a background-agent event as a system line.
func (a *App) handleBgAgentEvent(m bgAgentEventMsg) tea.Cmd {
	name := m.AgentName
	switch m.Kind {
	case agent.EventErrorKind:
		if m.Err != nil {
			a.addSystem(fmt.Sprintf("[agent:%s] error: %s", name, m.Err.Error()))
		}
	case agent.EventTextKind:
		a.addSystem(fmt.Sprintf("[agent:%s] %s", name, m.Text))
	case agent.EventToolStartKind:
		a.addSystem(fmt.Sprintf("[agent:%s] tool: %s", name, m.ToolName))
	case agent.EventToolResultKind:
		a.addSystem(fmt.Sprintf("[agent:%s] result: %s", name, m.ToolResult))
	case agent.EventDoneKind:
		a.addSystem(fmt.Sprintf("[agent:%s] done", name))
	}
	if inst, ok := a.bgManager.Lookup(name); ok && inst.State != bgagent.StateDone {
		return a.watchAgentEvents(name)
	}
	return nil
}

func (a *App) watchAgentEvents(name string) tea.Cmd {
	inst, ok := a.bgManager.Lookup(name)
	if !ok {
		return nil
	}
	return func() tea.Msg {
		e, open := <-inst.Events
		if !open {
			return bgAgentEventMsg{AgentName: name, Kind: agent.EventDoneKind}
		}
		return bgAgentEventMsg(e)
	}
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
	// Compaction forks the session; the todo list survives it. Re-appending it
	// under the new id keeps the in-memory panel and the new session file in
	// agreement, so rehydrating the compacted session restores the same list.
	if a.todos != nil && len(a.todos.Items) > 0 {
		a.appendEntry(a.todos.ToEntry(""))
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

// working reports whether an agent turn is in flight. It is true from send()
// until the turn's EventDone or EventError arrives (cancel is cleared by the
// done/error path via the request completing), and drives the composer's
// working state and steering submit.
func (a *App) working() bool { return a.cancel != nil }

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
