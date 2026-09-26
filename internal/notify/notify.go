// Package notify sends desktop notifications for moments Belai needs the
// user: a permission ask, a clarifying question, a plan to review, a long
// turn or a goal that ended. Every message is composed here from a fixed
// template; model output, tool output and file names never reach a
// notification, so one cannot show the user text a repository chose.
package notify

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/proc"
)

// Event names, as used in settings.
const (
	EventPermission  = "permission"
	EventClarify     = "clarify"
	EventPlanReady   = "plan_ready"
	EventTurnDone    = "turn_done"
	EventGoalDone    = "goal_done"
	EventGoalStalled = "goal_stalled"
	EventAgentDone   = "agent_done"
)

// Events lists every event name.
var Events = []string{EventPermission, EventClarify, EventPlanReady, EventTurnDone, EventGoalDone, EventGoalStalled, EventAgentDone}

// DefaultEvents is the set notified when settings name none.
var DefaultEvents = []string{EventPermission, EventClarify, EventPlanReady, EventGoalDone, EventGoalStalled}

// Backend names.
const (
	BackendAuto       = "auto"
	BackendOSC        = "osc"
	BackendBell       = "bell"
	BackendNotifySend = "notify-send"
	BackendOsascript  = "osascript"
)

// Backends lists every backend name.
var Backends = []string{BackendAuto, BackendOSC, BackendBell, BackendNotifySend, BackendOsascript}

// ValidEvent reports whether name is a known event.
func ValidEvent(name string) bool {
	for _, e := range Events {
		if e == name {
			return true
		}
	}
	return false
}

// ValidBackend reports whether name is a known backend.
func ValidBackend(name string) bool {
	for _, b := range Backends {
		if b == name {
			return true
		}
	}
	return false
}

// Message returns the fixed title and body for an event. subject is a tool
// or agent name; it is reduced to a short identifier so nothing but a name
// can ride in it.
func Message(event, subject string) (title, body string) {
	subject = identifier(subject)
	title = "Belai"
	switch event {
	case EventPermission:
		if subject == "" {
			return title, "Permission needed"
		}
		return title, "Permission needed for " + subject
	case EventClarify:
		return title, "A question is waiting for you"
	case EventPlanReady:
		return title, "A plan is ready for review"
	case EventTurnDone:
		return title, "Turn finished"
	case EventGoalDone:
		return title, "Goal complete"
	case EventGoalStalled:
		return title, "Goal stopped"
	case EventAgentDone:
		if subject == "" {
			return title, "Background agent finished"
		}
		return title, "Background agent " + subject + " finished"
	}
	return title, "Belai needs you"
}

// identifier keeps [A-Za-z0-9_.:-], capped at 48 bytes.
func identifier(s string) string {
	var b strings.Builder
	for _, r := range s {
		if b.Len() >= 48 {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == ':', r == '-':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Env is the environment the backend choice reads. Tests substitute it.
type Env struct {
	Getenv   func(string) string
	LookPath func(string) (string, error)
	GOOS     string
}

// SystemEnv reads the real process environment.
func SystemEnv() Env {
	return Env{Getenv: os.Getenv, LookPath: exec.LookPath, GOOS: runtime.GOOS}
}

// oscFlavour names the escape sequence a terminal understands.
type oscFlavour int

const (
	oscNone oscFlavour = iota
	osc9               // iTerm2, Windows Terminal, ConEmu, Ghostty
	osc777             // kitty, WezTerm, foot, rxvt-unicode, Konsole
)

func (e Env) oscSupport() oscFlavour {
	switch {
	case e.Getenv("KITTY_WINDOW_ID") != "", e.Getenv("TERM_PROGRAM") == "WezTerm",
		strings.HasPrefix(e.Getenv("TERM"), "foot"), strings.HasPrefix(e.Getenv("TERM"), "rxvt"),
		e.Getenv("KONSOLE_VERSION") != "":
		return osc777
	case e.Getenv("TERM_PROGRAM") == "iTerm.app", e.Getenv("WT_SESSION") != "",
		e.Getenv("TERM_PROGRAM") == "ghostty", e.Getenv("ConEmuPID") != "":
		return osc9
	}
	return oscNone
}

// Resolve picks the concrete backend for name. auto prefers an OSC sequence
// the terminal is known to show, then the platform notifier, then the bell.
func (e Env) Resolve(name string) string {
	if name != "" && name != BackendAuto {
		return name
	}
	if e.oscSupport() != oscNone {
		return BackendOSC
	}
	switch e.GOOS {
	case "linux", "freebsd", "openbsd", "netbsd":
		if _, err := e.LookPath("notify-send"); err == nil {
			return BackendNotifySend
		}
	case "darwin":
		if _, err := e.LookPath("osascript"); err == nil {
			return BackendOsascript
		}
	}
	return BackendBell
}

// Notifier sends notifications through one backend.
type Notifier struct {
	Backend string
	Env     Env
	// TTY receives escape sequences for the osc and bell backends.
	TTY io.Writer
	// run executes an external notifier; tests substitute it.
	run func(ctx context.Context, name string, args ...string) error
}

// New returns a notifier for the named backend (auto resolves now).
func New(backend string, env Env, tty io.Writer) *Notifier {
	return &Notifier{Backend: env.Resolve(backend), Env: env, TTY: tty, run: runArgv}
}

// Send notifies event with an optional subject.
func (n *Notifier) Send(ctx context.Context, event, subject string) error {
	title, body := Message(event, subject)
	switch n.Backend {
	case BackendOSC:
		return n.write(n.oscSequence(title, body))
	case BackendBell:
		return n.write("\a")
	case BackendNotifySend:
		return n.run(ctx, "notify-send", "--app-name=Belai", title, body)
	case BackendOsascript:
		script := fmt.Sprintf("display notification %q with title %q", body, title)
		return n.run(ctx, "osascript", "-e", script)
	}
	return fmt.Errorf("unknown notification backend %q", n.Backend)
}

func (n *Notifier) oscSequence(title, body string) string {
	var seq string
	if n.Env.oscSupport() == osc777 {
		seq = "\x1b]777;notify;" + title + ";" + body + "\x07"
	} else {
		seq = "\x1b]9;" + title + ": " + body + "\x07"
	}
	if n.Env.Getenv("TMUX") != "" {
		// tmux passes an escape sequence through only when wrapped, with
		// every ESC doubled; it also needs allow-passthrough on.
		seq = "\x1bPtmux;" + strings.ReplaceAll(seq, "\x1b", "\x1b\x1b") + "\x1b\\"
	}
	return seq
}

func (n *Notifier) write(s string) error {
	if n.TTY == nil {
		return fmt.Errorf("no terminal to notify on")
	}
	_, err := io.WriteString(n.TTY, s)
	return err
}

// runArgv runs a fixed argv with the scrubbed environment and a short
// timeout, so a hung notifier never holds the UI.
func runArgv(ctx context.Context, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = proc.ScrubbedEnv()
	proc.SetProcessGroup(cmd)
	return cmd.Run()
}
