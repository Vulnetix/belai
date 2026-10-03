package budget

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/config"
)

// clock is a settable time source safe to read from the flusher goroutine.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

// openClock opens a recorder whose time the test moves.
func openClock(t *testing.T, path, session string, start time.Time) (*Recorder, *clock) {
	t.Helper()
	c := &clock{t: start}
	r, err := Open(path, session, 28)
	if err != nil {
		t.Fatal(err)
	}
	r.now = c.now
	t.Cleanup(func() { _ = r.Close() })
	return r, c
}

// setDay writes one day's total straight into the recorder's disk snapshot.
func setDay(r *Recorder, key string, at time.Time, tokens int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.disk.Days[key] == nil {
		r.disk.Days[key] = map[string]int64{}
	}
	r.disk.Days[key][at.Format(dayLayout)] = tokens
}

func at(day int, hour, min int) time.Time {
	return time.Date(2026, 9, day, hour, min, 0, 0, time.Local)
}

// R18: roll-ups (today, the last 7 days, the last 30) read day totals; calls,
// the sparkline and pace read hour buckets. Sessions count by the window their
// last activity falls in.
func TestBudgetRule18_RollupsReadDaysAndSparkReadsHours(t *testing.T) {
	path := filepath.Join(t.TempDir(), LedgerFile)
	r, c := openClock(t, path, "s1", at(24, 20, 0))
	r.AddCall("p", "m", "agent", 400) // yesterday 20:00
	c.set(at(25, 9, 10))
	r.AddCall("p", "m", "agent", 100)
	c.set(at(25, 13, 50))
	r.AddCall("q", "n", "security", 200)
	c.set(at(25, 14, 30))

	in := r.Intel(c.now(), "p", nil)
	today, week, month := in.Rollups[0], in.Rollups[1], in.Rollups[2]
	if today.Tokens != 300 || week.Tokens != 700 || month.Tokens != 700 {
		t.Fatalf("tokens today/week/30d = %d/%d/%d, want 300/700/700", today.Tokens, week.Tokens, month.Tokens)
	}
	if today.Calls != 2 || week.Calls != 3 {
		t.Fatalf("calls today/week = %d/%d, want 2/3 from hour buckets", today.Calls, week.Calls)
	}
	if len(today.ByModel) != 2 || today.ByModel[0].Key != "q/n" || today.ByModel[0].Tokens != 200 {
		t.Fatalf("by model = %+v, want q/n first with 200", today.ByModel)
	}
	// The sparkline ends at the current hour: 14:00 is 0, 13:00 is 200, 09:00
	// is 100 and yesterday 20:00 (18 hours back) is 400.
	if in.Spark[23] != 0 || in.Spark[22] != 200 || in.Spark[18] != 100 || in.Spark[5] != 400 {
		t.Fatalf("spark = %v", in.Spark)
	}
	if today.Sessions != 1 || week.Sessions != 1 {
		t.Fatalf("sessions today/week = %d/%d, want this session once", today.Sessions, week.Sessions)
	}

	// A window ends at now and a rollup never counts tomorrow's day key.
	setDay(r, "p/m", at(26, 0, 0), 999)
	if got := r.Intel(c.now(), "p", nil).Rollups[0].Tokens; got != 300 {
		t.Fatalf("today counted a future day: %d", got)
	}
}

// R18: a session counts in the window its last activity falls in, whether the
// ledger recorded it live or imported it from a transcript.
func TestBudgetRule18_SessionsCountByLastActivity(t *testing.T) {
	path := filepath.Join(t.TempDir(), LedgerFile)
	// The other sessions are on disk before the recorder opens: AddCall wakes
	// the flusher, which re-reads the file, so entries only in memory could
	// vanish before Intel reads them.
	seed := newLedgerData()
	seed.Version = ledgerVersion
	seed.Sessions["three-days-ago"] = &sessionEntry{Updated: at(22, 10, 0)}
	seed.Sessions["earlier-today"] = &sessionEntry{Updated: at(25, 8, 0)}
	seed.Imported["imported-week"] = "2026-09-20"
	seed.Imported["imported-old"] = "2026-08-01"
	buf, err := json.Marshal(seed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatal(err)
	}
	r, c := openClock(t, path, "now", at(25, 12, 0))
	r.AddCall("p", "m", "", 10) // this session, counted before or after its flush

	in := r.Intel(c.now(), "p", nil)
	got := [3]int{in.Rollups[0].Sessions, in.Rollups[1].Sessions, in.Rollups[2].Sessions}
	// today: earlier-today + this one. week (Sep 19-25): + three-days-ago +
	// imported-week. 30 days (Aug 27-Sep 25): imported-old (Aug 1) is outside.
	if got != [3]int{2, 4, 4} {
		t.Fatalf("sessions today/week/30d = %v, want [2 4 4]", got)
	}
}

