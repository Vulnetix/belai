package tools

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/calltrace"
	"github.com/vulnetix/belai/internal/proc"
	"github.com/vulnetix/belai/internal/sandbox"
	"github.com/vulnetix/belai/internal/shellsafe"
	"github.com/vulnetix/belai/internal/vaultenv"
)

// ShellMetacharacters are shell syntax that would let a command escape a
// no-shell execution model. Read-only Bash never executes through a shell, so
// rejecting these before tokenising keeps that gate honest and fails closed.
const ShellMetacharacters = ";&|$`<>\n()"

// BashAllowed reports whether the whole command passes the read-only gate.
// It is the single source of truth for the read-only Bash allowlist; plan mode
// and the Git native forward to it. The command is parsed with a shell grammar
// (internal/shellsafe): it must be one plain command, on the allowlist, with
// none of the flags that write a file or run a program.
func BashAllowed(command string) bool {
	argv, _ := shellsafe.ReadOnly(command)
	return argv != nil
}

// Bash runs a single command. With ReadOnly set it executes without a shell
// and is confined to the read-only allowlist; otherwise (the zero value, the
// default) it runs through `sh -c` with full shell syntax (pipes,
// redirections, chaining).
//
// Timeout is the default per-call time limit; a call may ask for a different
// one with the timeout argument, up to BashMaxTimeout.
type Bash struct {
	Cwd      *Cwd
	Root     string
	ReadOnly bool
	Timeout  time.Duration
	MaxBytes int
	// Vulnetix is set when the Vulnetix tool is registered: a command that
	// runs the vulnetix binary is refused with a pointer to that tool.
	Vulnetix bool
	// Launcher runs a command in the background when a call sets
	// run_in_background. Nil where the session has no process manager.
	Launcher ProcessLauncher
}

// Bash time limits, in the trained shape: timeout is milliseconds.
const (
	// BashDefaultTimeout is the limit when a call names none. A normal test
	// run takes longer than the old fixed 30 seconds.
	BashDefaultTimeout = 120 * time.Second
	// BashMaxTimeout caps what a call may ask for.
	BashMaxTimeout = 600 * time.Second
)

// effectiveTimeout resolves the call's time limit: the timeout argument in
// milliseconds when given (capped at BashMaxTimeout), else the tool default,
// else BashDefaultTimeout.
func (b *Bash) effectiveTimeout(args map[string]any) (time.Duration, error) {
	if ms, ok := argInt64(args, "timeout"); ok {
		if ms <= 0 {
			return 0, fmt.Errorf("timeout must be a positive number of milliseconds")
		}
		return min(time.Duration(ms)*time.Millisecond, BashMaxTimeout), nil
	}
	if b.Timeout > 0 {
		return b.Timeout, nil
	}
	return BashDefaultTimeout, nil
}

// readOnlyBashHint follows every read-only rejection so the model moves on to
// a tool that can answer instead of rephrasing the same refused command.
const readOnlyBashHint = "this Bash is read-only (the read_only setting is on for agent mode, or this is a plan/explore surface), so it cannot build, test, or pipe — send one allowlisted command, or use Grep, Glob, or Read instead"

