package agentstore

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/vulnetix/signet/internal/session"
)

func testdataPath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join("testdata", name)
}

func mustRegex(t *testing.T, pattern string) *regexp.Regexp {
	t.Helper()
	re, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("compile %q: %v", pattern, err)
	}
	return re
}

func TestClaudeAdapter(t *testing.T) {
	t.Skip("pending external adapter fixture alignment")
	path := testdataPath(t, "claude.jsonl")
	a := claudeAdapter{}

	srcs, err := a.Sources(path)
	if err != nil {
		t.Fatalf("Sources: %v", err)
	}
	if len(srcs) != 1 {
		t.Fatalf("Sources = %d, want 1", len(srcs))
	}
	src := srcs[0]
	if src.SessionID != "aaaa1111-2222-3333-4444-555566667777" {
		t.Fatalf("SessionID = %q", src.SessionID)
	}
	if src.Project != "/home/u/proj/signet" {
		t.Fatalf("Project = %q", src.Project)
	}

	turns, err := a.Turns(src, 0, 0)
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("Turns = %d, want 2", len(turns))
	}
	if turns[0].Role != "user" || turns[0].Text != "please find the nonce rule" {
		t.Fatalf("turn 0 = %+v", turns[0])
	}
	if turns[1].Role != "assistant" || turns[1].Text != "The delimiters carry a nonce plus a SHA-256 integrity hash." {
		t.Fatalf("turn 1 = %+v", turns[1])
	}
	want := time.Date(2026, 9, 8, 22, 31, 5, 0, time.UTC)
	if !turns[1].At.Equal(want) {
		t.Fatalf("turn 1 At = %v, want %v", turns[1].At, want)
	}

	hits, err := a.Scan(context.Background(), src, mustRegex(t, "integrity"), DefaultCaps())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(hits))
	}
	if hits[0].Role != "assistant" || hits[0].Turn != 1 || hits[0].SessionID != src.SessionID {
		t.Fatalf("hit = %+v", hits[0])
	}
}

func TestCodexAdapterCwdOnlyOnLineOne(t *testing.T) {
	t.Skip("pending external adapter fixture alignment")
	path := testdataPath(t, "codex.jsonl")
	a := codexAdapter{}

	srcs, err := a.Sources(path)
	if err != nil {
		t.Fatalf("Sources: %v", err)
	}
	src := srcs[0]
	if src.SessionID != "bbbb1111-2222-3333-4444-555566667777" {
		t.Fatalf("SessionID = %q", src.SessionID)
	}
	if src.Project != "/home/u/proj/signet" {
		t.Fatalf("Project = %q (cwd must come from line 1 only)", src.Project)
	}

	turns, err := a.Turns(src, 0, 0)
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	if len(turns) != 2 || turns[0].Role != "user" || turns[1].Role != "assistant" {
		t.Fatalf("turns = %+v", turns)
	}
	if turns[0].Text != "where is the nonce" || turns[1].Text != "the nonce lives in delimiters" {
		t.Fatalf("texts = %q / %q", turns[0].Text, turns[1].Text)
	}
}

func TestPiAdapterCwdOnlyOnLineOne(t *testing.T) {
	path := testdataPath(t, "pi.jsonl")
	a := piAdapter{}

	srcs, err := a.Sources(path)
	if err != nil {
		t.Fatalf("Sources: %v", err)
	}
	src := srcs[0]
	if src.SessionID != "cccc1111-2222-3333-4444-555566667777" {
		t.Fatalf("SessionID = %q", src.SessionID)
	}
	if src.Project != "/home/u/proj/signet" {
		t.Fatalf("Project = %q", src.Project)
	}

	turns, err := a.Turns(src, 0, 0)
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	if len(turns) != 2 || turns[0].Role != "user" || turns[1].Role != "assistant" {
		t.Fatalf("turns = %+v", turns)
	}
	if turns[1].Text != "the nonce lives in delimiters" {
		t.Fatalf("text = %q", turns[1].Text)
	}
}

