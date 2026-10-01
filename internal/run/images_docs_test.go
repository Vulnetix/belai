package run

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestImagesPageStatesTheTokenAndKiroLimits pins the token estimate and the
// Kiro size limit docs/image-attachments.md states to the code.
func TestImagesPageStatesTheTokenAndKiroLimits(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/image-attachments.md")), " ")
	for _, want := range []string{
		"its area divided by 750 pixels, at least 85 and at most 1600 tokens",
		"(over 3.75 MB, or a format it does not list)",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/image-attachments.md does not say %q", want)
		}
	}
	if imageTokenPixels != 750 || imageTokenFloor != 85 || imageTokenCap != 1600 || kiroMaxImageBytes != 3_750_000 {
		t.Errorf("constants %d/%d/%d/%d disagree with the page", imageTokenPixels, imageTokenFloor, imageTokenCap, kiroMaxImageBytes)
	}
}
