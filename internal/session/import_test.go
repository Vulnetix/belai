package session

import (
	"strings"
	"testing"
)

func TestImportWritesANewSessionAndNeverReplacesOne(t *testing.T) {
	s := NewStoreAt(t.TempDir())
	key, _ := KeyFor(t.TempDir())
	id := MustID()
	root := Meta{Schema: SchemaVersion, Cwd: "/here", TeleportedFrom: "origin"}.ToEntry("")
	entries := []Entry{root, {ID: "u1", ParentID: root.ID, Type: "user", Role: "user", Content: "hi"}}

	if err := s.Import(key, id, entries); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadFrom(key, id)
	if err != nil || len(got) != 2 || got[1].Content != "hi" {
		t.Fatalf("read back = %+v %v", got, err)
	}
	if m, _ := LatestMeta(got); m.TeleportedFrom != "origin" {
		t.Fatalf("meta = %+v", m)
	}
	// A second import under the same id is refused and leaves the first alone.
	if err := s.Import(key, id, entries[:1]); err == nil {
		t.Fatal("an import replaced a session")
	}
	if again, _ := s.ReadFrom(key, id); len(again) != 2 {
		t.Fatalf("the first session was changed: %d entries", len(again))
	}
}

func TestImportRefusesAnIdThatIsNotASessionId(t *testing.T) {
	s := NewStoreAt(t.TempDir())
	key, _ := KeyFor(t.TempDir())
	for _, id := range []string{"", "../escape", "ABCDEFAB-1111-4111-8111-111111111111", strings.Repeat("a", 36)} {
		if err := s.Import(key, id, nil); err == nil {
			t.Errorf("import accepted %q", id)
		}
		if err := s.Remove(key, id); err == nil {
			t.Errorf("remove accepted %q", id)
		}
	}
}

func TestRemoveDeletesAnImportAndToleratesAMissingOne(t *testing.T) {
	s := NewStoreAt(t.TempDir())
	key, _ := KeyFor(t.TempDir())
	id := MustID()
	if err := s.Import(key, id, []Entry{{ID: "a", Type: "user"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(key, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadFrom(key, id); err == nil {
		t.Fatal("the session is still there")
	}
	if err := s.Remove(key, id); err != nil {
		t.Fatalf("removing a missing session: %v", err)
	}
}
