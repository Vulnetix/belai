package components

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestContextSegmentAnchoredFresh(t *testing.T) {
	f := Footer{Tokens: 12400, ContextLimit: 200000, Width: 120}
	s := f.contextSegment()
	if !strings.Contains(s, "12.4k/200k") {
		t.Fatalf("context = %q", s)
	}
	if !strings.Contains(s, "(93%)") {
		t.Fatalf("expected coloured percent, got %q", s)
	}
}

func TestContextSegmentEstimated(t *testing.T) {
	f := Footer{Tokens: 12400, ContextLimit: 200000, Estimated: true, Width: 120}
	if !strings.Contains(f.contextSegment(), "~12.4k/200k") {
		t.Fatalf("estimated context = %q", f.contextSegment())
	}
}

func TestContextSegmentStale(t *testing.T) {
	f := Footer{Tokens: 12400, ContextLimit: 200000, ContextStale: true, Width: 120}
	s := f.contextSegment()
	if !strings.Contains(s, "(?)") {
		t.Fatalf("stale context = %q", s)
	}
	if strings.Contains(s, "%") {
		t.Fatalf("stale context must not show a percent: %q", s)
	}
}

func TestContextSegmentUnknownWindow(t *testing.T) {
	f := Footer{Tokens: 12400, Estimated: true, Width: 120}
	if !strings.Contains(f.contextSegment(), "/ unknown") {
		t.Fatalf("unknown window = %q, want an explicit unknown denominator", f.contextSegment())
	}
}

