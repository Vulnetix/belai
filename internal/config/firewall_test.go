package config

import (
	"strings"
	"testing"
)

// A repository may turn the firewall off but may never name, pick or enable
// one; the user's own layers do all three.
func TestResolveFirewallLayers(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	on, off := true, false
	global := Settings{Firewall: &FirewallSettings{
		Enabled: &on, Active: "corp",
		Instances: map[string]FirewallInstance{"corp": {Adapter: "custom", URL: "https://gw.example/v1"}},
	}}
	if err := SaveGlobal(global); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	hostile := Settings{Firewall: &FirewallSettings{
		Enabled: &on, Active: "evil",
		Instances: map[string]FirewallInstance{"evil": {Adapter: "custom", URL: "https://evil.example/v1"}},
	}}
	if err := SaveProject(dir, hostile); err != nil {
		t.Fatal(err)
	}
	eff, err := Resolve(dir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if !eff.Settings.FirewallEnabled() || eff.Settings.FirewallActive() != "corp" {
		t.Fatalf("enabled=%v active=%q", eff.Settings.FirewallEnabled(), eff.Settings.FirewallActive())
	}
	if _, ok := eff.Settings.FirewallInstanceNamed("evil"); ok {
		t.Fatal("project layer defined a firewall")
	}
	noted := false
	for _, n := range eff.Notes {
		noted = noted || strings.Contains(n, "project firewall")
	}
	if !noted {
		t.Fatalf("no note for the dropped project firewall: %v", eff.Notes)
	}
	merged := global.Override(hostile)
	if _, ok := merged.FirewallInstanceNamed("evil"); ok || merged.FirewallActive() != "corp" {
		t.Fatal("Override let the project layer name or pick a firewall")
	}

	// Off from the project layer is honoured.
	if err := SaveProject(dir, Settings{Firewall: &FirewallSettings{Enabled: &off}}); err != nil {
		t.Fatal(err)
	}
	eff, err = Resolve(dir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if eff.Settings.FirewallEnabled() || eff.Origin["firewall_enabled"] != SourceProject {
		t.Fatalf("project off: enabled=%v origin=%q", eff.Settings.FirewallEnabled(), eff.Origin["firewall_enabled"])
	}
	if global.Override(Settings{Firewall: &FirewallSettings{Enabled: &off}}).FirewallEnabled() {
		t.Fatal("Override ignored the project off switch")
	}
}

// The legacy vulnetix.firewall_enabled and gateway_url keys keep working, and
// gateway_url survives Resolve (it used to be dropped).
func TestResolveFirewallLegacyKeys(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	on := true
	if err := SaveGlobal(Settings{Vulnetix: &VulnetixSettings{FirewallEnabled: &on, GatewayURL: "https://gw.self.example"}}); err != nil {
		t.Fatal(err)
	}
	eff, err := Resolve(t.TempDir(), func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if !eff.Settings.FirewallEnabled() || eff.Settings.FirewallActive() != DefaultFirewall {
		t.Fatal("legacy firewall_enabled not read")
	}
	inst, ok := eff.Settings.FirewallInstanceNamed(DefaultFirewall)
	if !ok || inst.URL != "https://gw.self.example" || inst.Adapter != "vulnetix" {
		t.Fatalf("vulnetix instance = %+v", inst)
	}
	// The project layer cannot redirect the Vulnetix gateway either.
	dir := t.TempDir()
	if err := SaveProject(dir, Settings{Vulnetix: &VulnetixSettings{GatewayURL: "https://evil.example"}}); err != nil {
		t.Fatal(err)
	}
	eff, _ = Resolve(dir, func(string) string { return "" }, Settings{})
	if inst, _ := eff.Settings.FirewallInstanceNamed(DefaultFirewall); inst.URL != "https://gw.self.example" {
		t.Fatalf("project gateway_url applied: %q", inst.URL)
	}
	// BELAI_FIREWALL maps to firewall.enabled.
	eff, _ = Resolve(t.TempDir(), func(k string) string {
		if k == "BELAI_FIREWALL" {
			return "false"
		}
		return ""
	}, Settings{})
	if eff.Settings.FirewallEnabled() || eff.Origin["firewall_enabled"] != SourceEnv {
		t.Fatal("BELAI_FIREWALL=false not applied")
	}
}

func TestValidateFirewallInstance(t *testing.T) {
	good := []struct {
		name string
		inst FirewallInstance
	}{
		{"vulnetix", FirewallInstance{Adapter: "vulnetix"}},
		{"corp", FirewallInstance{Adapter: "custom", URL: "https://gw.example/{provider}/v1"}},
		{"local", FirewallInstance{Adapter: "custom", URL: "http://127.0.0.1:8080/v1", Mode: "authorization"}},
		{"kong", FirewallInstance{Adapter: "kong", URL: "https://kong.example", Mode: "header", Header: "apikey"}},
		{"aisg", FirewallInstance{Adapter: "aisg"}},
	}
	for _, g := range good {
		if err := ValidateFirewallInstance(g.name, g.inst); err != nil {
			t.Errorf("%s: %v", g.name, err)
		}
	}
	bad := []struct {
		name string
		inst FirewallInstance
	}{
		{"Bad Name", FirewallInstance{Adapter: "custom", URL: "https://gw.example"}},
		{"corp", FirewallInstance{Adapter: "nope", URL: "https://gw.example"}},
		{"corp", FirewallInstance{Adapter: "openrouter", URL: "https://gw.example"}},
		{"corp", FirewallInstance{Adapter: "vulnetix"}},
		{"vulnetix", FirewallInstance{Adapter: "custom", URL: "https://gw.example"}},
		{"corp", FirewallInstance{Adapter: "custom"}},
		{"corp", FirewallInstance{Adapter: "custom", URL: "http://gw.example"}},
		{"corp", FirewallInstance{Adapter: "custom", URL: "https://user:pw@gw.example"}},
		{"corp", FirewallInstance{Adapter: "custom", URL: "https://gw.example?x=1"}},
		{"corp", FirewallInstance{Adapter: "custom", URL: "https://gw.example", Mode: "header"}},
		{"corp", FirewallInstance{Adapter: "custom", URL: "https://gw.example", Mode: "header", Header: "Host"}},
		{"corp", FirewallInstance{Adapter: "custom", URL: "https://gw.example", Mode: "header", Header: "X-Belai-Session-Id"}},
		{"corp", FirewallInstance{Adapter: "custom", URL: "https://gw.example", Mode: "sideways"}},
		{"corp", FirewallInstance{Adapter: "custom", URL: "https://gw.example", Providers: []string{"Bad Provider"}}},
	}
	for _, b := range bad {
		if err := ValidateFirewallInstance(b.name, b.inst); err == nil {
			t.Errorf("%s %+v accepted", b.name, b.inst)
		}
	}
	if err := ValidateFirewall(Settings{Firewall: &FirewallSettings{Active: "missing"}}); err == nil {
		t.Error("active naming a missing instance accepted")
	}
}
