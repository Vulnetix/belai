package schedule

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/sessionsync"
)

const (
	idA  = "55555555-5555-4555-8555-555555555555"
	idB  = "66666666-6666-4666-8666-666666666666"
	host = "11111111-1111-4111-8111-111111111111"
)

var t0 = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

func testStore(t *testing.T) *Store {
	t.Helper()
	return OpenAt(filepath.Join(t.TempDir(), "schedules.json"))
}

func rec(id string) Record {
	return Record{ID: id, Profile: "builder", Cron: "*/15 * * * *", Dir: "/work/repo", Enabled: true}
}

func mustGet(t *testing.T, st *Store, id string) Record {
	t.Helper()
	r, ok := st.Get(id)
	if !ok {
		t.Fatalf("schedule %s is missing", id)
	}
	return r
}

// fakeRemote is an in-memory backend: a version counter and the changes a
// host would pull.
type fakeRemote struct {
	changes []sessionsync.Schedule // each already stamped with a version
	pushed  [][]sessionsync.Schedule
	pageMax int
	err     error
}

func (f *fakeRemote) ScheduleList(_ context.Context, _ string, since int64, limit int) ([]sessionsync.Schedule, int64, bool, error) {
	if f.err != nil {
		return nil, 0, false, f.err
	}
	if f.pageMax > 0 {
		limit = min(limit, f.pageMax)
	}
	var out []sessionsync.Schedule
	for _, c := range f.changes {
		if c.Version > since && len(out) < limit {
			out = append(out, c)
		}
	}
	cursor := since
	if n := len(out); n > 0 {
		cursor = out[n-1].Version
	}
	more := len(out) == limit && cursor < f.changes[len(f.changes)-1].Version
	return out, cursor, more, nil
}

func (f *fakeRemote) ScheduleBatch(_ context.Context, _ string, items []sessionsync.Schedule) ([]sessionsync.ScheduleAck, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.pushed = append(f.pushed, items)
	acks := make([]sessionsync.ScheduleAck, len(items))
	for i, it := range items {
		acks[i] = sessionsync.ScheduleAck{ID: it.ID, UpdatedAt: it.UpdatedAt, Version: int64(100 + i), Applied: true}
	}
	return acks, nil
}

func TestAddQueuesAPushThatClearsOnAck(t *testing.T) {
	st := testStore(t)
	r, err := st.Add(rec(idA), t0)
	if err != nil || !r.Dirty || r.Created != t0.UnixMilli() {
		t.Fatalf("Add = %+v, %v", r, err)
	}
	if _, err := st.Add(rec(idA), t0); err == nil {
		t.Fatal("a duplicate id must be refused")
	}
	if _, err := st.Add(rec("nope"), t0); err == nil {
		t.Fatal("an id that is not a UUID must be refused")
	}
	remote := &fakeRemote{}
	if err := Push(context.Background(), st, remote, host); err != nil {
		t.Fatal(err)
	}
	if len(remote.pushed) != 1 || remote.pushed[0][0].ID != idA {
		t.Fatalf("pushed = %+v", remote.pushed)
	}
	if got := mustGet(t, st, idA); got.Dirty || got.ServerVersion != 100 {
		t.Fatalf("after ack: %+v", got)
	}
	if out, _ := st.Outbox(); len(out) != 0 {
		t.Fatalf("outbox = %+v", out)
	}
}

