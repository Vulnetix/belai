package rolemanager

import "testing"

func TestBuildClassifierPayloadHasZeroToolsSkillsAgent(t *testing.T) {
	p := BuildClassifierPayload("some untrusted content")

	if len(p.Tools) != 0 {
		t.Fatalf("classifier payload must have zero tools, got %d", len(p.Tools))
	}
	if len(p.Skills) != 0 {
		t.Fatalf("classifier payload must have zero skills, got %d", len(p.Skills))
	}
	if p.Agent != "" {
		t.Fatalf("classifier payload must have no agent block, got %q", p.Agent)
	}
	if p.System == "" {
		t.Fatalf("classifier payload must carry a system prompt")
	}
	if p.User != "some untrusted content" {
		t.Fatalf("classifier user content = %q", p.User)
	}

	msgs := p.Messages()
	if len(msgs) != 2 {
		t.Fatalf("expected system+user messages, got %d", len(msgs))
	}
	if msgs[0].Role != "system" || msgs[1].Role != "user" {
		t.Fatalf("message order/roles wrong: %+v", msgs)
	}
	if msgs[1].Content != "some untrusted content" {
		t.Fatalf("user message content = %q", msgs[1].Content)
	}
}

func TestParseSentinelValid(t *testing.T) {
	cases := []struct {
		in   string
		want Sentinel
	}{
		{"SAFE", SentinelSafe},
		{"  SAFE\n", SentinelSafe},
		{"SAFE.", SentinelSafe},
		{"**SAFE**", SentinelSafe},
		{"`SAFE`", SentinelSafe},
		{"```\nSAFE\n```", SentinelSafe},
		{"<thinking>…</thinking>\nSAFE", SentinelSafe},
		{"PROMPT_INJECTION", SentinelPromptInjection},
		{"JAILBREAK", SentinelJailbreak},
		{"DATA_EXTRACTION", SentinelDataExtraction},
		{"MODEL_EXTRACTION", SentinelModelExtraction},
	}
	for _, tc := range cases {
		got, err := ParseSentinel(tc.in)
		if err != nil {
			t.Fatalf("ParseSentinel(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("ParseSentinel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseSentinelRejectsMalformed(t *testing.T) {
	bad := []string{
		"",
		"safe",
		"sAfE",
		"yes",
		"the content is safe",
		"SAFE PROMPT_INJECTION",
		"SAFE\nPROMPT_INJECTION",
	}
	for _, in := range bad {
		if _, err := ParseSentinel(in); err == nil {
			t.Fatalf("ParseSentinel(%q) expected error", in)
		}
	}
}

// The shared normalizer/matcher accepts only reasoning wrappers and markdown
// around an otherwise-standalone token. A token inside prose, or ambiguity,
// still fails closed.
func TestMatchSentinelNormalizationTable(t *testing.T) {
	allowed := []string{"SAFE", "GOAL_PARTIAL", "GOAL_COMPLETE"}
	cases := []struct {
		raw  string
		want string
		ok   bool
	}{
		{"<thinking>…</thinking>\nGOAL_PARTIAL", "GOAL_PARTIAL", true},
		{"**GOAL_PARTIAL**", "GOAL_PARTIAL", true},
		{"```\nSAFE\n```", "SAFE", true},
		{"GOAL_PARTIAL or GOAL_COMPLETE", "", false},
		{"maybe SAFE?", "", false},
		{"<thinking>unterminated", "", false},
	}
	for _, c := range cases {
		got, err := matchSentinel(c.raw, allowed)
		if c.ok {
			if err != nil || got != c.want {
				t.Fatalf("matchSentinel(%q) = %q, %v; want %q", c.raw, got, err, c.want)
			}
		} else if err == nil {
			t.Fatalf("matchSentinel(%q) = %q, want error", c.raw, got)
		}
	}
}

func TestSentinelIsSafe(t *testing.T) {
	if !SentinelSafe.IsSafe() {
		t.Fatalf("SAFE.IsSafe() = false")
	}
	if SentinelPromptInjection.IsSafe() {
		t.Fatalf("PROMPT_INJECTION.IsSafe() = true")
	}
}

func TestParseExtractionSentinelValid(t *testing.T) {
	cases := []struct {
		in   string
		want Sentinel
	}{
		{"SAFE", SentinelSafe},
		{"PROMPT_INJECTION", SentinelPromptInjection},
		{"DATA_EXTRACTION", SentinelDataExtraction},
		{"MODEL_EXTRACTION", SentinelModelExtraction},
		{"<thinking>…</thinking>\nSAFE", SentinelSafe},
	}
	for _, tc := range cases {
		got, err := ParseExtractionSentinel(tc.in)
		if err != nil {
			t.Fatalf("ParseExtractionSentinel(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("ParseExtractionSentinel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseExtractionSentinelRejectsOutOfScope(t *testing.T) {
	// Jailbreak is owned by the local phase-2 gate, so it is out of scope for
	// phase 3 and must be rejected, not accepted as a verdict.
	bad := []string{"", "JAILBREAK", "SAFE\nDATA_EXTRACTION"}
	for _, in := range bad {
		if _, err := ParseExtractionSentinel(in); err == nil {
			t.Fatalf("ParseExtractionSentinel(%q) expected error", in)
		}
	}
}
