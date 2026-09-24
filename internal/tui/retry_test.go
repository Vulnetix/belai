package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/signet/internal/agent"
)

// TestRetryLineShowsLayerBudget pins that the retry system line renders the
// retrying layer's real budget, and drops the denominator when it is unknown.
func TestRetryLineShowsLayerBudget(t *testing.T) {
	cases := []struct {
		max  int
		want string
	}{
		{3, "retrying (2/3) after 500ms — connection/stream reset"},
		{0, "retrying (2) after 500ms — connection/stream reset"},
	}
	for _, tc := range cases {
		a := newPersistApp(t)
		a.handleAgentEvent(agentEventMsg{Kind: agent.EventRetryKind, RetryAttempt: 2, RetryMax: tc.max, RetryDelay: 500 * time.Millisecond, RetryReason: "connection/stream reset"})
		var found bool
		for _, m := range a.messages {
			if m.Role == "system" && strings.Contains(m.Content, tc.want) {
				found = true
			}
		}
		if !found {
			t.Fatalf("max=%d: no system line %q in %+v", tc.max, tc.want, a.messages)
		}
	}
}
