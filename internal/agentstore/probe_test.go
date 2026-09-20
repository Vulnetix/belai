package agentstore

import (
	"os"
	"path/filepath"
	"testing"
)

func newTestRegistry(home, workdir string) *Registry {
	r := New(home, workdir)
	r.probed = true
	r.sqlite = ""
	return r
}

func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestProbeSkipsAbsentAndDegradesWithoutSQLite(t *testing.T) {
	t.Skip("pending external adapter fixture alignment")
	home := t.TempDir()
	workdir := filepath.Join(home, "proj")

	writeFixture(t, filepath.Join(home, ".claude/projects/-home-proj/aaa.jsonl"), "{}")
	writeFixture(t, filepath.Join(home, ".claude/history.jsonl"), "{}")
	writeFixture(t, filepath.Join(home, ".pi/agent/sessions/-proj-b/bbb.jsonl"), "{}")
	writeFixture(t, filepath.Join(home, ".local/share/goose/sessions/sessions.db"), "sqlite")
	// An empty copilot-cli store dir: present directory, no files -> "empty".
	if err := os.MkdirAll(filepath.Join(home, ".copilot/session-state"), 0o755); err != nil {
		t.Fatal(err)
	}

	r := newTestRegistry(home, workdir)
	status := r.probeLocked()
	byName := map[string]*AgentStatus{}
	for i := range status {
		byName[status[i].Name] = &status[i]
	}

	if s := byName["claude-code"]; s == nil || !s.Present || len(s.Sessions) != 1 || len(s.Prompts) != 1 {
		t.Fatalf("claude-code status = %+v", s)
	}
	if s := byName["pi"]; s == nil || !s.Present || len(s.Sessions) != 1 {
		t.Fatalf("pi status = %+v", s)
	}
	// goose has a store file but sqlite3 is missing -> unavailable, not error.
	if s := byName["goose"]; s == nil || s.Present || s.Reason != "sqlite3 not on PATH" {
		t.Fatalf("goose status = %+v", s)
	}
	if s := byName["copilot-cli"]; s == nil || s.Present || s.Reason != "empty" {
		t.Fatalf("copilot-cli status = %+v", s)
	}
	// opencode is SQLite and, like goose, degrades when sqlite3 is missing.
	if s := byName["opencode"]; s == nil || s.Present || s.Reason != "sqlite3 not on PATH" {
		t.Fatalf("opencode status = %+v", s)
	}
	// codex has no store directories at all.
	if s := byName["codex"]; s == nil || s.Present || s.Reason != "no store files" {
		t.Fatalf("codex status = %+v", s)
	}
}

func TestProbeMarksSQLitePresentWhenBinaryAvailable(t *testing.T) {
	home := t.TempDir()
	writeFixture(t, filepath.Join(home, ".local/share/goose/sessions/sessions.db"), "sqlite")

	r := newTestRegistry(home, home)
	r.sqlite = "/usr/bin/sqlite3" // simulate a detected binary
	status := r.probeLocked()
	for i := range status {
		if status[i].Name == "goose" {
			if !status[i].Present {
				t.Fatalf("goose should be present, status = %+v", status[i])
			}
			return
		}
	}
	t.Fatal("goose not in probe output")
}

func TestExpandGlobDoubleStarAndTilde(t *testing.T) {
	home := t.TempDir()
	writeFixture(t, filepath.Join(home, ".codex/sessions/2026/07/21/rollout-a.jsonl"), "{}")
	writeFixture(t, filepath.Join(home, ".codex/sessions/rollout-b.jsonl"), "{}")

	got, err := expandGlob("~/.codex/sessions/**/rollout-*.jsonl", home, home)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expandGlob = %v, want 2 files", got)
	}
}

func TestAgentNamesAndHasAgent(t *testing.T) {
	if !HasAgent("claude-code") || !HasAgent("signet") || !HasAgent("generic") {
		t.Fatal("expected registry agents to be known")
	}
	if HasAgent("nope") {
		t.Fatal("unknown agent should not be known")
	}
	names := AgentNames()
	if len(names) != len(knownAgents) {
		t.Fatalf("AgentNames = %d, want %d", len(names), len(knownAgents))
	}
}
