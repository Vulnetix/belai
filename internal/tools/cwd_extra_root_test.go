package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestAddRootWidensConfinement verifies that added roots become resolvable,
// subsumption is rejected, and the primary root stays first in Roots.
func TestAddRootWidensConfinement(t *testing.T) {
	primary := t.TempDir()
	extra := t.TempDir()
	if err := os.WriteFile(filepath.Join(extra, "extra.txt"), []byte("extra"), 0o600); err != nil {
		t.Fatalf("write extra file: %v", err)
	}

	c := NewCwd(primary)
	if err := c.AddRoot(extra); err != nil {
		t.Fatalf("AddRoot: %v", err)
	}

	roots := c.Roots()
	if len(roots) != 2 || roots[0] != primary || roots[1] != extra {
		t.Fatalf("Roots = %v, want [%s %s]", roots, primary, extra)
	}

	// Resolving an absolute path under the added root lands there.
	res, err := resolvePath(primary, c, filepath.Join(extra, "extra.txt"))
	if err != nil {
		t.Fatalf("resolvePath: %v", err)
	}
	if res.Root != extra || res.Rel != "extra.txt" {
		t.Fatalf("resolved = %+v, want root=%s rel=extra.txt", res, extra)
	}

	// A directory that was not added is still refused.
	other := t.TempDir()
	if _, err := resolvePath(primary, c, filepath.Join(other, "x.txt")); err == nil {
		t.Fatal("path outside every root should be refused")
	}

	// Subsumption: a root inside an existing root is refused.
	nested := filepath.Join(extra, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if err := c.AddRoot(nested); err == nil {
		t.Fatal("nested root inside existing root should be rejected")
	}

	// Subsumption: a root that contains the primary root is refused.
	parent := t.TempDir()
	inside := filepath.Join(parent, "inside")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatalf("mkdir inside: %v", err)
	}
	c2 := NewCwd(inside)
	if err := c2.AddRoot(parent); err == nil {
		t.Fatal("root containing primary root should be rejected")
	}

	// Duplicate add is rejected.
	if err := c.AddRoot(extra); err == nil {
		t.Fatal("duplicate AddRoot should be rejected")
	}
}

// TestExtraRootRejectsEscape verifies that absolute paths inside an extra root
// cannot escape it through ".." or through a symlink.
func TestExtraRootRejectsEscape(t *testing.T) {
	primary := t.TempDir()
	extra := t.TempDir()
	if err := os.WriteFile(filepath.Join(extra, "extra.txt"), []byte("extra"), 0o600); err != nil {
		t.Fatalf("write extra file: %v", err)
	}

	c := NewCwd(primary)
	if err := c.AddRoot(extra); err != nil {
		t.Fatalf("AddRoot: %v", err)
	}

	// ".." escape from the extra root is refused.
	bad := filepath.Join(extra, "..", "passwd")
	if _, err := resolvePath(primary, c, bad); err == nil {
		t.Fatal(".. escape from extra root should be refused")
	}

	// Symlink escape: a symlink inside the extra root pointing outside it.
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	link := filepath.Join(extra, "escape")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := resolvePath(primary, c, link); err == nil {
		t.Fatal("symlink escape from extra root should be refused")
	}
}

// TestSubjectPathDistinguishesRootKinds checks that permission-rule subjects
// are absolute for an extra root and root-relative for the primary root.
func TestSubjectPathDistinguishesRootKinds(t *testing.T) {
	primary := t.TempDir()
	extra := t.TempDir()
	c := NewCwd(primary)
	if err := c.AddRoot(extra); err != nil {
		t.Fatalf("AddRoot: %v", err)
	}

	primarySubj := subjectPath(primary, c, "file.txt")
	if primarySubj != "file.txt" {
		t.Fatalf("primary subject = %q, want file.txt", primarySubj)
	}

	extraSubj := subjectPath(primary, c, filepath.Join(extra, "file.txt"))
	if extraSubj != filepath.Join(extra, "file.txt") {
		t.Fatalf("extra subject = %q, want absolute path", extraSubj)
	}
}

// TestResolveNewPathWorksInExtraRoot checks that Write/Edit targets can be
// resolved inside an added root.
func TestResolveNewPathWorksInExtraRoot(t *testing.T) {
	primary := t.TempDir()
	extra := t.TempDir()
	c := NewCwd(primary)
	if err := c.AddRoot(extra); err != nil {
		t.Fatalf("AddRoot: %v", err)
	}

	res, err := resolveNewPath(primary, c, filepath.Join(extra, "new.txt"))
	if err != nil {
		t.Fatalf("resolveNewPath: %v", err)
	}
	if res.Root != extra || res.Rel != "new.txt" {
		t.Fatalf("resolved = %+v, want root=%s rel=new.txt", res, extra)
	}
}

// TestGlobGrepReturnAbsolutePathsForExtraRoot verifies that searches landing
// in an added workspace directory come back as absolute paths.
func TestGlobGrepReturnAbsolutePathsForExtraRoot(t *testing.T) {
	primary := t.TempDir()
	extra := t.TempDir()
	if err := os.WriteFile(filepath.Join(extra, "extra.go"), []byte("package extra\n// needle\n"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	c := NewCwd(primary)
	if err := c.AddRoot(extra); err != nil {
		t.Fatalf("AddRoot: %v", err)
	}

	glob := &Glob{Root: primary, MaxResults: 100, Cwd: c}
	res, err := glob.Execute(context.Background(), map[string]any{"pattern": "*.go", "path": extra})
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	want := filepath.Join(extra, "extra.go")
	if res.Content != want {
		t.Fatalf("Glob result = %q, want %q", res.Content, want)
	}

	grep := &Grep{Root: primary, MaxMatches: 100, MaxLineLen: 200, Cwd: c}
	res, err = grep.Execute(context.Background(), map[string]any{"pattern": "needle", "path": extra})
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if res.Content == "" {
		t.Fatal("Grep returned no matches")
	}
	if !filepath.IsAbs(firstPath(res.Content)) {
		t.Fatalf("Grep result should be absolute: %q", res.Content)
	}
}

func firstPath(line string) string {
	for i := 0; i < len(line); i++ {
		if line[i] == ':' {
			return line[:i]
		}
	}
	return line
}
