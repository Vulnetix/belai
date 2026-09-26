package budget

import (
	"testing"
	"time"
	_ "time/tzdata" // DST tests must not depend on the host's zoneinfo

	"github.com/vulnetix/belai/internal/config"
)

func day(tokens int64) config.TokenBudget {
	return config.TokenBudget{Provider: "p", Model: "m", Scope: config.BudgetScopeDay, Tokens: tokens}
}

func month(tokens int64) config.TokenBudget {
	return config.TokenBudget{Provider: "p", Model: "m", Scope: config.BudgetScopeMonth, Tokens: tokens}
}

func session(tokens int64) config.TokenBudget {
	return config.TokenBudget{Provider: "p", Model: "m", Scope: config.BudgetScopeSession, Tokens: tokens}
}

// R5: a day runs midnight to midnight and a month the 1st to the 1st, in the
// clock's own location; a session has no window.
func TestBudgetRule5_ScopeWindows(t *testing.T) {
	loc := time.FixedZone("AEST", 10*3600)
	now := time.Date(2026, 9, 25, 13, 30, 0, 0, loc)
	s, e := Window(config.BudgetScopeDay, now)
	if !s.Equal(time.Date(2026, 9, 25, 0, 0, 0, 0, loc)) || !e.Equal(time.Date(2026, 9, 26, 0, 0, 0, 0, loc)) {
		t.Fatalf("day window = %v – %v", s, e)
	}
	s, e = Window(config.BudgetScopeMonth, now)
	if !s.Equal(time.Date(2026, 9, 1, 0, 0, 0, 0, loc)) || !e.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, loc)) {
		t.Fatalf("month window = %v – %v", s, e)
	}
	if s, e := Window(config.BudgetScopeSession, now); !s.IsZero() || !e.IsZero() {
		t.Fatalf("session window = %v – %v, want none", s, e)
	}
	// Half the day gone at noon.
	noon := time.Date(2026, 9, 25, 12, 0, 0, 0, loc)
	if g := Status(day(100), 0, noon); g.TimePctLeft != 50 || g.TimeLeft != 12*time.Hour {
		t.Fatalf("noon gauge = %+v, want 50%% and 12h left", g)
	}
}

// R6: the token percentage left rounds up, so 0% means exhausted.
func TestBudgetRule6_TokenPercentRoundsUp(t *testing.T) {
	g := Status(session(1000), 996, time.Now())
	if g.TokenPctLeft != 1 || g.State == Red {
		t.Fatalf("996 of 1000 used = %d%% %v, want 1%% and not red", g.TokenPctLeft, g.State)
	}
	if g := Status(session(1000), 0, time.Now()); g.TokenPctLeft != 100 {
		t.Fatalf("unused = %d%%, want 100%%", g.TokenPctLeft)
	}
}

// R7: exhausted is red, and red outranks amber.
func TestBudgetRule7_RedWhenExhaustedOutranksAmber(t *testing.T) {
	loc := time.UTC
	early := time.Date(2026, 9, 25, 1, 0, 0, 0, loc) // most of the day left: would be amber
	g := Status(day(100), 100, early)
	if g.State != Red {
		t.Fatalf("exhausted early in the day = %v, want red", g.State)
	}
	if Worst([]Gauge{{State: Teal}, {State: Red}, {State: Amber}}) != Red {
		t.Fatal("Worst must rank red above amber and teal")
	}
	if Worst([]Gauge{{State: Teal}, {State: Amber}}) != Amber {
		t.Fatal("Worst must rank amber above teal")
	}
}

// R8: amber when a larger share of the window than of the tokens is left.
func TestBudgetRule8_AmberWhenSpendOutrunsTheClock(t *testing.T) {
	loc := time.UTC
	noon := time.Date(2026, 9, 25, 12, 0, 0, 0, loc) // 50% of the day left
	if g := Status(day(100), 60, noon); g.State != Amber {
		t.Fatalf("60%% used at noon = %v, want amber", g.State)
	}
	if g := Status(day(100), 40, noon); g.State != Teal {
		t.Fatalf("40%% used at noon = %v, want teal", g.State)
	}
	// Equal shares are not amber: the pace exactly lasts the window.
	if g := Status(day(100), 50, noon); g.State != Teal {
		t.Fatalf("50%% used at noon = %v, want teal", g.State)
	}
	// Exact fractions decide, not the rounded display: 49.6% of tokens left
	// (displayed 50%) against 50% of time left is amber.
	if g := Status(day(1000), 504, noon); g.State != Amber || g.TokenPctLeft != 50 || g.TimePctLeft != 50 {
		t.Fatalf("504/1000 at noon = %+v, want amber though both display 50%%", g)
	}
}

