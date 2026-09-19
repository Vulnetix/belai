package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/vulnetix/signet/internal/agentpool"
	"github.com/vulnetix/signet/internal/delimiters"
	"github.com/vulnetix/signet/internal/explore"
	"github.com/vulnetix/signet/internal/modes"
	"github.com/vulnetix/signet/internal/nonce"
	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/sanitize"
	"github.com/vulnetix/signet/internal/tools"
)

// exploreTurns launches bounded-parallel read-only subagents and returns their
// findings as user turns, in task index order. Each finding is model output,
// therefore untrusted: unless the parent's posture ignores the unsafe-result
// gate, it is classified and only SAFE findings are sealed as <exploration>
// blocks. It is never SourceHarness.
//
// Plan mode's repository survey can be switched off with
// resilience.plan_explore: false, in which case exploration is skipped and
// planning starts immediately. Goal mode's survey is unaffected.
func (s *Session) exploreTurns(ctx context.Context, decision rolemanager.ModeDecision, clean string, pipe *rolemanager.Pipeline, emit func(Event)) []run.Turn {
	if !s.allowExplore {
		return nil
	}
	if decision.Mode == modes.ModePlan && !s.settings.PlanExploreEnabled() {
		return nil
	}
	return s.runExploreTasks(ctx, explore.Plan(clean, decision), "explore", pipe, emit)
}

// exploreContextDigest renders exploration findings as the plain-text context
// the plan evaluator is shown. The findings have already been classified and
// admitted as SAFE before they were sealed, and the evaluator call sanitizes
// them again, so this is never a new trust question.
func exploreContextDigest(turns []run.Turn) string {
	parts := make([]string, 0, len(turns))
	for _, t := range turns {
		if c := strings.TrimSpace(t.Content); c != "" {
			parts = append(parts, c)
		}
	}
	return strings.Join(parts, "\n\n")
}

// steerBridge fans one steering message out to every explore subagent running
// under the current fan-out. It is the parent→subagent steering channel that
// makes reset-on-steer possible: a steering message sent while explore is
// running reaches the affected subagents, whose iteration budget then resets.
type steerBridge struct {
	mu    sync.Mutex
	chans []chan string
}

func (b *steerBridge) subscribe() chan string {
	ch := make(chan string, steerBuffer)
	b.mu.Lock()
	b.chans = append(b.chans, ch)
	b.mu.Unlock()
	return ch
}

func (b *steerBridge) push(text string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.chans {
		select {
		case ch <- text:
		default: // drop rather than block the UI
		}
	}
}

