package tools

import (
	"fmt"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestScreenshotPageStatesTheLimits pins the numbers and the program lists
// docs/screenshots.md gives to the constants the tool enforces.
func TestScreenshotPageStatesTheLimits(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/screenshots.md")), " ")
	for _, want := range []string{
		fmt.Sprintf("default %d by %d, clamped to %d to %d and %d to %d",
			screenshotDefaultW, screenshotDefaultH, screenshotMinW, screenshotMaxW, screenshotMinH, screenshotMaxH),
		fmt.Sprintf("default 500, at most %d", screenshotMaxWaitMs),
		fmt.Sprintf("the newest %d are kept", screenshotKeepFiles),
		fmt.Sprintf("(%d seconds for the whole call)", int(screenshotTimeout.Seconds())),
		fmt.Sprintf("A capture over %d MiB", screenshotMaxFileMB),
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/screenshots.md does not say %q", want)
		}
	}
	// The browsers it tries, in order.
	var names []string
	for _, n := range chromeNames {
		names = append(names, "`"+n+"`")
	}
	list := strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
	if !strings.Contains(doc, "the first of "+list) {
		t.Errorf("docs/screenshots.md does not list the browsers %s in order", list)
	}
	// The desktop programs, by platform.
	for _, want := range []string{"`grim`", "`maim`, `scrot`, ImageMagick `import`, `gnome-screenshot`", "`screencapture`"} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/screenshots.md does not name the desktop program %s", want)
		}
	}
}
