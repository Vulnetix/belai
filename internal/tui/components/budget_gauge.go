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

// IntelGauge is the session intelligence slot of the footer's line-1 cycle. It
// stands beside BudgetGauge: the app shows one or the other, and the intel
// slot is the only one when routing is routed or the model has no budget.
type IntelGauge struct {
	// Today is the day's tokens, formatted ("34.4M").
	Today string
	// Limit is the tightest plan limit's short label ("5h 23%"); empty when the
	// provider reported none.
	Limit string
	// LimitFrac is that limit's used share, 0 to 1, and ElapsedFrac the share of
	// its window that has passed (the ╹ marker); both are negative when there is
	// no limit.
	LimitFrac   float64
	ElapsedFrac float64
	// Spark is tokens per hour for the last 24 hours, drawn when there is no
	// limit. Pace is the label shown beside it ("idle", "active").
	Spark []int64
	Pace  string
	State BudgetState
	// Hint adds the subtle shortcut key to the roomiest rendering.
	Hint bool
}

// Colour returns the state's colour: teal, amber or red.
func (g IntelGauge) Colour() lipgloss.TerminalColor {
	return BudgetGauge{State: g.State}.Colour()
}

// intelHintKey is the shortcut the hint advertises.
const intelHintKey = "f12"

// Bar draws the slot's picture: the tightest plan limit with its reset marker,
// else the 24-hour sparkline, else a dotted trough when nothing has been spent.
func (g IntelGauge) Bar() string {
	switch {
	case g.LimitFrac >= 0 && g.Limit != "":
		return LimitBar(g.LimitFrac, g.ElapsedFrac, barWidth, g.Colour())
	case SparkTotal(g.Spark) > 0:
		return Sparkline(g.Spark, barWidth, lipgloss.NewStyle().Foreground(g.Colour()), LowStyle)
	}
	return LowStyle.Render(strings.Repeat("·", barWidth))
}

// segmentAt renders the slot at one of three widths: 2 is everything
// ("intel  today 34.4M · 5h 23% bar"), 1 drops the limit or pace, 0 keeps only
// the label and the bar. hint appends the shortcut to width 2 only.
func (g IntelGauge) segmentAt(detail int, hint bool) string {
	style := lipgloss.NewStyle().Foreground(g.Colour())
	text := style.Render("intel")
	if detail >= 1 && g.Today != "" {
		text += MutedStyle.Render("  today " + g.Today)
	}
	if detail >= 2 {
		label := g.Limit
		if label == "" {
			label = g.Pace
		}
		if label != "" {
			text += MutedStyle.Render(" · ") + style.Render(label)
		}
	}
	text += " " + g.Bar()
	if hint && detail >= 2 {
		text += LowStyle.Render("  " + intelHintKey)
	}
	return text
}
