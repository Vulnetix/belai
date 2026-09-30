package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Panel is one flat block carrying its title inline on the top edge and
// optional right-aligned metadata. Boxed, it is a rounded frame; Open, it is
// only the top rule over an indented body (docs/tui-design.md). Panels never
// nest — tool activity and system notices render as flat rows beside them,
// not as boxes inside them.
type Panel struct {
	Title  string
	Meta   string // right-aligned on the top edge (timing, token counts, hints)
	Body   string
	Width  int // total width including both border cells
	Accent lipgloss.TerminalColor
	// TitleAccent colours the title separately from the frame. When nil the
	// frame accent is used, so panels that want one colour for both keep
	// working unchanged.
	TitleAccent lipgloss.TerminalColor
	// Detail is a low-contrast note after the title on the top edge (the
	// model id, a tool count). It is dropped before the title is truncated.
	Detail string
	// Open draws only the top rule: no side bars and no bottom edge. Thread
	// blocks are open; the composer and full-screen views stay boxed.
	Open bool
	// Raw keeps the body verbatim (no re-wrapping) for bodies that already
	// render at the right width, such as the textarea.
	Raw bool

	// BodyRows takes precedence over Body: pre-built rows rendered at the inner
	// width, one screen line each, and not re-wrapped. It is how markdown and
	// grouped notices reach the transcript with their provenance intact.
	BodyRows []Row

	// Marker names a truncation hint (plain text, e.g. "… 12 more lines")
	// that occupies a whole body line; Hidden is the text the hint hides.
	// Render attaches them to the last body line whose stripped text equals
	// Marker so a selection over the hint copies the hidden remainder.
	// Empty Marker means the panel has no truncation marker.
	// Wave turns the frame's top and bottom rules into a slow ripple, drawn from
	// scan-line glyphs at staggered heights and advanced by WavePhase. It is off
	// for every panel except the voice composer while speech is heard.
	Wave      bool
	WavePhase int

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
// it emits. The map exists so hit-testing and copying can recover clean text
// without pattern-matching the rendered output (the │ panel bar and the │
// system-row marker are the same glyph, and only the renderer knows which
// columns are decoration).
func (p Panel) Render() (string, LineMap) {
	width := max(p.Width, panelMinWidth)
	inner := width - 4 // two border cells plus one space of padding each side

	accent := p.Accent
	if accent == nil {
		accent = ColorTeal
	}
	titleAccent := p.TitleAccent
	if titleAccent == nil {
		titleAccent = accent
	}
	edge := lipgloss.NewStyle().Foreground(accent)
	titleStyle := lipgloss.NewStyle().Foreground(titleAccent).Bold(true)

	top := TopEdgeRule(width, p.Title, p.Detail, p.Meta, edge, titleStyle, p.Open, p.ruleFill())

	// An open panel indents its body by the same two columns the boxed
	// frame's bar and padding take, so line-map columns match either way.
	left, right := edge.Render("│")+" ", " "+edge.Render("│")
	if p.Open {
		left, right = "  ", ""
	}
	barCol := visibleLen("│ ") // border cell plus its one column of padding

	// row lays one body line out: boxed rows pad to the inner width so the
	// right bar lines up; open rows end where their content ends.
	row := func(line string) string {
		if p.Open {
			return strings.TrimRight(left+line, " ")
		}
		if w := visibleLen(line); w < inner {
			line += spaces(inner - w)
		}
		return left + line + right
	}

	// Pre-built rows take precedence and skip the string re-wrap: they already
	// render at the inner width, so wrapping them again would double-wrap
	// already-laid-out lines and lose per-row markers and reopen state.
	if p.BodyRows != nil {
		var b strings.Builder
		var lm LineMap
		b.WriteString(top + "\n")
		lm = append(lm, SourceLine{Chrome: true})
		for _, r := range p.BodyRows {
			line, sl := r.Render(inner)
			b.WriteString(row(line) + "\n")
			if sl.Chrome {
				lm = append(lm, sl)
				continue
			}
			// The row's provenance is in its own coordinate system (left edge
			// at 0); shift it past the bar and padding so a selection still
			// matches the full frame line.
			sl.Col += barCol
			if sl.MarkerWidth > 0 {
				sl.MarkerCol += barCol
			}
			if len(sl.Hits) > 0 {
				shifted := make([]Hit, len(sl.Hits))
				for i, h := range sl.Hits {
					h.Col += barCol
					shifted[i] = h
				}
				sl.Hits = shifted
			}
			lm = append(lm, sl)
		}
		return p.finish(&b, lm, width, edge)
	}

	body := p.Body
	if !p.Raw {
		body = lipgloss.NewStyle().Width(inner).Render(body)
	}

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
		if visibleLen(line) > inner {
			line = lipgloss.NewStyle().MaxWidth(inner).Render(line)
		}
		b.WriteString(row(line) + "\n")
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
	return p.finish(&b, lm, width, edge)
}

// finish closes the panel: a boxed panel gets its bottom edge, an open one
// ends on its last body row (the next block's top rule is the separator).
func (p Panel) finish(b *strings.Builder, lm LineMap, width int, edge lipgloss.Style) (string, LineMap) {
	if p.Open {
		return strings.TrimSuffix(b.String(), "\n"), lm
	}
	rule := p.ruleFill()
	if rule == nil {
		rule = func(n int) string { return repeatRune('─', n) }
	}
	b.WriteString(edge.Render("╰" + rule(width-2) + "╯"))
	return b.String(), append(lm, SourceLine{Chrome: true})
}

// TopEdge renders a panel's top line. Boxed, it is the rounded frame's edge
// `╭─ title ──── meta ─╮` in the frame colour. Open, it is the rule
// `── title  detail ──── meta ──`: the lead-in and bold title in the accent,
// the detail and meta in ColorLow and the fill in ColorLine, so the rule reads
// as structure and only the title carries colour. The layout is computed on
// plain text first; when the width runs out the meta goes first, then the
// detail, then the title is truncated.
func TopEdge(width int, title, detail, meta string, edge, titleStyle lipgloss.Style, open bool) string {
	return TopEdgeRule(width, title, detail, meta, edge, titleStyle, open, nil)
}

// TopEdgeRule is TopEdge with a custom fill for the rule; nil is the plain
// `─` run.
func TopEdgeRule(width int, title, detail, meta string, edge, titleStyle lipgloss.Style, open bool, rule func(n int) string) string {
	if rule == nil {
		rule = func(n int) string { return repeatRune('─', n) }
	}
	lead, tail, end := "╭─ ", " ─╮", "─╮"
	if open {
		lead, tail, end = "── ", " ──", ""
	}
	measure := func() int {
		n := visibleLen(lead+title) + 1
		if detail != "" {
			n += visibleLen("  " + detail)
		}
		if meta != "" {
			return n + visibleLen(" "+meta+tail)
		}
		return n + visibleLen(end)
	}
	fill := width - measure()
	if fill < 0 && meta != "" {
		meta = ""
		fill = width - measure()
	}
	if fill < 0 && detail != "" {
		detail = ""
		fill = width - measure()
	}
	if fill < 0 {
		title = truncateRunes(title, len([]rune(title))+fill)
		fill = max(width-measure(), 0)
	}

	top := edge.Render(lead) + titleStyle.Render(title)
	if !open {
		if detail != "" {
			top += MutedStyle.Render("  " + detail)
		}
		top += edge.Render(" " + rule(fill))
		if meta != "" {
			return top + MutedStyle.Render(" "+meta+" ") + edge.Render(end)
		}
		return top + edge.Render(end)
	}
	if detail != "" {
		top += LowStyle.Render("  " + detail)
	}
	top += LineStyle.Render(" " + rule(fill))
	if meta != "" {
		top += LowStyle.Render(" "+meta) + LineStyle.Render(tail)
	}
	return top
}

// waveGlyphs are horizontal scan lines at four heights. Laid side by side
// in a sine-like order they read as a ripple along the rule.
var waveGlyphs = []rune("─⎽⎼⎻⎺⎻⎼⎽")

// WaveRule returns n cells of rippling rule at the given phase. The same
// phase draws the same rule, so a frame is reproducible.
func WaveRule(n, phase int) string {
	if n <= 0 {
		return ""
	}
	out := make([]rune, n)
	for i := range out {
		out[i] = waveGlyphs[((i-phase)%len(waveGlyphs)+len(waveGlyphs))%len(waveGlyphs)]
	}
	return string(out)
}

// ruleFill is the panel's rule fill: a wave while Wave is set, else nil for
// the plain rule.
func (p Panel) ruleFill() func(int) string {
	if !p.Wave {
		return nil
	}
	return func(n int) string { return WaveRule(n, p.WavePhase) }
}
