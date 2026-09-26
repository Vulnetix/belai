package session

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
)

func testStore(t *testing.T, workdir string) *Store {
	t.Helper()
	return NewStoreAt(t.TempDir())
}

func TestSessionPathAccessor(t *testing.T) {
	st := NewStoreAt(t.TempDir())
	k, err := KeyFor(t.TempDir())
	if err != nil {
		t.Fatalf("KeyFor: %v", err)
	}
	got := st.SessionPath(k, "sess-1")
	if got != st.sessionPathForKey(k, "sess-1") {
		t.Fatalf("SessionPath = %q, want %q", got, st.sessionPathForKey(k, "sess-1"))
	}
	if !strings.HasSuffix(got, filepath.Join(string(k), "sess-1.jsonl")) {
		t.Fatalf("SessionPath = %q, want a .jsonl path under the key", got)
	}
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

func TestKeyForAndProject(t *testing.T) {
	workdir := t.TempDir()
	k, err := KeyFor(workdir)
	if err != nil {
		t.Fatalf("KeyFor: %v", err)
	}
	if string(k) != WorkdirKey(workdir) {
		t.Fatalf("KeyFor = %q, WorkdirKey = %q", k, WorkdirKey(workdir))
	}
	base := filepath.Base(workdir)
	if got := k.Project(); got != base {
		t.Fatalf("Project() = %q, want basename %q", got, base)
	}
}

func TestMetaRoundTripAndMerge(t *testing.T) {
	m1 := Meta{Schema: 1, Cwd: "/a", Version: "v1", CreatedAt: 1, Mode: "agent", ActivePlan: "p1"}
	e1 := m1.ToEntry("")
	if e1.Type != EntryTypeSessionMeta {
		t.Fatalf("ToEntry type = %q", e1.Type)
	}
	got1, err := MetaFromEntry(e1)
	if err != nil {
		t.Fatalf("MetaFromEntry: %v", err)
	}
	if got1.Schema == 0 || got1.Cwd != "/a" || got1.ActivePlan != "p1" {
		t.Fatalf("round trip mismatch: %+v", got1)
	}

	// Merge: later non-zero fields win, earlier retained otherwise.
	m2 := Meta{Schema: 2, ActiveGoal: "g1"}
	entries := []Entry{m1.ToEntry(""), m2.ToEntry("")}
	merged, ok := LatestMeta(entries)
	if !ok {
		t.Fatal("LatestMeta not found")
	}
	if merged.Cwd != "/a" || merged.Schema != 2 || merged.ActivePlan != "p1" || merged.ActiveGoal != "g1" || merged.Version != "v1" {
		t.Fatalf("merge mismatch: %+v", merged)
	}
}

func TestLatestMetaSkipsMalformed(t *testing.T) {
	good := Meta{Schema: 2, Cwd: "/a"}.ToEntry("")
	malformed := Entry{ID: "x", Type: EntryTypeSessionMeta, Content: "{not json"}
	merged, ok := LatestMeta([]Entry{malformed, good})
	if !ok {
		t.Fatal("LatestMeta should skip malformed and find good")
	}
	if merged.Cwd != "/a" {
		t.Fatalf("malformed entry was fatal or overwrote: %+v", merged)
	}
}

func TestMetaToEntryDefaults(t *testing.T) {
	e := (Meta{}).ToEntry("")
	m, err := MetaFromEntry(e)
	if err != nil {
		t.Fatalf("MetaFromEntry: %v", err)
	}
	if m.Schema != SchemaVersion {
		t.Fatalf("Schema defaulted to %d, want %d", m.Schema, SchemaVersion)
	}
	if m.CreatedAt == 0 {
		t.Fatal("CreatedAt not defaulted")
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

// The on-disk key is shared by every per-project store. Pin a literal so a
// refactor of the derivation can never orphan an existing session directory.
func TestWorkdirKeyLiteral(t *testing.T) {
	if got, want := WorkdirKey("/home/user/proj"), "proj-7d73bf4f"; got != want {
		t.Fatalf("WorkdirKey = %q, want %q", got, want)
	}
	if got := config.WorkdirKey("/home/user/proj"); got != WorkdirKey("/home/user/proj") {
		t.Fatalf("config.WorkdirKey = %q, want session.WorkdirKey %q", got, WorkdirKey("/home/user/proj"))
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

func TestUserPromptsDedupedAndOrdered(t *testing.T) {
	st := testStore(t, t.TempDir())
	workdir := t.TempDir()

	if err := st.Append(workdir, "sess-old", Entry{ID: "a", Type: "user", Role: "user", Content: "duplicate"}); err != nil {
		t.Fatalf("append old: %v", err)
	}
	if err := st.Append(workdir, "sess-old", Entry{ID: "b", Type: "assistant", Role: "assistant", Content: "ok"}); err != nil {
		t.Fatalf("append old assistant: %v", err)
	}

	if err := st.Append(workdir, "sess-new", Entry{ID: "c", Type: "user", Role: "user", Content: "unique new"}); err != nil {
		t.Fatalf("append new: %v", err)
	}
	if err := st.Append(workdir, "sess-new", Entry{ID: "d", Type: "user", Role: "user", Content: "duplicate"}); err != nil {
		t.Fatalf("append new dup: %v", err)
	}

	prompts, err := st.UserPrompts(workdir)
	if err != nil {
		t.Fatalf("UserPrompts: %v", err)
	}
	// Most recently appended first across sessions (sessions ordered by most
	// recent modification); the duplicated "duplicate" prompt keeps its most
	// recent occurrence, which came after "unique new".
	want := []string{"duplicate", "unique new"}
	if !reflect.DeepEqual(prompts, want) {
		t.Fatalf("got %v, want %v", prompts, want)
	}
}

func TestUserPromptsMissingDir(t *testing.T) {
	st := testStore(t, t.TempDir())
	prompts, err := st.UserPrompts(t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(prompts) != 0 {
		t.Fatalf("expected empty, got %v", prompts)
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

func TestTimedUserPromptsOldestFirstWithModTimeFallback(t *testing.T) {
	st := testStore(t, t.TempDir())
	workdir := t.TempDir()

	if err := st.Append(workdir, "sess-1", Entry{ID: "a", Type: "user", Role: "user", Content: "stamped", Timestamp: 42}); err != nil {
		t.Fatal(err)
	}
	if err := st.Append(workdir, "sess-1", Entry{ID: "b", Type: "assistant", Role: "assistant", Content: "ok"}); err != nil {
		t.Fatal(err)
	}
	// Append stamps every entry it writes, so an unstamped row can only come
	// from an older store: write those lines to the file directly.
	pre, err := st.Sessions(workdir)
	if err != nil || len(pre) != 1 {
		t.Fatalf("sessions = %v, %v", pre, err)
	}
	f, err := os.OpenFile(pre[0].Path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(`{"id":"c","type":"user","role":"user","content":"unstamped"}` + "\n" + `{"id":"d","type":"user","role":"user","content":""}` + "\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}

	got, err := st.TimedUserPrompts(workdir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Content != "stamped" || got[1].Content != "unstamped" {
		t.Fatalf("got %+v, want stamped then unstamped, empty and assistant rows skipped", got)
	}
	if got[0].Timestamp != 42 {
		t.Fatalf("stamped ts = %d, want 42", got[0].Timestamp)
	}
	infos, _ := st.Sessions(workdir)
	if got[1].Timestamp != infos[0].ModTime {
		t.Fatalf("unstamped ts = %d, want session mod time %d", got[1].Timestamp, infos[0].ModTime)
	}
}

func TestNewStoreRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BELAI_HOME", home)
	st, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	want, err := config.SessionsDir()
	if err != nil {
		t.Fatalf("SessionsDir: %v", err)
	}
	if st.Root != want {
		t.Fatalf("Root = %q, want %q", st.Root, want)
	}
}

func TestKeyString(t *testing.T) {
	if got := Key("belai-275e7780").String(); got != "belai-275e7780" {
		t.Fatalf("String() = %q", got)
	}
}

func TestSessionPathUnresolved(t *testing.T) {
	st := NewStoreAt(t.TempDir())
	workdir := t.TempDir()
	got, err := st.sessionPath(workdir, "sess-1")
	if err != nil {
		t.Fatalf("sessionPath: %v", err)
	}
	k, err := KeyFor(workdir)
	if err != nil {
		t.Fatalf("KeyFor: %v", err)
	}
	if want := st.sessionPathForKey(k, "sess-1"); got != want {
		t.Fatalf("sessionPath = %q, want %q", got, want)
	}
}

func TestResolveForAppendCreatesOnDemand(t *testing.T) {
	st := NewStoreAt(t.TempDir())
	workdir := t.TempDir()

	// A brand-new id is returned as-is: append-only stores create on demand.
	got, err := st.resolveForAppend(workdir, "brand-new")
	if err != nil {
		t.Fatalf("resolveForAppend: %v", err)
	}
	if got != "brand-new" {
		t.Fatalf("got %q, want brand-new", got)
	}

	// An existing id resolves to itself (the shared single-match branch).
	if err := st.Append(workdir, "existing", Entry{ID: "e1", Type: "user", Content: "x"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	got, err = st.resolveForAppend(workdir, "existing")
	if err != nil {
		t.Fatalf("resolveForAppend existing: %v", err)
	}
	if got != "existing" {
		t.Fatalf("got %q, want existing", got)
	}

	// Empty id is rejected.
	if _, err := st.resolveForAppend(workdir, ""); err == nil {
		t.Fatal("expected empty id to error")
	}
}
