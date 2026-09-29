package components

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

func intelFooter(width int, g *IntelGauge) Footer {
	return Footer{Mode: "agent", Cwd: "/home/user/project", Branch: "main", Width: width, Intel: g}
}

func limitGauge() *IntelGauge {
	return &IntelGauge{Today: "34.4M", Limit: "5h 23%", LimitFrac: 0.23, ElapsedFrac: 0.55, Spark: make([]int64, 24), Pace: "comfortable", State: BudgetTeal}
}

func sparkGauge() *IntelGauge {
	spark := make([]int64, 24)
	spark[3], spark[10], spark[20] = 5, 40, 12
	return &IntelGauge{Today: "1.2M", LimitFrac: -1, ElapsedFrac: -1, Spark: spark, Pace: "idle", State: BudgetTeal}
}

// R16: the intel slot is right-aligned on line 1 at three detail levels: the
// label, today's tokens, the limit (or the pace) and the picture.
func TestBudgetRule16_IntelSegmentThreeDetailLevels(t *testing.T) {
	g := limitGauge()
	plain, _ := line1(t, intelFooter(100, g))
	if lipgloss.Width(plain) != 100 {
		t.Fatalf("line 1 width = %d, want the full 100: %q", lipgloss.Width(plain), plain)
	}
	if !strings.Contains(plain, "intel  today 34.4M · 5h 23% ") || !strings.Contains(plain, "╹") {
		t.Fatalf("line 1 = %q, want the limit with its reset marker", plain)
	}
	if !strings.HasPrefix(strings.TrimSpace(plain), "agent") {
		t.Fatalf("line 1 = %q, want the mode still on the left", plain)
	}

	for detail, want := range map[int]string{2: "intel  today 34.4M · 5h 23% ", 1: "intel  today 34.4M ", 0: "intel "} {
		seg := ansi.Strip(g.segmentAt(detail, false))
		if !strings.HasPrefix(seg, want) {
			t.Fatalf("detail %d = %q, want prefix %q", detail, seg, want)
		}
	}
	if seg := ansi.Strip(g.segmentAt(1, false)); strings.Contains(seg, "5h") {
		t.Fatalf("detail 1 = %q, want the limit dropped", seg)
	}

	// Without a limit the pace stands in beside a 24-hour sparkline.
	s := sparkGauge()
	seg := ansi.Strip(s.segmentAt(2, false))
	if !strings.HasPrefix(seg, "intel  today 1.2M · idle ") || !strings.ContainsAny(seg, "▁▂▃▄▅▆▇█") || strings.Contains(seg, "╹") {
		t.Fatalf("sparkline segment = %q", seg)
	}
	// With nothing recorded the picture is a dotted trough.
	empty := &IntelGauge{Today: "0", LimitFrac: -1, ElapsedFrac: -1, Spark: make([]int64, 24), Pace: "idle"}
	if seg := ansi.Strip(empty.segmentAt(0, false)); seg != "intel "+strings.Repeat("·", barWidth) {
		t.Fatalf("empty segment = %q", seg)
	}
}

// R16: every picture is exactly one bar wide, so the slot never changes width
// with the data.
func TestBudgetRule16_IntelPicturesAreBarWide(t *testing.T) {
	for name, g := range map[string]*IntelGauge{
		"limit": limitGauge(), "spark": sparkGauge(),
		"empty": {LimitFrac: -1, ElapsedFrac: -1, Spark: make([]int64, 24)},
		"spent": {Limit: "5h 100%", LimitFrac: 1, ElapsedFrac: 0.4, State: BudgetRed},
	} {
		if w := lipgloss.Width(g.Bar()); w != barWidth {
			t.Fatalf("%s picture is %d cells, want %d", name, w, barWidth)
		}
	}
}

// R16: the slot takes the state colour like the budget gauge: teal, amber, red.
func TestBudgetRule16_IntelColoursByState(t *testing.T) {
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(old)

	for state, want := range map[BudgetState]lipgloss.TerminalColor{BudgetTeal: ColorTeal, BudgetAmber: ColorAmber, BudgetRed: ColorDanger} {
		g := limitGauge()
		g.State = state
		if g.Colour() != want {
			t.Fatalf("state %d colour = %v, want %v", state, g.Colour(), want)
		}
		seq := lipgloss.NewStyle().Foreground(want).Render("x")
		prefix := seq[:strings.Index(seq, "x")]
		if seg := g.segmentAt(2, false); !strings.HasPrefix(seg, prefix) {
			t.Fatalf("state %d segment = %q, want the state colour", state, seg)
		}
		if !strings.Contains(g.Bar(), prefix) {
			t.Fatalf("state %d bar = %q, want the fill in the state colour", state, g.Bar())
		}
	}
}

