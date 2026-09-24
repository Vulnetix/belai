package config

import "testing"

func TestVulnetixGatewayURLOrDefault(t *testing.T) {
	if got := (VulnetixSettings{}).GatewayURLOrDefault(); got != "https://guardrails.vulnetix.com" {
		t.Fatalf("default gateway = %q", got)
	}
	if got := (VulnetixSettings{GatewayURL: "https://self.example.com"}).GatewayURLOrDefault(); got != "https://self.example.com" {
		t.Fatalf("configured gateway = %q", got)
	}
}

func TestUpdateCheckEnabled(t *testing.T) {
	if !(Settings{}).UpdateCheckEnabled() {
		t.Fatal("unset update_check must default on")
	}
	if !(Settings{UpdateCheck: boolPtr(true)}).UpdateCheckEnabled() {
		t.Fatal("explicit true must be on")
	}
	if (Settings{UpdateCheck: boolPtr(false)}).UpdateCheckEnabled() {
		t.Fatal("explicit false must be off")
	}
}

func TestSweepEnabledAndRoots(t *testing.T) {
	if !(Settings{}).SweepEnabled() {
		t.Fatal("unset sweep must default on")
	}
	if !(Settings{VulnetixSweepEnabled: boolPtr(true)}).SweepEnabled() {
		t.Fatal("explicit true must be on")
	}
	if (Settings{VulnetixSweepEnabled: boolPtr(false)}).SweepEnabled() {
		t.Fatal("explicit false must be off")
	}
	if got := (Settings{}).SweepRoots(); got != nil {
		t.Fatalf("unset sweep roots = %v, want nil", got)
	}
	got := (Settings{VulnetixSweepRoots: []string{"/a", "/b"}}).SweepRoots()
	if len(got) != 2 || got[0] != "/a" || got[1] != "/b" {
		t.Fatalf("sweep roots = %v", got)
	}
}

func TestMaxProcessRecoveriesOr(t *testing.T) {
	var nilR *ResilienceSettings
	if got := nilR.MaxProcessRecoveriesOr(3); got != 3 {
		t.Fatalf("nil = %d, want 3", got)
	}
	if got := (&ResilienceSettings{}).MaxProcessRecoveriesOr(3); got != 3 {
		t.Fatalf("unset = %d, want 3", got)
	}
	if got := (&ResilienceSettings{MaxProcessRecoveries: 1}).MaxProcessRecoveriesOr(3); got != 1 {
		t.Fatalf("set = %d, want 1", got)
	}
}

func TestClassifierPhaseSettingsIsZeroNilReceiver(t *testing.T) {
	// resolve_test.go's TestClassifierSettingsIsZero exercises the non-nil
	// receivers; the nil receiver branch is pinned here.
	var nilP *ClassifierPhaseSettings
	if !nilP.IsZero() {
		t.Fatal("nil phase must be zero")
	}
	if !(&ClassifierPhaseSettings{}).IsZero() {
		t.Fatal("empty phase must be zero")
	}
}
