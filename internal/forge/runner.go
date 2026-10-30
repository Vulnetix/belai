package forge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

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
	return execRun(ctx, dir, nil, argv)
}

// NonInteractiveRunner is ExecRunner for git work nobody is watching: git may
// not prompt for credentials or open an editor, so a call that would wait for
// a person fails at once instead of hanging until its timeout.
func NonInteractiveRunner(ctx context.Context, dir string, argv ...string) ([]byte, error) {
	return execRun(ctx, dir, []string{"GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true", "GIT_SEQUENCE_EDITOR=true", "GCM_INTERACTIVE=never"}, argv)
}

func execRun(ctx context.Context, dir string, env []string, argv []string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, errors.New("forge: empty command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
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

// HardenedGit is every git call made for work nobody is watching (a fleet
// worker, teleport): repository hooks and fsmonitor off (a repository's own
// config must not run code), no file:// transport, no credential prompt, the
// scrubbed environment, its own process group, no stdin. With gitDir set it
// also pins --git-dir and --work-tree, so a worktree's .git file, which a
// model can write, never decides which repository git opens.
func HardenedGit(gitDir, workTree string, extraEnv ...string) Runner {
	return func(ctx context.Context, dir string, argv ...string) ([]byte, error) {
		if len(argv) == 0 {
			return nil, errors.New("forge: empty command")
		}
		if argv[0] == "git" {
			pre := []string{"git",
				"-c", "core.hooksPath=" + os.DevNull,
				"-c", "core.fsmonitor=false",
				"-c", "protocol.file.allow=never",
				"-c", "credential.interactive=never",
			}
			if gitDir != "" {
				pre = append(pre, "--git-dir="+gitDir, "--work-tree="+workTree)
			}
			argv = append(pre, argv[1:]...)
		}
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Dir = dir
		cmd.Env = append(append(proc.ScrubbedEnv(), "GIT_TERMINAL_PROMPT=0"), extraEnv...)
		proc.SetProcessGroup(cmd)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			msg := strings.TrimSpace(stderr.String())
			if i := strings.LastIndexByte(msg, '\n'); i >= 0 {
				msg = msg[i+1:]
			}
			if ctx.Err() != nil {
				msg = "timed out"
			}
			if msg == "" {
				msg = err.Error()
			}
			return stdout.Bytes(), fmt.Errorf("%s: %s", argv[0], Clean(msg))
		}
		return stdout.Bytes(), nil
	}
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
// is removed, whitespace becomes single spaces, control, bidi and zero-width
// runes are dropped, and the result is capped at maxCleanLen runes.
func Clean(s string) string { return sanitize.Clip(s, maxCleanLen) }

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
