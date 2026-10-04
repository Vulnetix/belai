package vaultenv

import (
	"io"
	"os"
	"sync"
)

var protectOnce sync.Once

// SkipProtect turns Protect off, for a test binary whose own output must stay as it is.
var SkipProtect bool

// Protect is called once, the first time this process holds a vault value. It marks
// the process non-dumpable (HardenProcess) and puts the scrubber in front of the
// process's own stdout and stderr, so a value that reaches Belai's log (a session
// started by `belai rc` writes it to a file) is hidden there too.
//
// A stream that is a terminal is left alone: wrapping it would break the TUI, and
// nothing writes a log to a terminal.
func Protect() {
	if SkipProtect {
		return
	}
	protectOnce.Do(func() {
		_ = HardenProcess()
		os.Stdout = scrubbed(os.Stdout)
		os.Stderr = scrubbed(os.Stderr)
	})
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err != nil || st.Mode()&os.ModeCharDevice != 0
}

// scrubbed returns a file whose writes reach f through the scrubber. If anything
// fails it returns f unchanged.
func scrubbed(f *os.File) *os.File {
	if f == nil || isTerminal(f) {
		return f
	}
	r, w, err := os.Pipe()
	if err != nil {
		return f
	}
	go func() {
		sw := NewWriter(f, Default)
		_, _ = io.Copy(sw, r)
		_ = sw.Close()
	}()
	return w
}
