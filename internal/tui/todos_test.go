package tui

import (
	"testing"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/session"
	"github.com/vulnetix/signet/internal/todos"
)

func testList() todos.List {
	return todos.New("ship the thing", []string{"first step", "second step", "third step"})
}

func TestTodosVisibleRequiresSettingAndItems(t *testing.T) {
	a := New(Options{})

	if a.todosVisible() {
		t.Fatal("no list tracked: panel must stay hidden")
	}

	empty := todos.New("goal", nil)
	a.todos = &empty
	if a.todosVisible() {
		t.Fatal("empty list: panel must stay hidden")
	}

	l := testList()
	a.todos = &l
	if !a.todosVisible() {
		t.Fatal("populated list with default settings: panel must show")
	}

	off := false
	a.settings = config.Settings{UI: &config.UISettings{ShowTodos: &off}}
	if a.todosVisible() {
		t.Fatal("ui.show_todos=false: panel must stay hidden")
	}
}

func TestTodoPanelHeightCountsTowardChrome(t *testing.T) {
	a := New(Options{})
	a.width = 80
	a.height = 40

	bare := a.chromeHeight()
	if h := a.todoPanelHeight(); h != 0 {
		t.Fatalf("hidden panel height = %d, want 0", h)
	}

	l := testList()
	a.todos = &l
	h := a.todoPanelHeight()
	if h <= 0 {
		t.Fatalf("visible panel height = %d, want > 0", h)
	}
	if got := a.chromeHeight(); got != bare+h {
		t.Fatalf("chromeHeight = %d, want %d (bare %d + panel %d)", got, bare+h, bare, h)
	}
	if a.renderTodoPanel() == "" {
		t.Fatal("visible panel rendered empty")
	}
}

func TestSetTodosPersistsEntry(t *testing.T) {
	workdir := t.TempDir()
	t.Setenv("SIGNET_HOME", t.TempDir())

	st, err := session.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	a := New(Options{Workdir: workdir})
	a.store = st

	l := testList()
	a.setTodos(&l)

	entries, err := st.Read(workdir, a.sessionID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	got, ok := todos.Latest(entries)
	if !ok {
		t.Fatal("setTodos wrote no todo_list entry")
	}
	if len(got.Items) != len(l.Items) {
		t.Fatalf("persisted %d items, want %d", len(got.Items), len(l.Items))
	}
}

func TestSetTodosIgnoresNil(t *testing.T) {
	a := New(Options{})
	l := testList()
	a.todos = &l
	a.setTodos(nil)
	if a.todos == nil {
		t.Fatal("setTodos(nil) must leave the tracked list alone")
	}
}

func TestRehydrateTodosTakesTheLatestEntry(t *testing.T) {
	first := todos.New("goal", []string{"one"})
	second := todos.New("goal", []string{"one", "two"})

	a := New(Options{})
	a.rehydrateTodos([]session.Entry{
		{Type: "user", Role: "user", Content: "hi"},
		first.ToEntry(""),
		second.ToEntry(""),
	})

	if a.todos == nil {
		t.Fatal("rehydrateTodos tracked nothing")
	}
	if len(a.todos.Items) != 2 {
		t.Fatalf("rehydrated %d items, want the latest entry's 2", len(a.todos.Items))
	}
}

func TestRehydrateTodosWithoutEntryIsNoop(t *testing.T) {
	a := New(Options{})
	a.rehydrateTodos([]session.Entry{{Type: "user", Role: "user", Content: "hi"}})
	if a.todos != nil {
		t.Fatal("no todo_list entry: nothing should be tracked")
	}
}

func TestStartNewSessionDropsTodos(t *testing.T) {
	workdir := t.TempDir()
	t.Setenv("SIGNET_HOME", t.TempDir())

	st, _ := session.NewStore()
	a := New(Options{Workdir: workdir})
	a.store = st

	l := testList()
	a.setTodos(&l)
	a.startNewSession()

	if a.todos != nil {
		t.Fatal("a new session must not inherit the previous session's todo list")
	}
	if a.todosVisible() {
		t.Fatal("todo panel still visible after /clear")
	}
}

func TestCompactionCarriesTodosIntoTheNewSession(t *testing.T) {
	workdir := t.TempDir()
	t.Setenv("SIGNET_HOME", t.TempDir())

	st, _ := session.NewStore()
	a := New(Options{Workdir: workdir})
	a.store = st

	l := testList()
	a.setTodos(&l)
	old := a.sessionID

	a.applyCompaction("summary of the work so far")

	if a.sessionID == old {
		t.Fatal("compaction did not fork into a new session")
	}
	entries, err := st.Read(workdir, a.sessionID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	got, ok := todos.Latest(entries)
	if !ok {
		t.Fatal("compacted session carries no todo_list entry")
	}
	if len(got.Items) != len(l.Items) {
		t.Fatalf("carried %d items, want %d", len(got.Items), len(l.Items))
	}
	if a.todos == nil {
		t.Fatal("compaction dropped the in-memory list")
	}
}
