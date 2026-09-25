package budget

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/signet/internal/config"
)

func openAt(t *testing.T, path, session string, now time.Time) *Recorder {
	t.Helper()
	r, err := Open(path, session, 28)
	if err != nil {
		t.Fatal(err)
	}
	r.now = func() time.Time { return now }
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func readFile(t *testing.T, path string) ledgerData {
	t.Helper()
	d, _, err := readLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// R5: a session's total is its own; a new session starts from zero and a
// resumed one picks its stored total back up.
func TestBudgetRule5_SessionTotalsRestartAndResume(t *testing.T) {
	path := filepath.Join(t.TempDir(), LedgerFile)
	now := time.Date(2026, 9, 25, 10, 0, 0, 0, time.Local)
	r := openAt(t, path, "s1", now)
	r.Add("p", "m", 300)
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	b := session(1000)
	if got := r.Used(b); got != 300 {
		t.Fatalf("s1 = %d, want 300", got)
	}
	r.SetSession("s2")
	if got := r.Used(b); got != 0 {
		t.Fatalf("new session = %d, want 0", got)
	}
	r.Add("p", "m", 50)
	r.SetSession("s1")
	if got := r.Used(b); got != 300 {
		t.Fatalf("resumed s1 = %d, want 300", got)
	}
	// Day scope sums across sessions.
	if got := r.Used(day(1000)); got != 350 {
		t.Fatalf("day = %d, want 350", got)
	}
}

// R12: usage persists in usage.json and another process sees it after a
// refresh; pending usage is visible to its own process at once.
func TestBudgetRule12_LedgerPersistsAndIsShared(t *testing.T) {
	path := filepath.Join(t.TempDir(), LedgerFile)
	now := time.Date(2026, 9, 25, 10, 0, 0, 0, time.Local)
	a := openAt(t, path, "a", now)
	b := openAt(t, path, "b", now)
	a.Add("p", "m", 1000)
	if got := a.Used(day(1)); got != 1000 {
		t.Fatalf("own pending usage = %d, want 1000 at once", got)
	}
	if err := a.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := b.Used(day(1)); got != 0 {
		t.Fatalf("other process before refresh = %d, want 0", got)
	}
	b.Refresh(0)
	if got := b.Used(day(1)); got != 1000 {
		t.Fatalf("other process after refresh = %d, want 1000", got)
	}
	// A refresh younger than maxAge is skipped.
	a.Add("p", "m", 1)
	_ = a.Flush()
	b.Refresh(time.Hour)
	if got := b.Used(day(1)); got != 1000 {
		t.Fatalf("fresh refresh re-read the file: %d", got)
	}
	d := readFile(t, path)
	if d.Days["p/m"][now.Format(dayLayout)] != 1001 {
		t.Fatalf("file days = %v", d.Days)
	}
}

// R12: day totals older than 13 months and idle sessions are pruned on write.
func TestBudgetRule12_Pruning(t *testing.T) {
	path := filepath.Join(t.TempDir(), LedgerFile)
	now := time.Date(2026, 9, 25, 10, 0, 0, 0, time.Local)
	old := ledgerData{Version: 1,
		Days: map[string]map[string]int64{"p/m": {
			"2025-06-01": 5, // older than 13 months
			"2025-10-01": 7, // within 13 months
		}},
		Sessions: map[string]*sessionEntry{
			"idle":   {Updated: now.Add(-40 * 24 * time.Hour), Models: map[string]int64{"p/m": 1}},
			"recent": {Updated: now.Add(-time.Hour), Models: map[string]int64{"p/m": 1}},
		},
	}
	buf, _ := json.Marshal(old)
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatal(err)
	}
	r := openAt(t, path, "s", now)
	r.Add("p", "m", 1)
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	d := readFile(t, path)
	if _, ok := d.Days["p/m"]["2025-06-01"]; ok {
		t.Fatal("a day older than 13 months survived")
	}
	if d.Days["p/m"]["2025-10-01"] != 7 {
		t.Fatal("a day within 13 months was pruned")
	}
	if d.Sessions["idle"] != nil || d.Sessions["recent"] == nil {
		t.Fatalf("sessions = %v, want idle pruned and recent kept", d.Sessions)
	}
}

// E4: the month total counts only the current local month.
func TestBudgetEdge4_MonthTotalCountsOnlyThisMonth(t *testing.T) {
	path := filepath.Join(t.TempDir(), LedgerFile)
	last := time.Date(2026, 8, 31, 23, 0, 0, 0, time.Local)
	r := openAt(t, path, "s", last)
	r.Add("p", "m", 400)
	r.now = func() time.Time { return time.Date(2026, 9, 1, 0, 30, 0, 0, time.Local) }
	r.Add("p", "m", 25)
	if got := r.Used(month(1)); got != 25 {
		t.Fatalf("September total = %d, want 25 (August's 400 dropped out)", got)
	}
	if got := r.Used(day(1)); got != 25 {
		t.Fatalf("1 September day total = %d, want 25", got)
	}
}

// E7: a lockfile left by a crashed process is taken over once stale.
func TestBudgetEdge7_StaleLockIsTakenOver(t *testing.T) {
	path := filepath.Join(t.TempDir(), LedgerFile)
	lock := path + ".lock"
	if err := os.WriteFile(lock, []byte("99999"), 0o600); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-config.LockStaleAfter - time.Second)
	if err := os.Chtimes(lock, stale, stale); err != nil {
		t.Fatal(err)
	}
	r := openAt(t, path, "s", time.Now())
	r.Add("p", "m", 10)
	if err := r.Flush(); err != nil {
		t.Fatalf("flush with a stale lock: %v", err)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatal("lock not released after the write")
	}
}

