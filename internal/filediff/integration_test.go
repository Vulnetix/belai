package filediff_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/filediff"
	"github.com/vulnetix/belai/internal/tools"
)

// These tests drive the real Bash tool through the real Recorder, which is the
// only place the two halves meet: everything else is unit-tested on either
// side of that seam.

func repoWithFile(t *testing.T, name, body string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	env := append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "initial")
	return dir
}

// runObserved runs a command through the Bash tool exactly as executeCall
// does, with the recorder wrapped around it.
func runObserved(t *testing.T, dir, command string) filediff.Change {
	t.Helper()
	bash := &tools.Bash{Root: dir, Timeout: 10 * time.Second, MaxBytes: 64 * 1024}
	rec := filediff.NewRecorder(dir)

	ctx := context.Background()
	snap := rec.Before(ctx, command)
	if _, err := bash.Execute(ctx, map[string]any{"command": command}); err != nil {
		t.Fatalf("bash %q: %v", command, err)
	}
	return snap.After(ctx)
}

// TestSedInPlaceProducesADiff is the motivating case: `sed -i` prints nothing,
// so before this the transcript showed a bare ✓ and no evidence of the edit.
func TestSedInPlaceProducesADiff(t *testing.T) {
	dir := repoWithFile(t, "main.go", "package main\n\nfunc main() {\n\tfoo := 1\n}\n")

	ch := runObserved(t, dir, `sed -i 's/foo/bar/' main.go`)

	if len(ch.Files) != 1 {
		t.Fatalf("files = %d (%q)", len(ch.Files), ch.Unavailable)
	}
	fc := ch.Files[0]
	if fc.Path != "main.go" {
		t.Fatalf("path = %q", fc.Path)
	}

	rows := fc.Rows()
	added, removed := filediff.Stat(rows)
	if added != 1 || removed != 1 {
		t.Fatalf("stat = +%d -%d, want +1 -1", added, removed)
	}

	var sawOld, sawNew bool
	for _, r := range rows {
		switch r.Op {
		case filediff.OpDel:
			sawOld = sawOld || strings.Contains(r.Text, "foo := 1")
			if r.OldLine != 4 {
				t.Fatalf("removed line numbered %d, want 4", r.OldLine)
			}
		case filediff.OpAdd:
			sawNew = sawNew || strings.Contains(r.Text, "bar := 1")
			if r.NewLine != 4 {
				t.Fatalf("added line numbered %d, want 4", r.NewLine)
			}
			// A one-line change is the shape that gets intra-line emphasis.
			if len(r.Emph) == 0 {
				t.Fatalf("no intra-line emphasis on %q", r.Text)
			}
		}
	}
	if !sawOld || !sawNew {
		t.Fatalf("diff did not capture the edit:\n%+v", rows)
	}
}

// TestHeredocCreatingAFileProducesADiff covers the other common shape: a file
// written wholesale from a heredoc.
func TestHeredocCreatingAFileProducesADiff(t *testing.T) {
	dir := repoWithFile(t, "main.go", "package main\n")

	ch := runObserved(t, dir, "cat > notes.md <<'EOF'\nhello\nworld\nEOF\n")

	if len(ch.Files) != 1 || ch.Files[0].Path != "notes.md" {
		t.Fatalf("files = %+v (%q)", ch.Files, ch.Unavailable)
	}
	if !ch.Files[0].Created {
		t.Fatalf("expected a creation: %+v", ch.Files[0])
	}
	if added, removed := filediff.Stat(ch.Files[0].Rows()); added != 2 || removed != 0 {
		t.Fatalf("stat = +%d -%d, want +2 -0", added, removed)
	}
}

// TestOpaqueCommandStillProducesADiff is why the git path exists: the command
// is one the parser cannot account for, and the change is still found.
func TestOpaqueCommandStillProducesADiff(t *testing.T) {
	dir := repoWithFile(t, "main.go", "package main\n")

	script := filepath.Join(dir, "build.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho generated > out.txt\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, confident := filediff.ParseTargets("sh build.sh"); confident {
		t.Fatal("precondition: the parser should not understand this command")
	}

	ch := runObserved(t, dir, "sh build.sh")

	var found bool
	for _, f := range ch.Files {
		if f.Path == "out.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("git discovery missed the generated file: %+v (%q)", ch.Files, ch.Unavailable)
	}
}

