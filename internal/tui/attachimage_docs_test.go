package tui

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestImagesPageStatesThePromptImageCap pins the per-prompt image cap the page
// gives to the one the composer enforces.
func TestImagesPageStatesThePromptImageCap(t *testing.T) {
	doc := docparity.Read(t, "docs/image-attachments.md")
	if !strings.Contains(doc, "**At most eight images per prompt.**") || maxAttachedImages != 8 {
		t.Errorf("the page says eight images per prompt; the composer allows %d", maxAttachedImages)
	}
}
