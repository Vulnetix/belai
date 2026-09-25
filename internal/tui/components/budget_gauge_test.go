package components

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// line1 returns the footer's first content line (after the rule), plain.
func line1(t *testing.T, f Footer) (plain, styled string) {
	t.Helper()
	lines := strings.Split(f.View(), "\n")
	if len(lines) < 2 {
		t.Fatalf("footer = %q", f.View())
	}
	return ansi.Strip(lines[1]), lines[1]
}

func gaugeFooter(width int, g *BudgetGauge) Footer {
	return Footer{Mode: "agent", Cwd: "/home/user/project", Branch: "main", Width: width, Budget: g}
}

// R9: the gauge is right-aligned on line 1: scope, percentage left, time left
// and the bar; a session budget shows no time.
func TestBudgetRule9_GaugeRightAlignedOnLineOne(t *testing.T) {
	g := &BudgetGauge{Scope: "day", TokenPct: 62, UsedFrac: 0.38, TimeLeft: "5h 12m", State: BudgetTeal}
	plain, _ := line1(t, gaugeFooter(100, g))
	if lipgloss.Width(plain) != 100 {
		t.Fatalf("line 1 width = %d, want the full 100 (right-aligned): %q", lipgloss.Width(plain), plain)
	}
	if !strings.Contains(plain, "day 62% · 5h 12m ") || !strings.HasSuffix(plain, "░") {
		t.Fatalf("line 1 = %q, want the gauge ending in the bar", plain)
	}
	if !strings.HasPrefix(strings.TrimSpace(plain), "agent") {
		t.Fatalf("line 1 = %q, want the mode chip still on the left", plain)
	}

	s := &BudgetGauge{Scope: "session", TokenPct: 80, UsedFrac: 0.2, State: BudgetTeal}
	plain, _ = line1(t, gaugeFooter(100, s))
	i := strings.Index(plain, "session 80% ")
	if i < 0 || strings.Contains(plain[i:], " · ") {
		t.Fatalf("session line 1 = %q, want the percentage and bar with no time part", plain)
	}

	// No gauge: line 1 is unchanged.
	plain, _ = line1(t, gaugeFooter(100, nil))
	if strings.Contains(plain, "%") {
		t.Fatalf("line 1 without a gauge = %q", plain)
	}
}

// R9: the used share fills in the state colour — teal, amber, red — over a
// grey trough, and the text takes the same colour.
func TestBudgetRule9_GaugeColoursByState(t *testing.T) {
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(old)

	for state, want := range map[BudgetState]lipgloss.Color{BudgetTeal: ColorTeal, BudgetAmber: ColorAmber, BudgetRed: ColorDanger} {
		g := BudgetGauge{Scope: "day", TokenPct: 40, UsedFrac: 0.6, TimeLeft: "1h 0m", State: state}
		if g.Colour() != want {
			t.Fatalf("state %d colour = %v, want %v", state, g.Colour(), want)
		}
		seq := lipgloss.NewStyle().Foreground(want).Render("x")
		prefix := seq[:strings.Index(seq, "x")]
		bar := g.Bar()
		if !strings.HasPrefix(bar, prefix) {
			t.Fatalf("state %d bar = %q, want the fill in the state colour", state, bar)
		}
		trough := MutedStyle.Render("░")
		if !strings.Contains(bar, trough[:strings.Index(trough, "░")]) {
			t.Fatalf("state %d bar = %q, want a grey trough", state, bar)
		}
		if seg := g.budgetSegment(2); !strings.HasPrefix(seg, prefix) {
			t.Fatalf("state %d text = %q, want the state colour", state, seg)
		}
	}
}

// E11: a narrow line drops the time first, then truncates the cwd side (down
// to minBudgetLeft cells), and drops the percentage only when even that does
// not fit.
func TestBudgetEdge11_NarrowTerminalShedsDetailFirst(t *testing.T) {
	g := &BudgetGauge{Scope: "month", TokenPct: 55, UsedFrac: 0.45, TimeLeft: "12d 3h", State: BudgetAmber}
	full, _ := line1(t, gaugeFooter(120, g))
	if !strings.Contains(full, "month 55% · 12d 3h") {
		t.Fatalf("wide = %q", full)
	}
	left := lipgloss.Width(strings.TrimRight(strings.Split(full, "month")[0], " "))
	pctSeg := lipgloss.Width("month 55% ") + barWidth

	// Room for the whole left side but not the time: the time goes, the
	// left side is untouched.
	noTime := left + 1 + pctSeg
	plain, _ := line1(t, gaugeFooter(noTime, g))
	if strings.Contains(plain, "12d") || !strings.Contains(plain, "month 55%") || strings.Contains(plain, "…") {
		t.Fatalf("width %d = %q, want the time dropped, the percentage and the whole left side kept", noTime, plain)
	}

	// Narrower: the cwd side truncates, the percentage stays.
	cut := minBudgetLeft + 1 + pctSeg
	plain, _ = line1(t, gaugeFooter(cut, g))
	if !strings.Contains(plain, "month 55%") || !strings.Contains(plain, "…") || !strings.HasPrefix(strings.TrimSpace(plain), "agent") {
		t.Fatalf("width %d = %q, want the left truncated with the mode chip and the percentage kept", cut, plain)
	}

	// Too narrow for minBudgetLeft plus the percentage: the percentage goes,
	// the scope and the bar stay.
	plain, _ = line1(t, gaugeFooter(cut-2, g))
	if strings.Contains(plain, "%") || !strings.Contains(plain, "month ") || lipgloss.Width(plain) > cut-2 {
		t.Fatalf("width %d = %q, want the percentage dropped and the gauge kept", cut-2, plain)
	}
}

// fillBar is shared with the context bar: whole blocks, one eighth partial,
// then trough, always exactly width cells, clamped at both ends.
func TestFillBar(t *testing.T) {
	for _, tc := range []struct {
		frac        float64
		fill, total int
	}{{0, 0, 10}, {1, 10, 10}, {1.7, 10, 10}, {-1, 0, 10}, {0.5, 5, 10}, {0.55, 6, 10}} {
		f, tr := fillBar(tc.frac, 10)
		if got := len([]rune(f)); got != tc.fill {
			t.Errorf("frac %v fill = %d cells (%q), want %d", tc.frac, got, f, tc.fill)
		}
		if got := len([]rune(f + tr)); got != tc.total {
			t.Errorf("frac %v width = %d, want %d", tc.frac, got, tc.total)
		}
	}
}
