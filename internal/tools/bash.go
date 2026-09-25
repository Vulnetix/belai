package tools

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/vulnetix/signet/internal/calltrace"
	"github.com/vulnetix/signet/internal/proc"
)

// ShellMetacharacters are shell syntax that would let a command escape a
// no-shell execution model. Read-only Bash never executes through a shell, so
// rejecting these before tokenising keeps that gate honest and fails closed.
const ShellMetacharacters = ";&|$`<>\n()"

// readOnlyBash holds the read-only commands the Bash tool is allowed to run.
// Git read-only subcommands and the metacharacter gate are defined below.
var readOnlyBash = map[string]bool{
	"cat": true, "env": true, "false": true, "grep": true, "egrep": true, "rg": true, "find": true,
	"ls": true, "uname": true, "pwd": true, "head": true, "tail": true,
	"wc": true, "sort": true, "uniq": true, "file": true, "which": true,
	"diff": true, "stat": true, "du": true, "basename": true,
	"dirname": true, "realpath": true, "readlink": true,
	"jq": true, "cut": true, "tr": true, "nl": true, "fold": true,
	"paste": true, "printenv": true, "join": true, "comm": true, "rev": true, "shuf": true,
	"seq": true, "sleep": true, "od": true, "xxd": true, "base64": true, "date": true,
	"printf": true, "echo": true, "tree": true, "fd": true, "zcat": true,
	"gunzip": true, "md5sum": true, "sha256sum": true, "column": true,
	"expand": true, "unexpand": true,
}

var gitReadSubcommands = map[string]bool{
	"status": true, "log": true, "diff": true, "show": true,
	"rev-parse": true, "ls-files": true, "grep": true, "describe": true,
}

var findUnsafeOptions = map[string]bool{
	"-exec": true, "-execdir": true, "-ok": true, "-okdir": true,
	"-delete": true, "-fprint": true, "-fls": true, "-fprintf": true,
}

// gitValueOptions are git options that consume a following argument. The git
// branch skips the option's value so `git -C sub status` cannot smuggle a
// mutating subcommand past the gate through an option argument.
var gitValueOptions = map[string]bool{
	"-C": true, "-c": true, "--git-dir": true, "--work-tree": true,
	"--namespace": true, "--exec-path": true, "--config-env": true,
}

// BashAllowed reports whether the whole command passes the read-only gate.
// It is the single source of truth for the read-only Bash allowlist; plan mode
// forwards to it.
func BashAllowed(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" || strings.ContainsAny(command, ShellMetacharacters) {
		return false
	}
	fields := strings.Fields(command)
	base := filepath.Base(fields[0])
	switch base {
	case "git":
		return gitReadOnly(fields)
	case "find":
		return findReadOnly(fields)
	case "env":
		return envReadOnly(fields)
	default:
		return readOnlyBash[base]
	}
}

// envReadOnly allows env only when it prints the environment rather than
// executing a command. `env VAR=x` and `env` print; `env cmd` executes, so any
// bare argument that is not an assignment or a print-only option is rejected.
// This keeps the read-only gate fail-closed even though env sits in the word
// list.
func envReadOnly(fields []string) bool {
	for _, f := range fields[1:] {
		if strings.HasPrefix(f, "-") || strings.Contains(f, "=") {
			continue
		}
		return false // a bare token names a command to execute
	}
	return true
}

// gitReadOnly rejects git invocations whose subcommand mutates. Options that
// consume a value are skipped along with their argument, so the subcommand is
// always the first non-option, non-value token.
func gitReadOnly(fields []string) bool {
	for i := 1; i < len(fields); i++ {
		f := fields[i]
		if gitValueOptions[f] {
			i++ // skip the option's argument
			continue
		}
		if strings.HasPrefix(f, "-") {
			continue
		}
		return gitReadSubcommands[f]
	}
	return false
}

// findReadOnly rejects find invocations that can write, delete, or execute.
func findReadOnly(fields []string) bool {
	for _, f := range fields[1:] {
		if findUnsafeOptions[f] {
			return false
		}
	}
	return true
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
		"Mutating, so it asks for approval unless an explicit allow rule matches, and it is unavailable in plan mode — use Read, Grep, Glob, and the read-only command tools there instead."
	arg := "The command to run, e.g. \"go test ./...\" or \"git commit -m msg\""
	if b.ReadOnly {
		desc = "Run one read-only shell command in the working directory. " +
			"It does not run through a shell, so pipes, redirections, chaining, substitutions, and newlines are rejected rather than escaped — send a single command with its arguments. " +
			"Only commands on the read-only allowlist are permitted (inspection utilities such as `cat`, `ls`, `head`, `find`, `wc`, `sort`, and read-only `git` subcommands: status, log, diff, show, rev-parse, ls-files, grep, describe); anything that could write, delete, or execute is refused. " +
			"Output is capped at 64 KiB, the command is killed after its timeout (at most 600000 ms), and provider credentials are stripped from the environment."
		arg = "The single command to run, e.g. \"git status\" or \"ls -la internal\" — no pipes, redirections, or chaining"
	}
	return Definition{
		Name:        "Bash",
		Description: desc,
		Properties: map[string]Property{
			"command":     {Type: "string", Description: arg},
			"timeout":     {Type: "integer", Description: "Optional time limit in milliseconds (max 600000)"},
			"description": {Type: "string", Description: "Optional short description of what the command does, shown to the user"},
		},
		Required: []string{"command"},
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
		if strings.ContainsAny(cmd, ShellMetacharacters) {
			return Result{}, fmt.Errorf("command contains shell metacharacters; %s", readOnlyBashHint)
		}
		if !BashAllowed(cmd) {
			return Result{}, fmt.Errorf("command not in read-only allowlist: %s; %s", cmd, readOnlyBashHint)
		}
		fields := strings.Fields(cmd)
		if len(fields) == 0 {
			return Result{}, fmt.Errorf("empty command")
		}
		ec = exec.CommandContext(ctx, fields[0], fields[1:]...)
	} else {
		// Full mode: `sh -c` so &&, pipes, and substitutions work. The
		// timeout, Dir confinement, env scrubbing, and output truncation
		// hardening below still apply.
		ec = exec.CommandContext(ctx, "sh", "-c", cmd)
	}
	ec.Dir = baseDir(b.Root, b.Cwd)
	ec.Env = proc.ScrubbedEnv()
	ec.Env = append(ec.Env, calltrace.Env(ctx)...)

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
		lineSink = func(line string) { sink(Progress{Stream: "stdout", Text: line}) }
	}
	tw := proc.NewLineTee(b.MaxBytes, lineSink)
	ec.Stdout = tw
	ec.Stderr = tw

	// Without WaitDelay, Wait blocks until every writer closes: a process that
	// spawns a background child inheriting the pipe would hang the caller
	// indefinitely, even after the parent exits and the context is cancelled.
	ec.WaitDelay = 2 * time.Second
	proc.SetProcessGroup(ec)

	if err := ec.Start(); err != nil {
		return Result{}, err
	}
	err = ec.Wait()
	tw.Flush()

	content := tw.Content()
	if ctx.Err() == context.DeadlineExceeded {
		// Keep whatever the command managed to produce. Returning an error
		// here would discard it: executeCall drops the Result when err is
		// non-nil, so a timed-out command used to report nothing at all, which
		// is the least useful moment to have no output.
		return BashResult(content + fmt.Sprintf("\n… command timed out after %s", timeout)), nil
	}
	if err != nil {
		content += fmt.Sprintf("\nexit status %d", exitCode(err))
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
