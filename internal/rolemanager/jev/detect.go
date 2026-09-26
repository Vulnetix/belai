// Package jev implements the Role Manager Jev tool-call gate and the Jev
// security classifier. Both ask the TypeSafe/Jev decision model on
// OpenRouter's Decisions API a set of boolean ("noul") questions and reduce
// the returned probabilities to strict single-token verdicts. Neither turn
// ever carries tools, skills, or an agent block.
package jev

import (
	"context"
	"fmt"

	"github.com/OpenRouterTeam/go-sdk/models/components"

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

// DetectIntent scores every offered intent with one noul question per intent.
// It returns a map of intent to probability, plus the model identity. A
// transport error or a malformed/out-of-range answer is returned as an error
// so the caller falls back to the LLM classifier.
func (c *Client) DetectIntent(ctx context.Context, in rolemanager.DetectInput) (map[rolemanager.Intent]float64, string, error) {
	req := c.buildRequest(in)
	sdk := c.decisionsSDK()
	resp, err := createDecision(ctx, sdk, req, c.endpoint)
	if err != nil {
		return nil, "", err
	}
	scores, err := readIntentScores(resp.Answers, req.Questions)
	if err != nil {
		return nil, "", err
	}
	return scores, c.model, nil
}

func (c *Client) buildRequest(in rolemanager.DetectInput) components.DecisionsRequest {
	questions := make(map[string]components.Questions, len(intentQuestions)+1)
	for _, q := range intentQuestions {
		questions[string(q.key)] = components.CreateQuestionsNoul(components.DecisionsNoulQuestion{
			Instructions: components.CreateDecisionsNoulQuestionInstructionsStr(q.instructions),
		})
	}
	if in.PlanAttachment != nil {
		q := intentHandoffQuestion
		questions[string(q.key)] = components.CreateQuestionsNoul(components.DecisionsNoulQuestion{
			Instructions: components.CreateDecisionsNoulQuestionInstructionsStr(q.instructions),
		})
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

	return components.DecisionsRequest{
		Model:     c.model,
		Questions: questions,
		State:     components.CreateStateMapOfAny(state),
	}
}

// readIntentScores reads one probability per question. A missing answer, a
// non-noul answer, or a probability outside [0,1] is an error.
func readIntentScores(answers map[string]components.Answers, questions map[string]components.Questions) (map[rolemanager.Intent]float64, error) {
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
