// Package components holds the Bubble Tea building blocks for the Signet TUI.
package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

var versionStyle = lipgloss.NewStyle().Foreground(ColorMuted)

func isVersionSentinel(s string) bool {
	switch s {
	case "", "dev", "unknown":
		return true
	}
	return false
}

// PixGrid is a 12×12 pixel cartoon owl rendered with half-blocks (▀).
// Each text cell shows two pixels; 12 cols × 6 text rows.
var PixGrid = [12]string{
	". . # . . . . . . # . .",
	". # T # . . . . # T # .",
	". # T T T T T T T T # .",
	"# T O O O T T O O O T #",
	"# T O * @ T T @ * O T #",
	"# T O @ @ T T @ @ O T #",
	"# T O O O T T O O O T #",
	"# T T T o o o o T T T #",
	". # T T T o o T T T # .",
	". # T T T T T T T T # .",
	". . # T T T T T T # . .",
	". . . # # # # # # . . .",
}

var pixColors = map[rune]lipgloss.Color{
	'#': lipgloss.Color("#1C3431"),
	'T': lipgloss.Color("#3AC4B4"),
	't': lipgloss.Color("#76E0CD"),
	'O': lipgloss.Color("#F6EED6"),
	'@': lipgloss.Color("#1C3431"),
	'*': lipgloss.Color("#FFFDF7"),
	'o': lipgloss.Color("#E8912B"),
	'.': lipgloss.Color(""),
}

// Banner renders the Pix owl + wordmark.
type Banner struct {
	Width   int
	Version string
	Commit  string
	Built   string
}

// View returns the banner. In ASCII/NO_COLOR mode it falls back to plain text.
func (b Banner) View() string {
	if termenv.NewOutput(nil).ColorProfile() == termenv.Ascii {
		return b.textView()
	}
	return b.pixView()
}

// versionLine renders one dim line with the build facts that are known.
// Empty fields and unstamped sentinels are dropped; if nothing is known it
// returns "" so a bare `go build` shows no line at all.
func (b Banner) versionLine() string {
	var parts []string
	if !isVersionSentinel(b.Version) {
		parts = append(parts, "v"+strings.TrimPrefix(b.Version, "v"))
	}
	if !isVersionSentinel(b.Commit) {
		parts = append(parts, b.Commit)
	}
	if !isVersionSentinel(b.Built) {
		parts = append(parts, b.Built)
	}
	if len(parts) == 0 {
		return ""
	}
	line := strings.Join(parts, " · ")
	if b.Width > 0 {
		line = versionStyle.Copy().MaxWidth(b.Width).Render(line)
	} else {
		line = versionStyle.Render(line)
	}
	return line
}

func (b Banner) pixView() string {
	var lines []string
	for row := 0; row < 6; row++ {
		var line strings.Builder
		for col := 0; col < 12; col++ {
			upper := rune(PixGrid[row*2][col*2])
			lower := rune(PixGrid[row*2+1][col*2])
			fg := pixColors[upper]
			bg := pixColors[lower]
			if upper == '.' && lower == '.' {
				line.WriteString(" ")
				continue
			}
			cell := "▀"
			style := lipgloss.NewStyle()
			if fg != "" {
				style = style.Foreground(fg)
			}
			if bg != "" {
				style = style.Background(bg)
			}
			line.WriteString(style.Render(cell))
		}
		lines = append(lines, line.String())
	}

	owl := strings.Join(lines, "\n")

	// The wordmark block sits beside the owl rather than under it: it keeps
	// the banner to six rows and leaves the transcript more of the screen.
	// Six rows, always: the owl is six rows tall, and an unstamped build must
	// not change the banner's height and reflow the transcript under it.
	right := []string{
		"",
		lipgloss.NewStyle().Foreground(ColorCream).Bold(true).Render("S I G N E T"),
		MutedStyle.Render("injection-safe coding harness"),
		b.versionLine(),
		"",
		MutedStyle.Render("type ") + KeyStyle.Render("/help") + MutedStyle.Render(" for commands"),
	}

	block := lipgloss.NewStyle().PaddingLeft(3).Render(strings.Join(right, "\n"))
	return lipgloss.JoinHorizontal(lipgloss.Top, owl, block)
}

func (b Banner) textView() string {
	var out []string
	out = append(out, "SIGNET")
	if v := b.versionLine(); v != "" {
		out = append(out, v)
	}
	return strings.Join(out, "\n")
}
