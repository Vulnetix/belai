// Package clipboard copies text to the system clipboard, falling back to the
// OSC 52 terminal escape sequence when no native clipboard is reachable
// (headless hosts, SSH sessions, containers).
package clipboard

import (
	"github.com/atotto/clipboard"
	"github.com/muesli/termenv"
)

// Copy writes text to the system clipboard, falling back to the OSC 52
// terminal escape sequence. It returns the method used ("native" or "osc52")
// so callers can tell the user how the copy happened. termenv.Copy is
// best-effort and has no error return; OSC 52 is emitted unconditionally when
// the native clipboard is unreachable.
func Copy(s string) (method string, err error) {
	if err := clipboard.WriteAll(s); err == nil {
		return "native", nil
	}
	termenv.Copy(s)
	return "osc52", nil
}
