// Package rc is Belai remote control: `belai rc` runs a coordinator daemon
// that registers this machine with the Vulnetix website, advertises the
// directories it will work in, and starts a headless, synced Belai session
// whenever the website asks for one. Each session is its own child process
// (`belai rc-session`), so one crashing never takes the daemon or another
// session with it.
//
// Nothing the website sends is trusted: the daemon re-checks every directory
// against its own list, a session's prompts go through the same admission as
// a typed prompt, and a session never asks anyone anything (asks are off; the
// posture and permission rules decide, as for `belai -prompt`).
package rc

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/credentials"
	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/vulnetixcli"
)

// SessionsURL is the website page that lists hosts and starts sessions on them.
const SessionsURL = "https://www.vulnetix.com/resolve/belai-sessions"

// ManageURL is SessionsURL on the configured origin ($VULNETIX_WEB_URL).
func ManageURL(webURL string) string {
	if webURL == "" {
		return SessionsURL
	}
	return strings.TrimRight(webURL, "/") + "/resolve/belai-sessions"
}

// Level grades a Check.
type Level int

const (
	// OK passes.
	OK Level = iota
	// Warn does not stop rc but limits it.
	Warn
	// Fail stops rc until fixed.
	Fail
)

// Check is one preflight step: what was checked, what was found and, when it
// did not pass, the one thing to do about it.
type Check struct {
	Name   string
	Level  Level
	Detail string
	Fix    string
}

// Checks is a preflight result.
type Checks []Check

// OK reports whether nothing failed.
func (cs Checks) OK() bool {
	for _, c := range cs {
		if c.Level == Fail {
			return false
		}
	}
	return true
}

// Find returns the named check.
func (cs Checks) Find(name string) (Check, bool) {
	for _, c := range cs {
		if c.Name == name {
			return c, true
		}
	}
	return Check{}, false
}

// Check names, stable for the TUI's setup screens.
const (
	CheckCLI        = "Vulnetix CLI"
	CheckCredential = "Vulnetix login"
	CheckSync       = "Session sync"
	CheckGuardrails = "Guardrails"
	CheckDirs       = "Directories"
	CheckServer     = "Vulnetix website"
)

// PreflightOptions are the inputs Preflight reads. Zero functions take the
// real ones; tests replace them.
type PreflightOptions struct {
	Workdir  string
	Settings config.Settings
	Dirs     []Dir
	// Server, when non-nil, is called last with a usable credential to
	// confirm the website accepts it (a PutHost). Skipped when nil.
	Server func(ctx context.Context, header string) error

	LookPath   func(string) (string, error)
	AuthHeader func(workdir string) (string, error)
}

