package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Panel is one flat bordered block: a rounded frame carrying its title inline
// on the top edge and optional right-aligned metadata. Panels never nest —
// tool activity and system notices render as flat rows beside them, not as
// boxes inside them.
type Panel struct {
	Title  string
	Meta   string // right-aligned on the top edge (timing, token counts, hints)
	Body   string
	Width  int // total width including both border cells
	Accent lipgloss.TerminalColor
	// Raw keeps the body verbatim (no re-wrapping) for bodies that already
	// render at the right width, such as the textarea.
	Raw bool
}

const panelMinWidth = 24

// View renders the panel.
func (p Panel) View() string {
	width := max(p.Width, panelMinWidth)
	inner := width - 4 // two border cells plus one space of padding each side

	accent := p.Accent
	if accent == nil {
		accent = ColorTeal
	}
	edge := lipgloss.NewStyle().Foreground(accent)
	titleStyle := lipgloss.NewStyle().Foreground(accent).Bold(true)

	title := p.Title
	meta := p.Meta

	// Lay the top edge out on plain text first, then style the pieces: the
	// arithmetic must not see escape sequences.
	leftPlain := "╭─ " + title + " "
	rightPlain := "─╮"
	if meta != "" {
		rightPlain = " " + meta + " ─╮"
	}
	fill := width - visibleLen(leftPlain) - visibleLen(rightPlain)
	if fill < 0 {
		// Drop the meta before truncating the title.
		rightPlain = "─╮"
		meta = ""
		fill = width - visibleLen(leftPlain) - visibleLen(rightPlain)
	}
	if fill < 0 {
		title = truncateRunes(title, len([]rune(title))+fill)
		leftPlain = "╭─ " + title + " "
		fill = max(width-visibleLen(leftPlain)-visibleLen(rightPlain), 0)
	}

	top := edge.Render("╭─ ") + titleStyle.Render(title) + edge.Render(" "+repeatRune('─', fill))
	if meta != "" {
		top += MutedStyle.Render(" "+meta+" ") + edge.Render("─╮")
	} else {
		top += edge.Render("─╮")
	}
	bottom := edge.Render("╰" + repeatRune('─', width-2) + "╯")

	body := p.Body
	if !p.Raw {
		body = lipgloss.NewStyle().Width(inner).Render(body)
	}

	bar := edge.Render("│")
	var b strings.Builder
	b.WriteString(top + "\n")
	for _, line := range strings.Split(body, "\n") {
		if w := visibleLen(line); w > inner {
			line = lipgloss.NewStyle().MaxWidth(inner).Render(line)
		} else {
			line += spaces(inner - w)
		}
		b.WriteString(bar + " " + line + " " + bar + "\n")
	}
	b.WriteString(bottom)
	return b.String()
}
