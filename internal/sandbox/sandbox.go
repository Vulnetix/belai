// Package sandbox runs the commands Signet executes for the model (Bash,
// inline !cmd, supervised processes) under an operating-system boundary:
// bubblewrap on Linux, sandbox-exec on macOS. Inside it the filesystem is
// read-only except for the workspace roots, a private /tmp and (by default)
// the usual tool caches; Signet's own state directory is hidden; and the
// network can be cut off. The policy is computed per call from the settings
// and the live guardrails switch, and rides on the call's context.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/posture"
)

// Modes and network settings.
const (
	ModeOff      = "off"
	ModeAuto     = "auto"
	ModeRequired = "required"

	NetworkAllow = "allow"
	NetworkDeny  = "deny"
)

// Policy is what one command runs under.
type Policy struct {
	// Mode is off, auto (sandbox when a backend exists) or required (refuse
	// to run without one).
	Mode string
	// DenyNetwork cuts the command off from the network.
	DenyNetwork bool
	// Writable are the paths the command may write, besides a private /tmp.
	Writable []string
	// Hidden are paths the command cannot see at all.
	Hidden []string
}

// ErrUnavailable is returned in required mode when no backend works here.
var ErrUnavailable = errors.New("sandbox required but no sandbox backend is available (install bubblewrap on Linux)")

type ctxKey struct{}

// WithPolicy attaches p to ctx for the command about to run.
func WithPolicy(ctx context.Context, p Policy) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromContext returns the policy on ctx; absent means off.
func FromContext(ctx context.Context) Policy {
	p, _ := ctx.Value(ctxKey{}).(Policy)
	return p
}

// guardrailsOff reports whether every posture gate is ignore, which is what
// the guardrails switch sets. Off takes the sandbox with it.
func guardrailsOff(p posture.Policy) bool {
	if len(p) == 0 {
		return false
	}
	for _, g := range posture.AllGates {
		if p.Level(g) != posture.Ignore {
			return false
		}
	}
	return true
}

// cacheDirs are home-relative tool caches writable in the default
// (dev-friendly) policy, so builds and package managers keep working.
var cacheDirs = []string{
	".cache", "go", ".npm", ".pnpm-store", ".yarn", ".bun", ".deno",
	".cargo", ".rustup", ".m2", ".gradle", ".nuget", ".gem", ".local/share/pnpm",
	".local/share/virtualenvs", ".pyenv", ".cache/pip", ".dotnet",
}

// FromSettings builds the policy for a command. roots are the workspace
// roots. pol is the effective posture: guardrails off turns the sandbox off.
func FromSettings(s *config.SandboxSettings, roots []string, pol posture.Policy) Policy {
	p := Policy{Mode: s.ModeOr(), DenyNetwork: s.NetworkOr() == NetworkDeny}
	if guardrailsOff(pol) {
		p.Mode = ModeOff
	}
	if p.Mode == ModeOff {
		return p
	}
	p.Writable = append(p.Writable, roots...)
	if home, err := os.UserHomeDir(); err == nil && s.CachesOr() {
		for _, d := range cacheDirs {
			p.Writable = append(p.Writable, filepath.Join(home, d))
		}
		for _, env := range []string{"GOCACHE", "GOMODCACHE", "GOPATH", "XDG_CACHE_HOME", "CARGO_HOME", "npm_config_cache"} {
			if v := os.Getenv(env); v != "" && filepath.IsAbs(v) {
				p.Writable = append(p.Writable, v)
			}
		}
	}
	if s != nil {
		for _, w := range s.ExtraWritable {
			if filepath.IsAbs(w) {
				p.Writable = append(p.Writable, filepath.Clean(w))
			}
		}
	}
	if g, err := config.GlobalDir(); err == nil {
		p.Hidden = append(p.Hidden, g)
	}
	return p
}

var (
	probeOnce sync.Once
	probeName string
	probePath string
)

