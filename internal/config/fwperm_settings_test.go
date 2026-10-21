package config

import "testing"

func TestFirewallPermissionDefaults(t *testing.T) {
	var s Settings
	if !s.FirewallPermissionOffersShown() {
		t.Fatal("permission offers should show by default")
	}
	if s.FirewallPermissionAutoApply() {
		t.Fatal("auto-apply should be off by default")
	}
	f, tr := false, true
	s.Firewall = &FirewallSettings{PermissionOffers: &f, PermissionAutoApply: &tr}
	if s.FirewallPermissionOffersShown() || !s.FirewallPermissionAutoApply() {
		t.Fatalf("settings not read: %+v", s.Firewall)
	}
}

// A repository may hide the card or stop auto-apply, never turn auto-apply on.
func TestFirewallPermissionProjectLayerCanOnlyTurnOff(t *testing.T) {
	f, tr := false, true
	dst := FirewallSettings{}
	mergeFirewall(&dst, &FirewallSettings{PermissionAutoApply: &tr, PermissionOffers: &f}, true)
	if dst.PermissionAutoApply != nil {
		t.Fatal("a project layer turned auto-apply on")
	}
	if dst.PermissionOffers == nil || *dst.PermissionOffers {
		t.Fatal("a project layer could not hide the card")
	}
	dst = FirewallSettings{PermissionAutoApply: &tr}
	mergeFirewall(&dst, &FirewallSettings{PermissionAutoApply: &f}, true)
	if dst.PermissionAutoApply == nil || *dst.PermissionAutoApply {
		t.Fatal("a project layer could not turn auto-apply off")
	}
	dst = FirewallSettings{}
	mergeFirewall(&dst, &FirewallSettings{PermissionAutoApply: &tr}, false)
	if dst.PermissionAutoApply == nil || !*dst.PermissionAutoApply {
		t.Fatal("the user's own layer could not turn auto-apply on")
	}
}
