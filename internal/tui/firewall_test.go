package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/credentials"
	"github.com/vulnetix/belai/internal/firewall"
	"github.com/vulnetix/belai/internal/tui/components"
)

// A verdict becomes one render-only report card; a retry of the same refused
// request folds into it, and gateway text cannot restyle the card.
func TestFirewallCardCoalescesAndQuotes(t *testing.T) {
	_, wd := isolate(t)
	a := New(Options{Workdir: wd})
	before := len(a.messages)
	v := firewall.Verdict{
		Firewall: "Vulnetix AI Firewall", Action: firewall.ActionBlock, Code: "request_blocked",
		Rules: []string{"No `secrets` **now**"}, RequestID: "req-1", Provider: "openai", Model: "gpt-5",
		Hint: "a guardrail refused this request",
	}.Clean()
	a.addFirewallCard(v)
	a.addFirewallCard(v)
	if got := len(a.messages) - before; got != 1 {
		t.Fatalf("cards = %d, want 1 (retries fold)", got)
	}
	m := a.messages[len(a.messages)-1]
	if m.Role != components.ReportRole || m.Status != components.ReportFailed || !strings.Contains(m.ToolName, "blocked") {
		t.Fatalf("card = %+v", m)
	}
	if !strings.Contains(m.Content, "`No 'secrets' **now**`") || !strings.Contains(m.Content, "`req-1`") {
		t.Fatalf("third-party text not in code spans:\n%s", m.Content)
	}
	if a.fwEventCount != 1 {
		t.Fatalf("event count = %d", a.fwEventCount)
	}
	for _, turn := range a.buildTurns() {
		if strings.Contains(turn.Content, "req-1") {
			t.Fatal("a firewall card reached a model turn")
		}
	}

	v.RequestID = "req-2"
	v.Action = firewall.ActionRedact
	a.addFirewallCard(v)
	if last := a.messages[len(a.messages)-1]; last.Status != components.ReportAttention {
		t.Fatalf("redaction status = %q", last.Status)
	}
}

// The screen adds a custom firewall to the global settings, and space makes
// it active and routes the current provider through it.
func TestFirewallScreenAddsAndActivatesCustom(t *testing.T) {
	_, wd := isolate(t)
	t.Setenv("OPENAI_API_KEY", "sk-provider")
	t.Setenv("BELAI_FIREWALL_CORP_API_KEY", "fw-key")
	t.Setenv("BELAI_PROVIDER", "openai")
	r, err := credentials.NewResolver(wd)
	if err != nil {
		t.Fatal(err)
	}
	a := New(Options{Workdir: wd, Resolver: r})
	a.cfg.Provider = "openai"
	a.push(viewFirewall)

	rows := a.firewallRows()
	for i, row := range rows {
		if row.kind == "add" {
			a.firewallState.selected = i
		}
	}
	a.handleFirewallKey(tea.KeyMsg{Type: tea.KeyEnter})
	if a.firewallState.mode != "form" {
		t.Fatalf("mode = %q, want form", a.firewallState.mode)
	}
	set := func(key, val string) {
		for i := range a.firewallState.form.fields {
			if a.firewallState.form.fields[i].key == key {
				a.firewallState.form.fields[i].value = val
			}
		}
	}
	set("name", "corp")
	set("url", "https://gw.example/v1")
	set("mode", config.FirewallModeHeader)
	set("header", "X-Gw-Key")
	a.handleFirewallKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	if a.firewallState.errorMsg != "" {
		t.Fatalf("commit: %s", a.firewallState.errorMsg)
	}
	inst, ok := a.settings.FirewallInstanceNamed("corp")
	if !ok || inst.Adapter != "custom" || inst.Header != "X-Gw-Key" {
		t.Fatalf("instance = %+v ok=%v", inst, ok)
	}

	for i, row := range a.firewallRows() {
		if row.kind == "instance" && row.instance == "corp" {
			a.firewallState.selected = i
		}
	}
	a.handleFirewallKey(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")})
	if a.settings.FirewallActive() != "corp" || !a.firewallEnabled() {
		t.Fatalf("active=%q enabled=%v", a.settings.FirewallActive(), a.firewallEnabled())
	}
	if a.cfg.Firewall == nil || a.cfg.BaseURL != "https://gw.example/v1" || a.cfg.Firewall.KeyValue != "fw-key" {
		t.Fatalf("cfg = %v route = %v", a.cfg, a.cfg.Firewall)
	}
	if !strings.Contains(a.footer.FirewallLabel, "corp") {
		t.Fatalf("footer = %q", a.footer.FirewallLabel)
	}

	// A project-layer file cannot pick another firewall.
	if err := config.SaveProject(wd, config.Settings{Firewall: &config.FirewallSettings{Active: "vulnetix"}}); err != nil {
		t.Fatal(err)
	}
	_ = a.reloadSettings()
	if a.settings.FirewallActive() != "corp" {
		t.Fatal("project layer changed the active firewall")
	}
}

// /vulnetix firewall runs on the UI loop and configures the Vulnetix adapter
// through the generic path: it makes vulnetix active.
func TestVulnetixFirewallCommandActivatesVulnetix(t *testing.T) {
	home, wd := isolate(t)
	writeVulnetixCred(t, home)
	t.Setenv("OPENAI_API_KEY", "sk-provider")
	if err := config.Mutate(config.ScopeGlobal, wd, func(s *config.Settings) error {
		s.Firewall = &config.FirewallSettings{Active: "corp", Instances: map[string]config.FirewallInstance{
			"corp": {Adapter: "custom", URL: "https://gw.example/v1"},
		}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r, err := credentials.NewResolver(wd)
	if err != nil {
		t.Fatal(err)
	}
	a := New(Options{Workdir: wd, Resolver: r})
	a.cfg.Provider = "openai"
	a.handleCommand("/vulnetix firewall")
	if a.settings.FirewallActive() != config.DefaultFirewall {
		t.Fatalf("active = %q, want vulnetix", a.settings.FirewallActive())
	}
	if !a.firewallEnabled() {
		t.Fatal("firewall not on")
	}
	if a.cfg.Firewall == nil || !strings.Contains(a.cfg.BaseURL, "guardrails.vulnetix.com/openai/") {
		t.Fatalf("cfg = %v", a.cfg)
	}
}
