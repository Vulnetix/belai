// Package sandbox runs the commands Belai executes for the model (Bash,
// inline !cmd, supervised processes) under an operating-system boundary:
// bubblewrap on Linux, sandbox-exec on macOS. Inside it the filesystem is
// read-only except for the workspace roots, a private /tmp and (by default)
// the usual tool caches; Belai's own state directory is hidden; and the
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
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/vaultenv"
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
	// Visible are paths the command may read even where the default layout
	// hides them (under the private /tmp). They stay read-only.
	Visible []string
	// Mounts are applied after everything else, in order, each over the
	// last: a writable directory can be narrowed by read-only entries inside
	// it and those re-opened in turn. A fleet worker's git common dir uses
	// this (fleet.Workspace.SandboxMounts).
	Mounts []Mount
	// Env is added to the command's environment when it is sandboxed: a
	// fleet worker's git writes new objects to its own store through it.
	Env []string
	// Hidden are paths the command cannot see at all.
	Hidden []string
}

// Mount is one ordered filesystem rule: Path readable, and writable or not.
type Mount struct {
	Path     string
	Writable bool
}

// EnvMarker is set to 1 in the environment of every sandboxed command.
const EnvMarker = "BELAI_SANDBOXED"

// Nested reports whether this process runs inside a Belai sandbox.
func Nested() bool { return os.Getenv(EnvMarker) != "" }

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

// customTempDir returns TMPDIR when it names a directory the private /tmp does
// not already cover. A host that points TMPDIR elsewhere (the Pix sandbox image
// uses /workspace/tmp) would otherwise get a read-only temp directory, and
// every tool that makes a work dir there (go build, pip, npm) fails. A TMPDIR at
// or under /tmp is left alone: binding the host's /tmp would defeat the private
// one. Where TMPDIR is a macOS per-user folder the seatbelt profile already
// allows it.
func customTempDir(v string) string {
	if v == "" || !filepath.IsAbs(v) {
		return ""
	}
	v = filepath.Clean(v)
	for _, covered := range []string{"/", "/tmp", "/private/tmp", "/var/folders", "/private/var/folders"} {
		if v == covered || (covered != "/" && strings.HasPrefix(v, covered+"/")) {
			return ""
		}
	}
	return v
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
		if t := customTempDir(os.Getenv("TMPDIR")); t != "" {
			p.Writable = append(p.Writable, t)
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
	probeWhy  string
)

// Probe tunables, shortened by tests.
var (
	probeTries   = 4
	probeBackoff = 250 * time.Millisecond
	// runBwrapProbe runs the one command that proves bubblewrap works here and
	// returns what it printed with its error.
	runBwrapProbe = func(path string) (string, error) {
		out, err := exec.Command(path, "--ro-bind", "/", "/", "--dev", "/dev", "--", "true").CombinedOutput()
		return string(out), err
	}
)

// transientProbeFailure reports a failure that says the machine was out of
// processes, threads or memory at that moment, not that bubblewrap cannot work
// here: a fork refused with EAGAIN, or an allocation refused. A fleet of
// workers on a small machine reaches this, and a probe that took it as "no
// backend" would refuse every autonomous worker with Bash until the process
// that probed ended.
func transientProbeFailure(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "resource temporarily unavailable") || strings.Contains(m, "cannot allocate memory") || strings.Contains(m, "eagain")
}

// Backend returns the working backend's name ("bwrap", "sandbox-exec") and
// path, or "" when none works here. It is probed once per process: bwrap
// can be installed yet unusable when unprivileged user namespaces are off.
// A probe that fails because the machine is out of processes or memory is tried
// again a few times before it counts, and BackendProblem says why it failed.
func Backend() (name, path string) {
	probeOnce.Do(func() {
		switch runtime.GOOS {
		case "linux":
			p, err := exec.LookPath("bwrap")
			if err != nil {
				probeWhy = "bwrap is not installed"
				return
			}
			for try := 0; try < probeTries; try++ {
				out, err := runBwrapProbe(p)
				if err == nil {
					probeName, probePath, probeWhy = "bwrap", p, ""
					return
				}
				probeWhy = strings.TrimSpace(strings.Join(strings.Fields(out+" "+err.Error()), " "))
				if len(probeWhy) > 200 {
					probeWhy = probeWhy[:200]
				}
				if !transientProbeFailure(probeWhy) {
					return
				}
				time.Sleep(probeBackoff << try)
			}
		case "darwin":
			if p, err := exec.LookPath("sandbox-exec"); err == nil {
				probeName, probePath = "sandbox-exec", p
			}
		}
	})
	return probeName, probePath
}

// BackendProblem is why Backend found no working backend, in a few words, or
// "" when it found one (or has not been asked). It is what a refusal quotes, so
// the person reading it can tell a machine out of processes from one that
// cannot run bubblewrap at all.
func BackendProblem() string {
	Backend()
	return probeWhy
}

