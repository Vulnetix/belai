package components

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/belai/internal/rolemanager"
)

// PermRole is the transcript role of the AI Firewall permission card
// (docs/firewall-permissions.md): the rules belai wrote for permissions a
// prompt stated, offered for the user to apply. The row is Ephemeral: the
// offer and each apply are their own session entries, and buildTurns never
// promotes the card, so no model sees it.
const PermRole = "fwperm"

// Click actions on the card. A rule's own apply action is PermApplyRule
// followed by the rule's key.
const (
	PermApplyAll  = "fwperm-apply-all"
	PermApplyRule = "fwperm-apply:"
	PermDismiss   = "fwperm-dismiss"
)

// Rule states on the card.
const (
	PermOffered  = ""
	PermApplying = "applying"
	PermApplied  = "applied"
	PermFailed   = "failed"
)

// PermCard is the card's state. Offer is the validated offer the role
// manager wrote; States and Errors are keyed by rule key.
type PermCard struct {
	Offer     rolemanager.PermissionOffer
	States    map[string]string
	Errors    map[string]string
	Dismissed bool
	// Auto is set when the rules were applied with no click
	// (firewall.permission_auto_apply).
	Auto bool
}

// State is one rule's state.
func (c *PermCard) State(key string) string {
	if c == nil {
		return PermOffered
	}
	return c.States[key]
}

// Pending lists the rules still waiting for Apply or failed, in offer order.
func (c *PermCard) Pending() []rolemanager.FirewallRule {
	if c == nil {
		return nil
	}
	var out []rolemanager.FirewallRule
	for _, r := range c.Offer.Rules {
		if s := c.States[r.Key]; s == PermOffered || s == PermFailed {
			out = append(out, r)
		}
	}
	return out
}

// Key is what makes two cards draw differently.
func (c *PermCard) Key() string {
	if c == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(c.Offer.ID)
	for _, r := range c.Offer.Rules {
		b.WriteString("|" + r.Key + "=" + c.States[r.Key] + ":" + c.Errors[r.Key])
	}
	if c.Dismissed {
		b.WriteString("|dismissed")
	}
	if c.Auto {
		b.WriteString("|auto")
	}
	return b.String()
}

// permPanel renders the card: one block per rule (its intent, what it does,
// whether this variant is the one switched on, and its state), the CLI
// command it runs, and the actions.
func permPanel(msg Message, width int) (string, LineMap) {
	c := msg.Perm
	if c == nil {
		c = &PermCard{}
	}
	accent := lipgloss.TerminalColor(ColorPolicy)
	var rows []Row

	var head rowBuilder
	head.text(c.Offer.Summary, ColorCream)
	rows = append(rows, head.row())

	applied, failed := 0, 0
	for _, r := range c.Offer.Rules {
		var line rowBuilder
		line.text("  ", nil)
		switch c.States[r.Key] {
		case PermApplied:
			applied++
			line.text("✓ ", ColorTeal)
		case PermFailed:
			failed++
			line.text("✗ ", ColorDanger)
		case PermApplying:
			line.text("… ", ColorMuted)
		default:
			line.text("◆ ", ColorPolicy)
		}
		line.text(r.RuleType, ColorPolicy)
		line.text(" "+r.Action, ColorText)
		if r.Pattern != "" {
			line.text(" "+r.Pattern, ColorCream)
		}
		if r.Variant != "" {
			sw := "off"
			if r.Enabled {
				sw = "on"
			}
			line.text(fmt.Sprintf("  %s variant · %s", r.Variant, sw), ColorMuted)
		}
		if s := c.States[r.Key]; (s == PermOffered || s == PermFailed) && !c.Dismissed {
			line.text("  ", nil)
			line.chip("[ apply ]", PermApplyRule+r.Key, ColorPolicy)
		}
		rows = append(rows, line.row())

		var why rowBuilder
		why.text("    ", nil)
		why.text(r.Intent, ColorMuted)
		if e := c.Errors[r.Key]; e != "" {
			why.text(" · "+e, ColorDanger)
		}
		rows = append(rows, why.row())
	}
	for _, n := range c.Offer.Notes {
		var note rowBuilder
		note.text("  note  ", ColorLow)
		note.text(n, ColorMuted)
		rows = append(rows, note.row())
	}
	if len(c.Offer.Rules) > 0 {
		var cmd rowBuilder
		cmd.text("  runs  ", ColorLow)
		cmd.text("vulnetix "+strings.Join(rolemanager.FirewallGuardrailArgs(c.Offer.Rules[0]), " "), ColorMuted)
		if n := len(c.Offer.Rules) - 1; n > 0 {
			cmd.text(fmt.Sprintf("  (+%d more)", n), ColorLow)
		}
		rows = append(rows, cmd.rowFrom(8))
	}

	var act rowBuilder
	act.text("  ", nil)
	pending := len(c.Pending())
	switch {
	case c.Dismissed:
		act.text("dismissed · nothing was applied", ColorMuted)
	case pending == 0 && c.Auto:
		act.text("applied automatically to your org's AI Firewall", ColorTeal)
	case pending == 0:
		act.text("applied to your org's AI Firewall", ColorTeal)
	default:
		act.chip("[ apply all ]", PermApplyAll, ColorPolicy)
		act.text("  ", nil)
		act.chip("[ dismiss ]", PermDismiss, ColorMuted)
		act.text("  nothing changes until you apply", ColorLow)
	}
	rows = append(rows, act.row())

	meta := fmt.Sprintf("%d rule", len(c.Offer.Rules))
	if len(c.Offer.Rules) != 1 {
		meta += "s"
	}
	switch {
	case failed > 0:
		meta += fmt.Sprintf(" · %d failed", failed)
		accent = ColorDanger
	case applied > 0 && applied < len(c.Offer.Rules):
		meta += fmt.Sprintf(" · %d applied", applied)
	}
	p := Panel{
		Title: "permission change detected", Detail: "AI Firewall", Meta: meta,
		Width: width, Accent: accent, Open: true, BodyRows: rows,
	}
	return p.Render()
}
