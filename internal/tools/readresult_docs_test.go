package tools

import (
	"fmt"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestContextOffloadPageStatesTheReadResultLimits pins the slice size and the
// argument defaults docs/context-offload.md gives for ReadResult.
func TestContextOffloadPageStatesTheReadResultLimits(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/context-offload.md")), " ")
	for _, want := range []string{
		fmt.Sprintf("A slice is bounded at %d KiB", readResultMaxChars>>10),
		"context` lines around each (default 3)",
		"`limit` lines (default 200)",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/context-offload.md does not say %q", want)
		}
	}
	// The description the model reads carries the same defaults and the cap.
	def := ReadResultTool{}.Definition()
	ctx := def.Properties["context"].Description
	if !strings.Contains(ctx, "default 3, at most 20") {
		t.Errorf("ReadResult context description = %q", ctx)
	}
	if !strings.Contains(def.Properties["limit"].Description, "default 200") {
		t.Errorf("ReadResult limit description = %q", def.Properties["limit"].Description)
	}
}
