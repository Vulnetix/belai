// Package desktop reads the process environment to decide whether this process
// can put something in front of a person at this machine, today whether it may
// open a web browser.
//
// A `belai rc` daemon is usually headless, detached, or on a host nobody sits
// at; opening a browser there does nothing useful, and on a desktop it can
// raise an error dialog from the platform's URL handler. What such a process
// needs from a person is sent to the Vulnetix website instead (docs/remote-control.md).
//
// Env mirrors internal/notify.Env: the lookups are fields so tests substitute
// them, and SystemEnv reads the real process.
package desktop

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// Environment variables that switch the browser off.
const (
	// EnvNoBrowser forces the answer to "no" for this process and its children.
	EnvNoBrowser = "BELAI_NO_BROWSER"
	// EnvRCDetached marks the detached `belai rc` daemon (set by rc itself).
	EnvRCDetached = "BELAI_RC_DETACHED"
	// EnvNoTUI marks a non-interactive run.
	EnvNoTUI = "BELAI_NO_TUI"
)

// Env is the environment the decision reads. Tests substitute it.
type Env struct {
	Getenv   func(string) string
	LookPath func(string) (string, error)
	GOOS     string
}

// SystemEnv reads the real process environment.
func SystemEnv() Env {
	return Env{Getenv: os.Getenv, LookPath: exec.LookPath, GOOS: runtime.GOOS}
}

// set reports whether an environment value switches something on: non-empty
// and not an explicit "no".
func set(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "no", "off":
		return false
	}
	return true
}

// CanOpenBrowser reports whether opening a browser here would reach a person.
// When it would not, reason says why in a form fit for a log line.
//
// It says no for a forced-off, detached, non-interactive or CI run, for an SSH
// session (the browser would open on the remote side), and on Linux and the
// BSDs for a process with no graphical session or no xdg-open. macOS and
// Windows have no display variable to read, so the first three checks decide.
func (e Env) CanOpenBrowser() (ok bool, reason string) {
	switch {
	case set(e.Getenv(EnvNoBrowser)):
		return false, EnvNoBrowser + " is set"
	case e.Getenv(EnvRCDetached) == "1":
		return false, "this is the detached remote-control daemon"
	case set(e.Getenv(EnvNoTUI)):
		return false, "this is a non-interactive run (" + EnvNoTUI + ")"
	case set(e.Getenv("CI")):
		return false, "this is a CI run"
	case e.Getenv("SSH_CONNECTION") != "" || e.Getenv("SSH_TTY") != "":
		return false, "this is an SSH session, so a browser would open on the remote host"
	}
	switch e.GOOS {
	case "linux", "freebsd", "openbsd", "netbsd":
		if e.Getenv("WAYLAND_DISPLAY") == "" && e.Getenv("DISPLAY") == "" {
			return false, "there is no graphical session (no WAYLAND_DISPLAY or DISPLAY)"
		}
		if _, err := e.LookPath("xdg-open"); err != nil {
			return false, "xdg-open is not installed"
		}
	}
	return true, ""
}
