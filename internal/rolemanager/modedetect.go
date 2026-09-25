package rolemanager

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/vulnetix/signet/internal/clarify"
	"github.com/vulnetix/signet/internal/modes"
)

// DetectInput carries the raw inputs to intent detection. It contains only
// harness-computed facts and the sanitized prompt; attachment bytes are never
// included.
type DetectInput struct {
	// Prompt is the sanitized user prompt.
	Prompt string
	// GoalLimit is the goal-mode length limit in runes; <=0 uses the default.
	GoalLimit int
	// HasReferences reports whether attachments or referenced files were
	// provided alongside the prompt.
	HasReferences bool
	// PlanAttachment is non-nil when a plan-file attachment is present.
	PlanAttachment *HandoffFacts
	// ModeHint is the currently selected mode and whether it is sticky.
	ModeHint ModeHint
}

// IntentDetector returns per-intent scores from one noul question per intent.
// A transport error or malformed reply returns an error so the caller can fall
// back to the LLM classifier.
type IntentDetector interface {
	DetectIntent(ctx context.Context, in DetectInput) (map[Intent]float64, string, error)
}

// Detection is the resolved result of intent detection. Either Jev or the LLM
// classifier produced it.
type Detection struct {
	// Source is "jev" or "llm".
	Source string
	// Model is the provider/model identity that produced the result.
	Model string
	// Scores carries the per-intent probabilities. For the LLM fallback the
	// chosen intent scores 1.0 and the rest 0.0.
	Scores map[Intent]float64
	// Top is the highest-scoring intent.
	Top Intent
	// TopScore is the highest score.
	TopScore float64
	// RunnerUp is the second-highest score.
	RunnerUp float64
	// Handoff carries harness facts about the plan-file attachment, if any.
	Handoff *HandoffFacts
}

// Detect runs the Jev detector when available; on error or timeout it falls
// back to the LLM classifier. The returned Detection carries the source and
// scores for tracing and for the mode-choice panel.
func Detect(ctx context.Context, jev IntentDetector, llm Classifier, in DetectInput) (Detection, error) {
	start := time.Now()
	if jev != nil {
		scores, model, err := jev.DetectIntent(ctx, in)
		if err == nil {
			det := buildDetection("jev", model, scores, in.PlanAttachment)
			recordTimed(EventModeDetect, string(det.Top), det.Source, detailScores(det.Scores), 0, model, time.Since(start))
			return det, nil
		}
	}

	start = time.Now()
	intent, sentinel, err := ClassifyModeIntent(ctx, llm, in.Prompt, in.PlanAttachment != nil)
	if err != nil {
		return Detection{}, err
	}
	scores := oneHotScores(intent)
	det := buildDetection("llm", sentinel, scores, in.PlanAttachment)
	recordTimed(EventModeDetect, string(det.Top), det.Source, "fallback", 0, sentinel, time.Since(start))
	return det, nil
}

func buildDetection(source, model string, scores map[Intent]float64, handoff *HandoffFacts) Detection {
	det := Detection{Source: source, Model: model, Scores: scores, Handoff: handoff}
	det.Top, det.TopScore, det.RunnerUp = topScores(scores)
	return det
}

func oneHotScores(i Intent) map[Intent]float64 {
	scores := map[Intent]float64{
		IntentAgent:  0,
		IntentPlan:   0,
		IntentGoal:   0,
		IntentDebug:  0,
		IntentFanOut: 0,
	}
	if i != IntentHandoff {
		scores[i] = 1.0
	}
	return scores
}

func topScores(scores map[Intent]float64) (Intent, float64, float64) {
	type pair struct {
		intent Intent
		score  float64
	}
	var pairs []pair
	for i, s := range scores {
		pairs = append(pairs, pair{i, s})
	}
	// Stable deterministic order: sort by score desc, then intent asc.
	sort.Slice(pairs, func(a, b int) bool {
		if pairs[a].score != pairs[b].score {
			return pairs[a].score > pairs[b].score
		}
		return pairs[a].intent < pairs[b].intent
	})
	if len(pairs) == 0 {
		return IntentAgent, 0, 0
	}
	top := pairs[0]
	runnerUp := 0.0
	if len(pairs) > 1 {
		runnerUp = pairs[1].score
	}
	return top.intent, top.score, runnerUp
}

// modeToIntent maps the three UI modes to their corresponding intents. Plan
// and goal map directly; agent maps to the default agent intent.
func modeToIntent(m modes.Mode) Intent {
	switch m {
	case modes.ModePlan:
		return IntentPlan
	case modes.ModeGoal:
		return IntentGoal
	default:
		return IntentAgent
	}
}

