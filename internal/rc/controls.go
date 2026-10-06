package rc

import (
	"encoding/json"
	"errors"
	"sync"

	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/sessionctl"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// Session controls from the web (`belai rc --web-controls`). A command the
// website sends is a slash line or a key, parsed by internal/sessionctl
// against its fixed table; nothing else is ever run. A control that only
// changes the next turn (the mode) or the display applies at once. One that
// changes how the agent session is built (the model, guardrails, ask, caveman,
// language servers, Jev) rebuilds it: at once between turns, or at the next
// turn boundary while a turn runs, keeping the conversation. Every change is
// for this session only and is never written to a settings file.

// Controller holds a remote session's controls and its current agent session.
type Controller struct {
	env   sessionctl.Env
	build func(sessionctl.State) (Runner, error)

	mu      sync.Mutex
	st      sessionctl.State
	built   sessionctl.State // the state the runner was built from
	runner  Runner
	pending bool // a rebuild waits for the turn boundary
}

// NewController builds the first agent session from st. env says what a
// control may name on this host; build makes an agent session for a state.
func NewController(st sessionctl.State, env sessionctl.Env, build func(sessionctl.State) (Runner, error)) (*Controller, error) {
	r, err := build(st)
	if err != nil {
		return nil, err
	}
	return &Controller{env: env, build: build, st: st, built: st.Clone(), runner: r}, nil
}

// Runner is the agent session the next turn runs on.
func (c *Controller) Runner() Runner {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.runner
}

// State is a copy of the current controls.
func (c *Controller) State() sessionctl.State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.st.Clone()
}

// StateJSON is the current controls as the website reads them.
func (c *Controller) StateJSON() json.RawMessage {
	b, _ := json.Marshal(c.State())
	return b
}

// Mode is the mode the next turn is forced into; "" lets the classifier pick.
func (c *Controller) Mode() modes.Mode {
	if m := c.State().Mode; m != "auto" {
		return modes.Mode(m)
	}
	return ""
}

// Asks reports whether permission asks go to the web.
func (c *Controller) Asks() bool { return c.State().Ask }

// Apply parses and applies one web command. idle says no turn is running, so
// a rebuild can happen now. It returns the harness-composed summary.
func (c *Controller) Apply(cmd sessionsync.RemoteCommand, idle bool) (string, error) {
	c.mu.Lock()
	cur := c.st.Clone()
	c.mu.Unlock()
	var ch sessionctl.Change
	var err error
	switch {
	case cmd.Line != "" && cmd.Key == "":
		ch, err = sessionctl.Parse(cmd.Line, cur, c.env)
	case cmd.Key != "" && cmd.Line == "":
		ch, err = sessionctl.ParseKey(cmd.Key, cur, c.env)
	default:
		return "", errors.New("a control is one slash command or one key")
	}
	if errors.Is(err, sessionctl.ErrUnknown) {
		return "", errors.New("that is not a session control this host takes")
	}
	if err != nil {
		return "", err
	}
	if !ch.Rebuild {
		c.mu.Lock()
		c.st = ch.State
		c.mu.Unlock()
		return ch.Summary, nil
	}
	if !idle {
		c.mu.Lock()
		c.st = ch.State
		c.pending = true
		c.mu.Unlock()
		return ch.Summary + " (from the next turn)", nil
	}
	r, err := c.build(ch.State)
	if err != nil {
		return "", errors.New("the session could not be rebuilt with that change: " + err.Error())
	}
	c.mu.Lock()
	c.st, c.built, c.runner, c.pending = ch.State, ch.State.Clone(), r, false
	c.mu.Unlock()
	return ch.Summary, nil
}

// Settle applies a rebuild deferred while a turn ran. On failure the session
// keeps its last good agent and the controls that built it are restored.
func (c *Controller) Settle() error {
	c.mu.Lock()
	if !c.pending {
		c.mu.Unlock()
		return nil
	}
	st := c.st.Clone()
	c.pending = false
	c.mu.Unlock()
	r, err := c.build(st)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		// Keep the display and mode the person chose; put back what the
		// running agent was built from.
		keep := c.st
		c.st = c.built.Clone()
		c.st.Mode, c.st.Reasoning, c.st.Tools, c.st.Decisions = keep.Mode, keep.Reasoning, keep.Tools, keep.Decisions
		c.st.AutoCommit, c.st.TestsPostEnd, c.st.TestsOnFail = keep.AutoCommit, keep.TestsPostEnd, keep.TestsOnFail
		return err
	}
	c.runner, c.built = r, st
	return nil
}

// Facts are the footer facts the turn_state lines carry.
func (c *Controller) Facts() map[string]any {
	st := c.State()
	return map[string]any{
		"mode": st.Mode, "provider": st.Provider, "model": st.Model, "effort": st.Effort,
		"guardrails": st.Guardrails, "ask": st.Ask, "caveman": st.Caveman,
	}
}
