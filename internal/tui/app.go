package tui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/signet/internal/activity"
	"github.com/vulnetix/signet/internal/agent"
	"github.com/vulnetix/signet/internal/agentpool"
	"github.com/vulnetix/signet/internal/agentprofile"
	"github.com/vulnetix/signet/internal/aifirewall"
	"github.com/vulnetix/signet/internal/bgagent"
	"github.com/vulnetix/signet/internal/clipboard"
	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/credentials"
	"github.com/vulnetix/signet/internal/gitinfo"
	"github.com/vulnetix/signet/internal/httpclient"
	"github.com/vulnetix/signet/internal/localinfer"
	"github.com/vulnetix/signet/internal/machineprobe"
	"github.com/vulnetix/signet/internal/modelinfo"
	"github.com/vulnetix/signet/internal/models"
	"github.com/vulnetix/signet/internal/modes"
	"github.com/vulnetix/signet/internal/permissions"
	"github.com/vulnetix/signet/internal/plans"
	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/profiles"
	"github.com/vulnetix/signet/internal/projectregistry"
	"github.com/vulnetix/signet/internal/prompt"
	"github.com/vulnetix/signet/internal/promptlib"
	"github.com/vulnetix/signet/internal/provider"
	"github.com/vulnetix/signet/internal/repoindex"
	"github.com/vulnetix/signet/internal/repomap"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/scanartifacts"
	"github.com/vulnetix/signet/internal/session"
	"github.com/vulnetix/signet/internal/todos"
	"github.com/vulnetix/signet/internal/tools"
	"github.com/vulnetix/signet/internal/trace"
	"github.com/vulnetix/signet/internal/transcript"
	"github.com/vulnetix/signet/internal/tui/components"
	"github.com/vulnetix/signet/internal/version"
	"github.com/vulnetix/signet/internal/vulnetixcli"
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

	// ResumeKey and ResumeSession load an existing session instead of minting
	// a fresh one. ResumeSession is a fully-resolved id; ResumeKey addresses
	// its project. Both are set by the CLI --resume flag.
	ResumeKey     session.Key
	ResumeSession string
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
	name    string
	profile agentprofile.AgentProfile
	path    string
	handle  *activity.Handle
	err     error
}

// modeClassifiedMsg carries the async mode-classification outcome plus the
// prompt to send once the decision lands.
type modeClassifiedMsg struct {
	input     string
	atts      []run.Attachment
	directive string
	firstUser bool
	decision  rolemanager.ModeDecision
	err       error
}

// vulnetixProbeMsg carries the result of probing the local Vulnetix CLI.
type vulnetixProbeMsg struct {
	cap vulnetixcli.Capabilities
	err error
}

// projectsLoadedMsg carries the project registry listing.
type projectsLoadedMsg struct {
	entries []projectregistry.Entry
	err     error
}

// sweepFoundMsg reports how many projects the sweep discovered.
type sweepFoundMsg struct {
	found int
	err   error
}

// artifactsLoadedMsg carries the artifact summary for the current project.
type artifactsLoadedMsg struct {
	summary scanartifacts.Summary
	err     error
}

// workingPhase describes what the in-flight prompt is doing so the composer
// can show a specific signal instead of a bare "working": either the Role
// Manager is classifying content, or it is generic provider/disk I/O.
type workingPhase int

const (
	phaseIdle        workingPhase = iota // no prompt in flight
	phaseRoleManager                     // Role Manager is classifying (admission, mode, tool result, steering)
	phaseWorking                         // generic network/disk I/O with no specific signal
	phaseExploring                       // explore fan-out subagents are running
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

// localModelReportMsg carries the result of a /local-model subcommand,
// rendered as a system notice.
type localModelReportMsg struct{ text string }

const (
	editorMinHeight = 3
	editorMaxHeight = 12
)

// promptsViewState is declared in prompts_view.go, alongside the manager.

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
	// modeSticky is set when the user picks a mode by hand (/mode, shift+tab,
	// f5, --plan, or the plan review pane). It holds that mode for every
	// following turn until the user picks again: the classifier must never
	// silently reroute a plan session into the unbounded goal loop.
	modeSticky bool
	// forceMode carries that manual choice into the agent session. Suppressing
	// the TUI's own classification is not enough: Session.run classifies again
	// internally, so without this the user's explicit mode is discarded.
	// One-shot, matching modeExplicit.
	forceMode    modes.Mode
	autocomplete []string

	// mode classification (optional; nil skips auto-detection)
	classifier   rolemanager.Classifier
	cache        *rolemanager.Cache
	namedAgent   string
	modeDecision rolemanager.ModeDecision
	modeWarning  string

	// working indicator
	phase   workingPhase // current activity; phaseIdle when no prompt is in flight
	rmPhase string       // Role Manager sub-phase (agent.RoleManagerPhase*) for the caption
	preSend bool         // prompt echoed, awaiting the async mode classification
	// exploreDone/exploreTotal/exploreRef drive the phaseExploring composer
	// caption: N/M completed plus the reference currently being explored.
	exploreDone  int
	exploreTotal int
	exploreRef   string
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
	// live is the process-wide shared posture/ask holder. Every session (the
	// running one and the next one) shares this pointer, so a toggle pressed
	// mid-turn lands on the next gate check instead of the next prompt.
	live     *posture.Live
	planMode bool
	agent    *agent.Session
	events   <-chan agent.Event
	pending  string  // pending prompt to send once configured
	initCmd  tea.Cmd // resume command batched into Init(), set by New
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
	reasoningOverride  *bool
	toolCallsOverride  *bool
	guardrailsOverride *bool
	askOverride        *bool
	firewallOverride   *bool
	lastPlanText       string
	// pendingPlanExecute/planExecuteName are set by the plan review pane
	// when the user approves a plan. The next send consumes them and tells
	// the agent to load the plan as the execution carrier.
	pendingPlanExecute bool
	planExecuteName    string
	// pendingPlanRevision is the revision number requested for the next
	// plan-mode recording. Zero means "compute next available". Set by
	// submitPlanRefine so a refined plan is written as -rN.
	pendingPlanRevision int
	// pendingDirective is an explicit harness directive for the next user
	// turn. Non-empty values override the attachment-derived directive and
	// are consumed in submitInput.
	pendingDirective string

	// attachments state
	attachments  map[int]*attachment
	attachOrder  []int
	attachSeq    int
	attachSpin   spinner.Model
	workSpin     spinner.Model
	pendingInput string // prompt held while attachments validate

	// view state
	view                   viewState
	viewStack              []viewState
	credentialState        credentialViewState
	providersState         providersViewState
	providerDetailState    providerDetailViewState
	settingsState          settingsViewState
	modelState             modelViewState
	permState              permissionsViewState
	importState            importViewState
	clarifyState           clarifyViewState
	permAskState           permissionAskViewState
	agentState             agentViewState
	classifierState        classifierViewState
	planReview             planReviewState
	resumeState            resumeViewState
	vulnetixConfigState    vulnetixConfigState
	vulnetixListState      vulnetixListState
	vulnetixArtifactsState vulnetixArtifactsState
	promptsState           promptsViewState

	// dirPickState drives the /add-dir directory picker.
	dirPickState dirPickState

	// which providers the pickers may offer, filled by an async probe
	avail providerAvailability

	// workspaceDirs are additional directories added to the session with
	// /add-dir; they widen the tool confinement boundary.
	workspaceDirs []string
	// workspaceMaps hold the harness-computed repo maps for workspaceDirs.
	workspaceMaps []repomap.Map

	// live model catalogue cache (on-demand fetch)
	catalogCache   map[string][]models.Model
	catalogErr     map[string]string
	catalogLoading map[string]bool
	catalogURLs    map[string]string // in-flight fetch URL per provider

	// workdir and git
	workdir string
	// cwd is where the agent's relative paths currently resolve from. It
	// starts at workdir and follows the session's working directory as the
	// agent moves around inside it; workdir itself never moves, because it is
	// the confinement boundary everything else is measured against.
	cwd     string
	gitInfo gitinfo.Info
	gitOK   bool

	// settings / state persistence
	settings config.Settings
	eff      config.Effective
	flags    config.Settings
	state    config.State

	// session persistence
	store          *session.Store
	sessionID      string
	sessionName    string
	sessionKey     session.Key // project key for the live session (resume-aware)
	sessionWorkdir string      // the project path this session was resolved from
	lastEntryID    string      // ParentID for the next append
	persistedUpTo  int         // count of leading a.messages already written
	nameRequested  bool        // auto-naming already attempted for this session
	storeDisabled  bool        // a store error was reported; degrade to memory-only

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
	historyResults  []historyItem

	// autocomplete cycling; noAutocompleteSelection means nothing is
	// highlighted yet, so the first tab lands on the first candidate.
	autocompleteIndex int

	// agent picker: the profiles offered above the composer in agent mode,
	// the highlighted one (noAgentSelection when none is), and whether the
	// strip is currently open. The picker is opened explicitly by /agent or by
	// pressing enter in agent mode when no agent is engaged; it is never
	// triggered by typing @.
	agents          []agentChoice
	agentIndex      int
	agentPickerOpen bool
	// agentPickerSubmit marks a picker that a submit attempt opened, so
	// choosing an agent finishes that submit. Without it the prompt stays in
	// the composer and two enters produce no turn at all.
	agentPickerSubmit bool
	// agentArgSub is the /agent subcommand whose <name> argument the picker is
	// completing ("start", "stop", …). Empty means the picker is doing its
	// other job: choosing the carrier for the next turn.
	agentArgSub string
	// agentArgCands are the names offered for that argument, already narrowed
	// to what has been typed.
	agentArgCands []agentChoice
	// namedAgentTools is the engaged background definition's tool allowlist,
	// applied to the session it carries. Empty means every registered tool.
	namedAgentTools []string

	// file picker: the @ file chooser above the composer.
	files         []string  // workspace listing, slash paths relative to workdir
	filesLoadedAt time.Time // 30s TTL, mirrors availability.go
	filesLoading  bool      // an async listing is in flight
	fileIndex     int       // highlight; -1 = none
	fileScroll    int       // window start over fileCandidates
	fileDismissed string    // the @token esc/left closed on; cleared when it changes

	// save to library
	savePromptMode    bool
	savePromptValue   string
	savePromptConfirm bool   // naming mode found a duplicate; y/N gate open
	savePromptName    string // the slugged name being confirmed for overwrite

	// prompt-library state. loadedPrompt is the library file the composer's
	// text came from, so ctrl+s overwrites the right file in the right scope;
	// it is dropped the moment the composer stops representing that entry.
	loadedPrompt  *promptlib.Entry
	promptAction  bool   // ctrl+s action bar is open
	promptConfirm string // "" | "overwrite" | "delete"
	// promptLegacyNoticed stops the prompts.json migration notice repeating
	// every time a session touches the prompt library.
	promptLegacyNoticed bool

	// save-file flow: ctrl+s on a hovered file panel turns the composer into a
	// destination-path prompt that writes the panel's content on enter.
	saveFileMode bool
	saveFileMsg  int // message index of the file being saved; -1 = none

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

	// hover state: the last mouse position plus the target derived from it,
	// re-derived every frame in chatView so the footer hint can never point at
	// a panel the frame no longer shows. mousePresent is false until the first
	// mouse event, which keeps the feature inert when mouse capture is off.
	mouseX, mouseY int
	mousePresent   bool
	hover          hoverTarget

	// expandAll disables truncation and shows every message in full.
	expandAll bool

	// background agent manager
	bgManager *bgagent.Manager

	// agentPool caps every fan-out subagent (explore plus background agents)
	// behind one settings-backed FIFO queue. The Role Manager owns it through
	// the session pipeline; the TUI owns the instance so explore and background
	// agents share the same ceiling.
	agentPool *agentpool.Pool

	// subagent roster (Phase 4): chips persist across turns and are removed
	// only on an explicit dismiss.
	subagents    []components.SubagentChip // insertion order
	subagentIdx  map[string]int
	threadFilter string // "" == main (unfiltered)

	// runs panel: unified activity + subagent panel rendered above the composer.
	runsOpen   bool // panel visible
	runsFocus  bool // panel owns the keyboard
	runsTab    int  // 0 == activity, 1 == subagents
	runsSel    int  // selected item index (into runsItems())
	runsScroll int  // first visible item when the list is windowed

	// runsOutput is the full-screen reader for one run's output.
	runsOutput runsOutputState

	// activity registry: the honest register of every process Signet launches.
	activity *activity.Registry
	// activityAnnounced/activityFinished dedupe the thread start/finish lines
	// driven by the registry event stream.
	activityAnnounced map[string]bool
	activityFinished  map[string]bool
	// pendingActivitySends are finished-activity attachments waiting for the
	// in-flight turn to go idle before they flush as one turn.
	pendingActivitySends []activitySend

	// todos is the shared goal/plan todo list rendered in the chat chrome and
	// persisted to the session. The agent emits it; the TUI owns persistence.
	todos *todos.List

	// execEditor runs $VISUAL/$EDITOR for the /prompts manager's e key. It is
	// a seam so tests exercise the reload path without spawning vi.
	execEditor func(*exec.Cmd, tea.ExecCallback) tea.Cmd

	// repoMap is the harness-computed repository map for the current workdir,
	// scanned once at startup. It enters every session's system block as facts
	// only (paths, counts, commands, sizes) — never repository prose.
	repoMap repomap.Map

	// startedAt marks when the current session began (or was resumed). It
	// drives the exit card's duration fact and is reset by startNewSession.
	startedAt time.Time
	// tokensTotal is the cumulative provider token usage for the session,
	// accumulated wherever a.usage is assigned and seeded on resume by summing
	// usageFromMeta. It drives the exit card's token fact.
	tokensTotal int
	// bannerTip is the stable rotating tip shown on the banner's last row,
	// chosen once in New from the session id so a per-frame re-render cannot
	// strobe between tips.
	bannerTip string
	// bannerResumed/bannerRestoredTurns render the resumed-session banner
	// variant. Populated by the resume path and cleared by startNewSession.
	bannerResumed       string
	bannerRestoredTurns int

	// armed is the two-press key arm for quit (ctrl+d) and composer clear
	// (esc esc). A pressed key arms its kind for ~2s; the same key again (or
	// esc for quit) fires it, and any other key disarms.
	armed struct {
		kind  armKind
		until time.Time
	}
}

type tickMsg time.Time

func tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// armKind identifies which two-press key action is armed.
type armKind int

const (
	armNone armKind = iota
	armQuit
	armClear
)

// armWindow is how long a two-press arm stays live. A second press after the
// window lapses is treated as a fresh first press.
const armWindow = 2 * time.Second