// Definition returns the static tool metadata. The description branches on
// the mode so the model knows which execution model it has.
func (b *Bash) Definition() Definition {
	desc := "Run a shell command in the working directory. " +
		"The full shell is available: pipes, redirections, chaining, and substitutions all work. " +
		"Output is the command's stdout and stderr interleaved, capped at 64 KiB and truncated beyond that, with a non-zero exit reported as a trailing `exit status N` line. " +
		"The command is killed after its timeout (default 120000 ms, at most 600000 ms; set timeout for a long build or test run), and whatever it printed up to that point is still returned. " +
		"Provider credentials are stripped from the environment, so a command cannot read or forward them. " +
		vaultEnvNote() +
		"Mutating, so it asks for approval unless an explicit allow rule matches, and it is unavailable in plan mode — use Read, Grep, Glob, and the read-only command tools there instead."
	arg := "The command to run, e.g. \"go test ./...\" or \"git commit -m msg\""
	if b.ReadOnly {
		desc = "Run one read-only shell command in the working directory. " +
			"It does not run through a shell, so pipes, redirections, chaining, substitutions, and newlines are rejected rather than escaped — send a single command with its arguments. " +
			"Only commands on the read-only allowlist are permitted (inspection utilities such as `cat`, `ls`, `head`, `find`, `wc`, `sort`, and read-only `git` subcommands: status, log, diff, show, rev-parse, ls-files, grep, describe); anything that could write, delete, or execute is refused. " +
			"Output is capped at 64 KiB, the command is killed after its timeout (at most 600000 ms), and provider credentials are stripped from the environment."
		arg = "The single command to run, e.g. \"git status\" or \"ls -la internal\" — no pipes, redirections, or chaining"
	}
	props := map[string]Property{
		"command":     {Type: "string", Format: FormatCommand, Description: arg},
		"timeout":     {Type: "integer", Description: "Optional time limit in milliseconds (max 600000)"},
		"description": {Type: "string", Description: "Optional short description of what the command does, shown to the user"},
	}
	if !b.ReadOnly && b.Launcher != nil {
		desc += " Set run_in_background for a server or other long-running process: the call returns a handle at once, the process keeps running (same sandbox, same approval), and BashOutput reads its output, KillShell stops it, and ProcessList names the running ones. It is stopped when the session ends."
		props["run_in_background"] = Property{Type: "boolean", Description: "Run the command in the background and return a handle instead of waiting for it to finish"}
	}
	return Definition{
		Name:        "Bash",
		Description: desc,
		Properties:  props,
		Required:    []string{"command"},
	}
}

// Kind returns "bash".
func (b *Bash) Kind() Kind { return KindBash }

// Mutates reports whether Bash mutates the workspace: full mode does,
// read-only mode does not.
func (b *Bash) Mutates() bool { return !b.ReadOnly }

// Subject returns the raw command for permission evaluation.
func (b *Bash) Subject(args map[string]any) string {
	if s, ok := args["command"].(string); ok {
		return s
	}
	return ""
}

// Execute runs the command and returns its output when it finishes.
func (b *Bash) Execute(ctx context.Context, args map[string]any) (Result, error) {
	return b.ExecuteStream(ctx, args, nil)
}

