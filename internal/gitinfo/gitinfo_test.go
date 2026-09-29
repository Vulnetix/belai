package gitinfo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectRepo(t *testing.T) {
	root := t.TempDir()
	gitDir := filepath.Join(root, ".git")
	_ = os.MkdirAll(gitDir, 0o755)
	_ = os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o600)

	info, ok := Detect(root)
	if !ok {
		t.Fatal("expected repo detected")
	}
	if info.Root != root {
		t.Fatalf("root = %q", info.Root)
	}
	if info.Branch != "main" {
		t.Fatalf("branch = %q", info.Branch)
	}
	if info.Detached {
		t.Fatal("expected attached")
	}
}

func TestDetectDetached(t *testing.T) {
	root := t.TempDir()
	gitDir := filepath.Join(root, ".git")
	_ = os.MkdirAll(gitDir, 0o755)
	sha := "abc1234def"
	_ = os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte(sha+"\n"), 0o600)

	info, ok := Detect(root)
	if !ok {
		t.Fatal("expected repo detected")
	}
	if !info.Detached {
		t.Fatal("expected detached")
	}
	if info.Head != "abc1234" {
		t.Fatalf("head = %q", info.Head)
	}
}

func TestDetectNested(t *testing.T) {
	root := t.TempDir()
	gitDir := filepath.Join(root, ".git")
	_ = os.MkdirAll(gitDir, 0o755)
	_ = os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/dev\n"), 0o600)
	nested := filepath.Join(root, "sub", "dir")
	_ = os.MkdirAll(nested, 0o755)

	info, ok := Detect(nested)
	if !ok {
		t.Fatal("expected repo detected")
	}
	if info.Branch != "dev" {
		t.Fatalf("branch = %q", info.Branch)
	}
}

func TestDetectNonRepo(t *testing.T) {
	root := t.TempDir()
	_, ok := Detect(root)
	if ok {
		t.Fatal("expected no repo")
	}
}

func TestDetectWorktree(t *testing.T) {
	root := t.TempDir()
	realGit := filepath.Join(root, "real.git")
	_ = os.MkdirAll(realGit, 0o755)
	wtRoot := filepath.Join(root, "wt")
	_ = os.MkdirAll(wtRoot, 0o755)
	_ = os.WriteFile(filepath.Join(wtRoot, ".git"), []byte("gitdir: "+realGit+"\n"), 0o600)
	_ = os.WriteFile(filepath.Join(realGit, "HEAD"), []byte("ref: refs/heads/feature\n"), 0o600)

	info, ok := Detect(wtRoot)
	if !ok {
		t.Fatal("expected repo detected")
	}
	if info.Branch != "feature" {
		t.Fatalf("branch = %q", info.Branch)
	}
}

// A stray .git directory with no HEAD (an empty ~/GitHub/.git/info, say) is
// not a repository: git skips it, and so must Detect, or the repo map walks
// the whole parent directory.
func TestDetectSkipsStrayGitDir(t *testing.T) {
	parent := t.TempDir()
	_ = os.MkdirAll(filepath.Join(parent, ".git", "info"), 0o755)
	child := filepath.Join(parent, "project")
	_ = os.MkdirAll(child, 0o755)

	if info, ok := Detect(child); ok {
		t.Fatalf("stray .git detected as repo at %q", info.Root)
	}
	if got := OriginURL(child); got != "" {
		t.Fatalf("OriginURL = %q", got)
	}
}

func TestDetectSkipsStrayGitDirAboveRealRepo(t *testing.T) {
	outer := t.TempDir()
	_ = os.MkdirAll(filepath.Join(outer, ".git", "info"), 0o755)
	repo := filepath.Join(outer, "mid", "repo")
	_ = os.MkdirAll(filepath.Join(outer, "mid", ".git"), 0o755) // stray, no HEAD
	_ = os.MkdirAll(filepath.Join(repo, "sub"), 0o755)
	_ = os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	_ = os.WriteFile(filepath.Join(repo, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o600)

	info, ok := Detect(filepath.Join(repo, "sub"))
	if !ok || info.Root != repo {
		t.Fatalf("Detect = %+v, %v; want root %q", info, ok, repo)
	}
	if info, ok := Detect(filepath.Join(outer, "mid")); ok {
		t.Fatalf("stray .git detected as repo at %q", info.Root)
	}
}

func TestDetectSkipsGitFileWithoutGitdir(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, ".git"), []byte("not a gitfile\n"), 0o600)
	if info, ok := Detect(root); ok {
		t.Fatalf("malformed .git file detected as repo at %q", info.Root)
	}
}