// E8: a ledger that does not parse is moved aside and counting restarts.
func TestBudgetEdge8_CorruptLedgerMovedAside(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, LedgerFile)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := openAt(t, path, "s", time.Now())
	if n := r.Notice(); !strings.Contains(n, "moved to") || !strings.Contains(n, ".corrupt-") {
		t.Fatalf("notice = %q, want it to name where the file went", n)
	}
	if got := r.Used(day(1)); got != 0 {
		t.Fatalf("usage after recovery = %d, want 0", got)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, LedgerFile+".corrupt-*"))
	if len(matches) != 1 {
		t.Fatalf("corrupt copies = %v, want one", matches)
	}
}

// E15: two processes writing at once lose nothing.
func TestBudgetEdge15_ConcurrentWritersKeepAllUsage(t *testing.T) {
	path := filepath.Join(t.TempDir(), LedgerFile)
	now := time.Date(2026, 9, 25, 10, 0, 0, 0, time.Local)
	a := openAt(t, path, "a", now)
	b := openAt(t, path, "b", now)
	var wg sync.WaitGroup
	for _, r := range []*Recorder{a, b} {
		wg.Add(1)
		go func(r *Recorder) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				r.Add("p", "m", 10)
				if i%10 == 0 {
					_ = r.Flush()
				}
			}
		}(r)
	}
	wg.Wait()
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path).Days["p/m"][now.Format(dayLayout)]; got != 1000 {
		t.Fatalf("day total = %d, want 1000 (2 writers × 50 × 10)", got)
	}
}

// E16: a failed write keeps the usage pending and counted; the next flush
// writes it.
func TestBudgetEdge16_FailedWriteKeepsUsagePending(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, LedgerFile)
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r := openAt(t, good, "s", time.Now())
	r.path = filepath.Join(blocker, LedgerFile) // a file where a directory must be
	r.Add("p", "m", 77)
	if err := r.Flush(); err == nil {
		t.Fatal("expected the write to fail")
	}
	if got := r.Used(day(1)); got != 77 {
		t.Fatalf("usage after a failed write = %d, want 77 still counted", got)
	}
	r.path = good
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, good).Days["p/m"][time.Now().Format(dayLayout)]; got != 77 {
		t.Fatalf("retried write = %d, want 77", got)
	}
}

// Add writes in the background without a Flush call, and Close is idempotent.
func TestRecorderFlushesInBackgroundAndClosesOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), LedgerFile)
	r, err := Open(path, "s", 0)
	if err != nil {
		t.Fatal(err)
	}
	r.Add("p", "m", 5)
	r.Add("p", "m", 0) // ignored
	deadline := time.Now().Add(5 * time.Second)
	for {
		if readFile(t, path).Days["p/m"][time.Now().Format(dayLayout)] == 5 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background flush never wrote the usage")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	gs := r.Gauges([]config.TokenBudget{day(10)})
	if len(gs) != 1 || gs[0].Used != 5 {
		t.Fatalf("gauges = %+v", gs)
	}
}

// A fresh install has no global directory yet; the first write creates it.
func TestRecorderCreatesMissingDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not", "yet", LedgerFile)
	r := openAt(t, path, "s", time.Now())
	r.Add("p", "m", 3)
	if err := r.Flush(); err != nil {
		t.Fatalf("first write into a missing directory: %v", err)
	}
	if got := readFile(t, path).Days["p/m"][time.Now().Format(dayLayout)]; got != 3 {
		t.Fatalf("written = %d, want 3", got)
	}
}