// Backend returns the working backend's name ("bwrap", "sandbox-exec") and
// path, or "" when none works here. It is probed once per process: bwrap
// can be installed yet unusable when unprivileged user namespaces are off.
func Backend() (name, path string) {
	probeOnce.Do(func() {
		switch runtime.GOOS {
		case "linux":
			if p, err := exec.LookPath("bwrap"); err == nil {
				if exec.Command(p, "--ro-bind", "/", "/", "--dev", "/dev", "--", "true").Run() == nil {
					probeName, probePath = "bwrap", p
				}
			}
		case "darwin":
			if p, err := exec.LookPath("sandbox-exec"); err == nil {
				probeName, probePath = "sandbox-exec", p
			}
		}
	})
	return probeName, probePath
}

// Wrap rewrites cmd to run under p. It returns true when the command is now
// sandboxed. In auto mode with no backend it leaves cmd alone; in required
// mode it returns ErrUnavailable and cmd must not run.
func Wrap(cmd *exec.Cmd, p Policy) (bool, error) {
	if p.Mode == "" || p.Mode == ModeOff {
		return false, nil
	}
	name, path := Backend()
	if name == "" {
		if p.Mode == ModeRequired {
			return false, ErrUnavailable
		}
		return false, nil
	}
	target := cmd.Path
	if target == "" && len(cmd.Args) > 0 {
		target = cmd.Args[0]
	}
	argv := append([]string{target}, cmd.Args[1:]...)
	var args []string
	switch name {
	case "bwrap":
		args = BwrapArgs(p, cmd.Dir, argv)
	case "sandbox-exec":
		args = append([]string{"-p", SeatbeltProfile(p), "--"}, argv...)
	}
	cmd.Path = path
	cmd.Args = append([]string{path}, args...)
	cmd.Err = nil
	return true, nil
}

// BwrapArgs builds the bubblewrap command line (without the bwrap binary).
func BwrapArgs(p Policy, dir string, argv []string) []string {
	args := []string{
		"--die-with-parent",
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--proc", "/proc",
		"--tmpfs", "/tmp",
	}
	for _, w := range uniq(p.Writable) {
		args = append(args, "--bind-try", w, w)
	}
	for _, h := range uniq(p.Hidden) {
		if _, err := os.Stat(h); err == nil {
			args = append(args, "--tmpfs", h)
		}
	}
	if p.DenyNetwork {
		args = append(args, "--unshare-net")
	}
	if dir != "" {
		args = append(args, "--chdir", dir)
	}
	args = append(args, "--")
	return append(args, argv...)
}

// SeatbeltProfile builds the sandbox-exec profile for p.
func SeatbeltProfile(p Policy) string {
	var b strings.Builder
	b.WriteString("(version 1)\n(allow default)\n(deny file-write*)\n")
	b.WriteString("(allow file-write* (subpath \"/private/tmp\") (subpath \"/private/var/folders\") (literal \"/dev/null\") (regex #\"^/dev/tty\")")
	for _, w := range uniq(p.Writable) {
		fmt.Fprintf(&b, " (subpath %s)", quote(w))
	}
	b.WriteString(")\n")
	for _, h := range uniq(p.Hidden) {
		fmt.Fprintf(&b, "(deny file-read* file-write* (subpath %s))\n", quote(h))
	}
	if p.DenyNetwork {
		b.WriteString("(deny network-outbound (remote ip))\n(deny network-inbound (local ip))\n")
	}
	return b.String()
}

// quote renders a path as a sandbox profile string literal.
func quote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = filepath.Clean(s)
		if s == "" || s == "." || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Status is the one-word footer state: on, off or n/a (wanted, but no
// backend here).
func Status(s *config.SandboxSettings, pol posture.Policy) string {
	if s.ModeOr() == ModeOff || guardrailsOff(pol) {
		return "off"
	}
	if name, _ := Backend(); name == "" {
		return "n/a"
	}
	return "on"
}
