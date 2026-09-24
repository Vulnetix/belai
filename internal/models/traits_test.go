package models

import "testing"

func TestDefaultEfforts(t *testing.T) {
	got := DefaultEfforts()
	if len(got) != 3 || got[0] != "low" || got[1] != "medium" || got[2] != "high" {
		t.Fatalf("DefaultEfforts() = %v, want [low medium high]", got)
	}
	// Returned slice must be the shared default set.
	if &got[0] != &defaultEfforts[0] {
		t.Fatalf("DefaultEfforts() returned a copy, want the shared slice")
	}
}

func TestContextWindowFor(t *testing.T) {
	// The static catalogue declares no context windows, so every lookup is 0;
	// the function must still walk the catalog and fall back safely.
	if got := ContextWindowFor("openai", "gpt-5"); got != 0 {
		t.Fatalf("ContextWindowFor(openai, gpt-5) = %d, want 0", got)
	}
	if got := ContextWindowFor("openai", "no-such-model"); got != 0 {
		t.Fatalf("ContextWindowFor(openai, no-such-model) = %d, want 0", got)
	}
	if got := ContextWindowFor("bogus", "gpt-5"); got != 0 {
		t.Fatalf("ContextWindowFor(bogus, gpt-5) = %d, want 0", got)
	}
}

func TestCatalogMaxOutput(t *testing.T) {
	cases := []struct {
		provider string
		model    string
		want     int
	}{
		{"openai", "gpt-5", 128000},
		{"openai", "gpt-4.1", 32768},
		{"anthropic", "claude-opus-5-5", 128000},
		{"anthropic", "claude-sonnet-5", 64000},
		{"google-gemini", "gemini-2.5-flash", 65536},
		// Unknown model in a known provider: no catalogue entry, no id rules.
		{"openai", "unknown-model", 0},
		// Known model, unknown provider.
		{"bogus", "gpt-5", 0},
	}
	for _, tc := range cases {
		if got := CatalogMaxOutput(tc.provider, tc.model); got != tc.want {
			t.Errorf("CatalogMaxOutput(%q, %q) = %d, want %d", tc.provider, tc.model, got, tc.want)
		}
	}
}

func TestMaxOutput(t *testing.T) {
	cases := []struct {
		provider string
		model    string
		want     int
	}{
		// Static catalogue wins.
		{"openai", "gpt-5", 128000},
		{"anthropic", "claude-opus-4-5", 64000},
		// Not in catalogue: Claude id rules apply (live-fetched / gateway ids).
		{"anthropic", "claude-opus-4-1", 32000},
		{"anthropic", "claude-3-7-sonnet", 64000},
		{"anthropic", "claude-3-5-sonnet", 8192},
		{"anthropic", "claude-3-opus", 4096},
		// Relay catalogue entry has no MaxOutput, id rules still resolve.
		{"openrouter", "anthropic/claude-3.5-sonnet", 8192},
		// Unknown model everywhere.
		{"openai", "unknown-model", 0},
		// Id rules apply regardless of provider, so a Claude id resolves even
		// for an unknown provider.
		{"bogus", "claude-opus-4-5", 64000},
		{"bogus", "unknown-model", 0},
	}
	for _, tc := range cases {
		if got := MaxOutput(tc.provider, tc.model); got != tc.want {
			t.Errorf("MaxOutput(%q, %q) = %d, want %d", tc.provider, tc.model, got, tc.want)
		}
	}
}

func TestThinking(t *testing.T) {
	cases := []struct {
		provider string
		model    string
		want     ThinkingStyle
	}{
		// Static catalogue entries.
		{"anthropic", "claude-opus-5-5", StyleAlways},
		{"anthropic", "claude-sonnet-5", StyleAdaptive},
		{"anthropic", "claude-opus-4-5", StyleBudget},
		// Non-Anthropic models have no thinking in the catalogue.
		{"openai", "gpt-5", StyleNone},
		// Not in catalogue: Claude id rules apply.
		{"anthropic", "claude-opus-4-1", StyleBudget},
		{"anthropic", "claude-3-7-sonnet", StyleBudget},
		{"anthropic", "claude-3-5-sonnet", StyleNone},
		{"anthropic", "claude-fable-5-1", StyleAlways},
		// Unknown model everywhere.
		{"openai", "unknown-model", StyleNone},
		// Id rules apply regardless of provider, so a Claude id resolves even
		// for an unknown provider.
		{"bogus", "claude-opus-4-5", StyleBudget},
		{"bogus", "unknown-model", StyleNone},
	}
	for _, tc := range cases {
		if got := Thinking(tc.provider, tc.model); got != tc.want {
			t.Errorf("Thinking(%q, %q) = %q, want %q", tc.provider, tc.model, got, tc.want)
		}
	}
}

func TestClaudeTraits(t *testing.T) {
	cases := []struct {
		id       string
		wantOK   bool
		maxOut   int
		thinking ThinkingStyle
	}{
		// Old naming: claude-3-5-sonnet etc.
		{"claude-3-5-sonnet", true, 8192, StyleNone},
		{"claude-3-5-haiku", true, 8192, StyleNone},
		{"claude-3-opus", true, 4096, StyleNone},
		{"claude-3-7-sonnet", true, 64000, StyleBudget},
		{"claude-3-7-haiku", true, 64000, StyleBudget},
		{"claude-3-5-sonnet-20241022", true, 8192, StyleNone},
		// New naming: claude-{family}-{major}-{minor}.
		{"claude-fable-5-1", true, 128000, StyleAlways},
		{"claude-opus-6", true, 128000, StyleAlways},
		{"claude-opus-5-5", true, 128000, StyleAlways},
		{"claude-sonnet-5", true, 64000, StyleAdaptive},
		{"claude-opus-4-6", true, 128000, StyleAdaptive},
		{"claude-sonnet-4-6", true, 64000, StyleAdaptive},
		{"claude-opus-4-5", true, 64000, StyleBudget},
		{"claude-opus-4-1", true, 32000, StyleBudget},
		{"claude-opus-4", true, 32000, StyleBudget},
		{"claude-haiku-4-5", true, 64000, StyleBudget},
		// Date snapshot in the minor slot is treated as minor 0.
		{"claude-opus-4-20250514", true, 32000, StyleBudget},
		// Provider prefix is tolerated.
		{"anthropic/claude-3-5-sonnet", true, 8192, StyleNone},
		// Case-insensitive.
		{"Claude-Opus-4-5", true, 64000, StyleBudget},
		// New-name regex matches but no family rule applies (major < 4).
		{"claude-opus-3", false, 0, StyleNone},
		{"claude-haiku-2", false, 0, StyleNone},
		// Not a Claude id.
		{"gpt-5", false, 0, StyleNone},
		{"unknown", false, 0, StyleNone},
		{"", false, 0, StyleNone},
	}
	for _, tc := range cases {
		got, ok := claudeTraits(tc.id)
		if ok != tc.wantOK || got.maxOutput != tc.maxOut || got.thinking != tc.thinking {
			t.Errorf("claudeTraits(%q) = (%+v, %v), want ({%d %q}, %v)",
				tc.id, got, ok, tc.maxOut, tc.thinking, tc.wantOK)
		}
	}
}

func TestMinorOf(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"5", 5},
		{"12", 12},
		{"20250514", 0}, // snapshot date, not a minor
		{"999", 0},      // too long to be a minor
		{"abc", 0},      // non-numeric
	}
	for _, tc := range cases {
		if got := minorOf(tc.in); got != tc.want {
			t.Errorf("minorOf(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
