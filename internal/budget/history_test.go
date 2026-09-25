package budget

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/signet/internal/config"
)

// writeTranscript writes a session transcript of the given entries under
// <sessions>/proj/<id>.jsonl.
func writeTranscript(t *testing.T, sessions, id string, entries ...map[string]any) {
	t.Helper()
	dir := filepath.Join(sessions, "proj")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, e := range entries {
		line, _ := json.Marshal(e)
		b.Write(line)
		b.WriteByte('\n')
	}
	b.WriteString("{not json\n") // a damaged line is skipped, not fatal
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assistant(at time.Time, provider, model, mode string, tokens int) map[string]any {
	meta := map[string]any{"provider": provider, "model": model, "mode": mode}
	if tokens > 0 {
		meta["total_tokens"] = tokens
	}
	return map[string]any{"type": "assistant", "timestamp": at.UnixMilli(), "content": "", "meta": meta}
}

func goalState(at time.Time, id string, tokens int) map[string]any {
	gs, _ := json.Marshal(map[string]any{"id": id, "tokensUsed": tokens, "status": "active"})
	return map[string]any{"type": "goal_state", "timestamp": at.UnixMilli(), "content": string(gs)}
}

func dayUsed(r *Recorder, key string, at time.Time) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.disk.Days[key][at.Format(dayLayout)]
}

// R14: sessions the ledger never saw are counted from their transcripts: a
// goal's tokensUsed growth on the day of each goal_state row, under the model
// its goal-mode turns name, and other turns' own usage under their own model.
// Goal-mode rows are not counted again.
func TestBudgetRule14_HistoryImportCountsEarlierSessions(t *testing.T) {
	home := t.TempDir()
	sessions := filepath.Join(home, "sessions")
	d1 := time.Date(2026, 9, 24, 23, 0, 0, 0, time.Local)
	d2 := time.Date(2026, 9, 25, 1, 0, 0, 0, time.Local)
	writeTranscript(t, sessions, "old-goal",
		assistant(d1, "cf", "pro", "goal", 999), // inside the goal: not counted on its own
		goalState(d1, "g1", 100),
		goalState(d1, "g1", 400), // +300 on the 24th
		assistant(d2, "cf", "pro", "goal", 999),
		goalState(d2, "g1", 1000), // +600 on the 25th
		assistant(d2, "cf", "flash", "agent", 40),
	)
	r := openAt(t, filepath.Join(home, LedgerFile), "current", d2)
	n, err := r.ImportHistory(sessions)
	if err != nil || n != 1 {
		t.Fatalf("ImportHistory = %d, %v; want 1 session", n, err)
	}
	if got := dayUsed(r, "cf/pro", d1); got != 400 {
		t.Fatalf("pro on the 24th = %d, want 400 (100 + 300)", got)
	}
	if got := dayUsed(r, "cf/pro", d2); got != 600 {
		t.Fatalf("pro on the 25th = %d, want 600", got)
	}
	if got := dayUsed(r, "cf/flash", d2); got != 40 {
		t.Fatalf("flash on the 25th = %d, want 40", got)
	}
	// Budgets read the imported totals like any other usage.
	month := config.TokenBudget{Provider: "cf", Model: "pro", Scope: config.BudgetScopeMonth, Tokens: 1}
	if got := r.Used(month); got != 1000 {
		t.Fatalf("pro September total = %d, want 1000", got)
	}
}

// E17: a session is imported once, by whichever process gets there first.
func TestBudgetEdge17_SessionImportedOnce(t *testing.T) {
	home := t.TempDir()
	sessions := filepath.Join(home, "sessions")
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.Local)
	writeTranscript(t, sessions, "s-old", assistant(now, "cf", "pro", "agent", 50))
	path := filepath.Join(home, LedgerFile)
	a := openAt(t, path, "a", now)
	b := openAt(t, path, "b", now) // loaded before a imports: its snapshot says unseen
	if n, _ := a.ImportHistory(sessions); n != 1 {
		t.Fatalf("first import = %d, want 1", n)
	}
	if n, _ := b.ImportHistory(sessions); n != 0 {
		t.Fatalf("second process imported %d, want 0 (re-checked under the lock)", n)
	}
	if n, _ := a.ImportHistory(sessions); n != 0 {
		t.Fatalf("repeat import = %d, want 0", n)
	}
	if got := readFile(t, path).Days["cf/pro"][now.Format(dayLayout)]; got != 50 {
		t.Fatalf("day total = %d, want 50 counted once", got)
	}
}

// E18: a session recorded live is never imported — not while its entry is
// kept, not after it is pruned — and a recorder never imports its own session.
func TestBudgetEdge18_LiveSessionsNeverImported(t *testing.T) {
	home := t.TempDir()
	sessions := filepath.Join(home, "sessions")
	path := filepath.Join(home, LedgerFile)
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.Local)
	for _, id := range []string{"live", "mine", "idle"} {
		writeTranscript(t, sessions, id, assistant(now, "cf", "pro", "agent", 1000))
	}
	// "live" recorded 7 tokens live today; "idle" recorded 9 tokens live 40
	// days ago and is pruned on the next write.
	old := newLedgerData()
	old.Days["cf/pro"] = map[string]int64{now.Format(dayLayout): 7, now.Add(-40 * 24 * time.Hour).Format(dayLayout): 9}
	old.Sessions["live"] = &sessionEntry{Updated: now, Models: map[string]int64{"cf/pro": 7}}
	old.Sessions["idle"] = &sessionEntry{Updated: now.Add(-40 * 24 * time.Hour), Models: map[string]int64{"cf/pro": 9}}
	buf, _ := json.Marshal(old)
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatal(err)
	}
	r := openAt(t, path, "mine", now)
	r.Add("cf", "pro", 1) // this write prunes "idle" into Imported
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	if d := readFile(t, path); d.Sessions["idle"] != nil || d.Imported["idle"] == "" {
		t.Fatalf("pruned live session = %+v / imported %q, want moved to Imported", d.Sessions["idle"], d.Imported["idle"])
	}
	if n, err := r.ImportHistory(sessions); err != nil || n != 0 {
		t.Fatalf("ImportHistory = %d, %v; want nothing imported", n, err)
	}
	if got := dayUsed(r, "cf/pro", now); got != 8 {
		t.Fatalf("today = %d, want 8 (7 live + 1 now), no transcript usage", got)
	}
}

// A missing or empty sessions directory imports nothing and is not an error.
func TestImportHistoryNoSessions(t *testing.T) {
	home := t.TempDir()
	r := openAt(t, filepath.Join(home, LedgerFile), "s", time.Now())
	for _, dir := range []string{filepath.Join(home, "absent"), home} {
		if n, err := r.ImportHistory(dir); n != 0 || err != nil {
			t.Fatalf("ImportHistory(%s) = %d, %v", dir, n, err)
		}
	}
}