// Resolve turns a Detection and the current mode hint into the intent to
// engage and whether the user must be asked. It is pure and deterministic.
//
// Confident means the top intent scores at least 0.80 and leads the runner-up
// by at least 0.25.
//
// Without a sticky hint: a confident top intent is engaged silently; otherwise
// the user is asked when interactive, and the top intent is kept only when it
// scores at least 0.50 (otherwise agent is the safe headless fallback).
//
// With a sticky hint: the hint is kept silently when the top intent matches
// the hint or when no other intent is confident. If another intent is confident
// and disagrees, the user is asked when interactive; headless never leaves a
// sticky mode.
func Resolve(det Detection, hint ModeHint, interactive bool) (Intent, bool) {
	top := det.Top
	topScore := det.TopScore
	runnerUp := det.RunnerUp

	confident := topScore >= 0.80 && (topScore-runnerUp) >= 0.25
	hintIntent := modeToIntent(hint.Mode)

	if !hint.Sticky {
		if confident {
			return top, false
		}
		if interactive {
			return hintIntent, true
		}
		if topScore >= 0.5 {
			return top, false
		}
		return IntentAgent, false
	}

	if top == hintIntent || !confident {
		return hintIntent, false
	}
	if interactive {
		return hintIntent, true
	}
	return hintIntent, false
}

// ModeChoiceQuestionnaire builds the deterministic mode-choice clarify panel.
// It returns the questionnaire and the parallel intent slice so the caller can
// map the user's answer back to an intent.
//
// The panel shows the top three intents by score plus the current mode when it
// is not already among them, capped at clarify's four-option limit. Jev scores
// render as percentages; the LLM fallback renders each top choice as
// "suggested".
func ModeChoiceQuestionnaire(det Detection, hint ModeHint) (clarify.Questionnaire, []Intent) {
	opts := modeChoiceOptions(det, hint)
	qq := clarify.Questionnaire{
		Groups: []clarify.Group{{
			Context: "Which mode should handle this prompt?",
			Multi:   false,
			Options: opts.labels,
		}},
	}
	return qq, opts.intents
}

// ModeChoiceAnswer returns the intent the user chose from the mode-choice
// questionnaire. A skip/empty answer returns the current mode's intent and
// false.
func ModeChoiceAnswer(answers clarify.Answers, opts []Intent, hint ModeHint) (Intent, bool) {
	if len(answers.Items) == 0 {
		return modeToIntent(hint.Mode), false
	}
	ans := answers.Items[0]
	if ans.Skipped || len(ans.Chosen) == 0 {
		return modeToIntent(hint.Mode), false
	}
	idx := ans.Chosen[0]
	if idx < 0 || idx >= len(opts) {
		return modeToIntent(hint.Mode), false
	}
	return opts[idx], true
}

type choiceOptions struct {
	labels  []clarify.Option
	intents []Intent
}

func modeChoiceOptions(det Detection, hint ModeHint) choiceOptions {
	hintIntent := modeToIntent(hint.Mode)

	type pair struct {
		intent Intent
		score  float64
	}
	var pairs []pair
	seen := map[Intent]bool{}
	if det.Scores != nil {
		for i, s := range det.Scores {
			pairs = append(pairs, pair{i, s})
			seen[i] = true
		}
	}
	for _, i := range []Intent{IntentAgent, IntentPlan, IntentGoal, IntentHandoff, IntentDebug, IntentFanOut} {
		if !seen[i] {
			pairs = append(pairs, pair{i, 0})
			seen[i] = true
		}
	}
	sort.Slice(pairs, func(a, b int) bool {
		if pairs[a].score != pairs[b].score {
			return pairs[a].score > pairs[b].score
		}
		return pairs[a].intent < pairs[b].intent
	})

	// Take top 3; add the current mode if absent.
	var top []pair
	for i := 0; i < len(pairs) && i < 3; i++ {
		top = append(top, pairs[i])
	}
	foundHint := false
	for _, p := range top {
		if p.intent == hintIntent {
			foundHint = true
			break
		}
	}
	if !foundHint {
		if len(top) == 3 {
			top = top[:len(top)-1]
		}
		top = append(top, pair{hintIntent, 0})
	}

	var labels []clarify.Option
	var intents []Intent
	for i, p := range top {
		label := intentLabel(p.intent)
		suffix := ""
		isTop := i == 0
		if p.intent == hintIntent && hint.Sticky {
			suffix = " (currently selected)"
		} else if isTop {
			if det.Source == "llm" {
				suffix = " (Recommended)"
			} else {
				suffix = fmt.Sprintf(" · %.0f%% (Recommended)", p.score*100)
			}
		} else {
			if det.Source == "llm" {
				suffix = " (suggested)"
			} else if p.score > 0 {
				suffix = fmt.Sprintf(" · %.0f%%", p.score*100)
			}
		}
		labels = append(labels, clarify.Option{
			Label:       label + suffix,
			Description: p.intent.Label(),
		})
		intents = append(intents, p.intent)
	}
	return choiceOptions{labels: labels, intents: intents}
}

func intentLabel(i Intent) string {
	switch i {
	case IntentPlan:
		return "Plan"
	case IntentGoal:
		return "Goal"
	case IntentHandoff:
		return "Plan handoff"
	case IntentDebug:
		return "Debug"
	case IntentFanOut:
		return "Fan-out"
	default:
		return "Agent"
	}
}

func detailScores(scores map[Intent]float64) string {
	return fmt.Sprintf("agent=%.2f plan=%.2f goal=%.2f handoff=%.2f debug=%.2f fanout=%.2f",
		scores[IntentAgent], scores[IntentPlan], scores[IntentGoal],
		scores[IntentHandoff], scores[IntentDebug], scores[IntentFanOut])
}
