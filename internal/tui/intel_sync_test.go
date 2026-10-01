package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/budget"
	"github.com/vulnetix/belai/internal/session"
)

func intelEntries(t *testing.T, workdir, id string) []session.Entry {
	t.Helper()
	st, err := session.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	all, err := st.Read(workdir, id)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	var out []session.Entry
	for _, e := range all {
		if e.Type == budget.EntryTypeIntelState {
			out = append(out, e)
		}
	}
	return out
}

func intelSyncApp(t *testing.T) (*App, string, func(float64, time.Time)) {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir})
	t.Cleanup(a.closeBudgets)
	if a.budgets == nil {
		t.Fatal("the usage ledger did not open")
	}
	a.cfg.Provider, a.cfg.Model = "p", "m"
	limit := func(used float64, now time.Time) {
		a.budgets.ObserveLimits([]budget.PlanLimit{{
			Provider: "p", Window: budget.WindowFiveHour, Used: used,
			Reset: now.Add(2 * time.Hour), ObservedAt: now,
		}})
	}

	return a, workdir, limit
}

// R24: the snapshot is written once, then again only when something a reader
// would notice moves and a minute has passed.
func TestBudgetRule24_WritesOnChangeOnly(t *testing.T) {
	a, workdir, limit := intelSyncApp(t)
	now := time.Now()

	limit(0.2, now)
	a.syncIntel(now)
	if n := len(intelEntries(t, workdir, a.sessionID)); n != 1 {
		t.Fatalf("first sync wrote %d entries, want 1", n)
	}

	limit(0.5, now)
	a.syncIntel(now.Add(10 * time.Second)) // moved, but inside the minute
	if n := len(intelEntries(t, workdir, a.sessionID)); n != 1 {
		t.Fatalf("a snapshot inside the minute was written (%d entries)", n)
	}

	a.syncIntel(now.Add(3 * time.Minute))
	got := intelEntries(t, workdir, a.sessionID)
	if len(got) != 2 {
		t.Fatalf("a moved limit after the minute wrote %d entries, want 2", len(got))
	}
	if !strings.Contains(got[1].Content, `"used":0.5`) {
		t.Fatalf("the second snapshot is not the moved one: %s", got[1].Content)
	}
}

// E30: a snapshot that says what the last one said is not written, however
// long it has been.
func TestBudgetEdge30_UnchangedSnapshotIsNotWritten(t *testing.T) {
	a, workdir, limit := intelSyncApp(t)
	now := time.Now()

	limit(0.2, now)
	a.budgets.AddCall("p", "m", "agent", 100) // the pace word reads "active" from here
	a.syncIntel(now)
	a.budgets.AddCall("p", "m", "agent", 40) // more tokens, same news
	a.syncIntel(now.Add(10 * time.Minute))
	if n := len(intelEntries(t, workdir, a.sessionID)); n != 1 {
		t.Fatalf("an unchanged snapshot was written again (%d entries)", n)
	}
}

// E29: with the session store off nothing is written and nothing panics.
func TestBudgetEdge29_StoreOffWritesNothing(t *testing.T) {
	a, _, limit := intelSyncApp(t)
	now := time.Now()
	last := a.lastEntryID

	limit(0.2, now)
	a.storeDisabled = true
	a.syncIntel(now)
	if a.lastEntryID != last {
		t.Fatal("a disabled store took an entry")
	}
	if !a.intelSyncAt.IsZero() {
		t.Fatal("a skipped write must not start the one minute wait")
	}

	a.storeDisabled = false
	a.store = nil
	a.syncIntel(now) // no store at all
}

// E32: a resumed or exported session ignores the snapshot lines.
func TestBudgetEdge32_ResumeAndExportIgnoreSnapshots(t *testing.T) {
	snap := budget.NewIntelState(budget.Intel{Now: time.Now(), Provider: "p"}).ToEntry("")
	entries := []session.Entry{
		{ID: "u1", Type: "user", Role: "user", Content: "hello"},
		snap,
		{ID: "a1", Type: "assistant", Role: "assistant", Content: "hi"},
	}

	msgs, dropped := messagesFromEntries(entries)
	if len(msgs) != 2 || dropped != 0 {
		t.Fatalf("rebuilt %d messages, %d dropped; want 2 and 0", len(msgs), dropped)
	}
	for _, m := range msgs {
		if strings.Contains(m.Content, "tokensPerHour") {
			t.Fatalf("a snapshot reached the conversation: %q", m.Content)
		}
	}

	md := session.ExportMarkdown(entries, session.ExportOptions{})
	if strings.Contains(md, "tokensPerHour") || strings.Contains(md, "intel_state") {
		t.Fatalf("a snapshot reached the export:\n%s", md)
	}
}
