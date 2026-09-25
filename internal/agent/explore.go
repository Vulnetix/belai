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
	"github.com/vulnetix/signet/internal/repomap"
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
// allEntrypoints returns the union of repo-map entrypoints across the
// primary workdir and any added workspace directories.
func (s *Session) allEntrypoints() []string {
	seen := map[string]bool{}
	var out []string
	add := func(m *repomap.Map) {
		if m == nil {
			return
		}
		for _, e := range m.Entrypoints {
			if !seen[e] {
				seen[e] = true
				out = append(out, e)
			}
		}
	}
	add(s.repoMap)
	for i := range s.workspaceMaps {
		add(&s.workspaceMaps[i])
	}
	return out
}

// planning starts immediately. A goal's pre-flight survey runs only with
// resilience.goal_explore: true; the not-started goal survey is unaffected.
func (s *Session) exploreTurns(ctx context.Context, decision rolemanager.ModeDecision, clean string, attached map[string]bool, pipe *rolemanager.Pipeline, emit func(Event)) []run.Turn {
	if !s.allowExplore {
		return nil
	}
	if decision.Mode == modes.ModePlan && !s.settings.PlanExploreEnabled() {
		return nil
	}
	if decision.Mode == modes.ModeGoal && !s.settings.GoalExploreEnabled() {
		return nil
	}
	tasks := explore.Plan(clean, decision)
	if decision.Mode == modes.ModePlan {
		entrypoints := s.allEntrypoints()
		if len(entrypoints) > 0 {
			// Enrich the no-reference survey with the concrete entrypoints the
			// repo map(s) already computed, so the subagent investigates real
			// files rather than rediscovering them. Workspace directories are
			// included so cross-repo entrypoints are covered.
			if len(tasks) > 0 && tasks[0].Reference == explore.SurveyReference {
				tasks = explore.PlanSurveyWithEntrypoints(clean, entrypoints)
			}
		}
	}
	// A file the user attached already rides on the turn whole; a subagent
	// reading it again only to summarise it is a wasted round trip.
	tasks = explore.DropAttached(tasks, attached)
	return s.runExploreTasks(ctx, tasks, "explore", pipe, emit)
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
		case "review":
			prefix = "r"
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

	// One grounding probe for the whole fan-out: every subagent used to run
	// its own git probes over the same unchanged working tree.
	grounding := s.groundingProbe(ctx)

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

			body := s.runSubagent(runCtx, t, grounding, bridge.subscribe(), id, forward, kind == "goal-survey")
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

// reviewTurns runs one read-only subagent per /vulnetix review scanner report
// and returns their classified findings as user turns. The reports were
// classified by the caller before they reached the session, and each rides to
// its subagent as a file attachment, never spliced into the prompt text.
//
// A scanner whose subagent already ran (findings, classified by the caller)
// is sealed as an exploration turn as it is, and only the scanners without
// one fan out here.
func (s *Session) reviewTurns(ctx context.Context, clean string, reports []explore.ReviewReport, findings []explore.ReviewFinding, pipe *rolemanager.Pipeline, emit func(Event)) []run.Turn {
	var turns []run.Turn
	done := map[string]bool{}
	for _, f := range findings {
		done[f.Scanner] = true
		body := sanitize.Sanitize(f.Body)
		if strings.TrimSpace(body) == "" {
			continue
		}
		if sealed := s.sealExploration("vulnetix " + f.Scanner + " review:\n" + body); sealed != "" {
			turns = append(turns, run.Turn{Role: "user", Content: sealed})
		}
	}
	var rest []explore.ReviewReport
	for _, r := range reports {
		if !done[r.Scanner] {
			rest = append(rest, r)
		}
	}
	// Findings that already ran need no fan-out; the rest do, which a
	// session that may not fan out (a subagent) skips.
	if len(rest) > 0 && s.allowExplore {
		turns = append(turns, s.runExploreTasks(ctx, explore.PlanReview(clean, rest), "review", pipe, emit)...)
	}
	return turns
}

// sealExploration wraps an admitted finding in a sealed exploration block,
// or returns "" when no nonce can be reserved.
func (s *Session) sealExploration(body string) string {
	nonceVal, err := s.pool.Reserve()
	if err != nil {
		return ""
	}
	return delimiters.Egress(delimiters.Wrap(delimiters.KindExploration, nonceVal, body), s.pool)
}

// exploreConfig returns the config an explore subagent runs under. The
// subagent performs a bounded, read-only survey whose quality does not need
// the full-size main model, so it runs on the fast tier whenever one is
// resolved. Plan mode is read-only and the user is waiting for a plan, so the
// pre-planning survey must not spend minutes on a slow reasoning model before
// the first planning turn. Only the main-model identity moves; the resolved
// classifier stack and routing stay the parent's.
func (s *Session) exploreConfig() run.Config {
	cfg := s.cfg
	if f := cfg.Routing.Fast; f != nil {
		cfg.Provider = f.Provider
		cfg.BaseURL = f.BaseURL
		cfg.APIKey = f.APIKey
		cfg.Model = f.Model
		cfg.Effort = "low" // the survey does not need extended thinking
		cfg.API = f.API
		cfg.Auth = f.Auth
		cfg.Kind = f.Kind
	}
	return cfg
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
func (s *Session) runSubagent(ctx context.Context, t explore.Task, g Grounding, steerCh chan string, id string, forward func(Event), goalSurvey bool) string {
	// The subagent runs in plan mode, so it gets the plan-mode surface:
	// read-only native and base tools with Bash removed. Building it with
	// .Plan() rather than relying on PlanMode alone keeps the advertised
	// list and the enforced list the same, so the preamble below cannot
	// promise a Bash the gate will refuse.
	reg := tools.DefaultWithCaps(s.workdir, true, s.caps, s.repoIndex).Plan()

	grounding := g.digest(t.Kind == explore.RefRepo || t.Kind == explore.RefOrg)
	promptText := t.Prompt
	if grounding != "" {
		promptText += "\n\nWorkspace grounding (untrusted evidence):\n" + grounding
	}

	opts := s.opts
	opts.Explore = true             // read-only exploration preamble
	opts.ExploreGoal = goalSurvey   // goal surveys name their own job
	opts.ExploreTools = reg.Names() // promise only the tools actually registered

	sub, err := NewSession(Options{
		Cfg:           s.exploreConfig(),
		Client:        s.client,
		Registry:      reg,
		Live:          s.live,
		PlanMode:      true,  // read-only even if the registry grows
		AllowExplore:  false, // a subagent must not fan out again
		MaxIterations: exploreBudget(t.Budget, s.settings.Resilience.MaxExploreIterationsOr(8)),
		Workdir:       s.workdir,
		Settings:      s.settings,
		PromptOptions: opts,
		Caps:          s.caps,
		RepoIndex:     s.repoIndex,
		Cache:         s.cache, // share the session verdict cache across fan-out
		SkipNonceSeed: true,    // the subagent re-seeds locally below
		RepoMap:       s.repoMap,
		SessionID:     s.sessionID,
	})
	if err != nil {
		return ""
	}
	sub.exploreSubagent = true
	sub.scope = t.Scope
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
	// The subagent's mode is known: it explores, read-only, in one bounded
	// pass. Forcing it skips the mode-select call and any goal-contract
	// draft, neither of which an explore run ever uses.
	in := TurnInput{Prompt: promptText, ForceMode: modes.ModeAgent}
	if t.Evidence != "" {
		// Already classified by whoever handed the evidence over; it rides
		// as an attachment exactly as an admitted @file does.
		in.Attachments = []run.Attachment{{Kind: "file", Label: t.EvidenceLabel, Body: t.Evidence}}
	}
	res, err := sub.run(ctx, nil, in, false, childEmitter)
	if err != nil {
		return ""
	}
	limit := maxExploreReport
	if t.ReportBytes > 0 {
		limit = t.ReportBytes
	}
	reply := capReport(res.Reply, limit)
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
	if s.live.Level(posture.ToolResultUnsafe) != posture.Ignore {
		pipe := run.NewPipeline(s.cfg, s.client, s.cache)
		dec, err := pipe.Process(ctx, tools.Result{Kind: tools.KindExplore, Content: reply})
		if err != nil || dec.Action != rolemanager.ActionProceed {
			return ""
		}
		body = dec.Content
	}

	return s.sealExploration(body)
}

// exploreBudget is a task's tool-round budget: the task's own, capped by the
// resilience.max_explore_iterations setting (which a task with no budget
// uses as is).
func exploreBudget(task, setting int) int {
	if task > 0 && task < setting {
		return task
	}
	return setting
}

// maxExploreReport bounds one finding, in bytes. The report contract asks
// for at most ten located bullets; a subagent that writes an essay anyway is
// cut at a line boundary, so the parent's context and the classifier call
// stay small.
const maxExploreReport = 6 * 1024

// capReport trims a finding to limit bytes at a line boundary.
func capReport(s string, limit int) string {
	s = strings.TrimSpace(s)
	if len(s) <= limit {
		return s
	}
	cut := s[:limit]
	if i := strings.LastIndexByte(cut, '\n'); i > 0 {
		cut = cut[:i]
	}
	return cut + "\n… (report truncated)"
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
