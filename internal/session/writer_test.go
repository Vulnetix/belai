package session

import (
	"strings"
	"testing"
)

func TestWriterChainsEntries(t *testing.T) {
	st := NewStoreAt(t.TempDir())
	key := Key("proj-1234")
	w, err := NewWriter(st, key, MustID(), Meta{Cwd: "/src/wt", OriginCwd: "/src/repo", ActiveProfile: "builder", Mode: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	w.User("do it")
	w.Assistant("ok", []map[string]any{{"id": "c1", "name": "Read", "args": "{}"}}, nil)
	w.Tool("c1", "Read", "{}", "done", strings.Repeat("x", MaxToolResultBytes+10))
	w.System("released K-123456 to review")
	if w.Err() != nil {
		t.Fatal(w.Err())
	}
	entries, err := st.ReadFrom(key, w.ID())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 5 {
		t.Fatalf("%d entries", len(entries))
	}
	for i := 1; i < len(entries); i++ {
		if entries[i].ParentID != entries[i-1].ID {
			t.Fatalf("entry %d not chained", i)
		}
	}
	if m, ok := LatestMeta(entries); !ok || m.ActiveProfile != "builder" {
		t.Fatalf("meta %+v", m)
	}
	if entries[3].Meta["truncated"] != true || len(entries[3].Content) > MaxToolResultBytes {
		t.Fatal("tool result not capped")
	}
}

func TestResumeWriterChainsFromTheLastEntry(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	key := Key("proj-aaaaaaaa")
	w, err := NewWriter(store, key, "s1", Meta{Cwd: "/w"})
	if err != nil {
		t.Fatal(err)
	}
	w.User("one")
	before, _ := store.ReadFrom(key, "s1")
	rw := ResumeWriter(store, key, "s1", before[len(before)-1].ID)
	rw.User("two")
	after, err := store.ReadFrom(key, "s1")
	if err != nil || len(after) != len(before)+1 {
		t.Fatalf("entries %d -> %d, %v", len(before), len(after), err)
	}
	if after[len(after)-1].ParentID != before[len(before)-1].ID {
		t.Fatal("the resumed entry does not chain from the old tail")
	}
}
