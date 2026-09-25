package rolemanager

import "testing"

// TestParseDeferredExtractionSentinel pins the deferred phase-3 token set: no
// local gate ruled on jailbreak or instruction injection, so all five tokens
// are verdicts.
func TestParseDeferredExtractionSentinel(t *testing.T) {
	valid := map[string]Sentinel{
		"SAFE":                              SentinelSafe,
		"PROMPT_INJECTION":                  SentinelPromptInjection,
		"JAILBREAK":                         SentinelJailbreak,
		"DATA_EXTRACTION":                   SentinelDataExtraction,
		"MODEL_EXTRACTION":                  SentinelModelExtraction,
		"<thinking>…</thinking>\nJAILBREAK": SentinelJailbreak,
	}
	for in, want := range valid {
		got, err := ParseDeferredExtractionSentinel(in)
		if err != nil {
			t.Fatalf("ParseDeferredExtractionSentinel(%q): %v", in, err)
		}
		if got != want {
			t.Fatalf("ParseDeferredExtractionSentinel(%q) = %q, want %q", in, got, want)
		}
	}

	bad := []string{"", "safe", "SAFE\nJAILBREAK", "SAFE DATA_EXTRACTION"}
	for _, in := range bad {
		if _, err := ParseDeferredExtractionSentinel(in); err == nil {
			t.Fatalf("ParseDeferredExtractionSentinel(%q) expected error", in)
		}
	}
}
