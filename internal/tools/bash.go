package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
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
type Bash struct {
	Cwd      *Cwd
	Root     string
	ReadOnly bool
	Timeout  time.Duration
	MaxBytes int
}

// Definition returns the static tool metadata. The description branches on
// the mode so the model knows which execution model it has.
func (b *Bash) Definition() Definition {
	desc := "Run a local shell command. The full shell is available (pipes, redirections, and command chaining)."
	if b.ReadOnly {
		desc = "Run a read-only local shell command: a single command from the read-only allowlist (no pipes, redirections, or command chaining)."
	}
	return Definition{
		Name:        "Bash",
		Description: desc,
		Properties: map[string]Property{
			"command": {Type: "string", Description: "The command to run, e.g. \"git status\" or \"ls -la\""},
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

	if b.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, b.Timeout)
		defer cancel()
	}

	var ec *exec.Cmd
	if b.ReadOnly {
		// Read-only mode: no shell, fail closed on anything outside the
		// allowlist. Plan-mode restrictions additionally live in
		// modes.ToolAllowed, which callers must apply before Execute.
		if strings.ContainsAny(cmd, ShellMetacharacters) {
			return Result{}, fmt.Errorf("command contains shell metacharacters")
		}
		if !BashAllowed(cmd) {
			return Result{}, fmt.Errorf("command not in read-only allowlist: %s", cmd)
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
	ec.Env = scrubbedEnv()

	if b.MaxBytes <= 0 {
		b.MaxBytes = 64 * 1024
	}

	// One writer for both streams. os/exec guarantees that when Stdout and
	// Stderr are the same comparable value it serialises writes through it, so
	// the two streams interleave exactly as the process emitted them — which is
	// what CombinedOutput does internally. Separate StdoutPipe/StderrPipe with
	// two scanners would reorder the output of anything that writes to both.
	tw := &tailWriter{sink: sink, max: b.MaxBytes, flushEvery: progressFlushInterval}
	ec.Stdout = tw
	ec.Stderr = tw

	// Without WaitDelay, Wait blocks until every writer closes: a process that
	// spawns a background child inheriting the pipe would hang the caller
	// indefinitely, even after the parent exits and the context is cancelled.
	ec.WaitDelay = 2 * time.Second
	setProcessGroup(ec)

	if err := ec.Start(); err != nil {
		return Result{}, err
	}
	err := ec.Wait()
	tw.Flush()

	content := tw.Content()
	if ctx.Err() == context.DeadlineExceeded {
		// Keep whatever the command managed to produce. Returning an error
		// here would discard it: executeCall drops the Result when err is
		// non-nil, so a timed-out command used to report nothing at all, which
		// is the least useful moment to have no output.
		return BashResult(content + fmt.Sprintf("\n… command timed out after %s", b.Timeout)), nil
	}
	if err != nil {
		content += fmt.Sprintf("\nexit status %d", exitCode(err))
	}

	return BashResult(content), nil
}

// progressFlushInterval bounds how often a running command can wake the UI.
// Without it, output like `find /` produces an event per line and floods the
// agent's event channel with work the terminal cannot draw anyway.
const progressFlushInterval = 50 * time.Millisecond

// progressFlushLines flushes early when a burst arrives faster than the
// interval, so a fast command still streams rather than arriving all at once.
const progressFlushLines = 64

// tailWriter accumulates a command's combined output, caps it at max bytes,
// and reports whole lines to a sink as they arrive.
//
// It is written to by the goroutine os/exec uses to copy from the process, and
// read by the caller after Wait returns; the mutex covers that handover. The
// sink is called with the lock released — it may block, and blocking it must
// not also block Content.
type tailWriter struct {
	sink       Sink
	max        int
	flushEvery time.Duration

	mu        sync.Mutex
	buf       []byte // full output, capped at max
	truncated bool
	partial   []byte // bytes since the last newline
	pending   []string
	lastFlush time.Time
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()

	if !w.truncated {
		if room := w.max - len(w.buf); room > 0 {
			if len(p) <= room {
				w.buf = append(w.buf, p...)
			} else {
				w.buf = append(w.buf, p[:room]...)
				w.truncated = true
			}
		} else {
			w.truncated = true
		}
	}

	if w.sink == nil {
		w.mu.Unlock()
		return len(p), nil
	}

	w.partial = append(w.partial, p...)
	for {
		i := bytes.IndexByte(w.partial, '\n')
		if i < 0 {
			break
		}
		w.pending = append(w.pending, string(bytes.TrimRight(w.partial[:i], "\r")))
		w.partial = w.partial[i+1:]
	}

	ready := w.takeLocked(false)
	w.mu.Unlock()

	w.emit(ready)
	return len(p), nil
}

// Flush reports any buffered lines, including a trailing line with no newline,
// which is how a prompt or a progress line without a terminator still reaches
// the UI.
func (w *tailWriter) Flush() {
	w.mu.Lock()
	if len(w.partial) > 0 {
		w.pending = append(w.pending, string(bytes.TrimRight(w.partial, "\r")))
		w.partial = nil
	}
	ready := w.takeLocked(true)
	w.mu.Unlock()
	w.emit(ready)
}

// takeLocked returns the buffered lines when they are due to be sent. Callers
// hold w.mu.
func (w *tailWriter) takeLocked(force bool) []string {
	if len(w.pending) == 0 {
		return nil
	}
	if !force && w.flushEvery > 0 &&
		len(w.pending) < progressFlushLines &&
		time.Since(w.lastFlush) < w.flushEvery {
		return nil
	}
	out := w.pending
	w.pending = nil
	w.lastFlush = time.Now()
	return out
}

func (w *tailWriter) emit(lines []string) {
	if len(lines) == 0 || w.sink == nil {
		return
	}
	w.sink(Progress{Stream: "stdout", Text: strings.Join(lines, "\n")})
}

// Content returns the captured output, with the truncation notice appended if
// the cap was reached.
func (w *tailWriter) Content() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.truncated {
		return string(w.buf) + fmt.Sprintf("\n… truncated at %d bytes", w.max)
	}
	return string(w.buf)
}

func exitCode(err error) int {
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() != 0 {
		return exitErr.ExitCode()
	}
	return 1
}

// scrubbedEnv returns a minimal environment with provider credentials and
// Signet config stripped so subprocess output cannot accidentally exfiltrate
// them to a model.
func scrubbedEnv() []string {
	var out []string
	for _, e := range os.Environ() {
		key, _, _ := strings.Cut(e, "=")
		upper := strings.ToUpper(key)
		if strings.HasPrefix(upper, "OPENAI_") || strings.HasPrefix(upper, "ANTHROPIC_") ||
			strings.HasPrefix(upper, "CLOUDFLARE_") || strings.HasPrefix(upper, "SIGNET_") ||
			strings.HasSuffix(upper, "_API_KEY") || strings.HasSuffix(upper, "_TOKEN") ||
			strings.HasSuffix(upper, "_SECRET") {
			continue
		}
		out = append(out, e)
	}
	return out
}

// BashResult constructs a Bash tool result.
func BashResult(content string) Result {
	return Result{Kind: KindBash, Content: content}
}

// NativeResult constructs a native read-only tool result.
func NativeResult(content string) Result {
	return Result{Kind: KindNative, Content: content}
}
