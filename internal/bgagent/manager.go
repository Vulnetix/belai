package bgagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vulnetix/signet/internal/agent"
	"github.com/vulnetix/signet/internal/agentpool"
	"github.com/vulnetix/signet/internal/agentprofile"
	"github.com/vulnetix/signet/internal/calltrace"
	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/permissions"
	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/prompt"
	"github.com/vulnetix/signet/internal/repoindex"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/tools"
)

// State is the background-agent lifecycle state.
type State string

const (
	StateIdle    State = "idle"
	StateRunning State = "running"
	StatePaused  State = "paused"
	StateDone    State = "done"
)

// Event is one background-agent event.
type Event struct {
	AgentName  string
	Kind       agent.EventKind
	Text       string
	ToolName   string
	ToolResult string
	// ToolArgs and ToolCallID pair a tool start with its result. Both are
	// render-only.
	ToolArgs   string
	ToolCallID string
	Warning    string
	Err        error
	// Subagent carries a roster delta for background agents, sharing the
	// same SubagentUpdate shape the explore fan-out emits.
	Subagent   *agent.SubagentUpdate
	SubagentID string
}

// AgentStatus is a snapshot of one agent for the UI.
type AgentStatus struct {
	Name       string
	Mode       string
	State      State
	Iteration  int
	LastOutput string
}

// AgentInstance is one running background agent.
type AgentInstance struct {
	Profile    agentprofile.AgentProfile
	State      State
	Events     chan Event
	Cancel     context.CancelFunc
	History    []run.Turn
	iteration  int
	lastOutput string
	workdir    string
	// live is this instance's own posture holder, seeded from the manager's
	// current policy stricter-of-merged with the project. It is set once at
	// StartIn and then only mutated through Set (atomic), so the field itself
	// never races; SetPosture updates it mid-turn and the next gate check in
	// the running session reads the new value.
	live *posture.Live
	// resume wakes a paused loop-mode agent. Buffered size 1 so Resume never
	// blocks the UI; the loop re-checks State after waking.
	resume chan struct{}
	// task is consumed by the first turn (see StartTask).
	task Task
	mu   sync.Mutex
}

// Manager owns a map of active background agents.
type Manager struct {
	mu       sync.RWMutex
	agents   map[string]*AgentInstance
	workdir  string
	cfg      run.Config
	client   *http.Client
	settings config.Settings
	posture  posture.Policy
	// pool caps every background-agent turn against the shared FIFO fan-out
	// ceiling. nil means no shared ceiling.
	pool *agentpool.Pool
	// src resolves credentials when an agent profile overrides the main
	// provider. nil means environment-only resolution.
	src run.CredentialSource
	// session is the owning transcript session id, stamped on every
	// background turn's provider requests and tool calls through calltrace.
	session atomic.Value // string
}

// SetSessionID records the owning session id. The TUI calls it whenever its
// session changes (new, resume, fork), so later background turns carry it.
func (m *Manager) SetSessionID(id string) { m.session.Store(id) }

// sessionID returns the owning session id, or "".
func (m *Manager) sessionID() string {
	id, _ := m.session.Load().(string)
	return id
}

// NewManager creates a Manager.
func NewManager(workdir string, cfg run.Config, client *http.Client, settings config.Settings, posture posture.Policy) *Manager {
	return &Manager{
		agents:   make(map[string]*AgentInstance),
		workdir:  workdir,
		cfg:      cfg,
		client:   client,
		settings: settings,
		posture:  posture,
	}
}

// SetPosture replaces the policy future agent sessions are built with and
// pushes it into every running instance's Live, so a guardrails toggle lands on
// the next gate check inside an agent already mid-turn rather than only on its
// next session. The project layer still only tightens (choosePosture).
func (m *Manager) SetPosture(p posture.Policy) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.posture = p
	for _, inst := range m.agents {
		if inst.live != nil {
			inst.live.Set(choosePosture(p, inst.workdir), false)
		}
	}
}

