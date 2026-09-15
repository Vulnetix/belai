package bgagent

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/signet/internal/agent"
	"github.com/vulnetix/signet/internal/agentprofile"
	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/permissions"
	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/prompt"
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
	Err        error
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
	mu         sync.Mutex
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

// Start launches a background agent by name.
func (m *Manager) Start(name string, profile agentprofile.AgentProfile) error {
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
		inst.State = StateDone
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
	m.executeTurn(ctx, inst)
}

func (m *Manager) runLoopMode(ctx context.Context, inst *AgentInstance) {
	maxIter := inst.Profile.MaxIterations
	if maxIter <= 0 {
		maxIter = m.settings.Resilience.MaxIterationsOr(10)
	}
	for i := 0; i < maxIter; i++ {
		select {
		case <-ctx.Done():
			return
		default:
		}
		inst.mu.Lock()
		inst.State = StateRunning
		inst.iteration = i + 1
		inst.mu.Unlock()
		m.executeTurn(ctx, inst)
		select {
		case <-ctx.Done():
			return
		default:
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

func (m *Manager) executeTurn(ctx context.Context, inst *AgentInstance) {
	sess, err := m.buildSession(inst.Profile)
	if err != nil {
		inst.pushEvent(Event{AgentName: inst.Profile.Name, Kind: agent.EventErrorKind, Err: err})
		return
	}
	history := append([]run.Turn{}, inst.History...)
	in := agent.TurnInput{Prompt: inst.Profile.SystemPrompt}
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

func (m *Manager) buildSession(profile agentprofile.AgentProfile) (*agent.Session, error) {
	reg := tools.Default(m.workdir, m.settings.BashReadOnlyEnabled())
	if len(profile.Tools) > 0 {
		var filtered []tools.Tool
		for _, name := range profile.Tools {
			if t, ok := reg.Find(name); ok {
				filtered = append(filtered, t)
			}
		}
		reg = tools.NewRegistry(filtered...)
	}
	perms := permissions.From(m.settings.Permissions.Allow, m.settings.Permissions.Ask, m.settings.Permissions.Deny)
	var promptOpts prompt.Options
	if m.settings.Caveman != nil && *m.settings.Caveman {
		promptOpts.Caveman = true
	}
	return agent.NewSession(agent.Options{
		Cfg:           m.cfg,
		Client:        m.client,
		Registry:      reg,
		Perms:         perms,
		Posture:       m.posture,
		Workdir:       m.workdir,
		Settings:      m.settings,
		PromptOptions: promptOpts,
		MaxIterations: 1,
	})
}

func (m *Manager) wrapEvent(name string, e agent.Event) Event {
	return Event{AgentName: name, Kind: e.Kind, Text: e.Text, ToolName: e.ToolName, ToolResult: e.ToolResult, Err: e.Err}
}

func (m *Manager) evaluateMonitor(ctx context.Context, condition string) (bool, error) {
	prompt := fmt.Sprintf("You are a trigger monitor. Given the condition %q, should the agent act now? Reply with exactly one word: YES or NO.", condition)
	reply, err := run.Run(ctx, m.cfg, prompt, m.client)
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
