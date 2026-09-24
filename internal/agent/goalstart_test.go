package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
)

// TestJoinGoalDraftNamesTheFailure checks that a failed draft says why it
// failed — a bare "drafting failed" left a slow model's timeout undiagnosable —
// and that a provider failure carries its status but never its body.
func TestJoinGoalDraftNamesTheFailure(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{fmt.Errorf("goal contract draft: %w", context.DeadlineExceeded), "timed out after " + goalDraftTimeout.String()},
		{rolemanager.ErrGoalDraftUnusable, "draft was unusable"},
		{fmt.Errorf("goal contract draft: %w", &run.ProviderError{Status: 503, Body: "secret body"}), "provider returned 503"},
		{fmt.Errorf("goal contract draft: %w", &run.ProviderError{Err: errors.New("dial tcp")}), "provider unreachable"},
		{errors.New("other"), "goal contract drafting failed; carrying the raw prompt"},
	}
	s := &Session{}
	for _, c := range cases {
		ch := make(chan goalDraft, 1)
		ch <- goalDraft{err: c.err}
		var warnings []string
		got := s.joinGoalDraft(ch, "ship it", func(e Event) { warnings = append(warnings, e.Warning) })
		if got != "ship it" {
			t.Fatalf("%v: goal text = %q, want the raw prompt", c.err, got)
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], c.want) {
			t.Fatalf("%v: warnings = %q, want one containing %q", c.err, warnings, c.want)
		}
		if strings.Contains(warnings[0], "secret body") {
			t.Fatalf("warning leaked the provider body: %q", warnings[0])
		}
	}
}