// TestReadOnlyCommandProducesNoDiff keeps the panel quiet for the common case.
func TestReadOnlyCommandProducesNoDiff(t *testing.T) {
	dir := repoWithFile(t, "main.go", "package main\n")
	if ch := runObserved(t, dir, "cat main.go"); !ch.Empty() {
		t.Fatalf("a read-only command produced a diff: %+v", ch)
	}
}

// TestNonRepoFallsBackToCommandParsing exercises the heuristic path end to end.
func TestNonRepoFallsBackToCommandParsing(t *testing.T) {
	dir := t.TempDir()
	if filediff.NewRecorder(dir).RepoRoot != "" {
		t.Skip("temp dir is inside a git repository")
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ch := runObserved(t, dir, "echo after > notes.txt")

	if len(ch.Files) != 1 || ch.Files[0].Path != "notes.txt" {
		t.Fatalf("files = %+v (%q)", ch.Files, ch.Unavailable)
	}
	if ch.Files[0].Old != "before\n" || ch.Files[0].New != "after\n" {
		t.Fatalf("sides wrong: %+v", ch.Files[0])
	}
}

// runObservedTool mirrors executeCall's diff seam for a named-target tool:
// Write and Edit report their targets through tools.Targeter, so the recorder
// snapshots exactly those paths via BeforePaths.
func runObservedTool(t *testing.T, dir string, tool tools.Tool, args map[string]any) filediff.Change {
	t.Helper()
	rec := filediff.NewRecorder(dir)
	ctx := context.Background()

	var snap *filediff.Snapshot
	if tt, ok := tool.(tools.Targeter); ok {
		snap = rec.BeforePaths(ctx, tt.Targets(args)...)
	} else {
		snap = rec.Before(ctx, tool.Subject(args))
	}
	if _, err := tool.Execute(ctx, args); err != nil {
		t.Fatalf("tool %s: %v", tool.Definition().Name, err)
	}
	return snap.After(ctx)
}

// TestWriteProducesCreationDiff covers the new BeforePaths seam outside a git
// repository: a file creation must produce a Created change even though the
// path did not exist when it was snapshotted.
func TestWriteProducesCreationDiff(t *testing.T) {
	dir := t.TempDir()
	if filediff.NewRecorder(dir).RepoRoot != "" {
		t.Skip("temp dir is inside a git repository")
	}

	ch := runObservedTool(t, dir, &tools.Write{Root: dir}, map[string]any{
		"path": "notes.md", "content": "hello\n",
	})

	if len(ch.Files) != 1 || ch.Files[0].Path != "notes.md" {
		t.Fatalf("files = %+v (%q)", ch.Files, ch.Unavailable)
	}
	if !ch.Files[0].Created {
		t.Fatalf("expected a creation: %+v", ch.Files[0])
	}
	if ch.Files[0].Old != "" || ch.Files[0].New != "hello\n" {
		t.Fatalf("sides wrong: %+v", ch.Files[0])
	}
}

// TestEditProducesReplaceDiff covers the Edit seam: the recorder must capture
// the exact before and after bytes.
func TestEditProducesReplaceDiff(t *testing.T) {
	dir := t.TempDir()
	if filediff.NewRecorder(dir).RepoRoot != "" {
		t.Skip("temp dir is inside a git repository")
	}
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ch := runObservedTool(t, dir, &tools.Edit{Root: dir}, map[string]any{
		"path": "f.txt", "old_string": "before", "new_string": "after",
	})

	if len(ch.Files) != 1 || ch.Files[0].Path != "f.txt" {
		t.Fatalf("files = %+v (%q)", ch.Files, ch.Unavailable)
	}
	if ch.Files[0].Old != "before\n" || ch.Files[0].New != "after\n" {
		t.Fatalf("sides wrong: %+v", ch.Files[0])
	}
}

// TestWriteEditDiffInGitRepo covers the git path: inside a repository the
// recorder still reports Write and Edit changes via git status.
func TestWriteEditDiffInGitRepo(t *testing.T) {
	dir := repoWithFile(t, "main.go", "package main\n")

	ch := runObservedTool(t, dir, &tools.Write{Root: dir}, map[string]any{
		"path": "notes.md", "content": "hello\n",
	})
	if len(ch.Files) != 1 || ch.Files[0].Path != "notes.md" || !ch.Files[0].Created {
		t.Fatalf("write files = %+v (%q)", ch.Files, ch.Unavailable)
	}
}
