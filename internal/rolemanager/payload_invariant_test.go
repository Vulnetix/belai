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
		{"goal evaluator", BuildGoalEvalPayload(GoalEvalInput{Goal: "g", Todos: "t", Facts: "f", Evidence: "e"})},
		{"goal evaluator repair", BuildGoalEvalRepairPayload(GoalEvalInput{Goal: "g", Todos: "t", Facts: "f", Evidence: "e"}, "raw")},
		{"goal contract", BuildGoalDraftPayload(GoalDraftInput{Prompt: "ship it", VerificationSurface: []string{"go test ./..."}})},
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

// TestClassifierPayloadsCarryCategories pins the threat-category set each
// security payload asks the Jev security classifier to rule on, so a new
// security builder also declares its categories.
func TestClassifierPayloadsCarryCategories(t *testing.T) {
	cases := []struct {
		name    string
		payload ClassifierPayload
		want    []Sentinel
	}{
		{"security", BuildClassifierPayload("untrusted tool output"), []Sentinel{SentinelPromptInjection, SentinelJailbreak, SentinelDataExtraction, SentinelModelExtraction}},
		{"extraction", BuildExtractionPayload("untrusted tool output"), []Sentinel{SentinelDataExtraction, SentinelModelExtraction}},
		{"deferred extraction", BuildDeferredExtractionPayload("untrusted tool output"), []Sentinel{SentinelJailbreak, SentinelDataExtraction, SentinelModelExtraction}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.payload.Categories) != len(tc.want) {
				t.Fatalf("Categories = %v, want %v", tc.payload.Categories, tc.want)
			}
			for i := range tc.want {
				if tc.payload.Categories[i] != tc.want[i] {
					t.Fatalf("Categories = %v, want %v", tc.payload.Categories, tc.want)
				}
			}
		})
	}
}

// TestClassifierPayloadsCarryUseCase pins the routing hint attached to every
// role-manager activity so adding a new builder also adds a use-case value.
func TestClassifierPayloadsCarryUseCase(t *testing.T) {
	cases := []struct {
		name    string
		payload ClassifierPayload
		want    string
	}{
		{"security", BuildClassifierPayload("untrusted tool output"), ""},
		{"mode", BuildModeClassifierPayload("classify my prompt"), UseCaseModeEval},
		{"session name", BuildSessionNamePayload("first user message", false), UseCaseSessionName},
		{"session name caveman", BuildSessionNamePayload("first user message", true), UseCaseSessionName},
		{"compaction", BuildCompactionPayload("<conversation>", false), UseCaseCompaction},
		{"compaction caveman", BuildCompactionPayload("<conversation>", true), UseCaseCompaction},
		{"goal evaluator", BuildGoalEvalPayload(GoalEvalInput{Goal: "g", Todos: "t", Facts: "f", Evidence: "e"}), UseCaseGoalEval},
		{"goal evaluator repair", BuildGoalEvalRepairPayload(GoalEvalInput{Goal: "g", Todos: "t", Facts: "f", Evidence: "e"}, "raw"), UseCaseGoalEval},
		{"goal contract", BuildGoalDraftPayload(GoalDraftInput{Prompt: "ship it", VerificationSurface: []string{"go test ./..."}}), UseCaseGoalContract},
		{"plan evaluator", BuildPlanEvalPayload(PlanEvalInput{Context: "c", Todos: "t", Evidence: "e"}), UseCasePlanEval},
		{"agent loop evaluator", BuildAgentEvalPayload("goals", "output"), UseCaseAgentEval},
		{"clarify", BuildClarifyPayload(ClarifyInput{Prompt: "p", Findings: "f", Round: "1"}), UseCaseClarify},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.payload.UseCase != tc.want {
				t.Fatalf("UseCase = %q, want %q", tc.payload.UseCase, tc.want)
			}
		})
	}
}
