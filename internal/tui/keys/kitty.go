package keys

import (
	"bytes"
	"os"
)

// Writer wraps an *os.File used as the bubbletea output so it can inject or
// remove the kitty keyboard-protocol push/pop sequences around alt-screen
// enter/exit. It satisfies term.File requirements because it embeds *os.File.
type Writer struct {
	*os.File
	Flags int
}

var (
	altScreenEnter = []byte("\x1b[?1049h")
	altScreenExit  = []byte("\x1b[?1049l")
)

// Write intercepts alt-screen enter/exit control strings and inserts the
// corresponding kitty stack operation. It always reports len(p), nil so a
// failed push can never break normal alt-screen handling.
func (w *Writer) Write(p []byte) (int, error) {
	switch {
	case bytes.Equal(p, altScreenEnter):
		_, _ = w.File.Write(p)
		_, _ = w.File.Write([]byte(Enter))
	case bytes.Equal(p, altScreenExit):
		_, _ = w.File.Write([]byte(Pop))
		_, _ = w.File.Write(p)
	default:
		return w.File.Write(p)
	}
	return len(p), nil
}