// E23: an imported transcript knows only days. It counts in the roll-ups and
// its session is marked with the day it was last active, but it never appears
// in the hourly views: the heatmap row is flagged day-only and the sparkline
// and pace ignore it.
func TestBudgetEdge23_ImportedDaysHaveNoHourlyShape(t *testing.T) {
	home := t.TempDir()
	sessions := filepath.Join(home, "sessions")
	when := at(23, 10, 0)
	writeTranscript(t, sessions, "old", assistant(when, "p", "m", "agent", 500))

	path := filepath.Join(home, LedgerFile)
	r, c := openClock(t, path, "cur", at(25, 12, 0))
	if n, err := r.ImportHistory(sessions); err != nil || n != 1 {
		t.Fatalf("import = %d, %v", n, err)
	}
	if got := readFile(t, path).Imported["old"]; got != "2026-09-23" {
		t.Fatalf("imported marker = %q, want the last-activity day 2026-09-23", got)
	}
	in := r.Intel(c.now(), "p", nil)
	if in.Rollups[1].Tokens != 500 || in.Rollups[1].Sessions != 1 {
		t.Fatalf("week = %d tokens / %d sessions, want 500 / 1", in.Rollups[1].Tokens, in.Rollups[1].Sessions)
	}
	if in.Rollups[0].Tokens != 0 {
		t.Fatalf("today = %d, an earlier day leaked into today", in.Rollups[0].Tokens)
	}
	row := 4 // the seven rows run Sep 19 to Sep 25; Sep 23 is the fifth
	if in.HeatDays[row] != "2026-09-23" || !in.HeatDayOnly[row] || in.HeatDayTokens[row] != 500 {
		t.Fatalf("heat row %d = %s day-only=%v tokens=%d", row, in.HeatDays[row], in.HeatDayOnly[row], in.HeatDayTokens[row])
	}
	var sum int64
	for _, v := range in.Spark {
		sum += v
	}
	if sum != 0 || in.Pace.TokensPerHour != 0 {
		t.Fatalf("imported usage shaped the hourly views: spark sum %d, pace %v", sum, in.Pace.TokensPerHour)
	}
	if in.Rollups[1].Calls != 0 {
		t.Fatalf("imported usage counted %d calls", in.Rollups[1].Calls)
	}
}

