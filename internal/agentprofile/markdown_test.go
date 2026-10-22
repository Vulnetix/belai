package agentprofile

import (
	"testing"
)

// TestMarshalMarkdownRoundTrip checks ParseMarkdown(MarshalMarkdown(p)) == p.
func TestMarshalMarkdownRoundTrip(t *testing.T) {
	p := AgentProfile{
		Name:         "roundtrip",
		DisplayName:  "Round Trip",
		Description:  "A profile for round-trip testing.",
		Mode:         ModeSingle,
		Autonomy:     AutonomySupervised,
		SystemPrompt: "You are a test agent.",
		Tools:        []string{"Read", "Grep"},
	}
	data, err := MarshalMarkdown(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseMarkdown(data)
	if err != nil {
		t.Fatalf("ParseMarkdown: %v\n%s", err, data)
	}
	if got.Name != p.Name || got.Mode != p.Mode || got.Autonomy != p.Autonomy {
		t.Fatalf("round-trip mismatch: got %+v want %+v", got, p)
	}
	if got.SystemPrompt != p.SystemPrompt {
		t.Fatalf("system prompt mismatch: got %q want %q", got.SystemPrompt, p.SystemPrompt)
	}
}

// TestParseMarkdownNeverPanics checks ParseMarkdown on malformed inputs.
func TestParseMarkdownNeverPanics(t *testing.T) {
	inputs := []string{"", "---", "---\nname: x", "not markdown", "---\n---\nbody"}
	for _, in := range inputs {
		_, _ = ParseMarkdown([]byte(in))
	}
}
