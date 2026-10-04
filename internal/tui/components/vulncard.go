package components

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/belai/internal/vulnid"
)

// VulnRole is the transcript role of the vulnerability row: the card the TUI
// adds once per identifier when a tool result or a reply names one. It is
// render-only, like the read-aloud card: it is Ephemeral, never produces a
// session entry, and buildTurns never promotes it, so no model, sync, telemetry
// or notification sees it.
const VulnRole = "vuln"

// Click actions a vulnerability row's hit regions carry. The identifier is not
// in the action: the TUI reads it from the clicked message's VulnCard.
const (
	VulnOpen      = "vuln-open"
	VulnCopyURL   = "vuln-copy-url"
	VulnCopyCmd   = "vuln-copy-cmd"
	VulnRemediate = "vuln-remediate"
	VulnTriage    = "vuln-triage"
)

// vulnLabelWidth is the width of the "console  " and "vdb      " labels. The
// link and the command that follow them are selectable text (drag to copy);
// everything else on the card is interface.
const vulnLabelWidth = 9

// VulnCard is everything the row draws. Every field is composed by the harness
// from one identifier vulnid.Valid accepted (vulnid.URL, vulnid.Command); none
// of it is text that came from a tool result or a reply.
type VulnCard struct {
	ID      string // the canonical identifier
	URL     string // the console page, https
	Command string // the vdb lookup command
	// Launched is set once the triage agent has been started from this row.
	Launched bool
	// Remediating is set once the remediation prompt has been sent from this
	// row.
	Remediating bool
}

// Key is what makes two cards draw differently.
func (c *VulnCard) Key() string {
	if c == nil {
		return ""
	}
	k := c.ID + "|" + c.URL + "|" + c.Command
	if c.Launched {
		k += "|launched"
	}
	if c.Remediating {
		k += "|remediating"
	}
	return k
}

// vulnPanel renders the row: the console link and the vdb command as text a
// drag selects, a note on the other lookups, and the actions. The labels, the
// note and the buttons are interface, not text, so nothing in them is copied.
func vulnPanel(msg Message, width int) (string, LineMap) {
	c := msg.Vuln
	if c == nil {
		c = &VulnCard{}
	}
	indent := spaces(vulnLabelWidth)

	var link rowBuilder
	link.text("console  ", ColorLow)
	link.text(c.URL, ColorTeal)

	var cmd rowBuilder
	cmd.text("vdb      ", ColorLow)
	cmd.text(c.Command, ColorMuted)

	var note rowBuilder
	note.text(indent, nil)
	note.text(vulnid.HelpNote, ColorLow)

	var work rowBuilder
	work.text(indent, nil)
	if c.Remediating {
		work.text("remediation started", ColorMuted)
	} else {
		work.chip("[ remediate ]", VulnRemediate, ColorVoice)
	}
	work.text("  ", nil)
	if c.Launched {
		work.text("belai:triage started", ColorMuted)
	} else {
		work.chip("[ launch belai:triage ]", VulnTriage, ColorVoice)
	}

	var tools rowBuilder
	tools.text(indent, nil)
	tools.chip("[ open link ]", VulnOpen, ColorTeal)
	tools.text("  ", nil)
	tools.chip("[ copy link ]", VulnCopyURL, ColorTeal)
	tools.text("  ", nil)
	tools.chip("[ copy command ]", VulnCopyCmd, ColorTeal)

	p := Panel{
		Title: "vulnerability", Detail: c.ID, Meta: "drag to copy",
		Width: width, Accent: lipgloss.TerminalColor(ColorTeal), Open: true,
		BodyRows: []Row{
			link.rowFrom(vulnLabelWidth), cmd.rowFrom(vulnLabelWidth),
			note.row(), work.row(), tools.row(),
		},
	}
	return p.Render()
}