// R20: pace is the trailing 60 minutes; against a plan window it is calibrated
// from the share of the window spent per token recorded since it opened, and
// runway compares the time to run out with the time to the reset.
func TestBudgetRule20_PaceAndRunwayAgainstAPlanWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), LedgerFile)
	r, c := openClock(t, path, "s", at(25, 13, 15))
	r.AddCall("p", "m", "agent", 600) // 13:00 bucket
	c.set(at(25, 14, 10))
	r.AddCall("p", "m", "agent", 1000) // 14:00 bucket
	now := at(25, 14, 30)              // halfway through the 14:00 hour
	c.set(now)

	limit := func(used float64) PlanLimit {
		return PlanLimit{Provider: "p", Window: WindowFiveHour, Used: used, Reset: now.Add(3 * time.Hour), ObservedAt: now}
	}
	measure := func(used float64) Intel {
		r.mu.Lock()
		r.pending.limits = map[string]PlanLimit{}
		r.disk.Limits = map[string]PlanLimit{}
		r.mu.Unlock()
		r.ObserveLimits([]PlanLimit{limit(used)})
		return r.Intel(now, "p", nil)
	}

	// pace = 1000 (this hour) + 600 × (1 − ½) (the last half of the last hour).
	in := measure(0.4)
	if in.Pace.TokensPerHour != 1300 {
		t.Fatalf("pace = %v tok/h, want 1300", in.Pace.TokensPerHour)
	}
	// Window opened 09:30 and holds 1600 tokens: 0.4 / 1600 per token, so 1300
	// tok/h spends 32.5 % of the window per hour and runs out in 0.6/0.325 h.
	if math.Abs(in.Pace.PctPerHour-32.5) > 0.01 {
		t.Fatalf("pace %%/h = %v, want 32.5", in.Pace.PctPerHour)
	}
	if in.Runway.Kind != RunwayLimit || in.Runway.Lasts {
		t.Fatalf("runway = %+v, want a limit runway that does not last", in.Runway)
	}
	hoursToFull := 0.6 / 0.325
	wantUntil := time.Duration(hoursToFull * float64(time.Hour))
	if d := in.Runway.Until - wantUntil; d > time.Second || d < -time.Second {
		t.Fatalf("runway until = %v, want about %v", in.Runway.Until, wantUntil)
	}
	if in.Pace.Label != PaceBrisk || in.State != Amber {
		t.Fatalf("pace %q state %v, want brisk and amber", in.Pace.Label, in.State)
	}
	if in.Runway.Text != "runs out in 1h 50m, 1h 9m before the reset" {
		t.Fatalf("runway text = %q", in.Runway.Text)
	}

	// Lightly used: outlasts the reset.
	in = measure(0.05)
	if !in.Runway.Lasts || in.Pace.Label != PaceComfortable || in.State != Teal || in.Runway.Text != "lasts past the reset" {
		t.Fatalf("light use = %+v pace %q state %v", in.Runway, in.Pace.Label, in.State)
	}
	// Nearly spent at this pace: hot.
	in = measure(0.9)
	if in.Runway.Lasts || in.Pace.Label != PaceHot {
		t.Fatalf("heavy use = %+v pace %q, want hot", in.Runway, in.Pace.Label)
	}
	// Spent: red until the reset.
	in = measure(1)
	if in.State != Red || in.Runway.Until != 0 || in.Runway.Text != "exhausted, refills in 3h 0m" {
		t.Fatalf("spent = %+v state %v", in.Runway, in.State)
	}
}

// R20: without a plan window a day or month budget stands in for the limit; with
// neither there is nothing to project and the pace is only active or idle.
func TestBudgetRule20_RunwayFallsBackToBudgetsThenNone(t *testing.T) {
	path := filepath.Join(t.TempDir(), LedgerFile)
	r, c := openClock(t, path, "s", at(25, 13, 15))
	r.AddCall("p", "m", "agent", 600)
	c.set(at(25, 14, 10))
	r.AddCall("p", "m", "agent", 1000)
	now := at(25, 14, 30)
	c.set(now)

	// 8400 of 10000 tokens left at 1300 tok/h is 6.5 h; the day has 9.5 h left.
	in := r.Intel(now, "p", []config.TokenBudget{session(1), day(10000)})
	if in.Runway.Kind != RunwayBudget || in.Runway.Lasts || in.Pace.Label != PaceBrisk {
		t.Fatalf("budget runway = %+v pace %q, want a brisk day-budget runway (a session budget has no window)", in.Runway, in.Pace.Label)
	}
	in = r.Intel(now, "p", []config.TokenBudget{day(1_000_000)})
	if !in.Runway.Lasts || in.Pace.Label != PaceComfortable {
		t.Fatalf("roomy budget = %+v pace %q, want it to last", in.Runway, in.Pace.Label)
	}
	in = r.Intel(now, "p", []config.TokenBudget{day(1600)})
	if in.Runway.Kind != RunwayBudget || in.State != Red || in.Runway.Until != 0 {
		t.Fatalf("spent budget = %+v state %v, want exhausted and red", in.Runway, in.State)
	}
	in = r.Intel(now, "p", nil)
	if in.Runway.Kind != RunwayNone || in.Runway.Text != "no limit set" || in.Pace.Label != PaceActive {
		t.Fatalf("no limit = %+v pace %q, want none and active", in.Runway, in.Pace.Label)
	}
	// An hour later nothing has been spent for the trailing 60 minutes: idle.
	idle := at(25, 16, 30)
	c.set(idle)
	in = r.Intel(idle, "p", []config.TokenBudget{day(10000)})
	if in.Pace.TokensPerHour != 0 || in.Pace.Label != PaceIdle || !in.Runway.Lasts {
		t.Fatalf("idle = pace %v %q runway %+v", in.Pace.TokensPerHour, in.Pace.Label, in.Runway)
	}
}

