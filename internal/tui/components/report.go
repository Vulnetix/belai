package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ReportRole is the transcript role of a markdown report card: a /vulnetix
// review scanner's harness-composed result, or a scanner agent's classified
// report. It is render-only: buildTurns never promotes it, so no model sees
// it through the transcript. The card's title rides on ToolName, its
// right-aligned meta (timing, counts) on ToolArgs, and a
// "failed" Status draws the frame in the warning accent.
const ReportRole = "report"

// reportPreviewLines is how much of a collapsed report card shows. A scanner
// agent's report is one line per finding, so the preview holds the most
// severe handful; ctrl+o expands the rest like any other panel.
const reportPreviewLines = 12

// reportPanel renders a report card with a markdown body.
func reportPanel(msg Message, width int, expandAll bool) (string, LineMap) {
	title := msg.ToolName
	if title == "" {
		title = "report"
	}
	accent := lipgloss.TerminalColor(ColorTeal)
	if msg.Status == "failed" {
		accent = ColorAmber
	}
	body := strings.TrimRight(msg.Text(), "\n")
	inner := max(width-4, 8)
	md := RenderMarkdown(body, inner)
	rows := md.Rows
	if !expandAll && !msg.Expanded {
		rows, _, _ = truncateMarkdown(body, md, reportPreviewLines)
	}
	p := Panel{Title: title, Meta: msg.ToolArgs, Width: width, Accent: accent, BodyRows: rows}
	return p.Render()
}
