package rc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"time"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/clarify"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/turnlog"
)

// Runner is the part of agent.Session a remote session drives.
type Runner interface {
	RunInputObserved(ctx context.Context, history []run.Turn, in agent.TurnInput, emit func(agent.Event)) (run.Result, error)
}

// Mirror is the part of sessionsync.Syncer a remote session drives.
type Mirror interface {
	Nudge()
	Prompts() <-chan sessionsync.RemotePrompt
	Ack(id, status, reason, entryID string)
	RemotePromptsEnabled() bool
}

// SessionOptions configure one remote session (`belai rc-session`).
type SessionOptions struct {
	Agent    Runner
	Log      *turnlog.Log
	Mirror   Mirror
	Dispatch string
	Prompt   string
	Mode     modes.Mode
	// Idle ends the session when no prompt has arrived for this long after
	// its last turn.
	Idle time.Duration
	// Facts are the footer facts written on every turn_state line.
	Facts map[string]any
	Out   io.Writer

	// Controls, when set, is the session's web controls (belai rc
	// --web-controls): the agent, the mode and the facts come from it, and
	// Commands are applied to it. Nil runs Agent with Mode, as before.
	Controls *Controller
	// Commands are session controls claimed from the inbox; AckCommand reports
	// each one's outcome with the new state.
	Commands   <-chan sessionsync.RemoteCommand
	AckCommand func(id, status, reason string, state json.RawMessage)
	// Changed is called after the controls changed, with the new state (the
	// syncer re-registers the session with it).
	Changed func(json.RawMessage, bool)
	// Answers, when set, answers asks from the web while the controls have ask
	// on. Without it every ask is denied, as nobody can answer it.
	Answers AnswerMirror
	// AskWait bounds the wait for one web answer (DefaultAskWait).
	AskWait time.Duration
	// AfterTurn runs after each turn that ended cleanly, with the prompt, the
	// turn's result and the paths its tools changed: the post-end test pass
	// and auto-commit, each when its control is on.
	AfterTurn func(ctx context.Context, prompt string, res run.Result, paths []string)
}