// R20: the trend compares today's total with the previous seven days' mean at
// the same point of the day.
func TestBudgetRule20_TrendAgainstTheSevenDayAverage(t *testing.T) {
	path := filepath.Join(t.TempDir(), LedgerFile)
	r, c := openClock(t, path, "s", at(25, 14, 30))
	now := c.now()
	if got := r.Intel(now, "p", nil).Trend.Label; got != TrendNew {
		t.Fatalf("empty ledger trend = %q, want %q", got, TrendNew)
	}
	for d := 18; d <= 24; d++ { // the previous seven days, 1000 tokens each
		setDay(r, "p/m", at(d, 0, 0), 1000)
	}
	// 14:30 is 14.5/24 of the day, so about 604 tokens are expected by now.
	for _, tc := range []struct {
		today int64
		want  string
	}{{300, TrendEasing}, {600, TrendSteady}, {1600, TrendRising}} {
		setDay(r, "p/m", at(25, 0, 0), tc.today)
		in := r.Intel(now, "p", nil)
		if in.Trend.Label != tc.want {
			t.Fatalf("today %d: trend %q (ratio %.2f), want %q", tc.today, in.Trend.Label, in.Trend.Ratio, tc.want)
		}
	}
}

// R19: a plan limit is a parsed reading. One that fails the shape check is
// dropped; of two for the same window the later observation wins; a provider's
// limits are its own; plan windows list before the per-minute allowances.
func TestBudgetRule19_PlanLimitsLatestWinsAndFailClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), LedgerFile)
	now := at(25, 12, 0)
	r, _ := openClock(t, path, "s", now)
	good := PlanLimit{Provider: "p", Window: WindowFiveHour, Used: 0.2, Reset: now.Add(time.Hour), ObservedAt: now}
	bad := []PlanLimit{
		{Provider: "p", Window: WindowSevenDay, Used: 1.5, Reset: now.Add(time.Hour)},                              // over 100 %
		{Provider: "p", Window: WindowSevenDay, Used: -0.1, Reset: now.Add(time.Hour)},                             // negative
		{Provider: "p", Window: WindowSevenDay, Used: 0.1, Reset: now.Add(-2 * time.Hour)},                         // reset long past
		{Provider: "p", Window: WindowSevenDay, Used: 0.1, Reset: now.Add(9 * 24 * time.Hour)},                     // reset too far ahead
		{Provider: "p", Window: "12h", Used: 0.1, Reset: now.Add(time.Hour)},                                       // unknown window
		{Provider: "", Window: WindowSevenDay, Used: 0.1, Reset: now.Add(time.Hour)},                               // no provider
		{Provider: "p", Window: WindowMinTokens, Used: 0.1, Limit: 10, Remaining: 11, Reset: now.Add(time.Minute)}, // remaining > limit
		{Provider: "p", Window: WindowMinTokens, Used: math.NaN(), Reset: now.Add(time.Minute)},
	}
	r.ObserveLimits(append(bad, good))
	if got := r.Limits("p"); len(got) != 1 || got[0].Window != WindowFiveHour {
		t.Fatalf("limits = %+v, want only the valid five-hour reading", got)
	}

	// A newer reading replaces it; an older one arriving later does not.
	newer := good
	newer.Used, newer.ObservedAt = 0.5, now.Add(time.Minute)
	older := good
	older.Used, older.ObservedAt = 0.1, now.Add(-time.Minute)
	r.ObserveLimits([]PlanLimit{newer})
	r.ObserveLimits([]PlanLimit{older})
	if got := r.Limits("p"); len(got) != 1 || got[0].Used != 0.5 {
		t.Fatalf("limits = %+v, want the later observation (0.5)", got)
	}

	// Another provider's limits stay apart, and plan windows come first.
	r.ObserveLimits([]PlanLimit{
		{Provider: "p", Window: WindowMinRequests, Used: 0.3, Limit: 100, Remaining: 70, Reset: now.Add(time.Minute), ObservedAt: now},
		{Provider: "p", Window: WindowSevenDay, Used: 0.4, Reset: now.Add(24 * time.Hour), ObservedAt: now},
		{Provider: "q", Window: WindowFiveHour, Used: 0.9, Reset: now.Add(time.Hour), ObservedAt: now},
	})
	got := r.Limits("p")
	if len(got) != 3 || got[0].Window != WindowFiveHour || got[1].Window != WindowSevenDay || got[2].Window != WindowMinRequests {
		t.Fatalf("limits = %+v, want 5h, 7d, then requests/min", got)
	}
	if tight := Tightest(got); tight == nil || tight.Window != WindowFiveHour {
		t.Fatalf("tightest = %+v, want the five-hour window", tight)
	}
	if Tightest(nil) != nil {
		t.Fatal("tightest of nothing is not nil")
	}
	if q := r.Limits("q"); len(q) != 1 || q[0].Used != 0.9 {
		t.Fatalf("q limits = %+v", q)
	}

	// The reading is written to the ledger and another process sees it.
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	other, _ := openClock(t, path, "other", now)
	if got := other.Limits("p"); len(got) != 3 {
		t.Fatalf("a second process sees %d limits, want 3", len(got))
	}
}

