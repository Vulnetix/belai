package rolemanager

import "testing"

// TestClassifierPayloadsAreToolSkillAgentFree pins the invariant that every
// classifier turn stays tool-less, skill-less, and agent-less. Adding a skills
// carrier to prompt.Options must not leak into any of these builders.
func TestClassifierPayloadsAreToolSkillAgentFree(t *testing.T) {
	cases := []struct {
		name    string
		payload ClassifierPayload
	}{
		{"security", BuildClassifierPayload("untrusted tool output")},
		{"mode", BuildModeClassifierPayload("classify my prompt")},
		{"session name", BuildSessionNamePayload("first user message", false)},
		{"session name caveman", BuildSessionNamePayload("first user message", true)},
		{"compaction", BuildCompactionPayload("<conversation>", false)},
		{"compaction caveman", BuildCompactionPayload("<conversation>", true)},
		{"goal evaluator", BuildGoalEvalPayload(GoalEvalInput{Goal: "g", Todos: "t", Evidence: "e"})},
		{"plan evaluator", BuildPlanEvalPayload(PlanEvalInput{Context: "c", Todos: "t", Evidence: "e"})},
		{"agent loop evaluator", BuildAgentEvalPayload("goals", "output")},
		{"clarify", BuildClarifyPayload(ClarifyInput{Prompt: "p", Findings: "f", Round: "1"})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.payload.Tools) != 0 {
				t.Fatalf("classifier payload carries tools: %+v", tc.payload.Tools)
			}
			if len(tc.payload.Skills) != 0 {
				t.Fatalf("classifier payload carries skills: %+v", tc.payload.Skills)
			}
			if tc.payload.Agent != "" {
				t.Fatalf("classifier payload carries an agent block: %q", tc.payload.Agent)
			}
		})
	}
}
