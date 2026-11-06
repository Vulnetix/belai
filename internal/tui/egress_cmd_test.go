package tui

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/egress"
	"github.com/vulnetix/belai/internal/sandbox"
)

func withEgressRegistry(t *testing.T) *egress.Registry {
	t.Helper()
	reg := egress.NewRegistry()
	old := egress.Default
	egress.Default = reg
	t.Cleanup(func() { egress.Default = old })
	return reg
}

func TestEgressCommandAllowsAndRevokesForTheSession(t *testing.T) {
	reg := withEgressRegistry(t)
	a := New(Options{})
	a.settings.Sandbox = &config.SandboxSettings{Network: "proxy", EgressAllow: []string{"registry.npmjs.org"}}

	a.egressCommand("allow Example.COM")
	if !strings.Contains(lastSystem(a), "allowed example.com for this session") || strings.Contains(lastSystem(a), "not proxy") {
		t.Fatalf("allow: %q", lastSystem(a))
	}
	if got := reg.SessionAllowed(); len(got) != 1 || got[0] != "example.com" {
		t.Fatalf("session list = %v", got)
	}
	a.egressCommand("")
	for _, want := range []string{"sandbox.network is proxy", "registry.npmjs.org", "example.com"} {
		if !strings.Contains(lastSystem(a), want) {
			t.Errorf("report lacks %q:\n%s", want, lastSystem(a))
		}
	}
	a.egressCommand("revoke example.com")
	if !strings.Contains(lastSystem(a), "revoked") || len(reg.SessionAllowed()) != 0 {
		t.Fatalf("revoke: %q %v", lastSystem(a), reg.SessionAllowed())
	}
	a.egressCommand("revoke registry.npmjs.org")
	if !strings.Contains(lastSystem(a), "is not a session entry") {
		t.Fatalf("a settings entry cannot be revoked here: %q", lastSystem(a))
	}
}

func TestEgressCommandRefusesBadEntriesAndUsage(t *testing.T) {
	reg := withEgressRegistry(t)
	a := New(Options{})
	for _, arg := range []string{"allow *", "allow *.com", "allow 10.0.0.1", "allow localhost", "allow https://x.test", "allow", "allow a b", "frob"} {
		a.egressCommand(arg)
		if len(reg.SessionAllowed()) != 0 {
			t.Fatalf("%q added %v", arg, reg.SessionAllowed())
		}
		if got := lastSystem(a); !strings.Contains(got, "not allowed") && !strings.Contains(got, "usage:") {
			t.Errorf("%q: %q", arg, got)
		}
	}
	a.settings.Sandbox = nil
	a.egressCommand("allow ok.test")
	if !strings.Contains(lastSystem(a), "sandbox.network is not proxy") {
		t.Fatalf("a hint that the list is unused: %q", lastSystem(a))
	}
}

func TestEgressReportCleansHostsAndShowsRefusals(t *testing.T) {
	reg := egress.NewRegistry()
	reg.Deny("evil\x1b[31m.test\nINJECT", 443, egress.ReasonNotAllowed)
	reg.Deny("", 0, egress.ReasonInvalid)
	out := egressReport(&config.SandboxSettings{Network: "proxy"}, reg)
	if strings.ContainsAny(out, "\x1b") || strings.Contains(out, "\nINJECT") {
		t.Fatalf("a host reached the screen uncleaned: %q", out)
	}
	for _, want := range []string{"refused: 2 connection(s)", "not_allowed", "(no host)", "/egress allow HOST"} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
	if out := egressReport(nil, egress.NewRegistry()); !strings.Contains(out, "sandbox.network is allow") || !strings.Contains(out, "used only when it is proxy") {
		t.Fatalf("default report: %s", out)
	}
}

func TestSandboxReportNamesTheProxy(t *testing.T) {
	if name, _ := sandbox.Backend(); name == "" {
		t.Skip("no sandbox backend here")
	}
	a := New(Options{})
	a.workdir = t.TempDir()
	a.settings.Sandbox = &config.SandboxSettings{Mode: "required", Network: "proxy", EgressAllow: []string{"a.test", "b.test"}}
	got := a.sandboxReport()
	if sandbox.NetworkDenyEnforced() {
		if !strings.Contains(got, "proxy, 2 allowed host(s)") || !strings.Contains(got, "/egress") {
			t.Fatalf("report: %s", got)
		}
	} else if !strings.Contains(got, "proxy requested, unavailable here") {
		t.Fatalf("report: %s", got)
	}
}