// arm arms kind and stamps the expiry, refreshing the footer hint.
func (a *App) arm(k armKind) {
	a.armed.kind = k
	a.armed.until = time.Now().Add(armWindow)
	a.refreshArmed()
}

// disarm clears any armed key and its footer hint.
func (a *App) disarm() {
	a.armed.kind = armNone
	a.armed.until = time.Time{}
	a.refreshArmed()
}

// isArmed reports whether kind is armed and unexpired. An expired arm is
// disarmed in place.
func (a *App) isArmed(k armKind) bool {
	if a.armed.kind != k {
		return false
	}
	if time.Now().After(a.armed.until) {
		a.disarm()
		return false
	}
	return true
}

// armKeyMatches reports whether m is the trigger key for the currently armed
// kind, so the global "any other key disarms" rule never disarms a second
// press of the trigger itself.
func (a *App) armKeyMatches(m tea.KeyMsg) bool {
	switch a.armed.kind {
	case armQuit:
		return m.String() == "ctrl+d"
	case armClear:
		return m.String() == "esc"
	}
	return false
}

// refreshArmed renders the armed state into the footer's transient hint line.
func (a *App) refreshArmed() {
	switch a.armed.kind {
	case armQuit:
		a.footer.Armed = "press ctrl+d again to exit · esc cancels"
	case armClear:
		a.footer.Armed = "press esc again to clear the composer"
	default:
		a.footer.Armed = ""
	}
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
		client = httpclient.Default()
	}

	store, _ := session.NewStore()
	sessionKey, _ := session.KeyFor(workdir)
	cache, _ := rolemanager.LoadCache(rolemanager.DefaultCachePath())

	a := &App{
		registry:          NewRegistry(workdir),
		editor:            components.NewEditor(),
		footer:            components.Footer{Session: "new", Model: initial.Model},
		mode:              mode,
		modeSticky:        opts.PlanMode || mode == "plan",
		ctx:               context.Background(),
		cfg:               initial,
		status:            initialStatus,
		client:            client,
		resolver:          opts.Resolver,
		posture:           pol,
		planMode:          mode == "plan",
		cache:             cache,
		pending:           opts.Prompt,
		requestedProvider: name,
		workdir:           workdir,
		cwd:               workdir,
		settings:          eff.Settings,
		eff:               eff,
		flags:             flags,
		state:             st,
		vp:                viewport.New(80, 24),
		store:             store,
		sessionID:         session.MustID(),
		sessionKey:        sessionKey,
		sessionWorkdir:    workdir,
		attachments:       map[int]*attachment{},
		attachSpin:        spinner.New(),
		workSpin:          spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		follow:            true,
		autocompleteIndex: noAutocompleteSelection,
		agentIndex:        noAgentSelection,
		agentPickerOpen:   false,
		saveFileMsg:       -1,
		bannerW:           -1,
		footerW:           -1,
		agentPool:         agentpool.New(eff.Settings.Resilience.MaxAgentsOr(3)),
		activity:          activity.NewRegistry(),
		activityAnnounced: map[string]bool{},
		activityFinished:  map[string]bool{},
		subagentIdx:       map[string]int{},
		execEditor:        tea.ExecProcess,
		trace:             trace.Env(),
	}
	// One Live for the process: the running session and the next one share it.
	a.live = posture.NewLive(a.effectivePosture(), !a.askEnabled())
	// The banner tip is chosen once from the session id, so the per-frame
	// banner re-render is stable. startedAt seeds the exit card's duration.
	a.bannerTip = components.PickTip(a.sessionID)
	a.startedAt = time.Now()
	// The repo map is an accelerant, never a gate: claim, reuse, or scan it
	// best-effort. A ready map for this HEAD is reused; a running claim from
	// another process is waited on briefly; otherwise this process claims and
	// scans once, storing the result for the next process.
	if info, ok := gitinfo.Detect(workdir); ok {
		head := info.Head
		if head == "" {
			head = repoindex.RunProbe(context.Background(), info.Root, "git", "rev-parse", "--short", "HEAD")
		}
		if m, ok := projectregistry.WaitRepoMap(context.Background(), workdir, head, 0); ok {
			a.repoMap = m
		} else if claimed, _ := projectregistry.ClaimRepoMap(workdir, head, a.sessionID); claimed {
			a.repoMap = a.scanRepoMap(context.Background(), workdir)
			if a.repoMap.Head != "" {
				_ = projectregistry.StoreRepoMap(workdir, a.repoMap)
			} else {
				_ = projectregistry.MarkRepoMapFailed(workdir)
			}
		} else if m, ok := projectregistry.WaitRepoMap(context.Background(), workdir, head, 2*time.Second); ok {
			a.repoMap = m
		} else {
			a.repoMap = a.scanRepoMap(context.Background(), workdir)
		}
	}
	if initialStatus.Configured {
		a.SetClassifier(run.NewClassifier(initial, a.client))
		a.bgManager = bgagent.NewManager(workdir, initial, a.client, a.settings, a.effectivePosture())
		a.bgManager.SetPool(a.agentPool)
	}
	a.applyGitInfo(gitinfo.Detect(a.workdir))
	a.loadAgents()
	scanCmds := a.loadWorkspaceDirs()
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
	for _, note := range eff.Notes {
		a.addSystem(note)
	}

	if opts.Prompt != "" {
		a.messages = append(a.messages, components.Message{Role: "user", Content: opts.Prompt})
	}

	a.initCredentialState()
	a.refreshFooter()

	if opts.ResumeSession != "" {
		a.initCmd = a.resumeSession(opts.ResumeKey, opts.ResumeSession)
	}
	if len(scanCmds) > 0 {
		a.initCmd = tea.Batch(a.initCmd, tea.Batch(scanCmds...))
	}
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
	a.maybeNoticeLegacyPrompts()
	cmds := []tea.Cmd{tickCmd(), a.watchActivityEvents()}
	if a.initCmd != nil {
		cmds = append(cmds, a.initCmd)
	}
	// Credential resolution can probe the host keychain; run it off the first
	// frame so the TUI paints from the env-only resolution immediately.
	if a.resolver != nil {
		cmds = append(cmds, a.resolveCredentialsCmd(), a.probeAvailabilityCmd())
	} else if cmd := a.prefetchCatalogCmd(a.cfg.Provider); cmd != nil {
		// Without a resolver the env-only provider is final, so the footer's
		// context meter can start warming its catalogue now. With one, the
		// provider can still change, so the prefetch waits for
		// credentialsResolvedMsg.
		cmds = append(cmds, cmd)
	}
	if a.pending != "" && a.status.Configured {
		cmds = append(cmds, a.sendPending())
	}
	// Discover the Vulnetix CLI quietly on startup so the footer and
	// /vulnetix configure view have fresh capabilities from the first frame.
	cmds = append(cmds, a.probeVulnetixSilentCmd())
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
	a.bannerH = lipgloss.Height(a.bannerView())
	a.bannerW = a.width
	return a.bannerH
}

// bannerView renders the banner with the session's stable tip and, when set,
// the resumed-session variant.
func (a *App) bannerView() string {
	return components.Banner{
		Width:         a.width,
		Version:       version.Version,
		Commit:        version.Commit,
		Built:         version.BuildDate,
		Tip:           a.bannerTip,
		Resumed:       a.bannerResumed,
		RestoredTurns: a.bannerRestoredTurns,
	}.View()
}

// scanRepoMap runs the repo-map scan and registers it in the activity drawer
// as a silent internal job, so the honest register shows the scan like every
// other harness launch without round-tripping its (empty) output to the model.
func (a *App) scanRepoMap(ctx context.Context, workdir string) repomap.Map {
	var h *activity.Handle
	if a.activity != nil {
		h = a.activity.Add(activity.Activity{
			Kind:   activity.KindShell,
			Label:  "repo map scan",
			Argv:   []string{"repomap.Scan", workdir},
			Dir:    workdir,
			State:  activity.StateRunning,
			Silent: true,
		}, nil)
	}
	m := repomap.Scan(ctx, workdir)
	if h != nil {
		h.Finish(0, false, nil)
	}
	return m
}

// prependBanner prepends the rendered banner plus one blank separator row to
// the transcript body and its line map. Each prepended row becomes a Chrome
// provenance entry (never highlighted, contributes a blank to copies), and the
// prefix carries one newline per entry so the invariant
// len(lm) == strings.Count(body, "\n")+1 is preserved exactly.
func (a *App) prependBanner(body string, lm components.LineMap) (string, components.LineMap) {
	if !a.bannerVisible() {
		return body, lm
	}
	banner := a.bannerView()
	// banner has H rows (H-1 newlines); the blank separator row adds one more
	// newline, so the prefix carries H+1 newlines and H+1 chrome entries.
	nPrefix := strings.Count(banner, "\n") + 2
	body = banner + "\n\n" + body
	chrome := make(components.LineMap, nPrefix)
	for i := range chrome {
		chrome[i] = components.SourceLine{Chrome: true, Owner: -1}
	}
	return body, append(chrome, lm...)
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
	// Only the outer frame's top padding. The banner is the first entry of the
	// scrollable transcript now, so it does not contribute to the chrome
	// height and the viewport gains its rows back.
	return 1
}

// belowViewportHeight is the rows below the viewport — the outer frame's
// bottom padding, the autocomplete and attachment strips, the todo panel, the
// composer and the footer.
func (a *App) belowViewportHeight() int {
	h := 1 // bottom padding from the outer lipgloss frame
	if len(a.autocomplete) > 0 {
		h++
	}
	if a.agentPickerVisible() {
		h++
	}
	if a.promptPickerVisible() {
		h++
	}
	h += a.filePickHeight()
	h += a.attachStripHeight()
	h += a.todoPanelHeight()
	h += a.runsPanelHeight()
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
		// The initial async credential resolution may not have landed yet, or
		// a transient resolver/keychain failure left us unconfigured. Retry
		// synchronously once before failing so that valid credentials are
		// used as soon as a message is actually sent.
		a.reResolveCredentials()
	}
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
	in := agent.TurnInput{Prompt: promptText, ForceMode: a.forceMode, Mode: a.modeDecision}
	a.forceMode = ""
	if a.pendingPlanExecute {
		in.ExecutePlan = true
		in.PlanName = a.planExecuteName
		a.pendingPlanExecute = false
		a.planExecuteName = ""
	}
	in.PlanRevision = a.pendingPlanRevision
	a.pendingPlanRevision = 0
	// Only agent mode carries an agent: ForceAgent also forces the mode, so
	// sending an engaged agent from plan or goal mode would silently leave the
	// mode the user chose.
	if eng := a.engagedAgent(); eng != "" {
		in.ForceAgent = eng
	}
	if n := len(turns); n > 0 && turns[n-1].Role == "user" {
		in.Attachments = turns[n-1].Attachments
		in.Directive = turns[n-1].Directive
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
	return tea.Batch(func() tea.Msg {
		sess, err := buildAgentSession(params)
		return agentReadyMsg{sess: sess, history: history, in: in, err: err}
	}, a.workSpin.Tick)
}

// startAgent begins the streaming turn on an already-built session.
func (a *App) startAgent(sess *agent.Session, history []run.Turn, in agent.TurnInput) tea.Cmd {
	a.events = sess.RunStream(a.ctx, history, in)
	return tea.Batch(a.nextAgent(), a.workSpin.Tick)
}

// consumeDirective returns any pending directive and clears it. If no
// pending directive exists, it returns the attachment-derived directive.
func (a *App) consumeDirective(fallback string) string {
	if a.pendingDirective != "" {
		d := a.pendingDirective
		a.pendingDirective = ""
		return d
	}
	return fallback
}

// submitInput finalises one user prompt, including any SAFE attachments, and
// starts the agent turn. The prompt is echoed to the transcript the instant
// Enter is pressed — before mode classification and any provider I/O — and
// the composer shows the Role Manager indicator while the pre-prompt
// classifier runs.
func (a *App) submitInput(input string) tea.Cmd {
	previews, directive := a.attachmentPreviews()
	directive = a.consumeDirective(directive)
	a.messages = append(a.messages, previews...)

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
	a.clearLoadedPrompt()
	a.clearAutocomplete()

	firstUser := !a.hasUserMessage()

	// Instant echo: the prompt becomes a "User prompt" in the transcript and
	// a session entry before any classification or provider I/O.
	a.echoUser(input)
	a.setPhaseRoleManager(agent.RoleManagerPhasePrePrompt)

	if a.modeSticky || a.modeExplicit || a.classifier == nil {
		a.forceMode = modes.Mode(a.mode)
		a.modeExplicit = false
		return a.sendTurn(firstUser, input, safe, directive)
	}
	a.modeExplicit = false
	a.preSend = true
	return tea.Batch(a.classifyAndSend(input, safe, directive, firstUser), a.workSpin.Tick)
}

// echoUser appends a submitted prompt to the transcript and persists it as a
// user entry.
func (a *App) echoUser(input string) {
	a.messages = append(a.messages, components.Message{Role: "user", Content: input})
	a.appendEntry(session.Entry{Type: "user", Role: "user", Content: input})
	// The user turn is already on disk; advance the persistence cursor past
	// it so persistTail never double-writes it.
	a.persistedUpTo = len(a.messages)
}

// sendTurnNoEcho starts the agent turn for a prompt already echoed to the
// transcript. The echoed prompt is the transcript's last user message, so the
// validated attachments are folded into it rather than appending a duplicate
// turn.
func (a *App) sendTurnNoEcho(input string, atts []run.Attachment, directive string) tea.Cmd {
	turns := a.buildTurns()
	if n := len(turns); n > 0 && turns[n-1].Role == "user" {
		turns[n-1].Attachments = atts
		turns[n-1].Directive = directive
		return a.send(turns)
	}
	turns = append(turns, run.Turn{Role: "user", Content: input, Attachments: atts, Directive: directive})
	return a.send(turns)
}

// sendTurn starts the agent turn and, for the session's first prompt, also
// kicks off the async session-naming call.
func (a *App) sendTurn(firstUser bool, input string, atts []run.Attachment, directive string) tea.Cmd {
	a.preSend = false
	cmd := a.sendTurnNoEcho(input, atts, directive)
	if firstUser && a.shouldAutoName() {
		a.nameRequested = true
		return tea.Batch(cmd, a.nameSessionCmd(input))
	}
	return cmd
}

