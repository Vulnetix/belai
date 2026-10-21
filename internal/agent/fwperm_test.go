package agent

import (
	"context"
	"testing"

	"github.com/vulnetix/belai/internal/rolemanager"
)

// The pass never asks a model or reads the firewall while the host's gate is
// shut, and a prompt that states no permission costs nothing.
func TestFirewallPermissionPassGates(t *testing.T) {
	calls := 0
	h := &FirewallPermissionHook{
		Enabled: func() bool { return false },
		Live: func(context.Context) ([]rolemanager.FirewallGuardrail, error) {
			calls++
			return nil, nil
		},
		Deliver: func(rolemanager.PermissionOffer) { calls++ },
	}
	s := &Session{fwPerm: h}
	s.offerFirewallPermissions("never use Bash")
	gateReads := 0
	h.Enabled = func() bool { gateReads++; return true }
	s.offerFirewallPermissions("add a parser test")
	if calls != 0 {
		t.Fatalf("the pass ran %d times with the gate shut or no permission words", calls)
	}
	if gateReads != 1 {
		t.Fatalf("gate read %d times", gateReads)
	}
	(&Session{}).offerFirewallPermissions("never use Bash") // no hook: off
}
