package sessionsync

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// An agent draft comes through the inbox beside prompts and answers, on the
// web-prompt switch, and its result goes back as one POST.
func TestInboxDeliversDraftsAndResult(t *testing.T) {
	fake := newFake()
	d := RemoteDraft{ID: "d1", SessionID: testSess, Premise: "a dependency reviewer"}
	d.Context.Workers = []string{"belai:reviewer"}
	d.Context.Labels = []string{"docs"}
	fake.drafts = []RemoteDraft{d}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	path := filepath.Join(t.TempDir(), testSess+".jsonl")
	appendLines(t, path, line("a", "user", "1"))
	s := startSyncer(t, srv, true)
	s.Activate(SessionInfo{ID: testSess, Path: path})

	select {
	case got := <-s.Drafts():
		if got.ID != "d1" || got.Premise != "a dependency reviewer" || got.Context.Labels[0] != "docs" || got.Context.Workers[0] != "belai:reviewer" {
			t.Fatalf("draft = %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no draft delivered")
	}
	s.DraftResult("d1", DraftDone, "", map[string]any{"fields": map[string]any{"name": map[string]any{"value": "x"}}})
	eventually(t, "draft result", func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		r := fake.results["d1"]
		return r != nil && r["status"] == DraftDone && r["result"] != nil
	})
}

// A host that takes no web prompts refuses a draft without delivering it.
func TestDraftsRefusedWhenPromptsOff(t *testing.T) {
	fake := newFake()
	fake.drafts = []RemoteDraft{{ID: "d2", SessionID: testSess, Premise: "p"}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	path := filepath.Join(t.TempDir(), testSess+".jsonl")
	appendLines(t, path, line("a", "user", "1"))
	c, err := NewClient(srv.URL+apiPath, func() (string, error) { return "ApiKey org:hex", nil }, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	// Answers on, prompts off: drafts still refuse, they ride the prompt switch.
	s := New(Options{Client: c, HostID: testHost, RemoteAnswers: true,
		TickEvery: 20 * time.Millisecond, HeartbeatEvery: time.Second, InboxWait: time.Second})
	s.Start(context.Background())
	t.Cleanup(func() { s.Close(time.Second) })
	s.Activate(SessionInfo{ID: testSess, Path: path})
	eventually(t, "refusal", func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		r := fake.results["d2"]
		return r != nil && r["status"] == DraftRefused && r["result"] == nil && r["reason"] != ""
	})
	select {
	case got := <-s.Drafts():
		t.Fatalf("a refused draft was delivered: %+v", got)
	default:
	}
}

// A refused result carries no result body; a 409 (the user cancelled on the
// website) reads as ErrConflict and is not a sync error.
func TestDraftResultShapeAndConflict(t *testing.T) {
	fake := newFake()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c, err := NewClient(srv.URL+apiPath, func() (string, error) { return "ApiKey org:hex", nil }, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.DraftResult(context.Background(), "d3", DraftRefused, "no model", map[string]int{"ignored": 1}); err != nil {
		t.Fatal(err)
	}
	if r := fake.results["d3"]; r["result"] != nil || r["reason"] != "no model" {
		t.Fatalf("refused body = %v", r)
	}
	fake.conflictDrafts = true
	err = c.DraftResult(context.Background(), "d4", DraftDone, "", map[string]any{})
	if !errors.Is(err, ErrConflict) || !IsConflict(err) {
		t.Fatalf("409 = %v", err)
	}

	s := New(Options{Client: c, HostID: testHost})
	s.DraftResult("d5", DraftDone, "", map[string]any{})
	time.Sleep(200 * time.Millisecond)
	if st := s.Status(); st.LastError != "" {
		t.Fatalf("a cancelled draft set a sync error: %q", st.LastError)
	}
}
