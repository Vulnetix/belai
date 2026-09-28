package rc

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/vulnetix/belai/internal/agent"
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
}

// RunSession runs the dispatched prompt, then every web prompt addressed to
// this session, one at a time, until ctx ends (a stop from the website, or
// the daemon shutting down) or it has been idle for Idle. Asks never happen:
// the session was built with asks off.
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
		// slash command or a shell command on the host.
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
			done <- runTurn(ctx, o, history, text)
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

// runTurn runs one turn between its turn_state lines and returns the
// assistant turn for the history ("" Role when it failed).
func runTurn(ctx context.Context, o SessionOptions, history []run.Turn, text string) run.Turn {
	o.Log.TurnStarted(o.Facts)
	o.Mirror.Nudge()
	emit := func(e agent.Event) {
		o.Log.Observe(e)
		o.Mirror.Nudge()
	}
	res, err := o.Agent.RunInputObserved(ctx, history, agent.TurnInput{Prompt: text, ForceMode: o.Mode}, emit)
	state := "ended"
	switch {
	case ctx.Err() != nil:
		state = "interrupted"
	case err != nil:
		state = "error"
		o.Log.System("turn failed: " + err.Error())
		fmt.Fprintf(o.Out, "rc-session: turn failed: %v\n", err)
	}
	o.Log.TurnEnded(state, o.Facts)
	o.Mirror.Nudge()
	if err != nil {
		return run.Turn{}
	}
	return run.Turn{Role: "assistant", Content: res.Reply}
}
