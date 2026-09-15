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
	f := Footer{Session: "abcd1234", Tokens: 42, Cost: "$0.00", Model: "gpt-5", Provider: "openai", Mode: "agent", Width: 80, Cwd: "/tmp", Branch: "main"}
	v := f.View()
	lines := strings.Split(strings.TrimSpace(v), "\n")
	if len(lines) < 2 {
		t.Fatalf("footer should be two lines: %q", v)
	}
}
