package components

import (
	"math"
	"regexp"
	"strconv"
	"sync/atomic"

	"github.com/charmbracelet/lipgloss"
)

// An agent profile can carry a palette of four colours: primary, secondary and
// the two shades the console derives from them (internal/agentprofile,
// identity.go). While a profile is engaged the TUI wears its colours: the
// primary replaces the brand accent (ColorTeal: titles, the mode chip, the
// model's turns) and the secondary replaces the soft accent (ColorTealSoft:
// your turns, plan mode, keycaps). Nothing else moves. ColorAmber and
// ColorDanger keep their safety meaning, and the greys, the text and the
// category colours stay put, so a persona changes who is speaking and never
// what a colour tells you.
//
// A palette colour is one value, but the terminal may be dark or light, so each
// one is fitted to both: kept as it is where it already reads at 4.5:1 on the
// terminal background, else moved toward white (dark) or black (light) until it
// does. The same promise docs/tui-design.md makes for the default palette holds
// for a persona's.

// Typical terminal backgrounds the palette is fitted against (the same two
// theme_docs_test.go checks the default palette on).
const (
	personaDarkBG  = "#1E1E1E"
	personaLightBG = "#FFFFFF"
	personaMinText = 4.5
)

var personaColour = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

var (
	// The default accents, kept so a persona can be taken off again.
	defaultTeal     = ColorTeal
	defaultTealSoft = ColorTealSoft

	// themeGen counts palette changes. A rendered message is cached as ANSI, so
	// its cache key carries this and a switch re-renders what is on screen.
	themeGen atomic.Uint64
)

// ThemeGeneration changes every time the persona colours change.
func ThemeGeneration() uint64 { return themeGen.Load() }

// PersonaPalette reports whether palette is a usable persona palette: four
// #rrggbb colours.
func PersonaPalette(palette []string) bool {
	if len(palette) != 4 {
		return false
	}
	for _, c := range palette {
		if !personaColour.MatchString(c) {
			return false
		}
	}

	return true
}

// ApplyPersona dresses the TUI in an agent's palette and reports whether it
// did. A palette that is not four #rrggbb colours (a profile with no persona)
// takes any persona off again and reports false.
func ApplyPersona(palette []string) bool {
	if !PersonaPalette(palette) {
		ResetPersona()

		return false
	}
	ColorTeal = fitAdaptive(palette[0])
	ColorTealSoft = fitAdaptive(palette[1])
	rebuildAccentStyles()
	themeGen.Add(1)

	return true
}

// ResetPersona returns the TUI to the brand accents.
func ResetPersona() {
	if ColorTeal == defaultTeal && ColorTealSoft == defaultTealSoft {
		return
	}
	ColorTeal = defaultTeal
	ColorTealSoft = defaultTealSoft
	rebuildAccentStyles()
	themeGen.Add(1)
}

// rebuildAccentStyles rebuilds the package-level styles that copied the accents
// when they were declared. Styles built at the call site read the colours then
// and need nothing.
func rebuildAccentStyles() {
	AccentStyle = lipgloss.NewStyle().Foreground(ColorTeal)
	KeyStyle = lipgloss.NewStyle().Foreground(ColorTealSoft).Bold(true)
	chipStyle = lipgloss.NewStyle().Foreground(ColorInk).Background(ColorTeal).Bold(true).Padding(0, 1)
}

// fitAdaptive gives a palette colour its two terminal variants.
func fitAdaptive(hex string) lipgloss.AdaptiveColor {
	return lipgloss.AdaptiveColor{
		Dark:  fitOn(hex, personaDarkBG, "#FFFFFF"),
		Light: fitOn(hex, personaLightBG, "#000000"),
	}
}

// fitOn is hex when it reads at 4.5:1 on bg, else the nearest mix of hex and
// toward (white on a dark ground, black on a light one) that does. The mix
// keeps the hue, and moves only as far as it must.
func fitOn(hex, bg, toward string) string {
	r, g, b := parseHex(hex)
	br, bgc, bb := parseHex(bg)
	if contrastRGB(r, g, b, br, bgc, bb) >= personaMinText {
		return formatHex(r, g, b)
	}
	tr, tg, tb := parseHex(toward)
	for step := 1; step <= 100; step++ {
		t := float64(step) / 100
		mr, mg, mb := mix(r, tr, t), mix(g, tg, t), mix(b, tb, t)
		if contrastRGB(mr, mg, mb, br, bgc, bb) >= personaMinText {
			return formatHex(mr, mg, mb)
		}
	}

	return toward
}

func parseHex(s string) (r, g, b uint8) {
	v, _ := strconv.ParseUint(s[1:], 16, 32)

	return uint8(v >> 16), uint8(v >> 8), uint8(v)
}

func formatHex(r, g, b uint8) string {
	const digits = "0123456789abcdef"
	out := []byte{'#', 0, 0, 0, 0, 0, 0}
	for i, c := range []uint8{r, g, b} {
		out[1+2*i] = digits[c>>4]
		out[2+2*i] = digits[c&0xf]
	}

	return string(out)
}

func mix(a, b uint8, t float64) uint8 {
	return uint8(math.Round(float64(a) + (float64(b)-float64(a))*t))
}

// relLuminance is the WCAG relative luminance.
func relLuminance(r, g, b uint8) float64 {
	lin := func(c uint8) float64 {
		f := float64(c) / 255
		if f <= 0.03928 {
			return f / 12.92
		}

		return math.Pow((f+0.055)/1.055, 2.4)
	}

	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
}

func contrastRGB(r1, g1, b1, r2, g2, b2 uint8) float64 {
	a, b := relLuminance(r1, g1, b1), relLuminance(r2, g2, b2)
	if a < b {
		a, b = b, a
	}

	return (a + 0.05) / (b + 0.05)
}
