package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sessionctl"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// Session controls for a fleet worker (`belai rc --web-controls`, passed to the
// workers it starts). A worker takes the controls that change how its next turn
// runs: the model, the effort, guardrails and caveman. Each is parsed by
// internal/sessionctl against its fixed table, exactly as for a remote session,
// and applies from the worker's next turn; nothing is written to a settings
// file. The rest are refused with a reason: a worker has nobody to ask, its
// mode is its profile's, and it has no display.

// WorkerControlIDs are the controls a fleet worker takes.
var WorkerControlIDs = []string{sessionctl.CtlModel, sessionctl.CtlEffort, sessionctl.CtlGuardrails, sessionctl.CtlCaveman}

// ErrNotForWorker refuses a control a fleet worker does not take.
var ErrNotForWorker = errors.New("a fleet worker takes only /model, /effort, /guardrails and /caveman")

// WorkerControls holds a worker's controls and the run config they resolve to.
type WorkerControls struct {
	// Env says what a control may name on this host.
	Env sessionctl.Env
	// Resolve builds the run config for a state whose provider or model
	// changed; it is called when the control is applied, so a model this host
	// cannot run is refused then rather than failing the next turn.
	Resolve func(sessionctl.State) (run.Config, error)

	mu    sync.Mutex
	st    sessionctl.State
	cfg   run.Config
	moved bool // the model was changed from the one the worker started on
}

// NewWorkerControls starts from the worker's own settings and run config.
func NewWorkerControls(settings config.Settings, cfg run.Config, env sessionctl.Env, resolve func(sessionctl.State) (run.Config, error)) *WorkerControls {
	return &WorkerControls{
		Env: env, Resolve: resolve,
		st:  sessionctl.FromSettings(settings, "agent", cfg.Provider, cfg.Model, settings.Effort),
		cfg: cfg,
	}
}

// State is a copy of the current controls.
func (c *WorkerControls) State() sessionctl.State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.st.Clone()
}

// StateJSON is the current controls as the website reads them.
func (c *WorkerControls) StateJSON() json.RawMessage {
	b, _ := json.Marshal(c.State())
	return b
}

// Apply parses one web command against the worker's controls and keeps the
// result for the next turn. It returns the harness-composed summary.
func (c *WorkerControls) Apply(cmd sessionsync.RemoteCommand) (string, error) {
	cur := c.State()
	var ch sessionctl.Change
	var err error
	switch {
	case cmd.Line != "" && cmd.Key == "":
		ch, err = sessionctl.Parse(cmd.Line, cur, c.Env)
	case cmd.Key != "" && cmd.Line == "":
		ch, err = sessionctl.ParseKey(cmd.Key, cur, c.Env)
	default:
		return "", errors.New("a control is one slash command or one key")
	}
	if errors.Is(err, sessionctl.ErrUnknown) {
		return "", errors.New("that is not a session control this host takes")
	}
	if err != nil {
		return "", err
	}
	if !slices.Contains(WorkerControlIDs, ch.Control) {
		return "", ErrNotForWorker
	}
	c.mu.Lock()
	cfg := c.cfg
	c.mu.Unlock()
	if ch.State.Provider != cur.Provider || ch.State.Model != cur.Model {
		if c.Resolve == nil {
			return "", errors.New("this worker cannot change its model")
		}
		nc, err := c.Resolve(ch.State)
		if err != nil {
			return "", errors.New("the worker cannot run that model: " + err.Error())
		}
		cfg = nc
	}
	c.mu.Lock()
	c.st, c.cfg = ch.State, cfg
	c.moved = c.moved || ch.State.Provider != cur.Provider || ch.State.Model != cur.Model
	c.mu.Unlock()
	return ch.Summary + " (from the next turn)", nil
}

// Turn is what the next turn runs on: the settings with the controls overlaid,
// the run config for the chosen model, and the posture (every rule ignored
// when guardrails are off, which the host allowed).
func (c *WorkerControls) Turn(settings config.Settings, cfg run.Config, pol posture.Policy) (config.Settings, run.Config, posture.Policy) {
	c.mu.Lock()
	st, rc, moved := c.st.Clone(), c.cfg, c.moved
	c.mu.Unlock()
	s := st.Apply(settings)
	if moved {
		// A model picked on the web outranks the routing table.
		s.Routing = nil
		cfg = rc
	}
	if st.Effort != "" {
		cfg.Effort = st.Effort
	}
	if !s.GuardrailsEnabled() {
		pol = posture.AllIgnore()
	}
	return s, cfg, pol
}

// Facts are the controls a worker's turn facts carry.
func (c *WorkerControls) Facts() map[string]any {
	st := c.State()
	return map[string]any{"provider": st.Provider, "model": st.Model, "effort": st.Effort, "guardrails": st.Guardrails, "caveman": st.Caveman}
}

// controlMirror is what the worker's session mirror does for controls.
type controlMirror interface {
	Commands() <-chan sessionsync.RemoteCommand
	AckCommand(commandID, status, reason string, state json.RawMessage)
	SetSessionControls(state json.RawMessage, remoteAnswers bool)
}

// runControls applies web controls until ctx ends: each is acked with the new
// state, or refused with the reason, and the session's controls re-registered
// so the website shows them. An applied control is a line in the worker's log.
func (w *Worker) runControls(ctx context.Context, m controlMirror) {
	ch := m.Commands()
	for {
		select {
		case <-ctx.Done():
			return
		case cmd, ok := <-ch:
			if !ok {
				return
			}
			summary, err := w.Controls.Apply(cmd)
			state := w.Controls.StateJSON()
			if err != nil {
				m.AckCommand(cmd.ID, sessionsync.AckRefused, err.Error(), state)
				continue
			}
			w.logf("web: %s", summary)
			m.AckCommand(cmd.ID, sessionsync.AckAccepted, "", state)
			// A worker never takes web answers: nobody waits on its asks.
			m.SetSessionControls(state, false)
		}
	}
}