func TestSignetAdapter(t *testing.T) {
	root := t.TempDir()
	keyDir := filepath.Join(root, "signet-275e7780")
	if err := os.MkdirAll(keyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(testdataPath(t, "signet.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	id := "aaaa1234-5678-90ab-cdef-000000000000"
	if err := os.WriteFile(filepath.Join(keyDir, id+".jsonl"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	store := session.NewStoreAt(root)
	a := signetAdapter{store: store}
	srcs, err := a.Sources(filepath.Join(keyDir, id+".jsonl"))
	if err != nil {
		t.Fatalf("Sources: %v", err)
	}
	src := srcs[0]
	if src.SessionID != id {
		t.Fatalf("SessionID = %q", src.SessionID)
	}
	if src.Project != "/home/u/proj/signet" {
		t.Fatalf("Project = %q", src.Project)
	}

	turns, err := a.Turns(src, 0, 0)
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	if len(turns) != 2 || turns[0].Role != "user" || turns[1].Role != "assistant" {
		t.Fatalf("turns = %+v", turns)
	}
	if turns[1].Model != "test-model" {
		t.Fatalf("model = %q", turns[1].Model)
	}
	if turns[1].Text != "the nonce lives in delimiters" {
		t.Fatalf("text = %q", turns[1].Text)
	}
}

func TestPromptsAdapters(t *testing.T) {
	for _, c := range []struct {
		file, agent, id, project string
	}{
		{"prompts-claude.jsonl", "claude-code", "dddd1111-2222-3333-4444-555566667777", "/home/u/proj/signet"},
		{"prompts-codex.jsonl", "codex", "eeee1111-2222-3333-4444-555566667777", ""},
	} {
		a := promptsAdapter{}
		path := testdataPath(t, c.file)
		srcs, err := a.Sources(path)
		if err != nil || len(srcs) != 1 {
			t.Fatalf("%s Sources = %v, %v", c.file, srcs, err)
		}
		src := srcs[0]
		src.Agent = c.agent
		hits, err := a.Scan(context.Background(), src, mustRegex(t, "nonce"), DefaultCaps())
		if err != nil {
			t.Fatalf("%s Scan: %v", c.file, err)
		}
		if len(hits) != 1 {
			t.Fatalf("%s hits = %d, want 1", c.file, len(hits))
		}
		if hits[0].SessionID != c.id {
			t.Fatalf("%s session id = %q", c.file, hits[0].SessionID)
		}
		if hits[0].Project != c.project {
			t.Fatalf("%s project = %q", c.file, hits[0].Project)
		}
		if hits[0].Role != "user" {
			t.Fatalf("%s role = %q", c.file, hits[0].Role)
		}
	}
}

func TestVSCodeAdapter(t *testing.T) {
	path := testdataPath(t, "vscode.json")
	a := vscodeAdapter{}
	srcs, err := a.Sources(path)
	if err != nil {
		t.Fatalf("Sources: %v", err)
	}
	src := srcs[0]
	if src.SessionID != "ffff1111-2222-3333-4444-555566667777" {
		t.Fatalf("SessionID = %q", src.SessionID)
	}
	turns, err := a.Turns(src, 0, 0)
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	if len(turns) != 2 || turns[0].Role != "user" || turns[1].Role != "assistant" {
		t.Fatalf("turns = %+v", turns)
	}
	if turns[1].Text != "the nonce lives in delimiters" {
		t.Fatalf("text = %q", turns[1].Text)
	}
}

func TestGenericAdapterDegrades(t *testing.T) {
	path := testdataPath(t, "vscode.json")
	a := genericAdapter{}
	srcs, err := a.Sources(path)
	if err != nil || len(srcs) != 1 {
		t.Fatalf("Sources = %v, %v", srcs, err)
	}
	// The generic adapter tolerates the VS Code shape; a non-VS-Code shape
	// simply yields no turns.
	turns, err := a.Turns(srcs[0], 0, 0)
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want 2 (VS Code shape)", len(turns))
	}
}
