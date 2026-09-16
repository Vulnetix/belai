package agent

import (
	"context"
	"sync"

	"github.com/vulnetix/signet/internal/delimiters"
	"github.com/vulnetix/signet/internal/explore"
	"github.com/vulnetix/signet/internal/nonce"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/tools"
)

const exploreConcurrency = 3

// exploreTurns launches bounded-parallel read-only subagents and returns their
// findings as user turns, in task index order. Each finding is model output,
// therefore untrusted: it is classified, and only SAFE findings are sealed as
// <exploration> blocks. It is never SourceHarness.
func (s *Session) exploreTurns(ctx context.Context, decision rolemanager.ModeDecision, clean string) []run.Turn {
	if !s.allowExplore {
		return nil
	}
	return s.runExploreTasks(ctx, explore.Plan(clean, decision))
}

// runExploreTasks fans the given tasks out over bounded-parallel read-only
// subagents and returns their classified findings as user turns, in task index
// order. It is the shared runner for initial explore and forced goal surveys.
func (s *Session) runExploreTasks(ctx context.Context, tasks []explore.Task) []run.Turn {
	if len(tasks) == 0 {
		return nil
	}

	results := make([]string, len(tasks))
	var wg sync.WaitGroup
	sem := make(chan struct{}, exploreConcurrency)
	for _, t := range tasks {
		wg.Add(1)
		go func(t explore.Task) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[t.Index] = s.runSubagent(ctx, t)
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

// runSubagent runs one read-only subagent and returns its classified, sealed
// finding (or "" when the finding is unsafe or the subagent fails).
func (s *Session) runSubagent(ctx context.Context, t explore.Task) string {
	reg := tools.Default(s.workdir, true) // read-only Bash
	sub, err := NewSession(Options{
		Cfg:           s.cfg,
		Client:        s.client,
		Registry:      reg,
		Posture:       s.posture,
		PlanMode:      true,  // read-only even if the registry grows
		AllowExplore:  false, // a subagent must not fan out again
		MaxIterations: 4,     // exploration is shallow by construction
		Workdir:       s.workdir,
		Settings:      s.settings,
		PromptOptions: s.opts,
	})
	if err != nil {
		return ""
	}
	// One fresh, locally-seeded pool per subagent: a child's nonces are unknown
	// to the parent pool, and a child Rotate cannot invalidate the parent's
	// already-sealed system block.
	sub.pool = nonce.New()
	if err := sub.pool.Seed(8); err != nil {
		return ""
	}

	ch := sub.RunStream(ctx, nil, TurnInput{Prompt: t.Prompt})
	var reply string
	for ev := range ch {
		switch ev.Kind {
		case EventErrorKind:
			return ""
		case EventDoneKind:
			reply = ev.Result.Reply
		}
	}
	if reply == "" {
		return ""
	}

	// Model output is untrusted: classify it under the parent posture.
	pipe := rolemanager.NewPipeline(run.NewClassifier(s.cfg, s.client))
	dec, err := pipe.Process(ctx, tools.Result{Kind: tools.KindExplore, Content: reply})
	if err != nil || dec.Action != rolemanager.ActionProceed {
		return ""
	}

	nonceVal, err := s.pool.Reserve()
	if err != nil {
		return ""
	}
	return delimiters.Egress(delimiters.Wrap(delimiters.KindExploration, nonceVal, dec.Content), s.pool)
}
