// Package components holds the Bubble Tea building blocks for the Signet TUI.
package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

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
type Banner struct{
	Width int
}

// View returns the banner. In ASCII/NO_COLOR mode it falls back to plain text.
func (b Banner) View() string {
	if termenv.NewOutput(nil).ColorProfile() == termenv.Ascii {
		return b.textView()
	}
	return b.pixView()
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

	wordmark := lipgloss.NewStyle().Bold(true).Render("SIGNET")
	lines = append(lines, "", wordmark)
	return strings.Join(lines, "\n")
}

func (b Banner) textView() string {
	return "SIGNET"
}
