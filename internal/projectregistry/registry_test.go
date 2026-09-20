package projectregistry

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestObserveCreatesEntry(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	dir := t.TempDir()
	if err := Observe(dir, SourceWorkdir); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	reg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	all := reg.All()
	if len(all) != 1 {
		t.Fatalf("entries = %d", len(all))
	}
	if all[0].Source != SourceWorkdir {
		t.Fatalf("source = %q", all[0].Source)
	}
}

func TestMergePreservesRicherSource(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	dir := t.TempDir()
	if err := Observe(dir, SourceSweep); err != nil {
		t.Fatal(err)
	}
	if err := Observe(dir, SourceManual); err != nil {
		t.Fatal(err)
	}
	reg, _ := Load()
	all := reg.All()
	if all[0].Source != SourceManual {
		t.Fatalf("expected manual to override sweep, got %q", all[0].Source)
	}
}

func TestPruneRemovesOldUnpinned(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	dir := t.TempDir()
	if err := Observe(dir, SourceSweep); err != nil {
		t.Fatal(err)
	}
	// Manually age the entry.
	reg, _ := Load()
	reg.file.Entries[0].LastSeen = time.Now().Add(-90 * 24 * time.Hour)
	_ = reg.save()

	n, err := Prune(30*24*time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("pruned = %d", n)
	}
	reg, _ = Load()
	if len(reg.All()) != 0 {
		t.Fatal("expected empty registry")
	}
}

func TestForget(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	dir := t.TempDir()
	if err := Observe(dir, SourceWorkdir); err != nil {
		t.Fatal(err)
	}
	reg, _ := Load()
	key := reg.All()[0].Key
	if err := Forget(key); err != nil {
		t.Fatal(err)
	}
	reg, _ = Load()
	if len(reg.All()) != 0 {
		t.Fatal("expected forgotten")
	}
}

func TestForwardVersionRefusesWrite(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	path, _ := registryPath()
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	_ = os.WriteFile(path, []byte(`{"version": 99, "entries": []}`), 0o600)
	_, err := Load()
	if !errors.Is(err, ErrorForwardVersion) {
		t.Fatalf("expected forward-version error, got %v", err)
	}
	err = Mutate(func(r *Registry) error { return nil })
	if !errors.Is(err, ErrorForwardVersion) {
		t.Fatalf("Mutate should refuse to write forward-version file")
	}
}

func TestAddWorkspaceDirStoresDirectories(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()
	extra := t.TempDir()
	if err := Observe(workdir, SourceWorkdir); err != nil {
		t.Fatal(err)
	}
	if err := AddWorkspaceDir(workdir, extra); err != nil {
		t.Fatalf("AddWorkspaceDir: %v", err)
	}
	if got := WorkspaceDirs(workdir); len(got) != 1 || got[0] != extra {
		t.Fatalf("WorkspaceDirs = %v, want [%s]", got, extra)
	}
}

func TestAddWorkspaceDirRejectsOverlappingRoots(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()
	extra := t.TempDir()
	_ = AddWorkspaceDir(workdir, extra)

	nested := filepath.Join(extra, "nested")
	_ = os.MkdirAll(nested, 0o755)
	if err := AddWorkspaceDir(workdir, nested); err == nil {
		t.Fatal("nested overlapping root should be rejected")
	}

	if err := AddWorkspaceDir(workdir, extra); err == nil {
		t.Fatal("duplicate root should be rejected")
	}
}

func TestAddWorkspaceDirRequiresDirectory(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()
	f := filepath.Join(t.TempDir(), "notadir")
	_ = os.WriteFile(f, []byte("x"), 0o600)
	if err := AddWorkspaceDir(workdir, f); err == nil {
		t.Fatal("non-directory should be rejected")
	}
}

func TestResolveWorkspaceDirsFiltersRegistry(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()
	a := t.TempDir()
	b := t.TempDir()
	_ = AddWorkspaceDir(workdir, a)
	_ = AddWorkspaceDir(workdir, b)

	filtered, err := ResolveWorkspaceDirs(workdir, []string{a})
	if err != nil {
		t.Fatalf("ResolveWorkspaceDirs: %v", err)
	}
	if len(filtered) != 1 || filtered[0] != a {
		t.Fatalf("filtered = %v, want [%s]", filtered, a)
	}
	if got := WorkspaceDirs(workdir); len(got) != 1 || got[0] != a {
		t.Fatalf("registry should be pruned to allowed entries, got %v", got)
	}
}

func TestResolveWorkspaceDirsReturnsAllWhenAllowedEmpty(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()
	a := t.TempDir()
	_ = AddWorkspaceDir(workdir, a)

	filtered, err := ResolveWorkspaceDirs(workdir, nil)
	if err != nil {
		t.Fatalf("ResolveWorkspaceDirs: %v", err)
	}
	if len(filtered) != 1 {
		t.Fatalf("filtered = %v, want 1 entry", filtered)
	}
}

func TestRaceManyObserves(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	const n = 50
	dirs := make([]string, n)
	for i := range dirs {
		dirs[i] = t.TempDir()
	}

	var wg sync.WaitGroup
	for _, d := range dirs {
		wg.Add(1)
		go func(dir string) {
			defer wg.Done()
			_ = Observe(dir, SourceWorkdir)
		}(d)
	}
	wg.Wait()

	reg, _ := Load()
	if len(reg.All()) != n {
		t.Fatalf("entries = %d, want %d", len(reg.All()), n)
	}
}
