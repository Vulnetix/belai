package repomemory

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const agentID = "11111111-1111-4111-8111-111111111111:22222222-2222-4222-8222-222222222222"

func TestScopedByRepoKey(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(dir, "vulnetix/belai")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Open(dir, "vulnetix/other")
	c, _ := Open(dir, "path:0123456789abcdef")
	if _, err := a.Append("A owns internal/x/", []string{"x"}, Stamp{AgentID: agentID}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Append("pathless repo note", nil, Stamp{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := b.Search("", 0); len(got) != 0 {
		t.Fatalf("other repo sees %v", got)
	}
	if got, _ := a.Search("owns", 0); len(got) != 1 || got[0].AgentID != agentID {
		t.Fatalf("search = %+v", got)
	}
	for _, bad := range []string{"", "../etc", "Owner/Name", "a", "path:zz", "a/b/../c"} {
		if _, err := Open(dir, bad); err == nil {
			t.Errorf("key %q accepted", bad)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "repos"))
	for _, e := range entries {
		if strings.Contains(e.Name(), "/") || filepath.Dir(filepath.Join(dir, e.Name())) != dir {
			t.Fatalf("file outside the store: %s", e.Name())
		}
	}
}

func TestTwoWritersAndPruning(t *testing.T) {
	dir := t.TempDir()
	a, _ := Open(dir, "o/r")
	b, _ := Open(dir, "o/r")
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		s := a
		if i == 1 {
			s = b
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < MaxNotes; j++ {
				if _, err := s.Append("note", nil, Stamp{}); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	notes, err := a.read()
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != MaxNotes {
		t.Fatalf("%d notes kept, want %d", len(notes), MaxNotes)
	}
	if got, _ := a.Search("", 1000); len(got) != MaxLimit {
		t.Fatalf("limit not capped: %d", len(got))
	}
}

func TestNoteCleanedAndStamped(t *testing.T) {
	s, _ := Open(t.TempDir(), "o/r")
	n, err := s.Append("hi\x1b[31m there"+strings.Repeat("x", 2*MaxNoteBytes), []string{"Docs", "bad tag!", "docs"},
		Stamp{AgentID: agentID, SessionID: "s", MessageID: "a2a-01k8z3m5x6y7z8a9b0c1d2e3f4", ContextID: "nope"})
	if err != nil {
		t.Fatal(err)
	}
	if len(n.Text) > MaxNoteBytes || strings.Contains(n.Text, "\x1b") {
		t.Fatalf("text not cleaned: %d bytes", len(n.Text))
	}
	if strings.Join(n.Tags, ",") != "docs" || n.MessageID == "" || n.ContextID != "" || n.CreatedAt == 0 || n.NoteID == "" {
		t.Fatalf("note = %+v", n)
	}
	if _, err := s.Append("  ", nil, Stamp{}); err == nil {
		t.Fatal("empty note stored")
	}
	if !strings.Contains(Format("o/r", []Note{n}), "re a2a-01k8z3m5x6y7z8a9b0c1d2e3f4") {
		t.Fatal("format drops the message link")
	}
}