// Posture returns the policy future agent sessions will be built with.
func (m *Manager) Posture() posture.Policy {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.posture
}

// SetPool attaches the shared FIFO fan-out pool so background-agent turns
// count against the same ceiling as explore subagents. A nil pool detaches it.
func (m *Manager) SetPool(p *agentpool.Pool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pool = p
}

// SetCredentialSource installs the resolver used to re-resolve a profile's
// provider override. Without it, provider overrides resolve from the
// environment only.
func (m *Manager) SetCredentialSource(src run.CredentialSource) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.src = src
}

// Start launches a background agent by name in the manager's default workdir.
func (m *Manager) Start(name string, profile agentprofile.AgentProfile) error {
	return m.StartIn(m.workdir, name, profile)
}

// StartIn launches a background agent rooted at workdir. The workdir is used
// for repository indexing, the tool registry confinement root, and the agent
// session's working directory, so the agent operates in project B while the
// TUI session remains in project A.
func (m *Manager) StartIn(workdir, name string, profile agentprofile.AgentProfile) error {
	return m.StartTask(workdir, name, profile, Task{})
}

// Task is a harness-composed job for one background run: a prompt appended
// to the profile's own, and attachments the caller has already sanitized and
// classified (never raw repository or remote bytes). It rides on the first
// turn only.
type Task struct {
	Prompt      string
	Attachments []run.Attachment
}

// StartTask is StartIn with a task for the agent's first turn.
func (m *Manager) StartTask(workdir, name string, profile agentprofile.AgentProfile, task Task) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.agents[name]; exists {
		return fmt.Errorf("agent %q already running", name)
	}
	ctx, cancel := context.WithCancel(context.Background())
	inst := &AgentInstance{
		Profile: profile,
		State:   StateIdle,
		Events:  make(chan Event, 64),
		Cancel:  cancel,
		resume:  make(chan struct{}, 1),
		workdir: workdir,
		live:    posture.NewLive(choosePosture(m.posture, workdir), false),
		task:    task,
	}
	m.agents[name] = inst
	go m.runLoop(ctx, inst)
	return nil
}

// Stop cancels and removes an agent.
func (m *Manager) Stop(name string) error {
	m.mu.Lock()
	inst, exists := m.agents[name]
	if !exists {
		m.mu.Unlock()
		return fmt.Errorf("agent %q not found", name)
	}
	delete(m.agents, name)
	m.mu.Unlock()
	if inst.Cancel != nil {
		inst.Cancel()
	}
	return nil
}

// Pause suspends a running loop-mode agent. The loop observes the state at
// its next boundary and blocks on the per-instance resume channel; the events
// channel stays open so Resume can continue the same goroutine.
func (m *Manager) Pause(name string) error {
	m.mu.RLock()
	inst, ok := m.agents[name]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("agent %q not found", name)
	}
	inst.mu.Lock()
	if inst.State != StateRunning {
		inst.mu.Unlock()
		return fmt.Errorf("agent %q is not running", name)
	}
	inst.State = StatePaused
	inst.mu.Unlock()
	return nil
}

// Resume wakes a paused loop-mode agent.
func (m *Manager) Resume(name string) error {
	m.mu.RLock()
	inst, ok := m.agents[name]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("agent %q not found", name)
	}
	inst.mu.Lock()
	if inst.State != StatePaused {
		inst.mu.Unlock()
		return fmt.Errorf("agent %q is not paused", name)
	}
	inst.State = StateRunning
	inst.mu.Unlock()
	select {
	case inst.resume <- struct{}{}:
	default:
	}
	return nil
}

// List returns snapshots of all agents.
func (m *Manager) List() []AgentStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]AgentStatus, 0, len(m.agents))
	for name, inst := range m.agents {
		inst.mu.Lock()
		out = append(out, AgentStatus{
			Name:       name,
			Mode:       inst.Profile.Mode,
			State:      inst.State,
			Iteration:  inst.iteration,
			LastOutput: inst.lastOutput,
		})
		inst.mu.Unlock()
	}
	return out
}

