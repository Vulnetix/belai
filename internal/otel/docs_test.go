package otel

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestTelemetryPageStatesTheExportLimits pins the batching interval, the span
// queue cap and the value rules docs/telemetry.md states.
func TestTelemetryPageStatesTheExportLimits(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/telemetry.md")), " ")
	for _, want := range []string{
		"batched on a background goroutine every 10 seconds",
		"at most 4096 spans wait between exports",
		"capped at 96 characters",
		"Each export request gives up after 10 seconds",
		"`<endpoint>/v1/traces` and `<endpoint>/v1/metrics`",
		"each run of other characters becomes a single `_`",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/telemetry.md does not say %q", want)
		}
	}
	if maxQueuedSpans != 4096 {
		t.Errorf("maxQueuedSpans = %d, the page says 4096", maxQueuedSpans)
	}
	if got := cleanValue("a  b"); got != "a_b" {
		t.Errorf("cleanValue(%q) = %q, the page says a_b", "a  b", got)
	}
	if got := cleanValue(strings.Repeat("x", 200)); len(got) != 96 {
		t.Errorf("a long value is cut at %d, the page says 96", len(got))
	}
}