// R8: a session budget has no window, shows no time and is never amber.
func TestBudgetRule8_SessionNeverAmber(t *testing.T) {
	g := Status(session(100), 99, time.Now())
	if g.State != Teal || g.HasTime() || g.TimeLeft != 0 {
		t.Fatalf("session gauge = %+v, want teal with no time", g)
	}
}

// R10: the cycle follows the clock — each budget shows for one period, in turn.
func TestBudgetRule10_CycleFollowsTheClock(t *testing.T) {
	period := 10 * time.Second
	base := time.Unix(990_000_000, 0) // a multiple of 30 s: a cycle of three starts here
	for i, want := range []int{0, 0, 1, 1, 2, 2, 0} {
		now := base.Add(time.Duration(i) * 5 * time.Second)
		if got := CycleIndex(3, period, now); got != want {
			t.Fatalf("t+%ds index = %d, want %d", i*5, got, want)
		}
	}
	if CycleIndex(1, period, base.Add(time.Hour)) != 0 || CycleIndex(0, period, base) != 0 {
		t.Fatal("zero or one budget must always be index 0")
	}
}

// E1: usage exactly at the allowance is 0% and red.
func TestBudgetEdge1_ExactlyAtTheAllowance(t *testing.T) {
	g := Status(month(5000), 5000, time.Now())
	if g.TokenPctLeft != 0 || g.State != Red {
		t.Fatalf("at the allowance = %+v, want 0%% red", g)
	}
}

// E2: usage past the allowance clamps to 0% and stays red.
func TestBudgetEdge2_PastTheAllowanceClamps(t *testing.T) {
	g := Status(session(100), 250, time.Now())
	if g.TokenPctLeft != 0 || g.State != Red {
		t.Fatalf("past the allowance = %+v, want 0%% red", g)
	}
}

// E3: a daylight-saving day is 23 or 25 hours long.
func TestBudgetEdge3_DaylightSavingDayLength(t *testing.T) {
	syd, err := time.LoadLocation("Australia/Sydney")
	if err != nil {
		t.Fatal(err)
	}
	// Sydney: clocks go forward on 2026-10-04 (23 h) and back on 2026-04-05 (25 h).
	for date, want := range map[int]time.Duration{4: 23 * time.Hour} {
		s, e := Window(config.BudgetScopeDay, time.Date(2026, 10, date, 12, 0, 0, 0, syd))
		if got := e.Sub(s); got != want {
			t.Fatalf("2026-10-%02d length = %v, want %v", date, got, want)
		}
	}
	s, e := Window(config.BudgetScopeDay, time.Date(2026, 4, 5, 12, 0, 0, 0, syd))
	if got := e.Sub(s); got != 25*time.Hour {
		t.Fatalf("2026-04-05 length = %v, want 25h", got)
	}
	// Time left uses the real length: 1 h before the end of a 25 h day.
	late := e.Add(-time.Hour)
	if g := Status(day(100), 0, late); g.TimeLeft != time.Hour || g.TimePctLeft != 4 {
		t.Fatalf("last hour of a 25h day = %+v, want 1h and 4%%", g)
	}
}

// E4: the month window follows the calendar, leap day included.
func TestBudgetEdge4_MonthRolloverAndLeapDay(t *testing.T) {
	s, e := Window(config.BudgetScopeMonth, time.Date(2028, 2, 29, 23, 0, 0, 0, time.UTC))
	if e.Sub(s) != 29*24*time.Hour {
		t.Fatalf("February 2028 = %v, want 29 days", e.Sub(s))
	}
	s, _ = Window(config.BudgetScopeMonth, time.Date(2028, 3, 1, 0, 0, 0, 0, time.UTC))
	if s.Month() != time.March || s.Day() != 1 {
		t.Fatalf("1 March month starts %v", s)
	}
}

func TestStateString(t *testing.T) {
	if Teal.String() != "teal" || Amber.String() != "amber" || Red.String() != "red" {
		t.Fatal("state names changed; the switcher row prints them")
	}
}
