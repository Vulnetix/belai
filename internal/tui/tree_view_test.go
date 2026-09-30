package tui

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/session"
)

// treeApp seeds and resumes a four-message session: u1 a1 u2 a2.
func treeApp(t *testing.T) (*App, session.Key) {
	t.Helper()
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1") // never reach a real account
	workdir := t.TempDir()
	a := newResumeApp(t, workdir)
	key, _ := session.KeyFor(workdir)
	seedEntries(t, a, key, "sess-1", []session.Entry{
		{ID: "u1", Type: "user", Role: "user", Content: "first"},
		{ID: "a1", ParentID: "u1", Type: "assistant", Role: "assistant", Content: "one"},
		{ID: "u2", ParentID: "a1", Type: "user", Role: "user", Content: "second"},
		{ID: "a2", ParentID: "u2", Type: "assistant", Role: "assistant", Content: "two"},
	})
	a.resumeSession(key, "sess-1")
	return a, key
}

func chatTexts(a *App) string {
	var out []string
	for _, m := range a.messages {
		if m.Role == "user" || m.Role == "assistant" {
			out = append(out, m.Text())
		}
	}
	return strings.Join(out, ",")
}

func TestTreeContinuesFromAnEarlierReplyAndBranches(t *testing.T) {
	a, key := treeApp(t)
	path := a.store.SessionPath(key, "sess-1")
	before, _ := os.ReadFile(path)

	a.treeCommand("a1")

	if got := chatTexts(a); got != "first,one" {
		t.Fatalf("after jump: %q", got)
	}
	after, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(after), string(before)) || len(after) <= len(before) {
		t.Fatal("navigation must only append to the session file")
	}

	// A new prompt grows a second branch off a1.
	a.appendEntry(session.Entry{ID: "u3", Type: "user", Role: "user", Content: "third"})
	a.appendEntry(session.Entry{ID: "a3", Type: "assistant", Role: "assistant", Content: "three"})
	a.resumeSession(key, "sess-1")
	if got := chatTexts(a); got != "first,one,third,three" {
		t.Fatalf("after reopening the branched session: %q", got)
	}

	entries, _ := a.store.ReadFrom(key, "sess-1")
	forks := 0
	for _, r := range session.TreeRows(entries, false) {
		if r.Fork {
			forks++
		}
	}
	if forks != 1 {
		t.Fatalf("tree shows %d forks, want 1", forks)
	}
}

func TestTreeUserRowPutsTheTextBackInTheComposer(t *testing.T) {
	a, _ := treeApp(t)
	a.treeCommand("u2")
	if got := chatTexts(a); got != "first,one" {
		t.Fatalf("history %q, want the turns before u2", got)
	}
	if a.editor.Value() != "second" {
		t.Fatalf("composer %q", a.editor.Value())
	}
}

func TestTreeRefusesWhileATurnRuns(t *testing.T) {
	a, _ := treeApp(t)
	a.cancel = func() {}
	defer func() { a.cancel = nil }()
	a.treeCommand("a1")
	if got := chatTexts(a); got != "first,one,second,two" {
		t.Fatalf("history changed mid-turn: %q", got)
	}
}

func TestTreeRejectsBadTargets(t *testing.T) {
	a, _ := treeApp(t)
	for _, arg := range []string{"nope", "u1", "fork zzz"} { // unknown, first message, unknown
		a.treeCommand(arg)
		if got := chatTexts(a); got != "first,one,second,two" {
			t.Fatalf("%q changed the history: %q", arg, got)
		}
	}
}

func TestTreeForkOpensANewSessionAndKeepsTheOriginal(t *testing.T) {
	a, key := treeApp(t)
	a.treeCommand("fork a1")
	if a.sessionID == "sess-1" {
		t.Fatal("fork stayed in the original session")
	}
	if got := chatTexts(a); got != "first,one" {
		t.Fatalf("fork history %q", got)
	}
	orig, _ := a.store.ReadFrom(key, "sess-1")
	if session.HasBranches(orig) {
		t.Fatal("forking must not navigate the original session")
	}
	if got := texts(orig); got != "first,one,second,two" {
		t.Fatalf("original session lost its history: %q", got)
	}
}

func texts(es []session.Entry) string {
	var out []string
	for _, e := range es {
		if e.Type == "user" || e.Type == "assistant" {
			out = append(out, e.Content)
		}
	}
	return strings.Join(out, ",")
}

func TestTreeViewKeys(t *testing.T) {
	a, _ := treeApp(t)
	a.push(viewTree)
	if len(a.treeState.rows) != 4 || a.treeState.cursor != 3 {
		t.Fatalf("rows %d cursor %d", len(a.treeState.rows), a.treeState.cursor)
	}
	if !strings.Contains(a.treeView(), "Session tree") {
		t.Fatal("view did not render")
	}
	a.handleTreeKey(tea.KeyMsg{Type: tea.KeyUp})
	a.handleTreeKey(tea.KeyMsg{Type: tea.KeyUp})
	a.handleTreeKey(tea.KeyMsg{Type: tea.KeyEnter}) // a1
	if a.view != viewChat {
		t.Fatalf("view %v, want chat", a.view)
	}
	if got := chatTexts(a); got != "first,one" {
		t.Fatalf("history %q", got)
	}
}