// R17: the shortcut hint rides on the roomiest rendering only when the whole line
// still fits with it. When it would not fit it is dropped and nothing else is:
// the left side and every figure keep their room.
func TestBudgetRule17_HintShowsOnlyWhenTheLineFits(t *testing.T) {
	g := limitGauge()
	g.Hint = true
	left := ansi.Strip((&Footer{Mode: "agent", Cwd: "/home/user/project", Branch: "main", Width: 200}).View())
	leftW := lipgloss.Width(strings.TrimRight(strings.Split(left, "\n")[0], " "))
	segW := lipgloss.Width(ansi.Strip(g.segmentAt(2, false)))

	exact := leftW + 1 + segW // the segment fits with no room to spare
	plain, _ := line1(t, intelFooter(exact, g))
	if strings.Contains(plain, intelHintKey) {
		t.Fatalf("width %d: line 1 = %q, want no hint when it does not fit", exact, plain)
	}
	if !strings.Contains(plain, "5h 23%") || !strings.Contains(plain, "today 34.4M") || strings.Contains(plain, "…") {
		t.Fatalf("width %d: line 1 = %q, want every figure and no truncation", exact, plain)
	}

	roomy := exact + len("  "+intelHintKey)
	plain, _ = line1(t, intelFooter(roomy, g))
	if !strings.HasSuffix(plain, "  "+intelHintKey) || lipgloss.Width(plain) != roomy {
		t.Fatalf("width %d: line 1 = %q, want the hint flush right", roomy, plain)
	}

	g.Hint = false
	plain, _ = line1(t, intelFooter(200, g))
	if strings.Contains(plain, intelHintKey) {
		t.Fatalf("line 1 = %q, want no hint when the cycle is not in its last third", plain)
	}
}

// E25: a narrow terminal sheds the intel slot's detail first, then the left side,
// but the label and the picture stay and the line never runs past the width.
func TestBudgetEdge25_NarrowTerminalShedsIntelDetailFirst(t *testing.T) {
	g := limitGauge()
	g.Hint = true
	full, _ := line1(t, intelFooter(120, g))
	if !strings.Contains(full, "5h 23%") {
		t.Fatalf("120 columns: %q", full)
	}
	for _, w := range []int{60, 44, 36, 30} {
		plain, _ := line1(t, intelFooter(w, g))
		if lipgloss.Width(plain) > w {
			t.Fatalf("width %d: line 1 is %d cells: %q", w, lipgloss.Width(plain), plain)
		}
		if !strings.Contains(plain, "intel") || !strings.Contains(plain, "╹") && !strings.ContainsAny(plain, "█▏▎▍▌▋▊▉░") {
			t.Fatalf("width %d: line 1 = %q, want the label and the picture kept", w, plain)
		}
		if strings.Contains(plain, intelHintKey) && w < 60 {
			t.Fatalf("width %d: the hint survived on a tight line: %q", w, plain)
		}
	}
	plain, _ := line1(t, intelFooter(44, g))
	if strings.Contains(plain, "5h 23%") {
		t.Fatalf("width 44: line 1 = %q, want the limit text shed", plain)
	}
	if !strings.HasPrefix(strings.TrimSpace(plain), "agent") {
		t.Fatalf("width 44: line 1 = %q, want the mode kept", plain)
	}
}

// R15: the budget gauge and the intel slot never draw together; a budget wins
// the line if both are set.
func TestBudgetRule15_OneGaugeOnLineOne(t *testing.T) {
	f := gaugeFooter(120, &BudgetGauge{Scope: "day", TokenPct: 50, UsedFrac: 0.5, State: BudgetTeal})
	f.Intel = limitGauge()
	plain, _ := line1(t, f)
	if strings.Contains(plain, "intel") || !strings.Contains(plain, "day 50%") {
		t.Fatalf("line 1 = %q, want only the budget gauge", plain)
	}
}

func TestSparklineScalesAndGroups(t *testing.T) {
	plain := func(s string) string { return ansi.Strip(s) }
	id := lipgloss.NewStyle()
	if got := plain(Sparkline([]int64{0, 1, 2, 4, 8}, 5, id, id)); got != "▁▂▃▅█" && got != "▁▂▃▄█" {
		t.Fatalf("scaled = %q", got)
	}
	if got := plain(Sparkline([]int64{0, 0, 0}, 3, id, id)); got != "▁▁▁" {
		t.Fatalf("all zero = %q, want the floor", got)
	}
	// 24 hourly values group into 10 cells by summing.
	vals := make([]int64, 24)
	vals[0], vals[23] = 10, 100
	got := []rune(plain(Sparkline(vals, 10, id, id)))
	if len(got) != 10 || got[9] != '█' || got[0] == '▁' {
		t.Fatalf("grouped = %q, want ten cells, first non-empty, last full", string(got))
	}
	if Sparkline(vals, 0, id, id) != "" {
		t.Fatal("zero cells drew something")
	}
	if SparkTotal([]int64{1, 2, 3}) != 6 {
		t.Fatal("SparkTotal")
	}
}

func TestLimitBarMarker(t *testing.T) {
	col := ColorTeal
	plain := func(used, elapsed float64) string { return ansi.Strip(LimitBar(used, elapsed, 10, col)) }
	if got := plain(0.2, 0.55); []rune(got)[5] != '╹' || len([]rune(got)) != 10 {
		t.Fatalf("marker in the trough = %q, want ╹ at cell 5", got)
	}
	if got := plain(0.9, 0.3); strings.Contains(got, "╹") {
		t.Fatalf("marker inside the fill = %q, want it hidden", got)
	}
	if got := plain(0.2, -1); strings.Contains(got, "╹") {
		t.Fatalf("no window = %q, want no marker", got)
	}
	if got := plain(0.2, 1.5); []rune(got)[9] != '╹' {
		t.Fatalf("elapsed past the end = %q, want the marker in the last cell", got)
	}
	if LimitBar(0.5, 0.5, 0, col) != "" {
		t.Fatal("zero width drew something")
	}
}
