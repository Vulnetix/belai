package components

import "github.com/charmbracelet/lipgloss"

// VulnRole is the transcript role of the vulnerability row: the card the TUI
// adds once per identifier when a tool result or a reply names one. It is
// render-only, like the read-aloud card: it is Ephemeral, never produces a
// session entry, and buildTurns never promotes it, so no model, sync, telemetry
// or notification sees it.
const VulnRole = "vuln"

// Click actions a vulnerability row's hit regions carry. The identifier is not
// in the action: the TUI reads it from the clicked message's VulnCard.
const (
	VulnOpen   = "vuln-open"
	VulnTriage = "vuln-triage"
)

// VulnCard is everything the row draws. Every field is composed by the harness
// from one identifier vulnid.Valid accepted (vulnid.URL, vulnid.Hint); none of
// it is text that came from a tool result or a reply.
type VulnCard struct {
	ID   string // the canonical identifier
	URL  string // the console page, https
	Hint string // the vdb hint line
	// Launched is set once the triage agent has been started from this row.
	Launched bool
}

// Key is what makes two cards draw differently.
func (c *VulnCard) Key() string {
	if c == nil {
		return ""
	}
	k := c.ID + "|" + c.URL + "|" + c.Hint
	if c.Launched {
		k += "|launched"
	}
	return k
}

// vulnPanel renders the row: the console link, the vdb hint and the launch
// action. The hint and the label are interface, not text, so nothing in it is
// copied; the link is the one stretch a click opens.
func vulnPanel(msg Message, width int) (string, LineMap) {
	c := msg.Vuln
	if c == nil {
		c = &VulnCard{}
	}
	var link rowBuilder
	link.text("console  ", ColorLow)
	link.chip(c.URL, VulnOpen, ColorTeal)

	var hint rowBuilder
	hint.text("vdb      ", ColorLow)
	hint.text(c.Hint, ColorMuted)

	var act rowBuilder
	act.text("         ", nil)
	if c.Launched {
		act.text("belai:triage started", ColorMuted)
	} else {
		act.chip("[ launch belai:triage ]", VulnTriage, ColorVoice)
	}

	p := Panel{
		Title: "vulnerability", Detail: c.ID, Meta: "click to open",
		Width: width, Accent: lipgloss.TerminalColor(ColorTeal), Open: true,
		BodyRows: []Row{link.row(), hint.row(), act.row()},
	}
	return p.Render()
}
