// Package cron parses standard five-field cron expressions — minute, hour,
// day of month, month, day of week — and finds the next time one matches.
// It exists for fleet workers' schedules, so it is deliberately small: no
// seconds field, no names (JAN, MON), no @macros beyond @hourly, @daily,
// @weekly and @monthly.
package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a parsed expression. Each field is a bitset of allowed values.
type Schedule struct {
	minute, hour, dom, month, dow uint64
	// domStar and dowStar record a field written as "*": cron's rule is that
	// when both day fields are restricted, a day matching either one counts.
	domStar, dowStar bool
	expr             string
}

// String returns the expression it was parsed from.
func (s Schedule) String() string { return s.expr }

var macros = map[string]string{
	"@hourly":  "0 * * * *",
	"@daily":   "0 0 * * *",
	"@weekly":  "0 0 * * 0",
	"@monthly": "0 0 1 * *",
}

// Parse parses a five-field expression.
func Parse(expr string) (Schedule, error) {
	expr = strings.TrimSpace(expr)
	src := expr
	if m, ok := macros[strings.ToLower(expr)]; ok {
		expr = m
	}
	f := strings.Fields(expr)
	if len(f) != 5 {
		return Schedule{}, fmt.Errorf("cron: %q has %d fields, want 5 (minute hour day-of-month month day-of-week)", src, len(f))
	}
	s := Schedule{expr: src, domStar: f[2] == "*", dowStar: f[4] == "*"}
	var err error
	if s.minute, err = field(f[0], 0, 59); err != nil {
		return Schedule{}, fmt.Errorf("cron minute: %w", err)
	}
	if s.hour, err = field(f[1], 0, 23); err != nil {
		return Schedule{}, fmt.Errorf("cron hour: %w", err)
	}
	if s.dom, err = field(f[2], 1, 31); err != nil {
		return Schedule{}, fmt.Errorf("cron day of month: %w", err)
	}
	if s.month, err = field(f[3], 1, 12); err != nil {
		return Schedule{}, fmt.Errorf("cron month: %w", err)
	}
	if s.dow, err = field(f[4], 0, 7); err != nil {
		return Schedule{}, fmt.Errorf("cron day of week: %w", err)
	}
	if s.dow&(1<<7) != 0 { // 7 is Sunday too
		s.dow |= 1
	}
	return s, nil
}

func field(spec string, lo, hi int) (uint64, error) {
	var bits uint64
	for _, part := range strings.Split(spec, ",") {
		step := 1
		if base, st, ok := strings.Cut(part, "/"); ok {
			n, err := strconv.Atoi(st)
			if err != nil || n <= 0 {
				return 0, fmt.Errorf("bad step %q", part)
			}
			step, part = n, base
		}
		from, to := lo, hi
		switch {
		case part == "*":
		case strings.Contains(part, "-"):
			a, b, _ := strings.Cut(part, "-")
			x, err1 := strconv.Atoi(a)
			y, err2 := strconv.Atoi(b)
			if err1 != nil || err2 != nil || x > y {
				return 0, fmt.Errorf("bad range %q", part)
			}
			from, to = x, y
		default:
			n, err := strconv.Atoi(part)
			if err != nil {
				return 0, fmt.Errorf("bad value %q", part)
			}
			from, to = n, n
			if step > 1 { // "5/15" means from 5 to the end, every 15
				to = hi
			}
		}
		if from < lo || to > hi {
			return 0, fmt.Errorf("%q is outside %d-%d", part, lo, hi)
		}
		for v := from; v <= to; v += step {
			bits |= 1 << uint(v)
		}
	}
	return bits, nil
}

func has(bits uint64, v int) bool { return bits&(1<<uint(v)) != 0 }

func (s Schedule) dayMatches(t time.Time) bool {
	d, w := has(s.dom, t.Day()), has(s.dow, int(t.Weekday()))
	switch {
	case s.domStar && s.dowStar:
		return true
	case s.domStar:
		return w
	case s.dowStar:
		return d
	}
	return d || w
}

// Next returns the first matching minute strictly after t, in t's location.
// It gives up after four years (an expression like "0 0 30 2 *" never
// matches) and returns the zero time.
func (s Schedule) Next(t time.Time) time.Time {
	t = t.Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(4, 0, 0)
	for t.Before(limit) {
		if !has(s.month, int(t.Month())) {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, t.Location())
			continue
		}
		if !s.dayMatches(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
			continue
		}
		if !has(s.hour, t.Hour()) {
			t = t.Truncate(time.Hour).Add(time.Hour)
			continue
		}
		if !has(s.minute, t.Minute()) {
			t = t.Add(time.Minute)
			continue
		}
		return t
	}
	return time.Time{}
}
