package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/session"
)

func TestFlattenGroupsOrder(t *testing.T) {
	groups := []session.ProjectSessions{
		{Key: "a", Label: "/proj/a", Current: true, Sessions: []session.SessionInfo{{ID: "a1", DisplayName: "session a"}}},
		{Key: "b", Label: "b (path unknown)", Sessions: []session.SessionInfo{{ID: "b1", DisplayName: "session b"}}},
	}
	rows := flattenGroups(groups)
	if len(rows) != 4 {
		t.Fatalf("rows = %d, want 4", len(rows))
	}
	if !rows[0].header || rows[0].label != "/proj/a" || !rows[0].current {
		t.Fatalf("first header wrong: %+v", rows[0])
	}
	if rows[1].header || rows[1].info.ID != "a1" || !rows[1].current {
		t.Fatalf("first session wrong: %+v", rows[1])
	}
	if !rows[2].header || rows[2].label != "b (path unknown)" || rows[2].current {
		t.Fatalf("second header wrong: %+v", rows[2])
	}
	if rows[3].header || rows[3].info.ID != "b1" {
		t.Fatalf("second session wrong: %+v", rows[3])
	}
}

func TestResumeCursorSkipsHeaders(t *testing.T) {
	rows := []resumeRow{
		{header: true, label: "a"},
		{info: session.SessionInfo{ID: "a1"}},
		{header: true, label: "b"},
		{info: session.SessionInfo{ID: "b1"}},
	}
	if got := firstSessionRow(rows); got != 1 {
		t.Fatalf("firstSessionRow = %d, want 1", got)
	}
	if got := nextSessionRow(rows, 1); got != 3 {
		t.Fatalf("nextSessionRow(1) = %d, want 3", got)
	}
	if got := nextSessionRow(rows, 3); got != 1 {
		t.Fatalf("nextSessionRow(3) = %d, want 1 (wrap)", got)
	}
	if got := prevSessionRow(rows, 1); got != 3 {
		t.Fatalf("prevSessionRow(1) = %d, want 3 (wrap)", got)
	}
}

func TestFilterResumeRows(t *testing.T) {
	rows := []resumeRow{
		{header: true, label: "a"},
		{info: session.SessionInfo{ID: "1111", DisplayName: "fix the bug"}},
		{info: session.SessionInfo{ID: "2222", DisplayName: "add feature"}},
	}
	got := filterResumeRows(rows, "bug")
	if len(got) != 2 {
		t.Fatalf("filtered = %d rows, want 2 (header + match)", len(got))
	}
	if got[1].info.ID != "1111" {
		t.Fatalf("filtered session = %+v", got[1])
	}
	if len(filterResumeRows(rows, "")) != 3 {
		t.Fatal("empty filter should return all rows")
	}
}

func TestResumeViewShowsProjectPath(t *testing.T) {
	a := New(Options{Provider: "openai", Model: "gpt-5"})
	a.resumeState = resumeViewState{
		rows: flattenGroups([]session.ProjectSessions{{
			Key:      "proj-abc",
			Label:    "/home/user/project",
			Current:  true,
			Sessions: []session.SessionInfo{{ID: "s1", DisplayName: "hello world", Turns: 3}},
		}}),
		cursor: 1,
	}
	view := a.resumeView()
	if !strings.Contains(view, "/home/user/project") {
		t.Fatalf("view missing project path: %q", view)
	}
	if !strings.Contains(view, "hello world") {
		t.Fatalf("view missing session name: %q", view)
	}
}

func TestResumeViewEmptyState(t *testing.T) {
	a := New(Options{Provider: "openai", Model: "gpt-5"})
	a.resumeState = resumeViewState{rows: nil, loading: false}
	view := a.resumeView()
	if !strings.Contains(view, "no sessions on disk") {
		t.Fatalf("expected empty state, got %q", view)
	}
}

func TestResumeEnterLoadsAndPops(t *testing.T) {
	a := New(Options{Provider: "openai", Model: "gpt-5", Workdir: t.TempDir()})
	key, _ := session.KeyFor(a.workdir)
	seedEntries(t, a, key, "sess-1", basicSessionEntries())
	a.resumeState = resumeViewState{
		rows:   []resumeRow{{header: true, label: "proj"}, {info: session.SessionInfo{ID: "sess-1", DisplayName: "hello"}, key: key}},
		cursor: 1,
	}
	// Set the view directly rather than push(), which would invoke
	// enterResume and restart the async scan over the prepared state.
	a.view = viewResume
	a.viewStack = []viewState{viewResume}

	_, _ = a.handleResumeKey(tea.KeyMsg{Type: tea.KeyEnter})

	if a.view != viewChat {
		t.Fatalf("view = %v, want chat after enter", a.view)
	}
	if a.sessionID != "sess-1" {
		t.Fatalf("sessionID = %q, want sess-1 after enter", a.sessionID)
	}
}
