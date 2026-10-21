package tui

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/credentials"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/tui/components"
	"github.com/vulnetix/belai/internal/vulnetixcli"
)

// The AI Firewall permission card (docs/firewall-permissions.md). After a
// prompt is admitted, the agent session reads a copy of it for permissions the
// Vulnetix AI Firewall can enforce (agent/fwperm.go). An offer comes back here
// on its own channel, whatever the turn is doing; it is recorded in the
// session for the website at once, and shown as a card when the turn ends, so
// a card never lands inside a streaming reply. The card's Apply runs the
// Vulnetix CLI for each rule; nothing is written to the firewall until the
// user clicks it, or has turned firewall.permission_auto_apply on.
//
// The pass runs only while the Vulnetix CLI is signed in. It runs whether or
// not the firewall is routing this session's traffic: a rule is org policy.

// Session entry types the card writes (docs/session-sync.md).
const (
	fwPermEntryType        = "firewall_permission"
	fwPermAppliedEntryType = "firewall_permission_applied"
)

// fwPermCLITimeout bounds one Vulnetix CLI call the card makes.
const fwPermCLITimeout = 30 * time.Second

// Sign-in states the bridge knows.
const (
	fwAuthUnknown int32 = iota // not probed yet: read the local credentials
	fwAuthYes
	fwAuthNo
)

// fwPermBridge is the part of the card the pass's goroutine touches. It holds
// no App state: the gate is an atomic the render loop sets from the CLI probe,
// and offers cross on a channel.
type fwPermBridge struct {
	workdir string
	auth    atomic.Int32
	offers  chan rolemanager.PermissionOffer
}

func newFwPermBridge(workdir string) *fwPermBridge {
	return &fwPermBridge{workdir: workdir, offers: make(chan rolemanager.PermissionOffer, 16)}
}

// signedIn reports whether the Vulnetix CLI is signed in: the probe's answer
// once it has one, the CLI's local credentials before.
func (b *fwPermBridge) signedIn() bool {
	switch b.auth.Load() {
	case fwAuthYes:
		return true
	case fwAuthNo:
		return false
	}
	h, err := credentials.VulnetixAuthHeader(b.workdir)
	return err == nil && strings.TrimSpace(h) != ""
}

// setSignedIn records the CLI probe's answer.
func (b *fwPermBridge) setSignedIn(on bool) {
	if b == nil {
		return
	}
	if on {
		b.auth.Store(fwAuthYes)
	} else {
		b.auth.Store(fwAuthNo)
	}
}

// hook is what the agent session runs the pass with.
func (b *fwPermBridge) hook() *agent.FirewallPermissionHook {
	if b == nil {
		return nil
	}
	return &agent.FirewallPermissionHook{
		Enabled: b.signedIn,
		Live:    liveGuardrails,
		Deliver: func(o rolemanager.PermissionOffer) {
			select {
			case b.offers <- o:
			default: // a backlog of offers nobody has seen: drop the newest
			}
		},
	}
}

// liveGuardrails reads the org's guardrails through the Vulnetix CLI.
func liveGuardrails(ctx context.Context) ([]rolemanager.FirewallGuardrail, error) {
	cli, err := vulnetixcli.Detect()
	if err != nil {
		return nil, err
	}
	c := *cli
	c.Timeout = fwPermCLITimeout
	gs, err := c.AiFirewallGuardrails(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]rolemanager.FirewallGuardrail, 0, len(gs))
	for _, g := range gs {
		out = append(out, rolemanager.FirewallGuardrail{
			UUID: g.UUID, Name: g.Name, RuleType: g.RuleType, Action: g.Action,
			Pattern: g.Pattern, Enabled: g.Enabled, Priority: g.Priority,
		})
	}
	return out, nil
}

type fwPermOfferMsg rolemanager.PermissionOffer

// fwPermAppliedMsg is one rule's apply outcome.
type fwPermAppliedMsg struct {
	offerID, key, name string
	uuid               string
	enabled            bool
	source             string
	err                error
}

// nextFwPermOffer waits for the next offer.
func (a *App) nextFwPermOffer() tea.Cmd {
	if a.fwPerm == nil {
		return nil
	}
	ch := a.fwPerm.offers
	return func() tea.Msg {
		o, ok := <-ch
		if !ok {
			return nil
		}
		return fwPermOfferMsg(o)
	}
}