// runExploreTasks fans the given tasks out over bounded-parallel read-only
// subagents and returns their classified findings as user turns, in task index
// order. It is the shared runner for initial explore, clarify rounds and
// forced goal surveys.
//
// Each task gets a roster lifecycle (queued → running → done/cancelled) as an
// EventSubagentKind, and its tool activity is forwarded as
// EventSubagentActivityKind stamped with the subagent's ID. The parent emitter
// is wrapped in a mutex-guarded forwarder because the subagent goroutines emit
// concurrently and a partial event must never interleave (the same reason
// selectWG exists at the top level).
//
// Steering sent while the fan-out is running is broadcast to every subagent so
// an explore subagent that has exhausted its iteration budget can reset and
// continue ("explore more", "now check X").
func (s *Session) runExploreTasks(ctx context.Context, tasks []explore.Task, kind string, pipe *rolemanager.Pipeline, emit func(Event)) []run.Turn {
	if len(tasks) == 0 {
		return nil
	}
	if emit == nil {
		emit = func(Event) {}
	}

	bridge := &steerBridge{}
	s.exploreBridge.Store(bridge)
	defer s.exploreBridge.Store(nil)

	var emitMu sync.Mutex
	forward := func(e Event) {
		emitMu.Lock()
		defer emitMu.Unlock()
		emit(e)
	}

	idFor := func(idx int) string {
		prefix := "e"
		switch kind {
		case "goal-survey":
			prefix = "g"
		case "clarify-explore":
			prefix = "c"
		}
		return fmt.Sprintf("%s%d", prefix, idx+1)
	}

	// Emit queued for every task first so the roster shows the whole fan-out
	// before any of it runs.
	for _, t := range tasks {
		forward(Event{Kind: EventSubagentKind, Subagent: &SubagentUpdate{
			ID: idFor(t.Index), Label: t.Reference, Kind: kind,
			State: string(agentpool.StateQueued), Index: t.Index, Total: len(tasks),
		}})
	}

	results := make([]string, len(tasks))
	var wg sync.WaitGroup
	for _, t := range tasks {
		wg.Add(1)
		go func(t explore.Task) {
			defer wg.Done()
			id := idFor(t.Index)
			label := t.Reference

			var lease *agentpool.Lease
			var err error
			if pipe != nil {
				lease, err = pipe.AcquireAgent(ctx, agentpool.Handle{
					ID: id, Label: label, Kind: kind, Index: t.Index, Total: len(tasks),
				})
				if err != nil {
					forward(Event{Kind: EventSubagentKind, Subagent: &SubagentUpdate{
						ID: id, Label: label, Kind: kind, State: string(agentpool.StateCancelled),
						Index: t.Index, Total: len(tasks),
					}})
					return
				}
			}

			runCtx := ctx
			if lease != nil {
				runCtx = lease.Context()
			}
			forward(Event{Kind: EventSubagentKind, Subagent: &SubagentUpdate{
				ID: id, Label: label, Kind: kind, State: string(agentpool.StateRunning),
				Index: t.Index, Total: len(tasks),
			}})

			body := s.runSubagent(runCtx, t, bridge.subscribe(), id, forward)
			results[t.Index] = body

			state := agentpool.StateDone
			if runCtx.Err() != nil {
				state = agentpool.StateCancelled
			}
			if lease != nil {
				lease.Done(state, "")
			}
			forward(Event{Kind: EventSubagentKind, Subagent: &SubagentUpdate{
				ID: id, Label: label, Kind: kind, State: string(state),
				Index: t.Index, Total: len(tasks),
			}})
		}(t)
	}
	wg.Wait()

	var turns []run.Turn
	for _, body := range results {
		if body == "" {
			continue
		}
		turns = append(turns, run.Turn{Role: "user", Content: body})
	}
	return turns
}

// goalSurveyTurns runs the forced codebase survey for a GOAL_NOT_STARTED
// verdict. It uses explore.PlanGoalSurvey so the forced explore surveys the
// repository rather than re-asking the raw prompt (a goal-mode prompt usually
// has no @references, so the ordinary plan would just repeat it).
func (s *Session) goalSurveyTurns(ctx context.Context, goalText string, pipe *rolemanager.Pipeline, emit func(Event)) []run.Turn {
	if !s.allowExplore {
		return nil
	}
	return s.runExploreTasks(ctx, explore.PlanGoalSurvey(goalText), "goal-survey", pipe, emit)
}

