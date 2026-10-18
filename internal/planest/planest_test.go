package planest

import (
	"strings"
	"testing"
)

func TestCompute(t *testing.T) {
	plan := strings.Repeat("a", 400) // 400 runes + 16 overhead → 104 tokens
	e := Compute(plan, 1000, 5000)
	if e.Plan != 104 {
		t.Fatalf("Plan = %d, want 104", e.Plan)
	}
	exec := (len(ExecutePrompt) + 16 + 3) / 4
	if e.New != 1000+104+exec {
		t.Errorf("New = %d, want %d", e.New, 1000+104+exec)
	}
	if e.Here != 5000+104+exec {
		t.Errorf("Here = %d, want %d", e.Here, 5000+104+exec)
	}
}

func TestComputeUnknownHistory(t *testing.T) {
	e := Compute("plan", 0, 0)
	if e.Here != 0 {
		t.Errorf("Here = %d, want 0 when the history is unknown", e.Here)
	}
	if e.New != e.Plan+(len(ExecutePrompt)+16+3)/4 {
		t.Errorf("New = %d, want plan + execute prompt only", e.New)
	}
	if e := Compute("plan", -5, -1); e.Base != 0 || e.Here != 0 {
		t.Errorf("negative inputs leaked: %+v", e)
	}
}

func TestShare(t *testing.T) {
	cases := []struct {
		tokens, window, pct int
		fits                bool
	}{
		{0, 1000, 0, true},
		{100, 0, 0, true},
		{1, 1000, 0, true},
		{500, 1000, 50, true},
		{1000, 1000, 100, true},
		{1500, 1000, 150, false},
		{999, 1000, 99, true},
	}
	for _, c := range cases {
		pct, fits := Share(c.tokens, c.window)
		if pct != c.pct || fits != c.fits {
			t.Errorf("Share(%d, %d) = %d, %v; want %d, %v", c.tokens, c.window, pct, fits, c.pct, c.fits)
		}
	}
}
