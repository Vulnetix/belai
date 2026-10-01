package agent

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestHooksPageStatesTheRunnerBounds pins the default timeout, the output cap
// and the tool result summary size docs/hooks.md states.
func TestHooksPageStatesTheRunnerBounds(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/hooks.md")), " ")
	for _, want := range []string{
		"the first 2 KiB of the tool's output",
		"`timeout_ms` of 0 means the 5-second default",
		"Belai reads at most 64 KiB of a hook's stdout",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/hooks.md does not say %q", want)
		}
	}
	if hookTimeout.Seconds() != 5 || hookMaxBytes != 64*1024 || hookResultSummaryBytes != 2*1024 {
		t.Errorf("runner bounds %v/%d/%d disagree with the page", hookTimeout, hookMaxBytes, hookResultSummaryBytes)
	}
}