// classifyAndSend runs the mode classifier in a goroutine and sends the turn
// when the decision lands. By the time this command starts, the prompt is
// already echoed and the Role Manager indicator is up.
func (a *App) classifyAndSend(input string, atts []run.Attachment, directive string, firstUser bool) tea.Cmd {
	c := a.classifier
	ctx := a.ctx
	return func() tea.Msg {
		d, err := rolemanager.Select(ctx, c, rolemanager.ModeInput{
			Prompt:        input,
			GoalLimit:     rolemanager.DefaultGoalPromptLengthLimit,
			HasReferences: len(atts) > 0,
		})
		return modeClassifiedMsg{input: input, atts: atts, directive: directive, firstUser: firstUser, decision: d, err: err}
	}
}

// handleModeClassified applies the mode decision and starts the agent turn.
// A prompt cancelled with esc during classification is dropped.
func (a *App) handleModeClassified(m modeClassifiedMsg) tea.Cmd {
	if !a.preSend {
		return nil
	}
	a.applyModeDecision(m.decision, m.err)
	return a.sendTurn(m.firstUser, m.input, m.atts, m.directive)
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

// setPhaseExploring marks the explore fan-out phase with its progress counter
// and the reference currently being explored. It calls startPhase so the
// elapsed clock still counts from Enter.
func (a *App) setPhaseExploring(done, total int, ref string) {
	a.startPhase()
	a.phase = phaseExploring
	a.exploreDone = done
	a.exploreTotal = total
	a.exploreRef = ref
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
// text, reasoning, or tool-progress deltas are concatenated into a single
// event so Bubble Tea updates — and therefore full transcript re-renders —
// happen once per drain, not once per streamed token or output line. The loop
// stops at the first event that cannot join the run (which is stashed as a
// lookahead and replayed next) or at an empty channel, so nothing is dropped
// and ordering is preserved.
func (a *App) nextAgent() tea.Cmd {
	return func() tea.Msg {
		e, ok := a.nextEvent()
		if !ok {
			return agentEventMsg{Kind: agent.EventDoneKind}
		}
		if !coalescable(e.Kind) {
			return agentEventMsg(e)
		}

		var b strings.Builder
		b.WriteString(eventDelta(e))
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
			if !joinsRun(e, next) {
				a.pendingEvent = next
				a.pendingEventSet = true
				return agentEventMsg(coalesced(e, b.String()))
			}
			if e.Kind == agent.EventToolProgressKind {
				// Progress deltas are whole lines; text deltas are mid-word.
				b.WriteString("\n")
			}
			b.WriteString(eventDelta(next))
		}
	}
}

// coalescable reports whether a run of events of this kind can be folded into
// one.
func coalescable(k agent.EventKind) bool {
	switch k {
	case agent.EventTextKind, agent.EventReasoningKind, agent.EventToolProgressKind:
		return true
	}
	return false
}

// joinsRun reports whether next can be folded into the run started by first.
// Progress additionally has to be for the same tool call: two tools running
// concurrently would otherwise have their output spliced into one row.
func joinsRun(first, next agent.Event) bool {
	if next.Kind != first.Kind {
		return false
	}
	if first.Kind == agent.EventToolProgressKind {
		return next.ToolCallID == first.ToolCallID
	}
	return true
}

// eventDelta is the accumulating payload for a coalescable event.
func eventDelta(e agent.Event) string {
	switch e.Kind {
	case agent.EventTextKind:
		return e.Text
	case agent.EventReasoningKind:
		return e.Reasoning
	case agent.EventToolProgressKind:
		return e.ToolProgress
	}
	return ""
}

// coalesced folds a run of deltas accumulated in acc back into the first
// event's payload field.
func coalesced(first agent.Event, acc string) agent.Event {
	switch first.Kind {
	case agent.EventTextKind:
		first.Text = acc
	case agent.EventToolProgressKind:
		first.ToolProgress = acc
	default:
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
	workdir  string
	settings config.Settings
	cfg      run.Config
	client   *http.Client
	// live is the shared posture/ask holder the session consults at each gate.
	// It is not snapshotted: the running session and the next one share it, so
	// a toggle pressed mid-turn lands on the next gate check.
	live *posture.Live
	// guardrails is the operator's current switch, so the plan-mode surface is
	// built from the effective value rather than the raw persisted setting.
	guardrails bool
	// posture is the effective policy for this session, computed as the
	// strictest policy across the primary workdir and any added workspace
	// directories.
	posture      posture.Policy
	planMode     bool
	allowClarify bool
	// profile is the engaged agent definition, if any. Its provider/model/
	// effort overrides are applied to cfg before the session is built.
	profile agentprofile.AgentProfile
	// toolAllow restricts the registry to an engaged background definition's
	// tools. Empty means every registered tool.
	toolAllow []string
	// agentPool is the shared FIFO fan-out ceiling the session's explore
	// subagents acquire a lease from.
	agentPool *agentpool.Pool
	// repoMap is the harness-computed repository map handed to the session's
	// system block.
	repoMap repomap.Map
	// workspaceDirs are additional directories added to the session with
	// /add-dir; they widen the tool confinement boundary.
	workspaceDirs []string
	// workspaceMaps hold the harness-computed repo maps for workspaceDirs.
	workspaceMaps []repomap.Map
}

func (a *App) sessionBuildParams() sessionBuildParams {
	p, _ := a.engagedProfile()
	pol := a.posture
	if a.guardrailsEnabled() {
		merged, err := posture.ForDirs(a.workdir, a.workspaceDirs)
		if err == nil {
			pol = merged
		}
	} else {
		pol = posture.AllIgnore()
	}
	return sessionBuildParams{
		workdir:       a.workdir,
		settings:      a.settings,
		cfg:           a.cfg,
		client:        a.client,
		live:          a.live,
		guardrails:    a.guardrailsEnabled(),
		posture:       pol,
		planMode:      a.planMode,
		allowClarify:  true,
		profile:       p,
		toolAllow:     a.engagedAgentTools(),
		agentPool:     a.agentPool,
		repoMap:       a.repoMap,
		workspaceDirs: a.workspaceDirs,
		workspaceMaps: a.workspaceMaps,
	}
}

// buildAgentSession constructs a top-level agent session from a snapshot. It
// is the pure construction half of agentSession, safe to run off the Bubble
// Tea goroutine.
func buildAgentSession(p sessionBuildParams) (*agent.Session, error) {
	caps := tools.DetectDefault()
	ix := repoindex.Scan(context.Background(), p.workdir)
	reg := tools.DefaultWithCaps(p.workdir, p.settings.ReadOnlyEnabled(), caps, ix)
	if len(p.toolAllow) > 0 {
		// An engaged background definition brings its allowlist with it, the
		// same narrowing internal/bgagent applies when it runs the definition
		// on its own. Only keeps the shared working-directory tracker, so a
		// Cd in an allowlisted session still reaches the footer.
		reg = reg.Only(p.toolAllow...)
	}
	perms := permissions.From(p.settings.Permissions.Allow, p.settings.Permissions.Ask, p.settings.Permissions.Deny)
	var promptOpts prompt.Options
	if p.settings.Caveman != nil && *p.settings.Caveman {
		promptOpts.Caveman = true
	}
	cfg := p.cfg
	if p.profile.Provider != "" {
		cfg.Provider = p.profile.Provider
	}
	if p.profile.Model != "" {
		cfg.Model = p.profile.Model
	} else if p.profile.Provider != "" && p.cfg.Provider != p.profile.Provider {
		cfg.Model = run.DefaultModel(p.profile.Provider)
	}
	if p.profile.Effort != "" {
		cfg.Effort = p.profile.Effort
	}
	return agent.NewSession(agent.Options{
		Cfg:           cfg,
		Client:        p.client,
		Registry:      reg,
		Perms:         perms,
		Live:          p.live,
		PlanMode:      p.planMode,
		Workdir:       p.workdir,
		Settings:      p.settings,
		PromptOptions: promptOpts,
		Caps:          caps,
		RepoIndex:     ix,
		PlanSurface:   tools.PlanSurface{GuardrailsOff: !p.guardrails, Perms: perms},
		// Top-level session: explore subagents may fan out from here. A
		// subagent sets this false so it can never fan out again.
		AllowExplore: true,
		// Top-level TUI session: the user is present, so the interactive
		// clarification loop may run. Subagents and the non-interactive CLI
		// leave this false.
		AllowClarify: true,
		// Top-level TUI session: mutating tool calls ask the user through the
		// approval view before touching disk. Never inherited by subagents.
		AllowAsk: true,
		// Top-level goal-mode prompts may run the unbounded pass loop; a
		// subagent never does.
		AllowPassLoop: true,
		// Explore fan-out is capped by the shared FIFO pool the TUI owns.
		AgentPool:     p.agentPool,
		RepoMap:       &p.repoMap,
		WorkspaceMaps: p.workspaceMaps,
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
//
// The next session is built with a fresh working-directory tracker rooted at
// the workdir, so the displayed directory returns there too. Leaving the
// footer pointing at the old directory would be a lie the very next tool call
// would expose.
func (a *App) invalidateAgentSession() {
	a.agent = nil
	if a.cwd != a.workdir {
		a.cwd = a.workdir
		a.footerW = -1
		a.refreshFooter()
	}
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
		if a.armed.kind != armNone && time.Now().After(a.armed.until) {
			a.disarm()
		}
		a.refreshFooter()
		return a, tea.Batch(tickCmd(), a.refreshGitInfoCmd())

	case streamChunkMsg:
		return a, a.handleStreamChunk(m)

	case agentEventMsg:
		return a, a.handleAgentEvent(m)

	case activityEventMsg:
		return a, a.handleActivityEvent(m)

	case agentReadyMsg:
		return a, a.handleAgentReady(m)

	case credentialsResolvedMsg:
		return a, a.handleCredentialsResolved(m)

	case availabilityMsg:
		return a, a.handleAvailability(m)

	case filesLoadedMsg:
		return a, a.handleFilesLoaded(m)

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

	case planEditedMsg:
		return a, a.handlePlanEdited(m)

	case localModelReportMsg:
		if a.view == viewProviderDetail {
			a.providerDetailState.localReport = m.text
			a.providerDetailState.localReportPending = false
		} else {
			a.addSystem(m.text)
		}
		return a, nil

	case promptEditedMsg:
		return a, a.handlePromptEdited(m)

	case vulnetixDoneMsg:
		return a, a.handleVulnetixDone(m)

	case vulnetixProbeMsg:
		return a, a.handleVulnetixProbe(m)

	case projectsLoadedMsg:
		return a, a.handleProjectsLoaded(m)

	case sweepFoundMsg:
		return a, a.handleSweepFound(m)

	case artifactsLoadedMsg:
		return a, a.handleArtifactsLoaded(m)

	case dirPickLoadedMsg:
		a.handleDirPickLoaded(m)
		return a, nil

	case workspaceDirAddedMsg:
		return a, a.handleWorkspaceDirAdded(m)

	case workspaceMapReadyMsg:
		if m.err == nil && m.m.Head != "" {
			a.workspaceMaps = append(a.workspaceMaps, m.m)
		}
		return a, nil

	case agentBuilderDoneMsg:
		return a, a.handleAgentBuilderDone(m)

	case agentFieldEditedMsg:
		return a, a.handleAgentFieldEdited(m)

	case bgAgentEventMsg:
		return a, a.handleBgAgentEvent(m)

	case catalogTargetMsg:
		return a, a.handleCatalogTarget(m)

	case modelsFetchedMsg:
		return a, a.handleModelsFetched(m)

	case sessionsScannedMsg:
		return a, a.handleSessionsScanned(m)

	case attachValidatedMsg:
		return a, a.handleAttachValidated(m)

	case shellProgressMsg:
		return a, a.handleShellProgress(m)

	case shellDoneMsg:
		return a, a.handleShellDone(m)

	case modeClassifiedMsg:
		return a, a.handleModeClassified(m)

	case tea.MouseMsg:
		// Record the pointer for hover hinting regardless of what the event
		// does: the transcript re-derives the hover target from this position
		// every frame, and a non-chat view has no footer or panels to hint at.
		a.mousePresent = a.view == viewChat
		a.mouseX, a.mouseY = m.X, m.Y
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
		} else if a.view == viewPlanReview && tea.MouseEvent(m).IsWheel() {
			a.planReview.vp, vpCmd = a.planReview.vp.Update(m)
		}
		cmd := a.editor.Update(m)
		a.refreshAutocomplete()
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
		// Any key other than the trigger disarms a two-press arm, so a stray
		// keypress never leaves a latent "press again" hanging over the next
		// keystroke.
		if a.armed.kind != armNone && !a.armKeyMatches(m) {
			a.disarm()
		}
		// Global keys work on every screen.
		switch m.String() {
		case "ctrl+c":
			// Context-sensitive: over a hovered file panel, ctrl+c copies the
			// file's content; everywhere else it keeps copying the prompt.
			if a.view == viewChat && a.hover.text {
				return a, a.copyHoveredPanel(a.hover.msg)
			}
			return a, a.copyPrompt()
		case "ctrl+d":
			// Two-press armed exit. The first press arms; the second (within
			// the window) tears down and quits, printing the exit card.
			if a.isArmed(armQuit) {
				a.disarm()
				a.stopLocalServers()
				return a, tea.Quit
			}
			a.arm(armQuit)
			return a, nil
		case "ctrl+r":
			a.reasoningOverride = nextBoolPtr(a.reasoningOverride)
			a.addSystem("reasoning display: " + boolLabel(a.reasoningVisible()))
			return a, nil
		case "ctrl+t":
			a.toolCallsOverride = nextBoolPtr(a.toolCallsOverride)
			a.addSystem("tool-call display: " + boolLabel(a.toolCallsVisible()))
			return a, nil
		// The four session toggles sit on the function-key row rather than on
		// ctrl+<letter>. Every free ctrl+<letter> is already spoken for by the
		// prompt editor (ctrl+a/e/k/u/w/n/p/v and friends), and ctrl+alt+<key>
		// cannot be used at all: under the kitty keyboard protocol Signet
		// pushes, a ctrl+<letter> event collapses to a legacy control code that
		// carries no alt bit, so those chords never reached this switch.
		case "f5":
			if a.view == viewPlanReview {
				return a, nil
			}
			a.cycleMode()
			a.syncPlanMode()
			return a, nil
		case "f2":
			if a.view == viewPlanReview {
				return a, nil
			}
			return a, a.toggleCaveman()
		case "f3":
			if a.view == viewPlanReview {
				return a, nil
			}
			return a, a.toggleGuardrails()
		case "f4":
			if a.view == viewPlanReview {
				return a, nil
			}
			return a, a.toggleAsk()
		case "f6":
			if a.view == viewPlanReview {
				return a, nil
			}
			return a, a.cycleEffort()
		case "f8":
			// Open the runs panel on the subagents tab.
			if a.view == viewChat {
				a.toggleRunsPanel(tabSubagents)
			}
			return a, nil
		case "f9":
			// Toggle the runs panel on the activity tab.
			if a.view == viewChat {
				a.toggleRunsPanel(tabActivity)
			}
			return a, nil
		case "f10":
			// Toggle the Vulnetix AI Firewall when the CLI is configured. This
			// is intentionally global: it works from /vulnetix config too.
			if a.view == viewPlanReview {
				return a, nil
			}
			return a, a.toggleFirewall()
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
	a.refreshAutocomplete()
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
	// The ctrl+s action bar swallows every key it does not handle, so y/n/d
	// can never reach the editor while a destructive confirm is on screen.
	if a.promptAction {
		return a.handlePromptActionKey(m)
	}
	if a.saveFileMode {
		return a.handleSaveFileKey(m)
	}
	if a.savePromptMode {
		return a.handleSavePromptKey(m)
	}
	if a.historyActive {
		return a.handleHistoryKey(m)
	}

	// The runs panel owns the composer's keys while focused.
	if a.runsFocus {
		return a.handleRunsPanelKey(m)
	}

	if a.filePickerVisible() {
		if cmd, handled := a.handleFilePickKey(m); handled {
			return cmd
		}
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
	case "ctrl+s":
		// Hover keeps priority: it is the older, narrower binding and needs a
		// live mouse position. Then a loaded library entry gets the action
		// bar; otherwise ctrl+s is save-as, the same shape as f7.
		if a.hover.text {
			return a.startSaveFile(a.hover.msg)
		}
		if a.loadedPrompt != nil {
			return a.openPromptAction()
		}
		return a.startSavePrompt()
	case "ctrl+x":
		// Copy the full session id. The hint is shown on the footer's session
		// segment, but the key works from the chat view without a hover too.
		return a.copySessionID()
	case "esc":
		// First step of the cascade: esc is the explicit cancel of a two-press
		// quit arm. The composer-clear arm is the last step, so its second esc
		// still reaches the clear below.
		if a.isArmed(armQuit) {
			a.disarm()
			return nil
		}
		// A live selection is cleared first, ahead of the existing esc
		// behaviour: the first esc dismisses the highlight, the second does
		// whatever esc would have done (cancel the request, drop pre-send…).
		if a.sel.active || a.sel.dragging {
			a.sel = selection{}
			return nil
		}
		// A highlighted completion is dropped before esc reaches the request:
		// the popup is the thing the user is looking at. The agent picker is
		// closed entirely by esc.
		if _, ok := a.autocompleteSelection(); ok {
			a.autocompleteIndex = noAutocompleteSelection
			return nil
		}
		if a.agentPickerOpen {
			a.closeAgentArgPicker()
			a.agentPickerOpen = false
			a.agentPickerSubmit = false
			a.agentIndex = noAgentSelection
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
			return nil
		}
		// Last step of the cascade: composer clear is two-press armed. Every
		// earlier esc step (selection → completion → picker → pending input →
		// pre-send → cancel request) has already taken precedence, so a draft
		// is only touched once nothing else is waiting.
		if strings.TrimSpace(a.editor.Value()) != "" {
			if a.isArmed(armClear) {
				a.disarm()
				a.editor.Reset()
				a.clearAutocomplete()
			} else {
				a.arm(armClear)
			}
		}
		return nil
	case "enter":
		// A highlighted completion is a selection, not a submission: enter
		// puts it in the prompt and a second enter sends it. A highlighted
		// agent is the same bargain — it engages the profile, it does not
		// send the turn.
		if _, ok := a.autocompleteSelection(); ok {
			return a.acceptAutocomplete()
		}
		if a.agentPickerVisible() {
			if _, ok := a.agentSelection(); ok {
				return a.acceptAgent()
			}
		}
		input := strings.TrimSpace(a.editor.Value())
		// A slash command and a `!cmd` are local acts, not model turns: they
		// need no carrier, so they run on this enter rather than falling into
		// the agent picker below and costing the user a second press.
		if isShellInput(input) {
			a.editor.Reset()
			a.clearLoadedPrompt()
			a.clearAutocomplete()
			return a.handleShell(input)
		}
		if strings.HasPrefix(input, "/") {
			a.editor.Reset()
			a.clearLoadedPrompt()
			a.clearAutocomplete()
			return a.handleCommand(input)
		}
		// In agent mode with no agent engaged, enter opens the picker rather
		// than sending a turn that has no carrier. The submit is deferred, not
		// dropped: acceptAgent finishes it once a carrier exists.
		if a.mode == "agent" && a.namedAgent == "" && !a.agentPickerOpen {
			a.openAgentPicker()
			a.agentPickerSubmit = input != ""
			a.relayout()
			return nil
		}
		if input == "" {
			return nil
		}
		if a.working() {
			a.messages = append(a.messages, components.Message{Role: "user", Content: input, Steering: true})
			if a.agent == nil || !a.agent.Steer(input) {
				a.addSystem("steering queue full — message dropped")
			}
			a.editor.Reset()
			a.clearLoadedPrompt()
			a.clearAutocomplete()
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
	case "f7":
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

	// The agent picker takes the same three keys, but only tab unprompted:
	// right and enter still belong to the editor until a candidate is
	// actually highlighted.
	if a.agentPickerVisible() {
		switch m.String() {
		case "tab":
			return a.cycleAgent()
		case "right":
			if _, ok := a.agentSelection(); ok {
				return a.acceptAgent()
			}
		case "ctrl+g":
			// Starting a background agent is a different act from engaging
			// one, so it gets its own key rather than overloading enter.
			if c, ok := a.agentSelection(); ok && c.Name != agentNoneLabel {
				return a.startAgentChoice(c)
			}
		}
	}

	return a.forwardToEditor(m)
}

// forwardToEditor hands a key to the composer and refreshes the state that
// depends on its contents.
func (a *App) forwardToEditor(m tea.KeyMsg) tea.Cmd {
	cmd := a.editor.Update(m)
	// The agent picker is explicit state now, so its highlight persists while
	// the user types; the file chooser is re-filtered on every keystroke, so
	// its highlight and dismissed token reset.
	if !a.agentPickerOpen {
		a.agentIndex = noAgentSelection
	}
	a.fileIndex = noFileSelection
	a.fileScroll = 0
	a.fileDismissed = ""
	a.refreshAutocomplete()
	a.relayout()
	// Lazily load the workspace listing the first time the file chooser could
	// appear. Returning the command alongside the editor update lets the
	// current keystroke take effect while the listing fills in the background.
	if loadCmd := a.fileListIfStale(); loadCmd != nil {
		return tea.Batch(cmd, loadCmd)
	}
	return cmd
}

// ---------------------------------------------------------------------------
// Prompt history / library cycling
// ---------------------------------------------------------------------------

// historyItem is one browsable prompt. Name is the library name when the
// prompt came from the prompt library, and empty for a plain session-history
// prompt, which nobody named. Entry is the library file the prompt came from,
// nil for plain session history.
type historyItem struct {
	Name   string
	Prompt string
	Entry  *promptlib.Entry
}

func (a *App) startHistoryCycle() tea.Cmd {
	a.historyQuery = strings.TrimSpace(a.editor.Value())
	a.historyOriginal = a.editor.Value()
	a.historyResults = a.buildHistoryResults(a.historyQuery)
	a.historyActive = true
	if len(a.historyResults) > 0 {
		a.setHistoryResult(0)
	} else {
		a.historyIndex = -1
		a.editor.SetValue(a.historyQuery)
		a.loadedPrompt = nil
	}
	a.clearAutocomplete()
	return nil
}

// setHistoryResult loads one browse result into the composer and records the
// library file it came from, so ctrl+s overwrites the right entry.
func (a *App) setHistoryResult(i int) {
	a.historyIndex = i
	a.editor.SetValue(a.historyResults[i].Prompt)
	a.editor.CursorEnd()
	a.loadedPrompt = a.historyResults[i].Entry
}

func (a *App) buildHistoryResults(query string) []historyItem {
	var results []historyItem
	seen := make(map[string]bool)

	globalLib, _ := promptlib.Load(config.ScopeGlobal, "")
	projLib, _ := promptlib.Load(config.ScopeProject, a.workdir)
	merged := promptlib.Merge(globalLib.Entries, projLib.Entries)
	for _, e := range promptlib.Filter(promptlib.Enabled(merged), query) {
		if !seen[e.Prompt] {
			seen[e.Prompt] = true
			entry := e
			results = append(results, historyItem{Name: e.Name, Prompt: e.Prompt, Entry: &entry})
		}
	}

	if a.store != nil {
		prompts, _ := a.store.UserPrompts(a.workdir)
		for _, p := range prompts {
			if !seen[p] && promptlib.Match(promptlib.Entry{Name: "", Prompt: p}, query) {
				seen[p] = true
				results = append(results, historyItem{Prompt: p})
			}
		}
	}

	return results
}

// namedHistoryCount is the number of leading results that carry a library
// name. buildHistoryResults puts every library entry first, so the named
// prompts are always the prefix of the list and the strip can index into it
// directly.
func (a *App) namedHistoryCount() int {
	n := 0
	for _, it := range a.historyResults {
		if it.Name == "" {
			break
		}
		n++
	}
	return n
}

// promptPickerVisible reports whether the named-prompt strip has anything to
// draw: the browse cycle is open and the library contributed at least one
// entry to it.
func (a *App) promptPickerVisible() bool {
	return a.historyActive && a.namedHistoryCount() > 0
}

// cyclePromptName moves the highlight to the next named prompt, wrapping at
// the end. It is deliberately confined to the named prefix: tab picks a
// library entry by name, while up/down still walk the whole list including
// the unnamed session history.
func (a *App) cyclePromptName() tea.Cmd {
	named := a.namedHistoryCount()
	if named == 0 {
		return nil
	}
	next := a.historyIndex + 1
	if next < 0 || next >= named {
		next = 0
	}
	a.historyIndex = next
	a.setHistoryResult(next)
	return nil
}

// renderPromptPicker draws the library names as a chip row above the composer,
// the same shape as the agent picker. It is what the browse cycle offers in
// place of an invisible search: the names are the thing worth reading, and the
// prompt text is already in the composer.
func (a *App) renderPromptPicker() string {
	named := a.namedHistoryCount()
	parts := make([]string, 0, named)
	for i := range named {
		name := a.historyResults[i].Name
		if i == a.historyIndex {
			parts = append(parts, components.Chip(name, components.ColorTealSoft))
			continue
		}
		parts = append(parts, components.KeyStyle.Render(name))
	}
	line := components.MutedStyle.Render("✎ ") + strings.Join(parts, components.MutedStyle.Render("  ·  "))
	return lipgloss.NewStyle().MaxWidth(a.contentWidth()).Render(line)
}

func (a *App) handleHistoryKey(m tea.KeyMsg) tea.Cmd {
	switch m.String() {
	case "up":
		if a.historyIndex < len(a.historyResults)-1 {
			a.setHistoryResult(a.historyIndex + 1)
		}
		return nil
	case "down":
		if a.historyIndex > 0 {
			a.setHistoryResult(a.historyIndex - 1)
		} else {
			a.exitHistoryCycle(false)
		}
		return nil
	case "tab":
		return a.cyclePromptName()
	case "right":
		// Accept the loaded prompt into the composer and leave the cycle, so
		// the next keystroke edits it instead of browsing away from it.
		a.exitHistoryCycle(true)
		a.editor.CursorEnd()
		return nil
	case "enter":
		a.exitHistoryCycle(true)
		input := strings.TrimSpace(a.editor.Value())
		if input == "" {
			return nil
		}
		if isShellInput(input) {
			a.editor.Reset()
			a.clearLoadedPrompt()
			a.clearAutocomplete()
			return a.handleShell(input)
		}
		if strings.HasPrefix(input, "/") {
			a.editor.Reset()
			a.clearLoadedPrompt()
			a.clearAutocomplete()
			return a.handleCommand(input)
		}
		if a.working() {
			a.messages = append(a.messages, components.Message{Role: "user", Content: input, Steering: true})
			if a.agent == nil || !a.agent.Steer(input) {
				a.addSystem("steering queue full — message dropped")
			}
			a.editor.Reset()
			a.clearLoadedPrompt()
			a.clearAutocomplete()
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
	case "esc":
		a.exitHistoryCycle(false)
		return nil
	}

	// Any other key — a rune, backspace, cursor motion, delete — means the user
	// is done browsing and wants to edit the prompt that was loaded. Leave the
	// cycle with the loaded text intact and hand the key to the editor so it
	// performs its normal edit instead of clearing the composer. Typing used to
	// re-filter the results invisibly, which read as the composer eating
	// keystrokes; the named-prompt strip replaces that with tab.
	a.exitHistoryCycle(true)
	return a.forwardToEditor(m)
}

// exitHistoryCycle leaves the browse cycle. accept keeps whatever prompt is
// loaded in the composer; otherwise the text from before the cycle is restored.
func (a *App) exitHistoryCycle(accept bool) {
	a.historyActive = false
	if !accept {
		a.editor.SetValue(a.historyOriginal)
		a.clearLoadedPrompt()
	}
	a.historyResults = nil
	a.historyIndex = 0
	a.historyQuery = ""
	a.historyOriginal = ""
	a.refreshAutocomplete()
}

// ---------------------------------------------------------------------------
// Autocomplete cycling
// ---------------------------------------------------------------------------

// noAutocompleteSelection is the autocompleteIndex value meaning "no candidate
// is highlighted": the popup is showing, but the prompt is still whatever the
// user typed.
const noAutocompleteSelection = -1

// cycleAutocomplete moves the highlight to the next candidate. It deliberately
// leaves the prompt alone: writing the candidate into the editor would narrow
// the candidate list to that one command on the next refresh, which pinned the
// cycle to a single entry. Enter (or right) is what commits the highlight.
func (a *App) cycleAutocomplete() tea.Cmd {
	if len(a.autocomplete) == 0 {
		return nil
	}
	a.autocompleteIndex++
	if a.autocompleteIndex >= len(a.autocomplete) {
		a.autocompleteIndex = 0
	}
	return nil
}

// acceptAutocomplete writes the highlighted candidate — or the first one, when
// nothing is highlighted yet — into the prompt and dismisses the popup.
func (a *App) acceptAutocomplete() tea.Cmd {
	choice, ok := a.autocompleteSelection()
	if !ok {
		if len(a.autocomplete) == 0 {
			return nil
		}
		choice = a.autocomplete[0]
	}
	a.editor.SetValue(choice)
	a.editor.CursorEnd()
	a.clearAutocomplete()
	a.relayout()
	return nil
}

// autocompleteSelection returns the highlighted candidate, if there is one.
func (a *App) autocompleteSelection() (string, bool) {
	if a.autocompleteIndex < 0 || a.autocompleteIndex >= len(a.autocomplete) {
		return "", false
	}
	return a.autocomplete[a.autocompleteIndex], true
}

// refreshAutocomplete recomputes the candidates for the current prompt text.
// The highlight survives only while the candidate list is unchanged, so a
// stale index can never point at a different command than the chip row showed.
func (a *App) refreshAutocomplete() {
	next := a.registry.Complete(a.editor.Value())
	if !slices.Equal(next, a.autocomplete) {
		a.autocompleteIndex = noAutocompleteSelection
	}
	a.autocomplete = next
	// The chips stop at the subcommand; the agent picker takes over for the
	// <name> that follows it. The two never overlap: Complete returns nothing
	// once a second word is being typed.
	a.refreshAgentArgPicker()
}

// clearAutocomplete dismisses the popup and drops the highlight.
func (a *App) clearAutocomplete() {
	a.autocomplete = nil
	a.autocompleteIndex = noAutocompleteSelection
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
	a.savePromptConfirm = false
	a.savePromptName = ""
	a.clearLoadedPrompt()
	a.editor.Reset()
	a.clearAutocomplete()
	return nil
}

func (a *App) handleSavePromptKey(m tea.KeyMsg) tea.Cmd {
	if a.savePromptConfirm {
		switch m.String() {
		case "y":
			return a.confirmSavePromptOverwrite()
		case "n", "esc":
			a.cancelSavePrompt()
		}
		return nil
	}
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
	e, err := promptlib.Create(config.ScopeProject, a.workdir, name, a.savePromptValue)
	if errors.Is(err, promptlib.ErrNameExists) {
		// Route into the confirm gate rather than silently replacing.
		slug, serr := promptlib.Slug(name)
		if serr != nil {
			a.addSystem("save failed: " + serr.Error())
			a.cancelSavePrompt()
			return nil
		}
		a.savePromptConfirm = true
		a.savePromptName = slug
		return nil
	}
	if err != nil {
		a.addSystem("save failed: " + err.Error())
	} else {
		a.addSystem("saved prompt to project library: " + e.Name)
	}
	a.cancelSavePrompt()
	return nil
}

// confirmSavePromptOverwrite overwrites the entry the user just named rather
// than silently replacing it. If the entry vanished between confirm and save
// (two instances), it falls back to creating it.
func (a *App) confirmSavePromptOverwrite() tea.Cmd {
	listing, err := promptlib.Load(config.ScopeProject, a.workdir)
	if err != nil {
		a.addSystem("save failed: " + err.Error())
		a.cancelSavePrompt()
		return nil
	}
	for i := range listing.Entries {
		if listing.Entries[i].Name == a.savePromptName {
			if _, err := promptlib.Update(listing.Entries[i], a.savePromptValue); err != nil {
				a.addSystem("save failed: " + err.Error())
			} else {
				a.addSystem("saved prompt to project library: " + a.savePromptName)
			}
			a.cancelSavePrompt()
			return nil
		}
	}
	if _, err := promptlib.Create(config.ScopeProject, a.workdir, a.savePromptName, a.savePromptValue); err != nil {
		a.addSystem("save failed: " + err.Error())
	} else {
		a.addSystem("saved prompt to project library: " + a.savePromptName)
	}
	a.cancelSavePrompt()
	return nil
}

func (a *App) cancelSavePrompt() {
	a.savePromptMode = false
	a.savePromptValue = ""
	a.savePromptConfirm = false
	a.savePromptName = ""
	a.editor.Reset()
}

// clearLoadedPrompt drops the loaded library entry and closes the action bar.
// The composer stops representing the entry the moment it is submitted,
// steered, routed to a shell/command, cancelled, or a view resets the editor.
func (a *App) clearLoadedPrompt() {
	a.loadedPrompt = nil
	a.promptAction = false
	a.promptConfirm = ""
}

// openPromptAction opens the ctrl+s action bar for the loaded entry, after a
// belt-and-braces re-check: the entry still points at a file and the composer
// is non-empty.
func (a *App) openPromptAction() tea.Cmd {
	if a.loadedPrompt == nil {
		return nil
	}
	if _, err := os.Stat(a.loadedPrompt.Path); err != nil {
		a.clearLoadedPrompt()
		a.addSystem("prompt file no longer exists")
		return nil
	}
	if strings.TrimSpace(a.editor.Value()) == "" {
		a.clearLoadedPrompt()
		a.addSystem("nothing to overwrite; the prompt is empty")
		return nil
	}
	a.promptAction = true
	a.promptConfirm = ""
	return nil
}

// handlePromptActionKey drives the ctrl+s action bar. It swallows every key it
// does not handle so y/n/d can never reach the editor while a destructive
// confirm is on screen.
func (a *App) handlePromptActionKey(m tea.KeyMsg) tea.Cmd {
	if a.loadedPrompt == nil {
		a.promptAction = false
		a.promptConfirm = ""
		return nil
	}
	switch a.promptConfirm {
	case "":
		switch m.String() {
		case "enter":
			a.promptConfirm = "overwrite"
		case "d":
			a.promptConfirm = "delete"
		case "esc":
			a.promptAction = false
		}
		return nil
	case "overwrite":
		switch m.String() {
		case "y":
			return a.confirmOverwritePrompt()
		case "n", "esc":
			a.promptConfirm = ""
		}
		return nil
	case "delete":
		switch m.String() {
		case "y":
			return a.confirmDeletePrompt()
		case "n", "esc":
			a.promptConfirm = ""
		}
		return nil
	}
	return nil
}

// confirmOverwritePrompt writes the composer over the loaded entry in place,
// preserving its path, order, enabled state and scope. A global entry stays
// global — this is the f7 bug fixed.
func (a *App) confirmOverwritePrompt() tea.Cmd {
	if a.loadedPrompt == nil || strings.TrimSpace(a.editor.Value()) == "" {
		a.clearLoadedPrompt()
		return nil
	}
	if _, err := os.Stat(a.loadedPrompt.Path); err != nil {
		a.clearLoadedPrompt()
		a.addSystem("prompt file no longer exists")
		return nil
	}
	updated, err := promptlib.Update(*a.loadedPrompt, a.editor.Value())
	if err != nil {
		a.addSystem("overwrite failed: " + err.Error())
		a.clearLoadedPrompt()
		return nil
	}
	a.loadedPrompt.Prompt = updated.Prompt
	a.promptAction = false
	a.promptConfirm = ""
	a.addSystem(fmt.Sprintf("updated prompt %s (%s)", updated.Name, updated.Scope))
	return nil
}

// confirmDeletePrompt deletes the loaded entry's file and drops the badge.
func (a *App) confirmDeletePrompt() tea.Cmd {
	if a.loadedPrompt == nil {
		a.clearLoadedPrompt()
		return nil
	}
	e := *a.loadedPrompt
	if err := promptlib.Delete(e); err != nil {
		a.addSystem("delete failed: " + err.Error())
	} else {
		a.addSystem("deleted prompt " + e.Name)
	}
	a.clearLoadedPrompt()
	return nil
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
		a.tokensTotal += m.Usage.TotalTokens
	}
	if !m.Done {
		return nil
	}
	if len(a.messages) > 0 && a.messages[len(a.messages)-1].Role == "assistant" {
		a.messages[len(a.messages)-1].Usage = m.Usage
		a.messages[len(a.messages)-1].Materialise()
	}
	a.persistTail()
	a.refreshFooter()
	return nil
}

// handleAgentEvent renders one agent streaming event.
func (a *App) handleAgentEvent(m agentEventMsg) tea.Cmd {
	evStart := time.Now()
	defer func() { a.trace.Event("tui", "agent_event", time.Since(evStart)) }()
	switch m.Kind {
	case agent.EventWarningKind:
		a.addSystem(m.Warning)
		return a.nextAgent()
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
		if m.Err != nil {
			a.addSystem("agent error: " + m.Err.Error())
		}
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
	case agent.EventToolDiffKind:
		a.setPhaseWorking()
		// Render-only, like progress: observed around the tool rather than
		// returned by it, and never part of the transcript sent to a model.
		if m.ToolCallID != "" && m.Diff != nil {
			for i := len(a.messages) - 1; i >= 0; i-- {
				if a.messages[i].Role == "tool" && a.messages[i].ToolCallID == m.ToolCallID {
					a.messages[i].SetDiff(m.Diff)
					break
				}
			}
		}
		return a.nextAgent()
	case agent.EventCwdKind:
		a.setPhaseWorking()
		a.applyCwd(m.CwdDir, m.Cwd)
		return a.nextAgent()
	case agent.EventToolMetaKind:
		a.setPhaseWorking()
		// Render-only metadata (e.g. Read start_line) that the TUI needs for
		// line numbering but never enters the conversation.
		if m.ToolCallID != "" && len(m.Meta) > 0 {
			for i := len(a.messages) - 1; i >= 0; i-- {
				if a.messages[i].Role == "tool" && a.messages[i].ToolCallID == m.ToolCallID {
					a.messages[i].Meta = m.Meta
					break
				}
			}
		}
		return a.nextAgent()
	case agent.EventToolProgressKind:
		a.setPhaseWorking()
		// Render-only: the live tail never enters a.messages' content and so
		// never reaches buildTurns or a model. It is replaced wholesale when
		// the authoritative result lands.
		if m.ToolCallID != "" {
			for i := len(a.messages) - 1; i >= 0; i-- {
				if a.messages[i].Role == "tool" && a.messages[i].ToolCallID == m.ToolCallID {
					a.messages[i].AppendProgress(m.ToolProgress)
					break
				}
			}
			if a.follow {
				a.vp.GotoBottom()
			}
		}
		return a.nextAgent()
	case agent.EventSubagentKind:
		// Roster delta. Do not call setPhaseWorking: the composer keeps the
		// exploring indicator until a real parent stream event lands.
		if m.Subagent != nil {
			a.handleSubagentUpdate(*m.Subagent)
			done, total, ref := a.exploringSummary()
			a.setPhaseExploring(done, total, ref)
		}
		return a.nextAgent()
	case agent.EventSubagentActivityKind:
		// One forwarded tool start/result from a subagent. Render-only; the
		// row is tagged with the subagent's ID and never enters buildTurns.
		if m.Err != nil {
			a.addSystem(fmt.Sprintf("[%s] error: %s", subagentGutterLabel(m.SubagentID), m.Err))
			return a.nextAgent()
		}
		found := false
		if m.ToolCallID != "" {
			for i := len(a.messages) - 1; i >= 0; i-- {
				mm := a.messages[i]
				if mm.SubagentID == m.SubagentID && mm.ToolCallID == m.ToolCallID {
					mm.SetContent(m.ToolResult)
					mm.Status = toolResultStatus(m.ToolName, m.ToolResult)
					a.messages[i] = mm
					found = true
					break
				}
			}
		}
		if !found {
			a.messages = append(a.messages, components.Message{
				Role:       "tool",
				SubagentID: m.SubagentID,
				ToolName:   m.ToolName,
				ToolArgs:   m.ToolArgs,
				ToolCallID: m.ToolCallID,
				StartedAt:  time.Now(),
			})
		}
		return a.nextAgent()
	case agent.EventToolResultKind:
		a.setPhaseWorking()
		// Key by ToolCallID: concurrent read-only tools may complete out of
		// order, so the result must land on its own row rather than the last
		// tool row.
		if m.ToolCallID != "" {
			for i := len(a.messages) - 1; i >= 0; i-- {
				if a.messages[i].Role == "tool" && a.messages[i].ToolCallID == m.ToolCallID {
					a.messages[i].SetContent(m.ToolResult)
					a.messages[i].Status = toolResultStatus(m.ToolName, m.ToolResult)
					a.persistTail() // mid-turn durability: the settled pair is now complete
					return a.nextAgent()
				}
			}
		}
		// Fallback for legacy events without a ToolCallID: the last tool row.
		if len(a.messages) > 0 && a.messages[len(a.messages)-1].Role == "tool" {
			a.messages[len(a.messages)-1].SetContent(m.ToolResult)
			a.messages[len(a.messages)-1].Status = toolResultStatus(m.ToolName, m.ToolResult)
			a.persistTail()
		}
		return a.nextAgent()
	case agent.EventPermissionAskKind:
		if m.Ask == nil || m.AskReply == nil {
			a.addSystem("permission ask required for " + m.AskName)
			return a.nextAgent()
		}
		a.permAskState = newPermissionAskState(m.Ask, m.AskReply)
		return a.push(viewPermissionAsk)
	case agent.EventPlanFileKind:
		a.planReview = newPlanReviewState(m.PlanName, m.PlanPath)
		a.addSystem("plan written: " + m.PlanPath)
		// Keep draining the stream: the plan review pane is non-blocking and
		// the turn still needs to finish cleanly (EventDoneKind).
		cmd := a.push(viewPlanReview)
		return tea.Batch(cmd, a.nextAgent())
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
	case agent.EventContinuationKind:
		if m.MaxPasses > 0 {
			a.addSystem(fmt.Sprintf("turn budget reached — continuing (%d/%d)", m.Pass, m.MaxPasses))
		} else {
			a.addSystem(fmt.Sprintf("turn budget reached — continuing (%d)", m.Pass))
		}
		return a.nextAgent()
	case agent.EventTodosKind:
		if m.Todos != nil {
			a.setTodos(m.Todos)
		}
		return a.nextAgent()
	case agent.EventGoalStateKind:
		if m.GoalState != nil {
			a.appendEntry(m.GoalState.ToEntry(a.lastEntryID))
		}
		return a.nextAgent()
	case agent.EventGoalEvalKind:
		if m.Todos != nil {
			a.setTodos(m.Todos)
		}
		if m.Malformed {
			a.addSystem(fmt.Sprintf("goal evaluator: malformed reply (pass %d) — continuing as %s", m.Pass, m.GoalSentinel.Label()))
		} else if m.GoalSentinel != "" {
			a.addSystem(fmt.Sprintf("goal evaluator: %s (pass %d)", m.GoalSentinel.Label(), m.Pass))
		}
		return a.nextAgent()
	case agent.EventPlanEvalKind:
		if m.Todos != nil {
			a.setTodos(m.Todos)
		}
		if m.Malformed {
			a.addSystem(fmt.Sprintf("plan evaluator: malformed reply (pass %d) — continuing as %s", m.Pass, m.PlanSentinel.Label()))
		} else if m.PlanSentinel != "" {
			a.addSystem(fmt.Sprintf("plan evaluator: %s (pass %d)", m.PlanSentinel.Label(), m.Pass))
		}
		return a.nextAgent()
	case agent.EventDoneKind:
		if !a.phaseStartedAt.IsZero() {
			a.trace.Event("tui", "turn_total", time.Since(a.phaseStartedAt))
		}
		a.cancel = nil
		a.endPhase()
		if last := a.trailingAssistant(); last >= 0 {
			a.messages[last].Usage = m.Result.Usage
		}
		// Backfill the final reply into the trailing assistant bubble so a
		// turn that streamed only tool calls or arrived as one final chunk is
		// never persisted (or rendered) as an empty frame. The trailing
		// assistant bubble is found past any informational system lines (a
		// pass-loop evaluator verdict lands after the reply).
		if last := a.trailingAssistant(); last >= 0 &&
			strings.TrimSpace(a.messages[last].Text()) == "" && m.Result.Reply != "" {
			a.messages[last].SetContent(m.Result.Reply)
		}
		// The turn is over: flush any streamed builder back into Content so the
		// message is a plain value for the persistence and rebuild paths.
		if last := a.trailingAssistant(); last >= 0 {
			a.messages[last].Materialise()
		}
		if m.Result.Usage != nil {
			a.usage = m.Result.Usage
			a.usageStale = false
			a.tokensTotal += m.Result.Usage.TotalTokens
		}
		a.persistTail()
		// In plan mode, extract a numbered plan out of the reply and persist it
		// so /todos and a later resume can rebuild progress. The review pane
		// (triggered by EventPlanFileKind) replaces the old /execute / /refine
		// prompt, so this block only records state.
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
			}
		}
		// Advance the shared todo list from [DONE:n] markers in the assistant
		// reply. Only assistant text is passed — tool results never reach this.
		if a.todos != nil && m.Result.Reply != "" {
			a.todos.ApplyMarkers(m.Result.Reply)
			a.appendEntry(a.todos.ToEntry(""))
		}
		a.refreshFooter()
		return a.flushPendingActivitySends()
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

// toolResultStatus maps a tool result to its row status glyph.
func toolResultStatus(name, result string) string {
	switch {
	case strings.HasPrefix(result, "tool result withheld:"):
		return "withheld"
	case name == "Bash" && strings.Contains(result, "exit status"):
		return "✗"
	default:
		return "✓"
	}
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
		// Subagent activity rows are render-only. Promoting one would push raw,
		// unclassified child tool output into the parent conversation.
		if m.SubagentID != "" {
			continue
		}
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
	return normaliseTurns(turns)
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
		Messages:      a.filteredMessages(),
		Width:         a.contentWidth(),
		ExpandAll:     a.expandAll,
		ShowReasoning: a.reasoningVisible(),
		ShowTools:     a.toolCallsVisible(),
	}.Render()
	// The banner is the first entry of the scrollable transcript. Prepending it
	// here — before the content compare, Highlight and SetContent — keeps the
	// line map aligned: each prepended screen row gets a Chrome provenance
	// entry, so hover and drag-selection never misattribute a transcript row
	// to the banner.
	body, lm = a.prependBanner(body, lm)
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
	// Derive the hover target from the frame that is about to be drawn, so the
	// footer hint always matches the panel under the pointer — including after
	// ctrl+o or a streaming delta, which arrive without a new mouse event.
	a.recomputeHover()

	var sb strings.Builder
	sb.WriteString(a.vp.View())
	sb.WriteString("\n")
	if len(a.autocomplete) > 0 {
		sb.WriteString(a.renderSuggestions())
		sb.WriteString("\n")
	}
	if a.agentPickerVisible() {
		sb.WriteString(a.renderAgentPicker())
		sb.WriteString("\n")
	}
	if a.promptPickerVisible() {
		sb.WriteString(a.renderPromptPicker())
		sb.WriteString("\n")
	}
	if a.filePickerVisible() {
		sb.WriteString(a.renderFilePicker())
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
	if a.runsOpen {
		sb.WriteString(a.renderRunsPanel())
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
		if a.savePromptConfirm {
			title, accent, meta = "overwrite existing prompt '"+a.savePromptName+"'?", lipgloss.TerminalColor(components.ColorAmber), "y/N · esc cancel"
		} else {
			title, accent, meta = "name prompt", lipgloss.TerminalColor(components.ColorAmber), "⏎ save · esc cancel"
		}
	}
	if a.saveFileMode {
		title, accent, meta = "save file", lipgloss.TerminalColor(components.ColorAmber), "⏎ save · esc cancel"
	}
	if a.historyActive {
		if a.promptPickerVisible() {
			meta = "↑↓ cycle · tab name · → accept · ⏎ use · esc cancel"
		} else {
			meta = "↑↓ cycle · → accept · ⏎ use · esc cancel"
		}
	}
	// The composer badge: when the text came from a library entry, say which
	// one. The dirty marker is honest about whether an overwrite would change
	// anything; `g` marks a global entry rather than a second colour.
	if a.loadedPrompt != nil && !a.promptAction {
		name := a.loadedPrompt.Name
		if a.loadedPrompt.Scope == config.ScopeGlobal {
			name += " g"
		}
		if a.editor.Value() != a.loadedPrompt.Prompt {
			name += "*"
		}
		title = "ask " + components.Chip("✎ "+name, components.ColorTealSoft)
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
	} else if a.phase == phaseExploring {
		// Explore fan-out: the composer keeps the exploring indicator until a
		// real parent stream event lands.
		pill := components.Chip("explore", components.ColorTealSoft)
		progress := fmt.Sprintf("exploring %d/%d", a.exploreDone, a.exploreTotal)
		if a.exploreRef != "" {
			progress += " · " + a.exploreRef
		}
		title = a.spinMark() + " " + pill + " " + components.MutedStyle.Render(progress+a.elapsedLabel())
		accent = lipgloss.TerminalColor(components.ColorTealSoft)
		meta = "⏎ steer · esc cancel"
	} else if a.phase == phaseWorking {
		title = a.spinMark() + " working" + a.elapsedLabel()
		accent = lipgloss.TerminalColor(components.ColorAmber)
		meta = "⏎ steer · esc cancel"
	}
	// The ctrl+s action bar: overwrite or delete the loaded entry. The confirm
	// question goes in Title (Meta is dropped at narrow widths, unacceptable
	// for a destructive confirm); amber for overwrite, red for delete.
	if a.promptAction && a.loadedPrompt != nil {
		switch a.promptConfirm {
		case "overwrite":
			title = "overwrite existing prompt '" + a.loadedPrompt.Name + "'?"
			accent = lipgloss.TerminalColor(components.ColorAmber)
			meta = "y/N · esc cancel"
		case "delete":
			title = "delete prompt '" + a.loadedPrompt.Name + "'?"
			accent = lipgloss.TerminalColor(components.ColorDanger)
			meta = "y/N · esc cancel"
		default:
			title = "ask " + components.Chip("✎ "+a.loadedPrompt.Name, components.ColorTealSoft)
			accent = lipgloss.TerminalColor(components.ColorTealSoft)
			meta = "⏎ overwrite · d delete · esc cancel"
		}
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
	for i, s := range a.autocomplete {
		if i == a.autocompleteIndex {
			parts = append(parts, components.Chip(s, components.ColorTealSoft))
			continue
		}
		parts = append(parts, components.KeyStyle.Render(s))
	}
	line := components.MutedStyle.Render("⌕ ") + strings.Join(parts, components.MutedStyle.Render("  ·  "))
	return lipgloss.NewStyle().MaxWidth(a.contentWidth()).Render(line)
}

func (a *App) bannerVisible() bool {
	if a.settings.UI != nil && a.settings.UI.Banner != nil {
		return *a.settings.UI.Banner
	}
	return true
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
	a.modeSticky = true
	a.modeDecision = rolemanager.ModeDecision{}
	a.syncPlanMode()
	a.saveMode()
}

func (a *App) toggleCaveman() tea.Cmd {
	next := !a.settings.CavemanEnabled()
	if err := a.mutateSetting(func(s *config.Settings) { s.Caveman = &next }); err != nil {
		a.addSystem("caveman toggle failed: " + err.Error())
		return nil
	}
	a.invalidateAgentSession()
	a.addSystem("caveman: " + boolLabel(next))
	return nil
}

// cycleEffort cycles the session reasoning-effort level through
// default → low → medium → high → default. The new value is written to
// session state only, never to settings.json.
func (a *App) cycleEffort() tea.Cmd {
	provider, model := a.cfg.Provider, a.cfg.Model
	levels := models.Efforts(provider, model)
	if len(levels) == 0 {
		levels = models.DefaultEfforts()
	}

	// Build the cycle: "" (default) followed by the provider/model levels.
	cycle := append([]string{""}, levels...)
	current := a.settings.Effort
	idx := 0
	for i, v := range cycle {
		if v == current {
			idx = i
			break
		}
	}
	next := cycle[(idx+1)%len(cycle)]

	a.state.Effort = next
	_ = config.SaveState(a.state)
	a.settings.Effort = next
	a.invalidateAgentSession()
	label := next
	if label == "" {
		label = "default"
	}
	d := run.ResolveDialect(run.Config{Provider: provider, Model: model})
	if !d.UsesEffort() {
		a.addSystem(fmt.Sprintf("reasoning effort: %s (ignored by %s)", label, provider))
	} else {
		a.addSystem("reasoning effort: " + label)
	}
	return a.refreshProvider()
}

func (a *App) toggleGuardrails() tea.Cmd {
	next := !a.guardrailsEnabled()
	a.guardrailsOverride = &next
	a.invalidateAgentSession()
	a.syncPosture()
	a.traceRecord("guardrails_toggle", boolLabel(next), "", "", 0)
	a.addSystem("guardrails: " + boolLabel(next))
	return nil
}

// syncPosture pushes the effective policy and ask gate to every surface that
// holds its own copy of them: the shared Live every session reads at each gate,
// and the background-agent manager. A toggle pressed mid-turn therefore lands
// on the next gate check in the running session, its explore subagents, and
// any background agent already running, instead of only on the next send.
func (a *App) syncPosture() {
	a.live.Set(a.effectivePosture(), !a.askEnabled())
	if a.bgManager != nil {
		a.bgManager.SetPosture(a.effectivePosture())
	}
}

func (a *App) toggleAsk() tea.Cmd {
	next := !a.askEnabled()
	a.askOverride = &next
	a.invalidateAgentSession()
	a.syncPosture()
	// Turning ask off while an approval prompt is on screen resolves it as
	// allow-once and dismisses it, rather than leaving the view up against a
	// gate that is now off.
	if !next {
		a.resolvePendingAsk(true)
	}
	a.traceRecord("ask_toggle", boolLabel(next), "", "", 0)
	a.addSystem("ask: " + boolLabel(next))
	return nil
}

func (a *App) setYolo(on bool) tea.Cmd {
	if on {
		v := false
		a.guardrailsOverride = &v
		a.askOverride = &v
		a.resolvePendingAsk(true)
	} else {
		a.guardrailsOverride = nil
		a.askOverride = nil
	}
	a.invalidateAgentSession()
	a.syncPosture()
	label := "off"
	if on {
		label = "on"
	}
	a.traceRecord("yolo", label, "", "", 0)
	a.addSystem("yolo: " + label)
	return nil
}

// announceProfileGateDrops emits a system message when an engaged agent's
// profile lowers guardrails or ask below the current settings. Silence is not
// allowed for this class of posture drop.
func (a *App) announceProfileGateDrops(name string) {
	p, err := agentprofile.Load(name)
	if err != nil {
		return
	}
	baseGuardrails := a.settings.GuardrailsEnabled()
	baseAsk := a.settings.AskPermissionEnabled()
	if (p.Guardrails != nil && !*p.Guardrails && baseGuardrails) ||
		(p.AskPermission != nil && !*p.AskPermission && baseAsk) {
		var dropped []string
		if p.Guardrails != nil && !*p.Guardrails && baseGuardrails {
			dropped = append(dropped, "guardrails")
		}
		if p.AskPermission != nil && !*p.AskPermission && baseAsk {
			dropped = append(dropped, "ask")
		}
		msg := fmt.Sprintf("agent profile %s lowers %s", p.Name, strings.Join(dropped, " and "))
		a.addSystem(msg)
		a.traceRecord("profile_posture_drop", strings.Join(dropped, ","), "", p.Name, 0)
	}
}

func (a *App) traceRecord(event, verdict, tool, detail string, pass int) {
	if a.trace == nil {
		return
	}
	a.trace.Record(trace.Record{Phase: "tui", Event: event, Verdict: verdict, Tool: tool, Pass: pass, Detail: detail})
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

// maybeNoticeLegacyPrompts emits a one-line notice the first time a session
// sees a leftover prompts.json from the old two-file library while the new
// prompts/ directory does not exist. A notice, not a migration: nothing reads
// or touches the old file.
func (a *App) maybeNoticeLegacyPrompts() {
	if a.promptLegacyNoticed {
		return
	}
	a.promptLegacyNoticed = true

	legacy := func(jsonPath, dirPath string) bool {
		if _, err := os.Stat(jsonPath); err != nil {
			return false
		}
		if _, err := os.Stat(dirPath); err == nil {
			return false
		}
		return true
	}

	if gd, err := config.GlobalDir(); err == nil {
		if gpd, err := config.GlobalPromptsDir(); err == nil &&
			legacy(filepath.Join(gd, "prompts.json"), gpd) {
			a.addSystem("prompt library moved to prompts/ under the signet home; prompts.json is no longer read")
			return
		}
	}
	if legacy(filepath.Join(a.workdir, ".vulnetix", "prompts.json"), config.ProjectPromptsDir(a.workdir)) {
		a.addSystem("prompt library moved to .vulnetix/prompts/; prompts.json is no longer read")
	}
}

// applyCwd records a working-directory move: the footer follows it and the
// thread gets one informational line, so a reader scrolling back can tell
// which directory the relative paths around it were resolved against.
//
// dir is the absolute location and rel is the same place relative to the
// session root ("" for the root itself). A move to where we already are is
// ignored rather than announced twice.
func (a *App) applyCwd(dir, rel string) {
	if dir == "" || dir == a.cwd {
		return
	}
	a.cwd = dir
	// The footer grows or loses its first line with the directory, so the
	// memoised height has to go with it.
	a.footerW = -1
	label := "/" + rel
	if rel == "" {
		label = "/ (session root)"
	}
	a.addSystem("working directory: " + label)
	a.refreshFooter()
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
		if !a.modeSticky {
			a.mode = "agent"
		}
		a.modeDecision = rolemanager.ModeDecision{}
		a.modeWarning = "mode classifier error: " + err.Error()
		a.addSystem(a.modeWarning)
		a.syncPlanMode()
		return
	}
	previous := a.mode
	a.namedAgent = d.AgentName
	a.modeDecision = d
	a.modeWarning = d.Warning
	if !a.modeSticky {
		a.mode = string(d.Mode)
	}
	a.syncPlanMode()
	if d.AgentName != "" {
		a.announceProfileGateDrops(d.AgentName)
	}
	switch {
	case d.AgentName != "":
		a.addSystem("engaged agent: " + d.AgentName)
	case string(d.Mode) != previous:
		msg := "mode: " + string(d.Mode)
		if d.Explore {
			msg += " (launch explore agents)"
		}
		a.addSystem(msg)
	case d.Explore:
		// The mode is where it already was, so naming it repeats the footer
		// chip. What the turn does differently is still worth a line.
		a.addSystem("launch explore agents")
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
			a.bgManager = bgagent.NewManager(a.workdir, m.cfg, a.client, a.settings, a.effectivePosture())
			a.bgManager.SetPool(a.agentPool)
		}
	} else {
		a.showCredentialMessage(m.cfg.Provider, a.resolver)
	}
	a.invalidateAvailability()
	a.setCredentialBackendDefault()
	a.refreshFooter()
	// Warm the live catalogue in the background: the footer's context meter
	// scales to the selected model's context window, which most providers
	// only declare in their live catalogue.
	cmds := []tea.Cmd{}
	if cmd := a.prefetchCatalogCmd(m.cfg.Provider); cmd != nil {
		cmds = append(cmds, cmd)
	}
	if a.pending != "" && m.status.Configured {
		cmds = append(cmds, a.sendPending())
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
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

// reResolveCredentials is the synchronous half of refreshProvider. It is used
// by send as a last-ditch retry when the async initial credential check has
// not landed or failed spuriously. If credentials resolve, it adopts the new
// config and rebuilds the classifier.
func (a *App) reResolveCredentials() bool {
	if a.resolver == nil {
		return false
	}
	src := run.CredentialSource(run.EnvSource(os.Getenv))
	if a.resolver != nil {
		src = a.resolver
	}
	cfg, status := run.Prepare(a.cfg.Model, a.cfg.Provider, src)
	if !status.Configured {
		return false
	}
	cfg.Effort = a.settings.Effort
	if cc, err := run.ResolveClassifier(cfg, a.settings.Classifier, src); err == nil {
		cfg.Classifier = cc
	}
	a.cfg = cfg
	a.status = status
	a.classifier = nil
	a.invalidateAgentSession()
	a.SetClassifier(run.NewClassifier(cfg, a.client))
	a.refreshFooter()
	return true
}

// reloadSettings recomputes the merged settings view after a mutation.
func (a *App) reloadSettings() error {
	eff, err := config.Resolve(a.workdir, os.Getenv, a.flags)
	if err != nil {
		return err
	}
	a.settings = eff.Settings
	a.eff = eff
	if a.resolver != nil {
		a.resolver.SetSettings(a.settings)
		a.resolver.SetFirewallEnabled(a.firewallOverride)
	}
	if a.agentPool != nil {
		a.agentPool.SetSize(a.settings.Resilience.MaxAgentsOr(3))
	}
	// A /settings edit of guardrails or ask_permission lands live through the
	// shared holder, exactly like the f3/f4 toggles.
	a.syncPosture()
	a.refreshFooter()
	return nil
}

func (a *App) refreshFooter() {
	// The footer draws a full-width rule, so it must measure the space inside
	// the outer padding, not the terminal.
	a.footer.Width = a.contentWidth()
	a.footer.Mode = a.mode
	a.footer.Agent = a.engagedAgent()
	a.footer.Provider = a.cfg.Provider
	a.footer.Model = run.WireModel(a.cfg.Provider, a.cfg.Model)
	// The effective settings are the UI's canonical effort source: the model
	// picker and settings view both write there, and refreshProvider copies
	// the value into cfg for the agent session. If the active provider does
	// not transmit effort on the wire, render it as "default" so we never
	// advertise a value that silently never ships.
	a.footer.Effort = a.settings.Effort
	if !run.ResolveDialect(a.cfg).UsesEffort() {
		a.footer.Effort = ""
	}
	a.footer.Guardrails = a.guardrailsEnabled()
	a.footer.Ask = a.askEnabled()
	a.footer.Firewall = a.firewallEnabled()
	a.footer.Caveman = a.settings.CavemanEnabled()
	// The footer shows where relative paths currently resolve from, which is
	// the session's working directory rather than the root it started at.
	// Outside a repository the directory is still worth showing once the
	// agent has moved: without it the footer would silently disagree with
	// every path in the transcript.
	switch {
	case a.gitOK:
		a.footer.Branch = a.gitInfo.Branch
		a.footer.Cwd = a.cwd
	case a.cwd != "" && a.cwd != a.workdir:
		a.footer.Cwd = a.cwd
	default:
		a.footer.Cwd = ""
	}
	a.footer.Session = a.sessionDisplay()
	a.footer.SessionName = a.sessionName
	a.footer.ShowName = a.settings.SessionNamesVisible()
	a.footer.Hint = a.hoverHint()
	// The subagent strip is now rendered inside the runs panel; the footer
	// line stays passive so focus never appears in the footer.
	for i := range a.subagents {
		a.subagents[i].Focused = false
	}
	a.footer.Subagents = a.subagents
	a.footer.MainFocused = false

	est := a.contextEstimate()
	a.footer.Tokens = est.Tokens
	a.footer.Estimated = est.LastUsageIndex < 0
	a.footer.ContextStale = a.usageStale
	if limit, ok := modelinfo.ResolveWith(a.cfg.Model, a.settings.ContextWindows, a.selectedModelWindow()); ok {
		a.footer.ContextLimit = limit
	} else {
		a.footer.ContextLimit = 0
	}
}

// selectedModelWindow returns the context window the selected model declares
// in the live-fetched or profile catalogue (or the static one), 0 when none.
func (a *App) selectedModelWindow() int {
	for _, m := range a.catalogFor(a.cfg.Provider) {
		if m.ID == a.cfg.Model {
			return m.ContextWindow
		}
	}
	return 0
}

func (a *App) guardrailsEnabled() bool {
	if a.guardrailsOverride != nil {
		return *a.guardrailsOverride
	}
	if p, ok := a.engagedProfile(); ok && p.Guardrails != nil {
		return *p.Guardrails
	}
	return a.settings.GuardrailsEnabled()
}

// firewallEnabled returns the effective firewall state: override first, then
// the resolved effective settings.
func (a *App) firewallEnabled() bool {
	if a.firewallOverride != nil {
		return *a.firewallOverride
	}
	return a.settings.FirewallEnabled()
}

// firewallAvailable reports whether the AI Firewall can route the current
// provider, independently of the enabled flag. The resolver's reasoned state
// is the single source of truth.
func (a *App) firewallAvailable() bool {
	if a.resolver == nil {
		return false
	}
	return a.resolver.FirewallState(a.cfg.Provider).Reason == ""
}

// engagedProfile returns the currently engaged agent definition, if any.
func (a *App) engagedProfile() (agentprofile.AgentProfile, bool) {
	if a.namedAgent == "" {
		return agentprofile.AgentProfile{}, false
	}
	p, err := agentprofile.Load(a.namedAgent)
	if err != nil {
		return agentprofile.AgentProfile{}, false
	}
	return p, true
}

// toggleFirewall flips the firewall override, persists it through the
// settings mutation seam, and refreshes the footer. Turning on when the
// firewall is unavailable prints the real reason and leaves state unchanged;
// turning off is never blocked.
func (a *App) toggleFirewall() tea.Cmd {
	on := !a.firewallEnabled()
	if on && !a.firewallAvailable() {
		if a.resolver != nil {
			st := a.resolver.FirewallState(a.cfg.Provider)
			if st.Reason != "" {
				a.addSystem("Vulnetix AI Firewall: " + st.Reason)
				return nil
			}
		}
		a.addSystem("Vulnetix AI Firewall: unavailable")
		return nil
	}
	a.firewallOverride = &on
	if err := a.mutateSetting(func(s *config.Settings) {
		if s.Vulnetix == nil {
			s.Vulnetix = &config.VulnetixSettings{}
		}
		s.Vulnetix.FirewallEnabled = &on
	}); err != nil {
		a.addSystem("firewall toggle failed: " + err.Error())
		return nil
	}
	// reloadSettings was called by mutateSetting; push override and refresh.
	if a.resolver != nil {
		a.resolver.SetFirewallEnabled(a.firewallOverride)
	}
	if cfg, err := a.resolveConfig(); err == nil {
		a.cfg = cfg
	}
	a.refreshFooter()
	if on {
		a.addSystem(a.firewallOnMessage())
	}
	return nil
}

// resolveConfig re-resolves the active provider config from current settings.
func (a *App) resolveConfig() (run.Config, error) {
	name := a.cfg.Provider
	if a.requestedProvider != "" {
		name = a.requestedProvider
	}
	return run.ResolveWithSource(a.cfg.Model, name, os.Getenv, a.resolver)
}

// firewallOnMessage reports what changed when the firewall was just enabled.
func (a *App) firewallOnMessage() string {
	var gateway, org string
	if a.resolver != nil {
		st := a.resolver.FirewallState(a.cfg.Provider)
		if st.Reason == "" {
			gateway = aifirewall.HostOf(st.BaseURL)
			org = aifirewall.URLPathUUID(st.BaseURL)
		}
	}
	msg := "Vulnetix AI Firewall on"
	if gateway != "" {
		msg += " · gateway " + gateway
	}
	if org != "" {
		msg += " · org " + org
	}
	msg += " · provider key is no longer sent"
	return msg
}

// effectivePosture is the policy every surface must consult, rather than
// a.posture. The configured posture is what preferences and flags asked for;
// this is what the operator's guardrails switch leaves of it. Reading
// a.posture directly is how a surface keeps enforcing after the footer says
// guardrails are off.
func (a *App) effectivePosture() posture.Policy {
	if !a.guardrailsEnabled() {
		return posture.AllIgnore()
	}
	return a.posture
}

func (a *App) askEnabled() bool {
	if a.askOverride != nil {
		return *a.askOverride
	}
	if p, ok := a.engagedProfile(); ok && p.AskPermission != nil {
		return *p.AskPermission
	}
	return a.settings.AskPermissionEnabled()
}

func (a *App) sessionDisplay() string {
	if len(a.sessionID) >= 8 {
		return a.sessionID[:8]
	}
	return a.sessionID
}

// exitCard assembles the branded session-end card printed after the TUI exits.
// It is the bookend to the banner: same owl, same facts, plus the resume
// command that returns to this session.
func (a *App) exitCard() components.ExitCard {
	return components.ExitCard{
		Name:      a.sessionName,
		SessionID: a.sessionID,
		ResumeArg: a.resumeArg(),
		Turns:     userTurnCount(a.messages),
		Duration:  formatDuration(time.Since(a.startedAt)),
		Tokens:    formatTokensForExit(a.tokensTotal),
		Model:     a.cfg.Model,
		Provider:  a.cfg.Provider,
		Path:      a.store.SessionPath(a.sessionKey, a.sessionID),
	}
}

// resumeArg returns the argument the printed exit command should carry: the
// 8-char prefix when it resolves uniquely, otherwise the full uuid.
func (a *App) resumeArg() string {
	prefix := shortID(a.sessionID)
	cur, _ := session.KeyFor(a.workdir)
	if _, _, err := a.store.ResolveAnywhere(cur, prefix); err == nil {
		return prefix
	}
	return a.sessionID
}

// userTurnCount counts the user prompts in the transcript.
func userTurnCount(msgs []components.Message) int {
	n := 0
	for _, m := range msgs {
		if m.Role == "user" {
			n++
		}
	}
	return n
}

// formatDuration renders a session duration as compact clock text.
func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Second {
		return ""
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

// formatTokensForExit renders a token total for the exit card's fact line.
func formatTokensForExit(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("~%.1fM tokens", float64(n)/1_000_000)
	case n >= 10_000:
		return fmt.Sprintf("~%.1fk tokens", float64(n)/1_000)
	case n > 0:
		return fmt.Sprintf("%d tokens", n)
	}
	return ""
}

// ensureSessionName persists a durable session name before the exit card
// prints. A short session can quit before the async classifier auto-name
// lands, which would leave the card and every future /resume row showing a
// bare uuid. The fallback is the first user prompt, the same rule DisplayName
// uses at read time, persisted through the existing rename path so the name
// survives rather than being reconstructed on every listing.
func (a *App) ensureSessionName() {
	if a.sessionName != "" {
		return
	}
	for _, m := range a.messages {
		if m.Role != "user" {
			continue
		}
		first := strings.Join(strings.Fields(m.Text()), " ")
		if first == "" {
			continue
		}
		a.renameSession(first)
		return
	}
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

// probeLocalBases returns the preferred local inference base URLs for an
// already-running server.
func probeLocalBases() []string {
	var bases []string
	for _, port := range localinfer.PreferredPorts() {
		bases = append(bases, localinfer.BaseURL("127.0.0.1", port))
	}
	return bases
}

// localModelReportCmd runs the machine probe and server detection off the
// render goroutine and returns a plain-language report.
func (a *App) localModelReportCmd(repo string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()

		rep := machineprobe.Probe(ctx)
		bin, haveBin := localinfer.Detect()
		running := localinfer.ProbeRunning(ctx, probeLocalBases())

		var b strings.Builder
		if running != "" {
			fmt.Fprintf(&b, "local inference server running at %s\n", running)
		} else if haveBin {
			fmt.Fprintf(&b, "local server binary found: %s (%s)\n", bin.Path, bin.Name)
		} else {
			b.WriteString("no local inference server found (llama-server, ollama, or vllm)\n")
			b.WriteString("see https://github.com/ggerganov/llama.cpp to get started\n")
		}
		fmt.Fprintf(&b, "machine: %d CPUs, %d MiB RAM (%d free), %d MiB disk free\n",
			rep.CPUs, rep.RAMTotalMiB, rep.RAMFreeMiB, rep.DiskFreeMiB)
		for _, g := range rep.GPUs {
			fmt.Fprintf(&b, "gpu: %s %d MiB\n", g.Backend, g.VRAMMiB)
		}

		modelRepo := repo
		if modelRepo == "" {
			modelRepo = "12B Q4_K_M"
		}
		v := rep.Assess(modelRepo, 7000)
		fmt.Fprintf(&b, "%s\n", v.Reason)

		if binary, ok := localinfer.HFBinary(); ok {
			fmt.Fprintf(&b, "hf CLI: %s\n", binary)
			if user, ok := localinfer.HFWhoami(ctx, binary); ok {
				fmt.Fprintf(&b, "hf auth: %s\n", user)
			}
		} else {
			b.WriteString("hf CLI: not found (install with 'pip install huggingface-hub' or 'cargo install hf')\n")
		}

		models, _ := localinfer.HFCacheScan(ctx, "")
		if len(models) > 0 {
			b.WriteString("cached models:\n")
			for _, m := range models {
				fmt.Fprintf(&b, "  %s  (%s)\n", m.File, filepath.Base(m.Path))
			}
		}

		if !haveBin {
			b.WriteString("a local server is required to serve the model; llama-server is recommended\n")
		}
		return localModelReportMsg{text: b.String()}
	}
}

// localModelStatusCmd reports every managed local inference server.
func (a *App) localModelStatusCmd() tea.Cmd {
	return func() tea.Msg {
		var b strings.Builder
		srvs := a.runningLocalServers()
		if len(srvs) == 0 {
			b.WriteString("no managed local inference server running\n")
		} else {
			b.WriteString("managed local inference servers:\n")
			for _, s := range srvs {
				fmt.Fprintf(&b, "  port %d  %s  pid %d  %s\n", s.port, s.baseURL, s.pid, s.state)
			}
		}
		origin := "not configured"
		if a.resolver != nil {
			if v, src, ok := a.resolver.Lookup("llama-server", "port"); ok {
				origin = v + " (" + string(src) + ")"
			}
		}
		fmt.Fprintf(&b, "persisted llama-server port credential: %s", origin)
		return localModelReportMsg{text: b.String()}
	}
}

type localServerInfo struct {
	port    int
	baseURL string
	pid     int
	state   string
}

// runningLocalServers returns the llama-server activities from the registry.
func (a *App) runningLocalServers() []localServerInfo {
	var out []localServerInfo
	if a.activity == nil {
		return out
	}
	for _, act := range a.activity.List() {
		if act.Kind != activity.KindShell || act.Label != "llama-server" {
			continue
		}
		info := localServerInfo{state: string(act.State)}
		if len(act.Argv) > 0 {
			for i, arg := range act.Argv {
				if arg == "--port" && i+1 < len(act.Argv) {
					if n, err := strconv.Atoi(act.Argv[i+1]); err == nil {
						info.port = n
						info.baseURL = localinfer.BaseURL("127.0.0.1", n)
					}
				}
			}
		}
		out = append(out, info)
	}
	return out
}

// localModelLaunchCmd launches a local llama-server for the given HF repo,
// persists the port credential, registers the process, and lands on the
// credential view.
func (a *App) localModelLaunchCmd(repo, portArg, quant string) tea.Cmd {
	return func() tea.Msg {
		bin, ok := localinfer.Detect()
		if !ok || bin.Name != "llama-server" {
			return localModelReportMsg{text: "launch requires the llama-server binary (llama.cpp); see https://github.com/ggerganov/llama.cpp"}
		}

		token := a.hfToken()
		var modelPath string
		if hfBin, ok := localinfer.HFBinary(); ok {
			a.addSystem("downloading model with hf CLI...")
			path, err := localinfer.HFDownload(context.Background(), hfBin, repo, quant, token, func(line string) {
				a.addSystem(line)
			})
			if err != nil {
				return localModelReportMsg{text: "download failed: " + err.Error()}
			}
			modelPath = path
		}

		port, err := a.pickLocalServerPort(portArg)
		if err != nil {
			return localModelReportMsg{text: "port allocation failed: " + err.Error()}
		}
		baseURL := localinfer.BaseURL("127.0.0.1", port)

		opts := localinfer.ArgsOptions{Port: port, Quant: quant}
		if modelPath != "" {
			opts.ModelPath = modelPath
		} else {
			opts.Repo = repo
		}
		args := localinfer.Args(opts)

		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()

		stop, err := localinfer.Launch(ctx, bin, args, baseURL, localinfer.LaunchOptions{
			Deadline: 30 * time.Second,
			HFToken:  token,
			Registry: a.activity,
			Pidfile:  a.localServerPidfile(port),
			OnLine: func(line string) {
				a.addSystem(line)
			},
		})
		if err != nil {
			return localModelReportMsg{text: "launch failed: " + err.Error()}
		}

		if err := a.persistLocalServerCredentials(port); err != nil {
			_ = stop()
			return localModelReportMsg{text: "credential persistence failed: " + err.Error()}
		}
		a.invalidateAvailability()

		// Land on the credential view with llama-server selected.
		a.initCredentialState()
		for i, p := range a.credentialState.providers {
			if p == "llama-server" {
				a.credentialState.selectedIdx = i
				break
			}
		}
		return a.push(viewCredentials)()
	}
}

// localModelDownloadCmd downloads a model with the HF CLI. When the CLI is
// absent it reports that the model will be fetched at launch.
func (a *App) localModelDownloadCmd(repo, quant string) tea.Cmd {
	return func() tea.Msg {
		bin, ok := localinfer.HFBinary()
		if !ok {
			return localModelReportMsg{text: "hf CLI not found; the model will be downloaded by llama-server at launch"}
		}
		path, err := localinfer.HFDownload(context.Background(), bin, repo, quant, a.hfToken(), func(line string) {
			a.addSystem(line)
		})
		if err != nil {
			return localModelReportMsg{text: "download failed: " + err.Error()}
		}
		return localModelReportMsg{text: "downloaded: " + path}
	}
}

// localModelStopCmd stops managed local inference servers.
func (a *App) localModelStopCmd(portArg string) tea.Cmd {
	return func() tea.Msg {
		count := 0
		for _, s := range a.runningLocalServers() {
			if portArg != "" && strconv.Itoa(s.port) != portArg {
				continue
			}
			for _, act := range a.activity.List() {
				if act.Kind == activity.KindShell && act.Label == "llama-server" {
					_ = a.activity.Kill(act.ID)
					count++
				}
			}
		}
		return localModelReportMsg{text: fmt.Sprintf("stopped %d local server(s)", count)}
	}
}

// stopLocalServers stops every managed llama-server on quit.
func (a *App) stopLocalServers() {
	for _, act := range a.activity.List() {
		if act.Kind == activity.KindShell && act.Label == "llama-server" {
			_ = a.activity.Kill(act.ID)
		}
	}
}

// hfToken resolves the HuggingFace token through the full credential stack.
func (a *App) hfToken() string {
	if a.resolver != nil {
		if token, _, ok := a.resolver.Lookup("huggingface", "api_key"); ok {
			return token
		}
	}
	if token, _, ok := run.EnvSource(os.Getenv).Lookup("huggingface", "api_key"); ok {
		return token
	}
	return ""
}

// pickLocalServerPort applies the allocation order: explicit argument, then
// the persisted llama-server credential if it is free or already serving this
// repo, then a fresh free port.
func (a *App) pickLocalServerPort(portArg string) (int, error) {
	if portArg != "" {
		return strconv.Atoi(portArg)
	}
	if a.resolver != nil {
		if v, _, ok := a.resolver.Lookup("llama-server", "port"); ok && v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				if a.portFreeOrServing(n) {
					return n, nil
				}
			}
		}
	}
	return localinfer.FreePort()
}

// portFreeOrServing reports whether a port is open or already answering as a
// local inference server.
func (a *App) portFreeOrServing(port int) bool {
	base := localinfer.BaseURL("127.0.0.1", port)
	if localinfer.ProbeRunning(context.Background(), []string{base}) != "" {
		return true
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// persistLocalServerCredentials writes host/port/protocol for the freshly
// launched server. These are non-secret optional fields.
func (a *App) persistLocalServerCredentials(port int) error {
	if a.resolver == nil {
		return nil
	}
	backend := credentials.SourceUserFile
	if a.credentialState.backend != "" {
		backend = a.credentialState.backend
	}
	if err := a.resolver.Store("llama-server", "host", "127.0.0.1", backend); err != nil {
		return err
	}
	if err := a.resolver.Store("llama-server", "port", strconv.Itoa(port), backend); err != nil {
		return err
	}
	return a.resolver.Store("llama-server", "protocol", "http", backend)
}

// localServerPidfile returns the pidfile path for a launched server.
func (a *App) localServerPidfile(port int) string {
	dir, err := config.GlobalDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "llama-server-"+strconv.Itoa(port)+".pid")
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
	if err := a.store.AppendTo(a.sessionKey, a.sessionID, e); err != nil {
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

// trailingAssistant returns the index of the last assistant message in the
// transcript, or -1. A pass-loop evaluator appends an informational system
// line after the assistant reply, so the trailing assistant bubble is not
// always the very last message; completion must still find and finalise it.
func (a *App) trailingAssistant() int {
	for i := len(a.messages) - 1; i >= 0; i-- {
		if a.messages[i].Role == "assistant" {
			return i
		}
	}
	return -1
}

// startNewSession resets to a brand-new, unnamed session. The previous
// session's file is left untouched; nothing is written until the next user
// message.
func (a *App) startNewSession() {
	a.sessionID = session.MustID()
	a.lastEntryID = ""
	a.persistedUpTo = 0
	a.sessionName = ""
	a.parentSession = ""
	a.nameRequested = false
	a.messages = nil
	a.summary = ""
	a.usage = nil
	a.usageStale = false
	// The banner tip, exit-card stats and resumed variant are all session
	// bookend state: a fresh session restarts the clock and re-picks the tip.
	a.bannerTip = components.PickTip(a.sessionID)
	a.bannerResumed = ""
	a.bannerRestoredTurns = 0
	a.startedAt = time.Now()
	a.tokensTotal = 0
	// The todo list belongs to the session that produced it. Carrying it into
	// a fresh one would render stale work and write it back under the new id.
	a.todos = nil
	// The engaged profile is session state too, and the picker reloads in case
	// a profile was written while this session ran.
	a.namedAgent = ""
	a.namedAgentTools = nil
	a.agentPickerOpen = false
	a.agentPickerSubmit = false
	a.hover = hoverTarget{}
	a.mousePresent = false
	a.saveFileMode = false
	a.saveFileMsg = -1
	a.clearLoadedPrompt()
	a.subagents = nil
	a.subagentIdx = map[string]int{}
	a.threadFilter = ""
	a.runsOpen = false
	a.runsFocus = false
	a.runsTab = tabActivity
	a.runsSel = 0
	a.runsScroll = 0
	a.loadAgents()
	a.saveSession()
	a.refreshFooter()
}

func (a *App) shouldAutoName() bool {
	return a.classifier != nil && a.sessionName == "" && !a.nameRequested && a.summary == ""
}

func (a *App) nameSessionCmd(firstUserMessage string) tea.Cmd {
	c := a.classifier
	caveman := a.settings.ClassifierCavemanEnabled()
	return func() tea.Msg {
		raw, err := c.Classify(a.ctx, rolemanager.BuildSessionNamePayload(firstUserMessage, caveman))
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

// handleVulnetixDone renders the result of an async /vulnetix run.
func (a *App) handleVulnetixDone(m vulnetixDoneMsg) tea.Cmd {
	if m.err != nil {
		a.addSystem("vulnetix failed: " + m.err.Error())
		return nil
	}
	if m.report.Status != "" {
		a.addSystem("vulnetix status:\n" + m.report.Status)
		return nil
	}
	if m.report.Summary != "" {
		a.addSystem("vulnetix:\n" + m.report.Summary)
	}
	return a.push(viewVulnetixArtifacts)
}

func (a *App) handleVulnetixProbe(m vulnetixProbeMsg) tea.Cmd {
	a.vulnetixConfigState.cap = m.cap
	a.vulnetixConfigState.loading = false
	a.vulnetixConfigState.probedAt = time.Now()
	if m.err != nil {
		a.vulnetixConfigState.errorMsg = m.err.Error()
	}
	return nil
}

func (a *App) handleProjectsLoaded(m projectsLoadedMsg) tea.Cmd {
	a.vulnetixListState.loading = false
	if m.err != nil {
		a.vulnetixListState.errorMsg = m.err.Error()
		return nil
	}
	a.vulnetixListState.entries = m.entries
	a.vulnetixListState.rows = flattenVulnetixRows(m.entries)
	return nil
}

func (a *App) handleSweepFound(m sweepFoundMsg) tea.Cmd {
	if m.err != nil {
		a.vulnetixListState.errorMsg = m.err.Error()
		return nil
	}
	// Reload the registry after the sweep batch is done.
	return a.loadVulnetixProjectsCmd()
}

func (a *App) handleArtifactsLoaded(m artifactsLoadedMsg) tea.Cmd {
	a.vulnetixArtifactsState.loading = false
	if m.err != nil {
		a.vulnetixArtifactsState.errorMsg = m.err.Error()
		return nil
	}
	a.vulnetixArtifactsState.summary = m.summary
	return nil
}

// handleAgentBuilderDone renders the result of an async /agent create run.
// After a successful save it opens the agent list with the new profile
// selected for editing, so the user can review and adjust its properties. On
// failure the user is never dropped back to chat empty-handed: a valid stub
// is saved and the editor still opens on it.
func (a *App) handleAgentBuilderDone(m agentBuilderDoneMsg) tea.Cmd {
	a.endPhase()
	if m.handle != nil {
		m.handle.Finish(0, false, m.err)
	}
	if m.err != nil {
		a.addSystem("agent builder failed: " + m.err.Error())
		stub := agentprofile.Stub(m.name)
		path, err := agentprofile.Save(stub)
		if err != nil {
			a.addSystem("agent create fallback failed: " + err.Error())
			return nil
		}
		a.addSystem("agent saved: " + path)
		a.agentState.pendingEditProfile = m.name
		return a.push(viewAgent)
	}
	a.addSystem("agent saved: " + m.path)
	a.agentState.pendingEditProfile = m.profile.Name
	return a.push(viewAgent)
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
	case agent.EventSubagentKind:
		// Background agents share the roster through the same SubagentUpdate
		// shape. It does not drive the explore phase indicator.
		if m.Subagent != nil {
			a.handleSubagentUpdate(*m.Subagent)
		}
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
	caveman := a.settings.ClassifierCavemanEnabled()
	return func() tea.Msg {
		raw, err := c.Classify(a.ctx, rolemanager.BuildCompactionPayload(doc, caveman))
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
	a.persistedUpTo = 0
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
	a.subagents = nil
	a.subagentIdx = map[string]int{}
	a.threadFilter = ""
	a.runsOpen = false
	a.runsFocus = false
	a.runsTab = tabActivity
	a.runsSel = 0
	a.runsScroll = 0

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
