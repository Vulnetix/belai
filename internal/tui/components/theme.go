package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The palette is lifted from the Pix owl mark in banner.go so every surface —
// panels, chips, headers, footer — reads as one brand rather than as raw ANSI.
// lipgloss degrades these to the 256- or 16-colour equivalents on terminals
// that cannot do truecolour, so no separate low-colour path is needed.
var (
	ColorInk      = lipgloss.Color("#1C3431") // owl outline; chip foreground
	ColorTeal     = lipgloss.Color("#3AC4B4") // plumage; primary accent
	ColorTealSoft = lipgloss.Color("#76E0CD") // highlight plumage
	ColorCream    = lipgloss.Color("#F6EED6") // face; primary text emphasis
	ColorAmber    = lipgloss.Color("#E8912B") // beak; tools and warnings
	ColorDanger   = lipgloss.Color("#E2564E")

	// Chrome that must stay legible on both light and dark terminals.
	ColorMuted = lipgloss.AdaptiveColor{Light: "#5C6E6B", Dark: "#7D918D"}
	ColorLine  = lipgloss.AdaptiveColor{Light: "#C6D5D2", Dark: "#2F4340"}

	// Diff row washes. Desaturated derivatives of the accent and danger hues,
	// dark enough on a dark terminal (and light enough on a light one) to sit
	// under body text without fighting it. These are backgrounds only: the
	// palette above is all foreground-weight and unreadable behind text.
	ColorDiffAddBg = lipgloss.AdaptiveColor{Light: "#DFF1E6", Dark: "#11301F"}
	ColorDiffDelBg = lipgloss.AdaptiveColor{Light: "#F9E2E0", Dark: "#3A1917"}
)

var (
	// MutedStyle is secondary text: provenance, hints, metadata.
	MutedStyle = lipgloss.NewStyle().Foreground(ColorMuted)
	// LineStyle draws hairline rules and panel edges.
	LineStyle = lipgloss.NewStyle().Foreground(ColorLine)
	// EmphStyle is primary text emphasis inside a panel body.
	EmphStyle = lipgloss.NewStyle().Foreground(ColorCream).Bold(true)
	// AccentStyle is the brand accent for titles and selected rows.
	AccentStyle = lipgloss.NewStyle().Foreground(ColorTeal)
	// KeyStyle renders a keycap in a help bar.
	KeyStyle = lipgloss.NewStyle().Foreground(ColorTealSoft).Bold(true)
	// DangerStyle renders errors.
	DangerStyle = lipgloss.NewStyle().Foreground(ColorDanger)
	// WarnStyle renders warnings and tool activity.
	WarnStyle = lipgloss.NewStyle().Foreground(ColorAmber)
	// SelectionStyle renders an active drag selection. Reverse video is
	// profile-neutral and degrades gracefully on 16-colour terminals.
	SelectionStyle = lipgloss.NewStyle().Reverse(true)

	headerStyle = lipgloss.NewStyle().Foreground(ColorCream).Bold(true)
	chipStyle   = lipgloss.NewStyle().Foreground(ColorInk).Background(ColorTeal).Bold(true).Padding(0, 1)
)

// Rule draws a full-width hairline.
func Rule(width int) string {
	if width < 1 {
		return ""
	}
	return LineStyle.Render(repeatRune('─', width))
}

// SectionHeader is the standard full-screen view header: a brand mark, the
// view title, an optional right-aligned subtitle, and a hairline under both.
// Titles are rendered verbatim so callers keep their own wording.
func SectionHeader(title, subtitle string, width int) string {
	if width < 20 {
		width = 20
	}
	mark := AccentStyle.Render("◈")
	plainLeft := "◈ " + title
	left := mark + " " + headerStyle.Render(title)

	line := left
	if subtitle != "" {
		pad := max(width-visibleLen(plainLeft)-visibleLen(subtitle), 1)
		line = left + spaces(pad) + MutedStyle.Render(subtitle)
	}
	return line + "\n" + Rule(width) + "\n"
}

// HelpBar renders key/action pairs as one dim line: keycaps in the accent
// colour, actions muted, separated by mid dots.
func HelpBar(pairs ...string) string {
	var b strings.Builder
	for i := 0; i+1 < len(pairs); i += 2 {
		if i > 0 {
			b.WriteString(MutedStyle.Render("  ·  "))
		}
		b.WriteString(KeyStyle.Render(pairs[i]))
		b.WriteString(MutedStyle.Render(" " + pairs[i+1]))
	}
	return b.String()
}

// Chip renders a filled label, used for modes and status pills.
func Chip(label string, bg lipgloss.TerminalColor) string {
	return chipStyle.Background(bg).Render(label)
}

// Cursor is the selection marker used by every list view.
func Cursor(selected bool) string {
	if selected {
		return AccentStyle.Render("▸ ")
	}
	return "  "
}

func repeatRune(r rune, n int) string {
	if n < 1 {
		return ""
	}
	out := make([]rune, n)
	for i := range out {
		out[i] = r
	}
	return string(out)
}

func spaces(n int) string { return repeatRune(' ', n) }

// visibleLen is the terminal cell width of an unstyled string.
func visibleLen(s string) int { return lipgloss.Width(s) }
