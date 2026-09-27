package config

import (
	"path/filepath"
	"testing"
)

func TestWorktreesDirStaysOutOfTheStateDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BELAI_HOME", home)
	t.Setenv("BELAI_WORKTREES_DIR", filepath.Join(home, "wt"))
	if _, err := WorktreesDir(); err == nil {
		t.Fatal("a worktrees dir under the state dir was accepted")
	}
	other := t.TempDir()
	t.Setenv("BELAI_WORKTREES_DIR", other)
	got, err := WorktreesDir()
	if err != nil || got != other {
		t.Fatalf("%q %v", got, err)
	}
}
