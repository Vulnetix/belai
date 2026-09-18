package rolemanager

import (
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/prompt"
)

// TestProseBuildersCarryCavemanVoice checks that the prose builders voice their
// system prompt when asked and leave it untouched when not.
func TestProseBuildersCarryCavemanVoice(t *testing.T) {
	builders := map[string]func(bool) ClassifierPayload{
		"compaction":   func(on bool) ClassifierPayload { return BuildCompactionPayload("<conversation>", on) },
		"session name": func(on bool) ClassifierPayload { return BuildSessionNamePayload("first message", on) },
	}
	for name, build := range builders {
		t.Run(name, func(t *testing.T) {
			off := build(false)
			if strings.Contains(off.System, prompt.CavemanVoice) {
				t.Fatalf("caveman off, system carries the voice: %q", off.System)
			}
			on := build(true)
			if !strings.Contains(on.System, prompt.CavemanVoice) {
				t.Fatalf("caveman on, system lacks the voice: %q", on.System)
			}
			if !strings.Contains(on.System, cavemanPreserve) {
				t.Fatalf("caveman on, system lacks the structure guard: %q", on.System)
			}
			if !strings.HasPrefix(on.System, off.System) {
				t.Fatalf("caveman on rewrote the base prompt: %q", on.System)
			}
		})
	}
}

// TestSentinelBuildersNeverCarryCavemanVoice is the security pin: a sentinel or
// strict-JSON reply is matched exactly, so no builder whose reply is parsed as
// a token or as JSON may ever be voiced.
func TestSentinelBuildersNeverCarryCavemanVoice(t *testing.T) {
	cases := map[string]ClassifierPayload{
		"security":             BuildClassifierPayload("untrusted tool output"),
		"mode":                 BuildModeClassifierPayload("classify my prompt"),
		"goal evaluator":       BuildGoalEvalPayload(GoalEvalInput{Goal: "g", Todos: "t", Evidence: "e"}),
		"plan evaluator":       BuildPlanEvalPayload(PlanEvalInput{Context: "c", Todos: "t", Evidence: "e"}),
		"agent loop evaluator": BuildAgentEvalPayload("goals", "output"),
		"clarify":              BuildClarifyPayload(ClarifyInput{Prompt: "p", Findings: "f", Round: "1"}),
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			if strings.Contains(p.System, prompt.CavemanVoice) {
				t.Fatalf("sentinel payload carries the caveman voice: %q", p.System)
			}
			if strings.Contains(strings.ToLower(p.System), "caveman") {
				t.Fatalf("sentinel payload mentions caveman: %q", p.System)
			}
		})
	}
}

// TestCavemanCompactionKeepsHeadingContract pins that the voiced compaction
// prompt still specifies every heading ValidateSummary requires, and still
// tells the model to keep them verbatim. Losing them fails compaction closed.
func TestCavemanCompactionKeepsHeadingContract(t *testing.T) {
	p := BuildCompactionPayload("<conversation>", true)
	for _, heading := range []string{"## Goal", "## Next Steps", "## Critical Context"} {
		if !strings.Contains(p.System, heading) {
			t.Fatalf("voiced compaction prompt lost %q: %q", heading, p.System)
		}
	}
	if !strings.Contains(p.System, cavemanPreserve) {
		t.Fatalf("voiced compaction prompt lacks the structure guard: %q", p.System)
	}
}

// TestValidateSummaryAcceptsVoicedSummary checks the end of that contract: a
// caveman-voiced summary that keeps its headings still validates.
func TestValidateSummaryAcceptsVoicedSummary(t *testing.T) {
	raw := strings.Join([]string{
		"## Goal",
		"Me fix classifier page.",
		"## Next Steps",
		"Me write test. Me run just check.",
		"## Critical Context",
		"File internal/tui/classifier_view.go new.",
	}, "\n")
	got, err := ValidateSummary(raw)
	if err != nil {
		t.Fatalf("ValidateSummary(voiced) = %v, want nil", err)
	}
	if !strings.Contains(got, "## Critical Context") {
		t.Fatalf("validated summary lost a heading: %q", got)
	}
}
