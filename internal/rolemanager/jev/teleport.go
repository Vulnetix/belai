package jev

import (
	"context"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/sanitize"
)

// TeleportVerdict is the decision backend's read of a replayed checkout.
type TeleportVerdict string

const (
	// TeleportUnclear is the answer when the job is off, the backend did not
	// answer, or the scores do not make one verdict clear. The model verifier
	// decides, with the scores as a hint.
	TeleportUnclear TeleportVerdict = ""
	// TeleportVerified is a checkout the backend rates clearly matching the
	// origin's summary.
	TeleportVerified TeleportVerdict = "verified"
	// TeleportIncomplete is a checkout the backend rates as partly replayed. A
	// lead for the model verifier and for another pass, never a verdict on its own.
	TeleportIncomplete TeleportVerdict = "incomplete"
	// TeleportFailed is a checkout the backend rates clearly not matching.
	TeleportFailed TeleportVerdict = "failed"
)

const (
	teleportItemVerified   = "verified"
	teleportItemIncomplete = "incomplete"
	teleportItemFailed     = "failed"
)

// TeleportScores are the three option scores as percentages, -1 for an option
// the backend did not answer.
type TeleportScores struct {
	Verified, Incomplete, Failed int
}

const teleportCriterion = "The state describes changes an origin host made and the harness's own check of a checkout where they were replayed. The checkout matches the description in the item. Files the harness found identical to the origin's are exactly right; a missing or different file is not."

// TeleportFacts is what the backend may see: the origin model's summary and the
// harness's own check, both as decision text. Never a file's contents, a patch
// or a tool's output.
type TeleportFacts struct {
	// Summary is the origin's account of the change (text from another host's
	// model, sanitised); Check is the harness's file-by-file result.
	Summary, Check string
}

// RateTeleport rates a replayed checkout as verified, incomplete and failed.
// Every value reaches the backend only as DecisionText. An option the backend
// did not answer is absent from the result.
func (j *Jobs) RateTeleport(ctx context.Context, f TeleportFacts) (map[string]float64, ScoreResult, error) {
	res, err := j.Client.Score(ctx, ScoreRequest{
		Job:       string(config.JevTeleportVerify),
		Criterion: sanitize.ForDecision(teleportCriterion, 0),
		Context:   sanitize.ForDecision(f.Summary, 1500),
		Extra:     map[string]sanitize.DecisionText{"check": sanitize.ForDecision(f.Check, 1500)},
		Items: []ScoreItem{
			{ID: teleportItemVerified, Label: sanitize.ForDecision("The checkout matches the origin: every changed file is as the origin left it and nothing else changed.", 240)},
			{ID: teleportItemIncomplete, Label: sanitize.ForDecision("The checkout is partly there: some files are missing or differ, and finishing them is likely to fix it.", 240)},
			{ID: teleportItemFailed, Label: sanitize.ForDecision("The checkout does not match and another attempt is unlikely to fix it.", 240)},
		},
		MaxRequests: 4,
	})
	return res.Scores, res, err
}

// PickTeleport turns the option scores into a verdict, with the goal judge's
// cut-offs (goal_complete_at and goal_rival_max, docs/jev-jobs.md): verified
// needs a score at or above goal_complete_at with both rivals at or below
// goal_rival_max, failed is the mirror, and a lead for incomplete is reported as
// incomplete so the model verifier can be told. Anything else, including an
// option the backend did not answer, is unclear. A verdict never replaces the
// harness's exact tree check and is never accepted for a file the harness found
// missing: the caller decides that from facts first.
func PickTeleport(scores map[string]float64) (TeleportVerdict, TeleportScores) {
	th := config.ActiveJevThresholds()
	out := TeleportScores{Verified: -1, Incomplete: -1, Failed: -1}
	v, okV := scores[teleportItemVerified]
	i, okI := scores[teleportItemIncomplete]
	f, okF := scores[teleportItemFailed]
	if okV {
		out.Verified = pct(v)
	}
	if okI {
		out.Incomplete = pct(i)
	}
	if okF {
		out.Failed = pct(f)
	}
	if !okV || !okI || !okF {
		return TeleportUnclear, out
	}
	switch {
	case v >= th.GoalCompleteAt && i <= th.GoalRivalMax && f <= th.GoalRivalMax:
		return TeleportVerified, out
	case f >= th.GoalCompleteAt && v <= th.GoalRivalMax && i <= th.GoalRivalMax:
		return TeleportFailed, out
	case i >= th.KeepAt && i > v && i > f:
		return TeleportIncomplete, out
	}
	return TeleportUnclear, out
}
