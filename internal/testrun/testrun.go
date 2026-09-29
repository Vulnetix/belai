// Package testrun runs a repository's test suites deterministically for the
// post-end test pass. A suite's argv comes from the harness (the fixed
// marker-file table in internal/testdetect) or from the user's own
// `tests.command` override, never from a model, and it runs without a shell.
// Pass or fail is decided by the process exit code alone; no model is asked.
//
// A run honours the user's permission rules (a deny rule refuses the command,
// an ask rule skips it because the pass has nobody to ask), the call's OS
// sandbox policy, the scrubbed environment and its own process group. The
// captured output is arbitrary text: callers must sanitise it and classify it
// as tools.KindProcess before it can reach a model.
package testrun

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/proc"
	"github.com/vulnetix/belai/internal/sandbox"
	"github.com/vulnetix/belai/internal/testdetect"
)

// Status is how one suite run ended.
type Status string

const (
	// Passed means the command exited 0.
	Passed Status = "pass"
	// Failed means the command exited non-zero.
	Failed Status = "fail"
	// TimedOut means the run hit its timeout.
	TimedOut Status = "timeout"
	// Denied means a deny rule refused the command.
	Denied Status = "denied"
	// NeedsApproval means an ask rule matched and the pass cannot ask.
	NeedsApproval Status = "needs_approval"
	// Errored means the command could not be started (missing binary, sandbox
	// required but unavailable).
	Errored Status = "error"
)

// DefaultMaxBytes caps the output one run keeps (head and tail).
const DefaultMaxBytes = 64 * 1024

// Options configures a run.
type Options struct {
	// Dir is the working directory, the trusted repository root.
	Dir string
	// Timeout bounds one suite. Zero means no bound beyond ctx.
	Timeout time.Duration
	// Perms is the user's permission configuration.
	Perms permissions.Settings
	// MaxBytes caps the kept output. Zero means DefaultMaxBytes.
	MaxBytes int
}

// Result is one suite's outcome. Output is raw process text and is never
// safe to hand to a model without sanitising and classifying it.
type Result struct {
	Suite    string
	Command  []string
	Status   Status
	ExitCode int
	Duration time.Duration
	Output   string
	// Note is harness-composed text for a run that did not execute or could
	// not start (the refusing reason, the start error class).
	Note string
}

// OK reports whether the suite passed.
func (r Result) OK() bool { return r.Status == Passed }

// Subject returns the string a Bash permission rule is matched against.
func Subject(argv []string) string { return strings.Join(argv, " ") }

// Run runs one argv and reports the outcome. It never returns an error: every
// failure to run is a Result with a Status, so a caller tallies results
// without a second error path.
func Run(ctx context.Context, opts Options, name string, argv []string) Result {
	res := Result{Suite: name, Command: append([]string(nil), argv...)}
	if len(argv) == 0 {
		res.Status = Errored
		res.Note = "empty command"
		return res
	}
	switch opts.Perms.Evaluate("Bash", Subject(argv)) {
	case permissions.DecisionBlock:
		res.Status = Denied
		res.Note = "a permission deny rule refused the test command"
		return res
	case permissions.DecisionAsk:
		res.Status = NeedsApproval
		res.Note = "an ask rule matches the test command; add an allow rule to run it after a session"
		return res
	}

	runCtx := ctx
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	max := opts.MaxBytes
	if max <= 0 {
		max = DefaultMaxBytes
	}

	ec := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	ec.Dir = opts.Dir
	ec.Env = proc.ScrubbedEnv()
	tw := proc.NewLineTee(max, nil).KeepTail()
	ec.Stdout = tw
	ec.Stderr = tw
	// A test run can leave a child holding the pipe; do not wait on it forever.
	ec.WaitDelay = 2 * time.Second
	proc.SetProcessGroup(ec)
	if _, err := sandbox.Wrap(ec, sandbox.FromContext(ctx)); err != nil {
		res.Status = Errored
		res.Note = "sandbox: " + errClass(err)
		return res
	}

	start := time.Now()
	if err := ec.Start(); err != nil {
		res.Status = Errored
		res.Note = "could not start: " + errClass(err)
		return res
	}
	err := ec.Wait()
	tw.Flush()
	res.Duration = time.Since(start)
	res.Output = tw.Content()

	switch {
	case runCtx.Err() == context.DeadlineExceeded:
		res.Status = TimedOut
		res.ExitCode = -1
		res.Note = fmt.Sprintf("timed out after %s", opts.Timeout)
	case err == nil:
		res.Status = Passed
	default:
		res.Status = Failed
		res.ExitCode = 1
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() > 0 {
			res.ExitCode = ee.ExitCode()
		}
	}
	return res
}

// errClass reduces a start error to a short class without echoing paths.
func errClass(err error) string {
	switch {
	case errors.Is(err, exec.ErrNotFound):
		return "command not found"
	case errors.Is(err, sandbox.ErrUnavailable):
		return "required but unavailable"
	}
	return "start failed"
}

// Plan is the ordered list of runs for one pass.
type Plan struct {
	Names []string
	Argvs [][]string
}

// BuildPlan picks what to run. A non-empty override runs alone. Otherwise the
// detected suites run in order, scoped to changed paths when scope is
// "affected" and full when it is "full". The first-listed just or make suite
// already covers the tree, so ecosystem suites that it duplicates are kept:
// the table lists what the repository declares, and the user narrows it with
// the override.
func BuildPlan(suites []testdetect.Suite, override []string, scope string, changed []string) Plan {
	var p Plan
	if len(override) > 0 {
		p.Names = []string{"override"}
		p.Argvs = [][]string{append([]string(nil), override...)}
		return p
	}
	if scope != "full" {
		suites = testdetect.Rescope(suites, changed)
	} else {
		suites = testdetect.Rescope(suites, nil)
	}
	for _, s := range suites {
		argv := s.ScopedCommand()
		if len(argv) == 0 {
			continue
		}
		p.Names = append(p.Names, s.Name)
		p.Argvs = append(p.Argvs, append([]string(nil), argv...))
	}
	return p
}

// RunPlan runs every entry of plan in order. It stops early only when ctx is
// cancelled.
func RunPlan(ctx context.Context, opts Options, plan Plan) []Result {
	var out []Result
	for i := range plan.Argvs {
		if ctx.Err() != nil {
			break
		}
		out = append(out, Run(ctx, opts, plan.Names[i], plan.Argvs[i]))
	}
	return out
}

// AllPassed reports whether results is non-empty and every suite passed.
func AllPassed(results []Result) bool {
	if len(results) == 0 {
		return false
	}
	for _, r := range results {
		if !r.OK() {
			return false
		}
	}
	return true
}
