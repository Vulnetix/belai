package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/rc"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sandbox"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/tools"
)

// rcShellTimeout bounds the classifier's look at one web shell line's output.
const rcShellTimeout = 30 * time.Second

// rcShellRunTimeout bounds the command itself. The page shows the output as it
// is produced, so a long build or a sign-in that waits for a browser is usable;
// this is the Bash tool's own ceiling.
const rcShellRunTimeout = tools.BashMaxTimeout

// rcShellEffective is the settings and posture a shell line is judged by now:
// they follow the session's web controls when it has them.
type rcShellEffective func() (config.Settings, posture.Policy)

// rcShellRunner runs the shell lines a web session sends (belai rc
// --web-shell). It is the TUI's handleShell without the panel: the same
// plan-mode gate, the same Bash tool under the same OS sandbox, and the same
// classification of the output. What a line's output does afterwards (shown,
// recorded, attached to the next turn) is internal/rc's decision, not this
// function's.
func rcShellRunner(workdir string, eff rcShellEffective, cfg run.Config, client *http.Client, cache *rolemanager.Cache) rc.ShellRunner {
	return func(ctx context.Context, req rc.ShellRequest) rc.ShellOutcome {
		cmd := strings.TrimSpace(req.Command)
		if cmd == "" {
			return rc.ShellOutcome{Refused: "the line is empty"}
		}
		settings, pol := eff()
		perms := permissions.From(settings.Permissions.Allow, settings.Permissions.Ask, settings.Permissions.Deny)
		surface := tools.PlanSurface{GuardrailsOff: !settings.GuardrailsEnabled(), Perms: perms}
		if !modes.ToolAllowed("bash", map[string]any{"command": cmd}, req.PlanMode, surface) {
			return rc.ShellOutcome{Refused: "not allowed in plan mode"}
		}
		switch dec, rule := perms.Explain("Bash", cmd); dec {
		case permissions.DecisionBlock:
			return rc.ShellOutcome{Refused: "blocked by the permission rule " + rule}
		case permissions.DecisionAsk:
			return rc.ShellOutcome{Refused: "the permission rule " + rule + " asks first, and nobody can answer on the host"}
		}

		root, dir, err := rcShellDir(workdir, req.Cwd)
		if err != nil {
			return rc.ShellOutcome{Refused: err.Error()}
		}
		if target, ok := rcShellCd(cmd); ok {
			next, err := rcShellChdir(root, dir, target)
			if err != nil {
				return rc.ShellOutcome{Output: "cd: " + err.Error(), ExitCode: 1, Cwd: dir, Safe: true}
			}
			return rc.ShellOutcome{Cwd: next, Safe: true}
		}

		reg := tools.Default(root, settings.ReadOnlyEnabled())
		if rel, err := filepath.Rel(root, dir); err == nil && rel != "." {
			if _, err := reg.Cwd().Change(rel); err != nil {
				return rc.ShellOutcome{Refused: "the directory is not available: " + err.Error()}
			}
		}
		bash, ok := reg.Find("Bash")
		if !ok {
			return rc.ShellOutcome{Err: fmt.Errorf("Bash tool not registered"), Cwd: dir}
		}
		runCtx, cancel := context.WithTimeout(sandbox.WithPolicy(ctx, sandbox.FromSettings(settings.Sandbox, []string{root}, pol)), rcShellRunTimeout)
		defer cancel()
		if req.Started != nil {
			req.Started()
		}
		args := map[string]any{"command": cmd, "timeout": int64(rcShellRunTimeout / time.Millisecond)}
		var res tools.Result
		if st, ok := bash.(tools.StreamingTool); ok && req.Stream != nil {
			res, err = st.ExecuteStream(runCtx, args, func(p tools.Progress) { req.Stream(p.Text) })
		} else {
			res, err = bash.Execute(runCtx, args)
		}
		if err != nil {
			return rc.ShellOutcome{Err: err, Cwd: dir}
		}
		out := rc.ShellOutcome{Output: res.Content, ExitCode: rcShellExit(res.Content), Cwd: dir}

		// With the gate ignored the verdict cannot change the outcome, so the
		// classifier is not called, as in the TUI. Sanitising still runs.
		out.Body = sanitize.Sanitize(res.Content)
		if pol.Level(posture.ToolResultUnsafe) == posture.Ignore {
			out.Safe = true
			return out
		}
		classCtx, cancelClass := context.WithTimeout(ctx, rcShellTimeout)
		defer cancelClass()
		dec, perr := run.NewPipeline(cfg, client, cache).Process(classCtx, res)
		switch {
		case perr != nil:
			out.Verdict = "classifier failed"
		case dec.Action == rolemanager.ActionProceed && dec.Sentinel.IsSafe():
			out.Body, out.Safe = dec.Content, true
		default:
			out.Verdict = dec.Sentinel.Label()
		}
		return out
	}
}

// rcShellCache is the classifier's verdict cache, loaded as the TUI loads it.
func rcShellCache() *rolemanager.Cache {
	cache, _ := rolemanager.LoadCache(rolemanager.DefaultCachePath())
	return cache
}

// rcShellDir resolves the directory a line runs in: the session's workspace by
// default, or the directory the page named when it is inside it. A symlink out
// of the workspace is refused, so the page cannot name a way out.
func rcShellDir(workdir, requested string) (root, dir string, err error) {
	root, err = filepath.EvalSymlinks(workdir)
	if err != nil {
		return "", "", fmt.Errorf("the session directory is not available")
	}
	if strings.TrimSpace(requested) == "" {
		return root, root, nil
	}
	dir, err = rcShellInside(root, requested)
	return root, dir, err
}

// rcShellInside returns path, resolved, when it is an existing directory at or
// below root.
func rcShellInside(root, path string) (string, error) {
	abs, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("%s: no such directory", path)
	}
	if abs != root && !strings.HasPrefix(abs, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("%s is outside the session's workspace", path)
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		return "", fmt.Errorf("%s: not a directory", path)
	}
	return abs, nil
}

// rcShellCdRE is a line that only changes directory: `cd`, `cd PATH` with no
// shell syntax in it.
var rcShellCdRE = regexp.MustCompile("^cd(?:\\s+([^\\s;&|<>$`(){}*?\\[\\]\"'\\\\]+))?$")

// rcShellCd reports whether line is a bare `cd` and where it goes. A change of
// directory needs no process: each line runs in a fresh sandbox, so the
// console keeps its directory by asking the host to resolve the move.
func rcShellCd(line string) (string, bool) {
	m := rcShellCdRE.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return "", false
	}
	return m[1], true
}

// rcShellChdir resolves a `cd` target from dir, inside root.
func rcShellChdir(root, dir, target string) (string, error) {
	switch {
	case target == "" || target == "~":
		return root, nil
	case filepath.IsAbs(target):
		return rcShellInside(root, target)
	default:
		return rcShellInside(root, filepath.Join(dir, target))
	}
}

var rcShellExitRE = regexp.MustCompile(`(?m)^exit status (\d+)$`)

// rcShellExit reads the exit code the Bash tool appends to a failed command.
func rcShellExit(content string) int {
	all := rcShellExitRE.FindAllStringSubmatch(content, -1)
	if len(all) == 0 {
		// The Bash tool reports a timeout in the output and returns no status.
		// 124 is timeout(1)'s code for it.
		if strings.Contains(content, "\n… command timed out after ") {
			return 124
		}
		return 0
	}
	n, err := strconv.Atoi(all[len(all)-1][1])
	if err != nil {
		return 1
	}
	return n
}
