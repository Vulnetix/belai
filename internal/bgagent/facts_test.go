package bgagent

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/agentprofile"
)

func factsProfile() agentprofile.AgentProfile {
	return agentprofile.AgentProfile{
		Name:         "ops",
		Description:  "d",
		SystemPrompt: "Check the logs.",
		Mode:         agentprofile.ModeSingle,
		Facts: agentprofile.Facts{
			"aws_region":      {"eu-west-2"},
			"aws_external_id": {"ext-1234"},
			"environment":     {"prod"},
		},
	}
}

// A background agent's turn carries its facts after the system prompt, minus
// a hidden one, so the model knows where it works.
func TestProfilePromptCarriesVisibleFacts(t *testing.T) {
	got := profilePrompt(factsProfile())
	if !strings.HasPrefix(got, "Check the logs.\n\nFacts the profile author declared") {
		t.Fatalf("the facts should follow the system prompt:\n%s", got)
	}
	for _, want := range []string{"- aws_region: eu-west-2", "- environment: prod"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "ext-1234") || strings.Contains(got, "aws_external_id") {
		t.Errorf("a hidden fact reached the prompt:\n%s", got)
	}
}

func TestProfilePromptWithoutFactsIsTheSystemPrompt(t *testing.T) {
	p := factsProfile()
	p.Facts = nil
	if got := profilePrompt(p); got != "Check the logs." {
		t.Fatalf("prompt = %q", got)
	}
}

// Reflection puts its preamble ahead of everything, the facts included.
func TestProfilePromptReflectionComesFirst(t *testing.T) {
	p := factsProfile()
	p.Reflection = true
	got := profilePrompt(p)
	if !strings.HasPrefix(got, "Before acting, emit a <thinking> block") {
		t.Fatalf("reflection preamble missing:\n%s", got)
	}
	if i, j := strings.Index(got, "Check the logs."), strings.Index(got, "Facts the profile"); i < 0 || j < i {
		t.Fatalf("order should be preamble, prompt, facts:\n%s", got)
	}
}
