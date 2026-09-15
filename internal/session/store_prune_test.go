package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPruneRemovesOldSessions(t *testing.T) {
	root := t.TempDir()
	s := NewStoreAt(root)

	oldDir := filepath.Join(root, "proj-123abc")
	_ = os.MkdirAll(oldDir, 0o755)
	oldFile := filepath.Join(oldDir, "old.jsonl")
	_ = os.WriteFile(oldFile, []byte("{}\n"), 0o600)
	_ = os.Chtimes(oldFile, time.Now().Add(-48*time.Hour), time.Now().Add(-48*time.Hour))

	newFile := filepath.Join(oldDir, "new.jsonl")
	_ = os.WriteFile(newFile, []byte("{}\n"), 0o600)

	removed, err := s.Prune(24 * time.Hour)
	if err != nil {
		t.Fatalf("Prune error: %v", err)
	}
	if removed != 1 {
		t.Fatalf("expected 1 removed, got %d", removed)
	}
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Fatal("old file should be removed")
	}
	if _, err := os.Stat(newFile); err != nil {
		t.Fatal("new file should remain")
	}
}

func TestPruneMissingRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "does-not-exist")
	s := NewStoreAt(root)
	removed, err := s.Prune(24 * time.Hour)
	if err != nil {
		t.Fatalf("Prune error: %v", err)
	}
	if removed != 0 {
		t.Fatalf("expected 0 removed, got %d", removed)
	}
}
