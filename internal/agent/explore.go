package agent

import (
	"context"
	"sync"

	"github.com/vulnetix/signet/internal/delimiters"
	"github.com/vulnetix/signet/internal/explore"
	"github.com/vulnetix/signet/internal/nonce"
	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/sanitize"
	"github.com/vulnetix/signet/internal/tools"
)

const exploreConcurrency = 3

// exploreTurns launches bounded-parallel read-only subagents and returns their
// findings as user turns, in task index order. Each finding is model output,
// therefore untrusted: unless the parent's posture ignores the unsafe-result
// gate, it is classified and only SAFE findings are sealed as <exploration>
// blocks. It is never SourceHarness.
func (s *Session) exploreTurns(ctx context.Context, decision rolemanager.ModeDecision, clean string) []run.Turn {
	if !s.allowExplore {
		return nil
	}
	return s.runExploreTasks(ctx, explore.Plan(clean, decision))
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
// order. It is the shared runner for initial explore and forced goal surveys.
//
// Steering sent while the fan-out is running is broadcast to every subagent so
// an explore subagent that has exhausted its iteration budget can reset and
// continue ("explore more", "now check X").
func (s *Session) runExploreTasks(ctx context.Context, tasks []explore.Task) []run.Turn {
	if len(tasks) == 0 {
		return nil
	}

	bridge := &steerBridge{}
	s.exploreBridge.Store(bridge)
	defer s.exploreBridge.Store(nil)

	results := make([]string, len(tasks))
	var wg sync.WaitGroup
	sem := make(chan struct{}, exploreConcurrency)
	for _, t := range tasks {
		wg.Add(1)
		go func(t explore.Task) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[t.Index] = s.runSubagent(ctx, t, bridge.subscribe())
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
func (s *Session) goalSurveyTurns(ctx context.Context, goalText string) []run.Turn {
	if !s.allowExplore {
		return nil
	}
	return s.runExploreTasks(ctx, explore.PlanGoalSurvey(goalText))
}

// runSubagent runs one read-only explore subagent and returns its classified,
// sealed finding (or "" when the finding is unsafe or the subagent fails).
// The subagent sees the original prompt, the grounding evidence, and the
// native read-only tool catalogue; it has a dedicated iteration budget from
// resilience.max_explore_iterations and may reset that budget when steering
// arrives through the steer channel.
func (s *Session) runSubagent(ctx context.Context, t explore.Task, steerCh chan string) string {
	// The subagent runs in plan mode, so it gets the plan-mode surface:
	// read-only native and base tools with Bash removed. Building it with
	// .Plan() rather than relying on PlanMode alone keeps the advertised
	// list and the enforced list the same, so the preamble below cannot
	// promise a Bash the gate will refuse.
	reg := tools.DefaultWithCaps(s.workdir, true, s.caps).Plan()

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

	// The subagent runs on the blocking transport: it only needs the final
	// finding, not streaming events, and the blocking path matches how the
	// non-interactive CLI drives a model.
	res, err := sub.Run(ctx, promptText)
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
