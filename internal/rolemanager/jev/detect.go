// Package jev implements the Role Manager Jev tool-call gate and the Jev
// security classifier. Both ask the TypeSafe/Jev decision model on
// OpenRouter's Decisions API a set of boolean ("noul") questions and reduce
// the returned probabilities to strict single-token verdicts. Neither turn
// ever carries tools, skills, or an agent block.
package jev

import (
	"context"
	"fmt"

	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/rolemanager"
)

// intentQuestion is one fixed proposition Jev scores for an intent.
type intentQuestion struct {
	key          rolemanager.Intent
	instructions string
}

// intentQuestions lists every intent except handoff. Handoff is appended only
// when a plan-file attachment is present, so the detector can never route a
// prompt to the handoff profile without proof of a plan.
var intentQuestions = []intentQuestion{
	{rolemanager.IntentAgent, "The user wants a general interactive coding request that needs no formal plan and no tracked goal."},
	{rolemanager.IntentPlan, "The user wants a read-only investigation that should first produce a step-by-step plan before any changes."},
	{rolemanager.IntentGoal, "The user wants a specific objective to be tracked and completed."},
	{rolemanager.IntentDebug, "The user wants to reproduce, isolate, and fix a bug or failure."},
	{rolemanager.IntentFanOut, "The user wants several independent read-only investigations in parallel before acting."},
}

// intentHandoffQuestion is offered only when a plan-file attachment exists.
var intentHandoffQuestion = intentQuestion{
	rolemanager.IntentHandoff,
	"The user wants an already-written plan in an attached file carried out now, step by step.",
}

// intentChoiceKey keys the single choice question the local backend is asked.
const intentChoiceKey = "intent"

// DetectIntent scores every offered intent. The hosted and self-hosted
// backends answer one noul question per intent. The local decision model
// answers one question per request, so it is asked a single choice question
// over the same intents instead; its probabilities sum to one. It returns a
// map of intent to probability, plus the model identity. A transport error or
// a malformed/out-of-range answer is returned as an error so the caller falls
// back to the LLM classifier.
func (c *Client) DetectIntent(ctx context.Context, in rolemanager.DetectInput) (map[rolemanager.Intent]float64, string, error) {
	req := c.buildRequest(in)
	answers, err := c.decide(ctx, req)
	if err != nil {
		return nil, "", err
	}
	scores, err := readIntentScores(answers, req.Questions)
	if err != nil {
		return nil, "", err
	}
	if c.backend != nil {
		return scores, c.backend.Identity(), nil
	}
	return scores, c.model, nil
}

func (c *Client) offeredIntents(in rolemanager.DetectInput) []intentQuestion {
	qs := append([]intentQuestion(nil), intentQuestions...)
	if in.PlanAttachment != nil {
		qs = append(qs, intentHandoffQuestion)
	}
	return qs
}

func (c *Client) buildRequest(in rolemanager.DetectInput) decisions.Request {
	offered := c.offeredIntents(in)
	questions := make(map[string]decisions.Question, len(offered))
	if c.Local() {
		q := decisions.Question{
			Type:         decisions.TypeChoice,
			Instructions: "Which kind of work does the user want next?",
			Descriptions: map[string]string{},
		}
		for _, iq := range offered {
			q.Options = append(q.Options, string(iq.key))
			q.Descriptions[string(iq.key)] = iq.instructions
		}
		questions[intentChoiceKey] = q
	} else {
		for _, iq := range offered {
			questions[string(iq.key)] = decisions.Noul(iq.instructions)
		}
	}

	attachments := []map[string]any{}
	if in.PlanAttachment != nil {
		attachments = append(attachments, map[string]any{
			"kind":  "plan_file",
			"tasks": in.PlanAttachment.Tasks,
		})
	}
	state := map[string]any{
		"prompt":                        in.Prompt,
		"current_mode":                  string(in.ModeHint.Mode),
		"current_mode_selected_by_user": in.ModeHint.Sticky,
		"attachments":                   attachments,
	}
	return decisions.Request{Questions: questions, State: state}
}

// readIntentScores reads one probability per offered intent: one noul answer
// per question, or the per-option probabilities of the single choice
// question. A missing answer or a probability outside [0,1] is an error.
func readIntentScores(answers map[string]decisions.Answer, questions map[string]decisions.Question) (map[rolemanager.Intent]float64, error) {
	if q, ok := questions[intentChoiceKey]; ok && q.Type == decisions.TypeChoice {
		a, ok := answers[intentChoiceKey]
		if !ok || a.Type != decisions.TypeChoice {
			return nil, fmt.Errorf("jev intent detection: missing intent answer")
		}
		scores := make(map[rolemanager.Intent]float64, len(q.Options))
		for _, o := range q.Options {
			p, ok := a.Probabilities[o]
			if !ok || p < 0 || p > 1 {
				return nil, fmt.Errorf("jev intent detection: missing or out-of-range %s", o)
			}
			scores[rolemanager.Intent(o)] = p
		}
		return scores, nil
	}
	scores := make(map[rolemanager.Intent]float64, len(questions))
	for key := range questions {
		n, err := noulOf(answers, key)
		if err != nil {
			return nil, fmt.Errorf("jev intent detection: %w", err)
		}
		scores[rolemanager.Intent(key)] = n
	}
	return scores, nil
}