// ExecuteStream runs the command, confining it to Root and scrubbing credential
// env vars from the subprocess. In ReadOnly mode the no-shell metacharacter
// gate and read-only allowlist apply; otherwise the command runs via `sh -c`.
//
// When sink is non-nil it receives whole lines of combined output as the
// process writes them. Execute is this function with a nil sink, so there is
// one implementation of the command construction and hardening rules.
func (b *Bash) ExecuteStream(ctx context.Context, args map[string]any, sink Sink) (Result, error) {
	cmd, ok := args["command"].(string)
	if !ok || strings.TrimSpace(cmd) == "" {
		return Result{}, fmt.Errorf("missing command argument")
	}

	if b.Vulnetix && VulnetixInCommand(cmd) {
		return Result{}, fmt.Errorf("run vulnetix with the Vulnetix tool, not Bash: pass the arguments after `vulnetix` as its command (it adds --no-progress, scopes --path, keeps fix to --dry-run and allows a 15-minute scan)")
	}

	if bg, _ := argBool(args, "run_in_background"); bg {
		if b.ReadOnly {
			return Result{}, fmt.Errorf("run_in_background is not available on the read-only Bash")
		}
		return startBackground(ctx, b.Launcher, cmd, baseDir(b.Root, b.Cwd))
	}

	timeout, err := b.effectiveTimeout(args)
	if err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var ec *exec.Cmd
	if b.ReadOnly {
		// Read-only mode: no shell, fail closed on anything outside the
		// allowlist. Plan-mode restrictions additionally live in
		// modes.ToolAllowed, which callers must apply before Execute.
		// The argv that runs is the argv that was judged: the command is parsed
		// once and its quote-free words are executed, never re-split.
		argv, why := shellsafe.ReadOnly(cmd)
		if argv == nil {
			return Result{}, fmt.Errorf("command refused by the read-only gate (%s): %s; %s", why, cmd, readOnlyBashHint)
		}
		ec = exec.CommandContext(ctx, argv[0], argv[1:]...)
	} else {
		// Full mode: `sh -c` so &&, pipes, and substitutions work. The
		// timeout, Dir confinement, env scrubbing, and output truncation
		// hardening below still apply.
		ec = exec.CommandContext(ctx, "sh", "-c", cmd)
	}
	ec.Dir = baseDir(b.Root, b.Cwd)
	ec.Env = proc.ScrubbedEnv()
	ec.Env = append(ec.Env, calltrace.Env(ctx)...)
	if !b.ReadOnly {
		// The vault's environment variables for this sandbox, set only on the
		// process the command starts (internal/vaultenv): never in Belai's own
		// environment, and removed from the output below.
		ec.Env = append(ec.Env, vaultenv.Default.Environ(time.Now())...)
	}

	if b.MaxBytes <= 0 {
		b.MaxBytes = 64 * 1024
	}

	// One writer for both streams. os/exec guarantees that when Stdout and
	// Stderr are the same comparable value it serialises writes through it, so
	// the two streams interleave exactly as the process emitted them — which is
	// what CombinedOutput does internally. Separate StdoutPipe/StderrPipe with
	// two scanners would reorder the output of anything that writes to both.
	var lineSink func(string)
	if sink != nil {
		lineSink = func(line string) { sink(Progress{Stream: "stdout", Text: vaultenv.Default.Scrub(line)}) }
	}
	// Head and tail: a build or test run prints its summary last.
	tw := proc.NewLineTee(b.MaxBytes, lineSink).KeepTail()
	ec.Stdout = tw
	ec.Stderr = tw

	// Without WaitDelay, Wait blocks until every writer closes: a process that
	// spawns a background child inheriting the pipe would hang the caller
	// indefinitely, even after the parent exits and the context is cancelled.
	ec.WaitDelay = 2 * time.Second
	proc.SetProcessGroup(ec)

	// The OS sandbox, when the call's policy asks for one. Required mode with
	// no backend refuses the command rather than running it bare.
	policy := sandbox.FromContext(ctx)
	sandboxed, err := sandbox.Wrap(ec, policy)
	if err != nil {
		return Result{}, err
	}

	if err := ec.Start(); err != nil {
		return Result{}, err
	}
	err = ec.Wait()
	tw.Flush()

	content := vaultenv.Default.Scrub(tw.Content())
	if ctx.Err() == context.DeadlineExceeded {
		// Keep whatever the command managed to produce. Returning an error
		// here would discard it: executeCall drops the Result when err is
		// non-nil, so a timed-out command used to report nothing at all, which
		// is the least useful moment to have no output.
		return BashResult(content + fmt.Sprintf("\n… command timed out after %s", timeout)), nil
	}
	if err != nil {
		content += fmt.Sprintf("\nexit status %d", exitCode(err))
		if sandboxed {
			content += "\n" + sandboxNote(policy)
		}
	}

	return BashResult(content), nil
}

func exitCode(err error) int {
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() != 0 {
		return exitErr.ExitCode()
	}
	return 1
}

// BashResult constructs a Bash tool result.
func BashResult(content string) Result {
	return Result{Kind: KindBash, Content: content}
}

// NativeResult constructs a native read-only tool result.
func NativeResult(content string) Result {
	return Result{Kind: KindNative, Content: content}
}

// sandboxNote tells the model, in the harness's words, that a failed command
// ran inside the OS sandbox, so it asks the user rather than retrying blindly.
func sandboxNote(p sandbox.Policy) string {
	note := "(ran inside the Belai sandbox: writes outside the workspace roots, /tmp and tool caches fail"
	if p.DenyNetwork {
		note += ", and the network is off"
	}
	return note + "; if the command needs more, ask the user to adjust sandbox settings)"
}

// vaultEnvNote names the environment variables the vault gives this sandbox's
// commands, so the model uses $NAME and never asks for a value. It names them and
// nothing else; the values are hidden from output, transcripts and logs.
func vaultEnvNote() string {
	names := vaultenv.Default.Names(time.Now())
	if len(names) == 0 {
		return ""
	}
	return "These environment variables are set for commands, from the organisation's vault: " + strings.Join(names, ", ") +
		". Use them as $NAME. Their values are hidden: they never appear in output, and are shown as [vault:NAME] if a command prints one. " +
		"Do not try to read, decode or transform them to see the value. "
}