// Lookup returns a single agent instance.
func (m *Manager) Lookup(name string) (*AgentInstance, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	inst, ok := m.agents[name]
	return inst, ok
}

func (m *Manager) runLoop(ctx context.Context, inst *AgentInstance) {
	defer close(inst.Events)
	defer func() {
		inst.mu.Lock()
		// A paused agent is suspended, not finished: closing its events
		// channel would make Resume impossible. Only a natural exit or stop
		// marks it done.
		if inst.State != StatePaused {
			inst.State = StateDone
		}
		inst.mu.Unlock()
	}()
	switch inst.Profile.Mode {
	case agentprofile.ModeSingle:
		m.runSingle(ctx, inst)
	case agentprofile.ModeLoop:
		m.runLoopMode(ctx, inst)
	case agentprofile.ModeScheduled:
		m.runScheduled(ctx, inst)
	case agentprofile.ModeMonitor:
		m.runMonitor(ctx, inst)
	}
}

func (m *Manager) runSingle(ctx context.Context, inst *AgentInstance) {
	inst.mu.Lock()
	inst.State = StateRunning
	inst.mu.Unlock()
	runCtx, lease, ok := m.acquireLease(ctx, inst)
	if !ok {
		return // dropped while queued (cancelled)
	}
	defer m.releaseLease(inst, lease)
	m.executeTurn(runCtx, inst)
}

func (m *Manager) runLoopMode(ctx context.Context, inst *AgentInstance) {
	maxIter := inst.Profile.MaxIterations
	if maxIter <= 0 {
		maxIter = m.settings.Resilience.MaxIterationsOr(config.DefaultMaxIterations)
	}
	classifier := run.NewRoleClassifier(m.cfg, m.client, nil)

	inner := 0
	for {
		if m.blockIfPaused(ctx, inst) {
			return
		}
		select {
		case <-ctx.Done():
			return
		default:
		}

		inst.mu.Lock()
		inst.State = StateRunning
		inst.iteration++
		inst.mu.Unlock()
		runCtx, lease, ok := m.acquireLease(ctx, inst)
		if !ok {
			return // dropped while queued (cancelled)
		}
		m.executeTurn(runCtx, inst)
		m.releaseLease(inst, lease)

		select {
		case <-ctx.Done():
			return
		default:
		}

		inner++
		if inner < maxIter {
			continue
		}

		// Exhausted the inner budget: ask the evaluator what to do next.
		verdict, err := rolemanager.EvaluateAgent(ctx, classifier, inst.Profile.SystemPrompt, inst.LastOutput())
		if err != nil && !errors.Is(err, rolemanager.ErrMalformedAgentEval) {
			// A transport failure is an unknown verdict; fail safe to PAUSE
			// rather than grant unattended compute.
			inst.pushEvent(Event{AgentName: inst.Profile.Name, Kind: agent.EventErrorKind, Err: err})
			verdict = rolemanager.AgentPause
		}

		switch verdict {
		case rolemanager.AgentContinue:
			// Autonomous mode requires an explicit opt-in. A supervised profile
			// that says CONTINUE is paused instead: unattended unbounded tool
			// use is exactly what supervised is meant to prevent.
			if inst.Profile.Autonomy != agentprofile.AutonomyAutonomous {
				verdict = rolemanager.AgentPause
			} else {
				inner = 0
				continue
			}
			fallthrough
		case rolemanager.AgentPause:
			inst.mu.Lock()
			inst.State = StatePaused
			inst.mu.Unlock()
			if m.blockIfPaused(ctx, inst) {
				return
			}
			inner = 0
		case rolemanager.AgentSleep:
			select {
			case <-ctx.Done():
				return
			case <-time.After(parseSchedule(inst.Profile.Schedule)):
			}
			inner = 0
		case rolemanager.AgentStop:
			return
		}
	}
}