func TestFormatTokens(t *testing.T) {
	cases := map[int]string{842: "842", 12400: "12.4k", 1_200_000: "1.2M", 9999: "9999", 10000: "10k"}
	for n, want := range cases {
		if got := formatTokens(n); got != want {
			t.Fatalf("formatTokens(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestSessionSegmentNameVsID(t *testing.T) {
	f := Footer{Session: "abcd1234", SessionName: "My Session", ShowName: true}
	if got := f.sessionSegment(40); got != "session: My Session" {
		t.Fatalf("session segment = %q", got)
	}
	f.ShowName = false
	if got := f.sessionSegment(40); got != "session: abcd1234" {
		t.Fatalf("session segment = %q", got)
	}
	f.ShowName = true
	f.SessionName = ""
	if got := f.sessionSegment(40); got != "session: abcd1234" {
		t.Fatalf("session segment = %q", got)
	}
}

func TestFooterRendersTwoLines(t *testing.T) {
	f := Footer{Session: "abcd1234", Tokens: 42, Model: "gpt-5", Provider: "openai", Mode: "agent", Width: 80, Cwd: "/tmp", Branch: "main"}
	v := f.View()
	lines := strings.Split(strings.TrimSpace(v), "\n")
	if len(lines) < 2 {
		t.Fatalf("footer should be two lines: %q", v)
	}
}

// The caveman slot is the only standing statement of whether replies are being
// rewritten, so it renders in both states and never collapses away.
func TestFooterCavemanIndicator(t *testing.T) {
	t.Run("enabled", func(t *testing.T) {
		f := Footer{Model: "gpt-5", Provider: "openai", Mode: "agent", Guardrails: true, Ask: true, Caveman: true, Width: 120}
		if v := f.View(); !strings.Contains(v, "caveman: on") {
			t.Fatalf("footer must render caveman: on, got %q", v)
		}
	})
	t.Run("disabled", func(t *testing.T) {
		f := Footer{Model: "gpt-5", Provider: "openai", Mode: "agent", Guardrails: true, Ask: true, Width: 120}
		if v := f.View(); !strings.Contains(v, "caveman: off") {
			t.Fatalf("footer must render caveman: off, got %q", v)
		}
	})
	t.Run("renders under YOLO too", func(t *testing.T) {
		// Both gates off collapses the permission chips into one YOLO chip;
		// the caveman slot is independent of that collapse.
		f := Footer{Model: "gpt-5", Provider: "openai", Mode: "agent", Caveman: true, Width: 120}
		v := f.View()
		if !strings.Contains(v, "YOLO") {
			t.Fatalf("both gates off must collapse to YOLO, got %q", v)
		}
		if !strings.Contains(v, "caveman: on") {
			t.Fatalf("caveman slot must survive the YOLO collapse, got %q", v)
		}
	})
	t.Run("zero-value footer still states it", func(t *testing.T) {
		// A footer with nothing configured yet must not read as silence on a
		// setting that changes every reply.
		var f Footer
		if v := f.View(); !strings.Contains(v, "caveman: off") {
			t.Fatalf("zero footer must render caveman: off, got %q", v)
		}
	})
}

// The permission chips and the caveman slot are separate rules: the chips
// collapse when both gates are off, the caveman slot never collapses.
func TestFooterPermissionChips(t *testing.T) {
	cases := []struct {
		guardrails, ask bool
		want, absent    []string
	}{
		{true, true, []string{"guardrails: on", "ask: on"}, []string{"YOLO"}},
		{true, false, []string{"guardrails: on", "ask: off"}, []string{"YOLO"}},
		{false, true, []string{"guardrails: off", "ask: on"}, []string{"YOLO"}},
		{false, false, []string{"YOLO"}, []string{"guardrails:", "ask:"}},
	}
	for _, tc := range cases {
		f := Footer{Guardrails: tc.guardrails, Ask: tc.ask, Width: 120}
		got := f.permissionChips()
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("guardrails=%v ask=%v: chips %q missing %q", tc.guardrails, tc.ask, got, w)
			}
		}
		for _, a := range tc.absent {
			if strings.Contains(got, a) {
				t.Errorf("guardrails=%v ask=%v: chips %q must not contain %q", tc.guardrails, tc.ask, got, a)
			}
		}
	}
}

func TestContextBarFilledAndEmpty(t *testing.T) {
	// 50% usage: 5 of 10 cells filled.
	f := Footer{Tokens: 50000, ContextLimit: 100000}
	bar := f.contextBar()
	filled := strings.Count(bar, "█")
	empty := strings.Count(bar, "░")
	if filled != 5 {
		t.Fatalf("expected 5 filled cells at 50%%, got %d in %q", filled, bar)
	}
	if filled+empty != barWidth {
		t.Fatalf("bar should be %d cells, got %d in %q", barWidth, filled+empty, bar)
	}
}

func TestContextBarEmptyWhenUnknown(t *testing.T) {
	f := Footer{Tokens: 50000, ContextLimit: 0}
	bar := f.contextBar()
	if strings.Contains(bar, "█") || strings.Contains(bar, "▏") {
		t.Fatalf("unknown window should render empty bar, got %q", bar)
	}
	if !strings.Contains(bar, "·") {
		t.Fatalf("unknown window should render a dotted trough, got %q", bar)
	}
}

func TestContextBarEmptyWhenStale(t *testing.T) {
	f := Footer{Tokens: 50000, ContextLimit: 100000, ContextStale: true}
	bar := f.contextBar()
	if strings.Contains(bar, "█") {
		t.Fatalf("stale context should render empty bar, got %q", bar)
	}
}

func TestBarColourThresholds(t *testing.T) {
	f := Footer{}
	if f.barColour(80) != ColorTeal {
		t.Fatal("80% remaining should be teal")
	}
	if f.barColour(40) != ColorAmber {
		t.Fatal("40% remaining should be amber")
	}
	if f.barColour(10) != ColorDanger {
		t.Fatal("10% remaining should be red")
	}
}

// TestContextBarEighthResolution pins the fill geometry: a 10-cell bar at
// eighth-cell resolution, so partial cells use the ▏..▉ glyphs and a fraction
// over 7/8 carries into the next cell.
func TestContextBarEighthResolution(t *testing.T) {
	dots := strings.Repeat("░", barWidth)
	cases := []struct {
		name string
		f    Footer
		want string
	}{
		{"zero", Footer{Tokens: 0, ContextLimit: 1000}, dots},
		{"half of first cell", Footer{Tokens: 400, ContextLimit: 8000}, "▌" + strings.Repeat("░", barWidth-1)},
		{"eighth cell", Footer{Tokens: 1000, ContextLimit: 8000}, "█▎" + strings.Repeat("░", barWidth-2)},
		{"quarter", Footer{Tokens: 1000, ContextLimit: 4000}, "██▌" + strings.Repeat("░", barWidth-3)},
		{"half exact", Footer{Tokens: 500, ContextLimit: 1000}, strings.Repeat("█", 5) + strings.Repeat("░", 5)},
		{"half plus eighth", Footer{Tokens: 510, ContextLimit: 1000}, strings.Repeat("█", 5) + "▏" + strings.Repeat("░", 4)},
		{"half cell boundary", Footer{Tokens: 550, ContextLimit: 1000}, strings.Repeat("█", 5) + "▌" + strings.Repeat("░", 4)},
		{"carry into next cell", Footer{Tokens: 595, ContextLimit: 1000}, strings.Repeat("█", 6) + strings.Repeat("░", 4)},
		{"small fraction", Footer{Tokens: 12400, ContextLimit: 200000}, "▋" + strings.Repeat("░", barWidth-1)},
		{"exact full", Footer{Tokens: 1000, ContextLimit: 1000}, strings.Repeat("█", barWidth)},
		{"over full clamps to full", Footer{Tokens: 1500, ContextLimit: 1000}, strings.Repeat("█", barWidth)},
		{"negative tokens clamp to zero", Footer{Tokens: -5, ContextLimit: 1000}, dots},
		{"estimated still fills", Footer{Tokens: 900, ContextLimit: 1000, Estimated: true}, strings.Repeat("█", 9) + "░"},
		{"stale renders empty despite usage", Footer{Tokens: 900, ContextLimit: 1000, ContextStale: true}, dots},
		{"unknown window renders dotted", Footer{Tokens: 900}, strings.Repeat("·", barWidth)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.f.contextBar()
			if len([]rune(got)) != barWidth {
				t.Fatalf("bar = %q has %d cells, want %d", got, len([]rune(got)), barWidth)
			}
			if got != tc.want {
				t.Fatalf("bar = %q, want %q", got, tc.want)
			}
		})
	}
}

