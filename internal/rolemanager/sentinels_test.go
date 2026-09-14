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
		"SAFE.",
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

func TestSentinelIsSafe(t *testing.T) {
	if !SentinelSafe.IsSafe() {
		t.Fatalf("SAFE.IsSafe() = false")
	}
	if SentinelPromptInjection.IsSafe() {
		t.Fatalf("PROMPT_INJECTION.IsSafe() = true")
	}
}
