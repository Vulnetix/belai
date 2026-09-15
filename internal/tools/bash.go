package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// bashAllowed reports whether the whole command passes the read-only gate.
func bashAllowed(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" || strings.ContainsAny(command, ShellMetacharacters) {
		return false
	}
	fields := strings.Fields(command)
	base := filepath.Base(fields[0])
	switch base {
	case "git":
		for i := 1; i < len(fields); i++ {
			f := fields[i]
			if f == "-C" || f == "-c" || f == "--git-dir" || f == "--work-tree" || strings.HasPrefix(f, "--") {
				continue
			}
			return gitReadSubcommands[f]
		}
		return false
	case "find":
		for _, f := range fields[1:] {
			if findUnsafeOptions[f] {
				return false
			}
		}
		return true
	default:
		return readOnlyBash[base]
	}
}

// Bash runs a single command. With ReadOnly true (the default, including when
// the field is nil) it executes without a shell and is confined to the
// read-only allowlist. Set ReadOnly to false explicitly to run through `sh -c`
// with full shell syntax (pipes, redirections, chaining).
type Bash struct {
	Root     string
	ReadOnly *bool
	Timeout  time.Duration
	MaxBytes int
}

// Definition returns the static tool metadata. The description branches on
// the mode so the model knows which execution model it has.
func (b *Bash) Definition() Definition {
	readOnly := b.ReadOnly == nil || *b.ReadOnly
	desc := "Run a local shell command. The full shell is available (pipes, redirections, and command chaining)."
	if readOnly {
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

// Subject returns the raw command for permission evaluation.
func (b *Bash) Subject(args map[string]any) string {
	if s, ok := args["command"].(string); ok {
		return s
	}
	return ""
}

// Execute runs the command, confining it to Root and scrubbing credential
// env vars from the subprocess. In ReadOnly mode the no-shell metacharacter
// gate and read-only allowlist apply; otherwise the command runs via `sh -c`.
func (b *Bash) Execute(ctx context.Context, args map[string]any) (Result, error) {
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
	readOnly := b.ReadOnly == nil || *b.ReadOnly
	if readOnly {
		// Read-only mode: no shell, fail closed on anything outside the
		// allowlist. Plan-mode restrictions additionally live in
		// modes.ToolAllowed, which callers must apply before Execute.
		if strings.ContainsAny(cmd, ShellMetacharacters) {
			return Result{}, fmt.Errorf("command contains shell metacharacters")
		}
		if !bashAllowed(cmd) {
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
	ec.Dir = b.Root
	ec.Env = scrubbedEnv()

	if b.MaxBytes <= 0 {
		b.MaxBytes = 64 * 1024
	}

	out, err := ec.CombinedOutput()
	content := string(out)
	if len(out) > b.MaxBytes {
		content = string(out[:b.MaxBytes]) + fmt.Sprintf("\n… truncated at %d bytes", b.MaxBytes)
	}
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return Result{}, fmt.Errorf("command timed out after %s", b.Timeout)
		}
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
