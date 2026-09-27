package kanban

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/sessionsync"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	return Open(filepath.Join(t.TempDir(), "kanban"))
}

var prov = Provenance{SessionID: "sess-1", HostID: "host-1", Project: "belai", ProjectKey: "belai-1234", Dir: "/src/belai"}

func TestRoundTrip(t *testing.T) {
	s := testStore(t)
	it, dup, err := s.Add(ItemInput{Title: "  write   docs ", Body: "for the tool", List: Review}, prov)
	if err != nil || dup {
		t.Fatalf("add: %v dup=%v", err, dup)
	}
	if it.Title != "write docs" || it.List != Review || it.SessionID != "sess-1" || it.Project != "belai" || !it.Dirty {
		t.Fatalf("unexpected item %+v", it)
	}
	// A fresh store reads the same file back.
	got, err := Open(s.Path()).Get(it.Short())
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != it.ID || got.Body != "for the tool" || len(got.History) != 1 {
		t.Fatalf("reload: %+v", got)
	}
	fi, err := os.Stat(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", fi.Mode().Perm())
	}
	data, _ := os.ReadFile(s.Path())
	if !strings.HasPrefix(string(data), "BKAN") {
		t.Fatal("missing magic")
	}
}

func TestCorruptBoardIsNeverOverwritten(t *testing.T) {
	for name, mutate := range map[string]func([]byte) []byte{
		"checksum": func(b []byte) []byte { b[len(b)-1] ^= 0xff; return b },
		"magic":    func(b []byte) []byte { b[0] = 'X'; return b },
		"version":  func(b []byte) []byte { b[5] = 9; return b },
		"short":    func(b []byte) []byte { return b[:3] },
	} {
		t.Run(name, func(t *testing.T) {
			s := testStore(t)
			if _, _, err := s.Add(ItemInput{Title: "one"}, prov); err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(s.Path())
			bad := mutate(append([]byte{}, data...))
			if err := os.WriteFile(s.Path(), bad, 0o600); err != nil {
				t.Fatal(err)
			}
			fresh := Open(s.Path())
			if _, err := fresh.Search(Query{}); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("search err = %v, want ErrCorrupt", err)
			}
			if _, _, err := fresh.Add(ItemInput{Title: "two"}, prov); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("add err = %v, want ErrCorrupt", err)
			}
			after, _ := os.ReadFile(s.Path())
			if string(after) != string(bad) {
				t.Fatal("corrupt board was overwritten")
			}
		})
	}
}

func TestConcurrentStoresShareTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kanban")
	a, b := Open(path), Open(path)
	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := a
			if i%2 == 1 {
				s = b
			}
			if _, _, err := s.Add(ItemInput{Title: "item " + string(rune('a'+i))}, prov); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	items, err := Open(path).Search(Query{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 10 {
		t.Fatalf("got %d items, want 10", len(items))
	}
}

func TestAddDedupesWithinProject(t *testing.T) {
	s := testStore(t)
	first, _, _ := s.Add(ItemInput{Title: "Fix the race"}, prov)
	again, dup, err := s.Add(ItemInput{Title: "fix  the RACE"}, prov)
	if err != nil || !dup || again.ID != first.ID {
		t.Fatalf("dup=%v id=%s err=%v", dup, again.ID, err)
	}
	other := prov
	other.ProjectKey, other.Project = "cli-9", "cli"
	if _, dup, _ := s.Add(ItemInput{Title: "Fix the race"}, other); dup {
		t.Fatal("another project's item counted as a duplicate")
	}
	if _, err := s.Move(first.ID, Done, "", "s"); err != nil {
		t.Fatal(err)
	}
	if _, dup, _ := s.Add(ItemInput{Title: "Fix the race"}, prov); dup {
		t.Fatal("a done item counted as a duplicate")
	}
}

func TestCleaningAndCaps(t *testing.T) {
	s := testStore(t)
	title := "evil\x1b[31m‮ title " + strings.Repeat("x", 400)
	it, _, err := s.Add(ItemInput{Title: title, Body: strings.Repeat("é", MaxBodyBytes)}, prov)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(it.Title, "\x1b‮") {
		t.Fatalf("control or bidi rune survived: %q", it.Title)
	}
	if n := len([]rune(it.Title)); n > MaxTitleRunes {
		t.Fatalf("title %d runes", n)
	}
	if len(it.Body) > MaxBodyBytes || !strings.HasSuffix(it.Body, "é") {
		t.Fatalf("body cap broke a rune or overflowed: %d", len(it.Body))
	}
	if _, _, err := s.Add(ItemInput{Title: "  \n "}, prov); !errors.Is(err, ErrNoTitle) {
		t.Fatalf("blank title err = %v", err)
	}
}

func TestMoveUpdateDeleteAndLookup(t *testing.T) {
	s := testStore(t)
	it, _, _ := s.Add(ItemInput{Title: "task"}, prov)
	moved, err := s.Move(it.Short(), Blocked, "waiting on creds", "sess-2")
	if err != nil || moved.List != Blocked || moved.LastNote() != "waiting on creds" {
		t.Fatalf("move: %+v %v", moved, err)
	}
	title := "renamed"
	up, err := s.Update(strings.TrimPrefix(it.Short(), "K-"), Patch{Title: &title, Note: "more"}, "sess-2")
	if err != nil || up.Title != "renamed" || up.LastNote() != "more" {
		t.Fatalf("update: %+v %v", up, err)
	}
	if _, err := s.Move(it.ID, List("nope"), "", ""); !errors.Is(err, ErrBadList) {
		t.Fatalf("bad list err = %v", err)
	}
	if _, err := s.Delete(it.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(it.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted item still found: %v", err)
	}
	out, _ := s.Outbox()
	if len(out) != 1 || !out[0].Deleted {
		t.Fatalf("tombstone not in outbox: %+v", out)
	}
	if _, err := s.Get("K-"); !errors.Is(err, ErrNotFound) {
		t.Fatal("empty ref resolved")
	}
}

func TestSearchFilters(t *testing.T) {
	s := testStore(t)
	s.Add(ItemInput{Title: "alpha docs", List: Review}, prov)
	s.Add(ItemInput{Title: "beta tests", List: Blocked}, prov)
	other := prov
	other.Project, other.ProjectKey = "cli", "cli-1"
	s.Add(ItemInput{Title: "alpha cli"}, other)
	if got, _ := s.Search(Query{Text: "alpha"}); len(got) != 2 {
		t.Fatalf("text: %d", len(got))
	}
	if got, _ := s.Search(Query{Lists: []List{Blocked}}); len(got) != 1 || got[0].Title != "beta tests" {
		t.Fatalf("lists: %+v", got)
	}
	if got, _ := s.Search(Query{Project: "cli"}); len(got) != 1 {
		t.Fatalf("project: %d", len(got))
	}
	counts, _ := s.Counts("belai")
	if counts[Review] != 1 || counts[Blocked] != 1 || counts[Backlog] != 0 {
		t.Fatalf("counts %v", counts)
	}
}

func TestMergeLastWriterWins(t *testing.T) {
	s := testStore(t)
	it, _, _ := s.Add(ItemInput{Title: "local"}, prov)

	// An older remote edit loses to the dirty local item, but its history
	// is kept.
	older := it
	older.Title, older.Updated, older.ServerVersion = "older remote", it.Updated-1000, 3
	older.History = append(older.History, Move{ID: "m-remote", To: Review, At: it.Updated - 500})
	if _, err := s.Merge([]Item{older}, 3); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(it.ID)
	if got.Title != "local" || !got.Dirty || len(got.History) != 2 || got.ServerVersion != 3 {
		t.Fatalf("local should win: %+v", got)
	}

	// Once pushed, a newer remote edit replaces it.
	if err := s.MarkPushed([]Pushed{{ID: it.ID, Updated: got.Updated, Version: 4}}); err != nil {
		t.Fatal(err)
	}
	newer := got
	newer.Title, newer.Updated, newer.ServerVersion, newer.List = "web edit\x1b[0m", got.Updated+1000, 5, List("bogus")
	if _, err := s.Merge([]Item{newer}, 5); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(it.ID)
	if got.Title != "web edit" || got.Dirty || got.List != Review {
		t.Fatalf("remote should win and be cleaned: %+v", got)
	}
	if c, _ := s.Cursor(); c != 5 {
		t.Fatalf("cursor %d", c)
	}

	// A remote tombstone removes it.
	gone := got
	gone.Deleted, gone.Updated = true, got.Updated+1
	s.Merge([]Item{gone}, 6)
	if _, err := s.Get(it.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("remote delete not applied")
	}
}

func TestMarkPushedKeepsLaterEdits(t *testing.T) {
	s := testStore(t)
	it, _, _ := s.Add(ItemInput{Title: "x"}, prov)
	pushedAt := it.Updated
	s.now = func() time.Time { return time.UnixMilli(pushedAt + 10) }
	s.Move(it.ID, Done, "", "")
	s.MarkPushed([]Pushed{{ID: it.ID, Updated: pushedAt, Version: 1}})
	got, _ := s.Get(it.ID)
	if !got.Dirty {
		t.Fatal("an edit made during the push was marked clean")
	}
}

// fakeRemote is an in-memory backend.
type fakeRemote struct {
	mu      sync.Mutex
	items   map[string]sessionsync.KanbanItem
	version int64
	fail    error
}

func (f *fakeRemote) KanbanBatch(_ context.Context, items []sessionsync.KanbanItem) ([]sessionsync.KanbanAck, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return nil, f.fail
	}
	var acks []sessionsync.KanbanAck
	for _, it := range items {
		f.version++
		it.Version = f.version
		f.items[it.ID] = it
		acks = append(acks, sessionsync.KanbanAck{ID: it.ID, UpdatedAt: it.UpdatedAt, Version: f.version, Applied: true})
	}
	return acks, nil
}

func (f *fakeRemote) KanbanList(_ context.Context, since int64, _ int) ([]sessionsync.KanbanItem, int64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []sessionsync.KanbanItem
	for _, it := range f.items {
		if it.Version > since {
			out = append(out, it)
		}
	}
	return out, f.version, false, nil
}

func TestSyncerPushesAndPulls(t *testing.T) {
	remote := &fakeRemote{items: map[string]sessionsync.KanbanItem{}}
	a := testStore(t)
	sa := NewSyncer(a, remote, SyncOptions{})
	it, _, _ := a.Add(ItemInput{Title: "shared"}, prov)
	if err := sa.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if out, _ := a.Outbox(); len(out) != 0 {
		t.Fatalf("outbox not drained: %d", len(out))
	}

	// A second host pulls it, moves it, and the first host pulls the move.
	b := testStore(t)
	sb := NewSyncer(b, remote, SyncOptions{})
	sb.cycle(context.Background(), true)
	if _, err := b.Move(it.Short(), Done, "finished on host b", "sess-b"); err != nil {
		t.Fatal(err)
	}
	sb.cycle(context.Background(), true)
	sa.cycle(context.Background(), true)
	got, _ := a.Get(it.ID)
	if got.List != Done || got.LastNote() != "finished on host b" {
		t.Fatalf("host a did not see the move: %+v", got)
	}

	remote.fail = errors.New("HTTP 500")
	a.Move(it.ID, Review, "", "")
	sa.cycle(context.Background(), true)
	if st := sa.Status(); st.LastError == "" || st.Pending != 1 {
		t.Fatalf("status after failure: %+v", st)
	}
	remote.fail = nil
	sa.cycle(context.Background(), true)
	if st := sa.Status(); st.LastError != "" || st.Pending != 0 {
		t.Fatalf("status after retry: %+v", st)
	}
}

func TestParseList(t *testing.T) {
	for in, want := range map[string]List{"In Progress": InProgress, "in-progress": InProgress, "DONE": Done, "todo": Backlog, "blocked": Blocked, "review": Review} {
		if got, ok := ParseList(in); !ok || got != want {
			t.Errorf("ParseList(%q) = %q, %v", in, got, ok)
		}
	}
	if _, ok := ParseList("later"); ok {
		t.Error("unknown list parsed")
	}
}
