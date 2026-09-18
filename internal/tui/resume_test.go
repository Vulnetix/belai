package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/modes"
	"github.com/vulnetix/signet/internal/session"
	"github.com/vulnetix/signet/internal/todos"
	"github.com/vulnetix/signet/internal/tui/components"
)

func newResumeApp(t *testing.T, workdir string) *App {
	t.Helper()
	t.Setenv("SIGNET_HOME", t.TempDir())
	return New(Options{Provider: "openai", Model: "gpt-5", Workdir: workdir})
}

func seedEntries(t *testing.T, a *App, key session.Key, id string, entries []session.Entry) {
	t.Helper()
	for _, e := range entries {
		if err := a.store.AppendTo(key, id, e); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
}

func basicSessionEntries() []session.Entry {
	return []session.Entry{
		{ID: "u1", Type: "user", Role: "user", Content: "hello"},
		{ID: "a1", ParentID: "u1", Type: "assistant", Role: "assistant", Content: "hi", Meta: map[string]any{"model": "gpt-5", "provider": "openai", "prompt_tokens": 10, "completion_tokens": 2, "total_tokens": 12}},
	}
}

func TestResumeRestoresTranscriptNameAndTodos(t *testing.T) {
	workdir := t.TempDir()
	a := newResumeApp(t, workdir)
	key, _ := session.KeyFor(workdir)

	list := todos.New("hello", []string{"one", "two"})
	entries := basicSessionEntries()
	entries = append(entries,
		session.Entry{ID: "n1", ParentID: "a1", Type: session.EntryTypeSessionName, Content: "named"},
		list.ToEntry("a1"),
	)

	seedEntries(t, a, key, "sess-1", entries)
	a.resumeSession(key, "sess-1")

	if a.sessionID != "sess-1" {
		t.Fatalf("sessionID = %q, want sess-1", a.sessionID)
	}
	if len(a.messages) < 2 || a.messages[0].Content != "hello" || a.messages[1].Content != "hi" {
		t.Fatalf("messages = %+v", a.messages)
	}
	if a.sessionName != "named" || !a.nameRequested {
		t.Fatalf("sessionName/nameRequested = %q/%v", a.sessionName, a.nameRequested)
	}
	if a.todos == nil || len(a.todos.Items) != 2 {
		t.Fatalf("todos = %+v", a.todos)
	}
}

func TestResumeThenSubmitAppendsInPlace(t *testing.T) {
	workdir := t.TempDir()
	a := newResumeApp(t, workdir)
	key, _ := session.KeyFor(workdir)
	// Seed a schema-2 session so no backfill entry is inserted between the old
	// tail and the new submission.
	entries := basicSessionEntries()
	entries = append(entries, session.Meta{Schema: 2, Cwd: workdir}.ToEntry("a1"))
	seedEntries(t, a, key, "sess-1", entries)

	a.resumeSession(key, "sess-1")
	a.echoUser("next")

	got, err := a.store.Read(a.workdir, a.sessionID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("entries = %d, want 4: %+v", len(got), got)
	}
	// The new entry branches from the old tail.
	if got[3].ParentID != entries[len(entries)-1].ID {
		t.Fatalf("new entry ParentID = %q, want %q", got[3].ParentID, entries[len(entries)-1].ID)
	}

	// Exactly one jsonl in the project directory.
	dir := filepath.Join(a.store.Root, string(key))
	des, _ := os.ReadDir(dir)
	n := 0
	for _, de := range des {
		if strings.HasSuffix(de.Name(), ".jsonl") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("jsonl files = %d, want 1", n)
	}

	// BuildTree sees a single root (no second forest).
	if roots := session.BuildTree(entries); len(roots) != 1 {
		t.Fatalf("roots = %d, want 1", len(roots))
	}
}

func TestResumeCancelsRunningTurn(t *testing.T) {
	workdir := t.TempDir()
	a := newResumeApp(t, workdir)
	key, _ := session.KeyFor(workdir)
	seedEntries(t, a, key, "sess-1", basicSessionEntries())

	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	a.preSend = true
	a.messages = append(a.messages, components.Message{Role: "assistant"})

	a.resumeSession(key, "sess-1")

	if a.cancel != nil {
		t.Fatal("cancel not cleared after resume")
	}
	if ctx.Err() == nil {
		t.Fatal("in-flight context was not cancelled")
	}
	if a.preSend {
		t.Fatal("preSend not cleared")
	}
	nonSystem := 0
	for _, m := range a.messages {
		if m.Role != "system" {
			nonSystem++
		}
	}
	if nonSystem != 2 {
		t.Fatalf("stray messages leaked: %+v", a.messages)
	}
}

func TestCrossProjectResumeForks(t *testing.T) {
	workdirA := t.TempDir()
	workdirB := t.TempDir()
	a := newResumeApp(t, workdirA)
	keyA, _ := session.KeyFor(workdirA)
	keyB, _ := session.KeyFor(workdirB)

	origin := append(basicSessionEntries(), (session.Meta{Schema: 2, Cwd: workdirB}).ToEntry("a1"))
	seedEntries(t, a, keyB, "sess-origin", origin)

	originPath := filepath.Join(a.store.Root, string(keyB), "sess-origin.jsonl")
	before, err := os.ReadFile(originPath)
	if err != nil {
		t.Fatalf("read origin: %v", err)
	}

	a.resumeSession(keyB, "sess-origin")

	if a.sessionKey != keyA {
		t.Fatalf("sessionKey = %q, want current project %q", a.sessionKey, keyA)
	}
	if a.sessionID == "sess-origin" {
		t.Fatal("cross-project resume should fork to a new id")
	}

	after, err := os.ReadFile(originPath)
	if err != nil {
		t.Fatalf("read origin after: %v", err)
	}
	if string(before) != string(after) {
		t.Fatal("origin file was mutated by cross-project resume")
	}

	forked, err := a.store.ReadFrom(keyA, a.sessionID)
	if err != nil {
		t.Fatalf("read fork: %v", err)
	}
	meta, ok := session.LatestMeta(forked)
	if !ok {
		t.Fatal("fork missing session_meta")
	}
	if meta.ResumedFrom != "sess-origin" || meta.OriginCwd != workdirB {
		t.Fatalf("fork meta = %+v", meta)
	}
}

func TestResumeBackfillsSchema1(t *testing.T) {
	workdir := t.TempDir()
	a := newResumeApp(t, workdir)
	key, _ := session.KeyFor(workdir)
	seedEntries(t, a, key, "sess-legacy", basicSessionEntries())

	a.resumeSession(key, "sess-legacy")

	entries, _ := a.store.ReadFrom(key, "sess-legacy")
	meta, ok := session.LatestMeta(entries)
	if !ok {
		t.Fatal("schema-1 file was not backfilled with session_meta")
	}
	if meta.Cwd != workdir {
		t.Fatalf("backfilled Cwd = %q, want %q", meta.Cwd, workdir)
	}
}

func TestResumeUnknownIDNonFatal(t *testing.T) {
	workdir := t.TempDir()
	a := newResumeApp(t, workdir)
	key, _ := session.KeyFor(workdir)
	orig := a.sessionID

	a.resumeSession(key, "does-not-exist")

	if a.sessionID != orig {
		t.Fatalf("sessionID changed to %q on a bad id", a.sessionID)
	}
	found := false
	for _, m := range a.messages {
		if m.Role == "system" && strings.Contains(m.Text(), "resume:") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a resume error system line")
	}
}

func TestResumeWritesActiveSession(t *testing.T) {
	workdir := t.TempDir()
	a := newResumeApp(t, workdir)
	key, _ := session.KeyFor(workdir)
	seedEntries(t, a, key, "sess-1", basicSessionEntries())

	a.resumeSession(key, "sess-1")

	if a.state.ActiveSession != "sess-1" {
		t.Fatalf("state.ActiveSession = %q, want sess-1", a.state.ActiveSession)
	}
	st, err := config.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if st.ActiveSession != "sess-1" {
		t.Fatalf("state.json ActiveSession = %q, want sess-1", st.ActiveSession)
	}
}

func TestResumePlanStateRestored(t *testing.T) {
	workdir := t.TempDir()
	a := newResumeApp(t, workdir)
	key, _ := session.KeyFor(workdir)
	entries := basicSessionEntries()
	entries = append(entries, modes.PlanState{Enabled: true, Todos: []modes.Todo{{N: 1, Text: "step"}}}.ToEntry("a1"))
	seedEntries(t, a, key, "sess-plan", entries)

	r := rehydrateSession(entries)
	if r.Plan == nil || !r.Plan.Enabled {
		t.Fatalf("plan state not rehydrated: %+v", r.Plan)
	}
}
