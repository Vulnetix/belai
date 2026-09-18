package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// globTree builds a small fixture tree and returns its root.
//
//	main.go
//	README.md
//	.hidden.go            (hidden files are enumerated, not skipped)
//	internal/a.go
//	internal/a_test.go
//	internal/deep/b.go
//	vendor/v.go
//	.git/objects/c.go     (never enumerated)
func globTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := []string{
		"main.go",
		"README.md",
		".hidden.go",
		"internal/a.go",
		"internal/a_test.go",
		"internal/deep/b.go",
		"vendor/v.go",
		".git/objects/c.go",
	}
	for _, f := range files {
		abs := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", f, err)
		}
		if err := os.WriteFile(abs, []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	return root
}

// globRun executes Glob and returns the matched paths and the backend used.
func globRun(t *testing.T, g *Glob, args map[string]any) ([]string, string) {
	t.Helper()
	res, err := g.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute(%v): %v", args, err)
	}
	if res.Kind != KindGlob {
		t.Fatalf("kind = %q, want %q", res.Kind, KindGlob)
	}
	backend, _ := res.Meta["backend"].(string)
	if res.Content == "" {
		return nil, backend
	}
	return strings.Split(res.Content, "\n"), backend
}

func equalPaths(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestGlobRecursivePatternMatches is the regression for the bug where every
// Glob call returned nothing. With fd installed, the old implementation
// handed fd the pattern directly; fd rejects a --glob pattern containing a
// path separator ("The search pattern '**/*.go' contains a path-separation
// character and will not lead to any search results"), the command exited
// non-zero, and runFd swallowed it into an empty match list. Matching now
// happens in-process, so the pattern reaches a matcher that understands it.
func TestGlobRecursivePatternMatches(t *testing.T) {
	root := globTree(t)
	g := &Glob{Root: root, MaxResults: 100}
	got, _ := globRun(t, g, map[string]any{"pattern": "**/*.go"})
	want := []string{".hidden.go", "internal/a.go", "internal/a_test.go", "internal/deep/b.go", "main.go", "vendor/v.go"}
	if !equalPaths(got, want) {
		t.Fatalf("Glob(**/*.go) = %q, want %q", got, want)
	}
}

// Both enumerators must produce the same matches for the same tree: fd is an
// enumerator, not a second matcher, so a machine with fd installed and one
// without cannot disagree about what a pattern means.
func TestGlobBackendsAgree(t *testing.T) {
	if _, err := exec.LookPath("fd"); err != nil {
		t.Skip("fd not installed; nothing to compare against")
	}
	root := globTree(t)
	patterns := []string{"**/*.go", "*.go", "*.md", "internal/**/*.go", "**/*_test.go", "**/deep/*.go", "nope/**"}
	for _, pattern := range patterns {
		withFd := &Glob{Root: root, MaxResults: 100}
		gotFd, backend := globRun(t, withFd, map[string]any{"pattern": pattern})
		if backend != "fd" {
			t.Fatalf("pattern %q: backend = %q, want fd", pattern, backend)
		}

		// fdLooked set with an empty fdPath pins the walk enumerator without
		// mutating $PATH for the whole test binary.
		withWalk := &Glob{Root: root, MaxResults: 100, fdLooked: true}
		gotWalk, backend := globRun(t, withWalk, map[string]any{"pattern": pattern})
		if backend != "walk" {
			t.Fatalf("pattern %q: backend = %q, want walk", pattern, backend)
		}

		if !equalPaths(gotFd, gotWalk) {
			t.Fatalf("pattern %q: fd = %q, walk = %q", pattern, gotFd, gotWalk)
		}
	}
}

// .git is never enumerated by either backend, so a pattern can never reach
// object storage and flood the result with unreadable blobs.
func TestGlobSkipsGitDirectory(t *testing.T) {
	root := globTree(t)
	for _, g := range []*Glob{{Root: root, MaxResults: 100}, {Root: root, MaxResults: 100, fdLooked: true}} {
		got, backend := globRun(t, g, map[string]any{"pattern": "**/*.go"})
		for _, p := range got {
			if strings.HasPrefix(p, ".git/") {
				t.Fatalf("backend %s returned %q from .git", backend, p)
			}
		}
	}
}

// Ignore files do not narrow a Glob. fd honours .gitignore by default, which
// would make the same pattern answer differently depending on whether fd is
// installed; --no-ignore removes that difference.
func TestGlobIgnoresGitignore(t *testing.T) {
	root := globTree(t)
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("vendor/\n"), 0o600); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	for _, g := range []*Glob{{Root: root, MaxResults: 100}, {Root: root, MaxResults: 100, fdLooked: true}} {
		got, backend := globRun(t, g, map[string]any{"pattern": "vendor/*.go"})
		if !equalPaths(got, []string{"vendor/v.go"}) {
			t.Fatalf("backend %s: gitignored path = %q, want [vendor/v.go]", backend, got)
		}
	}
}

