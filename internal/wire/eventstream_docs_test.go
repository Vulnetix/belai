package wire

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestKiroPageStatesTheFrameCap pins the event-stream frame cap docs/kiro.md
// states.
func TestKiroPageStatesTheFrameCap(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/kiro.md")), " ")
	if !strings.Contains(doc, "A frame over 1 MiB") || MaxEventFrame != 1<<20 {
		t.Errorf("the page says a frame over 1 MiB ends the stream; MaxEventFrame is %d", MaxEventFrame)
	}
}
