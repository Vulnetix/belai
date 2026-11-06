package headless

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/session"
)

func seed(t *testing.T, store *session.Store, key session.Key, id string, fill func(w *session.Writer)) {
	t.Helper()
	w, err := session.NewWriter(store, key, id, session.Meta{Cwd: "/work"})
	if err != nil {
		t.Fatal(err)
	}
	fill(w)
	if err := w.Err(); err != nil {
		t.Fatal(err)
	}
}

func calls(ids ...string) []map[string]any {
	var out []map[string]any
	for _, id := range ids {
		out = append(out, map[string]any{"id": id, "name": "Read", "args": `{"path":"a.go"}`})
	}
	return out
}

func conversation(w *session.Writer) {
	w.User("read a.go")
	w.Assistant("", calls("c1", "c2"), nil)
	w.Tool("c1", "Read", `{"path":"a.go"}`, "done", "package a")
	w.Tool("c2", "Read", `{"path":"a.go"}`, "done", "package a again")
	w.Assistant("it is package a", nil, nil)
	w.System("a harness line")
	w.User("now b.go")
	w.Assistant("on it", nil, nil)
}

func TestTurnsFromEntriesPairsCallsAndResults(t *testing.T) {
	store := session.NewStoreAt(t.TempDir())
	key := session.Key("proj-aaaaaaaa")
	seed(t, store, key, "s1", conversation)
	entries, err := store.ReadFrom(key, "s1")
	if err != nil {
		t.Fatal(err)
	}
	turns := TurnsFromEntries(session.ActivePath(entries))
	var roles []string
	for _, tu := range turns {
		roles = append(roles, tu.Role)
	}
	if got := strings.Join(roles, ","); got != "user,assistant,tool,tool,assistant,user,assistant" {
		t.Fatalf("roles = %s", got)
	}
	if len(turns[1].ToolCalls) != 2 || turns[1].ToolCalls[0].Args["path"] != "a.go" {
		t.Fatalf("tool calls = %+v", turns[1].ToolCalls)
	}
	if turns[2].ToolCallID != "c1" || turns[3].ToolCallID != "c2" || turns[2].Content != "package a" {
		t.Fatalf("tool turns = %+v %+v", turns[2], turns[3])
	}
}

func TestTurnsFromEntriesDropsOrphans(t *testing.T) {
	store := session.NewStoreAt(t.TempDir())
	key := session.Key("proj-aaaaaaaa")
	seed(t, store, key, "s1", func(w *session.Writer) {
		w.User("go")
		w.Assistant("", calls("keep", "lost"), nil)
		w.Tool("keep", "Read", "{}", "done", "ok")
		w.Tool("ghost", "Read", "{}", "done", "no call asked for this")
		w.Assistant("done", nil, nil)
	})
	entries, _ := store.ReadFrom(key, "s1")
	turns := TurnsFromEntries(entries)
	if len(turns[1].ToolCalls) != 1 || turns[1].ToolCalls[0].ID != "keep" {
		t.Fatalf("the call with no result was kept: %+v", turns[1].ToolCalls)
	}
	for _, tu := range turns {
		if tu.ToolCallID == "ghost" {
			t.Fatal("a result with no call was kept")
		}
	}
}

func TestTurnsFromEntriesSkipsSubagentsAndRenderRows(t *testing.T) {
	entries := []session.Entry{
		{ID: "1", Type: "user", Content: "hi"},
		{ID: "2", Type: "reasoning", Content: "thinking"},
		{ID: "3", Type: "assistant", Content: "sub", SubagentID: "agent-1"},
		{ID: "4", Type: "rolemanager", Content: "SAFE"},
		{ID: "5", Type: "assistant", Content: "hello"},
	}
	turns := TurnsFromEntries(entries)
	if len(turns) != 2 || turns[1].Content != "hello" {
		t.Fatalf("turns = %+v", turns)
	}
}

func TestTurnsFromEntriesStartsAtTheLastSummary(t *testing.T) {
	entries := []session.Entry{
		{ID: "1", Type: "user", Content: "old"},
		{ID: "2", Type: "assistant", Content: "old reply"},
		{ID: "3", Type: "summary", Content: "we fixed X"},
		{ID: "4", Type: "user", Content: "next"},
	}
	turns := TurnsFromEntries(entries)
	if len(turns) != 3 || !strings.Contains(turns[0].Content, "we fixed X") ||
		turns[1].Content != rolemanager.SummaryAck || turns[2].Content != "next" {
		t.Fatalf("turns = %+v", turns)
	}
}

func TestAdjacentUserTurnsMerge(t *testing.T) {
	turns := TurnsFromEntries([]session.Entry{
		{ID: "1", Type: "user", Content: "a"},
		{ID: "2", Type: "user", Content: "b"},
	})
	if len(turns) != 1 || turns[0].Content != "a\n\nb" {
		t.Fatalf("turns = %+v", turns)
	}
}

