package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// keyWorkdir builds a Key from a temp dir and returns both.
func keyWorkdir(t *testing.T) (Key, string) {
	t.Helper()
	wd := t.TempDir()
	k, err := KeyFor(wd)
	if err != nil {
		t.Fatalf("KeyFor: %v", err)
	}
	return k, wd
}

func TestKeyAddressedRoundTrip(t *testing.T) {
	st := testStore(t, t.TempDir())
	k, _ := keyWorkdir(t)

	e1 := Entry{ID: "a", ParentID: "", Type: "user", Content: "hello"}
	e2 := Entry{ID: "b", ParentID: "a", Type: "assistant", Content: "hi"}
	if err := st.AppendTo(k, "sess-1", e1); err != nil {
		t.Fatalf("append e1: %v", err)
	}
	if err := st.AppendTo(k, "sess-1", e2); err != nil {
		t.Fatalf("append e2: %v", err)
	}

	id, err := st.ResolveIn(k, "sess-")
	if err != nil {
		t.Fatalf("ResolveIn: %v", err)
	}
	if id != "sess-1" {
		t.Fatalf("ResolveIn = %q", id)
	}
	entries, err := st.ReadFrom(k, id)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("ReadFrom got %d entries", len(entries))
	}
	infos, err := st.SessionsIn(k)
	if err != nil {
		t.Fatalf("SessionsIn: %v", err)
	}
	if len(infos) != 1 || infos[0].ID != "sess-1" {
		t.Fatalf("SessionsIn = %+v", infos)
	}
	if infos[0].Key != k {
		t.Fatalf("SessionsIn Key = %q, want %q", infos[0].Key, k)
	}
	if infos[0].Turns != 1 || infos[0].Provider != "" || infos[0].Model != "" {
		t.Fatalf("session info fields wrong: %+v", infos[0])
	}
}

func TestSessionInfoEnrichedFields(t *testing.T) {
	st := testStore(t, t.TempDir())
	k, _ := keyWorkdir(t)

	entries := []Entry{
		{ID: "u1", Type: "user", Content: "one"},
		{ID: "a1", ParentID: "u1", Type: "assistant", Content: "ok", Meta: map[string]any{"model": "m1", "provider": "p1"}},
		{ID: "t1", ParentID: "a1", Type: "tool", Content: "result"},
		{ID: "u2", ParentID: "t1", Type: "user", Content: "two"},
	}
	for _, e := range entries {
		if err := st.AppendTo(k, "sess", e); err != nil {
			t.Fatalf("append %s: %v", e.ID, err)
		}
	}
	infos, err := st.SessionsIn(k)
	if err != nil {
		t.Fatalf("SessionsIn: %v", err)
	}
	si := infos[0]
	if si.Turns != 2 {
		t.Fatalf("Turns = %d, want 2", si.Turns)
	}
	if si.Model != "m1" || si.Provider != "p1" {
		t.Fatalf("model/provider = %q/%q", si.Model, si.Provider)
	}
	if !si.HasTools {
		t.Fatal("HasTools = false, want true")
	}
	if si.Compacted {
		t.Fatal("Compacted = true, want false")
	}
}