// RunSession runs the dispatched prompt, then every web prompt addressed to
// this session, one at a time, until ctx ends (a stop from the website, or
// the daemon shutting down) or it has been idle for Idle. Without Controls
// asks never happen: the session was built with asks off.
func RunSession(ctx context.Context, o SessionOptions) error {
	if o.Idle <= 0 {
		o.Idle = DefaultIdle
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	var history []run.Turn
	var prompts <-chan sessionsync.RemotePrompt
	if o.Mirror.RemotePromptsEnabled() {
		prompts = o.Mirror.Prompts()
	}
	// The dispatched prompt goes first; its request id is the dispatch, not
	// a web prompt, so it is never acked.
	queue := []sessionsync.RemotePrompt{{Content: o.Prompt}}
	idle := time.NewTimer(o.Idle)
	defer idle.Stop()
	var bridge *askBridge
	if o.Answers != nil {
		bridge = newAskBridge(o.Log, o.Answers, o.AskWait)
	}
	commands := o.Commands
	if o.Controls == nil {
		commands = nil
	}
	if o.Controls != nil && o.Changed != nil {
		st := o.Controls.State()
		o.Changed(o.Controls.StateJSON(), st.Ask && bridge != nil)
	}

	for {
		if ctx.Err() != nil {
			return nil
		}
		var p sessionsync.RemotePrompt
		if len(queue) > 0 {
			p, queue = queue[0], queue[1:]
		} else {
			select {
			case <-ctx.Done():
				return nil
			case <-idle.C:
				o.Log.System(fmt.Sprintf("remote session ended after %s without a prompt", o.Idle))
				o.Mirror.Nudge()
				return nil
			case q, ok := <-prompts:
				if !ok {
					prompts = nil
					continue
				}
				p = q
			case c, ok := <-commands:
				if !ok {
					commands = nil
					continue
				}
				applyCommand(o, c, true, bridge)
				continue
			}
		}
		text := sessionsync.CleanPrompt(p.Content)
		if text == "" {
			if p.ID != "" {
				o.Mirror.Ack(p.ID, sessionsync.AckRefused, "the prompt was empty after cleaning", "")
			}
			continue
		}
		// A leading "/" or "!" is plain prompt text: the website never runs a
		// slash command or a shell command on the host. Session controls arrive
		// on their own channel (Commands), typed and parsed by sessionctl.
		meta := map[string]any{"source": "web", "dispatch_id": o.Dispatch}
		if p.ID != "" {
			meta = map[string]any{"source": "web", "remote_prompt_id": p.ID}
		}
		id := o.Log.User(text, meta)
		if p.ID != "" {
			o.Mirror.Ack(p.ID, sessionsync.AckAccepted, "", id)
		}
		o.Mirror.Nudge()

		done := make(chan run.Turn, 1)
		go func() {
			done <- runTurn(ctx, o, history, text, bridge)
		}()
		// While the turn runs, later prompts wait their turn (FIFO) and the
		// website shows them queued.
		var reply run.Turn
	wait:
		for {
			select {
			case reply = <-done:
				break wait
			case q, ok := <-prompts:
				if !ok {
					prompts = nil
					continue
				}
				queue = append(queue, q)
				o.Mirror.Ack(q.ID, sessionsync.AckQueued, "", "")
			case c, ok := <-commands:
				if !ok {
					commands = nil
					continue
				}
				applyCommand(o, c, false, bridge)
			}
		}
		if o.Controls != nil {
			if err := o.Controls.Settle(); err != nil {
				o.Log.System("web: the change could not be applied, the session keeps its previous setup: " + err.Error())
				o.Mirror.Nudge()
			}
			if o.Changed != nil {
				o.Changed(o.Controls.StateJSON(), o.Controls.Asks() && bridge != nil)
			}
		}
		if reply.Role != "" {
			history = append(history, run.Turn{Role: "user", Content: text}, reply)
		}
		if !idle.Stop() {
			select {
			case <-idle.C:
			default:
			}
		}
		idle.Reset(o.Idle)
	}
}

// maxTurnPaths caps the changed paths one turn collects for auto-commit.
const maxTurnPaths = 1000

// applyCommand applies one web control and reports it: a harness line in the
// transcript, the ack with the new state, and the re-registration.
func applyCommand(o SessionOptions, c sessionsync.RemoteCommand, idle bool, bridge *askBridge) {
	summary, err := o.Controls.Apply(c, idle)
	if err != nil {
		if o.AckCommand != nil {
			o.AckCommand(c.ID, sessionsync.AckRefused, err.Error(), o.Controls.StateJSON())
		}
		return
	}
	o.Log.System("web: " + summary)
	o.Mirror.Nudge()
	state := o.Controls.StateJSON()
	if o.AckCommand != nil {
		o.AckCommand(c.ID, sessionsync.AckAccepted, "", state)
	}
	if o.Changed != nil {
		o.Changed(state, o.Controls.Asks() && bridge != nil)
	}
	// Ask turned off: an ask raised while it was on can no longer be answered
	// (web answers went off with it), so it resolves now rather than holding
	// the turn for the whole wait.
	if bridge != nil && !o.Controls.Asks() {
		bridge.releaseOpen()
	}
}

// turnFacts are the footer facts for a turn: the session's own, overlaid by
// the controls when the web can change them.
func turnFacts(o SessionOptions) map[string]any {
	if o.Controls == nil {
		return o.Facts
	}
	f := maps.Clone(o.Facts)
	if f == nil {
		f = map[string]any{}
	}
	maps.Copy(f, o.Controls.Facts())
	return f
}

// runTurn runs one turn between its turn_state lines and returns the
// assistant turn for the history ("" Role when it failed).
func runTurn(ctx context.Context, o SessionOptions, history []run.Turn, text string, bridge *askBridge) run.Turn {
	facts := turnFacts(o)
	agentRunner, mode := o.Agent, o.Mode
	asks := false
	if o.Controls != nil {
		agentRunner, mode, asks = o.Controls.Runner(), o.Controls.Mode(), o.Controls.Asks()
	}
	o.Log.TurnStarted(facts)
	o.Mirror.Nudge()
	var paths []string
	seen := map[string]bool{}
	emit := func(e agent.Event) {
		switch e.Kind {
		case agent.EventToolDiffKind:
			// The changed paths, for auto-commit. Render-only facts: the diff
			// itself never reaches a model.
			if e.Diff != nil {
				for _, f := range e.Diff.Files {
					p := filepath.ToSlash(f.Path)
					if p != "" && !seen[p] && len(paths) < maxTurnPaths {
						seen[p] = true
						paths = append(paths, p)
					}
				}
			}
		case agent.EventPermissionAskKind:
			// Only an explicit web allow lets the call run; with nobody to
			// ask, it is denied.
			if e.AskReply != nil {
				allow := false
				switch {
				case o.Controls != nil && !o.Controls.Asks():
					// Ask was turned off while this turn ran, after the agent
					// session was built with asks on. Ask off resolves every ask
					// to allow (the posture's AskDisabled), so this one does too.
					allow = true
				case asks && bridge != nil:
					allow = bridge.permission(ctx, e.Ask)
				}
				select {
				case e.AskReply <- agent.PermissionAskReply{Allow: allow}:
				case <-ctx.Done():
				}
			}
			return
		case agent.EventClarifyAskKind:
			if e.Reply != nil {
				var answers clarify.Answers
				if asks && bridge != nil && (o.Controls == nil || o.Controls.Asks()) {
					answers = bridge.clarify(ctx, e.Clarify, e.ModeChoice)
				}
				select {
				case e.Reply <- answers:
				case <-ctx.Done():
				}
			}
			return
		}
		o.Log.Observe(e)
		o.Mirror.Nudge()
	}
	res, err := agentRunner.RunInputObserved(ctx, history, agent.TurnInput{Prompt: text, ForceMode: mode}, emit)
	state := "ended"
	switch {
	case ctx.Err() != nil:
		state = "interrupted"
	case err != nil:
		state = "error"
		o.Log.System("turn failed: " + err.Error())
		fmt.Fprintf(o.Out, "rc-session: turn failed: %v\n", err)
	}
	o.Log.TurnEnded(state, facts)
	o.Mirror.Nudge()
	if err != nil {
		return run.Turn{}
	}
	if o.AfterTurn != nil && ctx.Err() == nil {
		o.AfterTurn(ctx, text, res, paths)
	}
	return run.Turn{Role: "assistant", Content: res.Reply}
}
