package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/tui/components"
)

// fakeVulnetix puts a vulnetix script on PATH that answers `ai-firewall get`
// with live and every guardrail write with a uuid, and logs its arguments.
func fakeVulnetix(t *testing.T, live string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := `#!/bin/sh
echo "$*" >> ` + log + `
case "$*" in
  *"ai-firewall get"*) echo '` + live + `' ;;
  *) echo '{"guardrail":{"uuid":"g-1"}}' ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "vulnetix"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func testOffer() rolemanager.PermissionOffer {
	return rolemanager.PermissionOffer{
		ID: "fwp-test", Summary: "Keep Bash out",
		Rules: []rolemanager.FirewallRule{
			{Key: "r1", Name: "belai-deny-bash", RuleType: "tool_deny", Action: "strip", Pattern: "Bash", Priority: 100, Enabled: true, Family: "tool", Variant: "deny", Group: "g1", Stance: rolemanager.StanceRestrict, Intent: "never use Bash"},
			{Key: "r2", Name: "belai-pii", RuleType: "pii_redact", Action: "redact", Priority: 100, Enabled: true, Family: "content", Group: "g2", Intent: "mask emails"},
		},
	}
}

func permRows(a *App) []components.Message {
	var out []components.Message
	for _, m := range a.messages {
		if m.Role == components.PermRole {
			out = append(out, m)
		}
	}
	return out
}

func newPermApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	if a.store == nil {
		t.Skip("no session store")
	}
	return a
}

func TestFwPermOfferIsRecordedAndShownAsARenderOnlyCard(t *testing.T) {
	a := newPermApp(t)
	if cmd := a.handleFwPermOffer(testOffer()); cmd != nil {
		t.Fatal("an offer did something before a click")
	}
	rows := permRows(a)
	if len(rows) != 1 || !rows[0].Ephemeral || !neverPersisted(rows[0]) {
		t.Fatalf("rows = %+v", rows)
	}
	for _, turn := range a.buildTurns() {
		if strings.Contains(turn.Content, "Bash") {
			t.Fatalf("the card reached the model transcript: %+v", turn)
		}
	}
	got := entriesOfType(t, a, fwPermEntryType)
	if len(got) != 1 {
		t.Fatalf("%d offer entries", len(got))
	}
	o, ok := fwPermOfferFromMeta(got[0].Meta)
	if !ok || o.ID != "fwp-test" || len(o.Rules) != 2 || o.Rules[0].Variant != "deny" {
		t.Fatalf("offer meta did not round-trip: %+v", got[0].Meta)
	}
}

func TestFwPermOfferWaitsForTheTurn(t *testing.T) {
	a := newPermApp(t)
	a.cancel = func() {}
	a.handleFwPermOffer(testOffer())
	if len(permRows(a)) != 0 || len(a.fwPermHeld) != 1 {
		t.Fatal("a card landed inside a running turn")
	}
	if len(entriesOfType(t, a, fwPermEntryType)) != 1 {
		t.Fatal("the offer was not recorded at once")
	}
	a.cancel = nil
	a.flushFwPermOffers()
	if len(permRows(a)) != 1 || len(a.fwPermHeld) != 0 {
		t.Fatal("the held card was not shown at the turn's end")
	}
}

func TestFwPermApplyRunsTheCLIAndRecordsEachRule(t *testing.T) {
	log := fakeVulnetix(t, `{"guardrails":[]}`)
	a := newPermApp(t)
	a.handleFwPermOffer(testOffer())
	owner := len(a.messages) - 1
	cmd := a.permHit(owner, components.PermApplyAll)
	if cmd == nil {
		t.Fatal("apply all did nothing")
	}
	card := a.messages[owner].Perm
	if card.State("r1") != components.PermApplying {
		t.Fatalf("state = %q", card.State("r1"))
	}
	batch, ok := cmd().(fwPermBatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("batch = %#v", batch)
	}
	a.handleFwPermApplied(batch)
	if card.State("r1") != components.PermApplied || card.State("r2") != components.PermApplied || len(card.Pending()) != 0 {
		t.Fatalf("states = %+v errors = %+v", card.States, card.Errors)
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "ai-firewall policy guardrail belai-deny-bash --rule-type tool_deny --action strip --pattern=Bash --priority 100 --enable -o json") {
		t.Fatalf("calls = %s", calls)
	}
	applied := entriesOfType(t, a, fwPermAppliedEntryType)
	if len(applied) != 2 || applied[0].Meta["rule_uuid"] != "g-1" || applied[0].Meta["source"] != "tui" {
		t.Fatalf("applied entries = %+v", applied)
	}
	if cmd := a.permHit(owner, components.PermApplyAll); cmd != nil {
		t.Fatal("apply all ran again with nothing pending")
	}
}

func TestFwPermApplySkipsARuleAlreadyLive(t *testing.T) {
	log := fakeVulnetix(t, `{"guardrails":[{"uuid":"u","ruleType":"tool_deny","action":"strip","pattern":"Bash","enabled":true}]}`)
	a := newPermApp(t)
	a.handleFwPermOffer(testOffer())
	owner := len(a.messages) - 1
	batch := a.permHit(owner, components.PermApplyRule+"r1")().(fwPermBatchMsg)
	a.handleFwPermApplied(batch)
	if a.messages[owner].Perm.State("r1") != components.PermApplied {
		t.Fatal("a live rule was not marked applied")
	}
	calls, _ := os.ReadFile(log)
	if strings.Contains(string(calls), "policy guardrail") {
		t.Fatalf("a rule already in force was written again: %s", calls)
	}
}

func TestFwPermAutoApply(t *testing.T) {
	fakeVulnetix(t, `{"guardrails":[]}`)
	a := newPermApp(t)
	tr := true
	a.settings.Firewall = &config.FirewallSettings{PermissionAutoApply: &tr}
	cmd := a.handleFwPermOffer(testOffer())
	if cmd == nil {
		t.Fatal("auto-apply did not apply")
	}
	a.handleFwPermApplied(cmd().(fwPermBatchMsg))
	card := permRows(a)[0].Perm
	if !card.Auto || len(card.Pending()) != 0 {
		t.Fatalf("card = %+v", card)
	}
	if e := entriesOfType(t, a, fwPermAppliedEntryType); len(e) != 2 || e[0].Meta["source"] != "auto" {
		t.Fatalf("applied entries = %+v", e)
	}
}

func TestFwPermDismissWritesNothing(t *testing.T) {
	a := newPermApp(t)
	a.handleFwPermOffer(testOffer())
	owner := len(a.messages) - 1
	if cmd := a.permHit(owner, components.PermDismiss); cmd != nil {
		t.Fatal("dismiss did something")
	}
	if cmd := a.permHit(owner, components.PermApplyAll); cmd != nil {
		t.Fatal("a dismissed card applied")
	}
	if len(entriesOfType(t, a, fwPermAppliedEntryType)) != 0 {
		t.Fatal("dismiss recorded an apply")
	}
}

func TestFwPermHiddenCardsStayRecorded(t *testing.T) {
	a := newPermApp(t)
	f := false
	a.settings.Firewall = &config.FirewallSettings{PermissionOffers: &f}
	a.addSystem("before")
	a.handleFwPermOffer(testOffer())
	a.addSystem("after")
	shown, idx := a.filteredMessagesIdx()
	for i, m := range shown {
		if m.Role == components.PermRole {
			t.Fatal("a hidden card was rendered")
		}
		if a.messages[idx[i]].Content != m.Content {
			t.Fatalf("row %d maps to the wrong message", i)
		}
	}
	if len(permRows(a)) != 1 || len(entriesOfType(t, a, fwPermEntryType)) != 1 {
		t.Fatal("hiding the card dropped the offer")
	}
}

func TestFwPermCardRehydratesWithItsApplies(t *testing.T) {
	o := testOffer()
	entries := []session.Entry{
		{Type: fwPermEntryType, Role: "system", Content: o.Summary, Meta: fwPermOfferMeta(o)},
		{Type: fwPermAppliedEntryType, Role: "system", Meta: map[string]any{"offer_id": o.ID, "rule_key": "r1", "source": "tui", "error": ""}},
		{Type: fwPermAppliedEntryType, Role: "system", Meta: map[string]any{"offer_id": o.ID, "rule_key": "r2", "source": "tui", "error": "AI Firewall guardrails need the Teams plan"}},
	}
	msgs, _ := messagesFromEntries(entries)
	if len(msgs) != 1 || msgs[0].Role != components.PermRole || !msgs[0].Ephemeral {
		t.Fatalf("msgs = %+v", msgs)
	}
	c := msgs[0].Perm
	if c.State("r1") != components.PermApplied || c.State("r2") != components.PermFailed || len(c.Pending()) != 1 {
		t.Fatalf("card = %+v", c)
	}
}

func TestFwPermGateFollowsTheProbe(t *testing.T) {
	b := newFwPermBridge(t.TempDir())
	b.setSignedIn(false)
	if b.signedIn() || b.hook().Enabled() {
		t.Fatal("the pass ran while the CLI is signed out")
	}
	b.setSignedIn(true)
	if !b.hook().Enabled() {
		t.Fatal("the pass did not run while signed in")
	}
}

func TestFirewallPermissionSettingsRows(t *testing.T) {
	a := newPermApp(t)
	for _, key := range []string{"firewall.permission_offers", "firewall.permission_auto_apply"} {
		row, _ := settingsRowByKey(a, key)
		if row.kind != "toggle" || settingsGroupOf(key) != "firewall" || !settingsWritesGlobalOnly(key) {
			t.Fatalf("%s: kind %q group %q", key, row.kind, settingsGroupOf(key))
		}
	}
	if row, _ := settingsRowByKey(a, "firewall.permission_auto_apply"); row.value != boolLabel(false) {
		t.Fatalf("auto-apply defaults to %q", row.value)
	}
	if err := a.cycleToggle("firewall.permission_auto_apply"); err != nil {
		t.Fatal(err)
	}
	if !a.settings.FirewallPermissionAutoApply() {
		t.Fatal("the toggle did not turn auto-apply on")
	}
	if err := a.cycleToggle("firewall.permission_offers"); err != nil {
		t.Fatal(err)
	}
	if a.settings.FirewallPermissionOffersShown() {
		t.Fatal("the toggle did not hide the card")
	}
	if err := a.unsetSetting("firewall.permission_offers"); err != nil {
		t.Fatal(err)
	}
	if !a.settings.FirewallPermissionOffersShown() {
		t.Fatal("unset did not restore the default")
	}
}
