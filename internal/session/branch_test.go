package session

import (
	"os"
	"strings"
	"testing"
)

// branched writes: meta, u1, a1, u2, a2, then navigates back to a1 and adds
// u3, a3. The tree has one fork, at a1.
func branched(t *testing.T) (*Store, Key, *Writer, []Entry) {
	t.Helper()
	st := NewStoreAt(t.TempDir())
	key := Key("proj-branch")
	w, err := NewWriter(st, key, MustID(), Meta{Cwd: "/src"})
	if err != nil {
		t.Fatal(err)
	}
	w.User("first")
	w.Assistant("one", nil, nil)
	w.User("second")
	w.Assistant("two", nil, nil)
	entries, err := st.ReadFrom(key, w.ID())
	if err != nil {
		t.Fatal(err)
	}
	if w.Branch(entries[2].ID) == "" {
		t.Fatal("branch refused")
	}
	w.User("third")
	w.Assistant("three", nil, nil)
	entries, err = st.ReadFrom(key, w.ID())
	if err != nil {
		t.Fatal(err)
	}
	return st, key, w, entries
}

func texts(es []Entry) string {
	var out []string
	for _, e := range es {
		if e.Type == "user" || e.Type == "assistant" {
			out = append(out, e.Content)
		}
	}
	return strings.Join(out, ",")
}

func TestActivePathFollowsTheBranch(t *testing.T) {
	_, _, _, entries := branched(t)
	if !HasBranches(entries) {
		t.Fatal("no marker found")
	}
	if got := texts(ActivePath(entries)); got != "first,one,third,three" {
		t.Fatalf("active path %q", got)
	}
	if got := texts(entries); got != "first,one,second,two,third,three" {
		t.Fatalf("file order %q", got)
	}
}

func TestUnnavigatedSessionStaysFlat(t *testing.T) {
	entries := []Entry{{ID: "a"}, {ID: "b"}, {ID: "c", ParentID: "a"}}
	if got := ActivePath(entries); len(got) != 3 {
		t.Fatalf("flat file changed: %d", len(got))
	}
}

func TestPathEdgeCases(t *testing.T) {
	entries := []Entry{
		{ID: "a", ParentID: "c"}, {ID: "b", ParentID: "a"}, {ID: "c", ParentID: "b"}, // cycle
		{ID: "x", ParentID: "gone"}, {ID: "x", ParentID: "a"}, // orphan, duplicate id
	}
	if got := Path(entries, "c"); len(got) != 3 {
		t.Fatalf("cycle not cut: %d", len(got))
	}
	if got := Path(entries, "x"); len(got) != 1 {
		t.Fatalf("orphan chain: %d", len(got))
	}
	if Path(entries, "nope") != nil || Path(nil, "") != nil {
		t.Fatal("unknown leaf should be nil")
	}
}

func TestTreeRows(t *testing.T) {
	_, _, _, entries := branched(t)
	rows := TreeRows(entries, false)
	var labels []string
	forks, leaves := 0, 0
	for _, r := range rows {
		labels = append(labels, r.Label)
		if r.Fork {
			forks++
			if r.Entry.Content != "one" {
				t.Fatalf("fork at %q", r.Entry.Content)
			}
		}
		if r.Leaf {
			leaves++
			if r.Entry.Content != "three" {
				t.Fatalf("leaf %q", r.Entry.Content)
			}
		}
	}
	if forks != 1 || leaves != 1 {
		t.Fatalf("forks %d leaves %d", forks, leaves)
	}
	if len(rows) != 6 || rows[2].Depth != 1 || rows[0].Depth != 0 {
		t.Fatalf("rows %v depths %d %d", labels, rows[0].Depth, rows[2].Depth)
	}
	if rows[2].OnPath || !rows[4].OnPath {
		t.Fatal("old branch should be off the active path")
	}
}

func TestLabelIsOneCleanLine(t *testing.T) {
	e := Entry{Type: "user", Content: "a\x1b[31m\nb\u0085c " + strings.Repeat("z", 200)}
	l := rowLabel(e)
	if strings.ContainsAny(l, "\n\x1b\u0085") || len([]rune(l)) > 90 {
		t.Fatalf("label %q", l)
	}
}

func TestTarget(t *testing.T) {
	leaf, prefill, err := Target(Entry{ID: "u", ParentID: "p", Type: "user", Content: "hi"})
	if err != nil || leaf != "p" || prefill != "hi" {
		t.Fatalf("user: %q %q %v", leaf, prefill, err)
	}
	leaf, _, err = Target(Entry{ID: "a", Type: "assistant"})
	if err != nil || leaf != "a" {
		t.Fatalf("assistant: %q %v", leaf, err)
	}
	if _, _, err = Target(Entry{ID: "a", Type: "assistant", Meta: map[string]any{"tool_calls": []any{1}}}); err == nil {
		t.Fatal("assistant with tool calls must not be a target")
	}
	if _, _, err = Target(Entry{ID: "t", Type: "tool"}); err == nil {
		t.Fatal("tool row must not be a target")
	}
}

func TestFindEntry(t *testing.T) {
	es := []Entry{{ID: "abc123"}, {ID: "abd456"}}
	if e, err := FindEntry(es, "abc"); err != nil || e.ID != "abc123" {
		t.Fatalf("%v %v", e, err)
	}
	if _, err := FindEntry(es, "ab"); err == nil {
		t.Fatal("ambiguous prefix accepted")
	}
	if _, err := FindEntry(es, "zz"); err == nil {
		t.Fatal("unknown id accepted")
	}
}

func TestBranchKeepsFileAppendOnly(t *testing.T) {
	st := NewStoreAt(t.TempDir())
	key := Key("proj-append")
	w, _ := NewWriter(st, key, MustID(), Meta{})
	w.User("a")
	w.Assistant("b", nil, nil)
	path := st.sessionPathForKey(key, w.ID())
	before, _ := os.ReadFile(path)
	entries, _ := st.ReadFrom(key, w.ID())
	w.Branch(entries[1].ID)
	after, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(after), string(before)) || len(after) <= len(before) {
		t.Fatal("branching must only append")
	}
	if w.Branch("") != "" {
		t.Fatal("empty target accepted")
	}
}

func TestForkPathCopiesOnlyTheChain(t *testing.T) {
	st, key, w, entries := branched(t)
	// Fork the abandoned branch: its leaf is "two".
	var leaf string
	for _, e := range entries {
		if e.Content == "two" {
			leaf = e.ID
		}
	}
	newID := MustID()
	if err := st.ForkPath(key, w.ID(), key, newID, leaf); err != nil {
		t.Fatal(err)
	}
	got, err := st.ReadFrom(key, newID)
	if err != nil {
		t.Fatal(err)
	}
	if texts(got) != "first,one,second,two" {
		t.Fatalf("fork holds %q", texts(got))
	}
	if m, _ := LatestMeta(got); m.ResumedFrom != w.ID() {
		t.Fatalf("resumedFrom %q", m.ResumedFrom)
	}
	if got[len(got)-1].ParentID != leaf {
		t.Fatal("meta not chained to the leaf")
	}
	if err := st.ForkPath(key, w.ID(), key, newID, leaf); err == nil {
		t.Fatal("existing destination overwritten")
	}
	if err := st.ForkPath(key, w.ID(), key, MustID(), "nope"); err == nil {
		t.Fatal("unknown leaf accepted")
	}
}
