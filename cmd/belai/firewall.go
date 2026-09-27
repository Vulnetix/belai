package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/vulnetix/belai/internal/firewall"
	"github.com/vulnetix/belai/internal/run"
)

// forceFirewall is set by -firewall or BELAI_FIREWALL: the active AI
// Firewall is on for this process whatever the settings files say.
var forceFirewall bool

// watchFirewallHeadless prints each firewall event of a headless run as one
// line on w. Verdicts are cleaned, capped facts; stdout is never touched, so
// the reply stays machine-readable. ACP registers no observer at all.
func watchFirewallHeadless(w io.Writer) (cancel func()) {
	return run.SetFirewallObserver(func(v firewall.Verdict) {
		fmt.Fprintln(w, firewallLine(v))
	}, nil)
}

func firewallLine(v firewall.Verdict) string {
	parts := []string{"firewall: " + v.Firewall + " " + string(v.Action)}
	if v.Code != "" {
		parts = append(parts, "code="+v.Code)
	}
	if len(v.Rules) > 0 {
		parts = append(parts, "rules="+strings.Join(v.Rules, ", "))
	}
	if v.Redactions > 0 {
		parts = append(parts, fmt.Sprintf("redactions=%d", v.Redactions))
	}
	if v.RequestID != "" {
		parts = append(parts, "request="+v.RequestID)
	}
	if v.Hint != "" {
		parts = append(parts, v.Hint)
	}
	return strings.Join(parts, " · ")
}
