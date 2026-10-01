package docsuite

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/docparity"
	"github.com/vulnetix/belai/internal/quality"
	"github.com/vulnetix/belai/internal/testrun"
)

// TestTestingPageStatesTheDefaultsAndBounds pins the numbers docs/testing.md
// gives for the pass: the setting defaults, the output bound and the quality
// record location and retention.
func TestTestingPageStatesTheDefaultsAndBounds(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/testing.md")), " ")
	for _, want := range []string{
		"| `tests.max_fix_passes` | integer | 3 |",
		"| `tests.timeout_seconds` | integer | 300 |",
		"output is kept head and tail up to 64 KiB",
		"`" + quality.Dir + "/<commit>.json` (the last twenty are kept)",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/testing.md does not say %q", want)
		}
	}
	if config.DefaultTestsMaxFixPasses != 3 || config.DefaultTestsTimeoutSeconds != 300 {
		t.Errorf("defaults %d/%d disagree with the page", config.DefaultTestsMaxFixPasses, config.DefaultTestsTimeoutSeconds)
	}
	if testrun.DefaultMaxBytes != 64*1024 {
		t.Errorf("DefaultMaxBytes = %d, the page says 64 KiB", testrun.DefaultMaxBytes)
	}
	if quality.KeepRecords != 20 {
		t.Errorf("KeepRecords = %d, the page says twenty", quality.KeepRecords)
	}
}