// handleFwPermOffer records an offer and shows its card, or holds the card
// until the running turn ends. Auto-apply does not wait for the turn.
func (a *App) handleFwPermOffer(o rolemanager.PermissionOffer) tea.Cmd {
	a.appendEntry(session.Entry{Type: fwPermEntryType, Role: "system", Content: o.Summary, Meta: fwPermOfferMeta(o)})
	card := &components.PermCard{Offer: o, States: map[string]string{}, Errors: map[string]string{}}
	var cmd tea.Cmd
	if a.settings.FirewallPermissionAutoApply() {
		card.Auto = true
		cmd = a.applyFwPerm(card, card.Pending(), "auto")
	}
	if a.working() || a.preSend {
		a.fwPermHeld = append(a.fwPermHeld, card)
	} else {
		a.showFwPermCard(card)
	}
	return cmd
}

// flushFwPermOffers shows the cards held through a turn.
func (a *App) flushFwPermOffers() {
	for _, card := range a.fwPermHeld {
		a.showFwPermCard(card)
	}
	a.fwPermHeld = nil
}

func (a *App) showFwPermCard(card *components.PermCard) {
	a.messages = append(a.messages, components.Message{Role: components.PermRole, Ephemeral: true, Perm: card})
}

// fwPermOfferMeta is the offer as session entry meta: the website's contract
// (docs/session-sync.md).
func fwPermOfferMeta(o rolemanager.PermissionOffer) map[string]any {
	raw, err := json.Marshal(o)
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

// fwPermOfferFromMeta reads an offer back from its entry.
func fwPermOfferFromMeta(meta map[string]any) (rolemanager.PermissionOffer, bool) {
	raw, err := json.Marshal(meta)
	if err != nil {
		return rolemanager.PermissionOffer{}, false
	}
	var o rolemanager.PermissionOffer
	if json.Unmarshal(raw, &o) != nil || o.ID == "" || len(o.Rules) == 0 {
		return rolemanager.PermissionOffer{}, false
	}
	return o, true
}

// fwPermCard finds an offer's card by its id, shown or held.
func (a *App) fwPermCard(offerID string) *components.PermCard {
	for _, c := range a.fwPermHeld {
		if c.Offer.ID == offerID {
			return c
		}
	}
	for i := len(a.messages) - 1; i >= 0; i-- {
		if m := a.messages[i]; m.Role == components.PermRole && m.Perm != nil && m.Perm.Offer.ID == offerID {
			return m.Perm
		}
	}
	return nil
}

// applyFwPerm runs the CLI for each rule. The variants are chosen again
// against the live guardrails at the moment of the click, so a rule that has
// since come into force is not written twice and each family's current mode
// decides which variant is switched on.
func (a *App) applyFwPerm(card *components.PermCard, rules []rolemanager.FirewallRule, source string) tea.Cmd {
	if card == nil || len(rules) == 0 {
		return nil
	}
	for _, r := range rules {
		card.States[r.Key] = components.PermApplying
		delete(card.Errors, r.Key)
	}
	offerID := card.Offer.ID
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return func() tea.Msg {
		cli, err := vulnetixcli.Detect()
		if err != nil {
			return fwPermBatchMsg(failAll(offerID, rules, source, err))
		}
		c := *cli
		c.Timeout = fwPermCLITimeout
		if live, err := liveGuardrails(ctx); err == nil {
			rules = rolemanager.SelectFirewallVariants(rules, live)
			var out []fwPermAppliedMsg
			var todo []rolemanager.FirewallRule
			for _, r := range rules {
				if rolemanager.FirewallEnforced(r, live) {
					out = append(out, fwPermAppliedMsg{offerID: offerID, key: r.Key, name: r.Name, enabled: r.Enabled, source: source})
					continue
				}
				todo = append(todo, r)
			}
			return fwPermBatchMsg(append(out, applyRules(ctx, c, offerID, todo, source)...))
		}
		return fwPermBatchMsg(applyRules(ctx, c, offerID, rules, source))
	}
}

// fwPermBatchMsg is the outcome of one apply, rule by rule.
type fwPermBatchMsg []fwPermAppliedMsg

func applyRules(ctx context.Context, c vulnetixcli.CLI, offerID string, rules []rolemanager.FirewallRule, source string) []fwPermAppliedMsg {
	out := make([]fwPermAppliedMsg, 0, len(rules))
	for _, r := range rules {
		uuid, err := c.ApplyGuardrail(ctx, rolemanager.FirewallGuardrailArgs(r))
		out = append(out, fwPermAppliedMsg{offerID: offerID, key: r.Key, name: r.Name, uuid: uuid, enabled: r.Enabled, source: source, err: err})
	}
	return out
}

func failAll(offerID string, rules []rolemanager.FirewallRule, source string, err error) []fwPermAppliedMsg {
	out := make([]fwPermAppliedMsg, 0, len(rules))
	for _, r := range rules {
		out = append(out, fwPermAppliedMsg{offerID: offerID, key: r.Key, name: r.Name, enabled: r.Enabled, source: source, err: err})
	}
	return out
}

// handleFwPermApplied settles each rule on its card and records the outcome
// for the website.
func (a *App) handleFwPermApplied(batch fwPermBatchMsg) {
	for _, m := range batch {
		if card := a.fwPermCard(m.offerID); card != nil {
			for i := range card.Offer.Rules {
				if card.Offer.Rules[i].Key == m.key {
					card.Offer.Rules[i].Enabled = m.enabled
				}
			}
			if m.err != nil {
				card.States[m.key] = components.PermFailed
				card.Errors[m.key] = mcpClean(m.err.Error(), 120)
			} else {
				card.States[m.key] = components.PermApplied
				delete(card.Errors, m.key)
			}
		}
		meta := map[string]any{
			"offer_id": m.offerID, "rule_key": m.key, "rule_name": m.name,
			"rule_uuid": m.uuid, "enabled": m.enabled, "source": m.source, "error": "",
		}
		content := "applied " + m.name
		if m.err != nil {
			meta["error"] = mcpClean(m.err.Error(), 200)
			content = "could not apply " + m.name
		}
		a.appendEntry(session.Entry{Type: fwPermAppliedEntryType, Role: "system", Content: content, Meta: meta})
	}
}

// permMouse handles a press over a permission card's buttons. It reports
// whether the press was one, so text selection does not start.
func (a *App) permMouse(p components.Pos) (bool, tea.Cmd) {
	if p.Line < 0 || p.Line >= len(a.lastFrame.lines) {
		return false, nil
	}
	line := a.lastFrame.lines[p.Line]
	for _, h := range line.Hits {
		if p.Col < h.Col || p.Col >= h.Col+h.Width || !strings.HasPrefix(h.Action, "fwperm-") {
			continue
		}
		return true, a.permHit(line.Owner, h.Action)
	}
	return false, nil
}

// permHit applies a click on a card.
func (a *App) permHit(owner int, action string) tea.Cmd {
	if !a.messageIs(owner, components.PermRole) || a.messages[owner].Perm == nil {
		return nil
	}
	card := a.messages[owner].Perm
	if card.Dismissed {
		return nil
	}
	switch {
	case action == components.PermDismiss:
		card.Dismissed = true
		return nil
	case action == components.PermApplyAll:
		return a.applyFwPerm(card, card.Pending(), "tui")
	case strings.HasPrefix(action, components.PermApplyRule):
		key := strings.TrimPrefix(action, components.PermApplyRule)
		for _, r := range card.Pending() {
			if r.Key == key {
				return a.applyFwPerm(card, []rolemanager.FirewallRule{r}, "tui")
			}
		}
	}
	return nil
}

// firewallPermToggle cycles a firewall.* row. Both rows are global only: a
// repository must never make Belai write org policy without a click.
func (a *App) firewallPermToggle(key string) error {
	return a.mutateGlobalSetting(func(s *config.Settings) {
		if s.Firewall == nil {
			s.Firewall = &config.FirewallSettings{}
		}
		switch key {
		case "firewall.permission_offers":
			// Shown by default, so the cycle is on (unset) -> off -> on.
			if s.Firewall.PermissionOffers == nil || *s.Firewall.PermissionOffers {
				f := false
				s.Firewall.PermissionOffers = &f
			} else {
				s.Firewall.PermissionOffers = nil
			}
		case "firewall.permission_auto_apply":
			if s.Firewall.PermissionAutoApply != nil && *s.Firewall.PermissionAutoApply {
				s.Firewall.PermissionAutoApply = nil
			} else {
				t := true
				s.Firewall.PermissionAutoApply = &t
			}
		}
	})
}

// firewallPermUnset returns a firewall.* row to its default.
func (a *App) firewallPermUnset(key string) error {
	return a.mutateGlobalSetting(func(s *config.Settings) {
		if s.Firewall == nil {
			return
		}
		switch key {
		case "firewall.permission_offers":
			s.Firewall.PermissionOffers = nil
		case "firewall.permission_auto_apply":
			s.Firewall.PermissionAutoApply = nil
		}
	})
}
