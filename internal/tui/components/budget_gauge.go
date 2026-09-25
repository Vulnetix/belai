package components

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// BudgetState is a token budget's colour state. It mirrors budget.State; the
// component package stays free of the budget package.
type BudgetState int

const (
	BudgetTeal BudgetState = iota
	BudgetAmber
	BudgetRed
)

// BudgetGauge is the token budget the footer shows on line 1, right-aligned.
type BudgetGauge struct {
	// Scope is "session", "day" or "month".
	Scope string
	// TokenPct is the share of the allowance left, 0–100.
	TokenPct int
	// UsedFrac is the share of the allowance used, 0–1: the bar's fill.
	UsedFrac float64
	// TimeLeft is the time until the scope's window ends ("5h 12m"); empty
	// for a session budget, which has no window.
	TimeLeft string
	State    BudgetState
}

// Colour returns the state's colour: teal, amber or red.
func (g BudgetGauge) Colour() lipgloss.TerminalColor {
	switch g.State {
	case BudgetRed:
		return ColorDanger
	case BudgetAmber:
		return ColorAmber
	default:
		return ColorTeal
	}
}

// budgetSegment renders the gauge at one of three widths: 2 is everything
// ("day 62% · 5h 12m ▕bar▏"), 1 drops the time, 0 drops the percentage too.
// The scope, percentage and time take the state colour; the bar fills in the
// state colour over a grey trough.
func (g BudgetGauge) budgetSegment(detail int) string {
	style := lipgloss.NewStyle().Foreground(g.Colour())
	var parts []string
	label := g.Scope
	if detail >= 1 {
		label += fmt.Sprintf(" %d%%", g.TokenPct)
	}
	parts = append(parts, style.Render(label))
	if detail >= 2 && g.TimeLeft != "" {
		parts = append(parts, style.Render(g.TimeLeft))
	}
	text := strings.Join(parts, MutedStyle.Render(" · "))
	return text + " " + g.Bar()
}

// Bar renders the used share in the state colour over a grey remaining trough.
func (g BudgetGauge) Bar() string {
	fill, trough := fillBar(g.UsedFrac, barWidth)
	return lipgloss.NewStyle().Foreground(g.Colour()).Render(fill) + MutedStyle.Render(trough)
}

// fillBar splits a width-cell bar at frac (clamped to 0–1) into its filled
// part — whole blocks plus one eighth-block partial cell — and the empty part
// drawn with '░'. The context bar and the budget gauge share it.
func fillBar(frac float64, width int) (fill, trough string) {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	cells := frac * float64(width)
	full := int(cells)
	eighth := int((cells-float64(full))*8 + 0.5)
	if eighth > 7 {
		full++
		eighth = 0
	}
	var f, t strings.Builder
	for i := 0; i < width; i++ {
		switch {
		case i < full:
			f.WriteRune('█')
		case i == full && eighth > 0:
			f.WriteRune(barEighths[eighth-1])
		default:
			t.WriteRune('░')
		}
	}
	return f.String(), t.String()
}
