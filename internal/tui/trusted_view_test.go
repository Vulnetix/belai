package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
