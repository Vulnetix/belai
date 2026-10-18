package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/belai/internal/run"
)

// The cache line reports the last agent call's read share and the session's,
// ignores role-manager calls, and says so when the provider reports nothing.
func TestCacheStatsLine(t *testing.T) {
	var c cacheStats
	if got := ansi.Strip(c.line("anthropic")); !strings.Contains(got, "nothing sent yet") {
		t.Fatalf("empty = %q", got)
	}
	c.add(run.UsageEvent{Role: run.RoleAgent, Prompt: 1000})
	if got := ansi.Strip(c.line("ollama")); !strings.Contains(got, "not reported by ollama") {
		t.Fatalf("unreported = %q", got)
	}
	c.add(run.UsageEvent{Role: "security", Prompt: 500, CacheWrite: 500})
	c.add(run.UsageEvent{Role: run.RoleAgent, Prompt: 1000, CacheRead: 900, CacheWrite: 100})
	got := ansi.Strip(c.line("anthropic"))
	for _, want := range []string{"last call 90% read", "session 45% read over 2 calls"} {
		if !strings.Contains(got, want) {
			t.Fatalf("line %q lacks %q", got, want)
		}
	}
}
