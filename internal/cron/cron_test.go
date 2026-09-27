package cron

import (
	"testing"
	"time"
)

func TestNext(t *testing.T) {
	base := time.Date(2026, 9, 28, 10, 7, 30, 0, time.UTC) // a Monday
	for expr, want := range map[string]time.Time{
		"*/15 * * * *":  time.Date(2026, 9, 28, 10, 15, 0, 0, time.UTC),
		"0 9 * * 1-5":   time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC),
		"@hourly":       time.Date(2026, 9, 28, 11, 0, 0, 0, time.UTC),
		"30 2 1 * *":    time.Date(2026, 10, 1, 2, 30, 0, 0, time.UTC),
		"0 0 * * 7":     time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC),
		"5/20 10 * * *": time.Date(2026, 9, 28, 10, 25, 0, 0, time.UTC),
		"0 12 15 * 3":   time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC), // Wednesday, before the 15th
	} {
		s, err := Parse(expr)
		if err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		if got := s.Next(base); !got.Equal(want) {
			t.Errorf("%s: next %v, want %v", expr, got, want)
		}
	}
	if s, _ := Parse("0 0 30 2 *"); !s.Next(base).IsZero() {
		t.Error("Feb 30 matched")
	}
}

func TestParseErrors(t *testing.T) {
	for _, bad := range []string{"", "* * * *", "60 * * * *", "* 24 * * *", "*/0 * * * *", "5-1 * * * *", "a * * * *", "* * 0 * *"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}
