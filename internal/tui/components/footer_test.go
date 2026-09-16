package components

import (
	"strings"
	"testing"
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
	if !strings.Contains(f.contextSegment(), "(?)") {
		t.Fatalf("unknown window = %q", f.contextSegment())
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
	if got := f.sessionSegment(); got != "session: My Session" {
		t.Fatalf("session segment = %q", got)
	}
	f.ShowName = false
	if got := f.sessionSegment(); got != "session: abcd1234" {
		t.Fatalf("session segment = %q", got)
	}
	f.ShowName = true
	f.SessionName = ""
	if got := f.sessionSegment(); got != "session: abcd1234" {
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
	if !strings.Contains(bar, "░") {
		t.Fatalf("unknown window should render empty placeholders, got %q", bar)
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