// blockIfPaused blocks while an agent is paused, returning true when the
// context is cancelled. Resume sends on inst.resume to wake it; the loop then
// re-checks State so a spurious wake never runs a paused agent.
func (m *Manager) blockIfPaused(ctx context.Context, inst *AgentInstance) bool {
	for {
		inst.mu.Lock()
		paused := inst.State == StatePaused
		inst.mu.Unlock()
		if !paused {
			return false
		}
		select {
		case <-ctx.Done():
			return true
		case <-inst.resume:
		}
	}
}

func (m *Manager) runScheduled(ctx context.Context, inst *AgentInstance) {
	interval := parseSchedule(inst.Profile.Schedule)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			inst.mu.Lock()
			inst.State = StateRunning
			inst.mu.Unlock()
			m.executeTurn(ctx, inst)
			inst.mu.Lock()
			inst.State = StateIdle
			inst.mu.Unlock()
		}
	}
}

func (m *Manager) runMonitor(ctx context.Context, inst *AgentInstance) {
	interval := 30 * time.Second
	if s := inst.Profile.Schedule; s != "" {
		if d := parseSchedule(s); d > 0 {
			interval = d
		}
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			triggered, err := m.evaluateMonitor(ctx, inst.Profile.MonitorCondition)
			if err != nil {
				inst.pushEvent(Event{AgentName: inst.Profile.Name, Kind: agent.EventErrorKind, Err: err})
				continue
			}
			if !triggered {
				continue
			}
			inst.mu.Lock()
			inst.State = StateRunning
			inst.mu.Unlock()
			m.executeTurn(ctx, inst)
			inst.mu.Lock()
			inst.State = StateIdle
			inst.mu.Unlock()
		}
	}
}

// acquireLease admits a background-agent turn through the shared FIFO pool,
// emitting roster deltas through the events bridge so background agents share
// the same SubagentUpdate shape as the explore fan-out. ok is false when the
// turn was dropped while queued (cancelled); the caller must return.
func (m *Manager) acquireLease(ctx context.Context, inst *AgentInstance) (runCtx context.Context, lease *agentpool.Lease, ok bool) {
	m.mu.RLock()
	pool := m.pool
	m.mu.RUnlock()
	if pool == nil {
		return ctx, nil, true
	}

	id := "bg:" + inst.Profile.Name
	update := func(state agentpool.State) {
		inst.pushEvent(Event{
			AgentName: inst.Profile.Name,
			Kind:      agent.EventSubagentKind,
			Subagent:  &agent.SubagentUpdate{ID: id, Label: inst.Profile.Name, Kind: "background", State: string(state)},
		})
	}

	update(agentpool.StateQueued)
	lease, err := pool.Acquire(ctx, agentpool.Handle{ID: id, Label: inst.Profile.Name, Kind: "background"})
	if err != nil {
		update(agentpool.StateCancelled)
		return ctx, nil, false
	}
	update(agentpool.StateRunning)
	return lease.Context(), lease, true
}

// releaseLease releases a background-agent turn's slot and emits the terminal
// roster delta. A cancelled lease context forces the cancelled state.
func (m *Manager) releaseLease(inst *AgentInstance, lease *agentpool.Lease) {
	if lease == nil {
		return
	}
	state := agentpool.StateDone
	if lease.Context().Err() != nil {
		state = agentpool.StateCancelled
	}
	lease.Done(state, "")
	id := "bg:" + inst.Profile.Name
	inst.pushEvent(Event{
		AgentName: inst.Profile.Name,
		Kind:      agent.EventSubagentKind,
		Subagent:  &agent.SubagentUpdate{ID: id, Label: inst.Profile.Name, Kind: "background", State: string(state)},
	})
}

