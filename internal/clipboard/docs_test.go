package clipboard

import (
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestImagesPageStatesTheClipboardLimits pins the clipboard paragraph of
// docs/image-attachments.md: the programs, the timeout and the size cap.
func TestImagesPageStatesTheClipboardLimits(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/image-attachments.md")), " ")
	for _, want := range []string{
		"`wl-paste` on Wayland, `xclip` on X11, `pngpaste` on macOS",
		"for at most five seconds and 32 MiB",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/image-attachments.md does not say %q", want)
		}
	}
	if readTimeout != 5*time.Second || MaxImageBytes != 32<<20 {
		t.Errorf("readTimeout %v and MaxImageBytes %d disagree with the page", readTimeout, MaxImageBytes)
	}
}
