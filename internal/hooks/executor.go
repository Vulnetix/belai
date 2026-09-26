package hooks

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/vulnetix/signet/internal/calltrace"
	"github.com/vulnetix/signet/internal/proc"
)

// Runner executes a validated hook command under its directory. Commands run
// argv-direct (never through a shell), with a scrubbed environment and bounded
// timeout/output, after re-resolving the command path through the directory
// so a symlink inside it cannot point at /bin/sh.
type Runner struct {
	// Root is the directory used for a hook whose Dir is empty.
	Root     string
	Timeout  time.Duration
	MaxBytes int
}

// Run executes h.Command with no stdin and returns its combined output. A
// nonzero exit is reported as an error so callers can fail closed.
func (r *Runner) Run(ctx context.Context, h Hook) (string, error) {
	out, _, err := r.exec(ctx, h, nil)
	return out, err
}

// runStdin executes h.Command with stdin and returns stdout and stderr
// separately: stdout carries the decision, stderr is only for the operator.
func (r *Runner) runStdin(ctx context.Context, h Hook, stdin []byte) (stdout, stderr string, err error) {
	return r.exec(ctx, h, stdin)
}

func (r *Runner) exec(ctx context.Context, h Hook, stdin []byte) (string, string, error) {
	fields := strings.Fields(h.Command)
	if len(fields) == 0 {
		return "", "", fmt.Errorf("hook %q has an empty command", h.Name)
	}
	dir := h.Dir
	if dir == "" {
		dir = r.Root
	}
	resolved, err := resolveIn(dir, fields[0])
	if err != nil {
		return "", "", err
	}
	timeout := r.Timeout
	if h.TimeoutMS > 0 {
		timeout = time.Duration(h.TimeoutMS) * time.Millisecond
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	ec := exec.CommandContext(ctx, resolved, fields[1:]...)
	ec.Dir = dir
	ec.Env = append(proc.ScrubbedEnv(), calltrace.Env(ctx)...)
	proc.SetProcessGroup(ec)
	ec.WaitDelay = time.Second
	if stdin != nil {
		ec.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	ec.Stdout = &capped{w: &out, max: r.MaxBytes}
	if stdin == nil {
		// Run keeps the old combined-output shape.
		ec.Stderr = ec.Stdout
	} else {
		ec.Stderr = &capped{w: &errb, max: r.MaxBytes}
	}
	err = ec.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return out.String(), errb.String(), fmt.Errorf("hook %q timed out", h.Name)
	}
	if err != nil {
		return out.String(), errb.String(), fmt.Errorf("hook %q: %w", h.Name, err)
	}
	return out.String(), errb.String(), nil
}

// capped writes at most max bytes and silently discards the rest, so a noisy
// hook cannot grow memory without bound. max <= 0 means unbounded.
type capped struct {
	w   io.Writer
	max int
	n   int
}

func (c *capped) Write(p []byte) (int, error) {
	if c.max > 0 {
		room := c.max - c.n
		if room <= 0 {
			return len(p), nil
		}
		if len(p) > room {
			c.n += room
			_, err := c.w.Write(p[:room])
			return len(p), err
		}
	}
	c.n += len(p)
	_, err := c.w.Write(p)
	return len(p), err
}

// resolveIn maps a relative command path onto dir and confirms it still
// resolves inside dir after following symlinks.
func resolveIn(dir, cmd string) (string, error) {
	absRoot, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(absRoot); err == nil {
		absRoot = r
	}
	joined := filepath.Join(absRoot, cmd)
	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absRoot, resolved)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("hook command %q escapes the hooks root", cmd)
	}
	return resolved, nil
}
