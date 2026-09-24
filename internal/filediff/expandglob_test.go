package filediff

import (
	"path/filepath"
	"testing"
)

func TestExpandGlob(t *testing.T) {
	dir := t.TempDir()
	// Non-glob paths are returned verbatim without touching the filesystem.
	if got, ok := expandGlob("a/b/c.go"); !ok || len(got) != 1 || got[0] != "a/b/c.go" {
		t.Fatalf("expandGlob(non-glob) = (%v, %v), want ([a/b/c.go], true)", got, ok)
	}

	// A real glob resolves to concrete paths.
	for _, f := range []string{"a.go", "b.go", "c.txt"} {
		write(t, filepath.Join(dir, f), "x")
	}
	got, ok := expandGlob(filepath.Join(dir, "*.go"))
	if !ok || len(got) != 2 {
		t.Fatalf("expandGlob(glob) = (%v, %v), want 2 matches", got, ok)
	}

	// A bad pattern refuses rather than guessing.
	if got, ok := expandGlob("["); ok || got != nil {
		t.Fatalf("expandGlob(bad pattern) = (%v, %v), want (nil, false)", got, ok)
	}

	// A sweep matching more than maxGlobMatches refuses.
	for i := 0; i < maxGlobMatches+1; i++ {
		write(t, filepath.Join(dir, "sweep", filepath.Base(t.TempDir())+"-"+itoa(i)+".go"), "x")
	}
	if _, ok := expandGlob(filepath.Join(dir, "sweep", "*.go")); ok {
		t.Fatal("expandGlob(sweep) = true, want false")
	}
}
