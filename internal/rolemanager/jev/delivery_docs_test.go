package jev

import (
	"fmt"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/docparity"
)

// docs/jev-jobs.md states each delivery cut-off with its default; these tests
// fail when the code and the page disagree.
func TestDeliveryJobsAndCutoffsAreDocumented(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/jev-jobs.md")), " ")
	d := config.DefaultJevThresholds()
	for _, want := range []string{
		"## Delivery crew jobs",
		"### Handoff clarity",
		"### Gate alignment",
		"### Request coverage",
		fmt.Sprintf("`clear_at` (%.2f)", d.ClearAt),
		fmt.Sprintf("`align_at` (%.2f)", d.AlignAt),
		fmt.Sprintf("`cover_at` (%.2f)", d.CoverAt),
		fmt.Sprintf("| `clear_at`, `align_at`, `cover_at` | %.2f, %.2f, %.2f |", d.ClearAt, d.AlignAt, d.CoverAt),
		"a timeout (8 seconds)",
		"review: the decision model rated it unclear",
		"review: gate G1 may not measure its title",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/jev-jobs.md lacks %q", want)
		}
	}
	for _, ev := range []string{"handoff_clarity", "gate_alignment", "request_coverage"} {
		if !strings.Contains(doc, "Recorded as a `"+ev+"` event") {
			t.Errorf("docs/jev-jobs.md does not describe the %s event", ev)
		}
	}
}

func TestDeliveryDefaultsSitOnTheSafeSideOfHalf(t *testing.T) {
	d := config.DefaultJevThresholds()
	for name, v := range map[string]float64{"clear_at": d.ClearAt, "align_at": d.AlignAt, "cover_at": d.CoverAt} {
		if v <= 0 || v > 0.5 {
			t.Errorf("%s default %v: a delivery cut-off only narrows, so its default asks for a clear objection, not a coin flip", name, v)
		}
	}
}
