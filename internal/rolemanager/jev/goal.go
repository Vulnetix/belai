package jev

import (
	"context"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/sanitize"
)

// GoalVerdict is the decision backend's read of a goal pass.
type GoalVerdict string

const (
	// GoalUnclear is the answer when the job is off, the backend did not
	// answer, or the scores do not make one verdict clear. The model judge
	// decides, with the scores as a hint.
	GoalUnclear GoalVerdict = ""
	// GoalComplete is a pass whose goal the backend rates clearly achieved.
	GoalComplete GoalVerdict = "complete"
	// GoalPartial is a pass the backend rates as advanced but unfinished. It
	// is a lead for the model judge, never a verdict on its own.
	GoalPartial GoalVerdict = "partial"
	// GoalNotStarted is a pass the backend rates clearly without progress.
	GoalNotStarted GoalVerdict = "not_started"
)

// goal item ids, the harness's own constants.
const (
	goalItemComplete   = "complete"
	goalItemPartial    = "partial"
	goalItemNotStarted = "not_started"
)

// GoalScores are the three option scores as percentages, -1 for an option the
// backend did not answer.
type GoalScores struct {
	Complete, Partial, NotStarted int
}

// goalCriterion is the statement each item is judged against.
const goalCriterion = "The goal in state.context, judged from the todo list and the facts about the pass, matches the description in the item. A goal is complete only when every step is done and the pass shows the change was checked, not merely made."

// GoalFacts is what the backend may see: the goal, the rendered todo list and
// harness-computed facts about the pass. Never tool output, a reply or file
// contents.
type GoalFacts struct {
	// Goal and Todos are the goal text and the tracked list.
	Goal, Todos string
	// Facts is the pass ledger's own observation (counts and paths).
	Facts string
	// Verified says the pass ran its own passing check after its last change.
	Verified string
}

// RateGoal rates a goal pass as complete, partial and not started. Every value
// reaches the backend only as DecisionText. An option the backend did not
// answer is absent from the result.
func (j *Jobs) RateGoal(ctx context.Context, f GoalFacts) (map[string]float64, ScoreResult, error) {
	extra := map[string]sanitize.DecisionText{
		"todos": sanitize.ForDecision(f.Todos, 1500),
		"facts": sanitize.ForDecision(f.Facts, 1200),
	}
	if f.Verified != "" {
		extra["checked"] = sanitize.ForDecision(f.Verified, 80)
	}
	res, err := j.Client.Score(ctx, ScoreRequest{
		Job:       string(config.JevGoalJudge),
		Criterion: sanitize.ForDecision(goalCriterion, 0),
		Context:   sanitize.ForDecision(f.Goal, 1500),
		Extra:     extra,
		Items: []ScoreItem{
			{ID: goalItemComplete, Label: sanitize.ForDecision("The goal is complete: every step is done and the pass checked the result.", 240)},
			{ID: goalItemPartial, Label: sanitize.ForDecision("The goal is partly done: work has advanced but steps remain or nothing was checked.", 240)},
			{ID: goalItemNotStarted, Label: sanitize.ForDecision("The goal is not started: no meaningful work toward it has happened.", 240)},
		},
		MaxRequests: 4,
	})
	return res.Scores, res, err
}

// PickGoal turns the option scores into a verdict. Complete needs a score at
// or above goal_complete_at with both rivals at or below goal_rival_max;
// not started is the mirror at goal_not_started_at. Anything else, including
// an option the backend did not answer, is unclear, and a lead for partial is
// reported as partial so the model judge can be told. A verdict only ends a
// pass early; it never overrides the harness verification gate.
func PickGoal(scores map[string]float64) (GoalVerdict, GoalScores) {
	th := config.ActiveJevThresholds()
	out := GoalScores{Complete: -1, Partial: -1, NotStarted: -1}
	c, okC := scores[goalItemComplete]
	p, okP := scores[goalItemPartial]
	n, okN := scores[goalItemNotStarted]
	if okC {
		out.Complete = pct(c)
	}
	if okP {
		out.Partial = pct(p)
	}
	if okN {
		out.NotStarted = pct(n)
	}
	if !okC || !okP || !okN {
		return GoalUnclear, out
	}
	switch {
	case c >= th.GoalCompleteAt && p <= th.GoalRivalMax && n <= th.GoalRivalMax:
		return GoalComplete, out
	case n >= th.GoalNotStartedAt && c <= th.GoalRivalMax && p <= th.GoalRivalMax:
		return GoalNotStarted, out
	case p >= th.KeepAt && p > c && p > n:
		return GoalPartial, out
	}
	return GoalUnclear, out
}

func pct(v float64) int { return int(v*100 + 0.5) }