func (m *Manager) executeTurn(ctx context.Context, inst *AgentInstance) {
	ctx = calltrace.WithSession(ctx, m.sessionID())
	sess, err := m.buildSession(inst)
	if err != nil {
		inst.pushEvent(Event{AgentName: inst.Profile.Name, Kind: agent.EventErrorKind, Err: err})
		return
	}
	// Reset before the turn. lastOutput is only *assigned* on EventDoneKind; a
	// turn that ends in EventErrorKind never reaches that assignment, so
	// without this the previous turn's reply is carried forward and appended to
	// History a second time. A bounded loop hid it; a restarting one compounds
	// it every pass.
	inst.mu.Lock()
	inst.lastOutput = ""
	inst.mu.Unlock()

	history := append([]run.Turn{}, inst.History...)
	promptText := inst.Profile.SystemPrompt
	if inst.Profile.Reflection {
		// Reflection requests a thinking preamble on every loop turn so the
		// agent's reasoning is visible before it acts.
		promptText = "Before acting, emit a <thinking> block with your reasoning, then proceed.\n\n" + promptText
	}
	in := agent.TurnInput{Prompt: promptText}
	inst.mu.Lock()
	task := inst.task
	inst.task = Task{}
	inst.mu.Unlock()
	if task.Prompt != "" {
		in.Prompt += "\n\nTask:\n" + task.Prompt
	}
	if len(task.Attachments) > 0 {
		in.Attachments = task.Attachments
		in.HasReferences = true
	}
	ch := sess.RunStream(ctx, history, in)
	for e := range ch {
		inst.pushEvent(m.wrapEvent(inst.Profile.Name, e))
		if e.Kind == agent.EventDoneKind {
			inst.mu.Lock()
			inst.lastOutput = e.Result.Reply
			inst.mu.Unlock()
		}
		if e.Kind == agent.EventTextKind {
			inst.mu.Lock()
			inst.lastOutput += e.Text
			inst.mu.Unlock()
		}
	}
	inst.mu.Lock()
	inst.History = append(inst.History, run.Turn{Role: "user", Content: inst.Profile.SystemPrompt})
	if inst.lastOutput != "" {
		inst.History = append(inst.History, run.Turn{Role: "assistant", Content: inst.lastOutput})
	}
	inst.mu.Unlock()
}

func (m *Manager) buildSession(inst *AgentInstance) (*agent.Session, error) {
	workdir := inst.workdir
	if workdir == "" {
		workdir = m.workdir
	}
	profile := inst.Profile
	caps := tools.DetectDefault()
	ix := repoindex.Scan(context.Background(), workdir)
	reg := tools.DefaultWithCaps(workdir, m.settings.ReadOnlyEnabled(), caps, ix)
	if len(profile.Tools) > 0 {
		reg = reg.Only(profile.Tools...)
	}
	perms := permissions.From(m.settings.Permissions.Allow, m.settings.Permissions.Ask, m.settings.Permissions.Deny)
	var promptOpts prompt.Options
	if m.settings.Caveman != nil && *m.settings.Caveman {
		promptOpts.Caveman = true
	}

	base := m.posture
	if profile.Guardrails != nil {
		if *profile.Guardrails {
			base = posture.Defaults()
		} else {
			base = posture.AllIgnore()
		}
	}
	pol := choosePosture(base, workdir)
	live := inst.live
	if live == nil {
		live = posture.NewLive(pol, true)
	}

	cfg, err := run.ApplyProfileOverride(m.cfg, run.ProfileOverride{
		Provider: profile.Provider,
		Model:    profile.Model,
		Effort:   profile.Effort,
	}, m.settings.Classifier, m.src)
	if err != nil {
		return nil, err
	}

	return agent.NewSession(agent.Options{
		Cfg:           cfg,
		Client:        m.client,
		Registry:      reg,
		Perms:         perms,
		Live:          live,
		Workdir:       workdir,
		Settings:      m.settings,
		PromptOptions: promptOpts,
		MaxIterations: sessionRounds(profile),
		Caps:          caps,
		RepoIndex:     ix,
		// The plan-mode surface is frozen for this session: GuardrailsOff reads
		// the persisted setting, which the TUI's guardrails override does not
		// touch. Re-deriving it mid-turn would break the advertisement/
		// enforcement agreement, so it stays as-built; the wider surface lands
		// on the next turn through SetPosture + a fresh session.
		PlanSurface: tools.PlanSurface{GuardrailsOff: !m.settings.GuardrailsEnabled(), Perms: perms},
		// Background agents run unattended. Live servers are off here; fallback
		// syntax checks still honour the user's settings.
		Diagnostics: rolemanager.DiagnosticsGateFromSettings(m.settings, reg.Cwd().Roots(), false),
	})
}

