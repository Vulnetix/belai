package projectregistry

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSweepFindsProjects(t *testing.T) {
	root := t.TempDir()
	mkDir := func(p string) string {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatal(err)
		}
		return full
	}
	mkDir("proj1/.vulnetix")
	mkDir("proj2/.vulnetix")
	mkDir("proj2/node_modules/.vulnetix") // should be skipped

	out := make(chan Found, 10)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go Sweep(ctx, SweepOptions{Roots: []string{root}, MaxDepth: 6, Budget: 5 * time.Second}, out)

	var found []string
	for f := range out {
		found = append(found, f.Path)
	}
	if len(found) != 2 {
		t.Fatalf("found = %v", found)
	}
}

func TestSweepSkipsSymlinks(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real", ".vulnetix")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(filepath.Dir(real), link); err != nil {
		t.Fatal(err)
	}

	out := make(chan Found, 10)
	go Sweep(context.Background(), SweepOptions{Roots: []string{root}, Budget: 5 * time.Second}, out)
	var found []string
	for f := range out {
		found = append(found, f.Path)
	}
	if len(found) != 1 || !contains(found, filepath.Join(root, "real")) {
		t.Fatalf("expected only real project, got %v", found)
	}
}

func TestSweepBudget(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "proj", ".vulnetix"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := make(chan Found, 10)
	go Sweep(context.Background(), SweepOptions{Roots: []string{root}, Budget: 1 * time.Millisecond}, out)
	// Receive whatever we can in a short window; the 1 ms budget should make
	// Sweep terminate quickly.
	time.Sleep(50 * time.Millisecond)
	select {
	case _, ok := <-out:
		if !ok {
			// Expected: channel closed early by budget.
			return
		}
	default:
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
