package processlib

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
)

func TestNameForDerivesFromArgvZero(t *testing.T) {
	cases := []struct {
		cmd  string
		want string
	}{
		{"llama-server -hf foo --port 18080", "llama-server"},
		{"/usr/local/bin/python script.py", "python"},
		{"sleep 30", "sleep"},
	}
	for _, c := range cases {
		got, err := NameFor(c.cmd)
		if err != nil {
			t.Fatalf("NameFor(%q): %v", c.cmd, err)
		}
		if got != c.want {
			t.Fatalf("NameFor(%q) = %q, want %q", c.cmd, got, c.want)
		}
	}

	// An empty/whitespace command has no argv[0] to derive a slug from.
	if _, err := NameFor("   "); err == nil {
		t.Fatal("NameFor(whitespace) should fail")
	}
	if _, err := NameFor(""); err == nil {
		t.Fatal("NameFor(empty) should fail")
	}
}

func TestDirRejectsUnknownScope(t *testing.T) {
	if _, err := Dir(config.Scope("bogus"), t.TempDir()); err == nil {
		t.Fatal("Dir(bogus) should fail")
	}
}

func TestCreateUniqueReusesIdenticalBody(t *testing.T) {
	dir := t.TempDir()
	cmd := "llama-server --port 18080"
	e1, err := CreateUnique(config.ScopeProject, dir, cmd)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	e2, err := CreateUnique(config.ScopeProject, dir, cmd)
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if e1.Path != e2.Path || e1.Name != e2.Name {
		t.Fatalf("expected reuse, got %q / %q and %q / %q", e1.Path, e1.Name, e2.Path, e2.Name)
	}
	listing, _ := Load(config.ScopeProject, dir)
	if len(listing.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(listing.Entries))
	}
}

func TestCreateUniqueSuffixesDifferentBody(t *testing.T) {
	dir := t.TempDir()
	cmdA := "llama-server --port 18080"
	cmdB := "llama-server --port 18081"
	eA, err := CreateUnique(config.ScopeProject, dir, cmdA)
	if err != nil {
		t.Fatalf("create A: %v", err)
	}
	eB, err := CreateUnique(config.ScopeProject, dir, cmdB)
	if err != nil {
		t.Fatalf("create B: %v", err)
	}
	if eA.Name == eB.Name {
		t.Fatalf("expected different names, got %q and %q", eA.Name, eB.Name)
	}
	if !strings.HasSuffix(eB.Name, "-2") && eB.Name != "llama-server-2" {
		t.Fatalf("expected suffixed name, got %q", eB.Name)
	}
	listing, _ := Load(config.ScopeProject, dir)
	if len(listing.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(listing.Entries))
	}
}

func TestCommandStoredVerbatim(t *testing.T) {
	dir := t.TempDir()
	cmd := "llama-server -hf yuxinlu1/model --port 18080"
	e, err := CreateUnique(config.ScopeProject, dir, cmd)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	data, err := os.ReadFile(e.Path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// The generic library stores the body with a trailing newline.
	if string(data) != cmd+"\n" {
		t.Fatalf("file = %q, want %q", string(data), cmd+"\n")
	}
}

func TestLoadAndMerge(t *testing.T) {
	dir := t.TempDir()
	if _, err := CreateUnique(config.ScopeProject, dir, "sleep 1"); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateUnique(config.ScopeProject, dir, "sleep 2"); err != nil {
		t.Fatal(err)
	}
	listing, err := Load(config.ScopeProject, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(listing.Entries) != 2 {
		t.Fatalf("got %d entries", len(listing.Entries))
	}
	got := Filter(listing.Entries, "sleep 2")
	if len(got) != 1 {
		t.Fatalf("filter returned %d", len(got))
	}
}

func TestUpdateSetEnabledDeleteMatchEnabled(t *testing.T) {
	dir := t.TempDir()
	e, err := CreateUnique(config.ScopeProject, dir, "sleep 1")
	if err != nil {
		t.Fatal(err)
	}

	// Update replaces the body in place, keeping the identity.
	updated, err := Update(e, "sleep 30")
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Path != e.Path || updated.Name != e.Name || updated.Command != "sleep 30" {
		t.Fatalf("Update = %+v, want same identity with new body", updated)
	}

	// SetEnabled toggles the disabled marker and keeps the body.
	disabled, err := SetEnabled(updated, false)
	if err != nil {
		t.Fatalf("SetEnabled(false): %v", err)
	}
	if disabled.Enabled {
		t.Fatal("entry should be disabled")
	}
	if !strings.HasPrefix(filepath.Base(disabled.Path), "_") {
		t.Fatalf("disabled path = %q, want leading underscore", disabled.Path)
	}
	reEnabled, err := SetEnabled(disabled, true)
	if err != nil {
		t.Fatalf("SetEnabled(true): %v", err)
	}
	if !reEnabled.Enabled || reEnabled.Command != "sleep 30" {
		t.Fatalf("re-enabled entry = %+v", reEnabled)
	}

	// Match and Enabled both reflect the state.
	if !Match(reEnabled, "SLEEP 30") {
		t.Fatal("Match should be case-insensitive against body")
	}
	if got := Enabled([]Entry{disabled, reEnabled}); len(got) != 1 || got[0].Name != reEnabled.Name {
		t.Fatalf("Enabled = %+v, want only the re-enabled entry", got)
	}

	// Delete removes the file.
	if err := Delete(reEnabled); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(reEnabled.Path); !os.IsNotExist(err) {
		t.Fatalf("file should be gone, stat err = %v", err)
	}
}

func TestReorderAndMerge(t *testing.T) {
	dir := t.TempDir()
	a, _ := CreateUnique(config.ScopeProject, dir, "sleep 1")
	b, _ := CreateUnique(config.ScopeProject, dir, "sleep 2")

	// Reorder swaps the two entries.
	moved, err := Reorder(config.ScopeProject, dir, []Entry{a, b}, 0, 1)
	if err != nil {
		t.Fatalf("Reorder: %v", err)
	}
	if len(moved) != 2 || moved[0].Name != b.Name || moved[1].Name != a.Name {
		t.Fatalf("Reorder = %+v, want swapped", moved)
	}

	// Merge overlays a project entry over a same-named global entry.
	global := []Entry{{Name: "shared", Command: "global", Order: 1, Enabled: true, Scope: config.ScopeGlobal, Path: "/g/shared"}}
	project := []Entry{{Name: "shared", Command: "project", Order: 1, Enabled: true, Scope: config.ScopeProject, Path: "/p/shared"}}
	merged := Merge(global, project)
	if len(merged) != 1 || merged[0].Command != "project" || merged[0].Path != "/p/shared" {
		t.Fatalf("Merge = %+v, want project entry to win", merged)
	}
}

func TestProcessDirCreated(t *testing.T) {
	dir := t.TempDir()
	if _, err := CreateUnique(config.ScopeProject, dir, "sleep 1"); err != nil {
		t.Fatal(err)
	}
	pd := filepath.Join(dir, ".vulnetix", "processes")
	if _, err := os.Stat(pd); err != nil {
		t.Fatalf("process dir not created: %v", err)
	}
}
