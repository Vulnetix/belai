package tui

import (
	"fmt"
	"strings"

	"github.com/vulnetix/belai/internal/sandbox"
)

// sandboxReport renders /sandbox: whether commands run sandboxed here, with
// which backend, and what the policy lets them touch.
func (a *App) sandboxReport() string {
	pol := a.effectivePosture()
	status := sandbox.Status(a.settings.Sandbox, pol)
	p := sandbox.FromSettings(a.settings.Sandbox, append([]string{a.workdir}, a.workspaceDirs...), pol)
	var b strings.Builder
	name, _ := sandbox.Backend()
	switch status {
	case "off":
		b.WriteString("sandbox: off · Bash, !cmd and supervised processes run unsandboxed")
		if a.settings.Sandbox.ModeOr() != sandbox.ModeOff {
			b.WriteString(" (guardrails are off)")
		}
		return b.String()
	case "n/a":
		if a.settings.Sandbox.ModeOr() == sandbox.ModeRequired {
			return "sandbox: required but no backend is available here · commands are refused (install bubblewrap on Linux)"
		}
		return "sandbox: n/a · no backend is available here, so commands run unsandboxed (install bubblewrap on Linux)"
	}
	network := "allowed"
	if p.DenyNetwork {
		network = "denied"
	}
	fmt.Fprintf(&b, "sandbox: on (%s, mode %s) · network %s", name, p.Mode, network)
	b.WriteString("\n  writable: " + strings.Join(p.Writable, ", ") + ", a private /tmp")
	if len(p.Hidden) > 0 {
		b.WriteString("\n  hidden: " + strings.Join(p.Hidden, ", "))
	}
	return b.String()
}
