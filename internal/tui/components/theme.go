package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The palette is lifted from the Pix owl mark in banner.go so every surface —
// panels, chips, headers, footer — reads as one brand rather than as raw ANSI.
// lipgloss degrades these to the 256- or 16-colour equivalents on terminals
// that cannot do truecolour, so no separate low-colour path is needed.
//
// Every colour has one role; docs/tui-design.md is the reference. The light
// values are darker variants of the brand hues, because the dark-terminal
// accents are unreadable on a light background.
var (
	ColorInk      = lipgloss.AdaptiveColor{Light: "#FBFBF8", Dark: "#1C3431"} // chip foreground
	ColorTeal     = lipgloss.AdaptiveColor{Light: "#137A6F", Dark: "#3AC4B4"} // the model's voice; success; the one brand accent
	ColorTealSoft = lipgloss.AdaptiveColor{Light: "#178574", Dark: "#76E0CD"} // your turns, plan mode, keycaps
	ColorCream    = lipgloss.AdaptiveColor{Light: "#0F1F1C", Dark: "#F6EED6"} // emphasis: what you typed, named identifiers
	ColorAmber    = lipgloss.AdaptiveColor{Light: "#A95A0B", Dark: "#E8912B"} // needs you, or you stepped in
	ColorDanger   = lipgloss.AdaptiveColor{Light: "#B8322A", Dark: "#E2564E"} // failures, relaxed guardrails
	ColorYou      = lipgloss.AdaptiveColor{Light: "#B23C7E", Dark: "#F49AC8"} // the prompts you type
	ColorVoice    = lipgloss.AdaptiveColor{Light: "#7B5BBE", Dark: "#C9B0F2"} // dictated turns, the voice frame while the fast model works

	// Role-manager categories: the colour of a row's icon, by the kind of
	// work. They never colour an outcome; tone does that.
	ColorCatSecurity = lipgloss.AdaptiveColor{Light: "#7A3FB0", Dark: "#B58AE6"}
	ColorCatMode     = lipgloss.AdaptiveColor{Light: "#2C6FB7", Dark: "#6FA8E8"}
	ColorCatContext  = lipgloss.AdaptiveColor{Light: "#8A6D0B", Dark: "#D2B24A"}
	ColorCatTools    = lipgloss.AdaptiveColor{Light: "#0E7C9B", Dark: "#4FC3E0"}
	ColorCatCode     = lipgloss.AdaptiveColor{Light: "#3F7D20", Dark: "#8CCB5E"}
	ColorCatAsk      = lipgloss.AdaptiveColor{Light: "#B0357A", Dark: "#E58AB8"}
	ColorCatRouting  = lipgloss.AdaptiveColor{Light: "#6B5B95", Dark: "#A99BD6"}

	// ColorText is reply body copy: softer than Cream so emphasis still has
	// somewhere to go.
	ColorText = lipgloss.AdaptiveColor{Light: "#2A3835", Dark: "#C9D6D2"}

	// Chrome that must stay legible on both light and dark terminals.
	ColorMuted = lipgloss.AdaptiveColor{Light: "#5C6E6B", Dark: "#7D918D"}
	// ColorLow is metadata a reader may ignore: counts, timings, hints. It is
	// deliberately below text contrast and never carries required information.
	ColorLow  = lipgloss.AdaptiveColor{Light: "#7F908C", Dark: "#4A5F5B"}
	ColorLine = lipgloss.AdaptiveColor{Light: "#C6D5D2", Dark: "#2F4340"}

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
	// LowStyle is ignorable metadata: counts, timings, hints.
	LowStyle = lipgloss.NewStyle().Foreground(ColorLow)
	// TextStyle is reply body copy.
	TextStyle = lipgloss.NewStyle().Foreground(ColorText)
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
