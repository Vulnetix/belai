package repomap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func writeFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The layout lists visible top-level directories by name, counts files
// recursively and never descends into the skipped directories.
func TestLayoutCountsRecursivelyAndSkips(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, filepath.Join(root, "cmd"), "a.go", "b.go")
	writeFiles(t, filepath.Join(root, "cmd", "sub"), "c.go")
	writeFiles(t, filepath.Join(root, "cmd", "node_modules"), "ignored.js")
	writeFiles(t, filepath.Join(root, "bin"), "x")
	writeFiles(t, filepath.Join(root, ".hidden"), "h")
	writeFiles(t, filepath.Join(root, "vendor"), "v")
	writeFiles(t, root, "top.txt")
	got := layout(context.Background(), root)
	want := []DirSummary{{Name: "bin", Files: 1}, {Name: "cmd", Files: 3}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("layout = %v, want %v", got, want)
	}
}

// A directory over the file cap reports exactly the cap, never one past it.
func TestLayoutCapsAPerDirectoryCountAtMaxFiles(t *testing.T) {
	root := t.TempDir()
	big := filepath.Join(root, "big")
	if err := os.MkdirAll(big, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxFiles+50; i++ {
		if err := os.WriteFile(filepath.Join(big, fmt.Sprintf("f%d", i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFiles(t, filepath.Join(root, "small"), "a")
	got := layout(context.Background(), root)
	if len(got) != 2 || got[0] != (DirSummary{Name: "big", Files: maxFiles}) || got[1] != (DirSummary{Name: "small", Files: 1}) {
		t.Fatalf("layout = %v, want big capped at %d and small at 1", got, maxFiles)
	}
}

// More than 24 top-level directories keep the first 24 by name.
func TestLayoutKeepsTheFirst24ByName(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 30; i++ {
		writeFiles(t, filepath.Join(root, fmt.Sprintf("d%02d", i)), "f")
	}
	got := layout(context.Background(), root)
	if len(got) != 24 || got[0].Name != "d00" || got[23].Name != "d23" {
		t.Fatalf("layout kept %d entries from %v to %v", len(got), got[0].Name, got[len(got)-1].Name)
	}
}

// A cancelled scan returns what it has instead of walking on.
func TestLayoutStopsWhenTheScanIsCancelled(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, filepath.Join(root, "a"), "f")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := layout(ctx, root); len(got) != 0 {
		t.Fatalf("a cancelled layout returned %v", got)
	}
}

// Extensions are lower-cased, a file with none is (none), the order is count
// descending then name, and only the top 12 are kept.
func TestLanguagesOrderAndCap(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, "a.GO", "b.go", "c.go", "Makefile", "x.md", "y.md")
	got := languages(context.Background(), root)
	want := []LangCount{{Ext: "go", Files: 3}, {Ext: "md", Files: 2}, {Ext: "(none)", Files: 1}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("languages = %v, want %v", got, want)
	}
	many := t.TempDir()
	for i := 0; i < 20; i++ {
		writeFiles(t, many, fmt.Sprintf("f.e%02d", i))
	}
	if got := languages(context.Background(), many); len(got) != 12 {
		t.Fatalf("languages kept %d extensions, want 12", len(got))
	}
}