// ErrPIDIsolation is returned when a command would hold vault variables and the sandbox
// can neither give it its own pid namespace nor show that it cannot read another
// process's /proc/PID/environ. The command is not run.
var ErrPIDIsolation = errors.New("a command holding vault variables needs its own pid namespace, bubblewrap cannot mount /proc in one here, and the sandbox does not keep a command from reading other processes' environments, so the command was not run")

// pidMode is how a command that holds vault variables is kept from reading another
// process's /proc/PID/environ.
type pidMode int

const (
	// pidNone: neither works. Such a command must not run.
	pidNone pidMode = iota
	// pidNamespace: bwrap mounts a fresh /proc in its own pid namespace.
	pidNamespace
	// pidUserNS: no fresh /proc, but the user namespace bwrap always creates already
	// makes another process's environ unreadable, which the probe checked.
	pidUserNS
)

var (
	pidOnce  sync.Once
	pidState pidMode
	// pidModeFn is swapped by tests.
	pidModeFn = pidIsolation
)

// pidIsolation probes once. A container that masks paths under /proc (Cloudflare
// containers do: acpi, kcore, keys) refuses a fresh /proc mount in a new pid namespace
// ("Can't mount proc on /proc: Operation not permitted"); the namespace is then
// replaced only when a command inside the sandbox demonstrably cannot read the
// environment of a process outside it, never on an assumption.
func pidIsolation() pidMode {
	pidOnce.Do(func() {
		name, path := Backend()
		if name != "bwrap" {
			return
		}
		base := []string{"--ro-bind", "/", "/", "--dev", "/dev"}
		if exec.Command(path, append(append([]string{}, base...), "--proc", "/proc", "--unshare-pid", "--", "true")...).Run() == nil {
			pidState = pidNamespace
			return
		}
		holder := exec.Command("sleep", "30")
		holder.Env = append(os.Environ(), "BELAI_PIDPROBE=1")
		if holder.Start() != nil {
			return
		}
		defer func() { _ = holder.Process.Kill(); _ = holder.Wait() }()
		// Exit 3: the probe cannot read its own environ, so it proves nothing. Exit 0:
		// it read the other process's. Exit 4: it could not.
		script := fmt.Sprintf("( : < /proc/self/environ ) || exit 3; ( : < /proc/%d/environ ) 2>/dev/null && exit 0; exit 4", holder.Process.Pid)
		args := append(append([]string{}, base...), "--proc", "/proc", "--tmpfs", "/tmp", "--", "sh", "-c", script)
		if err := exec.Command(path, args...).Run(); err != nil {
			if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 4 {
				pidState = pidUserNS
			}
		}
	})
	return pidState
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
	if name == "bwrap" && vaultenv.Default.HasActive(time.Now()) && pidModeFn() == pidNone {
		return false, ErrPIDIsolation
	}
	// Mark the command as sandboxed, so a test that would nest a second
	// sandbox inside this one (bubblewrap cannot reliably see the outer
	// sandbox's private /tmp) knows to skip.
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Env = append(cmd.Env, EnvMarker+"=1")
	if len(p.Env) > 0 {
		if cmd.Env == nil {
			cmd.Env = os.Environ()
		}
		cmd.Env = append(cmd.Env, p.Env...)
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
	// Visible paths are re-exposed read-only after the private /tmp, and
	// before the writable binds so a writable path inside one stays writable.
	for _, v := range uniq(p.Visible) {
		args = append(args, "--ro-bind-try", v, v)
	}
	for _, w := range uniq(p.Writable) {
		args = append(args, "--bind-try", w, w)
	}
	for _, m := range p.Mounts {
		if m.Writable {
			args = append(args, "--bind-try", m.Path, m.Path)
		} else {
			args = append(args, "--ro-bind-try", m.Path, m.Path)
		}
	}
	for _, h := range uniq(p.Hidden) {
		if _, err := os.Stat(h); err == nil {
			args = append(args, "--tmpfs", h)
		}
	}
	if p.DenyNetwork {
		args = append(args, "--unshare-net")
	}
	// With vault variables in the command's environment, give it its own pid
	// namespace: it cannot see another process (a background command, an earlier
	// call's leftovers) to read its /proc/PID/environ.
	// Where a fresh /proc cannot be mounted in one, pidIsolation has checked that the
	// user namespace bwrap always makes keeps another process's environ unreadable
	// anyway, and the flag is left off; Wrap refuses when neither holds.
	if vaultenv.Default.HasActive(time.Now()) && pidModeFn() != pidUserNS {
		args = append(args, "--unshare-pid")
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
	// Seatbelt lets a later rule override an earlier one, which gives the
	// ordered mounts the same layering bubblewrap does.
	for _, m := range p.Mounts {
		if m.Writable {
			fmt.Fprintf(&b, "(allow file-write* (subpath %s))\n", quote(m.Path))
		} else {
			fmt.Fprintf(&b, "(deny file-write* (subpath %s))\n", quote(m.Path))
		}
	}
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
