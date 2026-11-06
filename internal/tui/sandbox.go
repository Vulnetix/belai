package tui

import (
	"fmt"
	"strings"

	"github.com/vulnetix/belai/internal/sandbox"
)

// egressFallback says what a proxy request becomes where the backend cannot
// confine a command to it.
func egressFallback(mode string) string {
	if mode == sandbox.ModeRequired {
		return "commands are refused"
	}
	return "commands run as network deny does there"
}

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
		why := sandbox.BackendProblem()
		if why != "" {
			why = " (" + why + ")"
		}
		if a.settings.Sandbox.ModeOr() == sandbox.ModeRequired {
			return "sandbox: required but no backend is available here · commands are refused" + why
		}
		return "sandbox: n/a · no backend is available here, so commands run unsandboxed" + why
	}
	network := "allowed"
	switch {
	case p.DenyNetwork:
		network = "denied"
		if name == "landlock" && sandbox.NetworkDenyEnforced() {
			network = "denied (TCP only)"
		}
	case p.Egress:
		switch {
		case !sandbox.NetworkDenyEnforced():
			network = "proxy requested, unavailable here: " + egressFallback(p.Mode)
		case name == "landlock":
			network = fmt.Sprintf("proxy, %d allowed host(s) (TCP only)", len(p.EgressAllow))
		default:
			network = fmt.Sprintf("proxy, %d allowed host(s)", len(p.EgressAllow))
		}
		network += "; /egress lists them"
	}
	fmt.Fprintf(&b, "sandbox: on (%s, mode %s) · network %s", sandbox.Describe(), p.Mode, network)
	tmp, hidden := ", a private /tmp", ""
	if name == "landlock" {
		tmp, hidden = ", /tmp (shared with the host)", " (listable, unreadable under Landlock)"
	}
	b.WriteString("\n  writable: " + strings.Join(p.Writable, ", ") + tmp)
	if len(p.Hidden) > 0 {
		b.WriteString("\n  hidden: " + strings.Join(p.Hidden, ", ") + hidden)
	}
	for _, l := range sandbox.Limits() {
		b.WriteString("\n  limits: " + l)
	}
	return b.String()
}