func TestLoadResumeContinuesTheSameTranscript(t *testing.T) {
	store := session.NewStoreAt(t.TempDir())
	cur := session.Key("proj-aaaaaaaa")
	seed(t, store, cur, "s1", conversation)
	before, _ := store.ReadFrom(cur, "s1")

	r, err := LoadResume(store, cur, cur, "s1", true)
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != "s1" || r.Key != cur || r.Forked != "" || len(r.History) == 0 {
		t.Fatalf("resumed = %+v", r)
	}
	r.Writer.User("and c.go")
	after, _ := store.ReadFrom(cur, "s1")
	if len(after) != len(before)+1 {
		t.Fatalf("entries %d -> %d, want one appended to the same file", len(before), len(after))
	}
	last := after[len(after)-1]
	if last.ParentID != before[len(before)-1].ID {
		t.Fatalf("the new entry parents to %q, want the old tail %q (one root)", last.ParentID, before[len(before)-1].ID)
	}
	roots := 0
	for _, e := range after {
		if e.ParentID == "" {
			roots++
		}
	}
	if roots != 1 {
		t.Fatalf("%d roots, want 1", roots)
	}
}

func TestLoadResumeFromAnotherProjectForksIntoThisOne(t *testing.T) {
	store := session.NewStoreAt(t.TempDir())
	cur := session.Key("here-aaaaaaaa")
	other := session.Key("there-bbbbbbbb")
	seed(t, store, other, "s1", conversation)

	r, err := LoadResume(store, cur, other, "s1", true)
	if err != nil {
		t.Fatal(err)
	}
	if r.Key != cur || r.ID == "s1" || r.Forked != "s1" {
		t.Fatalf("resumed = %+v", r)
	}
	if _, err := store.ReadFrom(cur, r.ID); err != nil {
		t.Fatalf("the fork is missing: %v", err)
	}
	orig, _ := store.ReadFrom(other, "s1")
	if len(orig) == 0 {
		t.Fatal("the original is gone")
	}
	r.Writer.User("more")
	again, _ := store.ReadFrom(other, "s1")
	if len(again) != len(orig) {
		t.Fatal("the original session was written to")
	}
	copied, _ := store.ReadFrom(cur, r.ID)
	meta, ok := session.LatestMeta(copied)
	if !ok || meta.ResumedFrom != "s1" {
		t.Fatalf("meta = %+v, want resumedFrom s1", meta)
	}
}

func TestLoadResumeWithoutWritingTouchesNothing(t *testing.T) {
	store := session.NewStoreAt(t.TempDir())
	cur := session.Key("here-aaaaaaaa")
	other := session.Key("there-bbbbbbbb")
	seed(t, store, other, "s1", conversation)
	r, err := LoadResume(store, cur, other, "s1", false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Writer != nil || r.Forked != "" || len(r.History) == 0 {
		t.Fatalf("resumed = %+v", r)
	}
	if keys, _ := store.Keys(); len(keys) != 1 {
		t.Fatalf("keys = %v, want only the original project", keys)
	}
}

func TestResolveResumeErrors(t *testing.T) {
	store := session.NewStoreAt(t.TempDir())
	cur := session.Key("here-aaaaaaaa")
	seed(t, store, cur, "abc111", conversation)
	seed(t, store, cur, "abc222", conversation)
	if _, _, err := ResolveResume(store, cur, "zzz"); err == nil {
		t.Error("a missing id was resolved")
	}
	if _, _, err := ResolveResume(store, cur, "abc"); err == nil {
		t.Error("an ambiguous prefix was resolved")
	}
	if _, id, err := ResolveResume(store, cur, "abc1"); err != nil || id != "abc111" {
		t.Errorf("unique prefix = %q, %v", id, err)
	}
}

func TestLoadResumeRefusesAnEmptySession(t *testing.T) {
	store := session.NewStoreAt(t.TempDir())
	cur := session.Key("here-aaaaaaaa")
	if err := store.Create(cur, "empty"); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadResume(store, cur, cur, "empty", true); err == nil {
		t.Error("an empty session was resumed")
	}
}

func TestLatestInProject(t *testing.T) {
	store := session.NewStoreAt(t.TempDir())
	cur := session.Key("here-aaaaaaaa")
	if _, _, err := LatestInProject(store, cur); err == nil {
		t.Error("no sessions: want an error")
	}
	seed(t, store, cur, "s1", conversation)
	if _, id, err := LatestInProject(store, cur); err != nil || id != "s1" {
		t.Errorf("LatestInProject = %q, %v", id, err)
	}
}