// The pattern is matched relative to path, but results are reported relative
// to the working directory, so the model can feed a result straight back into
// Read without rewriting it.
func TestGlobPathScopesPatternAndReportsFromRoot(t *testing.T) {
	root := globTree(t)
	for _, g := range []*Glob{{Root: root, MaxResults: 100}, {Root: root, MaxResults: 100, fdLooked: true}} {
		got, backend := globRun(t, g, map[string]any{"pattern": "*.go", "path": "internal"})
		want := []string{"internal/a.go", "internal/a_test.go"}
		if !equalPaths(got, want) {
			t.Fatalf("backend %s: Glob(*.go, internal) = %q, want %q", backend, got, want)
		}

		got, _ = globRun(t, g, map[string]any{"pattern": "**/*.go", "path": "internal"})
		want = []string{"internal/a.go", "internal/a_test.go", "internal/deep/b.go"}
		if !equalPaths(got, want) {
			t.Fatalf("backend %s: Glob(**/*.go, internal) = %q, want %q", backend, got, want)
		}
	}
}

// A path argument is confined to the root exactly like every other path
// argument: a traversal is an error, not a search of the parent.
func TestGlobPathEscapingRootIsRejected(t *testing.T) {
	root := globTree(t)
	g := &Glob{Root: root}
	if _, err := g.Execute(context.Background(), map[string]any{"pattern": "*", "path": ".."}); err == nil {
		t.Fatal("expected an error for a path escaping the root")
	}
}

// A pattern that matches nothing is an empty result, not an error.
func TestGlobNoMatchesIsEmpty(t *testing.T) {
	root := globTree(t)
	for _, g := range []*Glob{{Root: root, MaxResults: 100}, {Root: root, MaxResults: 100, fdLooked: true}} {
		res, err := g.Execute(context.Background(), map[string]any{"pattern": "**/*.rs"})
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if res.Content != "" {
			t.Fatalf("content = %q, want empty", res.Content)
		}
	}
}

// Results are sorted and capped, and the cap is applied after sorting so the
// truncated head is stable rather than dependent on enumeration order.
func TestGlobSortsThenCaps(t *testing.T) {
	root := globTree(t)
	g := &Glob{Root: root, MaxResults: 2}
	got, _ := globRun(t, g, map[string]any{"pattern": "**/*.go"})
	want := []string{".hidden.go", "internal/a.go"}
	if !equalPaths(got, want) {
		t.Fatalf("capped Glob = %q, want %q", got, want)
	}
}

// MaxResults is a bound, not a requirement: zero means the 200 default rather
// than "return nothing".
func TestGlobZeroMaxResultsUsesDefault(t *testing.T) {
	root := globTree(t)
	g := &Glob{Root: root}
	got, _ := globRun(t, g, map[string]any{"pattern": "*.go"})
	if !equalPaths(got, []string{".hidden.go", "main.go"}) {
		t.Fatalf("Glob = %q", got)
	}
}

// The tool description has to tell the model what ** means and how path
// interacts with the pattern, because in plan mode Glob is one of the only
// ways left to find a file.
func TestGlobDefinitionDocumentsSemantics(t *testing.T) {
	d := (&Glob{}).Definition()
	for _, want := range []string{"`**` spans zero or more segments", "relative to the working directory", "Grep"} {
		if !strings.Contains(d.Description, want) {
			t.Errorf("Glob description missing %q:\n%s", want, d.Description)
		}
	}
	for _, key := range []string{"pattern", "path"} {
		if d.Properties[key].Description == "" {
			t.Errorf("Glob property %q has no description", key)
		}
	}
}
