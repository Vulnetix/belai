package imageguard

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestImagesPageStatesTheAdmissionLimits pins the numbers docs/image-attachments.md
// gives for the admission gate to the limits the gate enforces.
func TestImagesPageStatesTheAdmissionLimits(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/image-attachments.md")), " ")
	for _, want := range []string{
		"pixel budget (50 million)",
		"capped at 32 MiB",
		"long edge of 1568 pixels",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/image-attachments.md does not say %q", want)
		}
	}
	if Default.MaxPixels != 50_000_000 || Default.MaxBytes != 32<<20 || Default.MaxEdge != 1568 {
		t.Errorf("Default limits are %+v, but the page says 50 million pixels, 32 MiB and 1568 pixels", Default)
	}
}
