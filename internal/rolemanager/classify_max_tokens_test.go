package rolemanager

import "testing"

// TestStructuredPayloadsOverrideClassifierBudget pins the completion-budget
// contract at the payload level: structured-output builders (compaction and
// clarification) carry a larger per-call budget than the single-token
// sentinel default, because their replies are multi-token summaries or JSON.
func TestStructuredPayloadsOverrideClassifierBudget(t *testing.T) {
	cases := []struct {
		name    string
		payload ClassifierPayload
	}{
		{"compaction", BuildCompactionPayload("<conversation>", false)},
		{"compaction caveman", BuildCompactionPayload("<conversation>", true)},
		{"clarify", BuildClarifyPayload(ClarifyInput{Prompt: "p", Findings: "f", Round: "1"})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.payload.MaxTokens != ClassifierStructuredMaxTokens {
				t.Fatalf("MaxTokens = %d, want %d", tc.payload.MaxTokens, ClassifierStructuredMaxTokens)
			}
		})
	}
}

// TestSentinelPayloadsUseDefaultClassifierBudget pins the inverse: single-token
// sentinel builders leave MaxTokens zero so the classifier's configured
// default (run.ClassifierMaxTokens) applies.
func TestSentinelPayloadsUseDefaultClassifierBudget(t *testing.T) {
	cases := map[string]ClassifierPayload{
		"security":             BuildClassifierPayload("untrusted tool output"),
		"mode":                 BuildModeClassifierPayload("classify my prompt"),
		"session name":         BuildSessionNamePayload("first user message", false),
		"session name caveman": BuildSessionNamePayload("first user message", true),
		"goal evaluator": BuildGoalEvalPayload(GoalEvalInput{
			Goal: "g", Todos: "t", Evidence: "e",
		}),
		"plan evaluator": BuildPlanEvalPayload(PlanEvalInput{
			Context: "c", Todos: "t", Evidence: "e",
		}),
		"agent loop evaluator": BuildAgentEvalPayload("goals", "output"),
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			if p.MaxTokens != 0 {
				t.Fatalf("MaxTokens = %d, want 0 (classifier default)", p.MaxTokens)
			}
		})
	}
}
