package proserules

import (
	"os"
	"strings"
	"testing"
)

func TestAppend(t *testing.T) {
	got := Append("Reply with one token.\n")
	if want := "Reply with one token.\n\n" + Rules + "\n"; got != want {
		t.Fatalf("Append = %q, want %q", got, want)
	}
	if again := Append(got); again != got {
		t.Fatal("Append twice added the rules twice")
	}
	if Append("") != "" || Append("  \n") != "  \n" {
		t.Fatal("Append gave an empty system prompt the rules")
	}
}

func TestRulesHaveNoDashPunctuation(t *testing.T) {
	if strings.ContainsAny(Rules, "—–") {
		t.Fatal("Rules contains an em or en dash")
	}
	if !strings.HasPrefix(Rules, "Prose rules.") {
		t.Fatalf("Rules starts %q", Rules[:20])
	}
}

// AGENTS.md carries the same text for people and for coding agents; the two must not drift.
func TestRulesMatchAgentsMd(t *testing.T) {
	raw, err := os.ReadFile("../../AGENTS.md")
	if err != nil {
		t.Skip("AGENTS.md not found")
	}
	_, section, ok := strings.Cut(string(raw), "\n## Prose rules\n")
	if !ok {
		t.Fatal("AGENTS.md has no Prose rules section")
	}
	if !strings.Contains(section, "\n"+Rules+"\n") {
		t.Fatal("AGENTS.md Prose rules section differs from proserules.Rules")
	}
}