func TestAllSessionsCurrentFirst(t *testing.T) {
	st := testStore(t, t.TempDir())
	cur, curWD := keyWorkdir(t)
	other, _ := keyWorkdir(t)

	// Other project has a newer session.
	if err := st.AppendTo(other, "o1", Entry{ID: "x1", Type: "user", Content: "other"}); err != nil {
		t.Fatalf("append other: %v", err)
	}
	_ = os.Chtimes(filepath.Join(st.dirForKey(other), "o1.jsonl"), time.UnixMilli(200), time.UnixMilli(200))

	// Current project has an older one (plus a session_meta so its workdir is
	// recoverable).
	if err := st.AppendTo(cur, "c1", Entry{ID: "y1", Type: "user", Content: "current"}); err != nil {
		t.Fatalf("append current: %v", err)
	}
	if err := st.AppendTo(cur, "c1", (Meta{Schema: 2, Cwd: curWD}).ToEntry("y1")); err != nil {
		t.Fatalf("append meta: %v", err)
	}
	_ = os.Chtimes(filepath.Join(st.dirForKey(cur), "c1.jsonl"), time.UnixMilli(100), time.UnixMilli(100))

	groups, err := st.AllSessions(cur)
	if err != nil {
		t.Fatalf("AllSessions: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("groups = %d, want 2", len(groups))
	}
	if !groups[0].Current || groups[0].Key != cur {
		t.Fatalf("current group not first: %+v", groups[0])
	}
	if groups[0].Workdir != curWD {
		t.Fatalf("current Workdir = %q, want %q", groups[0].Workdir, curWD)
	}
	if groups[1].Current || groups[1].Key != other {
		t.Fatalf("other group wrong: %+v", groups[1])
	}
}

func TestAllSessionsUnknownWorkdir(t *testing.T) {
	st := testStore(t, t.TempDir())
	k, _ := keyWorkdir(t)
	if err := st.AppendTo(k, "s1", Entry{ID: "x1", Type: "user", Content: "no meta"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	groups, err := st.AllSessions(k)
	if err != nil {
		t.Fatalf("AllSessions: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("groups = %d", len(groups))
	}
	if groups[0].Workdir != "" {
		t.Fatalf("Workdir = %q, want empty", groups[0].Workdir)
	}
	if !strings.Contains(groups[0].Label, "path unknown") {
		t.Fatalf("Label = %q, want path unknown", groups[0].Label)
	}
}

func TestAllSessionsRejectsMismatchedCwd(t *testing.T) {
	st := testStore(t, t.TempDir())
	k, _ := keyWorkdir(t)
	// Record a cwd that does not hash to this directory.
	if err := st.AppendTo(k, "s1", Entry{ID: "x1", Type: "user", Content: "copied"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := st.AppendTo(k, "s1", (Meta{Schema: 2, Cwd: "/some/other/machine"}).ToEntry("x1")); err != nil {
		t.Fatalf("append meta: %v", err)
	}
	groups, err := st.AllSessions(k)
	if err != nil {
		t.Fatalf("AllSessions: %v", err)
	}
	if groups[0].Workdir != "" {
		t.Fatalf("Workdir = %q, want empty (integrity check)", groups[0].Workdir)
	}
}

func TestForkAcrossCopiesAndRefusesExisting(t *testing.T) {
	st := testStore(t, t.TempDir())
	src, _ := keyWorkdir(t)
	dst, _ := keyWorkdir(t)

	if err := st.AppendTo(src, "parent", Entry{ID: "p1", Type: "user", Content: "one"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := st.AppendTo(src, "parent", Entry{ID: "p2", ParentID: "p1", Type: "assistant", Content: "two"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := st.ForkAcross(src, "parent", dst, "child"); err != nil {
		t.Fatalf("ForkAcross: %v", err)
	}
	child, err := st.ReadFrom(dst, "child")
	if err != nil {
		t.Fatalf("ReadFrom child: %v", err)
	}
	if len(child) != 2 || child[1].ParentID != "p1" {
		t.Fatalf("child wrong: %+v", child)
	}
	// Refusing a pre-existing destination (O_EXCL).
	if err := st.ForkAcross(src, "parent", dst, "child"); err == nil {
		t.Fatal("expected ForkAcross to refuse existing destination")
	}
}

func TestSessionsInSkipsOversizedFile(t *testing.T) {
	st := testStore(t, t.TempDir())
	k, _ := keyWorkdir(t)
	dir := st.dirForKey(k)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Write a file larger than maxListBytes but with a valid first line.
	path := filepath.Join(dir, "big.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.WriteString(`{"id":"b1","type":"user","content":"big"}` + "\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := f.Write(make([]byte, maxListBytes+1)); err != nil {
		t.Fatalf("pad: %v", err)
	}
	f.Close()

	infos, err := st.SessionsIn(k)
	if err != nil {
		t.Fatalf("SessionsIn: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("infos = %d", len(infos))
	}
	if infos[0].DisplayName != "big" {
		t.Fatalf("DisplayName = %q, want fallback short id", infos[0].DisplayName)
	}
	if infos[0].Turns != 0 {
		t.Fatalf("oversized file should not be parsed, Turns = %d", infos[0].Turns)
	}
}

func TestResolveAnywherePrefersCurrent(t *testing.T) {
	st := testStore(t, t.TempDir())
	cur, _ := keyWorkdir(t)
	other, _ := keyWorkdir(t)

	if err := st.AppendTo(cur, "aaaa1111-0000", Entry{ID: "e1", Type: "user", Content: "cur"}); err != nil {
		t.Fatalf("append cur: %v", err)
	}
	if err := st.AppendTo(other, "bbbb2222-0000", Entry{ID: "e2", Type: "user", Content: "other"}); err != nil {
		t.Fatalf("append other: %v", err)
	}

	key, id, err := st.ResolveAnywhere(cur, "aaaa1111")
	if err != nil {
		t.Fatalf("ResolveAnywhere cur: %v", err)
	}
	if key != cur || id != "aaaa1111-0000" {
		t.Fatalf("preferred = %q/%q", key, id)
	}

	key, id, err = st.ResolveAnywhere(cur, "bbbb2222")
	if err != nil {
		t.Fatalf("ResolveAnywhere other: %v", err)
	}
	if key != other || id != "bbbb2222-0000" {
		t.Fatalf("scanned = %q/%q", key, id)
	}

	if _, _, err := st.ResolveAnywhere(cur, "zzzz"); err == nil {
		t.Fatal("expected unknown id error")
	}
}

func TestResolveAnywhereAmbiguityListsProjects(t *testing.T) {
	st := testStore(t, t.TempDir())
	pref, _ := keyWorkdir(t) // no matching session here
	a, _ := keyWorkdir(t)
	b, _ := keyWorkdir(t)

	if err := st.AppendTo(a, "cccc3333-0000", Entry{ID: "e1", Type: "user", Content: "a"}); err != nil {
		t.Fatalf("append a: %v", err)
	}
	if err := st.AppendTo(b, "cccc3333-1111", Entry{ID: "e2", Type: "user", Content: "b"}); err != nil {
		t.Fatalf("append b: %v", err)
	}
	_, _, err := st.ResolveAnywhere(pref, "cccc3333")
	if err == nil {
		t.Fatal("expected ambiguity error")
	}
	msg := err.Error()
	if !strings.Contains(msg, a.Project()) || !strings.Contains(msg, b.Project()) {
		t.Fatalf("ambiguity error must name both projects: %q", msg)
	}
}
