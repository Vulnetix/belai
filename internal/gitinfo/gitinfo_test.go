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
