package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/belai/internal/projectregistry"
	"github.com/vulnetix/belai/internal/trustgate"
)

// trustedApp is an App on its own BELAI_HOME with the given directories
// trusted, open on /trusted.
func trustedApp(t *testing.T, dirs ...string) *App {
	t.Helper()
	a := budgetApp(t)
	for _, d := range dirs {
		if err := trustgate.Grant(d, nil); err != nil {
			t.Fatal(err)
		}
	}
	a.handleCommand("/trusted")
	if a.view != viewTrusted {
		t.Fatalf("view = %v, want trusted", a.view)
	}
	return a
}

func isTrusted(t *testing.T, dir string) bool {
	t.Helper()
	ok, _, _, err := projectregistry.TrustOf(dir)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestTrustedListsAndRevokesAfterConfirm(t *testing.T) {
	d1, d2 := t.TempDir(), t.TempDir()
	a := trustedApp(t, d1, d2)
	if n := len(a.trustedState.rows); n != 2 {
		t.Fatalf("rows = %d, want 2", n)
	}
	if !strings.Contains(a.trustedView(), "temporary") {
		t.Fatal("a scratch directory is not marked as hidden from rc")
	}
	target := a.trustedState.rows[0].path

	a.handleTrustedKey(key("x"))
	a.handleTrustedKey(key("n"))
	if !isTrusted(t, target) {
		t.Fatal("n revoked trust")
	}
	a.handleTrustedKey(key("x"))
	a.handleTrustedKey(key("y"))
	if isTrusted(t, target) {
		t.Fatal("y did not revoke trust")
	}
	if n := len(a.trustedState.rows); n != 1 {
		t.Fatalf("rows after revoke = %d, want 1", n)
	}
}

func TestTrustedPruneRevokesMissingAndTemp(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "gone")
	if err := os.Mkdir(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := t.TempDir()
	a := budgetApp(t)
	for _, d := range []string{gone, keep} {
		if err := trustgate.Grant(d, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	a.handleCommand("/trusted")
	if !strings.Contains(a.trustedView(), "missing") {
		t.Fatal("a removed directory is not marked missing")
	}
	a.handleTrustedKey(key("p"))
	if a.trustedState.mode != "confirm-prune" || len(a.trustedState.pending) != 2 {
		t.Fatalf("prune staged %d rows in mode %q", len(a.trustedState.pending), a.trustedState.mode)
	}
	a.handleTrustedKey(key("y"))
	if isTrusted(t, gone) || isTrusted(t, keep) {
		t.Fatal("prune left a stale directory trusted")
	}
}

func TestTrustedAddAsksThenGrantsDirectoryOnly(t *testing.T) {
	a := trustedApp(t)
	dir := t.TempDir()
	a.handleTrustedKey(key("a"))
	a.editor.SetValue(dir)
	a.handleTrustedKey(key("enter"))
	if a.trustedState.mode != "confirm-add" {
		t.Fatalf("mode = %q, want confirm-add (note %q)", a.trustedState.mode, a.trustedState.note)
	}
	if isTrusted(t, dir) {
		t.Fatal("trusted before confirmation")
	}
	a.handleTrustedKey(key("y"))
	if !isTrusted(t, dir) {
		t.Fatal("y did not trust the directory")
	}
	if ws := projectregistry.WorkspaceDirs(dir); len(ws) != 0 {
		t.Fatalf("adding accepted workspace dirs: %v", ws)
	}
}

func TestTrustedAddRefusesMissingPath(t *testing.T) {
	a := trustedApp(t)
	a.handleTrustedKey(key("a"))
	a.editor.SetValue(filepath.Join(t.TempDir(), "nope"))
	a.handleTrustedKey(key("enter"))
	if a.trustedState.mode == "confirm-add" || !a.trustedState.noteErr {
		t.Fatalf("a missing path was staged: mode %q note %q", a.trustedState.mode, a.trustedState.note)
	}
}

// manyTrusted is /trusted on a 24-line terminal with n trusted directories.
func manyTrusted(t *testing.T, n int) (*App, []string) {
	t.Helper()
	root := t.TempDir()
	var dirs []string
	for i := 0; i < n; i++ {
		d := filepath.Join(root, fmt.Sprintf("proj-%03d", i))
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, d)
	}
	a := trustedApp(t, dirs...)
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	return a, dirs
}

func TestTrustedFitsTheScreenAndScrolls(t *testing.T) {
	a, _ := manyTrusted(t, 80)
	if h := lipgloss.Height(a.trustedView()); h > 24 {
		t.Fatalf("view is %d lines on a 24-line terminal", h)
	}
	if !strings.Contains(a.trustedView(), "↓") {
		t.Fatal("no marker for the rows below the window")
	}
	for i := 0; i < 50; i++ {
		a.handleTrustedKey(tea.KeyMsg{Type: tea.KeyDown})
	}
	v := a.trustedView()
	if !strings.Contains(v, "proj-050") || strings.Contains(v, "proj-000") {
		t.Fatal("the window did not follow the cursor to row 50")
	}
	if h := lipgloss.Height(v); h > 24 {
		t.Fatalf("scrolled view is %d lines", h)
	}
	a.handleTrustedKey(key("G"))
	if !strings.Contains(a.trustedView(), "proj-079") {
		t.Fatal("end did not reach the last row")
	}
}

func TestTrustedFilterAndRevokeShown(t *testing.T) {
	a, dirs := manyTrusted(t, 30)
	a.handleTrustedKey(key("X"))
	if a.trustedState.mode != "" {
		t.Fatal("X without a filter staged a revoke of everything")
	}
	a.handleTrustedKey(key("/"))
	for _, r := range "proj-01" {
		a.handleTrustedKey(key(string(r)))
	}
	a.handleTrustedKey(key("enter"))
	if n := len(a.trustedShown()); n != 10 {
		t.Fatalf("filter shows %d rows, want 10", n)
	}
	a.handleTrustedKey(key("X"))
	if a.trustedState.mode != "confirm-shown" {
		t.Fatalf("mode = %q", a.trustedState.mode)
	}
	a.handleTrustedKey(key("y"))
	for i, d := range dirs {
		if want := i < 10 || i >= 20; isTrusted(t, d) != want {
			t.Fatalf("%s trusted = %v, want %v", d, !want, want)
		}
	}
	a.handleTrustedKey(key("esc"))
	if a.trustedState.filter != "" || a.view != viewTrusted {
		t.Fatal("esc with a filter should clear it, not leave")
	}
}
