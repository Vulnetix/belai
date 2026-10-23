package sessionsync

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/repomemory"
)

// fakeMemoryServer is the server's half: notes kept by noteId, newest change
// winning, each change under a new seq.
type fakeMemoryServer struct {
	mu    sync.Mutex
	seq   int64
	notes map[string]repomemory.Note
	posts int
}

func (f *fakeMemoryServer) PostMemory(_ context.Context, hostID string, notes []repomemory.Note) (MemoryPostResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posts++
	for _, n := range notes {
		f.put(n)
	}
	return MemoryPostResult{Accepted: len(notes), LastSeq: f.seq}, nil
}

func (f *fakeMemoryServer) put(n repomemory.Note) {
	if f.notes == nil {
		f.notes = map[string]repomemory.Note{}
	}
	f.seq++
	n.Seq = f.seq
	f.notes[n.NoteID] = n
}

func (f *fakeMemoryServer) GetMemory(_ context.Context, _, _ string, after int64, limit int) (MemoryPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out MemoryPage
	for _, n := range f.notes {
		if n.Seq > after {
			out.Notes = append(out.Notes, n)
		}
	}
	out.LastSeq = f.seq
	return out, nil
}

func TestMemoryReplicationUploadPullTombstone(t *testing.T) {
	srv := &fakeMemoryServer{}
	rootA, rootB := t.TempDir(), t.TempDir()
	a := NewMemoryReplicator(srv, testHost, rootA)
	b := NewMemoryReplicator(srv, "33333333-3333-4333-8333-333333333333", rootB)
	sa, _ := repomemory.Open(rootA, "o/r")
	n, err := sa.Append("A owns x.go", []string{"x"}, repomemory.Stamp{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := a.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if got := srv.notes[n.NoteID]; got.HostID != testHost || got.Text != "A owns x.go" {
		t.Fatalf("server holds %+v", got)
	}
	// A second pass uploads nothing new.
	posts := srv.posts
	if err := a.Upload(ctx); err != nil || srv.posts != posts {
		t.Fatalf("re-upload: %v %d", err, srv.posts-posts)
	}
	if err := b.Pull(ctx); err != nil {
		t.Fatal(err)
	}
	sb, _ := repomemory.Open(rootB, "o/r")
	if got, _ := sb.Search("owns", 0); len(got) != 1 || got[0].NoteID != n.NoteID {
		t.Fatalf("b pulled %+v", got)
	}
	// The console edits the note (newer updatedAt wins), then deletes it.
	edit := srv.notes[n.NoteID]
	edit.Text, edit.UpdatedAt, edit.HostID = "A owns x.go and y.go", edit.CreatedAt+10, "web"
	srv.put(edit)
	_ = b.Pull(ctx)
	if got, _ := sb.Search("y.go", 0); len(got) != 1 {
		t.Fatalf("edit not applied: %+v", got)
	}
	// An older change never overwrites a newer one.
	stale := edit
	stale.Text, stale.UpdatedAt = "stale", edit.CreatedAt+1
	if _, err := repomemory.Apply(rootB, []repomemory.Note{stale}); err != nil {
		t.Fatal(err)
	}
	if got, _ := sb.Search("stale", 0); len(got) != 0 {
		t.Fatal("an older change won")
	}
	tomb := edit
	tomb.DeletedAt, tomb.UpdatedAt, tomb.Text = edit.UpdatedAt+1, edit.UpdatedAt+1, ""
	srv.put(tomb)
	_ = b.Pull(ctx)
	if got, _ := sb.Search("", 0); len(got) != 0 {
		t.Fatalf("tombstone did not remove: %+v", got)
	}
	// A local clear is uploaded as tombstones.
	if err := sa.Clear(); err != nil {
		t.Fatal(err)
	}
	_ = a.Upload(ctx)
	if got := srv.notes[n.NoteID]; got.DeletedAt == 0 {
		t.Fatalf("clear not uploaded: %+v", got)
	}
}

func TestMemoryHydratesAFreshMachine(t *testing.T) {
	srv := &fakeMemoryServer{}
	srv.put(repomemory.Note{NoteID: repomemory.NewNoteID(time.Now()), Scope: repomemory.ScopeRepo, ScopeKey: "o/r",
		Text: "agreed: B owns docs/", CreatedAt: time.Now().UnixMilli(), HostID: testHost})
	root := t.TempDir()
	r := NewMemoryReplicator(srv, "33333333-3333-4333-8333-333333333333", root)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx)
	// The first read waits for the hydration.
	s, _ := repomemory.Open(root, "o/r")
	got, err := s.Search("", 0)
	if err != nil || len(got) != 1 || !strings.Contains(got[0].Text, "B owns docs/") {
		t.Fatalf("hydrated read = %+v %v", got, err)
	}
	if !r.state().Hydrated {
		t.Fatal("hydration not recorded")
	}
}

func TestMemoryClientWire(t *testing.T) {
	var gotQuery, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case apiPath + "/hosts/" + testHost + "/memory":
			b := make([]byte, 4096)
			n, _ := r.Body.Read(b)
			gotBody = string(b[:n])
			w.Write([]byte(`{"accepted":1,"lastSeq":7}`))
		case apiPath + "/memory":
			gotQuery = r.URL.RawQuery
			w.Write([]byte(`{"notes":[{"seq":7,"noteId":"web-01k8z3m5x6y7z8a9b0c1d2e3f4","scope":"repo","scopeKey":"o/r","hostId":"web","text":"x","createdAt":1,"updatedAt":2}],"lastSeq":7}`))
		}
	}))
	defer srv.Close()
	c, _ := NewClient(BaseURL(srv.URL), func() (string, error) { return "ApiKey o:k", nil }, srv.Client())
	res, err := c.PostMemory(context.Background(), testHost, []repomemory.Note{{NoteID: "01k8z3m5x6y7z8a9b0c1d2e3f4", Scope: "repo", ScopeKey: "o/r", Text: "x", CreatedAt: 1}})
	if err != nil || res.LastSeq != 7 || !strings.Contains(gotBody, `"notes":[{"noteId":"01k8z3m5x6y7z8a9b0c1d2e3f4"`) {
		t.Fatalf("post %+v %v %s", res, err, gotBody)
	}
	page, err := c.GetMemory(context.Background(), "repo", "o/r", 3, 500)
	if err != nil || len(page.Notes) != 1 || page.Notes[0].HostID != "web" || page.Notes[0].UpdatedAt != 2 {
		t.Fatalf("get %+v %v", page, err)
	}
	if !strings.Contains(gotQuery, "after=3") || !strings.Contains(gotQuery, "scopeKey=o%2Fr") || !strings.Contains(gotQuery, "limit=500") {
		t.Fatalf("query %q", gotQuery)
	}
}