// E24: a reading whose window has already reset describes a window that no
// longer exists, so it is left out until a fresh one arrives.
func TestBudgetEdge24_ResetWindowIsHidden(t *testing.T) {
	path := filepath.Join(t.TempDir(), LedgerFile)
	now := at(25, 12, 0)
	r, c := openClock(t, path, "s", now)
	r.ObserveLimits([]PlanLimit{{Provider: "p", Window: WindowFiveHour, Used: 0.8, Reset: now.Add(30 * time.Minute), ObservedAt: now}})
	if len(r.Limits("p")) != 1 {
		t.Fatal("the reading is missing before its reset")
	}
	c.set(now.Add(31 * time.Minute))
	if got := r.Limits("p"); len(got) != 0 {
		t.Fatalf("limits after the reset = %+v, want none", got)
	}
	if in := r.Intel(c.now(), "p", nil); len(in.Limits) != 0 || in.Runway.Kind != RunwayNone {
		t.Fatalf("intel after the reset = %+v, runway %+v", in.Limits, in.Runway)
	}
}

// R19: the validity rules are exposed as PlanLimit.Valid and Span.
func TestBudgetRule19_ValidAndSpan(t *testing.T) {
	now := at(25, 12, 0)
	ok := PlanLimit{Provider: "p", Window: WindowSevenDay, Used: 1, Reset: now.Add(time.Hour)}
	if !ok.Valid(now) {
		t.Fatal("a spent seven-day window at the edge is valid")
	}
	for w, want := range map[string]time.Duration{
		WindowFiveHour: 5 * time.Hour, WindowSevenDay: 7 * 24 * time.Hour,
		WindowMinTokens: time.Minute, WindowMinInput: time.Minute, WindowMinOutput: time.Minute, WindowMinRequests: time.Minute, "x": 0,
	} {
		if got := (PlanLimit{Window: w}).Span(); got != want {
			t.Fatalf("span(%s) = %v, want %v", w, got, want)
		}
	}
}

// E19: a version 1 ledger (days, sessions and imported only) loads unchanged;
// the first write upgrades it to version 2 without losing a day.
func TestBudgetEdge19_V1LedgerLoadsUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), LedgerFile)
	v1 := `{"version":1,"days":{"p/m":{"2026-09-25":50}},"sessions":{"old":{"updated":"2026-09-25T09:00:00+00:00","models":{"p/m":50}}},"imported":{"x":"2026-09-01"}}`
	if err := os.WriteFile(path, []byte(v1), 0o600); err != nil {
		t.Fatal(err)
	}
	r, _ := openClock(t, path, "s", at(25, 12, 0))
	if got := r.Used(day(1000)); got != 50 {
		t.Fatalf("day usage from a v1 ledger = %d, want 50", got)
	}
	in := r.Intel(at(25, 12, 0), "p", nil)
	if in.Rollups[0].Tokens != 50 || len(in.Limits) != 0 {
		t.Fatalf("intel over a v1 ledger = %d tokens, %d limits", in.Rollups[0].Tokens, len(in.Limits))
	}
	r.AddCall("p", "m", "", 7)
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	d := readFile(t, path)
	if d.Version != ledgerVersion || d.Days["p/m"]["2026-09-25"] != 57 || d.Imported["x"] != "2026-09-01" {
		t.Fatalf("upgraded ledger = version %d days %v imported %v", d.Version, d.Days, d.Imported)
	}
	if d.Hours["p/m"]["2026-09-25T12"].T != 7 || d.Hours["p/m"]["2026-09-25T12"].C != 1 {
		t.Fatalf("hours = %v, want the new call's bucket", d.Hours)
	}
}