// Preflight runs every check in order. It never stops early, so the output
// lists everything that needs doing at once.
func Preflight(ctx context.Context, o PreflightOptions) Checks {
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	if o.AuthHeader == nil {
		o.AuthHeader = credentials.VulnetixAuthHeader
	}
	var out Checks

	// The CLI is how you log in; a credential from the environment or a
	// credentials file works without it, so its absence alone is a warning.
	header, credErr := o.AuthHeader(o.Workdir)
	if p, err := o.LookPath("vulnetix"); err == nil {
		out = append(out, Check{Name: CheckCLI, Level: OK, Detail: p})
	} else {
		lvl := Warn
		if credErr != nil {
			lvl = Fail
		}
		out = append(out, Check{Name: CheckCLI, Level: lvl, Detail: "vulnetix is not on PATH", Fix: InstallHint()})
	}

	switch {
	case credErr != nil:
		out = append(out, Check{Name: CheckCredential, Level: Fail,
			Detail: "no Vulnetix credential found",
			Fix:    "run `vulnetix auth login` (it opens your browser)"})
	case sessionsync.UsableCredential(header) != nil:
		detail := "this credential is not accepted by the website"
		if errors.Is(sessionsync.UsableCredential(header), sessionsync.ErrTokenCredential) {
			detail = "the Vulnetix CLI is logged in with an API token, which the website does not accept for remote control"
		}
		out = append(out, Check{Name: CheckCredential, Level: Fail, Detail: detail,
			Fix: "run `vulnetix auth login` and sign in through the browser (not --token), and unset VULNETIX_API_TOKEN"})
	default:
		out = append(out, Check{Name: CheckCredential, Level: OK, Detail: credentialKind(header)})
	}

	switch {
	case !o.Settings.SyncEnabled():
		out = append(out, Check{Name: CheckSync, Level: Fail,
			Detail: "session sync is off, so the website cannot see or start sessions",
			Fix:    "run /sync on in Belai, or set \"sync\": {\"enabled\": true} in ~/.vulnetix/belai/settings.json"})
	case !o.Settings.SyncRemotePromptsEnabled():
		out = append(out, Check{Name: CheckSync, Level: Warn,
			Detail: "sync.remote_prompts is off: sessions start from the website but take no follow-up prompts",
			Fix:    "set \"sync\": {\"remote_prompts\": true} to prompt them from the website"})
	default:
		out = append(out, Check{Name: CheckSync, Level: OK, Detail: "on"})
	}

	if o.Settings.GuardrailsEnabled() {
		out = append(out, Check{Name: CheckGuardrails, Level: OK, Detail: "on"})
	} else {
		out = append(out, Check{Name: CheckGuardrails, Level: Fail,
			Detail: "guardrails are off; remote control never runs sessions nobody is watching without them",
			Fix:    "turn guardrails back on (f3 in Belai, or remove \"guardrails\": false from settings)"})
	}

	if len(o.Dirs) == 0 {
		out = append(out, Check{Name: CheckDirs, Level: Fail,
			Detail: "no directory to offer: nothing is trusted yet and no --dir was given",
			Fix:    "run `belai rc --dir <path>`, or open `belai` in a project once and trust it"})
	} else {
		out = append(out, Check{Name: CheckDirs, Level: OK, Detail: fmt.Sprintf("%d offered", len(o.Dirs))})
	}

	if o.Server != nil && credErr == nil && sessionsync.UsableCredential(header) == nil {
		if err := o.Server(ctx, header); err != nil {
			c := Check{Name: CheckServer, Level: Fail, Detail: err.Error(), Fix: "check your network and try again"}
			if errors.Is(err, sessionsync.ErrUnauthorized) {
				c.Detail = "the website refused this credential"
				c.Fix = "run `vulnetix auth login` again"
			}
			out = append(out, c)
		} else {
			out = append(out, Check{Name: CheckServer, Level: OK, Detail: "connected"})
		}
	}
	return out
}

func credentialKind(header string) string {
	if strings.HasPrefix(header, "ApiKey ") {
		if org, _, ok := strings.Cut(strings.TrimPrefix(header, "ApiKey "), ":"); ok {
			return "organisation " + org
		}
	}
	return "browser login"
}

// InstallHint is how to install the Vulnetix CLI on this OS.
func InstallHint() string {
	if plan, ok := vulnetixcli.PlanInstall(runtime.GOOS, nil); ok && len(plan.Steps) > 0 {
		return "install it: " + plan.Commands()
	}
	return "install it: " + vulnetixcli.ManualInstallHint
}

// Render writes checks as ✓ / ! / ✗ lines, each non-passing line followed by
// its fix.
func (cs Checks) Render() string {
	var b strings.Builder
	for _, c := range cs {
		mark := "✓"
		switch c.Level {
		case Warn:
			mark = "!"
		case Fail:
			mark = "✗"
		}
		fmt.Fprintf(&b, "  %s %-18s %s\n", mark, c.Name, c.Detail)
		if c.Level != OK && c.Fix != "" {
			fmt.Fprintf(&b, "    %-18s → %s\n", "", c.Fix)
		}
	}
	return b.String()
}