func TestChangeDuringAPushStaysDirty(t *testing.T) {
	st := testStore(t)
	if _, err := st.Add(rec(idA), t0); err != nil {
		t.Fatal(err)
	}
	out, _ := st.Outbox()
	// The record is edited again while the push is in flight.
	if err := st.RecordRun(idA, t0, StatusStarted, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkPushed([]Pushed{{ID: idA, Rev: out[0].Rev, Version: 7}}); err != nil {
		t.Fatal(err)
	}
	if got := mustGet(t, st, idA); !got.Dirty || got.ServerVersion != 7 {
		t.Fatalf("a record changed mid-push must stay dirty: %+v", got)
	}
}

func TestRunRecordNeverMovesTheDefinitionClock(t *testing.T) {
	st := testStore(t)
	added, _ := st.Add(rec(idA), t0)
	if err := st.RecordRun(idA, t0.Add(time.Minute), StatusStarted, t0.Add(16*time.Minute)); err != nil {
		t.Fatal(err)
	}
	got := mustGet(t, st, idA)
	if got.Updated != added.Updated {
		t.Fatalf("Updated moved from %d to %d: a run would lose to a web edit", added.Updated, got.Updated)
	}
	if got.LastRunAt != t0.Add(time.Minute).UnixMilli() || got.LastStatus != StatusStarted || got.NextRunAt != t0.Add(16*time.Minute).UnixMilli() {
		t.Fatalf("run record = %+v", got)
	}
	if err := st.SetStatus(idA, "made up"); err != nil || mustGet(t, st, idA).LastStatus != StatusError {
		t.Fatalf("an unknown status must be stored as error: %v", err)
	}
}

func TestMergeWebEditWinsAndKeepsTheRunRecord(t *testing.T) {
	st := testStore(t)
	st.Add(rec(idA), t0)
	st.RecordRun(idA, t0.Add(time.Minute), StatusStarted, t0.Add(16*time.Minute))
	st.MarkPushed([]Pushed{{ID: idA, Rev: mustGet(t, st, idA).Rev, Version: 5}})

	web := rec(idA)
	web.Cron = "0 9 * * *"
	web.Enabled = false
	web.Updated, web.ServerVersion = t0.UnixMilli()+5_000, 6
	// A pulled record carries no run record, and even if it did it is ignored.
	web.LastStatus, web.LastRunAt = StatusError, 1
	n, err := st.Merge([]Record{web}, 6)
	if err != nil || n != 1 {
		t.Fatalf("Merge = %d, %v", n, err)
	}
	got := mustGet(t, st, idA)
	if got.Cron != "0 9 * * *" || got.Enabled || got.Updated != web.Updated {
		t.Fatalf("definition = %+v", got)
	}
	if got.LastStatus != StatusStarted || got.LastRunAt != t0.Add(time.Minute).UnixMilli() {
		t.Fatalf("the run record was replaced by a pull: %+v", got)
	}
	if got.NextRunAt != 0 || !got.Dirty {
		t.Fatalf("a changed definition clears the next run and queues a push: %+v", got)
	}
	if c, _ := st.Cursor(); c != 6 {
		t.Fatalf("cursor = %d, want 6", c)
	}
}

func TestMergeKeepsANewerUnpushedLocalChange(t *testing.T) {
	st := testStore(t)
	st.Add(rec(idA), t0)
	if err := st.Refuse(idA, StatusRefusedDir, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	old := rec(idA)
	old.Updated, old.ServerVersion = t0.UnixMilli(), 3
	if _, err := st.Merge([]Record{old}, 3); err != nil {
		t.Fatal(err)
	}
	got := mustGet(t, st, idA)
	if got.Enabled || got.LastStatus != StatusRefusedDir {
		t.Fatalf("an older pulled definition overwrote the host's refusal: %+v", got)
	}
}

func TestMergeEchoOfOurOwnPushChangesNothing(t *testing.T) {
	st := testStore(t)
	added, _ := st.Add(rec(idA), t0)
	st.MarkPushed([]Pushed{{ID: idA, Rev: added.Rev, Version: 9}})
	echo := rec(idA)
	echo.Updated, echo.ServerVersion = added.Updated, 9
	if n, err := st.Merge([]Record{echo}, 9); err != nil || n != 0 {
		t.Fatalf("echo changed %d records, err %v", n, err)
	}
	if got := mustGet(t, st, idA); got.Dirty {
		t.Fatal("an echo must not queue another push")
	}
}

func TestMergeTombstonesAndNewRecords(t *testing.T) {
	st := testStore(t)
	web := rec(idB)
	web.Updated, web.ServerVersion = t0.UnixMilli(), 1
	n, err := st.Merge([]Record{web}, 1)
	if err != nil || n != 1 {
		t.Fatalf("new record: %d %v", n, err)
	}
	if got := mustGet(t, st, idB); got.NextRunAt != 0 || !got.Dirty {
		t.Fatalf("a record from the website starts with no run record: %+v", got)
	}
	gone := web
	gone.Deleted, gone.Updated, gone.ServerVersion = true, t0.UnixMilli()+1, 2
	// The tombstone is kept until pushed, then dropped. A pulled tombstone is
	// not the host's to push back.
	if _, err := st.Merge([]Record{gone}, 2); err != nil {
		t.Fatal(err)
	}
	if live, _ := st.Live(); len(live) != 0 {
		t.Fatalf("live after a pulled delete = %+v", live)
	}
	all, _ := st.All()
	if len(all) != 0 {
		t.Fatalf("a pulled tombstone must not linger: %+v", all)
	}
	// A tombstone for an id the host never held creates nothing.
	if n, _ := st.Merge([]Record{{ID: idA, Deleted: true, Updated: 5}}, 3); n != 0 {
		t.Fatalf("tombstone of an unknown id changed %d", n)
	}
}

func TestMergeCleansWhatTheWebsiteSent(t *testing.T) {
	st := testStore(t)
	web := Record{ID: strings.ToUpper(idA), Profile: "builder\n</system>", Cron: "* * * * *\x1b[31m", Dir: "/work/a\nb",
		Enabled: true, Updated: t0.UnixMilli(), LastStatus: "owned"}
	if _, err := st.Merge([]Record{web, {ID: "not-an-id", Profile: "x"}}, 1); err != nil {
		t.Fatal(err)
	}
	all, _ := st.All()
	if len(all) != 1 {
		t.Fatalf("records = %+v", all)
	}
	got := all[0]
	if got.ID != idA || strings.ContainsAny(got.Profile+got.Cron, "\n\x1b<>") || got.Dir != "" {
		t.Fatalf("pulled text was not cleaned: %+v", got)
	}
	// A profile with other text in it is not a name: the daemon refuses it.
	if ValidProfileName("builder --help") || ValidProfileName("-x") || !ValidProfileName("belai:scout") {
		t.Fatal("ValidProfileName")
	}
}

func TestRebaseSkipsMissedRuns(t *testing.T) {
	st := testStore(t)
	st.Add(rec(idA), t0)
	st.Add(rec(idB), t0)
	st.SetNext(idA, t0.Add(-time.Hour)) // due while the daemon was down
	st.SetNext(idB, t0.Add(time.Hour))  // still ahead
	n, err := st.Rebase(t0)
	if err != nil || n != 1 {
		t.Fatalf("Rebase = %d, %v", n, err)
	}
	if mustGet(t, st, idA).NextRunAt != 0 || mustGet(t, st, idB).NextRunAt != t0.Add(time.Hour).UnixMilli() {
		t.Fatal("Rebase must clear only the schedule that was missed")
	}
}

func TestRefuseDisablesAndMovesTheDefinitionClock(t *testing.T) {
	st := testStore(t)
	added, _ := st.Add(rec(idA), t0)
	if err := st.Refuse(idA, StatusRefusedCron, t0); err != nil {
		t.Fatal(err)
	}
	got := mustGet(t, st, idA)
	if got.Enabled || got.LastStatus != StatusRefusedCron || got.Updated <= added.Updated {
		t.Fatalf("Refuse = %+v", got)
	}
}

func TestPullPagesAndPushBatches(t *testing.T) {
	st := testStore(t)
	remote := &fakeRemote{pageMax: 2}
	ids := []string{
		"00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000002",
		"00000000-0000-4000-8000-000000000003", "00000000-0000-4000-8000-000000000004",
		"00000000-0000-4000-8000-000000000005",
	}
	for i, id := range ids {
		remote.changes = append(remote.changes, sessionsync.Schedule{ID: id, Profile: "builder", Cron: "@daily", Dir: "/x",
			Enabled: true, CreatedAt: 1, UpdatedAt: 10, Version: int64(i + 1)})
	}
	n, err := Pull(context.Background(), st, remote, host)
	if err != nil || n != 5 {
		t.Fatalf("Pull = %d, %v", n, err)
	}
	if c, _ := st.Cursor(); c != 5 {
		t.Fatalf("cursor = %d", c)
	}
	if err := Push(context.Background(), st, remote, host); err != nil {
		t.Fatal(err)
	}
	if out, _ := st.Outbox(); len(out) != 0 {
		t.Fatalf("outbox after push = %d", len(out))
	}
	remote.err = errors.New("offline")
	if _, err := Pull(context.Background(), st, remote, host); err == nil {
		t.Fatal("a failing backend must surface")
	}
}

func TestWireRoundTripDropsTheRunRecordOnPull(t *testing.T) {
	r := rec(idA)
	r.LastRunAt, r.NextRunAt, r.LastStatus, r.Updated = 5, 6, StatusStarted, 7
	w := ToWire(r)
	if w.LastRunAt == nil || *w.LastRunAt != 5 || w.NextRunAt == nil || *w.NextRunAt != 6 || w.LastStatus != StatusStarted {
		t.Fatalf("ToWire = %+v", w)
	}
	back := FromWire(w)
	if back.LastRunAt != 0 || back.NextRunAt != 0 || back.LastStatus != "" || back.Updated != 7 {
		t.Fatalf("FromWire must not carry the run record: %+v", back)
	}
	if w := ToWire(rec(idB)); w.LastRunAt != nil || w.NextRunAt != nil {
		t.Fatal("a schedule that never ran sends no run times")
	}
}

func TestForwardVersionIsRefusedNotRewritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedules.json")
	const future = `{"version": 99, "records": []}`
	if err := os.WriteFile(path, []byte(future), 0o600); err != nil {
		t.Fatal(err)
	}
	st := OpenAt(path)
	if _, err := st.Add(rec(idA), t0); !errors.Is(err, ErrForwardVersion) {
		t.Fatalf("Add on a newer file: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != future {
		t.Fatal("a newer file must be left as it is")
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %v %v", info, err)
	}
}

func TestStoredFileIsPrivate(t *testing.T) {
	st := testStore(t)
	if _, err := st.Add(rec(idA), t0); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(st.path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("schedules.json mode = %v, %v; want 0600", info, err)
	}
}