// E20: an older belai that writes the ledger drops the hours and limits it does
// not know. Nothing breaks: the intel views are empty until the next call refills
// them, and day totals are untouched.
func TestBudgetEdge20_OlderBinaryDropsHoursAndLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), LedgerFile)
	now := at(25, 12, 30)
	r, c := openClock(t, path, "s", now)
	r.AddCall("p", "m", "agent", 100)
	r.ObserveLimits([]PlanLimit{{Provider: "p", Window: WindowFiveHour, Used: 0.1, Reset: now.Add(time.Hour), ObservedAt: now}})
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	// The old binary rewrites the file with the fields it knows.
	d := readFile(t, path)
	old, _ := json.Marshal(map[string]any{"version": 1, "days": d.Days, "sessions": d.Sessions})
	if err := os.WriteFile(path, old, 0o600); err != nil {
		t.Fatal(err)
	}
	r.Refresh(0)
	in := r.Intel(c.now(), "p", nil)
	var sum int64
	for _, v := range in.Spark {
		sum += v
	}
	if sum != 0 || len(in.Limits) != 0 || in.Rollups[0].Tokens != 100 {
		t.Fatalf("after the rewrite: spark %d, limits %d, today %d; want 0, 0, 100", sum, len(in.Limits), in.Rollups[0].Tokens)
	}
	r.AddCall("p", "m", "agent", 5)
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path).Hours["p/m"]["2026-09-25T12"].T; got != 5 {
		t.Fatalf("hour bucket after the refill = %d, want 5", got)
	}
}

// E22: roles are this process's tally, kept in memory: the ledger has no role,
// so a second process starts with none.
func TestBudgetEdge22_RolesAreThisSessionOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), LedgerFile)
	now := at(25, 12, 0)
	r, c := openClock(t, path, "s", now)
	r.AddCall("p", "m", "agent", 500)
	r.AddCall("p", "m", "security", 100)
	r.AddCall("p", "m", "agent", 250)
	r.AddCall("p", "m", "", 40) // no role: counted in the totals, not in the tally
	in := r.Intel(c.now(), "p", nil)
	if len(in.Roles) != 2 || in.Roles[0].Role != "agent" || in.Roles[0].Tokens != 750 || in.Roles[1].Role != "security" {
		t.Fatalf("roles = %+v, want agent 750 then security 100", in.Roles)
	}
	if in.Rollups[0].Tokens != 890 {
		t.Fatalf("today = %d, want the unroled call counted too (890)", in.Rollups[0].Tokens)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	other, oc := openClock(t, path, "other", now)
	if got := other.Intel(oc.now(), "p", nil).Roles; len(got) != 0 {
		t.Fatalf("a second process sees roles %+v, want none", got)
	}
}

// E27: hour buckets and limit readings are kept for eight days; day totals for
// far longer.
func TestBudgetEdge27_HoursAndLimitsPruned(t *testing.T) {
	path := filepath.Join(t.TempDir(), LedgerFile)
	start := at(10, 12, 0)
	r, c := openClock(t, path, "s", start)
	r.AddCall("p", "m", "", 100)
	r.ObserveLimits([]PlanLimit{{Provider: "p", Window: WindowSevenDay, Used: 0.1, Reset: start.Add(24 * time.Hour), ObservedAt: start}})
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	c.set(start.Add(9 * 24 * time.Hour))
	r.AddCall("p", "m", "", 1)
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	d := readFile(t, path)
	if _, ok := d.Hours["p/m"]["2026-09-10T12"]; ok {
		t.Fatalf("the nine-day-old hour bucket survived: %v", d.Hours)
	}
	if d.Hours["p/m"]["2026-09-19T12"].T != 1 {
		t.Fatalf("the new hour bucket is missing: %v", d.Hours)
	}
	if len(d.Limits) != 0 {
		t.Fatalf("the nine-day-old limit reading survived: %v", d.Limits)
	}
	if d.Days["p/m"]["2026-09-10"] != 100 {
		t.Fatalf("day totals were pruned with the hours: %v", d.Days)
	}
}

func TestFormatDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		30 * time.Second:              "<1m",
		42 * time.Minute:              "42m",
		5*time.Hour + 12*time.Minute:  "5h 12m",
		3*24*time.Hour + 4*time.Hour:  "3d 4h",
		24*time.Hour + 59*time.Minute: "1d 0h",
		time.Hour:                     "1h 0m",
	} {
		if got := FormatDuration(d); got != want {
			t.Fatalf("FormatDuration(%v) = %q, want %q", d, got, want)
		}
	}
}
