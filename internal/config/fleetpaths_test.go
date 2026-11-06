package config

import (
	"path/filepath"
	"strings"
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

func TestCandidatesPathRejectsBadIDs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BELAI_HOME", home)
	got, err := CandidatesPath("belai-patcher-3f9a2c")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "agents", "candidates", "belai-patcher-3f9a2c.json"); got != want {
		t.Fatalf("%q, want %q", got, want)
	}
	for _, bad := range []string{"", "../x", "a/b", "A", "a.b", "a b", "a\x00", strings.Repeat("a", 65)} {
		if p, err := CandidatesPath(bad); err == nil {
			t.Errorf("%q accepted as %q", bad, p)
		}
	}
}
