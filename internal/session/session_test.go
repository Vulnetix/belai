package session

import (
	"reflect"
	"strings"
	"testing"
)

func testStore(t *testing.T, workdir string) *Store {
	t.Helper()
	return NewStoreAt(t.TempDir())
}

func TestAppendAndReadRoundTrip(t *testing.T) {
	st := testStore(t, t.TempDir())
	workdir := t.TempDir()

	e1 := Entry{ID: "a", ParentID: "", Type: "user", Content: "hello"}
	e2 := Entry{ID: "b", ParentID: "a", Type: "assistant", Content: "hi there"}
	if err := st.Append(workdir, "sess-1", e1); err != nil {
		t.Fatalf("append e1: %v", err)
	}
	if err := st.Append(workdir, "sess-1", e2); err != nil {
		t.Fatalf("append e2: %v", err)
	}

	got, err := st.Read(workdir, "sess-1")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}
	if got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("append order not preserved: %+v", got)
	}
	if got[1].ParentID != "a" {
		t.Fatalf("parent link lost: %+v", got[1])
	}
	if got[0].Timestamp == 0 || got[1].Timestamp == 0 {
		t.Fatalf("timestamps not filled: %+v", got)
	}
}

func TestAppendFillsMissingID(t *testing.T) {
	st := testStore(t, t.TempDir())
	workdir := t.TempDir()

	e := Entry{ParentID: "", Type: "user", Content: "x"}
	if err := st.Append(workdir, "sess", e); err != nil {
		t.Fatalf("append: %v", err)
	}
	got, err := st.Read(workdir, "sess")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got[0].ID == "" {
		t.Fatalf("id not generated: %+v", got[0])
	}
}

func TestBuildTree(t *testing.T) {
	entries := []Entry{
		{ID: "r1", ParentID: ""},
		{ID: "c1", ParentID: "r1"},
		{ID: "c2", ParentID: "r1"},
		{ID: "gc1", ParentID: "c1"},
		{ID: "r2", ParentID: ""},
		{ID: "orphan", ParentID: "missing"},
	}
	roots := BuildTree(entries)
	if len(roots) != 3 {
		t.Fatalf("expected 3 roots, got %d", len(roots))
	}
	byID := map[string]*Node{}
	for _, n := range roots {
		byID[n.Entry.ID] = n
	}
	r1 := byID["r1"]
	if r1 == nil {
		t.Fatalf("r1 root missing")
	}
	if len(r1.Children) != 2 {
		t.Fatalf("r1 should have 2 children, got %d", len(r1.Children))
	}
	// c1 is r1's first child
	c1 := r1.Children[0]
	if c1.Entry.ID != "c1" {
		t.Fatalf("expected c1 as first child, got %s", c1.Entry.ID)
	}
	if len(c1.Children) != 1 || c1.Children[0].Entry.ID != "gc1" {
		t.Fatalf("c1 child link wrong: %+v", c1.Children)
	}
	if byID["r2"] == nil || byID["orphan"] == nil {
		t.Fatalf("r2/orphan roots missing")
	}
}

func TestFork(t *testing.T) {
	st := testStore(t, t.TempDir())
	workdir := t.TempDir()

	parent := []Entry{
		{ID: "p1", ParentID: "", Type: "user", Content: "one"},
		{ID: "p2", ParentID: "p1", Type: "assistant", Content: "two"},
	}
	for _, e := range parent {
		if err := st.Append(workdir, "parent", e); err != nil {
			t.Fatalf("append parent: %v", err)
		}
	}
	if err := st.Fork(workdir, "parent", "child"); err != nil {
		t.Fatalf("fork: %v", err)
	}
	if err := st.Append(workdir, "child", Entry{ID: "c1", ParentID: "p2", Type: "user", Content: "three"}); err != nil {
		t.Fatalf("append child: %v", err)
	}

	gotParent, err := st.Read(workdir, "parent")
	if err != nil {
		t.Fatalf("read parent: %v", err)
	}
	if len(gotParent) != 2 {
		t.Fatalf("parent should be unchanged with 2 entries, got %d", len(gotParent))
	}

	gotChild, err := st.Read(workdir, "child")
	if err != nil {
		t.Fatalf("read child: %v", err)
	}
	if len(gotChild) != 3 {
		t.Fatalf("child should have 3 entries, got %d", len(gotChild))
	}
	if gotChild[2].ParentID != "p2" {
		t.Fatalf("child branch parent link wrong: %+v", gotChild[2])
	}
}

