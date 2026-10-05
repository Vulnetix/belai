package turnlog

import (
	"fmt"
	"testing"

	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/session"
)

func rmRows(entries []session.Entry) []session.Entry {
	var out []session.Entry
	for _, e := range entries {
		if e.Type == rolemanager.RecordType {
			out = append(out, e)
		}
	}
	return out
}

// Every decision made while a transcript is attached lands in it, in order,
// shown or not, and nothing is written after the detach.
func TestAttachRoleManagerWritesEveryDecisionInOrder(t *testing.T) {
	l, entries := newLog(t)
	detach := l.AttachRoleManager()

	const n = 1500
	for i := 0; i < n; i++ {
		if _, err := rolemanager.ParseSessionName(fmt.Sprintf("name %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	rolemanager.RecordSecurityFallback("clef/test")
	detach()
	detach() // idempotent

	rolemanager.RecordSecurityFallback("clef/test") // after the detach: not recorded
	rows := rmRows(entries())
	if len(rows) != n+1 {
		t.Fatalf("recorded %d decisions, want %d", len(rows), n+1)
	}
	var last uint64
	for i, r := range rows {
		seq, _ := r.Meta["seq"].(float64)
		if uint64(seq) <= last {
			t.Fatalf("row %d out of order: seq %v after %d", i, r.Meta["seq"], last)
		}
		last = uint64(seq)
		if r.Meta["activity"] == nil || r.Timestamp == 0 || r.Content == "" {
			t.Fatalf("row %d is incomplete: %+v", i, r)
		}
		if _, leaked := r.Meta["detail"]; leaked {
			t.Fatalf("row %d carries Detail", i)
		}
	}
	if rows[n].Meta["activity"] != string(rolemanager.EventSecurityFallback) {
		t.Fatalf("last row = %v", rows[n].Meta["activity"])
	}
}

func TestAttachRoleManagerOnANilLogIsInert(t *testing.T) {
	detach := New(nil).AttachRoleManager()
	rolemanager.RecordSecurityFallback("clef/test")
	detach()
	var nilLog *Log
	nilLog.AttachRoleManager()()
}

// Open builds a working transcript, and a store that cannot be opened yields a
// Log that still works as a no-op.
func TestOpenWritesATranscript(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	dir := t.TempDir()
	id := session.MustID()
	l, err := Open(dir, id, session.Meta{Mode: "agent"})
	if err != nil || l.Writer() == nil {
		t.Fatalf("Open = %v, %v", l, err)
	}
	l.User("hello", nil)
	detach := l.AttachRoleManager()
	rolemanager.RecordSecurityFallback("clef/test")
	detach()

	store, err := session.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	key, err := session.KeyFor(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.ReadFrom(key, id)
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]int{}
	for _, e := range got {
		types[e.Type]++
	}
	if types["session_meta"] != 1 || types["user"] != 1 || types[rolemanager.RecordType] != 1 {
		t.Fatalf("entry types = %v", types)
	}
}
