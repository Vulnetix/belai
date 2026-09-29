package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// sparkRunes are the eight vertical block heights, lowest first.
var sparkRunes = [8]rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// Sparkline draws vals as cells vertical bars, each scaled to the largest
// cell. When there are more values than cells they are grouped into cells by
// summing; when there are fewer, each value fills one cell and the rest are
// empty. A cell with nothing in it is a low bar in the zero style, so an idle
// hour still reads as a gap rather than as missing data. bar styles a cell
// that has usage.
func Sparkline(vals []int64, cells int, bar, zero lipgloss.Style) string {
	if cells <= 0 {
		return ""
	}
	grouped := make([]int64, cells)
	for i, v := range vals {
		if v < 0 {
			v = 0
		}
		c := i * cells / max(len(vals), 1)
		if c >= cells {
			c = cells - 1
		}
		grouped[c] += v
	}
	var peak int64
	for _, v := range grouped {
		peak = max(peak, v)
	}
	var b strings.Builder
	for _, v := range grouped {
		if v <= 0 || peak <= 0 {
			b.WriteString(zero.Render(string(sparkRunes[0])))
			continue
		}
		// Scale into the eight heights, keeping any non-zero cell above the floor.
		lvl := int((float64(v)/float64(peak))*7 + 0.5)
		if lvl < 1 {
			lvl = 1
		}
		b.WriteString(bar.Render(string(sparkRunes[min(lvl, 7)])))
	}
	return b.String()
}

// SparkTotal sums vals; a zero total means there is nothing to draw.
func SparkTotal(vals []int64) int64 {
	var n int64
	for _, v := range vals {
		n += v
	}
	return n
}

// LimitBar draws a limit's used share, 0 to 1, over a grey trough in colour,
// width cells wide, with a ╹ reset marker at the elapsed share of the window
// (elapsed < 0 draws no marker). The marker replaces a trough cell only, so
// fill left of it is on pace and fill past it is not.
func LimitBar(used, elapsed float64, width int, colour lipgloss.TerminalColor) string {
	if width <= 0 {
		return ""
	}
	fill, trough := fillBar(used, width)
	cells := append([]rune(fill), []rune(trough)...)
	mark := -1
	if elapsed >= 0 {
		mark = int(elapsed * float64(width))
		if mark >= width {
			mark = width - 1
		}
		if cells[mark] != '░' {
			mark = -1 // inside the fill: the fill already passed it
		}
	}
	fillStyle := lipgloss.NewStyle().Foreground(colour)
	var b strings.Builder
	for i, r := range cells {
		switch {
		case i == mark:
			b.WriteString(LowStyle.Render("╹"))
		case r == '░':
			b.WriteString(MutedStyle.Render(string(r)))
		default:
			b.WriteString(fillStyle.Render(string(r)))
		}
	}
	return b.String()
}
