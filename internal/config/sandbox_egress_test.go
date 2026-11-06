package config

import (
	"reflect"
	"testing"
)

func TestNetworkProxyIsAValueAndAnythingElseIsDeny(t *testing.T) {
	for in, want := range map[string]string{"": "allow", "allow": "allow", "proxy": "proxy", "deny": "deny", "Proxy": "deny", "PROXY ": "deny", "open": "deny"} {
		if got := (&SandboxSettings{Network: in}).NetworkOr(); got != want {
			t.Errorf("NetworkOr(%q) = %q, want %q", in, got, want)
		}
	}
	var nilSettings *SandboxSettings
	if nilSettings.NetworkOr() != "allow" || nilSettings.EgressAllowList() != nil {
		t.Fatal("a nil sandbox must default to allow with no hosts")
	}
}

// A project layer moves the network only toward deny: allow to proxy to deny.
func TestProjectLayerOnlyTightensTheNetwork(t *testing.T) {
	cases := []struct{ global, project, want string }{
		{"", "proxy", "proxy"},
		{"", "deny", "deny"},
		{"allow", "proxy", "proxy"},
		{"proxy", "allow", "proxy"},
		{"proxy", "proxy", "proxy"},
		{"proxy", "deny", "deny"},
		{"deny", "proxy", "deny"},
		{"deny", "allow", "deny"},
		{"deny", "", "deny"},
		{"", "allow", "allow"},
		{"proxy", "bogus", "deny"},
		{"deny", "bogus", "deny"},
	}
	for _, c := range cases {
		s := &SandboxSettings{Network: c.global}
		mergeSandbox(s, &SandboxSettings{Network: c.project}, true)
		if got := s.NetworkOr(); got != c.want {
			t.Errorf("global %q project %q: %s, want %s", c.global, c.project, got, c.want)
		}
	}
	// A user layer may move it any way.
	s := &SandboxSettings{Network: "deny"}
	mergeSandbox(s, &SandboxSettings{Network: "proxy"}, false)
	if s.NetworkOr() != "proxy" {
		t.Fatalf("a user layer could not set proxy: %q", s.Network)
	}
	mergeSandbox(s, &SandboxSettings{Network: "allow"}, false)
	if s.NetworkOr() != "allow" {
		t.Fatalf("a user layer could not set allow: %q", s.Network)
	}
}

func TestEgressAllowIsUserOnlyAndValidated(t *testing.T) {
	s := &SandboxSettings{}
	mergeSandbox(s, &SandboxSettings{EgressAllow: []string{"Example.com", "*.cdn.test", "bad entry", "*.com", "127.0.0.1", "example.com"}}, false)
	if want := []string{"example.com", "*.cdn.test"}; !reflect.DeepEqual(s.EgressAllowList(), want) {
		t.Fatalf("user list = %v, want %v", s.EgressAllowList(), want)
	}
	mergeSandbox(s, &SandboxSettings{EgressAllow: []string{"evil.test", "*.evil.test"}}, true)
	if want := []string{"example.com", "*.cdn.test"}; !reflect.DeepEqual(s.EgressAllowList(), want) {
		t.Fatalf("a project layer widened the allowlist: %v", s.EgressAllowList())
	}
	// The project layer's list is dropped even when it is the only one.
	p := &SandboxSettings{}
	mergeSandbox(p, &SandboxSettings{Network: "proxy", EgressAllow: []string{"evil.test"}}, true)
	if len(p.EgressAllowList()) != 0 || p.NetworkOr() != "proxy" {
		t.Fatalf("project-only: %+v", p)
	}
}

func TestResolveDropsAProjectEgressAllowAndNeverLoosensTheNetwork(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	resolve := func(global, project *SandboxSettings) *SandboxSettings {
		t.Helper()
		workdir := t.TempDir()
		if err := SaveGlobal(Settings{Sandbox: global}); err != nil {
			t.Fatal(err)
		}
		if err := SaveProject(workdir, Settings{Sandbox: project}); err != nil {
			t.Fatal(err)
		}
		eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
		if err != nil {
			t.Fatal(err)
		}
		return eff.Settings.Sandbox
	}
	got := resolve(&SandboxSettings{Network: "proxy", EgressAllow: []string{"registry.npmjs.org"}},
		&SandboxSettings{Network: "allow", EgressAllow: []string{"evil.test"}})
	if got.NetworkOr() != "proxy" || !reflect.DeepEqual(got.EgressAllowList(), []string{"registry.npmjs.org"}) {
		t.Fatalf("project loosened the network or widened the list: %+v", got)
	}
	got = resolve(&SandboxSettings{EgressAllow: []string{"registry.npmjs.org"}}, &SandboxSettings{Network: "proxy"})
	if got.NetworkOr() != "proxy" || len(got.EgressAllowList()) != 1 {
		t.Fatalf("project could not tighten to proxy: %+v", got)
	}
	// Override (the project preference path) drops the list as well.
	merged := (Settings{Sandbox: &SandboxSettings{Network: "proxy"}}).Override(Settings{Sandbox: &SandboxSettings{Network: "allow", EgressAllow: []string{"evil.test"}}})
	if merged.Sandbox.NetworkOr() != "proxy" || len(merged.Sandbox.EgressAllowList()) != 0 {
		t.Fatalf("Override: %+v", merged.Sandbox)
	}
}
