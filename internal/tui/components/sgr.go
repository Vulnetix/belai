package components

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// This file is the only place in the TUI that writes an SGR escape sequence by
// hand. Everything else goes through lipgloss.
//
// The reason for the exception is layering. lipgloss closes every styled span
// with a full reset (\x1b[0m), which clears the background as well as the
// foreground. That is harmless when each span stands alone, but it makes a
// background impossible to hold across a line: the first styled token inside a
// row would cancel the row's own background, and the rest of the line would
// render bare.
//
// So a row emits its background once on the left, closes it once on the right
// with \x1b[49m, and closes each foreground span with \x1b[39m — the
// foreground-only reset. Backgrounds and foregrounds then compose freely, and
// reverse video (\x1b[7m … \x1b[27m) can sit inside both without needing to
// know what either of them is.
//
// Rule for anyone editing this package: inside a Row, never emit \x1b[0m.
const (
	fgOff     = "\x1b[39m" // reset foreground only; leaves any background intact
	bgOff     = "\x1b[49m" // reset background only; leaves any foreground intact
	emphOn    = "\x1b[7m"  // reverse video: swaps fg and bg, whatever they are
	emphOff   = "\x1b[27m"
	strikeOn  = "\x1b[9m" // strikethrough
	strikeOff = "\x1b[29m"
)

// fgSeq returns the escape sequence that sets c as the foreground, or "" when
// the colour is nil or the terminal cannot render colour. Pair every non-empty
// result with fgOff, never with a full reset.
func fgSeq(c lipgloss.TerminalColor) string { return colorSeq(c, false) }

// bgSeq returns the escape sequence that sets c as the background, or "" when
// the colour is nil or the terminal cannot render colour. Pair every non-empty
// result with bgOff.
func bgSeq(c lipgloss.TerminalColor) string { return colorSeq(c, true) }

func colorSeq(c lipgloss.TerminalColor, bg bool) string {
	if c == nil {
		return ""
	}
	profile := lipgloss.ColorProfile()
	if profile == termenv.Ascii {
		return ""
	}
	spec := colorSpec(c)
	if spec == "" {
		return ""
	}
	tc := profile.Color(spec)
	if tc == nil {
		return ""
	}
	seq := tc.Sequence(bg)
	if seq == "" {
		return ""
	}
	return "\x1b[" + seq + "m"
}

// colorSpec reduces a lipgloss colour to the hex or ANSI-index string termenv
// parses. AdaptiveColor is resolved against the terminal's background here
// rather than deferred, because a Row emits its sequences once and cannot
// re-resolve them later.
func colorSpec(c lipgloss.TerminalColor) string {
	switch v := c.(type) {
	case lipgloss.NoColor:
		return ""
	case lipgloss.Color:
		return string(v)
	case lipgloss.ANSIColor:
		return fmt.Sprintf("%d", uint(v))
	case lipgloss.AdaptiveColor:
		if lipgloss.HasDarkBackground() {
			return v.Dark
		}
		return v.Light
	case lipgloss.CompleteColor:
		switch lipgloss.ColorProfile() {
		case termenv.TrueColor:
			return v.TrueColor
		case termenv.ANSI256:
			return v.ANSI256
		default:
			return v.ANSI
		}
	case lipgloss.CompleteAdaptiveColor:
		if lipgloss.HasDarkBackground() {
			return colorSpec(v.Dark)
		}
		return colorSpec(v.Light)
	}
	// Any other TerminalColor still satisfies color.Color, so fall back to its
	// RGBA. The 8-bit shift undoes colour.Color's 16-bit alpha-premultiplied
	// range.
	r, g, b, a := c.RGBA()
	if a == 0 {
		return ""
	}
	return fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
}
