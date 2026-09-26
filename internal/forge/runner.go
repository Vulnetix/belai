package forge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
	"unicode"

	"github.com/vulnetix/belai/internal/proc"
	"github.com/vulnetix/belai/internal/sanitize"
)

// Timeouts for one CLI call. Reads are short so a hung network never stalls
// the panel for long; mutations that talk to the forge get more room.
const (
	ReadTimeout  = 5 * time.Second
	WriteTimeout = 60 * time.Second
)

// Runner executes argv in dir and returns its stdout. A non-nil error carries
// the stderr text; stdout is still returned alongside it because some CLIs
// (gh pr checks) exit non-zero while printing a valid result.
type Runner func(ctx context.Context, dir string, argv ...string) ([]byte, error)

// LookPath resolves a binary on PATH; tests substitute a fake.
type LookPath func(name string) (string, error)

// ExecRunner is the real Runner: argv straight to exec, no shell, its own
// process group so a timeout kills any children, and no stdin so a CLI that
// wants to prompt fails instead of hanging.
func ExecRunner(ctx context.Context, dir string, argv ...string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, errors.New("forge: empty command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	proc.SetProcessGroup(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		msg := lastLine(stderr.String())
		if ctx.Err() != nil {
			msg = "timed out"
		}
		if msg == "" {
			msg = err.Error()
		}
		return stdout.Bytes(), fmt.Errorf("%s: %s", argv[0], msg)
	}
	return stdout.Bytes(), nil
}

// run calls r under a timeout and returns trimmed stdout.
func run(ctx context.Context, r Runner, timeout time.Duration, dir string, argv ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := r(ctx, dir, argv...)
	return strings.TrimSpace(string(out)), err
}

// lastLine returns the last non-blank line of s, trimmed.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}

// maxCleanLen caps one cleaned field, in runes.
const maxCleanLen = 200

// Clean makes third-party CLI text safe to render: harness delimiter markup
// is removed, newlines and tabs become spaces, other control and bidi runes
// are dropped, and the result is capped at maxCleanLen runes.
func Clean(s string) string {
	s = sanitize.Sanitize(s)
	var b strings.Builder
	n := 0
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t' || r == '\r':
			r = ' '
		case unicode.IsControl(r), isBidi(r):
			continue
		}
		if n >= maxCleanLen {
			b.WriteRune('…')
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

// CleanErr cleans an error's text for display; nil yields "".
func CleanErr(err error) string {
	if err == nil {
		return ""
	}
	return Clean(err.Error())
}

func isBidi(r rune) bool {
	switch {
	case r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069, r == 0x200E, r == 0x200F, r == 0x061C:
		return true
	}
	return false
}
