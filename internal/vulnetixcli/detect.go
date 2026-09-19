// Package vulnetixcli detects and drives the Vulnetix CLI. When the CLI is on
// PATH the harness unlocks its agent hooks, skills, and /code-review.
package vulnetixcli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/vulnetix/signet/internal/proc"
)

// Default subprocess limits.
const (
	DefaultTimeout = 15 * time.Second
	ProbeTimeout   = 30 * time.Second
	MaxOutputBytes = 4 << 20
)

// CLI is a detected vulnetix binary.
type CLI struct {
	Path string
	// Dir is the working directory for Run/ExecIn when not overridden.
	Dir string
	// Timeout caps a single execution. Zero means DefaultTimeout.
	Timeout time.Duration
	// Env overrides the process environment. Nil means probeEnv().
	Env []string
}

// timeoutOrDefault returns the configured timeout or the package default.
func (c CLI) timeoutOrDefault() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return DefaultTimeout
}

// Detect looks for the vulnetix binary on PATH.
func Detect() (*CLI, error) {
	p, err := exec.LookPath("vulnetix")
	if err != nil {
		return nil, fmt.Errorf("vulnetix not found on PATH: %w", err)
	}
	return &CLI{Path: p}, nil
}

// Result is the outcome of a CLI execution.
type Result struct {
	Stdout, Stderr string
	ExitCode       int
	Duration       time.Duration
	TimedOut       bool
}

// readOnlyProbes do not require --disable-memory, because the CLI should be
// allowed to write memory for scans. The probes listed here only read state.
var readOnlyProbes = map[string]bool{
	"env":     true,
	"version": true,
	"auth":    true,
}

// isReadOnlyProbe reports whether args is a read-only probe that should run
// with --disable-memory so it never mutates the project's artifacts.
func isReadOnlyProbe(args []string) bool {
	// The first positional argument is the subcommand; flags precede it.
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			return readOnlyProbes[a]
		}
	}
	return false
}

// argv builds the final argument list with hardening flags.
func argv(args []string) []string {
	out := []string{
		"--no-banner",
		"--no-progress",
		"--no-analytics",
	}
	if isReadOnlyProbe(args) {
		out = append(out, "--disable-memory")
	}
	return append(out, args...)
}

// Exec executes the vulnetix binary with the given arguments and returns stdout,
// stderr, and exit state. The context governs cancellation; c.Timeout is
// applied on top when the caller does not set a deadline.
func (c CLI) Exec(ctx context.Context, args ...string) (Result, error) {
	return c.ExecIn(ctx, "", args...)
}

// ExecIn executes the vulnetix binary in dir, overriding c.Dir when non-empty.
func (c CLI) ExecIn(ctx context.Context, dir string, args ...string) (Result, error) {
	if c.Path == "" {
		return Result{}, fmt.Errorf("vulnetix CLI path is empty")
	}

	if _, err := os.Stat(c.Path); err != nil {
		return Result{}, fmt.Errorf("vulnetix binary %q: %w", c.Path, err)
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeoutOrDefault())
	defer cancel()

	tmpDir := dir
	if tmpDir == "" {
		tmpDir = c.Dir
	}

	ec := exec.CommandContext(ctx, c.Path, argv(args)...)
	ec.Dir = tmpDir
	ec.Env = c.Env
	if ec.Env == nil {
		ec.Env = probeEnv()
	}
	ec.WaitDelay = 2 * time.Second
	proc.SetProcessGroup(ec)

	t0 := time.Now()
	stdout := &cappedWriter{max: MaxOutputBytes}
	stderr := &cappedWriter{max: MaxOutputBytes}
	ec.Stdout = stdout
	ec.Stderr = stderr

	if err := ec.Start(); err != nil {
		return Result{Duration: time.Since(t0)}, fmt.Errorf("start vulnetix %s: %w", strings.Join(args, " "), err)
	}

	err := ec.Wait()
	res := Result{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Duration: time.Since(t0),
		TimedOut: ctx.Err() == context.DeadlineExceeded,
	}
	if err != nil {
		res.ExitCode = exitCode(err)
		return res, fmt.Errorf("vulnetix %s: %w", strings.Join(args, " "), err)
	}
	return res, nil
}

// Run executes the vulnetix binary and returns stdout only. It is the legacy
// entry point kept for callers that only need the primary output stream.
func (c *CLI) Run(args ...string) (string, error) {
	res, err := c.Exec(context.Background(), args...)
	return res.Stdout, err
}

// exitCode converts an exec.ExitError into an exit code, defaulting to 1.
func exitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if code := exitErr.ExitCode(); code != 0 {
			return code
		}
	}
	return 1
}

// probeEnv returns the process environment with Signet provider secrets removed
// and the Vulnetix credential variables preserved.
func probeEnv() []string {
	keep := map[string]bool{
		"VULNETIX_API_TOKEN": true,
		"VULNETIX_API_KEY":   true,
		"VULNETIX_ORG_ID":    true,
		"VULNETIX_API_URL":   true,
		"VULNETIX_WEB_URL":   true,
		"VVD_ORG":            true,
		"VVD_SECRET":         true,
	}

	var out []string
	for _, e := range os.Environ() {
		key, _, _ := strings.Cut(e, "=")
		upper := strings.ToUpper(key)
		if keep[upper] {
			out = append(out, e)
			continue
		}
		if strings.HasPrefix(upper, "OPENAI_") ||
			strings.HasPrefix(upper, "ANTHROPIC_") ||
			strings.HasPrefix(upper, "CLOUDFLARE_") ||
			strings.HasPrefix(upper, "SIGNET_") ||
			strings.HasSuffix(upper, "_API_KEY") ||
			strings.HasSuffix(upper, "_TOKEN") ||
			strings.HasSuffix(upper, "_SECRET") {
			continue
		}
		out = append(out, e)
	}

	out = append(out,
		"HOMEBREW_NO_AUTO_UPDATE=1",
		"NO_COLOR=1",
		"TERM=dumb",
	)
	return out
}

// stripANSI removes ANSI escape sequences from CLI output before parsing.
func stripANSI(s string) string { return ansi.Strip(s) }

// cappedWriter accumulates bytes up to a limit and drops further writes.
type cappedWriter struct {
	mu   sync.Mutex
	buf  []byte
	max  int
	drop bool
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.drop {
		return len(p), nil
	}
	room := w.max - len(w.buf)
	if room <= 0 {
		w.drop = true
		return len(p), nil
	}
	if len(p) <= room {
		w.buf = append(w.buf, p...)
	} else {
		w.buf = append(w.buf, p[:room]...)
		w.drop = true
	}
	return len(p), nil
}

func (w *cappedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.buf)
}

// ErrMissingCLI is returned when a function requires the CLI but it is absent.
var ErrMissingCLI = fmt.Errorf("vulnetix CLI not available")