// The bar and the text percentage must use one colour rule. Every fresh
// anchored footer renders its bar with the same colour the percentage uses.
func TestBarAndPercentageShareColourRule(t *testing.T) {
	cases := map[int]lipgloss.TerminalColor{
		0:   ColorDanger, // window full
		19:  ColorDanger,
		20:  ColorAmber,
		49:  ColorAmber,
		50:  ColorTeal,
		100: ColorTeal,
	}
	for pct, want := range cases {
		// 1% of a 1000-token window is 10 tokens.
		f := Footer{Tokens: 1000 - pct*10, ContextLimit: 1000}
		got, ok := f.percentRemaining()
		if !ok {
			t.Fatalf("percentRemaining(pct=%d) = !ok", pct)
		}
		if got != pct {
			t.Fatalf("percentRemaining(pct=%d) = %d", pct, got)
		}
		if c := f.barColour(got); c != want {
			t.Fatalf("barColour(%d) = %v, want %v", pct, c, want)
		}
	}
}

// TestFooterHasNoCostLabel pins the removal of the cost element: no "cost"
// text may appear anywhere in the rendered footer.
func TestFooterHasNoCostLabel(t *testing.T) {
	f := Footer{Session: "abcd1234", SessionName: "s", ShowName: true, Tokens: 12400, ContextLimit: 200000, Model: "gpt-5", Provider: "openai", Mode: "agent", Width: 140, Cwd: "/tmp", Branch: "main"}
	v := f.View()
	if strings.Contains(v, "cost") {
		t.Fatalf("footer must not render a cost element: %q", v)
	}
}

// TestFooterEffortRendering pins the subtle effort display: effort renders
// muted, directly next to the model id, and only when both a model and an
// effort value are present.
func TestFooterEffortRendering(t *testing.T) {
	t.Run("effort shown next to model", func(t *testing.T) {
		f := Footer{Model: "gpt-5", Effort: "high", Provider: "openai", Mode: "agent", Width: 100}
		v := f.View()
		if !strings.Contains(v, "gpt-5 · high") {
			t.Fatalf("effort should sit next to the model: %q", v)
		}
	})
	t.Run("none is an explicit value and shows", func(t *testing.T) {
		f := Footer{Model: "gpt-5", Effort: "none", Mode: "agent", Width: 100}
		v := f.View()
		if !strings.Contains(v, "gpt-5 · none") {
			t.Fatalf("explicit none should render: %q", v)
		}
	})
	t.Run("empty effort renders nothing", func(t *testing.T) {
		f := Footer{Model: "gpt-5", Effort: "", Mode: "agent", Guardrails: true, Ask: true, Width: 100}
		v := f.View()
		lines := strings.Split(v, "\n")
		line2 := lines[len(lines)-1]
		if strings.Contains(line2, "gpt-5 · high") || strings.Contains(line2, "gpt-5 · none") {
			t.Fatalf("empty effort must not render an effort value: %q", line2)
		}
		if !strings.Contains(line2, "guardrails: on") {
			t.Fatalf("permission chips should render when effort is empty: %q", line2)
		}
	})
	t.Run("effort without a model renders nothing", func(t *testing.T) {
		f := Footer{Model: "", Effort: "high", Provider: "openai", Mode: "agent", Width: 100}
		v := f.View()
		if strings.Contains(v, "high") {
			t.Fatalf("effort must not render without a model: %q", v)
		}
	})
}