// runSubagent runs one read-only explore subagent and returns its classified,
// sealed finding (or "" when the finding is unsafe or the subagent fails).
// The subagent sees the original prompt, the grounding evidence, and the
// native read-only tool catalogue; it has a dedicated iteration budget from
// resilience.max_explore_iterations and may reset that budget when steering
// arrives through the steer channel.
//
// id keys the subagent's forwarded activity events, and forward is the parent's
// mutex-guarded emitter. Text and reasoning deltas are dropped (they would
// flood the transcript); tool starts, results and errors are mapped to
// EventSubagentActivityKind stamped with the subagent's ID. Every string on
// that path is sanitized before it leaves the child, and the finding itself
// still takes the existing sanitize + posture-gated classify route below.
func (s *Session) runSubagent(ctx context.Context, t explore.Task, steerCh chan string, id string, forward func(Event)) string {
	// The subagent runs in plan mode, so it gets the plan-mode surface:
	// read-only native and base tools with Bash removed. Building it with
	// .Plan() rather than relying on PlanMode alone keeps the advertised
	// list and the enforced list the same, so the preamble below cannot
	// promise a Bash the gate will refuse.
	reg := tools.DefaultWithCaps(s.workdir, true, s.caps, s.repoIndex).Plan()

	grounding := s.groundingProbe(ctx).digest()
	promptText := t.Prompt
	if grounding != "" {
		promptText += "\n\nWorkspace grounding (untrusted evidence):\n" + grounding
	}

	opts := s.opts
	opts.Explore = true             // plan-mode exploration preamble
	opts.ExploreTools = reg.Names() // promise only the tools actually registered

	sub, err := NewSession(Options{
		Cfg:           s.cfg,
		Client:        s.client,
		Registry:      reg,
		Posture:       s.posture,
		PlanMode:      true,  // read-only even if the registry grows
		AllowExplore:  false, // a subagent must not fan out again
		MaxIterations: s.settings.Resilience.MaxExploreIterationsOr(8),
		Workdir:       s.workdir,
		Settings:      s.settings,
		PromptOptions: opts,
		Caps:          s.caps,
		RepoIndex:     s.repoIndex,
		Cache:         s.cache, // share the session verdict cache across fan-out
		SkipNonceSeed: true,    // the subagent re-seeds locally below
	})
	if err != nil {
		return ""
	}
	sub.exploreSubagent = true
	sub.steerSource = func() string {
		select {
		case text := <-steerCh:
			return text
		default:
			return ""
		}
	}
	// One fresh, locally-seeded pool per subagent: a child's nonces are unknown
	// to the parent pool, and a child Rotate cannot invalidate the parent's
	// already-sealed system block.
	sub.pool = nonce.New()
	if err := sub.pool.Seed(8); err != nil {
		return ""
	}

	// The subagent runs on the blocking transport with a live emitter so its
	// tool activity streams into the parent transcript. Its finding still comes
	// from the reply, and the classify route below is unchanged.
	childEmitter := func(e Event) {
		switch e.Kind {
		case EventToolStartKind:
			forward(Event{
				Kind:       EventSubagentActivityKind,
				SubagentID: id,
				ToolName:   e.Tool.Name,
				ToolArgs:   sanitize.Sanitize(toolArgsJSON(e.Tool.Args)),
				ToolCallID: e.Tool.ID,
			})
		case EventToolResultKind:
			forward(Event{
				Kind:       EventSubagentActivityKind,
				SubagentID: id,
				ToolName:   e.ToolName,
				ToolResult: sanitize.Sanitize(e.ToolResult),
				ToolCallID: e.ToolCallID,
			})
		case EventErrorKind:
			forward(Event{Kind: EventSubagentActivityKind, SubagentID: id, ToolName: e.ToolName, Err: e.Err})
		}
	}
	res, err := sub.RunObserved(ctx, promptText, childEmitter)
	if err != nil {
		return ""
	}
	reply := res.Reply
	if reply == "" {
		return ""
	}

	// Model output is untrusted, so the finding is classified before it is
	// sealed — but under the parent's posture, not unconditionally. The
	// comment used to say "under the parent posture" while the code consulted
	// no posture at all, which meant a session with guardrails off still paid
	// a round trip per subagent and still silently dropped a finding the
	// classifier disliked.
	body := sanitize.Sanitize(reply)
	if s.posture.Level(posture.ToolResultUnsafe) != posture.Ignore {
		pipe := run.NewPipeline(s.cfg, s.client, s.cache)
		dec, err := pipe.Process(ctx, tools.Result{Kind: tools.KindExplore, Content: reply})
		if err != nil || dec.Action != rolemanager.ActionProceed {
			return ""
		}
		body = dec.Content
	}

	nonceVal, err := s.pool.Reserve()
	if err != nil {
		return ""
	}
	return delimiters.Egress(delimiters.Wrap(delimiters.KindExploration, nonceVal, body), s.pool)
}

// toolArgsJSON serializes a tool call's arguments for render-only display. A
// nil/empty map renders "", matching the TUI's tool row convention.
func toolArgsJSON(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	b, _ := json.Marshal(args)
	return string(b)
}
