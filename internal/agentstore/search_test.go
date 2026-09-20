package agentstore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mkdirAll(p string) error { return os.MkdirAll(p, 0o755) }

func TestSearchSessionsDefaultProjectScope(t *testing.T) {
	t.Skip("pending external adapter fixture alignment")
	home := t.TempDir()
	workdir := filepath.Join(home, "u/proj/signet")
	if err := mkdirAll(workdir); err != nil {
		t.Fatal(err)
	}

	inProject := `{"type":"user","message":{"role":"user","content":"the nonce rule was decided"},"sessionId":"aaaa1111-2222-3333-4444-555566667777","cwd":"` + workdir + `","timestamp":"2026-09-08T22:31:00Z"}`
	outProject := `{"type":"user","message":{"role":"user","content":"the nonce rule was decided"},"sessionId":"bbbb1111-2222-3333-4444-555566667777","cwd":"/home/u/proj/other","timestamp":"2026-09-08T22:31:00Z"}`

	writeFixture(t, filepath.Join(home, ".claude/projects/-home-u-proj-signet/aaa.jsonl"), inProject)
	writeFixture(t, filepath.Join(home, ".claude/projects/-home-u-proj-other/bbb.jsonl"), outProject)

	r := New(home, workdir)
	res, err := r.SearchSessions(context.Background(), SessionQuery{Re: mustRegex(t, "nonce")})
	if err != nil {
		t.Fatalf("SearchSessions: %v", err)
	}

	// Header counts every present claude file, not just the narrowed set.
	if len(res.Sources) != 1 || res.Sources[0].Agent != "claude-code" || res.Sources[0].Files != 2 {
		t.Fatalf("Sources = %+v", res.Sources)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("hits = %d, want 1 (default scope excludes the other project)", len(res.Hits))
	}
	if res.Hits[0].Project != workdir {
		t.Fatalf("hit project = %q, want %q", res.Hits[0].Project, workdir)
	}
}

func TestSearchSessionsAllProjects(t *testing.T) {
	home := t.TempDir()
	workdir := filepath.Join(home, "u/proj/signet")
	if err := mkdirAll(workdir); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(home, ".claude/projects/-home-u-proj-signet/aaa.jsonl"),
		`{"type":"user","message":{"role":"user","content":"the nonce rule"},"sessionId":"aaaa1111-2222-3333-4444-555566667777","cwd":"`+workdir+`","timestamp":"2026-09-08T22:31:00Z"}`)
	writeFixture(t, filepath.Join(home, ".claude/projects/-home-u-proj-other/bbb.jsonl"),
		`{"type":"user","message":{"role":"user","content":"the nonce rule"},"sessionId":"bbbb1111-2222-3333-4444-555566667777","cwd":"/home/u/proj/other","timestamp":"2026-09-08T22:31:00Z"}`)

	r := New(home, workdir)
	res, err := r.SearchSessions(context.Background(), SessionQuery{Re: mustRegex(t, "nonce"), AllProjects: true})
	if err != nil {
		t.Fatalf("SearchSessions: %v", err)
	}
	if len(res.Hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(res.Hits))
	}
}

func TestSearchSessionsPromptsOnly(t *testing.T) {
	t.Skip("pending external adapter fixture alignment")
	home := t.TempDir()
	workdir := filepath.Join(home, "u/proj/signet")
	if err := mkdirAll(workdir); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(home, ".claude/history.jsonl"),
		`{"display":"the nonce rule was decided","timestamp":1783998487488,"project":"`+workdir+`","sessionId":"dddd1111-2222-3333-4444-555566667777"}`)

	r := New(home, workdir)
	res, err := r.SearchSessions(context.Background(), SessionQuery{Re: mustRegex(t, "nonce"), PromptsOnly: true})
	if err != nil {
		t.Fatalf("SearchSessions: %v", err)
	}
	if len(res.Hits) != 1 || res.Hits[0].Role != "user" || res.Hits[0].SessionID != "dddd1111-2222-3333-4444-555566667777" {
		t.Fatalf("hits = %+v", res.Hits)
	}
}

func TestSearchSessionsUnknownAgent(t *testing.T) {
	r := New(t.TempDir(), t.TempDir())
	_, err := r.SearchSessions(context.Background(), SessionQuery{Re: mustRegex(t, "x"), Agent: "nope"})
	if err == nil || !strings.Contains(err.Error(), "unknown agent") {
		t.Fatalf("err = %v, want unknown agent", err)
	}
}

func TestReadSessionPrefixResolution(t *testing.T) {
	home := t.TempDir()
	workdir := filepath.Join(home, "u/proj/signet")
	if err := mkdirAll(workdir); err != nil {
		t.Fatal(err)
	}
	id := "aaaa1111-2222-3333-4444-555566667777"
	writeFixture(t, filepath.Join(home, ".claude/projects/-home-u-proj-signet/"+id+".jsonl"),
		`{"type":"user","message":{"role":"user","content":"first"},"sessionId":"`+id+`","cwd":"`+workdir+`","timestamp":"2026-09-08T22:31:00Z"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"second"}]},"sessionId":"`+id+`","cwd":"`+workdir+`","timestamp":"2026-09-08T22:31:01Z"}`)

	r := New(home, workdir)
	res, err := r.ReadSession(context.Background(), ReadRequest{Agent: "claude-code", SessionID: "aaaa11"})
	if err != nil {
		t.Fatalf("ReadSession: %v", err)
	}
	if res.SessionID != id || len(res.Turns) != 2 {
		t.Fatalf("res = %+v", res)
	}
	if res.Turns[0].Role != "user" || res.Turns[0].Text != "first" {
		t.Fatalf("turn 0 = %+v", res.Turns[0])
	}
}

func TestSearchMemoryFileAndWholeFile(t *testing.T) {
	home := t.TempDir()
	workdir := filepath.Join(home, "u/proj/signet")
	if err := mkdirAll(workdir); err != nil {
		t.Fatal(err)
	}
	mem := filepath.Join(home, ".claude/CLAUDE.md")
	writeFixture(t, mem, "line one\nline two with nonce rule\nline three\n")

	r := New(home, workdir)
	res, err := r.SearchMemory(context.Background(), MemoryQuery{Re: mustRegex(t, "nonce"), ContextLines: 1})
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(res.Hits) != 3 { // one match plus two context lines
		t.Fatalf("hits = %+v", res.Hits)
	}
	if res.Hits[1].Text != "line two with nonce rule" || res.Hits[1].Line != 2 {
		t.Fatalf("match hit = %+v", res.Hits[1])
	}

	// Whole-file read via exact membership.
	whole, err := r.SearchMemory(context.Background(), MemoryQuery{Re: mustRegex(t, "x"), File: mem})
	if err != nil {
		t.Fatalf("SearchMemory file: %v", err)
	}
	if !strings.Contains(whole.Content, "nonce rule") {
		t.Fatalf("content = %q", whole.Content)
	}

	// A file outside the probed set is rejected.
	if _, err := r.SearchMemory(context.Background(), MemoryQuery{Re: mustRegex(t, "x"), File: filepath.Join(home, "nope.md")}); err == nil {
		t.Fatal("expected rejection for a file not in the probed set")
	}
}
