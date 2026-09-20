package tui

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestListDirectoriesOnlyReturnsDirectories(t *testing.T) {
	dir := t.TempDir()
	wantDirs := []string{"alpha", "beta", "alpha/nested"}
	for _, d := range wantDirs {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "alpha", "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := listDirectories(dir)
	if err != nil {
		t.Fatalf("listDirectories: %v", err)
	}

	var want []string
	want = append(want, dir) // the root itself is included
	for _, d := range wantDirs {
		want = append(want, filepath.Join(dir, d))
	}
	sort.Strings(want)

	if len(got) != len(want) {
		t.Fatalf("listDirectories returned %d entries, want %d:\ngot:  %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("listDirectories[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestAddDirFilterable(t *testing.T) {
	a := NewApp(t.TempDir(), "")
	a.dirPickState = dirPickState{
		dirs: []string{"/home/user/go/src", "/home/user/rust/src", "/home/user/docs"},
	}

	a.dirPickState.setFilter("go")
	filtered := a.dirPickState.filtered()
	if len(filtered) != 1 || filtered[0] != "/home/user/go/src" {
		t.Fatalf("filtered = %v, want [/home/user/go/src]", filtered)
	}
}

func TestAddDirSelectAndConfirm(t *testing.T) {
	a := NewApp(t.TempDir(), "")
	a.dirPickState = dirPickState{
		open:     true,
		dirs:     []string{"/projects/a", "/projects/b", "/projects/c"},
		selected: 1,
	}

	cmd, handled := a.handleAddDirKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !handled || cmd != nil {
		t.Fatalf("enter: handled=%v cmd=%v, want handled=true cmd=nil", handled, cmd)
	}
	if !a.dirPickState.confirming {
		t.Fatal("expected confirming after enter")
	}
	if a.dirPickState.confirmPath != "/projects/b" {
		t.Fatalf("confirmPath = %q, want /projects/b", a.dirPickState.confirmPath)
	}

	cmd, handled = a.handleAddDirKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if !handled || cmd != nil {
		t.Fatalf("n: handled=%v cmd=%v, want handled=true cmd=nil", handled, cmd)
	}
	if a.dirPickState.confirming {
		t.Fatal("expected confirmation cancelled after n")
	}
}

func TestAddDirEscCloses(t *testing.T) {
	a := NewApp(t.TempDir(), "")
	a.dirPickState = dirPickState{open: true}

	cmd, handled := a.handleAddDirKey(tea.KeyMsg{Type: tea.KeyEsc})
	if !handled || cmd != nil {
		t.Fatalf("esc: handled=%v cmd=%v, want handled=true cmd=nil", handled, cmd)
	}
	if a.dirPickState.open {
		t.Fatal("expected picker to close after esc")
	}
}

func TestAddDirTypingFallsThroughToComposer(t *testing.T) {
	a := NewApp(t.TempDir(), "")
	a.dirPickState = dirPickState{
		open: true,
		dirs: []string{"/projects/a", "/projects/b"},
	}

	cmd, handled := a.handleAddDirKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("pro")})
	if handled {
		t.Fatal("rune keys must fall through so the composer can filter")
	}
	if cmd != nil {
		t.Fatalf("cmd = %v, want nil", cmd)
	}
}