func TestResumeByPartialUUID(t *testing.T) {
	st := testStore(t, t.TempDir())
	workdir := t.TempDir()

	full := "11111111-2222-3333-4444-555555555555"
	if err := st.Append(workdir, full, Entry{ID: "e1", Type: "user", Content: "resume me"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	// another session sharing the prefix to exercise ambiguity detection
	other := "11111111-9999-8888-7777-666666666666"
	if err := st.Append(workdir, other, Entry{ID: "e2", Type: "user", Content: "other"}); err != nil {
		t.Fatalf("append other: %v", err)
	}

	// exact match
	id, err := st.Resolve(workdir, full)
	if err != nil {
		t.Fatalf("resolve exact: %v", err)
	}
	if id != full {
		t.Fatalf("resolve exact = %q, want %q", id, full)
	}

	// unambiguous partial (shared prefix is ambiguous)
	if _, err := st.Resolve(workdir, "11111111"); err == nil {
		t.Fatalf("expected ambiguity error for shared prefix")
	}

	// unambiguous suffix-based partial
	id, err = st.Resolve(workdir, "11111111-2222")
	if err != nil {
		t.Fatalf("resolve partial: %v", err)
	}
	if id != full {
		t.Fatalf("resolve partial = %q, want %q", id, full)
	}

	entries, err := st.Read(workdir, "11111111-2222")
	if err != nil {
		t.Fatalf("read by partial: %v", err)
	}
	if len(entries) != 1 || entries[0].Content != "resume me" {
		t.Fatalf("resume content wrong: %+v", entries)
	}
}

func TestDisplayName(t *testing.T) {
	entries := []Entry{
		{ID: "1", Type: "user", Content: "  this is\na long user message that goes on and on and on and on and on and on and on  "},
	}
	name := DisplayName(entries, "abcdefgh-1234")
	if !strings.HasPrefix(name, "this is a long") {
		t.Fatalf("unexpected display name %q", name)
	}
	if len(name) > 60 {
		t.Fatalf("display name too long: %d", len(name))
	}

	// no user content -> short id
	empty := DisplayName([]Entry{{ID: "1", Type: "assistant"}}, "abcdefgh-1234")
	if empty != "abcdefgh" {
		t.Fatalf("expected short id fallback, got %q", empty)
	}
}

func TestNameExplicit(t *testing.T) {
	entries := []Entry{
		{ID: "1", Type: "user", Content: "hello"},
		{ID: "2", Type: EntryTypeSessionName, Content: "review PR #42"},
	}
	if got := Name(entries); got != "review PR #42" {
		t.Fatalf("expected explicit name, got %q", got)
	}
	if got := DisplayName(entries, "sess-id"); got != "review PR #42" {
		t.Fatalf("DisplayName should prefer explicit name, got %q", got)
	}
}

func TestNameOverwrite(t *testing.T) {
	entries := []Entry{
		{ID: "1", Type: EntryTypeSessionName, Content: "first"},
		{ID: "2", Type: EntryTypeSessionName, Content: "second"},
	}
	if got := Name(entries); got != "second" {
		t.Fatalf("expected last name to win, got %q", got)
	}
}

func TestNameEmptyClears(t *testing.T) {
	entries := []Entry{
		{ID: "1", Type: EntryTypeSessionName, Content: "first"},
		{ID: "2", Type: EntryTypeSessionName, Content: ""},
	}
	if got := Name(entries); got != "" {
		t.Fatalf("expected empty after clear, got %q", got)
	}
}

func TestDisplayNameUnicodeTruncation(t *testing.T) {
	entries := []Entry{
		{ID: "1", Type: "user", Content: strings.Repeat("あ", 70)},
	}
	name := DisplayName(entries, "id")
	runes := []rune(name)
	if len(runes) > 60 {
		t.Fatalf("unicode truncation failed: %d runes", len(runes))
	}
}

func TestWorkdirKey(t *testing.T) {
	a := WorkdirKey("/home/user/proj")
	b := WorkdirKey("/home/user/proj")
	c := WorkdirKey("/home/user/other")
	if a != b {
		t.Fatalf("WorkdirKey not deterministic: %q vs %q", a, b)
	}
	if a == c {
		t.Fatalf("different workdirs collided: %q", a)
	}
	if !strings.HasPrefix(a, "proj-") {
		t.Fatalf("expected basename prefix, got %q", a)
	}
}

func TestSessions(t *testing.T) {
	st := testStore(t, t.TempDir())
	workdir := t.TempDir()

	if err := st.Append(workdir, "sess-aaa", Entry{ID: "e1", Type: "user", Content: "first session"}); err != nil {
		t.Fatalf("append a: %v", err)
	}
	if err := st.Append(workdir, "sess-bbb", Entry{ID: "e2", Type: "user", Content: "second session"}); err != nil {
		t.Fatalf("append b: %v", err)
	}

	infos, err := st.Sessions(workdir)
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(infos))
	}
	names := map[string]string{}
	for _, in := range infos {
		names[in.ID] = in.DisplayName
	}
	want := map[string]string{
		"sess-aaa": "first session",
		"sess-bbb": "second session",
	}
	if !reflect.DeepEqual(want, names) {
		t.Fatalf("session names mismatch:\n want=%v\n  got=%v", want, names)
	}
}

func TestNewIDUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := MustID()
		if seen[id] {
			t.Fatalf("duplicate id generated: %s", id)
		}
		seen[id] = true
	}
}
