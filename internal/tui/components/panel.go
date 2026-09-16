package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
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

	// Marker names a truncation hint (plain text, e.g. "… 12 more lines")
	// that occupies a whole body line; Hidden is the text the hint hides.
	// Render attaches them to the last body line whose stripped text equals
	// Marker so a selection over the hint copies the hidden remainder.
	// Empty Marker means the panel has no truncation marker.
	Marker string
	Hidden string
}

const panelMinWidth = 24

// View renders the panel. It is the string-only half of Render; the line map
// is dropped because callers that only draw do not need provenance.
func (p Panel) View() string {
	s, _ := p.Render()
	return s
}

// Render renders the panel and returns the per-line provenance of every row
// it emits. The text is byte-identical to what View returns today; the map
// exists so hit-testing and copying can recover clean text without
// pattern-matching the rendered output (the │ panel bar and the │ system-row
// marker are the same glyph, and only the renderer knows which columns are
// decoration).
func (p Panel) Render() (string, LineMap) {
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
	barCol := visibleLen("│ ") // border cell plus its one column of padding

	// The marker, when set, rides the last body line whose stripped text
	// equals it; the caller passes truncation info down instead of the panel
	// string-matching its own output.
	bodyLines := strings.Split(body, "\n")
	markerIdx := -1
	if p.Marker != "" {
		for i := len(bodyLines) - 1; i >= 0; i-- {
			if strings.TrimRight(ansi.Strip(bodyLines[i]), " ") == p.Marker {
				markerIdx = i
				break
			}
		}
	}

	var b strings.Builder
	var lm LineMap
	b.WriteString(top + "\n")
	lm = append(lm, SourceLine{Chrome: true})
	for i, line := range bodyLines {
		if w := visibleLen(line); w > inner {
			line = lipgloss.NewStyle().MaxWidth(inner).Render(line)
		} else {
			line += spaces(inner - w)
		}
		b.WriteString(bar + " " + line + " " + bar + "\n")
		// Trailing padding is decoration, not text: the selectable region ends
		// where the content ends, so a drag over the gutter copies nothing.
		plain := strings.TrimRight(ansi.Strip(line), " ")
		sl := SourceLine{Col: barCol, Width: visibleLen(plain), Text: plain}
		if i == markerIdx {
			sl.MarkerCol = sl.Col
			sl.MarkerWidth = sl.Width
			sl.Hidden = p.Hidden
		}
		lm = append(lm, sl)
	}
	b.WriteString(bottom)
	lm = append(lm, SourceLine{Chrome: true})
	return b.String(), lm
}
