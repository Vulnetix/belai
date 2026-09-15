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
// no-shell execution model. The Bash tool never executes through a shell, so
// rejecting these before tokenising keeps the gate honest and fails closed.
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

// Bash runs a single command without a shell.
type Bash struct {
	Root     string
	Timeout  time.Duration
	MaxBytes int
}

// Definition returns the static tool metadata.
func (b *Bash) Definition() Definition {
	return Definition{
		Name:        "Bash",
		Description: "Run a local shell command read-only inspection command (no pipes, redirections, or command substitution).",
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

// Execute runs the command without a shell, confining it to Root and
// scrubbing credential env vars from the subprocess.
func (b *Bash) Execute(ctx context.Context, args map[string]any) (Result, error) {
	cmd, ok := args["command"].(string)
	if !ok || strings.TrimSpace(cmd) == "" {
		return Result{}, fmt.Errorf("missing command argument")
	}
	if strings.ContainsAny(cmd, ShellMetacharacters) {
		return Result{}, fmt.Errorf("command contains shell metacharacters")
	}
	// Fail closed unconditionally: the Bash tool is read-only by construction,
	// in plan mode and out of it.
	if !bashAllowed(cmd) {
		return Result{}, fmt.Errorf("command not in read-only allowlist: %s", cmd)
	}

	// The Bash executor has no shell; plan-mode restrictions live in
	// modes.ToolAllowed, which callers must apply before Execute.
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return Result{}, fmt.Errorf("empty command")
	}

	if b.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, b.Timeout)
		defer cancel()
	}

	ec := exec.CommandContext(ctx, fields[0], fields[1:]...)
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
