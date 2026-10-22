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

// TestNextIsStrictlyAfter confirms Next gives a minute strictly after t.
func TestNextIsStrictlyAfter(t *testing.T) {
	base := time.Date(2026, 9, 28, 10, 7, 0, 0, time.UTC)
	s, err := Parse("*/5 * * * *")
	if err != nil {
		t.Fatal(err)
	}
	got := s.Next(base)
	if !got.After(base) {
		t.Fatalf("Next(%v) = %v, want strictly after", base, got)
	}
}

// TestNextScheduleMatching confirms parsed schedules produce a matching time.
func TestNextScheduleMatching(t *testing.T) {
	for _, expr := range []string{"* * * * *", "0 0 * * *", "*/15 * * * *", "0 9 * * 1-5", "@daily", "@weekly", "@monthly"} {
		expr := expr
		t.Run(expr, func(t *testing.T) {
			s, err := Parse(expr)
			if err != nil {
				t.Fatal(err)
			}
			base := time.Date(2026, 9, 28, 10, 7, 30, 0, time.UTC)
			next := s.Next(base)
			if next.IsZero() {
				t.Fatal("Next returned zero")
			}
			if !s.minuteMatches(next.Minute()) {
				t.Errorf("minute %d does not match", next.Minute())
			}
			if !s.hourMatches(next.Hour()) {
				t.Errorf("hour %d does not match", next.Hour())
			}
			if !s.dayMatches(next) {
				t.Errorf("day does not match")
			}
			if !s.monthMatches(int(next.Month())) {
				t.Errorf("month %d does not match", next.Month())
			}
		})
	}
}

func (s Schedule) minuteMatches(v int) bool { return has(s.minute, v) }
func (s Schedule) hourMatches(v int) bool   { return has(s.hour, v) }
func (s Schedule) monthMatches(v int) bool  { return has(s.month, v) }