// sessionRounds is the tool-round budget of one background turn. A looping
// mode spends max_iterations on turns, one round each, so its turns stay one
// round. A single run has one turn, so max_iterations is that turn's round
// budget: a one-round triage cannot read a manifest, grep for its importers
// and answer.
func sessionRounds(p agentprofile.AgentProfile) int {
	if p.Mode == agentprofile.ModeSingle && p.MaxIterations > 1 {
		return p.MaxIterations
	}
	return 1
}

// choosePosture returns the stricter of the manager's current posture and the
// project posture of the target workdir. Stricter means Enforce > Warn >
// Ignore.
func choosePosture(managerPosture posture.Policy, workdir string) posture.Policy {
	proj, err := posture.Load(workdir)
	if err != nil {
		return managerPosture
	}
	out := make(posture.Policy, len(posture.AllGates))
	for _, g := range posture.AllGates {
		out[g] = stricterLevel(managerPosture.Level(g), proj.Level(g))
	}
	return out
}

func stricterLevel(a, b posture.Level) posture.Level {
	if a == posture.Enforce || b == posture.Enforce {
		return posture.Enforce
	}
	if a == posture.Warn || b == posture.Warn {
		return posture.Warn
	}
	return posture.Ignore
}

func (m *Manager) wrapEvent(name string, e agent.Event) Event {
	out := Event{AgentName: name, Kind: e.Kind, Text: e.Text, ToolName: e.ToolName, ToolResult: e.ToolResult, ToolArgs: e.ToolArgs, ToolCallID: e.ToolCallID, Warning: e.Warning, Err: e.Err, Subagent: e.Subagent, SubagentID: e.SubagentID}
	// A tool start carries its call in Tool, not in the flat fields.
	if e.Tool != nil {
		out.ToolName = e.Tool.Name
		out.ToolCallID = e.Tool.ID
		out.ToolArgs = e.Tool.RawArgs
		if out.ToolArgs == "" && len(e.Tool.Args) > 0 {
			if b, err := json.Marshal(e.Tool.Args); err == nil {
				out.ToolArgs = string(b)
			}
		}
	}
	return out
}

func (m *Manager) evaluateMonitor(ctx context.Context, condition string) (bool, error) {
	prompt := fmt.Sprintf("You are a trigger monitor. Given the condition %q, should the agent act now? Reply with exactly one word: YES or NO.", condition)
	reply, err := run.Run(calltrace.WithSession(ctx, m.sessionID()), m.cfg, prompt, m.client)
	if err != nil {
		return false, err
	}
	return strings.HasPrefix(strings.ToUpper(strings.TrimSpace(reply)), "YES"), nil
}

func parseSchedule(s string) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Minute
	}
	if d, err := time.ParseDuration(s); err == nil {
		if d <= 0 {
			return time.Minute
		}
		return d
	}
	var mins int
	if _, err := fmt.Sscanf(s, "%d", &mins); err == nil && mins > 0 {
		return time.Duration(mins) * time.Minute
	}
	return time.Minute
}

// LastOutput returns the agent's last output safely.
func (inst *AgentInstance) LastOutput() string {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	return inst.lastOutput
}

func (inst *AgentInstance) pushEvent(e Event) {
	select {
	case inst.Events <- e:
	default:
	}
}
