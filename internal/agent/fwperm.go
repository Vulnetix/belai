package agent

import (
	"context"
	"time"

	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
)

// firewallPermTimeout bounds one prompt's whole permission pass: the
// detector, the live-rule read and the writer.
const firewallPermTimeout = 2 * time.Minute

// FirewallPermissionHook wires the firewall permission pass
// (docs/firewall-permissions.md) to its host. The pass runs only when the
// host set one; headless, ACP and fleet sessions do not, so it is off there.
// Every func is called from the pass's own goroutine, never the turn's.
type FirewallPermissionHook struct {
	// Enabled reports whether the pass may run now: the Vulnetix CLI is
	// signed in. It is read once per prompt, before any model is asked.
	Enabled func() bool
	// Live reads the org's guardrails, so the writer skips rules already in
	// force and the variants follow each family's mode. An error ends the
	// pass: an offer that could not be checked is not shown.
	Live func(ctx context.Context) ([]rolemanager.FirewallGuardrail, error)
	// Deliver hands the host a finished offer.
	Deliver func(rolemanager.PermissionOffer)
}

// offerFirewallPermissions starts the permission pass over a copy of an
// admitted prompt. It returns at once: the pass has its own context and
// deadline, so the turn never waits on it and cancelling the turn does not
// cancel it.
func (s *Session) offerFirewallPermissions(prompt string) {
	h := s.fwPerm
	if h == nil || h.Deliver == nil || (h.Enabled != nil && !h.Enabled()) {
		return
	}
	if !rolemanager.MayStatePermission(prompt) {
		return
	}
	cfg, client := s.cfg, s.client
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), firewallPermTimeout)
		defer cancel()
		role := run.NewRoleClassifier(cfg, client, nil)
		var d decisions.Decider
		if cc := cfg.ClassifierOrDefault(); cc.Decisions.On() {
			d = cc.Decisions.NewDecider(client)
		}
		found, backend, err := rolemanager.DetectFirewallPermission(ctx, d, role, prompt)
		if err != nil || !found {
			return
		}
		var live []rolemanager.FirewallGuardrail
		if h.Live != nil {
			if live, err = h.Live(ctx); err != nil {
				return
			}
		}
		offer, err := rolemanager.WriteFirewallPermissions(ctx, role, prompt, live)
		if err != nil {
			return
		}
		offer.Backend = backend
		h.Deliver(offer)
	}()
}
