package rc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/clarify"
	"github.com/vulnetix/belai/internal/commandlib"
	"github.com/vulnetix/belai/internal/libitem"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/turnlog"
	"github.com/vulnetix/belai/internal/webask"
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
	// Profile is the agent profile engaged for agent-mode turns (a start
	// request's choice, validated by the daemon and again by the child).
	Profile string
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
	// Shell, when set, runs the shell lines the website sends (belai rc
	// --web-shell). The lines arrive on Commands, so Commands must be set too.
	Shell ShellRunner
	// Changed is called after the controls changed, with the new state (the
	// syncer re-registers the session with it).
	Changed func(json.RawMessage, bool)
	// Answers, when set, answers asks from the web while the controls have ask
	// on. Without it every ask is denied, as nobody can answer it.
	Answers AnswerMirror
	// AskWait bounds the wait for one web answer (DefaultAskWait).
	AskWait time.Duration
	// Workdir is the session's directory, where its project commands directory is
	// read, and SlashCommands says sync.commands is on: a web session may invoke a
	// custom slash command by name only then (RemoteCommand.Command).
	Workdir       string
	SlashCommands bool
	// Executor, when set, builds the agent session that carries out an approved
	// plan in a session without web controls. Such a session has asks off and no
	// terminal, so its own agent would refuse every edit the plan needs; the
	// executor's session resolves asks to allow because the person approved the
	// plan. Deny rules, the posture and the sandbox still apply to it. With web
	// controls the controlled session runs the plan and asks the web as usual.
	Executor func() (Runner, error)
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
	// Commands carry web controls and, with a Shell runner, shell lines.
	commands := o.Commands
	if o.Controls == nil && o.Shell == nil {
		commands = nil
	}
	shells := &shellQueue{}
	// A failed composer line raises a turn of its own, so the model reads the
	// error and offers a fix. Buffered: a burst of failures never blocks the
	// shell goroutine, and one past the buffer is dropped (its output is still
	// attached to the next turn).
	analysis := make(chan sessionsync.RemotePrompt, 8)
	analyse := func(p sessionsync.RemotePrompt) {
		select {
		case analysis <- p:
		default:
		}
	}
	// enqueue puts a prompt the host expanded itself (a custom slash command) in
	// line behind the running turn; false when the line is full.
	enqueue := func(p sessionsync.RemotePrompt) bool {
		select {
		case analysis <- p:
			return true
		default:
			return false
		}
	}
	resetIdle := func() {
		if !idle.Stop() {
			select {
			case <-idle.C:
			default:
			}
		}
		idle.Reset(o.Idle)
	}
	if o.Controls != nil && o.Changed != nil {
		st := o.Controls.State()
		o.Changed(o.Controls.StateJSON(), st.Ask && bridge != nil)
	}

	// open is the plan review the web has not answered yet. It outlives the
	// turn that wrote it and is answered while the session is idle.
	var open *planReview
	for {
		if ctx.Err() != nil {
			bridge.closePlanReview(open, "the session stopped")
			return nil
		}
		var p sessionsync.RemotePrompt
		var tp turnPlan
		var planAnswers <-chan sessionsync.RemoteAnswer
		if bridge != nil && open != nil {
			planAnswers = o.Answers.Answers()
		}
		if len(queue) > 0 {
			p, queue = queue[0], queue[1:]
		} else {
			select {
			case <-ctx.Done():
				bridge.closePlanReview(open, "the session stopped")
				return nil
			case <-idle.C:
				bridge.closePlanReview(open, "the session ended without an answer")
				o.Log.System(fmt.Sprintf("remote session ended after %s without a prompt", o.Idle))
				o.Mirror.Nudge()
				return nil
			case ans, ok := <-planAnswers:
				if !ok {
					planAnswers = nil
					continue
				}
				choice, notes, accepted := bridge.answerPlanReview(open, ans)
				if !accepted {
					continue
				}
				review := open
				open = nil
				resetIdle()
				switch choice {
				case webask.PlanApproveHere:
					// Approval leaves plan mode, as it does in the TUI: the
					// session's mode follows, so later prompts are not planned.
					if o.Controls != nil {
						if summary, err := o.Controls.Apply(sessionsync.RemoteCommand{Line: "/mode agent"}, true); err != nil {
							o.Log.System("web: the mode could not be set to agent: " + err.Error())
						} else {
							o.Log.System("web: " + summary)
						}
					}
					o.Log.System("plan approved: " + review.path + " — executing")
					tp = turnPlan{execute: review.name}
					p = sessionsync.RemotePrompt{Content: executePlanPrompt, Origin: "plan_review"}
				case webask.PlanRefine:
					tp = turnPlan{refine: true}
					p = sessionsync.RemotePrompt{Content: notes, Origin: "plan_review"}
				default:
					// Leave it: the plan file stays and the session stays in plan mode.
					o.Mirror.Nudge()
					continue
				}
			case q, ok := <-prompts:
				if !ok {
					prompts = nil
					continue
				}
				p = q
			case q := <-analysis:
				p = q
			case c, ok := <-commands:
				if !ok {
					commands = nil
					continue
				}
				applyCommand(ctx, o, c, true, bridge, shells, analyse, enqueue)
				// A shell line is activity: a console left open on a sandbox
				// keeps its session alive while it is in use.
				if c.Shell != "" {
					resetIdle()
				}
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
		// A leading "/" or "!" is plain prompt text: a prompt is never a slash
		// command or a shell command. Session controls and shell lines arrive on
		// their own channel (Commands): controls typed and parsed by
		// sessionctl, shell lines checked and run by the ShellRunner.
		meta := map[string]any{"source": "web", "dispatch_id": o.Dispatch}
		if p.ID != "" {
			meta = map[string]any{"source": "web", "remote_prompt_id": p.ID}
		}
		if p.Origin != "" {
			meta = map[string]any{"source": p.Origin}
		}
		if p.Command != "" {
			meta["command"] = p.Command
		}
		id := o.Log.User(text, meta)
		if p.ID != "" {
			o.Mirror.Ack(p.ID, sessionsync.AckAccepted, "", id)
		}
		o.Mirror.Nudge()

		// Output of the composer's shell lines since the last turn rides on this
		// one, exactly once. The console's lines never get here.
		attachments := shells.drain()
		done := make(chan turnResult, 1)
		go func() {
			done <- runTurn(ctx, o, history, text, bridge, attachments, tp)
		}()
		// While the turn runs, later prompts wait their turn (FIFO) and the
		// website shows them queued.
		var reply run.Turn
	wait:
		for {
			select {
			case res := <-done:
				reply = res.turn
				if res.plan != nil {
					// The turn wrote a plan and offered it for review. An
					// older review that is still open is superseded by it.
					if open != nil && open.askID != res.plan.askID {
						bridge.closePlanReview(open, "a newer plan replaced it")
					}
					open = res.plan
				}
				break wait
			case q, ok := <-prompts:
				if !ok {
					prompts = nil
					continue
				}
				queue = append(queue, q)
				o.Mirror.Ack(q.ID, sessionsync.AckQueued, "", "")
			case q := <-analysis:
				queue = append(queue, q)
			case c, ok := <-commands:
				if !ok {
					commands = nil
					continue
				}
				applyCommand(ctx, o, c, false, bridge, shells, analyse, enqueue)
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
func applyCommand(ctx context.Context, o SessionOptions, c sessionsync.RemoteCommand, idle bool, bridge *askBridge, shells *shellQueue, analyse func(sessionsync.RemotePrompt), enqueue func(sessionsync.RemotePrompt) bool) {
	if c.Command != "" {
		runSlashCommand(o, c, enqueue)
		return
	}
	if c.Shell != "" {
		if o.Shell == nil {
			if o.AckCommand != nil {
				o.AckCommand(c.ID, sessionsync.AckRefused, "this session does not run shell lines from the web", nil)
			}
			return
		}
		startShell(ctx, o, c, shells, planMode(o), analyse)
		return
	}
	if o.Controls == nil {
		if o.AckCommand != nil {
			o.AckCommand(c.ID, sessionsync.AckRefused, "this session takes no controls from the web", nil)
		}
		return
	}
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

// runSlashCommand runs a custom slash command a web session named: the host
// expands its own installed file with the arguments and queues the result as an
// ordinary prompt, so the turn admits it exactly as it admits a typed one (the
// sanitiser and the classifier run in the turn). The page sends a name and
// argument text, never a template, and a name this host does not hold is
// refused. A leading "/" in an ordinary web prompt stays plain text.
func runSlashCommand(o SessionOptions, c sessionsync.RemoteCommand, enqueue func(sessionsync.RemotePrompt) bool) {
	refuse := func(reason string) {
		if o.AckCommand != nil {
			o.AckCommand(c.ID, sessionsync.AckRefused, reason, nil)
		}
	}
	switch {
	case c.Line != "" || c.Key != "" || c.Shell != "":
		refuse("a command is one name and its arguments")
		return
	case !o.SlashCommands:
		refuse("custom slash commands are off on this host (sync.commands is false)")
		return
	case o.Mirror == nil || !o.Mirror.RemotePromptsEnabled():
		refuse("this session takes no prompts from the web")
		return
	case !libitem.ValidName(libitem.Command, c.Command):
		refuse("that is not a command name")
		return
	}
	cmd, ok := commandlib.Load(o.Workdir).Find(c.Command)
	if !ok {
		refuse("this host has no command named " + c.Command)
		return
	}
	text, err := commandlib.Expand(cmd, sessionsync.CleanPrompt(c.Args))
	if err != nil {
		refuse(err.Error())
		return
	}
	if text = sessionsync.CleanPrompt(text); text == "" {
		refuse("the command expanded to nothing")
		return
	}
	if !enqueue(sessionsync.RemotePrompt{Content: text, Origin: "web", Command: cmd.Name}) {
		refuse("too many commands are waiting; try again in a moment")
		return
	}
	o.Log.System("web: /" + cmd.Name)
	o.Mirror.Nudge()
	var state json.RawMessage
	if o.Controls != nil {
		state = o.Controls.StateJSON()
	}
	if o.AckCommand != nil {
		o.AckCommand(c.ID, sessionsync.AckAccepted, "", state)
	}
}

// planMode reports whether the session is in plan mode right now: the web
// controls' mode when it has them, the mode it started in otherwise.
func planMode(o SessionOptions) bool {
	if o.Controls != nil {
		return o.Controls.Mode() == modes.ModePlan
	}
	return o.Mode == modes.ModePlan
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

// planRefineDirective rides a refinement turn so the planner revises the plan
// it just wrote instead of starting over, as the TUI's review pane does.
const planRefineDirective = "Revise the approved plan above, incorporating the user's notes."

// executePlanPrompt is the prompt of the turn that carries out an approved plan.
const executePlanPrompt = "Execute the approved plan."

// turnPlan says a turn follows a plan review: execute the approved plan, or
// refine it with the web's notes. The zero value is an ordinary turn.
type turnPlan struct {
	execute string // the approved plan's name
	refine  bool
}

// turnResult is what a finished turn hands the session loop: the assistant turn
// for the history ("" Role when it failed) and the plan review it opened.
type turnResult struct {
	turn run.Turn
	plan *planReview
}

// runTurn runs one turn between its turn_state lines.
func runTurn(ctx context.Context, o SessionOptions, history []run.Turn, text string, bridge *askBridge, attachments []run.Attachment, tp turnPlan) turnResult {
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
	var review *planReview
	var reviewMu sync.Mutex
	emit := func(e agent.Event) {
		switch e.Kind {
		case agent.EventPlanFileKind:
			// A plan turn ends with a plan file. When the web can answer, it
			// is offered for review (approve, refine or leave it); otherwise
			// the path is recorded so the transcript says where the plan is.
			reviewMu.Lock()
			// The review is a question about the plan, not a permission ask, so
			// it does not wait for the ask switch; it needs only a way to take
			// the answer. With web controls and ask off nothing takes one.
			if bridge != nil && (o.Controls == nil || o.Controls.Asks()) {
				if review != nil {
					bridge.closePlanReview(review, "a newer plan replaced it")
				}
				review = bridge.recordPlanReview(e.PlanName, e.PlanPath)
			}
			recorded := review != nil
			reviewMu.Unlock()
			if !recorded {
				o.Log.System("plan written: " + e.PlanPath)
			}
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
	in := agent.TurnInput{Prompt: text, Attachments: attachments, ForceMode: mode}
	if o.Profile != "" && mode == modes.ModeAgent {
		in.ForceAgent = o.Profile
	}
	if tp.execute != "" && o.Controls == nil && o.Executor != nil {
		if r, err := o.Executor(); err != nil {
			o.Log.System("web: the plan could not be executed: " + err.Error())
		} else {
			agentRunner = r
		}
	}
	switch {
	case tp.execute != "":
		// An approved plan runs autonomously on the full tool surface
		// whatever mode the session was planning in.
		in.ExecutePlan, in.PlanName, in.ForceMode = true, tp.execute, modes.ModeAgent
	case tp.refine:
		in.ForceMode, in.Directive = modes.ModePlan, planRefineDirective
	}
	res, err := agentRunner.RunInputObserved(ctx, history, in, emit)
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
	reviewMu.Lock()
	opened := review
	reviewMu.Unlock()
	if err != nil {
		return turnResult{plan: opened}
	}
	if o.AfterTurn != nil && ctx.Err() == nil {
		o.AfterTurn(ctx, text, res, paths)
	}
	return turnResult{turn: run.Turn{Role: "assistant", Content: res.Reply}, plan: opened}
}
