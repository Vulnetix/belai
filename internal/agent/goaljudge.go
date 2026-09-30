package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/rolemanager/jev"
)

// Goal judge. When a decision backend is configured it rates each goal pass on
// three options (complete, partial, not started). A clear verdict settles the
// pass without a chat-model call. Anything else goes to the model judge as it
// always did, with the scores attached as a hint and an instruction to accept
// completion only when the pass checked the result with tools.
//
// The backend sees the goal, the todo list and the ledger's own facts, never
// the pass evidence, the reply or file contents. A complete verdict does not
// skip the harness verification gate: evaluateGoalPass only replaces the
// evaluator's answer, and the pass loop applies the gate to either.

// goalJudgeTimeout bounds the rating so a slow backend never delays a pass
// boundary: past it the model judge runs as it always did.
const goalJudgeTimeout = 8 * time.Second

// goalJevVerifyNote is added to the verification directive when the decision
// backend could not call the goal clearly complete.
const goalJevVerifyNote = " A decision model could not confirm this goal is complete, so verify it properly with your tools: run the project's tests or build, read back the files you changed, and check each requirement against what is on disk. Report what each check showed."

// rateGoal rates the pass. verdict is the sentinel when the backend settled
// it; hint is the harness-composed score line for the model judge when it did
// not (empty when no backend answered or the job is off).
func (s *Session) rateGoal(ctx context.Context, l *passLedger) (verdict rolemanager.GoalSentinel, hint string, settled bool) {
	if s.jev == nil || !s.jev.Enabled(config.JevGoalJudge) {
		return "", "", false
	}
	ctx, cancel := context.WithTimeout(ctx, goalJudgeTimeout)
	defer cancel()
	start := time.Now()
	facts := jev.GoalFacts{Goal: l.goalText, Todos: l.list.Render(), Facts: l.goalFacts()}
	if l.verificationPasses > 0 {
		facts.Verified = fmt.Sprintf("verification passes: %d", l.verificationPasses)
	}
	scores, res, err := s.jev.RateGoal(ctx, facts)
	if err != nil || len(scores) == 0 {
		rolemanager.RecordGoalJudge("unclear", -1, -1, -1, l.passes, res.Identity, time.Since(start))
		return "", "", false
	}
	v, pcts := jev.PickGoal(scores)
	name := string(v)
	if v == jev.GoalUnclear || v == jev.GoalPartial {
		name = "unclear"
	}
	rolemanager.RecordGoalJudge(name, pcts.Complete, pcts.Partial, pcts.NotStarted, l.passes, res.Identity, time.Since(start))
	switch v {
	case jev.GoalComplete:
		return rolemanager.GoalComplete, "", true
	case jev.GoalNotStarted:
		return rolemanager.GoalNotStarted, "", true
	}
	return "", goalJevHint(pcts), false
}

// goalJevHint renders the scores for the model judge: percentages only.
func goalJevHint(p jev.GoalScores) string {
	one := func(name string, v int) string {
		if v < 0 {
			return name + ": unknown"
		}
		return fmt.Sprintf("%s: %d%%", name, v)
	}
	return one("complete", p.Complete) + "\n" + one("partial", p.Partial) + "\n" + one("not started", p.NotStarted) + "\n"
}
